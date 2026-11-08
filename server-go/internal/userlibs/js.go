package userlibs

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// JavaScript's handling of request fields, which arrive as any JSON value.

// field is body[k], nil when absent.
func field(o *jsonv.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// str is String(v ?? ”).trim().
func str(v any) string { return jsTrim(jsonv.String(v)) }

// jsTrim is String.prototype.trim: white space and line terminators.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// units is a string's length in UTF-16 code units, as .length counts.
func units(s string) int { return len(utf16.Encode([]rune(s))) }

// slice is s.slice(0, n) by UTF-16 code units.
func slice(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

var hexNum = regexp.MustCompile(`^0[xX][0-9a-fA-F]+$`)

// number is Number(v) for the values a form sends: NaN for what does not
// read as one.
func number(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		s := jsTrim(x)
		if s == "" {
			return 0
		}
		if hexNum.MatchString(s) {
			n, err := strconv.ParseUint(s[2:], 16, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
		if s == "Infinity" || s == "+Infinity" {
			return math.Inf(1)
		}
		if s == "-Infinity" {
			return math.Inf(-1)
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || strings.ContainsAny(s, "_xXpP") || strings.EqualFold(s, "nan") || strings.HasPrefix(strings.ToLower(strings.TrimLeft(s, "+-")), "inf") {
			return math.NaN()
		}
		return f
	case []any:
		if len(x) == 0 {
			return 0
		}
		if len(x) == 1 {
			return number(jsonv.String(x[0]))
		}
	}
	return math.NaN()
}

// isInt is Number.isInteger.
func isInt(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) }

// thousands is n.toLocaleString('en-US') for a whole number.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// lastLine is the last line with something on it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			return lines[i]
		}
	}
	return ""
}

// lastOf is s.trim().split('\n').pop(): the last line, empty or not.
func lastOf(s string) string {
	t := strings.TrimSpace(s)
	return t[strings.LastIndex(t, "\n")+1:]
}

var idChars = regexp.MustCompile(`[^a-z0-9]+`)

// idBase is a library id's stem from what names it.
func idBase(name string) string {
	s := idChars.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimSuffix(s, "-")
	s = slice(s, 30)
	if s == "" {
		s = "library"
	}
	return "u-" + s
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
