package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	catalogapp "github.com/mss-boot-io/mss-knowledge/internal/app/catalog"
	fetchapp "github.com/mss-boot-io/mss-knowledge/internal/app/fetch"
	ingestionapp "github.com/mss-boot-io/mss-knowledge/internal/app/ingestion"
	searchapp "github.com/mss-boot-io/mss-knowledge/internal/app/search"
	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	catalogdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/catalog"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrUnauthenticated is returned when a request does not resolve to an authenticated principal.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrInvalidServerOptions is returned when required server dependencies are missing.
	ErrInvalidServerOptions = errors.New("invalid HTTP server options")
)

// SearchUseCase is implemented by the search application service.
type SearchUseCase interface {
	Search(
		ctx context.Context,
		principal searchdomain.Principal,
		request searchdomain.Request,
	) (searchdomain.Response, error)
}

// FetchUseCase retrieves one active, authorized chunk.
type FetchUseCase interface {
	Fetch(ctx context.Context, principal searchdomain.Principal, chunkID string) (searchdomain.Hit, error)
}

// CatalogUseCase lists authorization-filtered knowledge bases.
type CatalogUseCase interface {
	List(ctx context.Context, principal searchdomain.Principal) ([]catalogdomain.KnowledgeBase, error)
}

// IngestionUseCase accepts documents and reports asynchronous job status.
type IngestionUseCase interface {
	Submit(ctx context.Context, principal searchdomain.Principal, request ingestionapp.SubmitRequest) (ingestionapp.Submission, error)
	Status(ctx context.Context, principal searchdomain.Principal, jobID string) (ingestionapp.JobStatus, error)
}

// PrincipalResolver converts an authenticated HTTP request into an internal principal.
type PrincipalResolver interface {
	ResolvePrincipal(request *http.Request) (searchdomain.Principal, error)
}

// ReadinessProbe reports whether one dependency is ready to serve traffic.
type ReadinessProbe interface {
	Name() string
	Check(ctx context.Context) error
}

// RequestIDGenerator creates opaque request identifiers.
type RequestIDGenerator interface {
	New(prefix string) (string, error)
}

// Options configures the HTTP transport.
type Options struct {
	Logger          *slog.Logger
	Build           buildinfo.Info
	Search          SearchUseCase
	Fetch           FetchUseCase
	Catalog         CatalogUseCase
	Ingestion       IngestionUseCase
	Principals      PrincipalResolver
	MCP             http.Handler
	Readiness       []ReadinessProbe
	IDs             RequestIDGenerator
	MaxRequestBytes int64
}

// Server exposes the HTTP handler tree.
type Server struct {
	handler         http.Handler
	logger          *slog.Logger
	build           buildinfo.Info
	search          SearchUseCase
	fetch           FetchUseCase
	catalog         CatalogUseCase
	ingestion       IngestionUseCase
	principals      PrincipalResolver
	mcp             http.Handler
	readiness       []ReadinessProbe
	ids             RequestIDGenerator
	maxRequestBytes int64
}

// New builds an HTTP server without opening a network listener.
func New(options Options) (*Server, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.IDs == nil {
		return nil, fmt.Errorf("%w: ID generator is required", ErrInvalidServerOptions)
	}
	if options.MaxRequestBytes <= 0 {
		return nil, fmt.Errorf("%w: max request bytes must be positive", ErrInvalidServerOptions)
	}

	server := &Server{
		logger:          options.Logger,
		build:           options.Build,
		search:          options.Search,
		fetch:           options.Fetch,
		catalog:         options.Catalog,
		ingestion:       options.Ingestion,
		principals:      options.Principals,
		mcp:             options.MCP,
		readiness:       append([]ReadinessProbe(nil), options.Readiness...),
		ids:             options.IDs,
		maxRequestBytes: options.MaxRequestBytes,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReadiness)
	mux.HandleFunc("GET /version", server.handleVersion)
	mux.HandleFunc("POST /v1/search", server.handleSearch)
	mux.HandleFunc("GET /v1/chunks/{chunk_id}", server.handleFetch)
	mux.HandleFunc("GET /v1/knowledge-bases", server.handleCatalog)
	mux.HandleFunc("POST /v1/knowledge-bases/{kb_id}/documents", server.handleUpload)
	mux.HandleFunc("GET /v1/ingestion-jobs/{job_id}", server.handleIngestionStatus)
	if server.mcp != nil {
		mux.Handle("/mcp", server.mcp)
	}
	server.handler = server.recoverPanic(server.accessLog(server.requestID(mux)))
	return server, nil
}

// Handler returns the composed HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "up",
		"service": "mss-knowledge",
	})
}

func (s *Server) handleReadiness(writer http.ResponseWriter, request *http.Request) {
	checks := make(map[string]string, len(s.readiness))
	ready := true
	for _, probe := range s.readiness {
		if probe == nil {
			continue
		}
		if err := probe.Check(request.Context()); err != nil {
			ready = false
			checks[probe.Name()] = "not_ready"
			s.logger.WarnContext(request.Context(), "readiness probe failed",
				"probe", probe.Name(),
				"error", err,
			)
			continue
		}
		checks[probe.Name()] = "ready"
	}

	statusCode := http.StatusOK
	status := "ready"
	if !ready {
		statusCode = http.StatusServiceUnavailable
		status = "not_ready"
	}
	writeJSON(writer, statusCode, map[string]any{
		"status": status,
		"checks": checks,
	})
}

func (s *Server) handleVersion(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.build)
}

func (s *Server) handleSearch(writer http.ResponseWriter, request *http.Request) {
	if s.search == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Search is not configured.", requestIDFrom(request.Context()), true)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, s.maxRequestBytes)
	defer request.Body.Close()

	var input searchdomain.Request
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeDecodeError(writer, request, err)
		return
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		s.writeDecodeError(writer, request, err)
		return
	}

	principal, ok := s.resolvePrincipal(writer, request)
	if !ok {
		return
	}

	response, err := s.search.Search(request.Context(), principal, input)
	if err != nil {
		s.writeSearchError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) handleFetch(writer http.ResponseWriter, request *http.Request) {
	if s.fetch == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Fetch is not configured.", requestIDFrom(request.Context()), true)
		return
	}
	principal, ok := s.resolvePrincipal(writer, request)
	if !ok {
		return
	}
	hit, err := s.fetch.Fetch(request.Context(), principal, request.PathValue("chunk_id"))
	if err != nil {
		s.writeFetchError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, hit)
}

func (s *Server) handleCatalog(writer http.ResponseWriter, request *http.Request) {
	if s.catalog == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Catalog is not configured.", requestIDFrom(request.Context()), true)
		return
	}
	principal, ok := s.resolvePrincipal(writer, request)
	if !ok {
		return
	}
	items, err := s.catalog.List(request.Context(), principal)
	if err != nil {
		if errors.Is(err, catalogapp.ErrPermissionDenied) {
			writeAPIError(writer, http.StatusForbidden, "PERMISSION_DENIED", "The requested operation is not permitted.", requestIDFrom(request.Context()), false)
			return
		}
		s.logger.ErrorContext(request.Context(), "list knowledge bases", "error", err)
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The knowledge-base catalog could not be read.", requestIDFrom(request.Context()), true)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"knowledge_bases": items})
}

func (s *Server) handleUpload(writer http.ResponseWriter, request *http.Request) {
	if s.ingestion == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Ingestion is not configured.", requestIDFrom(request.Context()), true)
		return
	}
	principal, ok := s.resolvePrincipal(writer, request)
	if !ok {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, s.maxRequestBytes)
	defer request.Body.Close()

	result, err := s.ingestion.Submit(request.Context(), principal, ingestionapp.SubmitRequest{
		KnowledgeBaseID: request.PathValue("kb_id"),
		Filename:        request.Header.Get("X-File-Name"),
		Title:           request.Header.Get("X-Document-Title"),
		ExternalKey:     request.Header.Get("X-External-Key"),
		MediaType:       request.Header.Get("Content-Type"),
		Body:            request.Body,
	})
	if err != nil {
		s.writeIngestionError(writer, request, err)
		return
	}
	writer.Header().Set("Location", result.StatusURL)
	writeJSON(writer, http.StatusAccepted, result)
}

func (s *Server) handleIngestionStatus(writer http.ResponseWriter, request *http.Request) {
	if s.ingestion == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Ingestion is not configured.", requestIDFrom(request.Context()), true)
		return
	}
	principal, ok := s.resolvePrincipal(writer, request)
	if !ok {
		return
	}
	result, err := s.ingestion.Status(request.Context(), principal, request.PathValue("job_id"))
	if err != nil {
		s.writeIngestionError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) writeIngestionError(writer http.ResponseWriter, request *http.Request, err error) {
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesError):
		writeAPIError(writer, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "The document exceeds the configured upload limit.", requestIDFrom(request.Context()), false)
	case errors.Is(err, ingestionapp.ErrPermissionDenied):
		writeAPIError(writer, http.StatusForbidden, "PERMISSION_DENIED", "The requested operation is not permitted.", requestIDFrom(request.Context()), false)
	case errors.Is(err, ingestionapp.ErrUnsupportedMediaType):
		writeAPIError(writer, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Only UTF-8 TXT and Markdown documents are supported by v0.1.", requestIDFrom(request.Context()), false)
	case errors.Is(err, ingestionapp.ErrUploadTooLarge):
		writeAPIError(writer, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "The document exceeds the configured upload limit.", requestIDFrom(request.Context()), false)
	case errors.Is(err, ingestionapp.ErrInvalidUpload):
		writeAPIError(writer, http.StatusBadRequest, "INVALID_ARGUMENT", "The document upload is invalid.", requestIDFrom(request.Context()), false)
	case errors.Is(err, ports.ErrNotFound):
		writeAPIError(writer, http.StatusNotFound, "NOT_FOUND", "The requested ingestion resource was not found.", requestIDFrom(request.Context()), false)
	case errors.Is(err, context.DeadlineExceeded):
		writeAPIError(writer, http.StatusGatewayTimeout, "DEPENDENCY_UNAVAILABLE", "The ingestion deadline was exceeded.", requestIDFrom(request.Context()), true)
	case errors.Is(err, context.Canceled):
		writeAPIError(writer, http.StatusRequestTimeout, "REQUEST_CANCELLED", "The request was cancelled.", requestIDFrom(request.Context()), true)
	default:
		s.logger.ErrorContext(request.Context(), "ingestion request failed", "error", err)
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The ingestion request failed.", requestIDFrom(request.Context()), true)
	}
}

func (s *Server) resolvePrincipal(writer http.ResponseWriter, request *http.Request) (searchdomain.Principal, bool) {
	if s.principals == nil {
		writeAPIError(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication is required.", requestIDFrom(request.Context()), false)
		return searchdomain.Principal{}, false
	}
	principal, err := s.principals.ResolvePrincipal(request)
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="mss-knowledge"`)
			writeAPIError(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication is required.", requestIDFrom(request.Context()), false)
			return searchdomain.Principal{}, false
		}
		s.logger.ErrorContext(request.Context(), "resolve principal", "error", err)
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The request could not be authenticated.", requestIDFrom(request.Context()), false)
		return searchdomain.Principal{}, false
	}
	return principal, true
}

func (s *Server) writeFetchError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		writeAPIError(writer, http.StatusNotFound, "NOT_FOUND", "The requested chunk was not found.", requestIDFrom(request.Context()), false)
	case errors.Is(err, fetchapp.ErrPermissionDenied):
		writeAPIError(writer, http.StatusForbidden, "PERMISSION_DENIED", "The requested operation is not permitted.", requestIDFrom(request.Context()), false)
	case errors.Is(err, context.DeadlineExceeded):
		writeAPIError(writer, http.StatusGatewayTimeout, "DEPENDENCY_UNAVAILABLE", "The fetch deadline was exceeded.", requestIDFrom(request.Context()), true)
	case errors.Is(err, context.Canceled):
		writeAPIError(writer, http.StatusRequestTimeout, "REQUEST_CANCELLED", "The request was cancelled.", requestIDFrom(request.Context()), true)
	default:
		s.logger.ErrorContext(request.Context(), "fetch request failed", "error", err)
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The fetch request failed.", requestIDFrom(request.Context()), true)
	}
}

func (s *Server) writeDecodeError(writer http.ResponseWriter, request *http.Request, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeAPIError(writer, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "The request body is too large.", requestIDFrom(request.Context()), false)
		return
	}
	writeAPIError(writer, http.StatusBadRequest, "INVALID_ARGUMENT", "The request body must be one valid JSON object.", requestIDFrom(request.Context()), false)
}

func (s *Server) writeSearchError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, searchdomain.ErrInvalidRequest):
		writeAPIError(writer, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), requestIDFrom(request.Context()), false)
	case errors.Is(err, searchapp.ErrPermissionDenied):
		writeAPIError(writer, http.StatusForbidden, "PERMISSION_DENIED", "The requested operation is not permitted.", requestIDFrom(request.Context()), false)
	case errors.Is(err, context.DeadlineExceeded):
		writeAPIError(writer, http.StatusGatewayTimeout, "DEPENDENCY_UNAVAILABLE", "The search deadline was exceeded.", requestIDFrom(request.Context()), true)
	case errors.Is(err, context.Canceled):
		writeAPIError(writer, http.StatusRequestTimeout, "REQUEST_CANCELLED", "The request was cancelled.", requestIDFrom(request.Context()), true)
	default:
		s.logger.ErrorContext(request.Context(), "search request failed", "error", err)
		writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The search request failed.", requestIDFrom(request.Context()), true)
	}
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			generated, err := s.ids.New("req")
			if err != nil {
				s.logger.ErrorContext(request.Context(), "create request ID", "error", err)
				http.Error(writer, "internal server error", http.StatusInternalServerError)
				return
			}
			requestID = generated
		}
		writer.Header().Set("X-Request-ID", requestID)
		request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID))
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer, statusCode: http.StatusOK}
		next.ServeHTTP(recorder, request)
		s.logger.InfoContext(request.Context(), "HTTP request",
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode,
			"duration", time.Since(startedAt),
			"request_id", requestIDFrom(request.Context()),
		)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.ErrorContext(request.Context(), "HTTP handler panic",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				writeAPIError(writer, http.StatusInternalServerError, "INTERNAL", "The request failed.", requestIDFrom(request.Context()), true)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

type requestIDKey struct{}

func requestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Retryable bool   `json:"retryable"`
}

func writeAPIError(writer http.ResponseWriter, statusCode int, code, message, requestID string, retryable bool) {
	writeJSON(writer, statusCode, errorEnvelope{Error: apiError{
		Code:      code,
		Message:   message,
		RequestID: requestID,
		Retryable: retryable,
	}})
}

func writeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(value)
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}
