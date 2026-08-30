package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ingestionapp "github.com/mss-boot-io/mss-knowledge/internal/app/ingestion"
	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

type fixedIDGenerator struct{}

func (fixedIDGenerator) New(prefix string) (string, error) { return prefix + "_fixed", nil }

type staticPrincipalResolver struct {
	principal searchdomain.Principal
	err       error
}

func (r staticPrincipalResolver) ResolvePrincipal(*http.Request) (searchdomain.Principal, error) {
	return r.principal, r.err
}

type fakeSearchUseCase struct {
	response searchdomain.Response
	err      error
	request  searchdomain.Request
}

func (f *fakeSearchUseCase) Search(
	_ context.Context,
	_ searchdomain.Principal,
	request searchdomain.Request,
) (searchdomain.Response, error) {
	f.request = request
	return f.response, f.err
}

type fakeIngestionUseCase struct {
	submission ingestionapp.Submission
	status     ingestionapp.JobStatus
	err        error
	request    ingestionapp.SubmitRequest
	body       []byte
}

func (f *fakeIngestionUseCase) Submit(
	_ context.Context,
	_ searchdomain.Principal,
	request ingestionapp.SubmitRequest,
) (ingestionapp.Submission, error) {
	f.request = request
	content, err := io.ReadAll(request.Body)
	if err != nil {
		return ingestionapp.Submission{}, err
	}
	f.body = content
	if f.err != nil {
		return ingestionapp.Submission{}, f.err
	}
	return f.submission, nil
}

func (f *fakeIngestionUseCase) Status(
	context.Context,
	searchdomain.Principal,
	string,
) (ingestionapp.JobStatus, error) {
	return f.status, f.err
}

func newTestServer(t *testing.T, search SearchUseCase, principals PrincipalResolver, maxBytes int64) *Server {
	t.Helper()
	server, err := New(Options{
		Build:           buildinfo.Info{Version: "test", Commit: "abc", Date: "today"},
		Search:          search,
		Principals:      principals,
		IDs:             fixedIDGenerator{},
		MaxRequestBytes: maxBytes,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}

func TestHealthAndVersion(t *testing.T) {
	server := newTestServer(t, nil, nil, 1024)

	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("X-Request-ID") != "req_fixed" {
			t.Fatalf("GET %s request ID = %q", path, recorder.Header().Get("X-Request-ID"))
		}
	}
}

func TestSearchRequiresConfiguredAuthentication(t *testing.T) {
	useCase := &fakeSearchUseCase{}
	server := newTestServer(t, useCase, nil, 1024)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"test"}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchDecodesStrictJSONAndReturnsResponse(t *testing.T) {
	useCase := &fakeSearchUseCase{response: searchdomain.Response{
		QueryID: "qry_1",
		Mode:    searchdomain.ModeBalanced,
		Results: []searchdomain.Hit{},
	}}
	server := newTestServer(t, useCase, staticPrincipalResolver{principal: searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}}, 1024)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"test","mode":"balanced","top_k":5}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if useCase.request.Query != "test" || useCase.request.TopK != 5 {
		t.Fatalf("decoded request = %+v", useCase.request)
	}
	var response searchdomain.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.QueryID != "qry_1" {
		t.Fatalf("QueryID = %q", response.QueryID)
	}
}

func TestSearchRejectsUnknownJSONField(t *testing.T) {
	server := newTestServer(t, &fakeSearchUseCase{}, staticPrincipalResolver{principal: searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}}, 1024)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"test","unexpected":true}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchRejectsOversizedBody(t *testing.T) {
	server := newTestServer(t, &fakeSearchUseCase{}, staticPrincipalResolver{principal: searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}}, 16)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"this body is too long"}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchMapsUnauthenticatedResolver(t *testing.T) {
	server := newTestServer(t, &fakeSearchUseCase{}, staticPrincipalResolver{err: ErrUnauthenticated}, 1024)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"test"}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSearchMapsApplicationPermissionError(t *testing.T) {
	useCase := &fakeSearchUseCase{err: errors.New("wrapped permission")}
	server := newTestServer(t, useCase, staticPrincipalResolver{principal: searchdomain.Principal{
		TenantID:    "tenant_1",
		PrincipalID: "principal_1",
	}}, 1024)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"query":"test"}`))
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestUploadAcceptsRawMarkdown(t *testing.T) {
	useCase := &fakeIngestionUseCase{submission: ingestionapp.Submission{
		DocumentID: "doc_1", VersionID: "ver_1", JobID: "job_1", VersionNumber: 1,
		State: "PENDING", StatusURL: "/v1/ingestion-jobs/job_1",
	}}
	server, err := New(Options{
		Ingestion: useCase,
		Principals: staticPrincipalResolver{principal: searchdomain.Principal{
			TenantID: "tenant_1", PrincipalID: "principal_1",
		}},
		IDs: fixedIDGenerator{}, MaxRequestBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/knowledge-bases/kb_1/documents", strings.NewReader("# Design\n\nRedis context."))
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	request.Header.Set("X-File-Name", "design.md")
	request.Header.Set("X-Document-Title", "Design")
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Location") != "/v1/ingestion-jobs/job_1" {
		t.Fatalf("Location = %q", recorder.Header().Get("Location"))
	}
	if useCase.request.KnowledgeBaseID != "kb_1" || useCase.request.Filename != "design.md" || string(useCase.body) != "# Design\n\nRedis context." {
		t.Fatalf("upload request = %+v, body = %q", useCase.request, useCase.body)
	}
}

func TestUploadRejectsOversizedBody(t *testing.T) {
	useCase := &fakeIngestionUseCase{}
	server, err := New(Options{
		Ingestion: useCase,
		Principals: staticPrincipalResolver{principal: searchdomain.Principal{
			TenantID: "tenant_1", PrincipalID: "principal_1",
		}},
		IDs: fixedIDGenerator{}, MaxRequestBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/knowledge-bases/kb_1/documents", strings.NewReader("this body is larger than eight bytes"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("X-File-Name", "large.txt")
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
