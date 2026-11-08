package pdf

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/giraffesyo/pdf/internal/object"
	"github.com/giraffesyo/pdf/internal/safeio"
	"github.com/giraffesyo/pdf/pdftest"
)

// Regression tests for content-stream patterns encountered in real-world
// PDF corpora — all reproduced here synthetically.

// glyphText concatenates glyph strings.
func glyphText(glyphs []Glyph) string {
	var b strings.Builder
	for _, g := range glyphs {
		b.WriteString(g.Text)
	}
	return b.String()
}

// TestContentsArraySplitToken: a /Contents array where an array token opens
// in one stream segment and closes in the next. The upstream library's
// interpreter treats each segment as an isolated stream and spins forever
// at EOF inside the unterminated array; concatenation must handle it.
func TestContentsArraySplitToken(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.PageContentsArray(2, "<< /Font << /F1 6 0 R >> >>", 4, 5),
		pdftest.Stream("", `BT /F1 12 Tf 72 700 Td [(first`),
		pdftest.Stream("", `) -600 (second)] TJ ET`),
		pdftest.Helvetica(),
	)
	lines := lineText(extract(t, doc)) // must terminate, not hang
	if len(lines) != 1 || !strings.Contains(lines[0], "first") || !strings.Contains(lines[0], "second") {
		t.Errorf("lines = %q, want both halves of the split TJ array", lines)
	}
}

func TestContentsArrayTotalSizeLimit(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.PageContentsArray(2, "<< >>", 4, 5),
		pdftest.Stream("", "abc"),
		pdftest.Stream("", "def"),
	)
	r, err := object.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	got := contentBytesLimit(r.Page(1).Key("Contents"), 5)
	if string(got) != "abc\nd" {
		t.Fatalf("contentBytesLimit() = %q, want %q", got, "abc\nd")
	}
}

// TestNBSPNormalized: some generators join every word with non-breaking
// space glyphs; output must use plain spaces.
func TestNBSPNormalized(t *testing.T) {
	text := glyphText(extract(t, simpleDoc("BT /F1 12 Tf 72 700 Td (one\\240two) Tj ET")))
	if strings.ContainsRune(text, ' ') {
		t.Errorf("NBSP survived: %q", text)
	}
	if text != "one two" {
		t.Errorf("text = %q, want %q", text, "one two")
	}
}

// TestInlineImageSkipped: BI..ID..EI binary data must be skipped without
// derailing the lexer; text after the image must still extract.
func TestInlineImageSkipped(t *testing.T) {
	content := "BT /F1 12 Tf 72 700 Td (before) Tj ET\n" +
		"BI /W 4 /H 4 /BPC 8 /CS /G ID \x00\xffTj(fake)Tj\x01garbage\x02 EI\n" +
		"BT /F1 12 Tf 72 650 Td (after) Tj ET"
	lines := lineText(extract(t, simpleDoc(content)))
	if len(lines) != 2 || lines[0] != "before" || lines[1] != "after" {
		t.Errorf("lines = %q, want [before after]", lines)
	}
}

// TestType3WithoutToUnicodeDropped: Type3 glyph procedures with no
// ToUnicode map have no recoverable text; raw codes must not leak through
// as garbage symbols.
func TestType3WithoutToUnicodeDropped(t *testing.T) {
	font := "<< /Type /Font /Subtype /Type3 /FontBBox [0 0 1 1] /FontMatrix [0.001 0 0 0.001 0 0] " +
		"/CharProcs << >> /Encoding << /Differences [33 /g33 34 /g34] >> /FirstChar 33 /LastChar 34 /Widths [500 500] >>"
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< /Font << /F1 5 0 R >> >>"),
		pdftest.Stream("", `BT /F1 12 Tf 72 700 Td (!") Tj ET`),
		font,
	)
	if glyphs := extract(t, doc); len(glyphs) != 0 {
		t.Errorf("got %d glyphs from undecodable Type3 font, want 0", len(glyphs))
	}
}

// TestWorkBudgetTerminates: a content stream with far more operators than
// the per-page budget must abort quickly, keeping earlier glyphs.
func TestWorkBudgetTerminates(t *testing.T) {
	oldOps := maxOpsPerPage
	maxOpsPerPage = 500
	defer func() { maxOpsPerPage = oldOps }()

	var b strings.Builder
	b.WriteString("BT /F1 12 Tf 72 700 Td (kept) Tj ET\n")
	for range 5000 {
		b.WriteString("q Q ")
	}
	b.WriteString("BT /F1 12 Tf 72 600 Td (past the budget) Tj ET")
	text := glyphText(extract(t, simpleDoc(b.String())))
	if !strings.Contains(text, "kept") {
		t.Errorf("pre-budget glyphs lost: %q", text)
	}
	if strings.Contains(text, "past") {
		t.Errorf("budget did not stop the walk: %q", text)
	}
}

// TestLexerNeverLoops: pathological byte soups must terminate.
func TestLexerNeverLoops(_ *testing.T) {
	cases := []string{
		"[",                   // unterminated array
		"[[[[[[",              // nested unterminated arrays
		"<<",                  // unterminated dict
		"(",                   // unterminated string
		"{",                   // unterminated proc
		"< 4 1 >",             // hex string with spaces
		"] ) >> } ",           // stray closers
		"/",                   // bare name marker
		"BI /W 4 ID \x00\x01", // inline image without EI
		strings.Repeat("[", 1000) + strings.Repeat("]", 999),
	}
	for _, c := range cases {
		// Termination is the assertion: a loop would hang the test.
		interpretContent([]byte(c), func([]byte, []operand) {})
	}
}

func TestLexerLiteralStringEscapes(t *testing.T) {
	text := glyphText(extract(t, simpleDoc(`BT /F1 12 Tf 72 700 Td (a\(b\)c\\d\141) Tj ET`)))
	if text != `a(b)c\da` {
		t.Errorf("text = %q, want %q", text, `a(b)c\da`)
	}
}

func TestStalledStreamGuard(t *testing.T) {
	// readStreamBounded must not hang on a reader that keeps returning
	// (0, nil) — simulated via a stub since real filter chains are opaque.
	rs := &stallReader{}
	got := safeio.ReadAllGuarded(rs)
	if len(got) != 3 {
		t.Errorf("got %d bytes, want 3", len(got))
	}
}

// stallReader returns a few bytes then (0, nil) forever.
type stallReader struct{ reads int }

func (s *stallReader) Read(p []byte) (int, error) {
	s.reads++
	if s.reads == 1 && len(p) >= 3 {
		copy(p, "abc")
		return 3, nil
	}
	return 0, nil
}

func TestSimpleEndToEnd(t *testing.T) {
	// Sanity: the simple path still works end to end after the lexer swap.
	glyphs := extract(t, simpleDoc(`BT /F1 12 Tf 72 720 Td (hello) Tj ET`))
	if len(glyphs) != 5 {
		t.Errorf("glyphs = %d, want 5", len(glyphs))
	}
}

func TestPlainNumberMatchesStrconv(t *testing.T) {
	tokens := []string{
		"0", "7", "-7", "+7", "12.5", "-12.5", ".5", "-.5", "5.", "0.000001",
		"123456789012345", "1234567890123456", "-0", "0.1", "3.14159265358979",
		"72.000", "-300", "1e5", "--5", "3.4.5", ".", "+", "1_0", "0x10", "Infinity",
		"0.30000000000000004", "999999999999999", "0.1234567890123456789012",
	}
	for _, tok := range tokens {
		want, wantErr := strconv.ParseFloat(tok, 64)
		got, ok := parsePlainNumber([]byte(tok))
		if !ok {
			continue // falls back to strconv; any form is allowed to
		}
		if wantErr != nil {
			t.Errorf("%q: fast path accepted what strconv rejects (%v)", tok, wantErr)
			continue
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("%q: fast path %v (%x), strconv %v (%x)", tok, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
	for _, tok := range []string{"0", "-12.5", ".5", "5.", "72.000"} {
		if _, ok := parsePlainNumber([]byte(tok)); !ok {
			t.Errorf("%q: expected the fast path to handle it", tok)
		}
	}
}
