package pytext

import (
	"strings"
	"testing"
)

// The expected values are Python 3.12's and Node 22's for the same input.

func TestDecode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"abc", "abc"},
		{"a\xffb", "a\ufffdb"},
		{"\xe2\x82A", "\ufffdA"},       // truncated three-byte sequence: one replacement
		{"\xe0\x80A", "\ufffd\ufffdA"}, // E0 needs A0..BF next
		{"\xed\xa0\x80", "\ufffd\ufffd\ufffd"},
		{"\xf0\x9f\x98", "\ufffd"},
		{"\xf0\x9f\x98A", "\ufffdA"},
		{"\xc0\xaf", "\ufffd\ufffd"},
		{"\xf4\x90\x80\x80", "\ufffd\ufffd\ufffd\ufffd"},
		{"\xe2\x82\xac", "\u20ac"},
		{"\ufffd", "\ufffd"}, // a real replacement character passes
	} {
		if got := Decode([]byte(c.in)); got != c.want {
			t.Errorf("Decode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStrip(t *testing.T) {
	if got := Strip(" \x1c a \x1f\u0085\u00a0\u3000"); got != "a" {
		t.Errorf("Strip = %q", got)
	}
	if got := Strip("\x1c\x1d\x1e\x1fa"); got != "a" {
		t.Errorf("Strip of separators = %q", got)
	}
}

func TestTrimJS(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{" \x1c\u3000 ", "\x1c"},
		{"\u0085a", "\u0085a"}, // not whitespace to JavaScript
		{"\ufeffa", "a"},
		{"\u180ea", "\u180ea"},
		{"\u200ba", "\u200ba"},
	} {
		if got := TrimJS(c.in); got != c.want {
			t.Errorf("TrimJS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHeadUTF16(t *testing.T) {
	s := "ab\U0001F600cd"
	for _, c := range []struct {
		n    int
		want string
	}{
		{0, ""}, {2, "ab"}, {3, "ab\ufffd"}, {4, "ab\U0001F600"}, {5, "ab\U0001F600c"}, {99, s},
	} {
		if got := HeadUTF16(s, c.n); got != c.want {
			t.Errorf("HeadUTF16(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	long := strings.Repeat("\u00e9", 10)
	if got := HeadUTF16(long, 3); got != "\u00e9\u00e9\u00e9" {
		t.Errorf("HeadUTF16 of two-byte runes = %q", got)
	}
}

func TestHead(t *testing.T) {
	if got := Head("h\u00e9llo", 2); got != "h\u00e9" {
		t.Errorf("Head = %q", got)
	}
	if got := Head("ab", 5); got != "ab" {
		t.Errorf("Head past the end = %q", got)
	}
	if got := Head("ab", 0); got != "" {
		t.Errorf("Head(0) = %q", got)
	}
}

func TestJSONSafe(t *testing.T) {
	if got := JSONSafe("a\xe2\x82b"); got != "a\ufffd\ufffdb" {
		t.Errorf("JSONSafe = %q", got) // one per byte, as encoding/json writes it
	}
}

func TestReadText(t *testing.T) {
	if got := ReadText([]byte("a\r\nb\rc\n\xff"), -1); got != "a\nb\nc\n\ufffd" {
		t.Errorf("ReadText = %q", got)
	}
	if got := ReadText([]byte("a\r\nbc"), 3); got != "a\nb" {
		t.Errorf("ReadText(3) = %q", got)
	}
}

func TestParseInt(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		okay bool
	}{
		{"0", 0, true}, {"-2", -2, true}, {" 3 ", 3, true}, {"+4", 4, true}, {"1_0", 10, true},
		{"_1", 0, false}, {"1_", 0, false}, {"1__0", 0, false}, {"", 0, false}, {"x", 0, false},
	} {
		n, ok := ParseInt(c.in)
		if ok != c.okay || (ok && n != c.n) {
			t.Errorf("ParseInt(%q) = %d, %v; want %d, %v", c.in, n, ok, c.n, c.okay)
		}
	}
}
