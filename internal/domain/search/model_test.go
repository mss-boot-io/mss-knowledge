package search

import (
	"errors"
	"testing"
	"time"
)

func TestRequestDefaultsAndValidation(t *testing.T) {
	request := Request{Query: "Redis hybrid search"}.WithDefaults()
	if request.Mode != ModeBalanced || request.TopK != 8 {
		t.Fatalf("unexpected defaults: %+v", request)
	}
	if err := request.Validate(20); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRequestRejectsInvalidInputs(t *testing.T) {
	now := time.Now()
	before := now.Add(-time.Hour)

	tests := []Request{
		{Query: "", Mode: ModeBalanced, TopK: 1},
		{Query: "query", Mode: "unknown", TopK: 1},
		{Query: "query", Mode: ModeFast, TopK: 0},
		{Query: "query", Mode: ModeFast, TopK: 21},
		{Query: "query", Mode: ModeFast, TopK: 1, KnowledgeBaseIDs: []string{""}},
		{Query: "query", Mode: ModeFast, TopK: 1, Filters: Filters{UpdatedAfter: &now, UpdatedBefore: &before}},
	}

	for i, request := range tests {
		if err := request.Validate(20); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("case %d Validate() error = %v, want ErrInvalidRequest", i, err)
		}
	}
}

func TestPrincipalScope(t *testing.T) {
	principal := Principal{Scopes: map[string]struct{}{"knowledge.search": {}}}
	if !principal.HasScope("knowledge.search") {
		t.Fatal("HasScope(knowledge.search) = false")
	}
	if principal.HasScope("knowledge.write") {
		t.Fatal("HasScope(knowledge.write) = true")
	}
}
