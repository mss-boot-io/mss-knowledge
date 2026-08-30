package deterministic

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

var (
	// ErrInvalidProfile is returned when a deterministic embedding profile is unsafe.
	ErrInvalidProfile = errors.New("invalid deterministic embedding profile")
	// ErrInvalidInput is returned for empty or malformed text.
	ErrInvalidInput = errors.New("invalid embedding input")
)

// Provider is a deterministic, dependency-free embedding provider intended for
// local verification and tests. It is not a replacement for a semantic model.
type Provider struct{}

// EmbedDocuments implements ports.EmbeddingProvider.
func (Provider) EmbedDocuments(
	ctx context.Context,
	texts []string,
	profile ports.EmbeddingProfile,
) ([][]float32, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("%w: document batch must not be empty", ErrInvalidInput)
	}
	vectors := make([][]float32, len(texts))
	for index, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vector, err := embed(text, profile.Dimension, profile.Normalize)
		if err != nil {
			return nil, fmt.Errorf("embed document %d: %w", index, err)
		}
		vectors[index] = vector
	}
	return vectors, nil
}

// EmbedQuery implements ports.EmbeddingProvider.
func (Provider) EmbedQuery(
	ctx context.Context,
	query string,
	profile ports.EmbeddingProfile,
) ([]float32, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if instruction := strings.TrimSpace(profile.QueryInstruction); instruction != "" {
		query = instruction + "\n" + query
	}
	return embed(query, profile.Dimension, profile.Normalize)
}

// Check validates that the provider can be used. It has no external dependency.
func (Provider) Check(context.Context) error { return nil }

func validateProfile(profile ports.EmbeddingProfile) error {
	if profile.Dimension < 16 || profile.Dimension > 4096 {
		return fmt.Errorf("%w: dimension must be between 16 and 4096", ErrInvalidProfile)
	}
	if profile.VectorType != "" && strings.ToUpper(profile.VectorType) != "FLOAT32" {
		return fmt.Errorf("%w: vector type must be FLOAT32", ErrInvalidProfile)
	}
	return nil
}

func embed(text string, dimension int, normalize bool) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) {
		return nil, ErrInvalidInput
	}

	vector := make([]float32, dimension)
	tokens := tokenize(strings.ToLower(text))
	if len(tokens) == 0 {
		return nil, ErrInvalidInput
	}
	for _, token := range tokens {
		digest := sha256.Sum256([]byte(token))
		index := binary.LittleEndian.Uint32(digest[:4]) % uint32(dimension)
		sign := float32(1)
		if digest[4]&1 == 1 {
			sign = -1
		}
		weight := float32(1 + int(digest[5]&3))
		vector[index] += sign * weight

		second := binary.LittleEndian.Uint32(digest[8:12]) % uint32(dimension)
		vector[second] += sign * 0.5
	}
	if normalize {
		normalizeVector(vector)
	}
	return vector, nil
}

func tokenize(text string) []string {
	result := make([]string, 0, len(text)/4+1)
	word := make([]rune, 0, 16)
	flush := func() {
		if len(word) == 0 {
			return
		}
		result = append(result, string(word))
		word = word[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			flush()
			result = append(result, string(r))
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.':
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return result
}

func normalizeVector(vector []float32) {
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}
	factor := float32(1 / math.Sqrt(sum))
	for index := range vector {
		vector[index] *= factor
	}
}
