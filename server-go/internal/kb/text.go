package kb

import (
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// DecodeUTF8 is Buffer.toString('utf8'): valid UTF-8 as it is, each
// maximal ill-formed subsequence replaced by one U+FFFD (the WHATWG
// decoder's rule, which differs from both strings.ToValidUTF8 and a
// replacement per byte).
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + 8)
	i := 0
	for i < len(b) {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		need, lower, upper := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need = 1
		case c == 0xE0:
			need, lower = 2, 0xA0
		case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
			need = 2
		case c == 0xED:
			need, upper = 2, 0x9F
		case c == 0xF0:
			need, lower = 3, 0x90
		case c >= 0xF1 && c <= 0xF3:
			need = 3
		case c == 0xF4:
			need, upper = 3, 0x8F
		default:
			sb.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := i + 1
		ok := true
		for k := 0; k < need; k++ {
			if j >= len(b) {
				ok = false
				break
			}
			lo, hi := byte(0x80), byte(0xBF)
			if k == 0 {
				lo, hi = lower, upper
			}
			if b[j] < lo || b[j] > hi {
				ok = false
				break
			}
			j++
		}
		if !ok {
			// The bytes consumed so far are one maximal subpart.
			sb.WriteRune(utf8.RuneError)
			i = j
			continue
		}
		sb.Write(b[i:j])
		i = j
	}
	return sb.String()
}

// UTF16Len is a JavaScript string's length.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// SliceUTF16 is s.slice(0, n) counted in UTF-16 code units. A cut inside a
// surrogate pair, which leaves JavaScript a lone surrogate, drops that
// character here.
func SliceUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// jsSpace is what String.prototype.trim removes: WhiteSpace and
// LineTerminator.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// JSTrim is String.prototype.trim.
func JSTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

var (
	collatorMu sync.Mutex
	collator   = collate.New(language.Und)
)

// LocaleCompare is a.localeCompare(b) under Node's default ICU locale:
// the root collation of the Unicode Collation Algorithm, which x/text
// implements as ICU does.
func LocaleCompare(a, b string) int {
	collatorMu.Lock()
	defer collatorMu.Unlock()
	return collator.CompareString(a, b)
}
