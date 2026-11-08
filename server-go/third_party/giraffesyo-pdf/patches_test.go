package pdf

import (
	"fmt"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

// Tests for the changes this copy carries over v0.6.0.

func joinText(gs []Glyph) string {
	var b strings.Builder
	for _, g := range gs {
		b.WriteString(g.Text)
	}
	return b.String()
}

func onePage(resources, content string, objs ...string) []byte {
	all := append([]string{pdftest.Catalog(2), pdftest.Pages(3), pdftest.Page(2, 4, resources), pdftest.Stream("", content)}, objs...)
	return pdftest.Build(1, all...)
}

func TestToUnicodeIdentityName(t *testing.T) {
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2('V', 0xFD, 'b')+` Tj ET`,
		"<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [6 0 R] /ToUnicode /Identity-H >>",
		pdftest.CIDFont(""))
	if got := joinText(extract(t, doc)); got != "Výb" {
		t.Errorf("got %q", got)
	}
}

func TestToUnicodeNarrowerCodespace(t *testing.T) {
	// Two-byte Identity-H codes against a one-byte ToUnicode.
	tu := pdftest.Stream("", "begincmap\n1 begincodespacerange\n<00> <FF>\nendcodespacerange\n1 beginbfrange\n<41> <43> <0061>\nendbfrange\nendcmap")
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(0x41, 0x42, 0x43)+` Tj ET`,
		pdftest.Type0Font(6, 7), pdftest.CIDFont(""), tu)
	if got := joinText(extract(t, doc)); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestToUnicodeOneByteDestination(t *testing.T) {
	tu := pdftest.Stream("", "begincmap\n1 begincodespacerange\n<00> <FF>\nendcodespacerange\n1 beginbfrange\n<00> <7f> <00>\nendbfrange\nendcmap")
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td (Hi) Tj ET`,
		"<< /Type /Font /Subtype /TrueType /BaseFont /Tahoma /ToUnicode 6 0 R >>", tu)
	if got := joinText(extract(t, doc)); got != "Hi" {
		t.Errorf("got %q", got)
	}
}

func TestPredefinedCharsetCMaps(t *testing.T) {
	for _, tc := range []struct {
		cmap, codes, want string
	}{
		{"90ms-RKSJ-H", "<82A082A282A4>", "あいう"},
		{"90ms-RKSJ-V", "<6162>", "ab"},
		{"EUC-H", "<A4A2A4A4>", "あい"},
		{"GBK-EUC-H", "<C4BFC2BC>", "目录"},
		{"UniGB-UTF16-H", "<76EE5F55>", "目录"},
		{"UniJIS-UCS2-H", "<30423044>", "あい"},
		{"KSCms-UHC-H", "<C7D1B1DB>", "한글"},
		{"ETen-B5-H", "<A4A4A4E5>", "中文"},
	} {
		doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td `+tc.codes+` Tj ET`,
			"<< /Type /Font /Subtype /Type0 /BaseFont /MS-Mincho /Encoding /"+tc.cmap+" /DescendantFonts [6 0 R] >>",
			"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /MS-Mincho /DW 1000 /W [1 [250]] >>")
		if got := joinText(extract(t, doc)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.cmap, got, tc.want)
		}
	}
}

func TestType3GlyphNames(t *testing.T) {
	// dvips names (a<code>) and glyph-list names are read; g<n> is not.
	font := "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [0.01 0 0 0.01 0 0] /CharProcs << >> " +
		"/Encoding << /Differences [65 /a65 /B 67 /g67 68 /a99 224 /a224] >> /FirstChar 65 /LastChar 224 /Widths [50 50 50 50] >>"
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td (ABCD\340) Tj ET`, font)
	gs := extract(t, doc)
	if got := joinText(gs); got != "ABà" {
		t.Errorf("got %q", got)
	}
	// Widths scale by the font matrix: 50 glyph units at 0.01 is half an em.
	if len(gs) > 1 {
		if adv := gs[0].Advance; adv < 5.9 || adv > 6.1 {
			t.Errorf("advance %g, want 6", adv)
		}
	}
}

func TestLargeFontDictionary(t *testing.T) {
	// A font dictionary past the reader's first 4 KB window keeps the
	// entries after the long /W array.
	var w strings.Builder
	w.WriteString("/W [")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&w, "%d [500] ", 1000+i)
	}
	w.WriteString("]")
	desc := pdftest.CIDFont(w.String())
	font := "<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [" + desc + "] /ToUnicode 6 0 R >>"
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(1)+` Tj ET`,
		font, pdftest.ToUnicodeCMap("1 beginbfchar\n<0001> <0041>\nendbfchar"))
	if got := joinText(extract(t, doc)); got != "A" {
		t.Errorf("got %q", got)
	}
}

func TestMalformedKeyInDictionary(t *testing.T) {
	// "/Arial,Unicode MS": the bare MS is skipped and the keys after it
	// are kept.
	font := "<< /Type /Font /BaseFont /Arial,Unicode MS /Subtype /Type0 /Encoding /Identity-H /DescendantFonts [6 0 R] /ToUnicode 7 0 R >>"
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td `+pdftest.Hex2(1)+` Tj ET`,
		font, pdftest.CIDFont(""), pdftest.ToUnicodeCMap("1 beginbfchar\n<0001> <0041>\nendbfchar"))
	if got := joinText(extract(t, doc)); got != "A" {
		t.Errorf("got %q", got)
	}
}

func TestWrongXrefOffset(t *testing.T) {
	doc := onePage("<< /Font << /F1 5 0 R >> >>", `BT /F1 12 Tf 72 700 Td (moved) Tj ET`, pdftest.Helvetica())
	// Point object 5's entry at object 4.
	s := string(doc)
	x := strings.Index(s, "xref\n")
	lines := strings.Split(s[x:], "\n")
	lines[7] = lines[6] // entry 5 = entry 4's offset
	doc = []byte(s[:x] + strings.Join(lines, "\n"))
	if got := joinText(extract(t, doc)); got != "moved" {
		t.Errorf("got %q", got)
	}
}
