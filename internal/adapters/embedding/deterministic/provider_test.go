package deterministic

import (
	"reflect"
	"testing"

	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func TestProviderIsDeterministicAndNormalized(t *testing.T) {
	profile := ports.EmbeddingProfile{Dimension: 64, VectorType: "FLOAT32", Normalize: true}
	provider := Provider{}

	first, err := provider.EmbedQuery(t.Context(), "Redis 向量搜索", profile)
	if err != nil {
		t.Fatalf("EmbedQuery() error = %v", err)
	}
	second, err := provider.EmbedQuery(t.Context(), "Redis 向量搜索", profile)
	if err != nil {
		t.Fatalf("EmbedQuery() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same input produced different vectors")
	}
	if len(first) != 64 {
		t.Fatalf("dimension = %d", len(first))
	}
}

func TestProviderSeparatesInputs(t *testing.T) {
	profile := ports.EmbeddingProfile{Dimension: 64, Normalize: true}
	provider := Provider{}

	first, _ := provider.EmbedQuery(t.Context(), "mss knowledge", profile)
	second, _ := provider.EmbedQuery(t.Context(), "rabbitmq delivery", profile)
	if reflect.DeepEqual(first, second) {
		t.Fatal("different inputs produced identical vectors")
	}
}

func TestProviderRejectsEmptyInput(t *testing.T) {
	_, err := (Provider{}).EmbedQuery(t.Context(), " ", ports.EmbeddingProfile{Dimension: 64})
	if err == nil {
		t.Fatal("EmbedQuery() error = nil")
	}
}
