package object

import (
	"bytes"
	"io"
	"testing"

	"github.com/giraffesyo/pdf/pdftest"
)

func open(t *testing.T, doc []byte) *Reader {
	t.Helper()
	r, err := NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

// simpleDoc is a one-page classic-xref PDF with a content stream.
func simpleDoc(content string) []byte {
	return pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		pdftest.Stream("", content),
	)
}

func streamText(t *testing.T, v Value) string {
	t.Helper()
	rc, err := v.Reader()
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func TestClassicXrefNavigation(t *testing.T) {
	r := open(t, simpleDoc("hello stream"))
	if n := r.NumPages(); n != 1 {
		t.Fatalf("NumPages = %d, want 1", n)
	}
	page := r.Page(1)
	if page.Key("Type").Name() != "Page" {
		t.Fatalf("page /Type = %q", page.Key("Type").Name())
	}
	if got := streamText(t, page.Key("Contents")); got != "hello stream" {
		t.Errorf("contents = %q", got)
	}
	if r.Err() != nil {
		t.Errorf("unexpected Err: %v", r.Err())
	}
}

func TestValueAccessors(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		pdftest.Stream("", "x"),
		"<< /Int 42 /Real 3.5 /Neg -7 /Str (hi) /Name /Foo /Bool true /Arr [1 2 3] /Ref 1 0 R >>",
	)
	r := open(t, doc)
	d := Value{r: r, data: ref{5, 0}}

	if n, ok := d.Key("Int").Int64(); !ok || n != 42 {
		t.Errorf("Int = %d,%v", n, ok)
	}
	if f, ok := d.Key("Real").Float64(); !ok || f != 3.5 {
		t.Errorf("Real = %v,%v", f, ok)
	}
	if f, ok := d.Key("Int").Float64(); !ok || f != 42 {
		t.Errorf("Int as Float64 = %v,%v (Integer must coerce)", f, ok)
	}
	if _, ok := d.Key("Real").Int64(); ok {
		t.Error("Real must not satisfy Int64")
	}
	if s := d.Key("Str").RawString(); s != "hi" {
		t.Errorf("Str = %q", s)
	}
	if n := d.Key("Name").Name(); n != "Foo" {
		t.Errorf("Name = %q", n)
	}
	if b, ok := d.Key("Bool").Bool(); !ok || !b {
		t.Errorf("Bool = %v,%v", b, ok)
	}
	if d.Key("Arr").Len() != 3 {
		t.Errorf("Arr len = %d", d.Key("Arr").Len())
	}
	if n, _ := d.Key("Arr").Index(1).Int64(); n != 2 {
		t.Errorf("Arr[1] = %d", n)
	}
	// Absent keys and wrong kinds return null / zero, never panic.
	if !d.Key("Missing").IsNull() {
		t.Error("missing key must be null")
	}
	if _, ok := d.Key("Missing").Int64(); ok {
		t.Error("missing key Int64 must be !ok")
	}
	if d.Key("Str").Index(0).Kind() != Null {
		t.Error("Index on non-array must be null")
	}
	// The /Ref entry resolves transparently to the catalog dict.
	if d.Key("Ref").Key("Type").Name() != "Catalog" {
		t.Error("indirect reference did not resolve")
	}
}

func TestContentsArray(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.PageContentsArray(2, "<< >>", 4, 5),
		pdftest.Stream("", "part one "),
		pdftest.Stream("", "part two"),
	)
	r := open(t, doc)
	contents := r.Page(1).Key("Contents")
	if contents.Kind() != Array || contents.Len() != 2 {
		t.Fatalf("contents kind=%v len=%d", contents.Kind(), contents.Len())
	}
	if got := streamText(t, contents.Index(0)) + streamText(t, contents.Index(1)); got != "part one part two" {
		t.Errorf("joined = %q", got)
	}
}

func TestInheritedResources(t *testing.T) {
	// /Resources set on the /Pages node, inherited by the page.
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /Resources << /Font << /F1 4 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 5 0 R >>",
		pdftest.Helvetica(),
		pdftest.Stream("", "x"),
	)
	r := open(t, doc)
	res := Inherited(r.Page(1), "Resources")
	if res.Key("Font").Key("F1").Key("BaseFont").Name() != "Helvetica" {
		t.Error("inherited /Resources not resolved through /Parent")
	}
}

func TestWrongStreamLengthRecovered(t *testing.T) {
	// /Length lies; the reader must recover via the endstream scan.
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		"<< /Length 2 >>\nstream\nthe real content is longer\nendstream",
	)
	r := open(t, doc)
	if got := streamText(t, r.Page(1).Key("Contents")); got != "the real content is longer" {
		t.Errorf("recovered content = %q", got)
	}
}

func TestDanglingReferenceIsNull(t *testing.T) {
	doc := simpleDoc("x")
	r := open(t, doc)
	// Object 99 was never defined.
	v := Value{r: r, data: ref{99, 0}}
	if !v.IsNull() {
		t.Error("reference to undefined object must be null")
	}
}

func TestLeadingJunkOffsetShift(t *testing.T) {
	doc := append([]byte("this is junk before the header\n"), simpleDoc("shifted")...)
	r := open(t, doc)
	if got := streamText(t, r.Page(1).Key("Contents")); got != "shifted" {
		t.Errorf("leading-junk file content = %q", got)
	}
}

func TestFilteredStream(t *testing.T) {
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3),
		pdftest.Page(2, 4, "<< >>"),
		pdftest.Flate("", "flate-compressed content stream"),
	)
	r := open(t, doc)
	if got := streamText(t, r.Page(1).Key("Contents")); got != "flate-compressed content stream" {
		t.Errorf("flate content = %q", got)
	}
}
