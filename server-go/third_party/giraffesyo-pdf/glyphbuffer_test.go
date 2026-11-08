package pdf

import (
	"strconv"
	"testing"
)

func TestGlyphBufferAcrossChunks(t *testing.T) {
	var b glyphBuffer
	total := glyphChunk*3 + 17
	for i := range total {
		b.add(Glyph{Text: strconv.Itoa(i)})
	}
	if b.len() != total {
		t.Fatalf("len = %d, want %d", b.len(), total)
	}
	// tail straddling a chunk boundary
	tail := b.tail(glyphChunk*2 - 3)
	if len(tail) != total-(glyphChunk*2-3) || tail[0].Text != strconv.Itoa(glyphChunk*2-3) || tail[len(tail)-1].Text != strconv.Itoa(total-1) {
		t.Fatalf("tail = %d glyphs, first %q, last %q", len(tail), tail[0].Text, tail[len(tail)-1].Text)
	}
	if got := b.tail(total); got != nil {
		t.Fatalf("tail at end = %v, want nil", got)
	}
	// truncate across a boundary, then refill: the freed chunk is reused
	b.truncate(glyphChunk*2 - 3)
	if b.len() != glyphChunk*2-3 || len(b.chunks) != 2 || len(b.free) != 2 {
		t.Fatalf("after truncate: len %d chunks %d free %d", b.len(), len(b.chunks), len(b.free))
	}
	b.add(Glyph{Text: "x"})
	b.add(Glyph{Text: "y"})
	b.add(Glyph{Text: "z"})
	b.add(Glyph{Text: "w"}) // first glyph of a recycled chunk
	if len(b.chunks) != 3 || len(b.free) != 1 || b.len() != glyphChunk*2+1 {
		t.Fatalf("after refill: len %d chunks %d free %d", b.len(), len(b.chunks), len(b.free))
	}
	got := b.take()
	if len(got) != glyphChunk*2+1 || cap(got) != len(got) || got[len(got)-1].Text != "w" || got[glyphChunk*2-3].Text != "x" {
		t.Fatalf("take: len %d cap %d last %q", len(got), cap(got), got[len(got)-1].Text)
	}
	if b.len() != 0 || len(b.chunks) != 0 || len(b.free) != 4 {
		t.Fatalf("after take: len %d chunks %d free %d", b.len(), len(b.chunks), len(b.free))
	}
	if b.take() != nil {
		t.Fatal("empty take != nil")
	}
	b.truncate(0) // no-op on empty
}
