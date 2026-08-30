package s3store

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

const metadataSHA256 = "content-sha256"

var (
	// ErrInvalidConfig is returned before an unsafe S3 client is opened.
	ErrInvalidConfig = errors.New("invalid S3 configuration")
	// ErrInvalidObject is returned when a request violates the immutable object boundary.
	ErrInvalidObject = errors.New("invalid S3 object")
	// ErrVersioningRequired is returned when a bucket cannot preserve immutable versions.
	ErrVersioningRequired = errors.New("S3 bucket versioning is required")
	// ErrIntegrityMismatch is returned when object bytes do not match their declared SHA-256.
	ErrIntegrityMismatch = errors.New("S3 object integrity mismatch")
)

// Config controls one S3-compatible object-store boundary.
type Config struct {
	Endpoint          string
	Region            string
	Bucket            string
	AccessKeyID       string
	SecretAccessKey   string
	SessionToken      string
	PathStyle         bool
	RequireVersioning bool
	TLSMinVersion     uint16
}

// Store implements ports.ObjectStore with explicit object versions.
type Store struct {
	client            *minio.Client
	bucket            string
	requireVersioning bool
}

// Open creates a version-aware S3-compatible object store and verifies its bucket.
func Open(ctx context.Context, config Config) (*Store, error) {
	endpoint, secure, err := parseEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	config.Bucket = strings.TrimSpace(config.Bucket)
	if config.Bucket == "" {
		return nil, fmt.Errorf("%w: bucket must not be empty", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.AccessKeyID) == "" || config.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: access key and secret key are required", ErrInvalidConfig)
	}

	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("%w: create HTTP transport: %v", ErrInvalidConfig, err)
	}
	if secure {
		minimum := config.TLSMinVersion
		if minimum == 0 {
			minimum = tls.VersionTLS12
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: minimum}
		} else {
			transport.TLSClientConfig.MinVersion = minimum
		}
	}

	lookup := minio.BucketLookupAuto
	if config.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			strings.TrimSpace(config.AccessKeyID),
			config.SecretAccessKey,
			config.SessionToken,
		),
		Secure:       secure,
		Region:       strings.TrimSpace(config.Region),
		BucketLookup: lookup,
		Transport:    transport,
	})
	if err != nil {
		return nil, fmt.Errorf("open S3 client: %w", err)
	}
	store := &Store{
		client:            client,
		bucket:            config.Bucket,
		requireVersioning: config.RequireVersioning,
	}
	if err := store.Check(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Name implements the readiness-probe contract.
func (s *Store) Name() string { return "s3" }

// Bucket returns the configured immutable object boundary.
func (s *Store) Bucket() string {
	if s == nil {
		return ""
	}
	return s.bucket
}

// Check verifies bucket access and, when configured, versioning.
func (s *Store) Check(ctx context.Context) error {
	if s == nil || s.client == nil || s.bucket == "" {
		return fmt.Errorf("S3 store is not initialized")
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("check S3 bucket: bucket %q does not exist", s.bucket)
	}
	if !s.requireVersioning {
		return nil
	}
	versioning, err := s.client.GetBucketVersioning(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("read S3 bucket versioning: %w", err)
	}
	if !versioning.Enabled() {
		return fmt.Errorf("%w: bucket %q status is %q", ErrVersioningRequired, s.bucket, versioning.Status)
	}
	return nil
}

// Put writes one object version and returns its immutable reference.
func (s *Store) Put(ctx context.Context, object ports.PutObject) (ports.ObjectRef, error) {
	if err := s.validatePut(object); err != nil {
		return ports.ObjectRef{}, err
	}

	hasher := sha256.New()
	reader := io.TeeReader(object.Body, hasher)
	metadata := copyMetadata(object.Metadata)
	declaredSHA := normalizeSHA256(metadata[metadataSHA256])
	options := minio.PutObjectOptions{
		ContentType:  strings.TrimSpace(object.ContentType),
		UserMetadata: metadata,
	}
	upload, err := s.client.PutObject(ctx, s.bucket, object.Key, reader, object.Size, options)
	if err != nil {
		return ports.ObjectRef{}, fmt.Errorf("put S3 object: %w", err)
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if declaredSHA != "" && declaredSHA != actualSHA {
		_ = s.client.RemoveObject(context.WithoutCancel(ctx), s.bucket, object.Key, minio.RemoveObjectOptions{VersionID: upload.VersionID})
		return ports.ObjectRef{}, fmt.Errorf("%w: declared %s, actual %s", ErrIntegrityMismatch, declaredSHA, actualSHA)
	}
	if s.requireVersioning && strings.TrimSpace(upload.VersionID) == "" {
		return ports.ObjectRef{}, fmt.Errorf("%w: put returned no version ID", ErrVersioningRequired)
	}

	reference := ports.ObjectRef{
		Bucket:    s.bucket,
		Key:       object.Key,
		VersionID: upload.VersionID,
		ETag:      upload.ETag,
		Size:      upload.Size,
		SHA256:    actualSHA,
		MediaType: options.ContentType,
	}
	verified, err := s.Stat(ctx, reference)
	if err != nil {
		return ports.ObjectRef{}, fmt.Errorf("verify S3 object: %w", err)
	}
	if verified.Size != object.Size || verified.SHA256 != actualSHA {
		return ports.ObjectRef{}, fmt.Errorf("%w: write-after-read verification failed", ErrIntegrityMismatch)
	}
	return verified, nil
}

// Open opens one explicit object version and verifies its metadata before returning bytes.
func (s *Store) Open(ctx context.Context, reference ports.ObjectRef) (io.ReadCloser, error) {
	if err := s.validateReference(reference); err != nil {
		return nil, err
	}
	object, err := s.client.GetObject(ctx, s.bucket, reference.Key, minio.GetObjectOptions{VersionID: reference.VersionID})
	if err != nil {
		return nil, fmt.Errorf("open S3 object: %w", err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("stat opened S3 object: %w", err)
	}
	return object, nil
}

// Stat reads one explicit object version and returns normalized metadata.
func (s *Store) Stat(ctx context.Context, reference ports.ObjectRef) (ports.ObjectRef, error) {
	if err := s.validateReference(reference); err != nil {
		return ports.ObjectRef{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, reference.Key, minio.StatObjectOptions{VersionID: reference.VersionID})
	if err != nil {
		return ports.ObjectRef{}, fmt.Errorf("stat S3 object: %w", err)
	}
	sha := normalizeSHA256(info.Metadata.Get("X-Amz-Meta-" + metadataSHA256))
	if sha == "" {
		sha = normalizeSHA256(info.UserMetadata[metadataSHA256])
	}
	if reference.SHA256 != "" && sha != normalizeSHA256(reference.SHA256) {
		return ports.ObjectRef{}, fmt.Errorf("%w: stored SHA-256 does not match reference", ErrIntegrityMismatch)
	}
	if reference.Size > 0 && info.Size != reference.Size {
		return ports.ObjectRef{}, fmt.Errorf("%w: stored size does not match reference", ErrIntegrityMismatch)
	}
	versionID := info.VersionID
	if versionID == "" {
		versionID = reference.VersionID
	}
	if s.requireVersioning && versionID == "" {
		return ports.ObjectRef{}, fmt.Errorf("%w: stat returned no version ID", ErrVersioningRequired)
	}
	return ports.ObjectRef{
		Bucket:    s.bucket,
		Key:       reference.Key,
		VersionID: versionID,
		ETag:      info.ETag,
		Size:      info.Size,
		SHA256:    sha,
		MediaType: info.ContentType,
	}, nil
}

// Delete removes one explicit object version. It never creates an unscoped delete marker.
func (s *Store) Delete(ctx context.Context, reference ports.ObjectRef) error {
	if err := s.validateReference(reference); err != nil {
		return err
	}
	if reference.VersionID == "" {
		return fmt.Errorf("%w: delete requires an explicit version ID", ErrInvalidObject)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, reference.Key, minio.RemoveObjectOptions{VersionID: reference.VersionID}); err != nil {
		return fmt.Errorf("delete S3 object version: %w", err)
	}
	return nil
}

func (s *Store) validatePut(object ports.PutObject) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("S3 store is not initialized")
	}
	if object.Bucket != "" && object.Bucket != s.bucket {
		return fmt.Errorf("%w: bucket %q is outside configured boundary", ErrInvalidObject, object.Bucket)
	}
	if err := validateObjectKey(object.Key); err != nil {
		return err
	}
	if object.Body == nil || object.Size < 0 {
		return fmt.Errorf("%w: body is required and size must be non-negative", ErrInvalidObject)
	}
	if object.Size == 0 {
		return fmt.Errorf("%w: zero-byte objects are not accepted", ErrInvalidObject)
	}
	if declared := object.Metadata[metadataSHA256]; declared != "" && normalizeSHA256(declared) == "" {
		return fmt.Errorf("%w: content SHA-256 metadata is invalid", ErrInvalidObject)
	}
	return nil
}

func (s *Store) validateReference(reference ports.ObjectRef) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("S3 store is not initialized")
	}
	if reference.Bucket != "" && reference.Bucket != s.bucket {
		return fmt.Errorf("%w: bucket %q is outside configured boundary", ErrInvalidObject, reference.Bucket)
	}
	if err := validateObjectKey(reference.Key); err != nil {
		return err
	}
	if s.requireVersioning && strings.TrimSpace(reference.VersionID) == "" {
		return fmt.Errorf("%w: version ID is required", ErrInvalidObject)
	}
	return nil
}

func validateObjectKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsRune(key, '\x00') {
		return fmt.Errorf("%w: object key is invalid", ErrInvalidObject)
	}
	cleaned := path.Clean(key)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != key {
		return fmt.Errorf("%w: object key must be canonical", ErrInvalidObject)
	}
	return nil
}

func parseEndpoint(raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, fmt.Errorf("%w: endpoint must not be empty", ErrInvalidConfig)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, fmt.Errorf("%w: endpoint must contain only scheme and host", ErrInvalidConfig)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("%w: endpoint scheme must be http or https", ErrInvalidConfig)
	}
	return parsed.Host, parsed.Scheme == "https", nil
}

func copyMetadata(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || strings.TrimSpace(value) == "" {
			continue
		}
		result[key] = strings.TrimSpace(value)
	}
	return result
}

func normalizeSHA256(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

var _ ports.ObjectStore = (*Store)(nil)
var _ interface {
	Name() string
	Check(context.Context) error
} = (*Store)(nil)
