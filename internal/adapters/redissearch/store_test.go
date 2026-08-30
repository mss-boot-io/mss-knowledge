package redissearch

import (
	"math"
	"strings"
	"testing"
	"time"

	searchdomain "github.com/mss-boot-io/mss-knowledge/internal/domain/search"
)

func TestBuildFilterEscapesTenantAndAuthorizationTags(t *testing.T) {
	updatedAfter := time.UnixMilli(10).UTC()
	updatedBefore := time.UnixMilli(20).UTC()
	filter := buildFilter(searchdomain.StoreRequest{
		TenantID:         "tenant 1",
		KnowledgeBaseIDs: []string{"kb:1", "kb|2"},
		Filters: searchdomain.Filters{
			DocumentIDs:   []string{"doc 1"},
			ContentTypes:  []string{"code"},
			Languages:     []string{"chinese"},
			UpdatedAfter:  &updatedAfter,
			UpdatedBefore: &updatedBefore,
		},
	})
	for _, expected := range []string{
		`@tenant_id:{tenant\ 1}`,
		`@kb_id:{kb\:1|kb\|2}`,
		`@document_id:{doc\ 1}`,
		`@content_type:{code}`,
		`@language:{chinese}`,
		`@updated_at:[10 20]`,
	} {
		if !strings.Contains(filter, expected) {
			t.Fatalf("filter %q does not contain %q", filter, expected)
		}
	}
}

func TestFuseRRFCombinesScoresAndUsesStableTieBreak(t *testing.T) {
	lexical := []searchdomain.Hit{
		{ID: "b", Scores: &searchdomain.Scores{Lexical: floatPointer(1)}},
		{ID: "a", Scores: &searchdomain.Scores{Lexical: floatPointer(0.5)}},
	}
	vector := []searchdomain.Hit{
		{ID: "a", Scores: &searchdomain.Scores{Vector: floatPointer(0.9)}},
		{ID: "c", Scores: &searchdomain.Scores{Vector: floatPointer(0.8)}},
	}

	result := fuseRRF(lexical, vector, 3, true)
	if len(result) != 3 || result[0].ID != "a" {
		t.Fatalf("fused order = %+v", result)
	}
	if result[0].Scores == nil || result[0].Scores.Lexical == nil || result[0].Scores.Vector == nil || result[0].Scores.Fused == nil {
		t.Fatalf("combined diagnostics = %+v", result[0].Scores)
	}
}

func TestParseSearchResult(t *testing.T) {
	result := []any{
		int64(1),
		"chk_1",
		"0.75",
		[]any{
			"tenant_id", "tenant_1",
			"kb_id", "kb_1",
			"document_id", "doc_1",
			"version_id", "ver_1",
			"ordinal", "2",
			"title", "Title",
			"heading_path_json", `["Architecture"]`,
			"body", "Body",
			"source_uri", "knowledge://kb_1/doc_1/ver_1",
			"content_sha256", strings.Repeat("a", 64),
			"updated_at", "1000",
		},
	}
	hits, err := parseSearchResult(result, true, false)
	if err != nil {
		t.Fatalf("parseSearchResult() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "chk_1" || hits[0].Ordinal != 2 || hits[0].UpdatedAt.UnixMilli() != 1000 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].Scores == nil || hits[0].Scores.Lexical == nil || math.Abs(*hits[0].Scores.Lexical-0.75) > 0.0001 {
		t.Fatalf("scores = %+v", hits[0].Scores)
	}
}

func TestEncodeVectorUsesLittleEndianFloat32(t *testing.T) {
	encoded := encodeVector([]float32{1, -2})
	if len(encoded) != 8 {
		t.Fatalf("encoded length = %d", len(encoded))
	}
	if got := math.Float32frombits(uint32(encoded[0]) | uint32(encoded[1])<<8 | uint32(encoded[2])<<16 | uint32(encoded[3])<<24); got != 1 {
		t.Fatalf("first value = %v", got)
	}
}

func floatPointer(value float64) *float64 { return &value }
