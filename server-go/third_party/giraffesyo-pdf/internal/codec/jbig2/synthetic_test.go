package jbig2

import (
	"encoding/binary"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateSynthetic = flag.Bool("update", false, "rewrite the synthetic fixtures under testdata (then run testdata/gen.py to validate them)")

// synthCase is an encoder-built stream with its known lossless result.
type synthCase struct {
	name    string
	page    []byte
	globals []byte
	want    *bitmap // page-sized expected output
	// roundTripOnly marks streams using features the jbig2dec reference
	// decoder does not implement; they are checked by TestSynthetic but
	// not written as cross-validated fixtures.
	roundTripOnly bool
}

// syntheticCases builds the test streams described in encoder_test.go.
func syntheticCases() []synthCase {
	var cases []synthCase
	add := func(name string, want *bitmap, page, globals []byte) {
		cases = append(cases, synthCase{name: name, page: page, globals: globals, want: want})
	}
	addRoundTripOnly := func(name string, want *bitmap, page, globals []byte) {
		cases = append(cases, synthCase{name: name, page: page, globals: globals, want: want, roundTripOnly: true})
	}

	// Generic regions: every template, TPGDON on/off, nominal and
	// non-nominal AT pixels, on an image wide enough for 16-pixel
	// contexts and with an odd width so row padding matters.
	img := testImage(133, 61)
	for tmpl := range uint8(4) {
		for _, tpgdon := range []bool{false, true} {
			for _, nominal := range []bool{true, false} {
				p := &genericParams{template: tmpl, tpgdon: tpgdon, at: nominalAT[tmpl]}
				if !nominal {
					p.at = [4][2]int{{-5, -3}, {4, -2}, {-1, -4}, {6, -1}}
					if tmpl > 0 {
						p.at[0] = [2]int{-2, 0} // current-row AT, the other legal shape
					}
				}
				name := "synth-generic-t" + string(rune('0'+tmpl))
				if tpgdon {
					name += "-tpgdon"
				}
				if !nominal {
					name += "-at"
				}
				w := &segWriter{}
				w.segment(segPageInfo, nil, pageInfoData(img.w, img.h, false, 0, opOr), false)
				w.segment(segGenericLossless, nil, genericRegionData(img, 0, 0, opOr, p, false), false)
				w.segment(segEndOfPage, nil, nil, false)
				add(name, img, w.buf, nil)
			}
		}
	}

	// Tiny and odd sizes through the generic path.
	for _, sz := range [][2]int{{1, 1}, {8, 3}, {37, 13}, {9, 70}} {
		im := testImage(sz[0], sz[1])
		p := &genericParams{template: 0, tpgdon: true, at: nominalAT[0]}
		w := &segWriter{}
		w.segment(segPageInfo, nil, pageInfoData(im.w, im.h, false, 0, opOr), false)
		w.segment(segGenericImmediate, nil, genericRegionData(im, 0, 0, opOr, p, false), false)
		add("synth-size-"+itoa(sz[0])+"x"+itoa(sz[1]), im, w.buf, nil)
	}

	// Composition: page default pixel 1 and every operator, with regions
	// placed at unaligned offsets and partly outside the page.
	{
		pageW, pageH := 70, 40
		img2 := testImage(40, 25)
		for _, op := range []combOp{opOr, opAnd, opXor, opXnor, opReplace} {
			for _, def := range []int{0, 1} {
				want, _ := newBitmap(pageW, pageH)
				if def == 1 {
					want.fill(1)
				}
				p := &genericParams{template: 0, at: nominalAT[0]}
				w := &segWriter{}
				w.segment(segPageInfo, nil, pageInfoData(pageW, pageH, false, def, op), false)
				for i, pos := range [][2]int{{3, 2}, {45, 20}, {-0 + 60, 30}} {
					// A second region placed with replace keeps the test
					// of "op" meaningful even over black pages.
					rop := op
					if i == 2 {
						rop = opReplace
					}
					w.segment(segGenericLossless, nil, genericRegionData(img2, pos[0], pos[1], rop, p, false), false)
					want.compose(img2, pos[0], pos[1], rop)
				}
				add("synth-compose-op"+itoa(int(op))+"-def"+itoa(def), want, w.buf, nil)
			}
		}
	}

	// Unknown data length (7.2.7) with unknown region height, and an
	// unknown page height closed by end-of-stripe segments.
	{
		im := testImage(50, 30)
		p := &genericParams{template: 0, tpgdon: true, at: nominalAT[0]}
		w := &segWriter{}
		w.segment(segPageInfo, nil, pageInfoData(im.w, im.h, true, 0, opOr), false)
		w.segment(segGenericImmediate, nil, genericRegionData(im, 0, 0, opOr, p, true), true)
		w.segment(segEndOfStripe, nil, binary.BigEndian.AppendUint32(nil, 29), false)
		w.segment(segEndOfPage, nil, nil, false)
		w.segment(segEndOfFile, nil, nil, false)
		add("synth-unknown-length", im, w.buf, nil)
	}

	// Symbol dictionaries and text regions.
	var syms []*bitmap
	for i := range 12 {
		syms = append(syms, glyph(i, 5+i%4*3, 7+i/4*4))
	}
	// Sort by (h, w) to match dictionary coding order.
	for i := 1; i < len(syms); i++ {
		for j := i; j > 0 && (syms[j-1].h > syms[j].h || (syms[j-1].h == syms[j].h && syms[j-1].w > syms[j].w)); j-- {
			syms[j-1], syms[j] = syms[j], syms[j-1]
		}
	}
	layout := func(n int, transposed bool) []textInstance {
		var inst []textInstance
		x, y := 4, 3
		for i := range n {
			id := (i*7 + 3) % len(syms)
			inst = append(inst, textInstance{id: id, x: x, y: y})
			if !transposed {
				x += syms[id].w + 1 + i%3
				if x > 150 {
					x, y = 4+i%5, y+20
				}
			} else {
				y += syms[id].h + 1 + i%3
				if y > 150 {
					x, y = x+20, 3+i%5
				}
			}
		}
		return inst
	}
	render := func(w, h int, inst []textInstance, op combOp, def int) *bitmap {
		bm, _ := newBitmap(w, h)
		if def == 1 {
			bm.fill(1)
		}
		for _, in := range inst {
			bm.compose(syms[in.id], in.x, in.y, op)
		}
		return bm
	}
	for corner := range uint8(4) {
		for _, transposed := range []bool{false, true} {
			for _, logStrips := range []uint{0, 2} {
				pw, ph := 180, 110
				if transposed {
					pw, ph = 110, 180
				}
				inst := layout(60, transposed)
				spec := &textRegionSpec{w: pw, h: ph, syms: syms, inst: inst, logStrips: logStrips, refCorner: corner, transposed: transposed, combOp: opOr, dsOffset: -2 + int(corner)}
				sd := &symbolDictSpec{syms: syms, template: 0, at: nominalAT[0]}
				// Half the cases carry the dictionary in a globals stream.
				var globals []byte
				w := &segWriter{}
				g := &segWriter{}
				var dictNum uint32
				if logStrips == 0 {
					dictNum = g.segment(segSymbolDict, nil, symbolDictData(sd, make([]mqCx, 1<<16)), false)
					globals = g.buf
					w.next = 1
				} else {
					dictNum = w.segment(segSymbolDict, nil, symbolDictData(sd, make([]mqCx, 1<<16)), false)
				}
				w.segment(segPageInfo, nil, pageInfoData(pw, ph, false, 0, opOr), false)
				w.segment(segTextRegionLossless, []uint32{dictNum}, textRegionData(spec), false)
				name := "synth-text-corner" + itoa(int(corner)) + "-strips" + itoa(1<<logStrips)
				if transposed {
					name += "-transposed"
				}
				add(name, render(pw, ph, inst, opOr, 0), w.buf, globals)
			}
		}
	}

	// Text region features: SBDEFPIXEL=1 with AND, a text region placed
	// at an offset inside a larger page with an external XOR, a
	// refinement-enabled region whose instances all have RI = 0, seven
	// dictionaries (so the region uses the long referral form; seven
	// rather than five because jbig2dec sizes the retain-bit field as
	// floor((R+1)/8) bytes where the spec says ceil, and the two agree
	// at R = 7) coded with templates 1 and 2, the second importing and
	// re-exporting the first's symbols. The ctxReuse variant additionally retains the
	// first dictionary's coding contexts and reuses them in the second
	// (7.4.3.1.1 bits 8–9), which jbig2dec does not implement.
	for _, ctxReuse := range []bool{false, true} {
		// The region's symbol list is the concatenation of the exports
		// of the referred-to dictionaries, so d1's symbols appear twice.
		parts := [][]*bitmap{syms[:3], syms[3:5], syms[5:6], syms[6:7], syms[7:8], syms[8:10], syms[10:]}
		var all []*bitmap
		all = append(all, parts[0]...)
		all = append(all, parts[0]...)
		for _, p := range parts[1:] {
			all = append(all, p...)
		}
		inst := layout(40, false)
		for i := range inst {
			inst[i].id = (inst[i].id * 5) % len(all)
		}
		pw, ph := 200, 130
		spec := &textRegionSpec{w: 180, h: 110, x: 13, y: 9, op: opXor, syms: all, inst: inst, logStrips: 1, refCorner: refTopLeft, combOp: opAnd, defPixel: 1, dsOffset: 3, refine: true}
		region, _ := newBitmap(180, 110)
		region.fill(1)
		for _, in := range inst {
			region.compose(all[in.id], in.x, in.y, opAnd)
		}
		want, _ := newBitmap(pw, ph)
		want.compose(region, 13, 9, opXor)

		w := &segWriter{}
		cx := make([]mqCx, 1<<16)
		d1 := &symbolDictSpec{syms: parts[0], template: 2, at: nominalAT[2], ctxRetained: ctxReuse}
		n1 := w.segment(segSymbolDict, nil, symbolDictData(d1, cx), false)
		if !ctxReuse {
			cx = make([]mqCx, 1<<16)
		}
		d2 := &symbolDictSpec{syms: parts[1], numInput: len(parts[0]), exportInputs: true, template: 2, at: nominalAT[2], ctxUsed: ctxReuse}
		refs := []uint32{n1, w.segment(segSymbolDict, []uint32{n1}, symbolDictData(d2, cx), false)}
		for _, p := range parts[2:] {
			d := &symbolDictSpec{syms: p, template: 1, at: nominalAT[1]}
			refs = append(refs, w.segment(segSymbolDict, nil, symbolDictData(d, make([]mqCx, 1<<16)), false))
		}
		w.segment(segPageInfo, nil, pageInfoData(pw, ph, false, 0, opOr), false)
		w.segment(segTextRegionImmediate, refs, textRegionData(spec), false)
		if ctxReuse {
			addRoundTripOnly("synth-text-ctxreuse", want, w.buf, nil)
		} else {
			add("synth-text-features", want, w.buf, nil)
		}
	}
	return cases
}

func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// TestSynthetic round-trips every encoder-built stream through Decode.
// The same streams, once written with -update and validated against
// jbig2dec by testdata/gen.py, are also covered by TestFixtures.
func TestSynthetic(t *testing.T) {
	for _, c := range syntheticCases() {
		t.Run(c.name, func(t *testing.T) {
			got, err := Decode(c.page, c.globals, c.want.w, c.want.h)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if diff := firstDiff(got, c.want.data); diff >= 0 {
				t.Fatalf("bitmap differs from the encoded image at byte %d", diff)
			}
		})
	}
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// TestWriteSyntheticFixtures writes the synthetic streams and their
// source bitmaps to testdata when run with -update; gen.py then checks
// them against jbig2dec and installs the expected PBMs.
func TestWriteSyntheticFixtures(t *testing.T) {
	if !*updateSynthetic {
		t.Skip("run with -update to rewrite synthetic fixtures")
	}
	for _, c := range syntheticCases() {
		if c.roundTripOnly {
			continue
		}
		base := filepath.Join("testdata", c.name)
		if err := os.WriteFile(base+".jb2", c.page, 0o600); err != nil {
			t.Fatal(err)
		}
		if c.globals != nil {
			if err := os.WriteFile(base+".globals", c.globals, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(base+".src.pbm", pbmBytes(c.want), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
