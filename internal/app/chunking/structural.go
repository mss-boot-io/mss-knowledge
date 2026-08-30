package chunking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/mss-boot-io/mss-knowledge/internal/domain/knowledge"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrInvalidDependency is returned when the chunker has no token counter.
	ErrInvalidDependency = errors.New("invalid chunker dependency")
	// ErrInvalidProfile is returned when chunk limits cannot be enforced consistently.
	ErrInvalidProfile = errors.New("invalid chunk profile")
	// ErrUnsupportedProfile is returned for a profile feature not yet represented honestly.
	ErrUnsupportedProfile = errors.New("unsupported chunk profile feature")
	// ErrNoChunkableContent is returned when a valid document has no textual retrieval content.
	ErrNoChunkableContent = errors.New("document has no chunkable content")
	// ErrTokenCount is returned when a token counter violates required monotonic assumptions.
	ErrTokenCount = errors.New("invalid token count")
)

// Structural preserves heading boundaries and content types while enforcing a hard token limit.
type Structural struct {
	counter ports.TokenCounter
}

// NewStructural creates a structure-aware chunker.
func NewStructural(counter ports.TokenCounter) (*Structural, error) {
	if counter == nil {
		return nil, ErrInvalidDependency
	}
	return &Structural{counter: counter}, nil
}

// Chunk implements ports.Chunker.
func (s *Structural) Chunk(
	ctx context.Context,
	document knowledge.Document,
	profile ports.ChunkProfile,
) ([]knowledge.Chunk, error) {
	if s == nil || s.counter == nil {
		return nil, ErrInvalidDependency
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := document.Validate(); err != nil {
		return nil, fmt.Errorf("validate document: %w", err)
	}
	if err := validateProfile(profile); err != nil {
		return nil, err
	}

	segments, err := s.documentSegments(ctx, document, profile)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, ErrNoChunkableContent
	}

	drafts, err := s.pack(ctx, segments, profile)
	if err != nil {
		return nil, err
	}
	drafts, err = s.mergeShortTails(drafts, profile)
	if err != nil {
		return nil, err
	}
	if profile.OverlapTokens > 0 {
		drafts, err = s.applyOverlap(drafts, profile)
		if err != nil {
			return nil, err
		}
	}

	chunks := make([]knowledge.Chunk, 0, len(drafts))
	for ordinal, draft := range drafts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if draft.tokens <= 0 || draft.tokens > profile.MaximumTokens {
			return nil, fmt.Errorf("%w: final chunk has %d tokens", ErrTokenCount, draft.tokens)
		}
		contentDigest := sha256.Sum256([]byte(draft.text))
		contentSHA256 := hex.EncodeToString(contentDigest[:])
		identityDigest := sha256.Sum256([]byte(
			document.VersionID + "\x00" + strconv.Itoa(ordinal) + "\x00" + contentSHA256,
		))
		chunks = append(chunks, knowledge.Chunk{
			ID:              "chk_" + hex.EncodeToString(identityDigest[:16]),
			DocumentID:      document.DocumentID,
			VersionID:       document.VersionID,
			KnowledgeBaseID: document.KnowledgeBaseID,
			Ordinal:         ordinal,
			ContentType:     draft.contentType,
			HeadingPath:     append([]string(nil), draft.headingPath...),
			Text:            draft.text,
			PageStart:       cloneInt(draft.pageStart),
			PageEnd:         cloneInt(draft.pageEnd),
			TokenCount:      draft.tokens,
			ContentSHA256:   contentSHA256,
		})
	}
	return chunks, nil
}

type segment struct {
	text        string
	contentType knowledge.BlockType
	headingPath []string
	pageStart   *int
	pageEnd     *int
	atomic      bool
}

type draft struct {
	segments    []segment
	text        string
	tokens      int
	contentType knowledge.BlockType
	headingPath []string
	pageStart   *int
	pageEnd     *int
	atomic      bool
}

func validateProfile(profile ports.ChunkProfile) error {
	if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Version) == "" {
		return fmt.Errorf("%w: name and version are required", ErrInvalidProfile)
	}
	if profile.MinimumTokens < 0 || profile.TargetTokens <= 0 || profile.MaximumTokens <= 0 {
		return fmt.Errorf("%w: token limits must be positive except minimum may be zero", ErrInvalidProfile)
	}
	if profile.MinimumTokens > profile.TargetTokens || profile.TargetTokens > profile.MaximumTokens {
		return fmt.Errorf("%w: require minimum <= target <= maximum", ErrInvalidProfile)
	}
	if profile.OverlapTokens < 0 || profile.OverlapTokens >= profile.MaximumTokens {
		return fmt.Errorf("%w: overlap must be non-negative and below maximum", ErrInvalidProfile)
	}
	if profile.ParentExpansion {
		return fmt.Errorf("%w: parent expansion requires persisted parent chunks", ErrUnsupportedProfile)
	}
	return nil
}

func (s *Structural) documentSegments(
	ctx context.Context,
	document knowledge.Document,
	profile ports.ChunkProfile,
) ([]segment, error) {
	segments := make([]segment, 0, len(document.Blocks))
	currentHeadingPath := []string(nil)

	for _, block := range document.Blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if block.Type == knowledge.BlockHeading {
			currentHeadingPath = append([]string(nil), block.HeadingPath...)
			if len(currentHeadingPath) == 0 {
				currentHeadingPath = []string{strings.TrimSpace(block.Text)}
			}
			continue
		}
		if block.Type == knowledge.BlockPageBreak || block.Type == knowledge.BlockImage {
			continue
		}

		text := cleanText(block.Text, block.Type)
		if text == "" {
			continue
		}
		headingPath := block.HeadingPath
		if len(headingPath) == 0 {
			headingPath = currentHeadingPath
		}
		atomic := (block.Type == knowledge.BlockCode && profile.PreserveCode) ||
			(block.Type == knowledge.BlockTable && profile.PreserveTables)

		parts, err := s.splitToBudget(text, block.Type, profile.MaximumTokens)
		if err != nil {
			return nil, fmt.Errorf("split block %s: %w", block.ID, err)
		}
		for _, part := range parts {
			segments = append(segments, segment{
				text:        part,
				contentType: block.Type,
				headingPath: append([]string(nil), headingPath...),
				pageStart:   cloneInt(block.Page),
				pageEnd:     cloneInt(block.Page),
				atomic:      atomic,
			})
		}
	}
	return segments, nil
}

func (s *Structural) splitToBudget(
	text string,
	contentType knowledge.BlockType,
	maximumTokens int,
) ([]string, error) {
	text = cleanText(text, contentType)
	if text == "" {
		return nil, nil
	}
	tokens := s.counter.Count(text)
	if tokens <= 0 {
		return nil, fmt.Errorf("%w: non-empty block counted as %d", ErrTokenCount, tokens)
	}
	if tokens <= maximumTokens {
		return []string{text}, nil
	}

	runes := []rune(text)
	parts := make([]string, 0, tokens/maximumTokens+1)
	for start := 0; start < len(runes); {
		if s.counter.Count(string(runes[start:])) <= maximumTokens {
			part := cleanText(string(runes[start:]), contentType)
			if part != "" {
				parts = append(parts, part)
			}
			break
		}

		best := s.maximumEnd(runes, start, maximumTokens)
		if best <= start {
			return nil, fmt.Errorf("%w: one text unit exceeds maximum %d", ErrTokenCount, maximumTokens)
		}
		cut := preferredCut(runes, start, best, contentType)
		if cut <= start {
			cut = best
		}
		part := cleanText(string(runes[start:cut]), contentType)
		if part == "" {
			start = cut
			continue
		}
		partTokens := s.counter.Count(part)
		if partTokens <= 0 || partTokens > maximumTokens {
			return nil, fmt.Errorf("%w: split part has %d tokens", ErrTokenCount, partTokens)
		}
		parts = append(parts, part)
		start = skipLeadingBoundary(runes, cut, contentType)
	}
	return parts, nil
}

func (s *Structural) maximumEnd(runes []rune, start, maximumTokens int) int {
	low := start + 1
	high := len(runes)
	best := start
	for low <= high {
		middle := low + (high-low)/2
		tokens := s.counter.Count(string(runes[start:middle]))
		if tokens > 0 && tokens <= maximumTokens {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

func (s *Structural) pack(
	ctx context.Context,
	segments []segment,
	profile ports.ChunkProfile,
) ([]draft, error) {
	drafts := make([]draft, 0, len(segments))
	current := make([]segment, 0, 8)

	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		built, err := s.buildDraft(current)
		if err != nil {
			return err
		}
		drafts = append(drafts, built)
		current = current[:0]
		return nil
	}

	for _, candidate := range segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if candidate.atomic {
			if err := flush(); err != nil {
				return nil, err
			}
			built, err := s.buildDraft([]segment{candidate})
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, built)
			continue
		}

		if len(current) > 0 && !sameStrings(current[0].headingPath, candidate.headingPath) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if len(current) > 0 {
			currentDraft, err := s.buildDraft(current)
			if err != nil {
				return nil, err
			}
			combined := append(append([]segment(nil), current...), candidate)
			combinedDraft, err := s.buildDraft(combined)
			if err != nil {
				return nil, err
			}
			if currentDraft.tokens >= profile.TargetTokens || combinedDraft.tokens > profile.MaximumTokens {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
		current = append(current, candidate)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return drafts, nil
}

func (s *Structural) buildDraft(segments []segment) (draft, error) {
	if len(segments) == 0 {
		return draft{}, ErrNoChunkableContent
	}
	texts := make([]string, 0, len(segments))
	contentType := segments[0].contentType
	pageStart := cloneInt(segments[0].pageStart)
	pageEnd := cloneInt(segments[0].pageEnd)
	atomic := false

	for _, item := range segments {
		texts = append(texts, item.text)
		if item.contentType != contentType {
			contentType = knowledge.BlockParagraph
		}
		pageStart = minimumPage(pageStart, item.pageStart)
		pageEnd = maximumPage(pageEnd, item.pageEnd)
		atomic = atomic || item.atomic
	}
	text := cleanText(strings.Join(texts, "\n\n"), contentType)
	tokens := s.counter.Count(text)
	if text == "" || tokens <= 0 {
		return draft{}, fmt.Errorf("%w: draft contains no countable text", ErrTokenCount)
	}
	return draft{
		segments:    append([]segment(nil), segments...),
		text:        text,
		tokens:      tokens,
		contentType: contentType,
		headingPath: append([]string(nil), segments[0].headingPath...),
		pageStart:   pageStart,
		pageEnd:     pageEnd,
		atomic:      atomic,
	}, nil
}

func (s *Structural) mergeShortTails(drafts []draft, profile ports.ChunkProfile) ([]draft, error) {
	if profile.MinimumTokens == 0 || len(drafts) < 2 {
		return drafts, nil
	}
	for index := 1; index < len(drafts); index++ {
		current := drafts[index]
		previous := drafts[index-1]
		if current.tokens >= profile.MinimumTokens || current.atomic || previous.atomic ||
			!sameStrings(current.headingPath, previous.headingPath) {
			continue
		}
		combinedSegments := append(append([]segment(nil), previous.segments...), current.segments...)
		combined, err := s.buildDraft(combinedSegments)
		if err != nil {
			return nil, err
		}
		if combined.tokens > profile.MaximumTokens {
			continue
		}
		drafts[index-1] = combined
		drafts = append(drafts[:index], drafts[index+1:]...)
		index--
	}
	return drafts, nil
}

func (s *Structural) applyOverlap(drafts []draft, profile ports.ChunkProfile) ([]draft, error) {
	for index := 1; index < len(drafts); index++ {
		previous := drafts[index-1]
		current := drafts[index]
		if previous.atomic || current.atomic || !sameStrings(previous.headingPath, current.headingPath) {
			continue
		}
		overlap := s.suffixWithinBudget(previous.text, profile.OverlapTokens, current.contentType)
		if overlap == "" {
			continue
		}
		candidate := overlap + "\n\n" + current.text
		tokens := s.counter.Count(candidate)
		if tokens <= 0 {
			return nil, fmt.Errorf("%w: overlapped text has no tokens", ErrTokenCount)
		}
		if tokens > profile.MaximumTokens {
			continue
		}
		current.text = candidate
		current.tokens = tokens
		current.pageStart = minimumPage(current.pageStart, previous.pageEnd)
		drafts[index] = current
	}
	return drafts, nil
}

func (s *Structural) suffixWithinBudget(text string, budget int, contentType knowledge.BlockType) string {
	if budget <= 0 || strings.TrimSpace(text) == "" {
		return ""
	}
	if s.counter.Count(text) <= budget {
		return cleanText(text, contentType)
	}

	runes := []rune(text)
	low := 0
	high := len(runes) - 1
	best := len(runes)
	for low <= high {
		middle := low + (high-low)/2
		tokens := s.counter.Count(string(runes[middle:]))
		if tokens > 0 && tokens <= budget {
			best = middle
			high = middle - 1
		} else {
			low = middle + 1
		}
	}
	if best >= len(runes) {
		return ""
	}
	best = advanceToBoundary(runes, best, contentType)
	if best >= len(runes) {
		return ""
	}
	result := cleanText(string(runes[best:]), contentType)
	if result == "" || s.counter.Count(result) > budget {
		return ""
	}
	return result
}

func preferredCut(runes []rune, start, best int, contentType knowledge.BlockType) int {
	minimum := start + (best-start)/2
	for index := best - 1; index >= minimum; index-- {
		if isPreferredBoundary(runes[index], contentType) {
			return index + 1
		}
	}
	return best
}

func advanceToBoundary(runes []rune, start int, contentType knowledge.BlockType) int {
	limit := start + (len(runes)-start)/3
	if limit <= start {
		limit = len(runes)
	}
	for index := start; index < limit && index < len(runes); index++ {
		if isPreferredBoundary(runes[index], contentType) {
			return skipLeadingBoundary(runes, index+1, contentType)
		}
	}
	return start
}

func isPreferredBoundary(character rune, contentType knowledge.BlockType) bool {
	if contentType == knowledge.BlockCode || contentType == knowledge.BlockTable {
		return character == '\n'
	}
	return unicode.IsSpace(character) || strings.ContainsRune("。！？.!?;；", character)
}

func skipLeadingBoundary(runes []rune, start int, contentType knowledge.BlockType) int {
	for start < len(runes) {
		character := runes[start]
		if contentType == knowledge.BlockCode || contentType == knowledge.BlockTable {
			if character != '\n' && character != '\r' {
				break
			}
		} else if !unicode.IsSpace(character) {
			break
		}
		start++
	}
	return start
}

func cleanText(text string, contentType knowledge.BlockType) string {
	if contentType == knowledge.BlockCode || contentType == knowledge.BlockTable {
		return strings.Trim(text, "\r\n")
	}
	return strings.TrimSpace(text)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func minimumPage(left, right *int) *int {
	if left == nil {
		return cloneInt(right)
	}
	if right == nil || *left <= *right {
		return cloneInt(left)
	}
	return cloneInt(right)
}

func maximumPage(left, right *int) *int {
	if left == nil {
		return cloneInt(right)
	}
	if right == nil || *left >= *right {
		return cloneInt(left)
	}
	return cloneInt(right)
}
