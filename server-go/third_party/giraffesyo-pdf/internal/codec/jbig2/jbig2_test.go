package jbig2

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is one testdata stream with its jbig2dec-produced expectation.
type fixture struct {
	name    string
	page    []byte
	globals []byte
	w, h    int
	want    []byte // packed rows, 1 = black
}

// readPBM parses a binary PBM (P4) file, optionally gzipped.
func readPBM(tb testing.TB, path string) (w, h int, data []byte) {
	tb.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		tb.Fatal(err)
	}
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			tb.Fatal(err)
		}
		if b, err = io.ReadAll(zr); err != nil {
			tb.Fatal(err)
		}
	}
	var magic string
	n, err := fmt.Fscanf(bytes.NewReader(b), "%2s\n%d %d\n", &magic, &w, &h)
	if err != nil || n != 3 || magic != "P4" {
		tb.Fatalf("%s: not a P4 PBM", path)
	}
	hdr := len(fmt.Sprintf("P4\n%d %d\n", w, h))
	data = b[hdr:]
	if len(data) != (w+7)/8*h {
		tb.Fatalf("%s: %d bytes of pixels for %dx%d", path, len(data), w, h)
	}
	return w, h, data
}

// loadFixtures reads every NAME.jb2 under testdata with its optional
// NAME.globals and its expected NAME.pbm or NAME.pbm.gz.
func loadFixtures(tb testing.TB) []fixture {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.jb2"))
	if err != nil {
		tb.Fatal(err)
	}
	if len(paths) == 0 {
		tb.Fatal("no fixtures under testdata; run testdata/gen.py")
	}
	var out []fixture
	for _, p := range paths {
		base := strings.TrimSuffix(p, ".jb2")
		f := fixture{name: filepath.Base(base)}
		if f.page, err = os.ReadFile(filepath.Clean(p)); err != nil {
			tb.Fatal(err)
		}
		if g, err := os.ReadFile(filepath.Clean(base + ".globals")); err == nil {
			f.globals = g
		}
		exp := base + ".pbm"
		if _, err := os.Stat(exp); err != nil {
			exp += ".gz"
		}
		f.w, f.h, f.want = readPBM(tb, exp)
		out = append(out, f)
	}
	return out
}

// TestFixtures decodes every stream under testdata and compares it
// bit-exactly with the jbig2dec reference decoder's output.
func TestFixtures(t *testing.T) {
	for _, f := range loadFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			got, err := Decode(f.page, f.globals, f.w, f.h)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if diff := firstDiff(got, f.want); diff >= 0 {
				stride := (f.w + 7) / 8
				t.Fatalf("differs from reference at byte %d (row %d, col %d)", diff, diff/stride, diff%stride*8)
			}
		})
	}
}

// TestTruncated feeds every prefix of each fixture to Decode: no prefix
// may panic, and the result is either an error or a full-size page.
func TestTruncated(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if len(f.page) > 4096 {
			continue // the large pages would make this quadratic
		}
		t.Run(f.name, func(t *testing.T) {
			for n := range len(f.page) {
				got, err := Decode(f.page[:n], f.globals, f.w, f.h)
				if err == nil && len(got) != (f.w+7)/8*f.h {
					t.Fatalf("prefix %d: no error but %d bytes", n, len(got))
				}
			}
			if f.globals != nil {
				for n := range len(f.globals) {
					if _, err := Decode(f.page, f.globals[:n], f.w, f.h); err == nil && n < len(f.globals)-1 {
						// A truncated dictionary must not silently yield a page.
						t.Fatalf("globals prefix %d: no error", n)
					}
				}
			}
		})
	}
}

// unsupportedStream builds a stream with one segment of the given type
// and body after a page info segment.
func unsupportedStream(typ uint8, body []byte) []byte {
	w := &segWriter{}
	w.segment(segPageInfo, nil, pageInfoData(64, 64, false, 0, opOr), false)
	w.segment(typ, nil, body, false)
	return w.buf
}

func TestUnsupported(t *testing.T) {
	region := regionInfoData(64, 64, 0, 0, opOr)
	cases := map[string][]byte{
		"huffman symbol dictionary": unsupportedStream(segSymbolDict, append(binary.BigEndian.AppendUint16(nil, 1), make([]byte, 16)...)),
		"refagg symbol dictionary":  unsupportedStream(segSymbolDict, append(binary.BigEndian.AppendUint16(nil, 2), make([]byte, 16)...)),
		"huffman text region":       unsupportedStream(segTextRegionImmediate, append(append(region, 0, 1), make([]byte, 16)...)),
		"refinement region":         unsupportedStream(segRefinementImmediate, append(region, make([]byte, 16)...)),
		"halftone region":           unsupportedStream(segHalftoneImmediate, append(region, make([]byte, 16)...)),
		"pattern dictionary":        unsupportedStream(segPatternDict, make([]byte, 16)),
		"extended template":         unsupportedStream(segGenericImmediate, append(append(region, 0x10), make([]byte, 32)...)),
		"colour extension":          unsupportedStream(segGenericImmediate, append(regionInfoData(64, 64, 0, 0, opOr|8), make([]byte, 16)...)),
		"colour palette":            unsupportedStream(segColourPalette, nil),
	}
	// A refinement-enabled text region whose instance has RI = 1.
	{
		syms := []*bitmap{glyph(0, 8, 8), glyph(1, 8, 8)}
		w := &segWriter{}
		n := w.segment(segSymbolDict, nil, symbolDictData(&symbolDictSpec{syms: syms, at: nominalAT[0]}, make([]mqCx, 1<<16)), false)
		w.segment(segPageInfo, nil, pageInfoData(64, 64, false, 0, opOr), false)
		b := regionInfoData(64, 64, 0, 0, opOr)
		b = binary.BigEndian.AppendUint16(b, 1<<1|refTopLeft<<4)
		b = append(b, 0, 0, 0, 0)
		b = binary.BigEndian.AppendUint32(b, 1)
		e := newMQEncoder()
		var iadt, iafs, iari intCtx
		e.encodeInt(&iadt, 0, false)
		e.encodeInt(&iadt, 0, false)
		e.encodeInt(&iafs, 3, false)
		e.encodeIAID(make([]mqCx, 4), 1, 1)
		e.encodeInt(&iari, 1, false)
		w.segment(segTextRegionImmediate, []uint32{n}, append(b, e.flush()...), false)
		cases["refined text instance"] = w.buf
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(stream, nil, 64, 64)
			if !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("err = %v, want errors.ErrUnsupported", err)
			}
		})
	}
}

// TestHostile checks that oversized declarations are rejected up front
// rather than allocated or decoded.
func TestHostile(t *testing.T) {
	huge := regionInfoData(1<<20, 1<<20, 0, 0, opOr)
	cases := map[string]struct {
		data []byte
		w, h int
	}{
		"page too large":              {nil, 1 << 15, 1 << 15},
		"zero size":                   {nil, 0, 10},
		"huge generic region":         {unsupportedStream(segGenericImmediate, append(append(huge, 0), make([]byte, 8)...)), 64, 64},
		"huge text region":            {unsupportedStream(segTextRegionImmediate, append(append(huge, 0, 0), make([]byte, 8)...)), 64, 64},
		"huge symbol count":           {unsupportedStream(segSymbolDict, append(append(append(binary.BigEndian.AppendUint16(nil, 0), make([]byte, 8)...), 0xFF, 0xFF, 0xFF, 0xFF), 0xFF, 0xFF, 0xFF, 0xFF)), 64, 64},
		"segment length past end":     {[]byte{0, 0, 0, 0, 38, 0, 1, 0, 0, 1, 0, 0}, 64, 64},
		"unknown length unterminated": {[]byte{0, 0, 0, 0, 38, 0, 1, 0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 64, 0, 0, 0, 64, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2}, 64, 64},
		"text region without symbols": {unsupportedStream(segTextRegionImmediate, append(append(regionInfoData(8, 8, 0, 0, opOr), 0, 0), 0, 0, 0, 1)), 64, 64},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(c.data, nil, c.w, c.h); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	// Garbage after a valid page: every byte value as a trailing segment
	// header must fail cleanly.
	f := loadFixtures(t)[0]
	for b := range 256 {
		_, _ = Decode(append(bytes.Clone(f.page), byte(b), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0), f.globals, f.w, f.h)
	}
}

func FuzzDecode(f *testing.F) {
	for _, fx := range loadFixtures(f) {
		f.Add(fx.page, fx.globals, fx.w, fx.h)
	}
	f.Fuzz(func(t *testing.T, data, globals []byte, w, h int) {
		// Keep pages small so each input decodes quickly; the declared
		// region and symbol sizes inside data are still attacker-chosen.
		if w <= 0 || h <= 0 || w > 2048 || h > 2048 {
			t.Skip()
		}
		out, err := Decode(data, globals, w, h)
		if err == nil && len(out) != (w+7)/8*h {
			t.Fatalf("no error but %d bytes for %dx%d", len(out), w, h)
		}
	})
}

func BenchmarkDecode(b *testing.B) {
	for _, name := range []string{"page.gen-tpgdon", "symbols.sym", "text.gen"} {
		b.Run(name, func(b *testing.B) {
			var fx *fixture
			for _, f := range loadFixtures(b) {
				if f.name == name {
					fx = &f
					break
				}
			}
			if fx == nil {
				b.Skip("fixture missing")
			}
			b.SetBytes(int64((fx.w + 7) / 8 * fx.h))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Decode(fx.page, fx.globals, fx.w, fx.h); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
