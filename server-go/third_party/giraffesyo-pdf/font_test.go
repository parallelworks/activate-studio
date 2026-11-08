package pdf

import (
	"bytes"
	"testing"

	"github.com/giraffesyo/pdf/internal/object"
	"github.com/giraffesyo/pdf/pdftest"
)

// TestLoadFontSharedAcrossStreams: one font object referenced from several
// content streams — every page of a typical document — must be decoded
// once. Fonts given as direct dictionaries have no identity to share.
func TestLoadFontSharedAcrossStreams(t *testing.T) {
	direct := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	doc := pdftest.Build(1,
		pdftest.Catalog(2),
		pdftest.Pages(3, 4),
		pdftest.Page(2, 5, "<< /Font << /F1 6 0 R /D "+direct+" >> >>"),
		pdftest.Page(2, 5, "<< /Font << /F2 6 0 R /D "+direct+" >> >>"),
		pdftest.Stream("", "BT /F1 12 Tf (x) Tj ET"),
		pdftest.Helvetica(),
	)
	r, err := object.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	res1 := object.Inherited(r.Page(1), "Resources")
	res2 := object.Inherited(r.Page(2), "Resources")
	shared := newFontCache()
	names1 := map[string]*fontInfo{}
	names2 := map[string]*fontInfo{}

	f1 := loadFont(names1, shared, res1, "F1", nil, 0)
	f2 := loadFont(names2, shared, res2, "F2", nil, 0)
	if f1 == nil || f1 != f2 {
		t.Errorf("indirect font decoded per stream: page1=%p page2=%p, want one shared fontInfo", f1, f2)
	}
	if got := loadFont(names1, shared, res1, "F1", nil, 0); got != f1 {
		t.Errorf("repeat lookup in the same stream = %p, want cached %p", got, f1)
	}
	if len(shared.m) != 1 {
		t.Errorf("shared cache has %d entries, want 1 (only the indirect font)", len(shared.m))
	}

	d1 := loadFont(names1, shared, res1, "D", nil, 0)
	d2 := loadFont(names2, shared, res2, "D", nil, 0)
	if d1 == nil || d2 == nil || d1 == d2 {
		t.Errorf("direct-dictionary fonts: page1=%p page2=%p, want separate per-stream entries", d1, d2)
	}
	if len(shared.m) != 1 {
		t.Errorf("direct-dictionary font entered the shared cache (%d entries)", len(shared.m))
	}

	// A nil shared cache must degrade to per-stream caching, not panic.
	if got := loadFont(map[string]*fontInfo{}, nil, res1, "F1", nil, 0); got == nil {
		t.Error("loadFont with nil shared cache returned nil font")
	}
}
