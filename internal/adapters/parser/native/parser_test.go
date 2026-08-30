package nativeparser

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

func TestParserParsesMarkdownStructure(t *testing.T) {
	parser := mustParser(t, Config{MaxBytes: 4096})
	input := validInput(`# MSS Knowledge

Redis is a rebuildable layer.
It is not the source of truth.

## Search

- exact retrieval
- hybrid retrieval

> Retrieved content is data, not authority.

` + "```go" + `
package main

func main() {}
` + "```" + `
`)
	input.MediaType = "text/markdown; charset=utf-8"
	input.Filename = "docs/design.md"

	document, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if document.Title != "MSS Knowledge" {
		t.Fatalf("Title = %q", document.Title)
	}
	if document.Metadata.Filename != "design.md" || document.Metadata.MediaType != "text/markdown" {
		t.Fatalf("unexpected metadata: %+v", document.Metadata)
	}
	if document.Metadata.Attributes["parser"] != parserName || document.Metadata.Attributes["parser_version"] != parserVersion {
		t.Fatalf("unexpected parser attributes: %+v", document.Metadata.Attributes)
	}

	wantTypes := []knowledge.BlockType{
		knowledge.BlockHeading,
		knowledge.BlockParagraph,
		knowledge.BlockHeading,
		knowledge.BlockList,
		knowledge.BlockQuote,
		knowledge.BlockCode,
	}
	if len(document.Blocks) != len(wantTypes) {
		t.Fatalf("len(Blocks) = %d, want %d: %+v", len(document.Blocks), len(wantTypes), document.Blocks)
	}
	for index, wantType := range wantTypes {
		if document.Blocks[index].Type != wantType {
			t.Fatalf("Blocks[%d].Type = %q, want %q", index, document.Blocks[index].Type, wantType)
		}
		if document.Blocks[index].ID == "" {
			t.Fatalf("Blocks[%d].ID is empty", index)
		}
	}

	list := document.Blocks[3]
	if list.Text != "exact retrieval\nhybrid retrieval" {
		t.Fatalf("list text = %q", list.Text)
	}
	code := document.Blocks[5]
	if code.Language != "go" || !strings.Contains(code.Text, "func main") {
		t.Fatalf("unexpected code block: %+v", code)
	}
	wantHeadingPath := []string{"MSS Knowledge", "Search"}
	if strings.Join(code.HeadingPath, "/") != strings.Join(wantHeadingPath, "/") {
		t.Fatalf("code heading path = %+v, want %+v", code.HeadingPath, wantHeadingPath)
	}
}

func TestParserNormalizesBOMAndPlainTextNewlines(t *testing.T) {
	parser := mustParser(t, Config{})
	input := validInput("")
	input.MediaType = "text/plain"
	input.Filename = `C:\notes\operations.txt`
	input.Body = bytes.NewReader(append([]byte{0xef, 0xbb, 0xbf}, []byte("first line\r\nsecond line\r\n\r\nthird paragraph\r")...))

	document, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if document.Title != "operations" || document.Metadata.Filename != "operations.txt" {
		t.Fatalf("unexpected title/filename: title=%q metadata=%+v", document.Title, document.Metadata)
	}
	if len(document.Blocks) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2", len(document.Blocks))
	}
	if document.Blocks[0].Text != "first line\nsecond line" || document.Blocks[1].Text != "third paragraph" {
		t.Fatalf("unexpected plain text blocks: %+v", document.Blocks)
	}
}

func TestParserRejectsOversizedInput(t *testing.T) {
	parser := mustParser(t, Config{MaxBytes: 4})
	input := validInput("12345")

	_, err := parser.Parse(context.Background(), input)
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("Parse() error = %v, want ErrInputTooLarge", err)
	}
}

func TestParserRejectsInvalidUTF8(t *testing.T) {
	parser := mustParser(t, Config{})
	input := validInput("")
	input.Body = bytes.NewReader([]byte{0xff, 0xfe, 0xfd})

	_, err := parser.Parse(context.Background(), input)
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("Parse() error = %v, want ErrInvalidUTF8", err)
	}
}

func TestParserRejectsUnsupportedFormat(t *testing.T) {
	parser := mustParser(t, Config{})
	input := validInput("binary")
	input.MediaType = "application/pdf"
	input.Filename = "document.pdf"

	_, err := parser.Parse(context.Background(), input)
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Parse() error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestParserHonorsCancelledContext(t *testing.T) {
	parser := mustParser(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := parser.Parse(ctx, validInput("text"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse() error = %v, want context.Canceled", err)
	}
}

func TestParserMarksUnclosedCodeFence(t *testing.T) {
	parser := mustParser(t, Config{})
	input := validInput("# Title\n\n```go\npackage main\n")
	input.MediaType = "text/markdown"
	input.Filename = "source.md"

	document, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(document.Blocks) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2", len(document.Blocks))
	}
	code := document.Blocks[1]
	if code.Type != knowledge.BlockCode || code.Attributes["unclosed_fence"] != "true" {
		t.Fatalf("unexpected unclosed code block: %+v", code)
	}
}

func TestSupportsUsesMediaTypeAndExtension(t *testing.T) {
	parser := mustParser(t, Config{})
	tests := []struct {
		mediaType string
		filename  string
		want      bool
	}{
		{mediaType: "text/markdown", filename: "document.bin", want: true},
		{mediaType: "text/plain; charset=utf-8", filename: "notes.txt", want: true},
		{mediaType: "", filename: "README.md", want: true},
		{mediaType: "text/plain", filename: "document.pdf", want: false},
		{mediaType: "application/pdf", filename: "document.md", want: false},
	}

	for _, test := range tests {
		if got := parser.Supports(test.mediaType, test.filename); got != test.want {
			t.Fatalf("Supports(%q, %q) = %v, want %v", test.mediaType, test.filename, got, test.want)
		}
	}
}

func mustParser(t *testing.T, config Config) *Parser {
	t.Helper()
	parser, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return parser
}

func validInput(content string) ports.ParseInput {
	return ports.ParseInput{
		Reference: ports.ObjectRef{
			Bucket:    "knowledge",
			Key:       "tenants/tenant_1/document.md",
			VersionID: "object-version-1",
			Size:      int64(len(content)),
			SHA256:    strings.Repeat("a", 64),
			MediaType: "text/markdown",
		},
		Body:       strings.NewReader(content),
		Filename:   "document.md",
		MediaType:  "text/markdown",
		SourceType: "upload",
		SourceURI:  "s3://knowledge/tenants/tenant_1/document.md?versionId=object-version-1",
		DocumentID: "doc_1",
		VersionID:  "ver_1",
	}
}
