package encoding

import "testing"

func TestGlyphNameConventions(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"A", "A"},
		{"afii10017", "А"}, // AGL-only name (Cyrillic А)
		{"uni0041", "A"},
		{"uni00660069", "fi"},     // multiple UTF-16BE units
		{"uniD83DDE00", "😀"},      // surrogate pair combines
		{"uniD800", ""},           // lone surrogate is unresolvable
		{"unia041", ""},           // lowercase hex rejected (AGL spec)
		{"uni041", ""},            // not a multiple of 4 digits
		{"u1F600", "😀"},           // scalar form, 5 digits
		{"u0041", "A"},            // scalar form, 4 digits
		{"u110000", ""},           // beyond MaxRune
		{"uD800", ""},             // surrogate scalar
		{"f_i", "fi"},             // ligature components
		{"f_i.sc", "fi"},          // suffix stripped before split
		{"a.sc", "a"},             // suffix stripped
		{"a.sc.alt", "a"},         // everything after first period
		{"f_bogus", ""},           // one bad component drops the name
		{"bogusname", ""},         // unknown
		{"", ""},                  // empty
		{".notdef", ""},           // suffix-only
		{"quotesingle", "'"},      // AGL basics
		{"germandbls", "ß"},       // AGL basics
		{"dalethatafpatah", "דֲ"}, // multi-codepoint AGL entry
	}
	for _, c := range cases {
		if got := GlyphToText(c.name); got != c.want {
			t.Errorf("GlyphToText(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecodeBaseTables(t *testing.T) {
	cases := []struct {
		base string
		code byte
		want string
	}{
		{"WinAnsiEncoding", 'A', "A"},
		{"WinAnsiEncoding", 0x93, "“"}, // quotedblleft
		{"WinAnsiEncoding", 0xA0, " "}, // note 5: space, not NBSP
		{"WinAnsiEncoding", 0xAD, "-"}, // note 5: hyphen, not SHY
		{"WinAnsiEncoding", 0x81, ""},  // unassigned slot
		{"MacRomanEncoding", 0x8E, "é"},
		{"MacRomanEncoding", 0xDB, "¤"}, // Annex D: currency, not Euro
		{"MacRomanEncoding", 0xBD, "Ω"}, // Greek omega, not ohm sign
		{"MacRomanEncoding", 0xF0, ""},  // no Apple-logo glyph in Annex D
		{"StandardEncoding", 0x27, "’"}, // quoteright
		{"StandardEncoding", 0x60, "‘"}, // quoteleft
		{"StandardEncoding", 0xA9, "'"}, // quotesingle
		{"StandardEncoding", 0xFB, "ß"},
		{"PDFDocEncoding", 0xA0, "€"},  // Euro
		{"PDFDocEncoding", 0x18, "˘"},  // breve
		{"NotARealEncoding", 'x', "x"}, // ASCII passthrough
		{"NotARealEncoding", 0x93, ""}, // high bytes dropped
		{"", 'x', "x"},
		{"", 0xE9, ""},
	}
	for _, c := range cases {
		if got := New(c.base, nil).Decode(c.code); got != c.want {
			t.Errorf("New(%q).Decode(0x%02X) = %q, want %q", c.base, c.code, got, c.want)
		}
	}
}

func TestDecodeDifferences(t *testing.T) {
	e := New("WinAnsiEncoding", map[byte]string{
		'A':  "α",
		'B':  "", // explicitly unresolvable: dropped, not base glyph
		0x93: "x",
	})
	cases := []struct {
		code byte
		want string
	}{
		{'A', "α"},
		{'B', ""},
		{0x93, "x"},
		{'C', "C"},  // untouched codes fall through to the base
		{0x94, "”"}, // untouched high code
	}
	for _, c := range cases {
		if got := e.Decode(c.code); got != c.want {
			t.Errorf("Decode(0x%02X) = %q, want %q", c.code, got, c.want)
		}
	}
}

func TestBase14SymbolEncodings(t *testing.T) {
	symbol := New("SymbolEncoding", nil)
	if got := symbol.Decode('A'); got != "Α" {
		t.Fatalf("Symbol A = %q", got)
	}
	if got := symbol.Decode(0xf2); got != "∫" {
		t.Fatalf("Symbol integral = %q", got)
	}
	dingbats := New("ZapfDingbatsEncoding", nil)
	if got := dingbats.Decode(0x21); got != "✁" {
		t.Fatalf("Zapf 0x21 = %q", got)
	}
	if got := dingbats.Decode(0xfe); got != "➾" {
		t.Fatalf("Zapf 0xfe = %q", got)
	}
}
