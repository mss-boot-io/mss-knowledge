package heuristictokenizer

import "unicode"

// Counter provides a conservative dependency-free estimate for foundation
// chunking. Production profiles may replace it with the embedding model's exact
// tokenizer through the same TokenCounter port.
type Counter struct{}

// Count estimates tokens across Latin text, CJK text, code, and punctuation.
// It intentionally rounds ASCII word runs up in groups of four bytes and counts
// CJK characters and visible symbols individually.
func (Counter) Count(text string) int {
	count := 0
	asciiRun := 0

	flushASCII := func() {
		if asciiRun == 0 {
			return
		}
		count += (asciiRun + 3) / 4
		asciiRun = 0
	}

	for _, character := range text {
		switch {
		case character <= unicode.MaxASCII && (unicode.IsLetter(character) || unicode.IsDigit(character)):
			asciiRun++
		case unicode.IsSpace(character):
			flushASCII()
		case isCJK(character):
			flushASCII()
			count++
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			flushASCII()
			count++
		default:
			flushASCII()
			count++
		}
	}
	flushASCII()
	return count
}

func isCJK(character rune) bool {
	return unicode.In(
		character,
		unicode.Han,
		unicode.Hiragana,
		unicode.Katakana,
		unicode.Hangul,
	)
}
