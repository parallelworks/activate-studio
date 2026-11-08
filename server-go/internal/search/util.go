package search

import (
	"regexp"
	"unicode/utf16"
)

func utf16Units(s string) []uint16  { return utf16.Encode([]rune(s)) }
func utf16String(u []uint16) string { return string(utf16.Decode(u)) }

// jsLess is JavaScript's < on strings: by UTF-16 code units.
func jsLess(a, b string) bool {
	ua, ub := utf16Units(a), utf16Units(b)
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

var wordsRe = regexp.MustCompile(`(?i)\bwords\b|\bwordf\b`)
