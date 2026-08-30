package nativeparser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

const (
	defaultMaxBytes = int64(16 << 20)
	parserName      = "native-text-markdown"
	parserVersion   = "1"
)

var (
	// ErrInvalidConfig is returned when the parser cannot enforce a safe bound.
	ErrInvalidConfig = errors.New("invalid native parser configuration")
	// ErrInvalidInput is returned when source metadata or textual content is invalid.
	ErrInvalidInput = errors.New("invalid native parser input")
	// ErrUnsupportedFormat is returned when another parser must handle the object.
	ErrUnsupportedFormat = errors.New("unsupported native parser format")
	// ErrInputTooLarge is returned before unbounded content is retained in memory.
	ErrInputTooLarge = errors.New("native parser input too large")
	// ErrInvalidUTF8 is returned because normalized text must be valid UTF-8.
	ErrInvalidUTF8 = errors.New("native parser input is not valid UTF-8")
)

// Config controls bounded native text parsing.
type Config struct {
	MaxBytes int64
}

// Parser converts bounded UTF-8 plain text and Markdown into KnowledgeDocument blocks.
type Parser struct {
	maxBytes int64
}

// New creates a native parser. A zero limit selects the safe default.
func New(config Config) (*Parser, error) {
	if config.MaxBytes == 0 {
		config.MaxBytes = defaultMaxBytes
	}
	if config.MaxBytes < 0 {
		return nil, fmt.Errorf("%w: max bytes must not be negative", ErrInvalidConfig)
	}
	return &Parser{maxBytes: config.MaxBytes}, nil
}

// Name implements ports.Parser.
func (p *Parser) Name() string {
	return parserName
}

// Supports reports whether the object can be parsed without an external document service.
func (p *Parser) Supports(mediaType, filename string) bool {
	mediaType = normalizeMediaType(mediaType)
	extension := strings.ToLower(path.Ext(normalizeFilename(filename)))

	switch mediaType {
	case "text/markdown", "text/x-markdown":
		return true
	case "text/plain":
		return extension == "" || extension == ".txt" || extension == ".text" ||
			extension == ".md" || extension == ".markdown"
	case "":
		return extension == ".txt" || extension == ".text" ||
			extension == ".md" || extension == ".markdown"
	default:
		return false
	}
}

// Parse implements ports.Parser.
func (p *Parser) Parse(ctx context.Context, input ports.ParseInput) (knowledge.Document, error) {
	if p == nil || p.maxBytes <= 0 {
		return knowledge.Document{}, fmt.Errorf("%w: parser is not initialized", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return knowledge.Document{}, err
	}
	if input.Body == nil || strings.TrimSpace(input.KnowledgeBaseID) == "" ||
		strings.TrimSpace(input.DocumentID) == "" || strings.TrimSpace(input.VersionID) == "" ||
		strings.TrimSpace(input.Filename) == "" || strings.TrimSpace(input.SourceType) == "" ||
		strings.TrimSpace(input.SourceURI) == "" {
		return knowledge.Document{}, fmt.Errorf("%w: required fields must not be empty", ErrInvalidInput)
	}
	if !p.Supports(input.MediaType, input.Filename) {
		return knowledge.Document{}, fmt.Errorf("%w: media type %q and filename %q", ErrUnsupportedFormat, input.MediaType, input.Filename)
	}

	content, err := readBounded(input.Body, p.maxBytes)
	if err != nil {
		return knowledge.Document{}, err
	}
	if err := ctx.Err(); err != nil {
		return knowledge.Document{}, err
	}
	content = stripUTF8BOM(content)
	if !utf8.Valid(content) {
		return knowledge.Document{}, ErrInvalidUTF8
	}

	text := normalizeNewlines(string(content))
	if strings.TrimSpace(text) == "" {
		return knowledge.Document{}, fmt.Errorf("%w: document contains no text", ErrInvalidInput)
	}

	markdown := isMarkdown(input.MediaType, input.Filename)
	blocks := parsePlainText(text)
	if markdown {
		blocks = parseMarkdown(text)
	}
	if len(blocks) == 0 {
		return knowledge.Document{}, fmt.Errorf("%w: parser produced no blocks", ErrInvalidInput)
	}

	title := titleFromFilename(input.Filename)
	for _, block := range blocks {
		if block.Type == knowledge.BlockHeading && block.Level == 1 && strings.TrimSpace(block.Text) != "" {
			title = strings.TrimSpace(block.Text)
			break
		}
	}

	mediaType := normalizeMediaType(input.MediaType)
	if mediaType == "" {
		if markdown {
			mediaType = "text/markdown"
		} else {
			mediaType = "text/plain"
		}
	}

	document := knowledge.Document{
		SchemaVersion:   "1.0",
		KnowledgeBaseID: strings.TrimSpace(input.KnowledgeBaseID),
		DocumentID:      strings.TrimSpace(input.DocumentID),
		VersionID:       strings.TrimSpace(input.VersionID),
		Title:           title,
		Metadata: knowledge.Metadata{
			SourceType:    strings.TrimSpace(input.SourceType),
			SourceURI:     strings.TrimSpace(input.SourceURI),
			ContentSHA256: strings.ToLower(strings.TrimSpace(input.Reference.SHA256)),
			MediaType:     mediaType,
			Filename:      normalizeFilename(input.Filename),
			Attributes: map[string]string{
				"parser":         parserName,
				"parser_version": parserVersion,
			},
		},
		Blocks: blocks,
	}
	if err := document.Validate(); err != nil {
		return knowledge.Document{}, fmt.Errorf("%w: normalized document: %v", ErrInvalidInput, err)
	}
	return document, nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read native parser input: %w", err)
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("%w: limit is %d bytes", ErrInputTooLarge, maxBytes)
	}
	return content, nil
}

func parsePlainText(content string) []knowledge.Block {
	lines := strings.Split(content, "\n")
	blocks := make([]knowledge.Block, 0, len(lines)/2+1)
	paragraph := make([]string, 0, 8)

	flush := func() {
		text := strings.TrimSpace(strings.Join(paragraph, "\n"))
		paragraph = paragraph[:0]
		if text == "" {
			return
		}
		blocks = append(blocks, newBlock(len(blocks), knowledge.BlockParagraph, text, 0, nil, "", nil))
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		paragraph = append(paragraph, strings.TrimRight(line, " \t"))
	}
	flush()
	return blocks
}

func parseMarkdown(content string) []knowledge.Block {
	lines := strings.Split(content, "\n")
	blocks := make([]knowledge.Block, 0, len(lines)/2+1)
	headings := make([]string, 6)
	paragraph := make([]string, 0, 8)
	list := make([]string, 0, 8)
	quote := make([]string, 0, 8)
	code := make([]string, 0, 16)
	inCode := false
	fenceChar := byte(0)
	fenceLength := 0
	codeLanguage := ""
	codeHeadingPath := []string(nil)

	currentHeadingPath := func() []string {
		result := make([]string, 0, len(headings))
		for _, heading := range headings {
			if heading != "" {
				result = append(result, heading)
			}
		}
		return result
	}

	flushParagraph := func() {
		text := strings.TrimSpace(strings.Join(paragraph, "\n"))
		paragraph = paragraph[:0]
		if text == "" {
			return
		}
		blocks = append(blocks, newBlock(len(blocks), knowledge.BlockParagraph, text, 0, currentHeadingPath(), "", nil))
	}
	flushList := func() {
		text := strings.TrimSpace(strings.Join(list, "\n"))
		list = list[:0]
		if text == "" {
			return
		}
		blocks = append(blocks, newBlock(len(blocks), knowledge.BlockList, text, 0, currentHeadingPath(), "", nil))
	}
	flushQuote := func() {
		text := strings.TrimSpace(strings.Join(quote, "\n"))
		quote = quote[:0]
		if text == "" {
			return
		}
		blocks = append(blocks, newBlock(len(blocks), knowledge.BlockQuote, text, 0, currentHeadingPath(), "", nil))
	}
	flushText := func() {
		flushParagraph()
		flushList()
		flushQuote()
	}
	flushCode := func(unclosed bool) {
		text := strings.TrimRight(strings.Join(code, "\n"), "\n")
		code = code[:0]
		if text == "" {
			return
		}
		attributes := map[string]string(nil)
		if unclosed {
			attributes = map[string]string{"unclosed_fence": "true"}
		}
		blocks = append(blocks, newBlock(len(blocks), knowledge.BlockCode, text, 0, codeHeadingPath, codeLanguage, attributes))
	}

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)

		if inCode {
			if isClosingFence(trimmed, fenceChar, fenceLength) {
				flushCode(false)
				inCode = false
				fenceChar = 0
				fenceLength = 0
				codeLanguage = ""
				codeHeadingPath = nil
				continue
			}
			code = append(code, rawLine)
			continue
		}

		if marker, length, language, ok := parseOpeningFence(trimmed); ok {
			flushText()
			inCode = true
			fenceChar = marker
			fenceLength = length
			codeLanguage = language
			codeHeadingPath = currentHeadingPath()
			continue
		}

		if level, heading, ok := parseHeading(line); ok {
			flushText()
			for index := level - 1; index < len(headings); index++ {
				headings[index] = ""
			}
			headings[level-1] = heading
			blocks = append(blocks, newBlock(len(blocks), knowledge.BlockHeading, heading, level, currentHeadingPath(), "", nil))
			continue
		}

		if trimmed == "" {
			flushText()
			continue
		}

		if item, ok := parseListItem(trimmed); ok {
			flushParagraph()
			flushQuote()
			list = append(list, item)
			continue
		}

		if quoted, ok := parseQuote(trimmed); ok {
			flushParagraph()
			flushList()
			quote = append(quote, quoted)
			continue
		}

		flushList()
		flushQuote()
		paragraph = append(paragraph, line)
	}

	if inCode {
		flushCode(true)
	} else {
		flushText()
	}
	return blocks
}

func newBlock(
	index int,
	blockType knowledge.BlockType,
	text string,
	level int,
	headingPath []string,
	language string,
	attributes map[string]string,
) knowledge.Block {
	return knowledge.Block{
		ID:          fmt.Sprintf("block_%06d", index+1),
		Type:        blockType,
		Level:       level,
		Text:        text,
		Language:    language,
		HeadingPath: append([]string(nil), headingPath...),
		Attributes:  attributes,
	}
}

func parseHeading(line string) (int, string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	level := 0
	for level < len(trimmed) && level < 6 && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
		return 0, "", false
	}
	heading := strings.TrimSpace(trimmed[level:])
	heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
	if heading == "" {
		return 0, "", false
	}
	return level, heading, true
}

func parseListItem(trimmed string) (string, bool) {
	if len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && trimmed[1] == ' ' {
		return strings.TrimSpace(trimmed[2:]), true
	}

	index := 0
	for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
		index++
	}
	if index == 0 || index+1 >= len(trimmed) || (trimmed[index] != '.' && trimmed[index] != ')') || trimmed[index+1] != ' ' {
		return "", false
	}
	return strings.TrimSpace(trimmed[index+2:]), true
}

func parseQuote(trimmed string) (string, bool) {
	if len(trimmed) < 2 || trimmed[0] != '>' {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:]), true
}

func parseOpeningFence(trimmed string) (byte, int, string, bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	language := strings.TrimSpace(trimmed[length:])
	return marker, length, language, true
}

func isClosingFence(trimmed string, marker byte, minimumLength int) bool {
	if len(trimmed) < minimumLength || trimmed[0] != marker {
		return false
	}
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	return length >= minimumLength && strings.TrimSpace(trimmed[length:]) == ""
}

func isMarkdown(mediaType, filename string) bool {
	mediaType = normalizeMediaType(mediaType)
	if mediaType == "text/markdown" || mediaType == "text/x-markdown" {
		return true
	}
	extension := strings.ToLower(path.Ext(normalizeFilename(filename)))
	return extension == ".md" || extension == ".markdown"
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

func normalizeFilename(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	return path.Base(value)
}

func titleFromFilename(filename string) string {
	filename = normalizeFilename(filename)
	extension := path.Ext(filename)
	title := strings.TrimSpace(strings.TrimSuffix(filename, extension))
	if title == "" {
		return filename
	}
	return title
}

func normalizeNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func stripUTF8BOM(value []byte) []byte {
	if len(value) >= 3 && value[0] == 0xef && value[1] == 0xbb && value[2] == 0xbf {
		return value[3:]
	}
	return value
}
