package redissearch

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

const rrfConstant = 60.0

var (
	// ErrInvalidConfig is returned before opening an unsafe Redis projection.
	ErrInvalidConfig = errors.New("invalid Redis search configuration")
	// ErrInvalidProjection is returned when chunk metadata cannot be indexed safely.
	ErrInvalidProjection = errors.New("invalid Redis chunk projection")
)

// Config controls one Redis Query Engine chunk index.
type Config struct {
	Address          string
	Username         string
	Password         string
	Database         int
	TLS              bool
	IndexName        string
	KeyPrefix        string
	EmbeddingProfile ports.EmbeddingProfile
}

// Store implements the search, fetch, readiness, and chunk-index ports.
type Store struct {
	client   *redis.Client
	config   Config
	embedder ports.EmbeddingProvider
}

// Open connects to Redis and ensures the versioned chunk index exists.
func Open(ctx context.Context, config Config, embedder ports.EmbeddingProvider) (*Store, error) {
	config.Address = strings.TrimSpace(config.Address)
	config.IndexName = strings.TrimSpace(config.IndexName)
	config.KeyPrefix = strings.TrimSpace(config.KeyPrefix)
	if config.Address == "" || config.IndexName == "" || config.KeyPrefix == "" || embedder == nil {
		return nil, fmt.Errorf("%w: address, index name, key prefix, and embedder are required", ErrInvalidConfig)
	}
	if config.Database < 0 || config.EmbeddingProfile.Dimension < 16 || config.EmbeddingProfile.Dimension > 4096 {
		return nil, fmt.Errorf("%w: database or embedding dimension is invalid", ErrInvalidConfig)
	}
	options := &redis.Options{
		Addr:     config.Address,
		Username: config.Username,
		Password: config.Password,
		DB:       config.Database,
		Protocol: 2,
	}
	if config.TLS {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(options)
	store := &Store{client: client, config: config, embedder: embedder}
	if err := store.Check(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := store.ensureIndex(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return store, nil
}

// Close releases Redis connections.
func (s *Store) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Name implements the HTTP readiness-probe contract.
func (s *Store) Name() string { return "redis-search" }

// Check verifies Redis connectivity.
func (s *Store) Check(ctx context.Context) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("Redis search store is not initialized")
	}
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping Redis: %w", err)
	}
	return nil
}

func (s *Store) ensureIndex(ctx context.Context) error {
	if _, err := s.client.Do(ctx, "FT.INFO", s.config.IndexName).Result(); err == nil {
		return nil
	} else if !isUnknownIndex(err) {
		return fmt.Errorf("inspect Redis search index: %w", err)
	}

	args := []any{
		"FT.CREATE", s.config.IndexName,
		"ON", "HASH",
		"PREFIX", 1, s.config.KeyPrefix,
		"LANGUAGE_FIELD", "language",
		"SCHEMA",
		"tenant_id", "TAG",
		"kb_id", "TAG",
		"document_id", "TAG",
		"version_id", "TAG",
		"content_type", "TAG",
		"language", "TAG",
		"title", "TEXT", "WEIGHT", 4,
		"heading_path", "TEXT", "WEIGHT", 3,
		"body", "TEXT", "WEIGHT", 1,
		"source_uri", "TEXT", "NOSTEM",
		"ordinal", "NUMERIC", "SORTABLE",
		"page_start", "NUMERIC",
		"page_end", "NUMERIC",
		"updated_at", "NUMERIC", "SORTABLE",
		"embedding", "VECTOR", "HNSW", 6,
		"TYPE", "FLOAT32",
		"DIM", s.config.EmbeddingProfile.Dimension,
		"DISTANCE_METRIC", "COSINE",
	}
	if err := s.client.Do(ctx, args...).Err(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "index already exists") {
		return fmt.Errorf("create Redis search index: %w", err)
	}
	return nil
}

// IndexBatch writes a version-scoped, rebuildable chunk projection.
func (s *Store) IndexBatch(ctx context.Context, chunks []ports.IndexedChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	pipe := s.client.Pipeline()
	for index, item := range chunks {
		if err := validateIndexedChunk(item, s.config.EmbeddingProfile.Dimension); err != nil {
			return fmt.Errorf("%w: chunk %d: %v", ErrInvalidProjection, index, err)
		}
		headingPath, err := json.Marshal(item.Chunk.HeadingPath)
		if err != nil {
			return fmt.Errorf("marshal heading path: %w", err)
		}
		fields := map[string]any{
			"tenant_id":         item.TenantID,
			"kb_id":             item.KnowledgeBaseID,
			"document_id":       item.DocumentID,
			"version_id":        item.VersionID,
			"parent_chunk_id":   item.Chunk.ParentChunkID,
			"ordinal":           item.Chunk.Ordinal,
			"content_type":      string(item.Chunk.ContentType),
			"language":          normalizeLanguage(item.Language),
			"title":             item.Title,
			"heading_path":      strings.Join(item.Chunk.HeadingPath, " / "),
			"heading_path_json": string(headingPath),
			"body":              item.Chunk.Text,
			"source_uri":        item.SourceURI,
			"content_sha256":    item.Chunk.ContentSHA256,
			"updated_at":        item.UpdatedAt.UTC().UnixMilli(),
			"embedding":         encodeVector(item.Embedding),
		}
		if item.Chunk.PageStart != nil {
			fields["page_start"] = *item.Chunk.PageStart
		}
		if item.Chunk.PageEnd != nil {
			fields["page_end"] = *item.Chunk.PageEnd
		}
		pipe.HSet(ctx, s.chunkKey(item.TenantID, item.Chunk.ID), fields)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("write Redis chunk projection: %w", err)
	}
	return nil
}

// CountVersion returns the number of projected chunks for one version.
func (s *Store) CountVersion(ctx context.Context, tenantID, versionID, _ string) (int64, error) {
	query := fmt.Sprintf("@tenant_id:{%s} @version_id:{%s}", escapeTag(tenantID), escapeTag(versionID))
	result, err := s.client.Do(ctx, "FT.SEARCH", s.config.IndexName, query, "LIMIT", 0, 0, "DIALECT", 2).Result()
	if err != nil {
		return 0, fmt.Errorf("count Redis version projection: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) == 0 {
		return 0, fmt.Errorf("count Redis version projection: unexpected response %T", result)
	}
	return asInt64(values[0])
}

// DeleteVersion removes all projections for a tenant-scoped document version.
func (s *Store) DeleteVersion(ctx context.Context, tenantID, versionID, _ string) error {
	query := fmt.Sprintf("@tenant_id:{%s} @version_id:{%s}", escapeTag(tenantID), escapeTag(versionID))
	result, err := s.client.Do(ctx, "FT.SEARCH", s.config.IndexName, query, "NOCONTENT", "LIMIT", 0, 10000, "DIALECT", 2).Result()
	if err != nil {
		return fmt.Errorf("list Redis version projection: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) == 0 {
		return fmt.Errorf("list Redis version projection: unexpected response %T", result)
	}
	if len(values) == 1 {
		return nil
	}
	keys := make([]string, 0, len(values)-1)
	for _, value := range values[1:] {
		keys = append(keys, asString(value))
	}
	if len(keys) > 0 {
		if err := s.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("delete Redis version projection: %w", err)
		}
	}
	return nil
}

// Search performs bounded lexical/vector retrieval and fuses rankings using RRF.
func (s *Store) Search(ctx context.Context, request searchdomain.StoreRequest) ([]searchdomain.Hit, error) {
	if err := validateSearchRequest(request); err != nil {
		return nil, err
	}
	filter := buildFilter(request)
	lexical, err := s.lexicalSearch(ctx, filter, request.Query, request.CandidateLimit)
	if err != nil {
		return nil, err
	}
	if request.Mode == searchdomain.ModeExact {
		return lexical, nil
	}
	vector, err := s.embedder.EmbedQuery(ctx, request.Query, s.config.EmbeddingProfile)
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	vectorHits, err := s.vectorSearch(ctx, filter, vector, request.CandidateLimit)
	if err != nil {
		return nil, err
	}
	return fuseRRF(lexical, vectorHits, request.CandidateLimit, request.IncludeDiagnostics), nil
}

func (s *Store) lexicalSearch(ctx context.Context, filter, query string, limit int) ([]searchdomain.Hit, error) {
	text := escapeTextQuery(query)
	searchQuery := filter + " @title|heading_path|body:(" + text + ")"
	fields := returnFields(false)
	args := []any{"FT.SEARCH", s.config.IndexName, searchQuery, "WITHSCORES", "SCORER", "BM25STD", "RETURN", len(fields)}
	for _, field := range fields {
		args = append(args, field)
	}
	args = append(args, "LIMIT", 0, limit, "DIALECT", 2)
	result, err := s.client.Do(ctx, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis lexical search: %w", err)
	}
	return parseSearchResult(result, true, false)
}

func (s *Store) vectorSearch(ctx context.Context, filter string, vector []float32, limit int) ([]searchdomain.Hit, error) {
	if len(vector) != s.config.EmbeddingProfile.Dimension {
		return nil, fmt.Errorf("query embedding dimension %d does not match index dimension %d", len(vector), s.config.EmbeddingProfile.Dimension)
	}
	searchQuery := "(" + filter + ")=>[KNN " + strconv.Itoa(limit) + " @embedding $vector AS vector_distance]"
	fields := returnFields(true)
	args := []any{"FT.SEARCH", s.config.IndexName, searchQuery, "PARAMS", 2, "vector", encodeVector(vector), "SORTBY", "vector_distance", "ASC", "RETURN", len(fields)}
	for _, field := range fields {
		args = append(args, field)
	}
	args = append(args, "LIMIT", 0, limit, "DIALECT", 2)
	result, err := s.client.Do(ctx, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis vector search: %w", err)
	}
	return parseSearchResult(result, false, true)
}

// GetChunk returns one tenant-scoped chunk projection.
func (s *Store) GetChunk(ctx context.Context, tenantID, chunkID string) (searchdomain.Hit, error) {
	tenantID = strings.TrimSpace(tenantID)
	chunkID = strings.TrimSpace(chunkID)
	if tenantID == "" || chunkID == "" {
		return searchdomain.Hit{}, fmt.Errorf("tenant and chunk IDs must not be empty")
	}
	values, err := s.client.HMGet(ctx, s.chunkKey(tenantID, chunkID), returnFields(false)...).Result()
	if err != nil {
		return searchdomain.Hit{}, fmt.Errorf("read Redis chunk projection: %w", err)
	}
	found := false
	fields := make(map[string]string, len(values))
	for index, value := range values {
		if value == nil {
			continue
		}
		found = true
		fields[returnFields(false)[index]] = asString(value)
	}
	if !found {
		return searchdomain.Hit{}, ports.ErrNotFound
	}
	hit, err := fieldsToHit(chunkID, fields, false)
	if err != nil {
		return searchdomain.Hit{}, err
	}
	return hit, nil
}

func (s *Store) chunkKey(tenantID, chunkID string) string {
	return s.config.KeyPrefix + tenantID + ":" + chunkID
}

func validateIndexedChunk(item ports.IndexedChunk, dimension int) error {
	if strings.TrimSpace(item.TenantID) == "" || strings.TrimSpace(item.KnowledgeBaseID) == "" ||
		strings.TrimSpace(item.DocumentID) == "" || strings.TrimSpace(item.VersionID) == "" ||
		strings.TrimSpace(item.Chunk.ID) == "" || strings.TrimSpace(item.Chunk.Text) == "" ||
		strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.SourceURI) == "" {
		return fmt.Errorf("identity, title, source, and text are required")
	}
	if len(item.Embedding) != dimension {
		return fmt.Errorf("embedding dimension is %d, want %d", len(item.Embedding), dimension)
	}
	for _, value := range item.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("embedding contains a non-finite value")
		}
	}
	if item.UpdatedAt.IsZero() {
		return fmt.Errorf("updated time is required")
	}
	return nil
}

func validateSearchRequest(request searchdomain.StoreRequest) error {
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.Query) == "" || len(request.KnowledgeBaseIDs) == 0 {
		return fmt.Errorf("invalid Redis search request")
	}
	if request.CandidateLimit <= 0 || request.CandidateLimit > 1000 {
		return fmt.Errorf("candidate limit must be between 1 and 1000")
	}
	return nil
}

func buildFilter(request searchdomain.StoreRequest) string {
	parts := []string{
		"@tenant_id:{" + escapeTag(request.TenantID) + "}",
		"@kb_id:{" + joinTags(request.KnowledgeBaseIDs) + "}",
	}
	if len(request.Filters.DocumentIDs) > 0 {
		parts = append(parts, "@document_id:{"+joinTags(request.Filters.DocumentIDs)+"}")
	}
	if len(request.Filters.ContentTypes) > 0 {
		parts = append(parts, "@content_type:{"+joinTags(request.Filters.ContentTypes)+"}")
	}
	if len(request.Filters.Languages) > 0 {
		parts = append(parts, "@language:{"+joinTags(request.Filters.Languages)+"}")
	}
	if request.Filters.UpdatedAfter != nil || request.Filters.UpdatedBefore != nil {
		minimum := "-inf"
		maximum := "+inf"
		if request.Filters.UpdatedAfter != nil {
			minimum = strconv.FormatInt(request.Filters.UpdatedAfter.UTC().UnixMilli(), 10)
		}
		if request.Filters.UpdatedBefore != nil {
			maximum = strconv.FormatInt(request.Filters.UpdatedBefore.UTC().UnixMilli(), 10)
		}
		parts = append(parts, "@updated_at:["+minimum+" "+maximum+"]")
	}
	return strings.Join(parts, " ")
}

func returnFields(includeDistance bool) []string {
	fields := []string{
		"tenant_id", "kb_id", "document_id", "version_id", "parent_chunk_id",
		"ordinal", "content_type", "language", "title", "heading_path_json", "body",
		"source_uri", "page_start", "page_end", "content_sha256", "updated_at",
	}
	if includeDistance {
		fields = append(fields, "vector_distance")
	}
	return fields
}

func parseSearchResult(result any, withScores, withDistance bool) ([]searchdomain.Hit, error) {
	values, ok := result.([]any)
	if !ok || len(values) == 0 {
		return nil, fmt.Errorf("unexpected Redis search response %T", result)
	}
	hits := make([]searchdomain.Hit, 0, (len(values)-1)/2)
	for index := 1; index < len(values); {
		id := asString(values[index])
		index++
		var lexical *float64
		if withScores {
			if index >= len(values) {
				return nil, fmt.Errorf("Redis search score is missing")
			}
			score, err := asFloat64(values[index])
			if err != nil {
				return nil, err
			}
			lexical = &score
			index++
		}
		if index >= len(values) {
			return nil, fmt.Errorf("Redis search fields are missing")
		}
		fieldValues, ok := values[index].([]any)
		if !ok {
			return nil, fmt.Errorf("unexpected Redis search fields %T", values[index])
		}
		index++
		fields := make(map[string]string, len(fieldValues)/2)
		for fieldIndex := 0; fieldIndex+1 < len(fieldValues); fieldIndex += 2 {
			fields[asString(fieldValues[fieldIndex])] = asString(fieldValues[fieldIndex+1])
		}
		hit, err := fieldsToHit(id, fields, withDistance)
		if err != nil {
			return nil, err
		}
		if lexical != nil {
			hit.Scores = &searchdomain.Scores{Lexical: lexical}
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func fieldsToHit(id string, fields map[string]string, withDistance bool) (searchdomain.Hit, error) {
	ordinal, err := strconv.Atoi(defaultString(fields["ordinal"], "0"))
	if err != nil {
		return searchdomain.Hit{}, fmt.Errorf("parse chunk ordinal: %w", err)
	}
	updatedMillis, err := strconv.ParseInt(defaultString(fields["updated_at"], "0"), 10, 64)
	if err != nil {
		return searchdomain.Hit{}, fmt.Errorf("parse chunk updated time: %w", err)
	}
	var headingPath []string
	if encoded := fields["heading_path_json"]; encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &headingPath); err != nil {
			return searchdomain.Hit{}, fmt.Errorf("parse chunk heading path: %w", err)
		}
	}
	hit := searchdomain.Hit{
		ID:              id,
		TenantID:        fields["tenant_id"],
		KnowledgeBaseID: fields["kb_id"],
		DocumentID:      fields["document_id"],
		VersionID:       fields["version_id"],
		ParentChunkID:   fields["parent_chunk_id"],
		Ordinal:         ordinal,
		Title:           fields["title"],
		HeadingPath:     headingPath,
		Text:            fields["body"],
		SourceURI:       fields["source_uri"],
		ContentSHA256:   fields["content_sha256"],
		UpdatedAt:       time.UnixMilli(updatedMillis).UTC(),
	}
	if value := fields["page_start"]; value != "" {
		page, err := strconv.Atoi(value)
		if err != nil {
			return searchdomain.Hit{}, fmt.Errorf("parse page start: %w", err)
		}
		hit.PageStart = &page
	}
	if value := fields["page_end"]; value != "" {
		page, err := strconv.Atoi(value)
		if err != nil {
			return searchdomain.Hit{}, fmt.Errorf("parse page end: %w", err)
		}
		hit.PageEnd = &page
	}
	if withDistance {
		distance, err := asFloat64(fields["vector_distance"])
		if err != nil {
			return searchdomain.Hit{}, fmt.Errorf("parse vector distance: %w", err)
		}
		similarity := 1 - distance
		hit.Scores = &searchdomain.Scores{Vector: &similarity}
	}
	return hit, nil
}

func fuseRRF(lexical, vector []searchdomain.Hit, limit int, includeDiagnostics bool) []searchdomain.Hit {
	type candidate struct {
		hit   searchdomain.Hit
		score float64
	}
	candidates := make(map[string]*candidate, len(lexical)+len(vector))
	add := func(hits []searchdomain.Hit) {
		for index, hit := range hits {
			item, exists := candidates[hit.ID]
			if !exists {
				copy := hit
				item = &candidate{hit: copy}
				candidates[hit.ID] = item
			} else if item.hit.Scores == nil && hit.Scores != nil {
				item.hit.Scores = hit.Scores
			} else if item.hit.Scores != nil && hit.Scores != nil {
				if hit.Scores.Lexical != nil {
					item.hit.Scores.Lexical = hit.Scores.Lexical
				}
				if hit.Scores.Vector != nil {
					item.hit.Scores.Vector = hit.Scores.Vector
				}
			}
			item.score += 1 / (rrfConstant + float64(index+1))
		}
	}
	add(lexical)
	add(vector)
	ordered := make([]*candidate, 0, len(candidates))
	for _, item := range candidates {
		ordered = append(ordered, item)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score == ordered[j].score {
			return ordered[i].hit.ID < ordered[j].hit.ID
		}
		return ordered[i].score > ordered[j].score
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	result := make([]searchdomain.Hit, 0, len(ordered))
	for _, item := range ordered {
		if includeDiagnostics {
			if item.hit.Scores == nil {
				item.hit.Scores = &searchdomain.Scores{}
			}
			fused := item.score
			item.hit.Scores.Fused = &fused
		}
		result = append(result, item.hit)
	}
	return result
}

func encodeVector(vector []float32) []byte {
	encoded := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(value))
	}
	return encoded
}

func escapeTextQuery(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) {
			builder.WriteRune(' ')
			continue
		}
		if strings.ContainsRune(`,.<>{}[]"':;!@#$%^&*()-+=~|\\/?`, r) {
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func joinTags(values []string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, escapeTag(value))
		}
	}
	return strings.Join(result, "|")
}

func escapeTag(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if strings.ContainsRune(`,.<>{}[]"':;!@#$%^&*()-+=~|\\/? `, r) {
			builder.WriteRune('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "english"
	}
	switch value {
	case "zh", "zh-cn", "zh-hans", "chinese":
		return "chinese"
	case "en", "en-us", "en-gb", "english":
		return "english"
	default:
		return value
	}
}

func isUnknownIndex(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unknown index name") || strings.Contains(message, "no such index")
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func asFloat64(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, fmt.Errorf("parse Redis number %q: %w", typed, err)
		}
		return parsed, nil
	case []byte:
		return asFloat64(string(typed))
	default:
		return 0, fmt.Errorf("unexpected Redis number %T", value)
	}
}

func asInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse Redis integer %q: %w", typed, err)
		}
		return parsed, nil
	case []byte:
		return asInt64(string(typed))
	default:
		return 0, fmt.Errorf("unexpected Redis integer %T", value)
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
