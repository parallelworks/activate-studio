package pdf

import (
	"testing"
	"unsafe"
)

// TestGlyphGeometryDerivation pins Baseline and Quad for every glyph
// shape the extractor produces, and for glyphs built from origin,
// advance, and size alone.
func TestGlyphGeometryDerivation(t *testing.T) {
	cases := []struct {
		name     string
		glyph    Glyph
		baseline Line
		quad     Quad
	}{
		{
			name:     "horizontal upright",
			glyph:    positionedGlyph("a", matrix{12, 0, 0, 12, 100, 700}, 1, 0, 0.5),
			baseline: Line{Start: Point{X: 100, Y: 700}, End: Point{X: 106, Y: 700}},
			quad:     Quad{{X: 100, Y: 700}, {X: 106, Y: 700}, {X: 106, Y: 712}, {X: 100, Y: 712}},
		},
		{
			name:     "horizontal rotated 90 degrees",
			glyph:    positionedGlyph("a", matrix{0, 1, -1, 0, 100, 100}, 10, 0, 5),
			baseline: Line{Start: Point{X: 100, Y: 100}, End: Point{X: 100, Y: 105}},
			quad:     Quad{{X: 100, Y: 100}, {X: 100, Y: 105}, {X: 90, Y: 105}, {X: 90, Y: 100}},
		},
		{
			name:     "negative advance runs against the text axis",
			glyph:    positionedGlyph("a", matrix{1, 0, 0, 1, 0, 0}, 10, 0, -4),
			baseline: Line{Start: Point{}, End: Point{X: -4}},
			quad:     Quad{{}, {X: -4}, {X: -4, Y: 10}, {Y: 10}},
		},
		{
			name:     "vertical",
			glyph:    positionedVerticalGlyph("a", matrix{1, 0, 0, 1, 50, 700}, 10, -8),
			baseline: Line{Start: Point{X: 50, Y: 700}, End: Point{X: 50, Y: 692}},
			quad:     Quad{{X: 45, Y: 700}, {X: 45, Y: 692}, {X: 55, Y: 692}, {X: 55, Y: 700}},
		},
		{
			name:     "origin, advance and size only",
			glyph:    Glyph{Text: "ocr", X: 10, Y: 20, Advance: 40, Size: 12},
			baseline: Line{Start: Point{X: 10, Y: 20}, End: Point{X: 50, Y: 20}},
			quad:     Quad{{X: 10, Y: 20}, {X: 50, Y: 20}, {X: 50, Y: 32}, {X: 10, Y: 32}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.glyph.Baseline(); got != tc.baseline {
				t.Errorf("Baseline() = %+v, want %+v", got, tc.baseline)
			}
			if got := tc.glyph.Quad(); got != tc.quad {
				t.Errorf("Quad() = %+v, want %+v", got, tc.quad)
			}
		})
	}
	if got := positionedGlyph("a", matrix{1, 0, 0, 1, 0, 0}, 10, 0, -4).Advance; got != -4 {
		t.Errorf("negative advance not preserved: Advance = %v", got)
	}
}

// TestGlyphSize guards the struct against growing back: the glyph slice
// is the largest allocation of an extraction, and at 160 bytes it
// dominated both memory and GC time.
func TestGlyphSize(t *testing.T) {
	if size := unsafe.Sizeof(Glyph{}); size > 96 {
		t.Errorf("Glyph is %d bytes, want at most 96", size)
	}
}
