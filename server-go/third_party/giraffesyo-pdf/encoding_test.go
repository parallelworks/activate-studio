package pdf

import (
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

// Tests for the simple-font encoding fallback (used when a font has no
// /ToUnicode): base tables, /BaseEncoding + /Differences dictionaries,
// spec defaults, and the drop-don't-garble policy for symbolic fonts.

// fontDoc builds a one-page PDF using the given font object as /F1.
func fontDoc(font, content string) []byte {
	return pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", content),
		font,
	)
}

func TestWinAnsiQuotesFallback(t *testing.T) {
	doc := fontDoc(
		"<< /Type /Font /Subtype /TrueType /BaseFont /Arial /Encoding /WinAnsiEncoding >>",
		"BT /F1 12 Tf 72 700 Td (\x93x\x94) Tj ET") // CP1252 curly quotes
	if text := glyphText(extract(t, doc)); text != "“x”" {
		t.Errorf("text = %q, want %q", text, "“x”")
	}
}

func TestEncodingDictBaseAndDifferences(t *testing.T) {
	// /BaseEncoding must be honored for non-differenced codes (0x93 → “
	// from WinAnsi); /Differences names resolve through the glyph-name
	// algorithm (alpha via AGL, uni0042 via the hex convention), and
	// unresolvable names drop rather than leak raw bytes.
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /FAKEFT+Custom " +
		"/Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /alpha /uni0042 /bogusglyph] >> >>"
	doc := fontDoc(font, "BT /F1 12 Tf 72 700 Td (ABC\x93) Tj ET")
	if text := glyphText(extract(t, doc)); text != "αB“" {
		t.Errorf("text = %q, want %q (α B dropped “)", text, "αB“")
	}
}

func TestStandardEncodingDefault(t *testing.T) {
	// A nonsymbolic font with no /Encoding defaults to StandardEncoding
	// (ISO 32000-1 §9.6.5.4): code 0x27 is quoteright, not apostrophe.
	doc := fontDoc(
		"<< /Type /Font /Subtype /Type1 /BaseFont /FAKEFT+Serif >>",
		"BT /F1 12 Tf 72 700 Td (don't) Tj ET")
	if text := glyphText(extract(t, doc)); text != "don’t" {
		t.Errorf("text = %q, want %q", text, "don’t")
	}
}

func TestUnknownEncodingNamePassthrough(t *testing.T) {
	// An unrecognized named encoding keeps ASCII readable and drops high
	// bytes instead of guessing.
	doc := fontDoc(
		"<< /Type /Font /Subtype /Type1 /BaseFont /FAKEFT+Expert /Encoding /MacExpertEncoding >>",
		"BT /F1 12 Tf 72 700 Td (ok\x8e) Tj ET")
	if text := glyphText(extract(t, doc)); text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
}

func TestSymbolicFontNoEncodingPassthrough(t *testing.T) {
	// A symbolic font's encoding lives in the font program; ASCII survives
	// (subset fonts are often wrongly flagged symbolic) and high bytes drop.
	font := "<< /Type /Font /Subtype /TrueType /BaseFont /FAKEFT+Icons " +
		"/FontDescriptor << /Flags 4 >> >>"
	doc := fontDoc(font, "BT /F1 12 Tf 72 700 Td (abc\x93\xfe) Tj ET")
	if text := glyphText(extract(t, doc)); text != "abc" {
		t.Errorf("text = %q, want %q", text, "abc")
	}
}
