package service

import (
	"unicode"
	"unicode/utf16"
)

// HeuristicTokenCount estimates the token count using the lightweight rules
// used by the Java implementation: CJK characters count individually, ASCII
// characters count roughly four per token, and other characters count two per token.
func HeuristicTokenCount(text string) int {
	if len(text) == 0 {
		return 0
	}

	asciiCount := 0
	cjkCount := 0
	otherCount := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if r <= 0x7f {
			asciiCount++
			continue
		}
		if isCJK(r) {
			cjkCount++
			continue
		}
		otherCount += utf16.RuneLen(r)
	}

	asciiTokens := (asciiCount + 3) / 4
	otherTokens := (otherCount + 1) / 2
	total := asciiTokens + cjkCount + otherTokens
	if asciiCount == 0 && cjkCount == 0 && otherCount == 0 {
		return 0
	}
	if total == 0 {
		return 1
	}
	return total
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0x2e80 && r <= 0x2fff) ||
		(r >= 0x3000 && r <= 0x303f) ||
		(r >= 0x31a0 && r <= 0x31bf) ||
		(r >= 0xfe30 && r <= 0xfe4f)
}
