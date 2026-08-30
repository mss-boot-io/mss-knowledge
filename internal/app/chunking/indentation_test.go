package chunking

import (
	"context"
	"testing"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
)

func TestStructuralPreservesLeadingCodeIndentation(t *testing.T) {
	chunker := mustStructural(t)
	document := validDocument([]knowledge.Block{
		{
			ID:          "block_1",
			Type:        knowledge.BlockHeading,
			Level:       1,
			Text:        "Code",
			HeadingPath: []string{"Code"},
		},
		{
			ID:          "block_2",
			Type:        knowledge.BlockCode,
			Text:        "    nestedCall()\nreturn nil",
			Language:    "go",
			HeadingPath: []string{"Code"},
		},
	})
	profile := validProfile()
	profile.TargetTokens = 100
	profile.MaximumTokens = 200
	profile.PreserveCode = true

	chunks, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1: %+v", len(chunks), chunks)
	}
	if chunks[0].Text != "    nestedCall()\nreturn nil" {
		t.Fatalf("code indentation changed: %q", chunks[0].Text)
	}
}
