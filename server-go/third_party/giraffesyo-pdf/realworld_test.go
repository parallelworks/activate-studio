package pdf

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

// Tests in this file reproduce content-stream and font patterns that
// occur in real-world PDF generator output — every fixture is synthetic.

// extractDoc returns the full Document for multi-page assertions.
func extractDoc(t *testing.T, doc []byte) *Document {
	t.Helper()
	d, err := Extract(context.Background(), bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return d
}

func TestMultiPageDocument(t *testing.T) {
	res := "<< /Font << /F1 7 0 R >> >>"
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3, 5),
		pdftest.Page(2, 4, res),
		pdftest.Stream("", `BT /F1 12 Tf 72 720 Td (page one text) Tj ET`),
		pdftest.Page(2, 6, res),
		pdftest.Stream("", `BT /F1 12 Tf 72 720 Td (page two text) Tj ET`),
		pdftest.Helvetica(),
	)
	d := extractDoc(t, doc)
	if len(d.Pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(d.Pages))
	}
	if got, want := d.Text(), "page one text\n\npage two text"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

// TestWordSpacingOp: Word-generated PDFs commonly stretch lines with Tw;
// the extra advance applies to single-byte code 32 only.
func TestWordSpacingOp(t *testing.T) {
	glyphs := extract(t, simpleDoc(`BT /F1 10 Tf 5 Tw 72 700 Td (a b) Tj ET`))
	if len(glyphs) != 3 {
		t.Fatalf("glyphs = %d, want 3", len(glyphs))
	}
	// Space glyph advance = width(500)/1000*10 + Tw(5) = 10.
	if adv := glyphs[1].Advance; adv < 9.9 || adv > 10.1 {
		t.Errorf("space advance = %v, want ~10", adv)
	}
	if got := glyphs[2].X - glyphs[1].X; got < 9.9 || got > 10.1 {
		t.Errorf("b.X - space.X = %v, want ~10", got)
	}
}

// TestCharSpacingOp: Tc pads every glyph's advance.
func TestCharSpacingOp(t *testing.T) {
	glyphs := extract(t, simpleDoc(`BT /F1 10 Tf 2 Tc 72 700 Td (ab) Tj ET`))
	if len(glyphs) != 2 {
		t.Fatalf("glyphs = %d, want 2", len(glyphs))
	}
	// advance = 500/1000*10 + Tc(2) = 7.
	if adv := glyphs[0].Advance; adv < 6.9 || adv > 7.1 {
		t.Errorf("advance = %v, want ~7", adv)
	}
}

// TestHorizontalScalingOp: Tz scales advances (narrow/wide typesetting).
func TestHorizontalScalingOp(t *testing.T) {
	glyphs := extract(t, simpleDoc(`BT /F1 10 Tf 200 Tz 72 700 Td (ab) Tj ET`))
	if len(glyphs) != 2 {
		t.Fatalf("glyphs = %d, want 2", len(glyphs))
	}
	// advance doubles: 500/1000*10*2 = 10.
	if adv := glyphs[0].Advance; adv < 9.9 || adv > 10.1 {
		t.Errorf("advance = %v, want ~10", adv)
	}
}

// TestQuoteShowOperators: the ' and " operators (move to next line and
// show) appear in older generator output.
func TestQuoteShowOperators(t *testing.T) {
	glyphs := extract(t, simpleDoc(`BT /F1 10 Tf 14 TL 72 700 Td
(first) Tj
(second) '
5 1 (a b) "
ET`))
	lines := lineText(glyphs)
	if len(lines) != 3 || lines[0] != "first" || lines[1] != "second" || lines[2] != "a b" {
		t.Errorf("lines = %q, want [first second \"a b\"]", lines)
	}
}

// TestCTMScaling: scanner/print-driver output wraps text in a scaling cm;
// positions and sizes must transform so line clustering still works.
func TestCTMScaling(t *testing.T) {
	glyphs := extract(t, simpleDoc(`q 2 0 0 2 0 0 cm BT /F1 12 Tf 36 350 Td (scaled) Tj ET Q`))
	if len(glyphs) == 0 {
		t.Fatal("no glyphs")
	}
	if x := glyphs[0].X; x < 71.9 || x > 72.1 {
		t.Errorf("X = %v, want ~72 (36 × 2)", x)
	}
	if size := glyphs[0].Size; size < 23.9 || size > 24.1 {
		t.Errorf("Size = %v, want ~24 (12 × 2)", size)
	}
	if text := (Page{Glyphs: glyphs}).Text(); text != "scaled" {
		t.Errorf("Text = %q", text)
	}
}

// TestMacRomanFallback: fonts without ToUnicode fall back to their
// declared standard encoding.
func TestMacRomanFallback(t *testing.T) {
	font := "<< /Type /Font /Subtype /TrueType /BaseFont /Helvetica /Encoding /MacRomanEncoding >>"
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (caf\x8e) Tj ET"), // 0x8E = é in MacRoman
		font,
	)
	if text := glyphText(extract(t, doc)); text != "café" {
		t.Errorf("text = %q, want café", text)
	}
}

// TestHexStringShow: Word emits hex strings for show operands.
func TestHexStringShow(t *testing.T) {
	if text := glyphText(extract(t, simpleDoc(`BT /F1 12 Tf 72 700 Td <612062> Tj ET`))); text != "a b" {
		t.Errorf("text = %q, want %q", text, "a b")
	}
}

// TestLigatureToUnicode: subset fonts map ligature glyphs to multi-rune
// strings ("fi"); dropping them would corrupt words like "benefit".
func TestLigatureToUnicode(t *testing.T) {
	cmap := pdftest.ToUnicodeCMap(`1 beginbfchar
<0001> <00660069>
endbfchar`)
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(1)+` Tj ET`),
		pdftest.Type0Font(6, 7),
		pdftest.CIDFont("/W [1 [500]]"),
		cmap,
	)
	if text := glyphText(extract(t, doc)); text != "fi" {
		t.Errorf("text = %q, want fi", text)
	}
}

// TestLigatureFolding: TeX fonts name ligature glyphs /fi /fl /ffi in
// /Differences, which the Adobe Glyph List maps to U+FB01–U+FB03, and a
// ToUnicode CMap may name the same codepoints. Both fold to letter
// sequences so "file" is searchable, as poppler, pdf.js and MuPDF do.
func TestLigatureFolding(t *testing.T) {
	t.Run("differences", func(t *testing.T) {
		doc := pdftest.Build(1,
			pdftest.Catalog(2),
			pdftest.Pages(3),
			pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
			pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (\x01le \x02ow o\x03ce \x04\x05) Tj ET"),
			"<< /Type /Font /Subtype /Type1 /BaseFont /CMR10 "+
				"/Encoding << /Differences [1 /fi /fl /ffi /ff /ffl] >> >>",
		)
		if text, want := glyphText(extract(t, doc)), "file flow office ffffl"; text != want {
			t.Errorf("text = %q, want %q", text, want)
		}
	})
	t.Run("tounicode", func(t *testing.T) {
		doc := pdftest.Build(1,
			pdftest.Catalog(2),
			pdftest.Pages(3),
			pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
			pdftest.Stream("", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(1, 2)+` Tj ET`),
			pdftest.Type0Font(6, 7),
			pdftest.CIDFont("/W [1 [500 500]]"),
			pdftest.ToUnicodeCMap(`2 beginbfchar
<0001> <FB01>
<0002> <FB05>
endbfchar`),
		)
		if text := glyphText(extract(t, doc)); text != "fist" {
			t.Errorf("text = %q, want fist", text)
		}
	})
}

// TestSurrogatePairToUnicode: emoji and symbols map through UTF-16BE
// surrogate pairs in ToUnicode CMaps.
func TestSurrogatePairToUnicode(t *testing.T) {
	cmap := pdftest.ToUnicodeCMap(`1 beginbfchar
<0001> <D83DDE00>
endbfchar`)
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(1)+` Tj ET`),
		pdftest.Type0Font(6, 7),
		pdftest.CIDFont("/W [1 [500]]"),
		cmap,
	)
	if text := glyphText(extract(t, doc)); text != "😀" {
		t.Errorf("text = %q, want 😀", text)
	}
}

// TestFullDocumentReconstruction: end-to-end reconstruction of a
// Google-Docs-style export — every pattern at once: all content inside a
// Form XObject, a composite-font headline decoded via ToUnicode, a
// Helvetica body positioned with Td/TL/T*, and TJ kerning gaps that must
// become word boundaries.
func TestFullDocumentReconstruction(t *testing.T) {
	const headline = "SYNTHETIC HEADLINE 123"
	var bfchars, codes strings.Builder
	runes := []rune(headline)
	for i, r := range runes {
		fmt.Fprintf(&bfchars, "<%04X> <%04X>\n", i+1, r)
	}
	codes.WriteByte('<')
	for i := range runes {
		fmt.Fprintf(&codes, "%04X", i+1)
	}
	codes.WriteByte('>')
	cmap := pdftest.ToUnicodeCMap(fmt.Sprintf("%d beginbfchar\n%sendbfchar", len(runes), bfchars.String()))

	form := pdftest.Stream(
		"/Type /XObject /Subtype /Form /BBox [0 0 612 792] "+
			"/Resources << /Font << /F1 5 0 R /F2 7 0 R >> >>",
		`BT /F2 16 Tf 72 720 Td `+codes.String()+` Tj ET
BT /F1 10 Tf 14 TL 72 695 Td
(part one \(alpha\) | part two \(beta\) | part three) Tj
0 -30 Td
(SECTION ONE) Tj
T*
[(kerned segments need) -600 (a space here.)] TJ
ET`)
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /XObject << /X1 6 0 R >> >>"),
		pdftest.Stream("", "q /X1 Do Q"),
		pdftest.Helvetica(),
		form,
		pdftest.Type0Font(8, 9),
		pdftest.CIDFont(fmt.Sprintf("/W [1 %d 700]", len(runes))),
		cmap,
	)
	got := extractDoc(t, doc).Text()
	want := "SYNTHETIC HEADLINE 123\n" +
		"part one (alpha) | part two (beta) | part three\n" +
		"SECTION ONE\n" +
		"kerned segments need a space here."
	if got != want {
		t.Errorf("Text() =\n%q\nwant\n%q", got, want)
	}
}
