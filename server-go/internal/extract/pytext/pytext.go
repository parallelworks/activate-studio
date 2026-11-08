// Package pytext reproduces the string semantics of the Python and
// JavaScript code the Go extractors replace (indexer/enrich.py and
// server/src/preextract.ts), so that the text they write to the extract
// cache is the same byte for byte.
//
// Python counts and slices strings in code points, strips a slightly
// wider set of whitespace than Go's unicode.IsSpace, decodes invalid
// UTF-8 with one U+FFFD per maximal invalid subsequence, and translates
// "\r\n" and "\r" to "\n" when it reads a file in text mode. JavaScript
// slices strings in UTF-16 code units and trims a different set again.
package pytext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsSpace reports whether Python's str.isspace() holds for r: Go's
// unicode.IsSpace plus the four information separators U+001C to U+001F,
// which Python counts as whitespace (bidirectional class B or S).
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Strip is Python's str.strip() with no argument.
func Strip(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

// IsJSSpace reports whether JavaScript's String.prototype.trim() removes
// r: WhiteSpace (tab, VT, FF, space, NBSP, ZWNBSP and category Zs) and
// LineTerminator (LF, CR, LS, PS). U+0085 is not among them.
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0xFEFF, 0x2028, 0x2029:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// TrimJS is JavaScript's String.prototype.trim().
func TrimJS(s string) string {
	return strings.TrimFunc(s, IsJSSpace)
}

// Len is Python's len() of the string s holds: its code points. s must be
// valid UTF-8 (the output of Decode, or of a Go reader).
func Len(s string) int {
	return utf8.RuneCountInString(s)
}

// Head is Python's s[:n] for n >= 0: the first n code points.
func Head(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s // n bytes hold at most n code points
	}
	i := 0
	for k := 0; k < n; k++ {
		if i >= len(s) {
			return s
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// HeadUTF16 is JavaScript's s.slice(0, n) followed by writing the result
// as UTF-8, which Node does with a lone surrogate written as U+FFFD: when
// the cut falls between the two halves of a surrogate pair, the high half
// stays and becomes U+FFFD.
func HeadUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		// Every code point is at least one byte and at most two units, so
		// a string of n bytes or fewer has at most n units.
		return s
	}
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			if w == 2 && units+1 == n {
				return s[:i] + "\ufffd"
			}
			return s[:i]
		}
		units += w
	}
	return s
}

// JSONSafe replaces each byte of s that is not part of valid UTF-8 with
// U+FFFD, as encoding/json does when the downmark binary writes its
// result, so that the text the npm package hands preextract.ts is what an
// in-process conversion gives.
func JSONSafe(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\ufffd")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// Decode is Python's bytes.decode('utf-8', errors='replace'): valid UTF-8
// as is, and one U+FFFD for each maximal subpart of an ill-formed
// sequence (a lead byte and the continuation bytes that could still have
// completed it), as CPython's decoder does.
func Decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + 8)
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			sb.Write(b[i : i+size])
			i += size
			continue
		}
		sb.WriteString("\ufffd")
		i += maximalSubpart(b[i:])
	}
	return sb.String()
}

// maximalSubpart is the length of the ill-formed sequence starting at
// b[0] that one U+FFFD replaces: the lead byte plus each following byte
// that is a valid continuation at its position.
func maximalSubpart(b []byte) int {
	need, lo, hi := 0, byte(0x80), byte(0xBF)
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	i := 1
	for k := 0; k < need && i < len(b); k++ {
		x := b[i]
		if k == 0 {
			if x < lo || x > hi {
				return i
			}
		} else if x < 0x80 || x > 0xBF {
			return i
		}
		i++
	}
	return i
}

// Newlines is Python's universal-newlines translation on reading a file
// in text mode: "\r\n" and a lone "\r" both become "\n".
func Newlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// ReadText is Python's open(path, errors='replace').read(n) and
// Path.read_text(errors='replace')[:n] on the bytes of a file: decoded
// with Decode, newlines translated, then the first n code points (all of
// it for n < 0).
func ReadText(b []byte, n int) string {
	s := Newlines(Decode(b))
	if n < 0 {
		return s
	}
	return Head(s, n)
}

// ParseInt is Python's int() of a string, for the values ODF attributes
// carry: surrounding whitespace, an optional sign, and decimal digits
// with single underscores between them. ok is false where int() raises.
func ParseInt(s string) (n int, ok bool) {
	s = Strip(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	prevDigit := false
	for i, r := range s {
		switch {
		case r == '_':
			if !prevDigit || i == len(s)-1 {
				return 0, false
			}
			prevDigit = false
		case r >= '0' && r <= '9':
			if n < 1<<40 { // larger counts saturate; callers cap them anyway
				n = n*10 + int(r-'0')
			}
			prevDigit = true
		default:
			// Python also takes other Unicode decimal digits; no ODF
			// writer puts them in a count.
			return 0, false
		}
	}
	if neg {
		n = -n
	}
	return n, true
}
