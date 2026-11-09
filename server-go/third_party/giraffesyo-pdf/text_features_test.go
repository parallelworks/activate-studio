package pdf

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

func TestVariableWidthCompositeCMapAndCIDWidths(t *testing.T) {
	encodingCMap := pdftest.Stream("", `
begincmap
2 begincodespacerange
<00> <7F>
<8100> <81FF>
endcodespacerange
2 begincidchar
<41> 7
<8101> 300
endcidchar
endcmap`)
	toUnicode := pdftest.Stream("", `
begincmap
2 begincodespacerange
<00> <7F>
<8100> <81FF>
endcodespacerange
2 beginbfchar
<41> <0041>
<8101> <0042>
endbfchar
endcmap`)
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 10 100 Td <418101> Tj ET"),
		"<< /Type /Font /Subtype /Type0 /Encoding 8 0 R /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
		"<< /Type /Font /Subtype /CIDFontType2 /DW 1000 /W [7 [400] 300 [800]] >>",
		toUnicode,
		encodingCMap,
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := glyphText(doc.Pages[0].Glyphs); got != "AB" {
		t.Fatalf("text = %q", got)
	}
	glyphs := doc.Pages[0].Glyphs
	if len(glyphs) != 2 || glyphs[0].Advance != 4 || glyphs[1].Advance != 8 ||
		glyphs[1].X != 14 {
		t.Fatalf("glyph metrics = %#v", glyphs)
	}
}

func TestNamedAndUseCMapResolution(t *testing.T) {
	toUnicode := pdftest.Stream("", `
begincmap
/Base-UCS usecmap
endcmap`)
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 10 100 Td <41> Tj ET"),
		"<< /Type /Font /Subtype /Type0 /Encoding /Custom-H /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
		pdftest.CIDFont("/W [9 [500]]"),
		toUnicode,
	)
	resolver := func(name string) ([]byte, error) {
		switch name {
		case "Custom-H":
			return []byte(`
begincmap
1 begincodespacerange
<00> <FF>
endcodespacerange
1 begincidchar
<41> 9
endcidchar
endcmap`), nil
		case "Base-UCS":
			return []byte(`
begincmap
1 begincodespacerange
<00> <FF>
endcodespacerange
1 beginbfchar
<41> <03A9>
endbfchar
endcmap`), nil
		default:
			return nil, nil
		}
	}
	doc, err := extractOptions(t, data, Options{CMapResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Text(); got != "Ω" {
		t.Fatalf("text = %q", got)
	}
	if got := doc.Pages[0].Glyphs[0].Advance; got != 5 {
		t.Fatalf("CID-mapped advance = %v", got)
	}
}

func TestVerticalWritingMetricsAndLayout(t *testing.T) {
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 100 300 Td <00010002> Tj ET"),
		"<< /Type /Font /Subtype /Type0 /Encoding /Identity-V /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>",
		"<< /Type /Font /Subtype /CIDFontType2 /DW 1000 /DW2 [880 -1000] "+
			"/W2 [1 [-1000 500 880 -900 500 880]] >>",
		pdftest.ToUnicodeCMap("2 beginbfchar\n<0001> <0041>\n<0002> <0042>\nendbfchar"),
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	glyphs := doc.Pages[0].Glyphs
	if len(glyphs) != 2 || glyphs[0].Direction.Y >= 0 || glyphs[1].Y >= glyphs[0].Y {
		t.Fatalf("vertical glyphs = %#v", glyphs)
	}
	if got := doc.Text(); got != "AB" {
		t.Fatalf("vertical text = %q", got)
	}
}

func TestBase14SymbolAndZapfDingbatsFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		font string
		raw  string
		want string
	}{
		{"Symbol", "<< /Type /Font /Subtype /Type1 /BaseFont /Symbol >>", `\101`, "Α"},
		{"ZapfDingbats", "<< /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >>", `\041`, "✁"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := pdftest.Build(1,
				pdftest.Catalog(2),
				pdftest.Pages(3),
				pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
				pdftest.Stream("", "BT /F1 10 Tf 10 100 Td ("+test.raw+") Tj ET"),
				test.font,
			)
			doc, err := extractOptions(t, data, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := doc.Text(); got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBase14EncodingPrecedesEmbeddedFontHints(t *testing.T) {
	fontProgram := minimalTrueTypeCMap()
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 10 100 Td (A) Tj ET"),
		"<< /Type /Font /Subtype /TrueType /BaseFont /Symbol /FirstChar 65 /LastChar 65 "+
			"/Widths [500] /FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Symbol /Flags 4 /FontFile2 7 0 R >>",
		pdftest.Stream("", string(fontProgram)),
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Text(); got != "Α" {
		t.Fatalf("base-14 Symbol mapping = %q", got)
	}
}

func TestEmbeddedTrueTypeCompositeFallback(t *testing.T) {
	fontProgram := minimalTrueTypeCMap()
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 10 100 Td <0001> Tj ET"),
		"<< /Type /Font /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [6 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /DW 1000 /CIDToGIDMap /Identity /FontDescriptor 7 0 R >>",
		"<< /Type /FontDescriptor /FontName /Embedded /Flags 32 /FontFile2 8 0 R >>",
		pdftest.Stream("", string(fontProgram)),
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Text(); got != "A" {
		t.Fatalf("embedded TrueType fallback = %q", got)
	}
}

func TestEmbeddedTrueTypeMacRomanCMapFallback(t *testing.T) {
	fontProgram := minimalTrueTypeCMap()
	const cmapOffset = 28
	cmap := fontProgram[cmapOffset:]
	binary.BigEndian.PutUint16(cmap[4:6], 1) // Macintosh platform.
	binary.BigEndian.PutUint16(cmap[6:8], 0) // Roman encoding.
	cmap[12+6+65] = 0
	cmap[12+6+0x8e] = 1 // MacRoman 0x8e is é.
	mapping, err := parseSFNTCMap(fontProgram)
	if err != nil {
		t.Fatal(err)
	}
	if got := mapping['é']; got != 1 {
		t.Fatalf("MacRoman cmap mapping = %#v", mapping)
	}
	if got := simpleMapping(mapping)[0x8e]; got != "é" {
		t.Fatalf("MacRoman simple-font mapping = %q", got)
	}
}

func TestExplicitDifferencesOverrideEmbeddedFontFallback(t *testing.T) {
	fontProgram := minimalTrueTypeCMap()
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 10 Tf 10 100 Td (A) Tj ET"),
		"<< /Type /Font /Subtype /TrueType /BaseFont /Embedded /FirstChar 65 /LastChar 65 "+
			"/Widths [500] /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [65 /B] >> "+
			"/FontDescriptor 6 0 R >>",
		"<< /Type /FontDescriptor /FontName /Embedded /Flags 32 /FontFile2 7 0 R >>",
		pdftest.Stream("", string(fontProgram)),
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Text(); got != "B" {
		t.Fatalf("explicit Differences mapping = %q", got)
	}
}

func minimalTrueTypeCMap() []byte {
	const (
		cmapOffset = 28
		cmapLength = 274
	)
	data := make([]byte, cmapOffset+cmapLength)
	binary.BigEndian.PutUint32(data[0:4], 0x00010000)
	binary.BigEndian.PutUint16(data[4:6], 1)
	copy(data[12:16], "cmap")
	binary.BigEndian.PutUint32(data[20:24], cmapOffset)
	binary.BigEndian.PutUint32(data[24:28], cmapLength)
	cmap := data[cmapOffset:]
	binary.BigEndian.PutUint16(cmap[2:4], 1)
	binary.BigEndian.PutUint16(cmap[4:6], 3)
	binary.BigEndian.PutUint16(cmap[6:8], 1)
	binary.BigEndian.PutUint32(cmap[8:12], 12)
	format0 := cmap[12:]
	binary.BigEndian.PutUint16(format0[0:2], 0)
	binary.BigEndian.PutUint16(format0[2:4], 262)
	format0[6+65] = 1
	return data
}

func TestEmbeddedType1AndCFFFallbackParsers(t *testing.T) {
	type1 := []byte("/Encoding 256 array\ndup 1 /A put\ndup 2 /Omega put\n")
	if got := parseType1Encoding(type1); got[1] != "A" || got[2] != "Ω" {
		t.Fatalf("Type1 encoding = %#v", got)
	}
	cff := minimalCFF()
	got, err := parseCFFEncoding(cff)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "A" {
		t.Fatalf("CFF encoding = %#v", got)
	}
	cff[35] = 96 // ISOAdobe SID 96 is exclamdown.
	got, err = parseCFFEncoding(cff)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "¡" {
		t.Fatalf("CFF non-ASCII ISOAdobe encoding = %#v", got)
	}
	if len(cffStandardStrings) != 229 || cffStandardStrings[228] != "zcaron" {
		t.Fatalf("CFF ISOAdobe standard strings are incomplete: %d", len(cffStandardStrings))
	}
}

func minimalCFF() []byte {
	// One font, two CharStrings (.notdef and A), custom charset SID 34, and
	// custom encoding code 1 -> GID 1.
	return []byte{
		1, 0, 4, 1,
		0, 1, 1, 1, 2, 'F',
		0, 1, 1, 1, 7, 172, 15, 175, 16, 164, 17,
		0, 0,
		0, 0,
		0, 2, 1, 1, 2, 3, 14, 14,
		0, 0, 34,
		0, 1, 1,
	}
}

func TestTaggedActualTextAndArtifacts(t *testing.T) {
	content := `/Span << /ActualText (correct) >> BDC
BT /F1 10 Tf 10 100 Td (wrong) Tj ET
EMC
/Artifact BMC
BT /F1 10 Tf 10 120 Td (header) Tj ET
EMC
/Span /P1 BDC
BT /F1 10 Tf 10 80 Td (also wrong) Tj ET
EMC`
	resources := "<< /Font << /F1 5 0 R >> /Properties << /P1 << /ActualText <FEFF03A9> >> >> >>"
	data := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, resources),
		pdftest.Stream("", content),
		pdftest.Helvetica(),
	)
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := doc.Text()
	if strings.Contains(text, "wrong") || !strings.Contains(text, "correct") ||
		!strings.Contains(text, "header") || !strings.Contains(text, "Ω") {
		t.Fatalf("tagged text = %q", text)
	}
	doc, err = extractOptions(t, data, Options{IgnoreArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if text := doc.Text(); strings.Contains(text, "header") || !strings.Contains(text, "correct") {
		t.Fatalf("artifact-filtered text = %q", text)
	}
}

func TestRotatedGeometryRegionAndLayoutStrategies(t *testing.T) {
	data := simpleDoc("BT /F1 10 Tf 0 1 -1 0 100 100 Tm (AB) Tj ET")
	doc, err := extractOptions(t, data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	glyphs := doc.Pages[0].Glyphs
	if len(glyphs) != 2 || glyphs[0].Direction.Y <= 0 ||
		glyphs[0].Baseline().End.Y <= glyphs[0].Baseline().Start.Y || glyphs[0].Quad() == (Quad{}) {
		t.Fatalf("rotated geometry = %#v", glyphs)
	}
	if got := doc.Text(); got != "AB" {
		t.Fatalf("rotated text = %q", got)
	}
	if got := doc.Pages[0].TextIn(Rect{MinX: 90, MinY: 99, MaxX: 101, MaxY: 106}, LayoutOptions{}); got == "" {
		t.Fatal("region extraction returned no rotated text")
	}

	page := Page{
		CropBox: Rect{MaxX: 300, MaxY: 200},
		Glyphs: []Glyph{
			{Text: "L1", X: 10, Y: 100, Advance: 10, Size: 10},
			{Text: "R1", X: 200, Y: 100, Advance: 10, Size: 10},
			{Text: "L2", X: 10, Y: 80, Advance: 10, Size: 10},
			{Text: "R2", X: 200, Y: 80, Advance: 10, Size: 10},
		},
	}
	if got := page.TextWithOptions(LayoutOptions{Mode: LayoutPosition}); got != "L1 R1\nL2 R2" {
		t.Fatalf("position layout = %q", got)
	}
	if got := page.TextWithOptions(LayoutOptions{Mode: LayoutColumns}); got != "L1\nL2\nR1\nR2" {
		t.Fatalf("column layout = %q", got)
	}
	if got := page.TextWithOptions(LayoutOptions{Mode: LayoutContentOrder}); got != "L1 R1\nL2 R2" {
		t.Fatalf("content-order layout = %q", got)
	}
}

func TestDecodeTextStringForms(t *testing.T) {
	if got := decodeTextString([]byte{0xfe, 0xff, 0x03, 0xa9}); got != "Ω" {
		t.Fatalf("UTF-16BE = %q", got)
	}
	if got := decodeTextString([]byte{0xef, 0xbb, 0xbf, 'o', 'k'}); got != "ok" {
		t.Fatalf("UTF-8 = %q", got)
	}
	if got := decodeTextString(bytes.Clone([]byte("plain"))); got != "plain" {
		t.Fatalf("PDFDocEncoding = %q", got)
	}
}

// TestLigatureFolding: TeX fonts name ligature glyphs /fi /fl /ffi in
// /Differences, which the Adobe Glyph List maps to U+FB01–U+FB03; a
// ToUnicode CMap may name the same codepoints. Both fold to letter
// sequences by default so "file" is searchable, and PreserveLigatures keeps
// the presentation forms for callers that want them.
func TestLigatureFoldingOptions(t *testing.T) {
	differences := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf 72 700 Td (\x01le \x02ow o\x03ce \x04\x05) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /CMR10 "+
			"/Encoding << /Differences [1 /fi /fl /ffi /ff /ffl] >> >>",
	)
	toUnicode := pdftest.Build(1,
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
	cases := []struct {
		name        string
		doc         []byte
		folded, raw string
	}{
		{"differences", differences, "file flow office ffffl", "ﬁle ﬂow oﬃce ﬀﬄ"},
		{"tounicode", toUnicode, "fist", "ﬁﬅ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := extractOptions(t, tc.doc, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got := glyphText(doc.Pages[0].Glyphs); got != tc.folded {
				t.Errorf("default text = %q, want %q", got, tc.folded)
			}
			doc, err = extractOptions(t, tc.doc, Options{PreserveLigatures: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := glyphText(doc.Pages[0].Glyphs); got != tc.raw {
				t.Errorf("PreserveLigatures text = %q, want %q", got, tc.raw)
			}
		})
	}
}

func TestSanitizeWithLigatureOption(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"plain":               "plain",
		"a\u00a0b\x01\uE000c": "a bc", // NBSP, control, private use
		"\uFB00\uFB01\uFB02\uFB03\uFB04\uFB05\uFB06": "fffiflffifflstst",
		"De\uFB01nitions": "Definitions",
		"\uFB02 \uFB01":   "fl fi",  // unrelated runes pass through
		"\uFB07":          "\uFB07", // past the Latin ligature block
	}
	for in, want := range cases {
		if got := sanitizeText(in, true); got != want {
			t.Errorf("sanitizeText(%q, true) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeText("De\uFB01nitions\u00a0\x01", false); got != "De\uFB01nitions " {
		t.Errorf("sanitizeText(.., false) = %q, want ligature kept, NBSP and control still normalized", got)
	}
}
