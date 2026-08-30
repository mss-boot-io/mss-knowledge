package knowledge

import (
	"errors"
	"fmt"
	"strings"
)

// BlockType identifies one normalized document structure element.
type BlockType string

const (
	BlockHeading   BlockType = "heading"
	BlockParagraph BlockType = "paragraph"
	BlockList      BlockType = "list"
	BlockCode      BlockType = "code"
	BlockTable     BlockType = "table"
	BlockImage     BlockType = "image"
	BlockCaption   BlockType = "caption"
	BlockQuote     BlockType = "quote"
	BlockFormula   BlockType = "formula"
	BlockPageBreak BlockType = "page_break"
)

var ErrInvalidDocument = errors.New("invalid knowledge document")

// Document is the parser-independent normalized representation stored in S3.
type Document struct {
	SchemaVersion   string   `json:"schema_version"`
	KnowledgeBaseID string   `json:"knowledge_base_id"`
	DocumentID      string   `json:"document_id"`
	VersionID       string   `json:"version_id"`
	Title           string   `json:"title"`
	Language        string   `json:"language,omitempty"`
	Metadata        Metadata `json:"metadata"`
	Blocks          []Block  `json:"blocks"`
}

// Metadata identifies the immutable source and parser output.
type Metadata struct {
	SourceType    string            `json:"source_type"`
	SourceURI     string            `json:"source_uri"`
	ContentSHA256 string            `json:"content_sha256"`
	MediaType     string            `json:"media_type"`
	Filename      string            `json:"filename"`
	Attributes    map[string]string `json:"attributes,omitempty"`
}

// Block is one normalized structural unit.
type Block struct {
	ID          string            `json:"id"`
	Type        BlockType         `json:"type"`
	Level       int               `json:"level,omitempty"`
	Text        string            `json:"text,omitempty"`
	Language    string            `json:"language,omitempty"`
	Page        *int              `json:"page,omitempty"`
	HeadingPath []string          `json:"heading_path,omitempty"`
	AssetURI    string            `json:"asset_uri,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// Chunk is a versioned retrieval unit derived from a normalized document.
type Chunk struct {
	ID              string    `json:"id"`
	DocumentID      string    `json:"document_id"`
	VersionID       string    `json:"version_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	ParentChunkID   string    `json:"parent_chunk_id,omitempty"`
	Ordinal         int       `json:"ordinal"`
	ContentType     BlockType `json:"content_type"`
	HeadingPath     []string  `json:"heading_path,omitempty"`
	Text            string    `json:"text"`
	PageStart       *int      `json:"page_start,omitempty"`
	PageEnd         *int      `json:"page_end,omitempty"`
	TokenCount      int       `json:"token_count"`
	ContentSHA256   string    `json:"content_sha256"`
}

// Validate checks the normalized document before it is persisted or chunked.
func (d Document) Validate() error {
	if strings.TrimSpace(d.SchemaVersion) == "" || strings.TrimSpace(d.KnowledgeBaseID) == "" ||
		strings.TrimSpace(d.DocumentID) == "" || strings.TrimSpace(d.VersionID) == "" ||
		strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("%w: document identity fields must not be empty", ErrInvalidDocument)
	}
	if strings.TrimSpace(d.Metadata.SourceType) == "" || strings.TrimSpace(d.Metadata.SourceURI) == "" ||
		strings.TrimSpace(d.Metadata.MediaType) == "" || strings.TrimSpace(d.Metadata.Filename) == "" {
		return fmt.Errorf("%w: source metadata is incomplete", ErrInvalidDocument)
	}
	if len(d.Metadata.ContentSHA256) != 64 || !isLowerHex(d.Metadata.ContentSHA256) {
		return fmt.Errorf("%w: content SHA-256 is invalid", ErrInvalidDocument)
	}
	if len(d.Blocks) == 0 {
		return fmt.Errorf("%w: at least one block is required", ErrInvalidDocument)
	}

	seen := make(map[string]struct{}, len(d.Blocks))
	for index, block := range d.Blocks {
		if err := block.validate(); err != nil {
			return fmt.Errorf("%w: block %d: %v", ErrInvalidDocument, index, err)
		}
		if _, duplicate := seen[block.ID]; duplicate {
			return fmt.Errorf("%w: duplicate block ID %q", ErrInvalidDocument, block.ID)
		}
		seen[block.ID] = struct{}{}
	}
	return nil
}

func (b Block) validate() error {
	if strings.TrimSpace(b.ID) == "" {
		return fmt.Errorf("block ID must not be empty")
	}
	if !knownBlockType(b.Type) {
		return fmt.Errorf("unknown block type %q", b.Type)
	}
	if b.Type == BlockHeading && b.Level <= 0 {
		return fmt.Errorf("heading level must be positive")
	}
	if b.Type != BlockImage && b.Type != BlockPageBreak && strings.TrimSpace(b.Text) == "" {
		return fmt.Errorf("text must not be empty for %s", b.Type)
	}
	if b.Type == BlockImage && strings.TrimSpace(b.AssetURI) == "" {
		return fmt.Errorf("image asset URI must not be empty")
	}
	return nil
}

func knownBlockType(value BlockType) bool {
	switch value {
	case BlockHeading, BlockParagraph, BlockList, BlockCode, BlockTable,
		BlockImage, BlockCaption, BlockQuote, BlockFormula, BlockPageBreak:
		return true
	default:
		return false
	}
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
