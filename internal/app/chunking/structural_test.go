package chunking

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

type runeCounter struct{}

func (runeCounter) Count(text string) int {
	count := 0
	for _, character := range text {
		if !unicode.IsSpace(character) {
			count++
		}
	}
	return count
}

func TestStructuralPreservesHeadingsPagesAndDeterministicIDs(t *testing.T) {
	chunker := mustStructural(t)
	page1 := 1
	page2 := 2
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockHeading, Level: 1, Text: "Architecture", HeadingPath: []string{"Architecture"}, Page: &page1},
		{ID: "block_2", Type: knowledge.BlockParagraph, Text: "aaaaa", HeadingPath: []string{"Architecture"}, Page: &page1},
		{ID: "block_3", Type: knowledge.BlockParagraph, Text: "bbbbb", HeadingPath: []string{"Architecture"}, Page: &page2},
		{ID: "block_4", Type: knowledge.BlockHeading, Level: 2, Text: "Redis", HeadingPath: []string{"Architecture", "Redis"}, Page: &page2},
		{ID: "block_5", Type: knowledge.BlockParagraph, Text: "ccccc", HeadingPath: []string{"Architecture", "Redis"}, Page: &page2},
	})
	profile := validProfile()
	profile.TargetTokens = 8
	profile.MaximumTokens = 12

	first, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	second, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("second Chunk() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic chunking mismatch:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first) != 2 {
		t.Fatalf("len(chunks) = %d, want 2: %+v", len(first), first)
	}

	if first[0].KnowledgeBaseID != "kb_1" || first[0].DocumentID != "doc_1" || first[0].VersionID != "ver_1" {
		t.Fatalf("chunk identity was not preserved: %+v", first[0])
	}
	if strings.Join(first[0].HeadingPath, "/") != "Architecture" || first[0].PageStart == nil || *first[0].PageStart != 1 || first[0].PageEnd == nil || *first[0].PageEnd != 2 {
		t.Fatalf("first chunk context = %+v", first[0])
	}
	if strings.Join(first[1].HeadingPath, "/") != "Architecture/Redis" {
		t.Fatalf("second heading path = %+v", first[1].HeadingPath)
	}
	for index, chunk := range first {
		if chunk.Ordinal != index || !strings.HasPrefix(chunk.ID, "chk_") || len(chunk.ContentSHA256) != 64 {
			t.Fatalf("chunk %d has invalid deterministic metadata: %+v", index, chunk)
		}
		if chunk.TokenCount <= 0 || chunk.TokenCount > profile.MaximumTokens {
			t.Fatalf("chunk %d token count = %d", index, chunk.TokenCount)
		}
	}
}

func TestStructuralSplitsOversizedAtomicCodeAtLineBoundaries(t *testing.T) {
	chunker := mustStructural(t)
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockHeading, Level: 1, Text: "Code", HeadingPath: []string{"Code"}},
		{ID: "block_2", Type: knowledge.BlockCode, Text: "line1\nline2\nline3", Language: "go", HeadingPath: []string{"Code"}},
	})
	profile := validProfile()
	profile.TargetTokens = 5
	profile.MaximumTokens = 5
	profile.PreserveCode = true

	chunks, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3: %+v", len(chunks), chunks)
	}
	for index, chunk := range chunks {
		want := "line" + string(rune('1'+index))
		if chunk.ContentType != knowledge.BlockCode || chunk.Text != want || chunk.TokenCount != 5 {
			t.Fatalf("chunk %d = %+v, want code %q", index, chunk, want)
		}
	}
}

func TestStructuralHardSplitsLongTextWithoutWhitespace(t *testing.T) {
	chunker := mustStructural(t)
	content := "abcdefghijklmnop"
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockParagraph, Text: content},
	})
	profile := validProfile()
	profile.TargetTokens = 5
	profile.MaximumTokens = 5

	chunks, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("len(chunks) = %d, want 4: %+v", len(chunks), chunks)
	}
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		if chunk.TokenCount > 5 {
			t.Fatalf("chunk exceeds hard maximum: %+v", chunk)
		}
		rebuilt.WriteString(chunk.Text)
	}
	if rebuilt.String() != content {
		t.Fatalf("rebuilt content = %q, want %q", rebuilt.String(), content)
	}
}

func TestStructuralAppliesBestEffortOverlapWithinHardLimit(t *testing.T) {
	chunker := mustStructural(t)
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockHeading, Level: 1, Text: "Section", HeadingPath: []string{"Section"}},
		{ID: "block_2", Type: knowledge.BlockParagraph, Text: "abcdef", HeadingPath: []string{"Section"}},
		{ID: "block_3", Type: knowledge.BlockParagraph, Text: "ghijkl", HeadingPath: []string{"Section"}},
	})
	profile := validProfile()
	profile.TargetTokens = 6
	profile.MaximumTokens = 10
	profile.OverlapTokens = 2

	chunks, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2: %+v", len(chunks), chunks)
	}
	if chunks[1].Text != "ef\n\nghijkl" || chunks[1].TokenCount != 8 {
		t.Fatalf("overlapped chunk = %+v", chunks[1])
	}
}

func TestStructuralMergesShortTailWithinSameHeading(t *testing.T) {
	chunker := mustStructural(t)
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockParagraph, Text: "abcde"},
		{ID: "block_2", Type: knowledge.BlockParagraph, Text: "fg"},
	})
	profile := validProfile()
	profile.MinimumTokens = 3
	profile.TargetTokens = 5
	profile.MaximumTokens = 10

	chunks, err := chunker.Chunk(context.Background(), document, profile)
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 1 || chunks[0].Text != "abcde\n\nfg" || chunks[0].TokenCount != 7 {
		t.Fatalf("unexpected merged chunks: %+v", chunks)
	}
}

func TestStructuralRejectsUnsupportedParentExpansion(t *testing.T) {
	chunker := mustStructural(t)
	profile := validProfile()
	profile.ParentExpansion = true

	_, err := chunker.Chunk(context.Background(), validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockParagraph, Text: "content"},
	}), profile)
	if !errors.Is(err, ErrUnsupportedProfile) {
		t.Fatalf("Chunk() error = %v, want ErrUnsupportedProfile", err)
	}
}

func TestStructuralRejectsImageOnlyDocument(t *testing.T) {
	chunker := mustStructural(t)
	document := validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockImage, AssetURI: "s3://knowledge/image.png"},
	})

	_, err := chunker.Chunk(context.Background(), document, validProfile())
	if !errors.Is(err, ErrNoChunkableContent) {
		t.Fatalf("Chunk() error = %v, want ErrNoChunkableContent", err)
	}
}

func TestStructuralHonorsCancelledContext(t *testing.T) {
	chunker := mustStructural(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := chunker.Chunk(ctx, validDocument([]knowledge.Block{
		{ID: "block_1", Type: knowledge.BlockParagraph, Text: "content"},
	}), validProfile())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chunk() error = %v, want context.Canceled", err)
	}
}

func mustStructural(t *testing.T) *Structural {
	t.Helper()
	chunker, err := NewStructural(runeCounter{})
	if err != nil {
		t.Fatalf("NewStructural() error = %v", err)
	}
	return chunker
}

func validProfile() ports.ChunkProfile {
	return ports.ChunkProfile{
		Name:           "test",
		Version:        "1",
		TargetTokens:   8,
		MinimumTokens:  0,
		MaximumTokens:  12,
		OverlapTokens:  0,
		PreserveCode:   true,
		PreserveTables: true,
	}
}

func validDocument(blocks []knowledge.Block) knowledge.Document {
	return knowledge.Document{
		SchemaVersion:   "1.0",
		KnowledgeBaseID: "kb_1",
		DocumentID:      "doc_1",
		VersionID:       "ver_1",
		Title:           "Test Document",
		Language:        "en",
		Metadata: knowledge.Metadata{
			SourceType:    "upload",
			SourceURI:     "s3://knowledge/document.md",
			ContentSHA256: strings.Repeat("a", 64),
			MediaType:     "text/markdown",
			Filename:      "document.md",
		},
		Blocks: blocks,
	}
}
