package ccitt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
)

var testdata = os.DirFS("testdata")

// fixture describes one entry of testdata/fixtures.json, produced by
// testdata/gen.py from libtiff's encoders.
type fixture struct {
	File      string `json:"file"`
	PBM       string `json:"pbm"` // empty for the page, see pagePattern
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	K         int    `json:"k"`
	ByteAlign bool   `json:"byteAlign"`

	data []byte // encoded stream
	want []byte // packed pixels, 1 = black, zero padding
}

func (f fixture) options() Options {
	return Options{K: f.K, Columns: f.Width, Rows: f.Height, EncodedByteAlign: f.ByteAlign, BlackIs1: true}
}

func (f fixture) stride() int { return (f.Width + 7) / 8 }

func loadFixtures(tb testing.TB) []fixture {
	tb.Helper()
	index, err := fs.ReadFile(testdata, "fixtures.json")
	if err != nil {
		tb.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(index, &fixtures); err != nil {
		tb.Fatal(err)
	}
	pbms := map[string][]byte{}
	for i := range fixtures {
		f := &fixtures[i]
		if f.data, err = fs.ReadFile(testdata, f.File); err != nil {
			tb.Fatal(err)
		}
		switch {
		case f.PBM != "":
			want, ok := pbms[f.PBM]
			if !ok {
				want = readPBM(tb, f.PBM, f.Width, f.Height)
				pbms[f.PBM] = want
			}
			f.want = want
		case strings.HasPrefix(f.File, "page."):
			f.want = pagePattern(f.Width, f.Height, 42)
		default:
			tb.Fatalf("%s: no expected pixels", f.File)
		}
	}
	return fixtures
}

func fixtureByName(tb testing.TB, name string) fixture {
	tb.Helper()
	for _, f := range loadFixtures(tb) {
		if f.File == name {
			return f
		}
	}
	tb.Fatalf("no fixture %s", name)
	return fixture{}
}

// readPBM parses a binary PBM (P4) whose rows are packed MSB first and
// padded to byte boundaries, exactly the decoder's output layout.
func readPBM(tb testing.TB, name string, width, height int) []byte {
	tb.Helper()
	raw, err := fs.ReadFile(testdata, name)
	if err != nil {
		tb.Fatal(err)
	}
	var w, h int
	header := fmt.Sprintf("P4\n%d %d\n", width, height)
	if _, err := fmt.Sscanf(string(raw), "P4\n%d %d\n", &w, &h); err != nil || w != width || h != height {
		tb.Fatalf("%s: unexpected PBM header", name)
	}
	pixels := raw[len(header):]
	if len(pixels) != (width+7)/8*height {
		tb.Fatalf("%s: %d pixel bytes, want %d", name, len(pixels), (width+7)/8*height)
	}
	return pixels
}

// pagePattern mirrors page_pattern in testdata/gen.py: a deterministic
// text-like page that is too large to commit as a PBM.
func pagePattern(w, h int, seed uint32) []byte {
	stride := (w + 7) / 8
	bm := make([]byte, stride*h)
	set := func(x, y int) {
		if x >= 0 && x < w && y >= 0 && y < h {
			bm[y*stride+x>>3] |= 0x80 >> (x & 7)
		}
	}
	rect := func(x, y, rw, rh int) {
		for yy := y; yy < y+rh; yy++ {
			for xx := x; xx < x+rw; xx++ {
				set(xx, yy)
			}
		}
	}
	s := seed
	next := func() int {
		s = (s*1103515245 + 12345) & 0x7FFFFFFF
		return int(s)
	}
	for range w * h / 1500 {
		x, y := next()%w, next()%h
		rw, rh := 1+next()%12, 1+next()%12
		rect(x, y, rw, rh)
	}
	for i := range h / 50 {
		rect(w/10, (i*50+7)%h, w*8/10, 1)
	}
	for t := range min(w, h) {
		set(t, t)
		set(w-1-t, t)
	}
	return bm
}

// invert returns the bitmap with black as 0 bits and zero padding, the
// decoder's BlackIs1=false layout.
func invert(bm []byte, width int) []byte {
	stride := (width + 7) / 8
	out := make([]byte, len(bm))
	for i, b := range bm {
		out[i] = ^b
		if width&7 != 0 && i%stride == stride-1 {
			out[i] &= 0xFF << (8 - uint(width&7))
		}
	}
	return out
}

func firstDiff(got, want []byte, stride int) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("first difference at row %d byte %d: got %08b want %08b", i/stride, i%stride, got[i], want[i])
		}
	}
	return fmt.Sprintf("lengths differ: got %d want %d", len(got), len(want))
}

func TestFixtures(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			for _, blackIs1 := range []bool{true, false} {
				o := f.options()
				o.BlackIs1 = blackIs1
				want := f.want
				if !blackIs1 {
					want = invert(want, f.Width)
				}
				got, rows, err := Decode(f.data, o)
				if err != nil {
					t.Fatalf("BlackIs1=%v: Decode: %v (after %d rows)", blackIs1, err, rows)
				}
				if rows != f.Height {
					t.Fatalf("BlackIs1=%v: rows = %d, want %d", blackIs1, rows, f.Height)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("BlackIs1=%v: bitmap mismatch: %s", blackIs1, firstDiff(got, want, f.stride()))
				}
			}
		})
	}
}

// TestFixturesUnknownRows decodes with Rows=0, relying on EOFB/RTC or the
// end of data to stop.
func TestFixturesUnknownRows(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			o := f.options()
			o.Rows = 0
			got, rows, err := Decode(f.data, o)
			if err != nil {
				t.Fatalf("Decode: %v (after %d rows)", err, rows)
			}
			if rows != f.Height {
				t.Fatalf("rows = %d, want %d", rows, f.Height)
			}
			if !bytes.Equal(got, f.want) {
				t.Fatalf("bitmap mismatch: %s", firstDiff(got, f.want, f.stride()))
			}
		})
	}
}

// TestFixturesFewerRows checks that a smaller Rows stops early and ignores
// the rest of the data.
func TestFixturesFewerRows(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if f.Height < 3 {
			continue
		}
		t.Run(f.File, func(t *testing.T) {
			o := f.options()
			o.Rows = f.Height / 2
			got, rows, err := Decode(f.data, o)
			if err != nil || rows != o.Rows {
				t.Fatalf("Decode: rows=%d err=%v, want %d rows and no error", rows, err, o.Rows)
			}
			if !bytes.Equal(got, f.want[:o.Rows*f.stride()]) {
				t.Fatalf("bitmap mismatch: %s", firstDiff(got, f.want, f.stride()))
			}
		})
	}
}

// TestFixturesMoreRows checks that asking for more rows than the data holds
// returns every row plus an unexpected-EOF error.
func TestFixturesMoreRows(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			o := f.options()
			o.Rows = f.Height + 5
			got, rows, err := Decode(f.data, o)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("Decode: rows=%d err=%v, want io.ErrUnexpectedEOF", rows, err)
			}
			if rows != f.Height || !bytes.Equal(got, f.want) {
				t.Fatalf("rows = %d, want %d with the full bitmap", rows, f.Height)
			}
		})
	}
}

// rowSpan is the bit range of one row's coded data (after any EOL and tag
// bit) as decoded from a fixture, used to re-pack rows in other framings.
type rowSpan struct {
	start, end int
	twoD       bool
}

// rowSpans replays the decoder to find where each row's data lies.
func rowSpans(t *testing.T, f fixture) []rowSpan {
	t.Helper()
	o := f.options()
	d := decoder{br: bitReader{data: f.data, nbits: len(f.data) * 8}, o: o, cols: o.Columns, stride: f.stride()}
	var spans []rowSpan
	for range f.Height {
		twoD, end, err := d.beginRow()
		if err != nil || end {
			t.Fatalf("beginRow: end=%v err=%v", end, err)
		}
		start := d.br.pos
		if twoD {
			err = d.decode2D()
		} else {
			err = d.decode1D()
		}
		if err != nil {
			t.Fatal(err)
		}
		spans = append(spans, rowSpan{start, d.br.pos, twoD})
		d.ref, d.cur = d.cur, d.ref[:0]
	}
	return spans
}

// bitWriter packs MSB-first bits.
type bitWriter struct {
	buf  []byte
	nbit int
}

func (w *bitWriter) writeBit(b uint32) {
	if w.nbit&7 == 0 {
		w.buf = append(w.buf, 0)
	}
	if b != 0 {
		w.buf[len(w.buf)-1] |= 0x80 >> (w.nbit & 7)
	}
	w.nbit++
}

func (w *bitWriter) writeBits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit(v >> uint(i) & 1)
	}
}

func (w *bitWriter) copyBits(src []byte, from, to int) {
	for i := from; i < to; i++ {
		w.writeBit(uint32(src[i>>3] >> (7 - uint(i&7)) & 1))
	}
}

func (w *bitWriter) align() {
	for w.nbit&7 != 0 {
		w.writeBit(0)
	}
}

// reframe rebuilds a fixture's rows with a different EOL/alignment framing:
// eol puts an EOL (plus the 1-D/2-D tag for K > 0) before every row, align
// starts every row (after its EOL, if any) on a byte boundary, and eofb
// appends an EOFB.
func reframe(t *testing.T, f fixture, eol, align, eofb bool) []byte {
	t.Helper()
	var w bitWriter
	for _, s := range rowSpans(t, f) {
		if eol {
			w.writeBits(1, 12)
		}
		if align {
			w.align()
		}
		if f.K > 0 {
			tag := uint32(1)
			if s.twoD {
				tag = 0
			}
			w.writeBit(tag)
		}
		w.copyBits(f.data, s.start, s.end)
	}
	if eofb {
		if align {
			// Aligned rows mean the EOFB starts on a byte boundary too.
			w.align()
		}
		w.writeBits(1, 12)
		w.writeBits(1, 12)
	}
	return w.buf
}

// TestReframed exercises the row-framing variants that libtiff does not
// produce: byte-aligned rows without EOLs (EncodedByteAlign for K < 0 and
// the no-EOL reading of it for K >= 0), mixed-mode tag bits without EOLs,
// EOLs before G4 rows, and streams with no EOFB.
func TestReframed(t *testing.T) {
	variants := []struct {
		name             string
		eol, align, eofb bool
	}{
		{"bare", false, false, false},
		{"bare+eofb", false, false, true},
		{"aligned", false, true, false},
		{"aligned+eofb", false, true, true},
		{"eol", true, false, false},
		{"eol+eofb", true, false, true},
	}
	for _, f := range loadFixtures(t) {
		if strings.HasPrefix(f.File, "page.") {
			continue
		}
		for _, v := range variants {
			t.Run(f.File+"/"+v.name, func(t *testing.T) {
				data := reframe(t, f, v.eol, v.align, v.eofb)
				for _, knownRows := range []bool{true, false} {
					o := f.options()
					o.EncodedByteAlign = v.align
					if !knownRows {
						o.Rows = 0
					}
					got, rows, err := Decode(data, o)
					if err != nil {
						t.Fatalf("Rows=%d: Decode: %v (after %d rows)", o.Rows, err, rows)
					}
					if rows != f.Height || !bytes.Equal(got, f.want) {
						t.Fatalf("Rows=%d: rows=%d, want %d; %s", o.Rows, rows, f.Height, firstDiff(got, f.want, f.stride()))
					}
				}
			})
		}
	}
}

// TestTruncated feeds every prefix of the small fixtures (and a sample of
// prefixes of the large ones): decoding must never panic, must return no
// more rows than the full stream holds, and the rows it does return must
// be the right ones.
func TestTruncated(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.File, func(t *testing.T) {
			step := 1
			if len(f.data) > 4096 {
				step = 997
			}
			for n := 0; n < len(f.data); n += step {
				for _, knownRows := range []bool{true, false} {
					o := f.options()
					if !knownRows {
						o.Rows = 0
					}
					got, rows, err := Decode(f.data[:n], o)
					if rows > f.Height || len(got) != rows*f.stride() {
						t.Fatalf("prefix %d: rows=%d len=%d", n, rows, len(got))
					}
					if !bytes.Equal(got, f.want[:len(got)]) {
						t.Fatalf("prefix %d: decoded rows differ: %s", n, firstDiff(got, f.want[:len(got)], f.stride()))
					}
					if knownRows && rows < f.Height && err == nil {
						t.Fatalf("prefix %d: expected an error for a truncated stream", n)
					}
				}
			}
		})
	}
}

func TestOptionsValidation(t *testing.T) {
	if _, _, err := Decode(nil, Options{Columns: -1}); err == nil {
		t.Error("negative Columns accepted")
	}
	if _, _, err := Decode(nil, Options{Columns: maxColumns + 1}); err == nil {
		t.Error("absurd Columns accepted")
	}
	if _, _, err := Decode(nil, Options{Rows: -1}); err == nil {
		t.Error("negative Rows accepted")
	}
	// Empty input with unknown rows is an empty image, not an error.
	bm, rows, err := Decode(nil, Options{})
	if err != nil || rows != 0 || len(bm) != 0 {
		t.Errorf("Decode(nil) = %d rows, %d bytes, %v", rows, len(bm), err)
	}
	// Default columns: a single V0 row at 1728 columns.
	bm, rows, err = Decode([]byte{0x80}, Options{K: -1, Rows: 1})
	if err != nil || rows != 1 || len(bm) != 216 {
		t.Errorf("Decode(V0) = %d rows, %d bytes, %v", rows, len(bm), err)
	}
}

// TestHugeRows checks that a hostile Rows on a tiny input returns quickly
// without allocating for the declared size.
func TestHugeRows(t *testing.T) {
	bm, rows, err := Decode([]byte{0x00}, Options{Rows: 1 << 30, Columns: 1728})
	if !errors.Is(err, io.ErrUnexpectedEOF) || rows != 0 || len(bm) != 0 {
		t.Fatalf("Decode = %d rows, %d bytes, %v", rows, len(bm), err)
	}
	// 80 valid G4 rows followed by a claim of a billion more.
	f := fixtureByName(t, "white.g4.ccitt")
	o := f.options()
	o.Rows = 1 << 30
	bm, rows, err = Decode(f.data, o)
	if !errors.Is(err, io.ErrUnexpectedEOF) || rows != f.Height {
		t.Fatalf("Decode = %d rows, %v", rows, err)
	}
	if cap(bm) > 1<<16 {
		t.Fatalf("capacity %d for %d bytes of output", cap(bm), len(bm))
	}
}

// TestMaxPixels checks the cap on unknown-row decoding.
func TestMaxPixels(t *testing.T) {
	f := fixtureByName(t, "white.g4.ccitt")
	o := f.options()
	o.Rows = 0
	o.MaxPixels = f.Width * 10
	bm, rows, err := Decode(f.data, o)
	if !errors.Is(err, errTooManyRows) || rows != 10 || len(bm) != 10*f.stride() {
		t.Fatalf("Decode = %d rows, %d bytes, %v", rows, len(bm), err)
	}
}

// TestRowOverflowClamps checks that a 1-D row whose runs exceed Columns
// is clamped rather than rejected.
func TestRowOverflowClamps(t *testing.T) {
	// white 3 ("1000") then black 4 ("011") on a 5-pixel row: 2 pixels over.
	var w bitWriter
	w.writeBits(0b1000, 4)
	w.writeBits(0b011, 3)
	bm, rows, err := Decode(w.buf, Options{Columns: 5, Rows: 1, BlackIs1: true})
	if err != nil || rows != 1 || len(bm) != 1 || bm[0] != 0b00011000 {
		t.Fatalf("Decode = %08b (%d rows), %v", bm, rows, err)
	}
}

// TestInvalidCode checks that garbage yields the rows before it plus an
// error that is not a truncation.
func TestInvalidCode(t *testing.T) {
	var w bitWriter
	w.writeBits(0b1000, 4) // white 3
	w.writeBits(0b10, 2)   // black 3
	w.writeBits(0b0111, 4) // white 2: row of 8 done
	w.writeBits(0, 8)      // invalid: eight zeros then ones is no white code
	w.writeBits(0xFF, 8)
	w.writeBits(0xFF, 8)
	bm, rows, err := Decode(w.buf, Options{Columns: 8, Rows: 2, BlackIs1: true})
	if !errors.Is(err, errBadCode) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want invalid code", err)
	}
	if rows != 1 || len(bm) != 1 || bm[0] != 0b00011100 {
		t.Fatalf("Decode = %08b (%d rows)", bm, rows)
	}
}

func FuzzDecode(f *testing.F) {
	for _, fx := range loadFixtures(f) {
		if len(fx.data) < 8192 {
			f.Add(fx.data, fx.K, fx.Width, fx.Height, fx.ByteAlign, true)
		}
	}
	f.Add([]byte{0x00, 0x10, 0x01}, 0, 8, 0, false, false)
	f.Fuzz(func(t *testing.T, data []byte, k, columns, rows int, align, black bool) {
		o := Options{
			K:                k,
			Columns:          columns % 4096,
			Rows:             rows % 4096,
			EncodedByteAlign: align,
			BlackIs1:         black,
			MaxPixels:        1 << 22,
		}
		bm, n, err := Decode(data, o)
		if o.Columns < 0 || o.Rows < 0 {
			if err == nil {
				t.Fatal("invalid options accepted")
			}
			return
		}
		cols := o.Columns
		if cols == 0 {
			cols = 1728
		}
		if len(bm) != n*((cols+7)/8) {
			t.Fatalf("len(bitmap)=%d rows=%d cols=%d", len(bm), n, cols)
		}
		if o.Rows > 0 && n > o.Rows {
			t.Fatalf("rows=%d > Rows=%d", n, o.Rows)
		}
		if o.Rows == 0 && n*cols > o.MaxPixels {
			t.Fatalf("rows=%d exceeds MaxPixels", n)
		}
	})
}

func BenchmarkDecode(b *testing.B) {
	var largest fixture
	for _, f := range loadFixtures(b) {
		if f.Width*f.Height > largest.Width*largest.Height && f.K < 0 {
			largest = f
		}
	}
	o := largest.options()
	b.SetBytes(int64(len(largest.data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, rows, err := Decode(largest.data, o); err != nil || rows != largest.Height {
			b.Fatal(err)
		}
	}
}
