package ingestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	ingestiondomain "github.com/mss-boot-io/mss-knowledge/internal/domain/ingestion"
	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrPermissionDenied is returned when the caller cannot write the selected knowledge base.
	ErrPermissionDenied = errors.New("ingestion permission denied")
	// ErrInvalidUpload is returned when the submitted source is unsafe or incomplete.
	ErrInvalidUpload = errors.New("invalid document upload")
	// ErrUnsupportedMediaType is returned when v0.1 cannot parse the submitted format.
	ErrUnsupportedMediaType = errors.New("unsupported document media type")
	// ErrUploadTooLarge is returned before the source can exceed the configured in-memory bound.
	ErrUploadTooLarge = errors.New("document upload too large")
)

const ScopeKnowledgeWrite = "knowledge.write"

// IDGenerator creates stable opaque resource identifiers.
type IDGenerator interface {
	New(prefix string) (string, error)
}

// Config controls the bounded, immutable upload boundary.
type Config struct {
	Bucket             string
	BucketPrefix       string
	MaxBytes           int64
	Profiles           ports.ProcessingProfileIDs
	EmbeddingModel     string
	EmbeddingDimension int
}

// SubmitRequest carries one raw TXT or Markdown source.
type SubmitRequest struct {
	KnowledgeBaseID string
	Filename        string
	Title           string
	ExternalKey     string
	MediaType       string
	Body            io.Reader
}

// Submission is returned once S3 and PostgreSQL have durably accepted the source.
type Submission struct {
	DocumentID    string `json:"document_id"`
	VersionID     string `json:"version_id"`
	JobID         string `json:"job_id"`
	VersionNumber int64  `json:"version_number"`
	State         string `json:"state"`
	StatusURL     string `json:"status_url"`
}

// JobStatus is the public tenant-scoped ingestion state.
type JobStatus struct {
	ID              string     `json:"id"`
	KnowledgeBaseID string     `json:"knowledge_base_id"`
	DocumentID      string     `json:"document_id"`
	VersionID       string     `json:"version_id"`
	Kind            string     `json:"kind"`
	State           string     `json:"state"`
	CurrentStage    string     `json:"current_stage"`
	Attempt         int        `json:"attempt"`
	MaxAttempts     int        `json:"max_attempts"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// Service coordinates authorization, immutable S3 storage, and PostgreSQL scheduling.
type Service struct {
	authorizer     ports.KnowledgeBaseWriteAuthorizer
	readAuthorizer ports.SearchAuthorizer
	objects        ports.ObjectStore
	uploads        ports.UploadRepository
	jobs           ports.IngestionReader
	ids            IDGenerator
	config         Config
}

// New creates an ingestion application service.
func New(
	authorizer ports.KnowledgeBaseWriteAuthorizer,
	readAuthorizer ports.SearchAuthorizer,
	objects ports.ObjectStore,
	uploads ports.UploadRepository,
	jobs ports.IngestionReader,
	ids IDGenerator,
	config Config,
) (*Service, error) {
	if authorizer == nil || readAuthorizer == nil || objects == nil || uploads == nil || jobs == nil || ids == nil {
		return nil, fmt.Errorf("ingestion dependencies must not be nil")
	}
	config.Bucket = strings.TrimSpace(config.Bucket)
	if config.Bucket == "" {
		return nil, fmt.Errorf("ingestion bucket must not be empty")
	}
	config.BucketPrefix = strings.Trim(strings.TrimSpace(config.BucketPrefix), "/")
	if config.BucketPrefix == "" {
		config.BucketPrefix = "tenants"
	}
	if config.MaxBytes <= 0 {
		return nil, fmt.Errorf("ingestion max bytes must be positive")
	}
	if config.EmbeddingDimension < 16 || config.EmbeddingDimension > 4096 {
		return nil, fmt.Errorf("ingestion embedding dimension is invalid")
	}
	for _, profileID := range []string{
		config.Profiles.Parser,
		config.Profiles.Chunker,
		config.Profiles.Embedding,
		config.Profiles.Index,
	} {
		if strings.TrimSpace(profileID) == "" {
			return nil, fmt.Errorf("ingestion profile IDs must not be empty")
		}
	}
	return &Service{
		authorizer:     authorizer,
		readAuthorizer: readAuthorizer,
		objects:        objects,
		uploads:        uploads,
		jobs:           jobs,
		ids:            ids,
		config:         config,
	}, nil
}

// Submit accepts one bounded document and schedules asynchronous ingestion.
func (s *Service) Submit(
	ctx context.Context,
	principal searchdomain.Principal,
	request SubmitRequest,
) (Submission, error) {
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.PrincipalID) == "" ||
		!principal.HasScope(ScopeKnowledgeWrite) {
		return Submission{}, ErrPermissionDenied
	}
	request.KnowledgeBaseID = strings.TrimSpace(request.KnowledgeBaseID)
	request.Filename = sanitizeFilename(request.Filename)
	request.Title = strings.TrimSpace(request.Title)
	request.ExternalKey = strings.TrimSpace(request.ExternalKey)
	request.MediaType = normalizeMediaType(request.MediaType)
	if request.KnowledgeBaseID == "" || request.Filename == "" || request.Body == nil {
		return Submission{}, fmt.Errorf("%w: knowledge base, filename, and body are required", ErrInvalidUpload)
	}
	if !supportedMediaType(request.MediaType, request.Filename) {
		return Submission{}, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, request.MediaType)
	}

	allowed, err := s.authorizer.CanWriteKnowledgeBase(
		ctx,
		principal.TenantID,
		principal.PrincipalID,
		request.KnowledgeBaseID,
	)
	if err != nil {
		return Submission{}, fmt.Errorf("authorize document upload: %w", err)
	}
	if !allowed {
		return Submission{}, ErrPermissionDenied
	}

	content, err := readBounded(ctx, request.Body, s.config.MaxBytes)
	if err != nil {
		return Submission{}, err
	}
	digest := sha256.Sum256(content)
	contentSHA := hex.EncodeToString(digest[:])

	documentID, err := s.ids.New("doc")
	if err != nil {
		return Submission{}, fmt.Errorf("create document ID: %w", err)
	}
	versionID, err := s.ids.New("ver")
	if err != nil {
		return Submission{}, fmt.Errorf("create version ID: %w", err)
	}
	jobID, err := s.ids.New("job")
	if err != nil {
		return Submission{}, fmt.Errorf("create job ID: %w", err)
	}
	if request.Title == "" {
		request.Title = titleFromFilename(request.Filename)
	}
	if request.ExternalKey == "" {
		request.ExternalKey = documentID
	}

	objectKey := path.Join(
		s.config.BucketPrefix,
		principal.TenantID,
		"knowledge-bases",
		request.KnowledgeBaseID,
		"documents",
		documentID,
		"versions",
		versionID,
		"raw",
		request.Filename,
	)
	original, err := s.objects.Put(ctx, ports.PutObject{
		Bucket:      s.config.Bucket,
		Key:         objectKey,
		Body:        bytes.NewReader(content),
		Size:        int64(len(content)),
		ContentType: request.MediaType,
		Metadata: map[string]string{
			"content-sha256": contentSHA,
			"document-id":    documentID,
			"version-id":     versionID,
		},
	})
	if err != nil {
		return Submission{}, fmt.Errorf("store source object: %w", err)
	}

	now := time.Now().UTC()
	pipelineFingerprint := s.pipelineFingerprint(contentSHA)
	sourceURI := "s3://" + original.Bucket + "/" + original.Key + "?versionId=" + url.QueryEscape(original.VersionID)
	created, err := s.uploads.CreateUpload(ctx, ports.CreateUploadRequest{
		TenantID:            principal.TenantID,
		PrincipalID:         principal.PrincipalID,
		KnowledgeBaseID:     request.KnowledgeBaseID,
		DocumentID:          documentID,
		VersionID:           versionID,
		JobID:               jobID,
		ExternalKey:         request.ExternalKey,
		Title:               request.Title,
		Filename:            request.Filename,
		MediaType:           request.MediaType,
		SourceURI:           sourceURI,
		Original:            original,
		Profiles:            s.config.Profiles,
		PipelineFingerprint: pipelineFingerprint,
		CreatedAt:           now,
	})
	if err != nil {
		_ = s.objects.Delete(context.WithoutCancel(ctx), original)
		if errors.Is(err, ports.ErrPermissionDenied) {
			return Submission{}, ErrPermissionDenied
		}
		return Submission{}, fmt.Errorf("create ingestion control-plane records: %w", err)
	}
	return Submission{
		DocumentID:    created.DocumentID,
		VersionID:     created.VersionID,
		JobID:         created.JobID,
		VersionNumber: created.VersionNumber,
		State:         string(ingestiondomain.StatePending),
		StatusURL:     "/v1/ingestion-jobs/" + created.JobID,
	}, nil
}

// Status returns the caller's tenant-scoped ingestion state.
func (s *Service) Status(
	ctx context.Context,
	principal searchdomain.Principal,
	jobID string,
) (JobStatus, error) {
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.PrincipalID) == "" ||
		(!principal.HasScope("knowledge.read") && !principal.HasScope("knowledge.write")) {
		return JobStatus{}, ErrPermissionDenied
	}
	job, err := s.jobs.GetJob(ctx, principal.TenantID, ingestiondomain.JobID(strings.TrimSpace(jobID)))
	if err != nil {
		return JobStatus{}, err
	}
	allowed := false
	if principal.HasScope(ScopeKnowledgeWrite) {
		allowed, err = s.authorizer.CanWriteKnowledgeBase(
			ctx,
			principal.TenantID,
			principal.PrincipalID,
			job.KnowledgeBaseID,
		)
		if err != nil {
			return JobStatus{}, fmt.Errorf("authorize ingestion status write access: %w", err)
		}
	}
	if !allowed && principal.HasScope("knowledge.read") {
		knowledgeBaseIDs, readErr := s.readAuthorizer.AllowedKnowledgeBases(
			ctx,
			principal,
			[]string{job.KnowledgeBaseID},
		)
		if readErr != nil {
			return JobStatus{}, fmt.Errorf("authorize ingestion status read access: %w", readErr)
		}
		allowed = len(knowledgeBaseIDs) == 1 && knowledgeBaseIDs[0] == job.KnowledgeBaseID
	}
	if !allowed {
		return JobStatus{}, ErrPermissionDenied
	}
	return JobStatus{
		ID:              string(job.ID),
		KnowledgeBaseID: job.KnowledgeBaseID,
		DocumentID:      job.DocumentID,
		VersionID:       job.VersionID,
		Kind:            job.Kind,
		State:           string(job.State),
		CurrentStage:    string(job.Stage),
		Attempt:         job.Attempt,
		MaxAttempts:     job.MaxAttempts,
		ErrorCode:       job.ErrorCode,
		ErrorMessage:    job.ErrorMessage,
		CreatedAt:       job.CreatedAt.UTC(),
		UpdatedAt:       job.UpdatedAt.UTC(),
		CompletedAt:     job.CompletedAt,
	}, nil
}

func (s *Service) pipelineFingerprint(contentSHA string) string {
	material := strings.Join([]string{
		contentSHA,
		s.config.Profiles.Parser,
		s.config.Profiles.Chunker,
		s.config.Profiles.Embedding,
		s.config.Profiles.Index,
		strings.TrimSpace(s.config.EmbeddingModel),
		fmt.Sprintf("dimension=%d", s.config.EmbeddingDimension),
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func readBounded(ctx context.Context, reader io.Reader, maximum int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read document upload: %w", err)
	}
	if int64(len(content)) > maximum {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrUploadTooLarge, maximum)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("%w: document is empty", ErrInvalidUpload)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return content, nil
}

func normalizeMediaType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return value
	}
	return strings.ToLower(parsed)
}

func supportedMediaType(mediaType, filename string) bool {
	extension := strings.ToLower(path.Ext(filename))
	switch mediaType {
	case "text/markdown", "text/x-markdown":
		return extension == ".md" || extension == ".markdown"
	case "text/plain":
		return extension == ".txt" || extension == ".text" || extension == ".md" || extension == ".markdown"
	default:
		return false
	}
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = path.Base(value)
	if value == "." || value == "/" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			builder.WriteRune('_')
			continue
		}
		builder.WriteRune(r)
		if builder.Len() >= 200 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func titleFromFilename(filename string) string {
	base := strings.TrimSuffix(filename, path.Ext(filename))
	base = strings.TrimSpace(strings.ReplaceAll(base, "_", " "))
	if base == "" {
		return filename
	}
	return base
}
