package jbig2

// A minimal JBIG2 encoder used only to build test streams for the paths
// the available real encoder (jbig2enc) never exercises: GB templates 1–3,
// non-nominal AT pixels, every REFCORNER/TRANSPOSED combination, strip
// sizes above 1, SBDSOFFSET, composition operators, page defaults, the
// unknown-length and long-referral header forms, and retained coding
// contexts. It follows T.88 Annex E (MQ encoder), Annex A (integer
// encoding) and the segment syntax of §7; its output is cross-checked
// against the jbig2dec reference decoder by testdata/gen.py, so the
// fixtures it produces are independent evidence, not a mirror of the
// decoder. Contexts are computed here pixel by pixel from the explicit
// bit layouts of 6.2.5.3 (Figures 4–7 / 8), deliberately not sharing the
// decoder's incremental window code.

import (
	"encoding/binary"
	"fmt"
	"math"
)

// mqEncoder implements the Annex E encoder (software conventions).
type mqEncoder struct {
	out   []byte
	a, c  uint32
	ct    int
	b     byte // pending byte B
	first bool // no byte emitted yet: B is the dummy byte before BPST
}

func newMQEncoder() *mqEncoder {
	return &mqEncoder{a: 0x8000, ct: 12, b: 0, first: true}
}

func (e *mqEncoder) emit() {
	if e.first {
		e.first = false
		return
	}
	e.out = append(e.out, e.b)
}

// byteOut is BYTEOUT (Figure E.8) with carry propagation into B.
func (e *mqEncoder) byteOut() {
	if e.b == 0xFF {
		e.stuff()
		return
	}
	if e.c >= 0x8000000 {
		e.b++
		if e.b == 0xFF {
			e.c &= 0x7FFFFFF
			e.stuff()
			return
		}
	}
	e.emit()
	e.b = byte(e.c >> 19 & 0xFF)
	e.c &= 0x7FFFF
	e.ct = 8
}

func (e *mqEncoder) stuff() {
	e.emit()
	e.b = byte(e.c >> 20 & 0xFF)
	e.c &= 0xFFFFF
	e.ct = 7
}

func (e *mqEncoder) renorm() {
	for {
		e.a <<= 1
		e.c <<= 1
		e.ct--
		if e.ct == 0 {
			e.byteOut()
		}
		if e.a&0x8000 != 0 {
			break
		}
	}
}

// encode codes decision d in context cx (CODELPS / CODEMPS, Figures E.6, E.7).
func (e *mqEncoder) encode(cx *mqCx, d uint32) {
	q := &qeTable[cx.i]
	qe := q.qe
	if d == uint32(cx.mps) {
		e.a -= qe
		if e.a&0x8000 == 0 {
			if e.a < qe {
				e.a = qe
			} else {
				e.c += qe
			}
			cx.i = q.nmps
			e.renorm()
		} else {
			e.c += qe
		}
		return
	}
	e.a -= qe
	if e.a < qe {
		e.c += qe
	} else {
		e.a = qe
	}
	if q.sw == 1 {
		cx.mps = 1 - cx.mps
	}
	cx.i = q.nlps
	e.renorm()
}

// flush is FLUSH (Figure E.11) followed by the 0xFF 0xAC terminator.
func (e *mqEncoder) flush() []byte {
	tempc := e.c + e.a
	e.c |= 0xFFFF
	if e.c >= tempc {
		e.c -= 0x8000
	}
	e.c <<= uint(e.ct)
	e.byteOut()
	e.c <<= uint(e.ct)
	e.byteOut()
	e.emit()
	if e.b != 0xFF {
		e.out = append(e.out, 0xFF)
	}
	e.out = append(e.out, 0xAC)
	return e.out
}

// encodeInt is the inverse of Annex A.2; oob encodes OOB.
func (e *mqEncoder) encodeInt(cx *intCtx, v int, oob bool) {
	prev := uint32(1)
	bit := func(b uint32) {
		e.encode(&cx[prev], b)
		if prev < 256 {
			prev = prev<<1 | b
		} else {
			prev = (((prev << 1) | b) & 511) | 256
		}
	}
	bits := func(val uint32, n int) {
		for i := n - 1; i >= 0; i-- {
			bit(val >> uint(i) & 1)
		}
	}
	if oob {
		bit(1)
		bit(0)
		bits(0, 2)
		return
	}
	s := uint32(0)
	if v < 0 {
		s = 1
		v = -v
	}
	val := u32(v)
	bit(s)
	switch {
	case val <= 3:
		bit(0)
		bits(val, 2)
	case val <= 19:
		bit(1)
		bit(0)
		bits(val-4, 4)
	case val <= 83:
		bit(1)
		bit(1)
		bit(0)
		bits(val-20, 6)
	case val <= 339:
		bit(1)
		bit(1)
		bit(1)
		bit(0)
		bits(val-84, 8)
	case val <= 4435:
		bit(1)
		bit(1)
		bit(1)
		bit(1)
		bit(0)
		bits(val-340, 12)
	default:
		bit(1)
		bit(1)
		bit(1)
		bit(1)
		bit(1)
		bits(val-4436, 32)
	}
}

// encodeIAID is the inverse of Annex A.3.
func (e *mqEncoder) encodeIAID(cx []mqCx, codeLen uint, id uint32) {
	prev := uint32(1)
	for i := int(codeLen) - 1; i >= 0; i-- {
		b := id >> uint(i) & 1
		e.encode(&cx[prev], b)
		prev = prev<<1 | b
	}
}

// genericContext computes CONTEXT for pixel (x, y) from the explicit bit
// layouts of 6.2.5.3: row-major, top row first, MSB first, with the AT
// pixels at their fixed bit positions.
func genericContext(bm *bitmap, p *genericParams, x, y int) uint32 {
	px := func(dx, dy int) uint32 { return bm.get(x+dx, y+dy) }
	at := func(i int) uint32 { return px(p.at[i][0], p.at[i][1]) }
	switch p.template {
	case 0:
		return at(3)<<15 | px(-1, -2)<<14 | px(0, -2)<<13 | px(1, -2)<<12 | at(2)<<11 |
			at(1)<<10 | px(-2, -1)<<9 | px(-1, -1)<<8 | px(0, -1)<<7 | px(1, -1)<<6 | px(2, -1)<<5 | at(0)<<4 |
			px(-4, 0)<<3 | px(-3, 0)<<2 | px(-2, 0)<<1 | px(-1, 0)
	case 1:
		return px(-1, -2)<<12 | px(0, -2)<<11 | px(1, -2)<<10 | px(2, -2)<<9 |
			px(-2, -1)<<8 | px(-1, -1)<<7 | px(0, -1)<<6 | px(1, -1)<<5 | px(2, -1)<<4 | at(0)<<3 |
			px(-3, 0)<<2 | px(-2, 0)<<1 | px(-1, 0)
	case 2:
		return px(-1, -2)<<9 | px(0, -2)<<8 | px(1, -2)<<7 |
			px(-2, -1)<<6 | px(-1, -1)<<5 | px(0, -1)<<4 | px(1, -1)<<3 | at(0)<<2 |
			px(-2, 0)<<1 | px(-1, 0)
	default:
		return px(-3, -1)<<9 | px(-2, -1)<<8 | px(-1, -1)<<7 | px(0, -1)<<6 | px(1, -1)<<5 | at(0)<<4 |
			px(-4, 0)<<3 | px(-3, 0)<<2 | px(-2, 0)<<1 | px(-1, 0)
	}
}

// encodeGeneric codes bm with the generic region procedure into e.
func encodeGeneric(e *mqEncoder, cx []mqCx, bm *bitmap, p *genericParams) {
	ltp := uint32(0)
	for y := range bm.h {
		if p.tpgdon {
			same := true
			for x := range bm.w {
				if bm.get(x, y) != bm.get(x, y-1) {
					same = false
					break
				}
			}
			cur := uint32(0)
			if same {
				cur = 1
			}
			e.encode(&cx[tpgdContext[p.template]], cur^ltp)
			ltp = cur
			if cur == 1 {
				continue
			}
		}
		for x := range bm.w {
			e.encode(&cx[genericContext(bm, p, x, y)], bm.get(x, y))
		}
	}
}

// segWriter assembles embedded-stream segments.
type segWriter struct {
	buf  []byte
	next uint32
}

// segment appends a segment header (7.2) and data and returns its number.
func (w *segWriter) segment(typ uint8, referred []uint32, data []byte, unknownLen bool) uint32 {
	num := w.next
	w.next++
	w.buf = binary.BigEndian.AppendUint32(w.buf, num)
	w.buf = append(w.buf, typ&0x3F)
	if len(referred) > 4 {
		w.buf = binary.BigEndian.AppendUint32(w.buf, 7<<29|u32(len(referred)))
		w.buf = append(w.buf, make([]byte, (len(referred)+8)/8)...)
	} else {
		w.buf = append(w.buf, u8(len(referred))<<5)
	}
	for _, r := range referred {
		switch {
		case num > 65536:
			w.buf = binary.BigEndian.AppendUint32(w.buf, r)
		case num > 256:
			w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(r&0xFFFF))
		default:
			w.buf = append(w.buf, byte(r&0xFF))
		}
	}
	w.buf = append(w.buf, 1) // page association
	if unknownLen {
		w.buf = binary.BigEndian.AppendUint32(w.buf, 0xFFFFFFFF)
	} else {
		w.buf = binary.BigEndian.AppendUint32(w.buf, u32(len(data)))
	}
	w.buf = append(w.buf, data...)
	return num
}

// pageInfoData builds a page information segment body (7.4.8). With
// unknownHeight the height is 0xFFFFFFFF and the page is striped with h
// as the maximum stripe size, as end-of-stripe segments then close it.
func pageInfoData(w, h int, unknownHeight bool, defPixel int, defOp combOp) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint32(b, u32(w))
	if unknownHeight {
		b = binary.BigEndian.AppendUint32(b, 0xFFFFFFFF)
	} else {
		b = binary.BigEndian.AppendUint32(b, u32(h))
	}
	b = binary.BigEndian.AppendUint32(b, 0)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, u8(defPixel<<2|int(defOp)<<3))
	if unknownHeight {
		return binary.BigEndian.AppendUint16(b, 0x8000|u16(h))
	}
	return binary.BigEndian.AppendUint16(b, 0)
}

func regionInfoData(w, h, x, y int, op combOp) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint32(b, u32(w))
	if h < 0 {
		b = binary.BigEndian.AppendUint32(b, 0xFFFFFFFF) // unknown height (7.2.7)
	} else {
		b = binary.BigEndian.AppendUint32(b, u32(h))
	}
	b = binary.BigEndian.AppendUint32(b, u32(x))
	b = binary.BigEndian.AppendUint32(b, u32(y))
	return append(b, byte(op))
}

// genericRegionData builds a generic region segment body (7.4.6).
func genericRegionData(bm *bitmap, x, y int, op combOp, p *genericParams, unknownHeight bool) []byte {
	h := bm.h
	if unknownHeight {
		h = -1
	}
	b := regionInfoData(bm.w, h, x, y, op)
	flags := p.template << 1
	if p.tpgdon {
		flags |= 8
	}
	b = append(b, flags)
	nAT := 1
	if p.template == 0 {
		nAT = 4
	}
	for i := range nAT {
		b = append(b, i8(p.at[i][0]), i8(p.at[i][1]))
	}
	e := newMQEncoder()
	encodeGeneric(e, make([]mqCx, 1<<16), bm, p)
	b = append(b, e.flush()...)
	if unknownHeight {
		b = binary.BigEndian.AppendUint32(b, u32(bm.h))
	}
	return b
}

// symbolDictData builds a symbol dictionary segment body (7.4.3) exporting
// the given new symbols (plus exportInputs of the input symbols); cx is
// the shared generic context set, which may come from a retained
// dictionary. Symbols are coded in height classes of ascending height
// and ascending width within a class.
type symbolDictSpec struct {
	syms         []*bitmap
	numInput     int
	exportInputs bool
	template     uint8
	at           [4][2]int
	ctxUsed      bool
	ctxRetained  bool
}

func symbolDictData(s *symbolDictSpec, cx []mqCx) []byte {
	flags := uint16(s.template) << 10
	if s.ctxUsed {
		flags |= 1 << 8
	}
	if s.ctxRetained {
		flags |= 1 << 9
	}
	var b []byte
	b = binary.BigEndian.AppendUint16(b, flags)
	nAT := 1
	if s.template == 0 {
		nAT = 4
	}
	for i := range nAT {
		b = append(b, i8(s.at[i][0]), i8(s.at[i][1]))
	}
	numEx := len(s.syms)
	if s.exportInputs {
		numEx += s.numInput
	}
	b = binary.BigEndian.AppendUint32(b, u32(numEx))
	b = binary.BigEndian.AppendUint32(b, u32(len(s.syms)))

	// Order: the new symbol numbering follows coding order, so callers
	// must supply syms already sorted by (height, width).
	e := newMQEncoder()
	var iadh, iadw, iaex intCtx
	gp := &genericParams{template: s.template, at: s.at}
	hc := 0
	for i := 0; i < len(s.syms); {
		h := s.syms[i].h
		e.encodeInt(&iadh, h-hc, false)
		hc = h
		w := 0
		for i < len(s.syms) && s.syms[i].h == h {
			e.encodeInt(&iadw, s.syms[i].w-w, false)
			w = s.syms[i].w
			encodeGeneric(e, cx, s.syms[i], gp)
			i++
		}
		e.encodeInt(&iadw, 0, true) // OOB ends the height class
	}
	// Export flags: runs alternate starting with non-exported.
	if s.exportInputs {
		e.encodeInt(&iaex, 0, false)
		e.encodeInt(&iaex, s.numInput+len(s.syms), false)
	} else {
		e.encodeInt(&iaex, s.numInput, false)
		e.encodeInt(&iaex, len(s.syms), false)
	}
	return append(b, e.flush()...)
}

// textInstance places symbol id with its top-left corner at (x, y).
type textInstance struct {
	id   int
	x, y int
}

type textRegionSpec struct {
	w, h       int
	x, y       int
	op         combOp // external combination operator
	syms       []*bitmap
	inst       []textInstance
	logStrips  uint
	refCorner  uint8
	transposed bool
	combOp     combOp
	defPixel   int
	dsOffset   int
	refine     bool // SBREFINE=1 with every RI = 0
}

// textRegionData builds a text region segment body (7.4.4) by the
// inverse of 6.4.5: instances are grouped into strips by their T
// coordinate and sorted by S within a strip.
func textRegionData(s *textRegionSpec) []byte {
	b := regionInfoData(s.w, s.h, s.x, s.y, s.op)
	flags := u16(int(s.logStrips))<<2 | uint16(s.refCorner)<<4 | uint16(s.combOp)<<7 | u16(s.defPixel)<<9 | u16(s.dsOffset&0x1F)<<10
	if s.transposed {
		flags |= 1 << 6
	}
	if s.refine {
		flags |= 1 << 1
	}
	b = binary.BigEndian.AppendUint16(b, flags)
	if s.refine {
		b = append(b, 0, 0, 0, 0) // refinement AT (template 0), unused for RI = 0
	}
	b = binary.BigEndian.AppendUint32(b, u32(len(s.inst)))

	strips := 1 << s.logStrips
	type placed struct {
		id         int
		s, t, span int // span: how far CURS advances (w-1 or h-1)
	}
	var ps []placed
	for _, in := range s.inst {
		sym := s.syms[in.id]
		var p placed
		p.id = in.id
		switch {
		case !s.transposed:
			p.s = in.x
			p.t = in.y
			if s.refCorner == refBottomLeft || s.refCorner == refBottomRight {
				p.t = in.y + sym.h - 1
			}
			p.span = sym.w - 1
		default:
			p.s = in.y
			p.t = in.x
			if s.refCorner == refTopRight || s.refCorner == refBottomRight {
				p.t = in.x + sym.w - 1
			}
			p.span = sym.h - 1
		}
		ps = append(ps, p)
	}
	floorDiv := func(a, n int) int {
		q := a / n
		if a%n != 0 && a < 0 {
			q--
		}
		return q
	}
	// Stable sort by (strip, s).
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0; j-- {
			a, bb := ps[j-1], ps[j]
			ka, kb := floorDiv(a.t, strips), floorDiv(bb.t, strips)
			if ka > kb || (ka == kb && a.s > bb.s) {
				ps[j-1], ps[j] = ps[j], ps[j-1]
			} else {
				break
			}
		}
	}

	e := newMQEncoder()
	codeLen := symCodeLen(len(s.syms))
	iaid := make([]mqCx, 1<<(codeLen+1))
	var iadt, iafs, iads, iait, iari intCtx
	e.encodeInt(&iadt, 0, false) // STRIPT = 0
	stript, firsts := 0, 0
	for i := 0; i < len(ps); {
		stripT := floorDiv(ps[i].t, strips) * strips
		e.encodeInt(&iadt, (stripT-stript)/strips, false)
		stript = stripT
		e.encodeInt(&iafs, ps[i].s-firsts, false)
		firsts = ps[i].s
		curs := firsts
		first := true
		for ; i < len(ps) && floorDiv(ps[i].t, strips)*strips == stripT; i++ {
			p := ps[i]
			if !first {
				e.encodeInt(&iads, p.s-curs-s.dsOffset, false)
			}
			first = false
			curs = p.s
			if strips > 1 {
				e.encodeInt(&iait, p.t-stript, false)
			}
			e.encodeIAID(iaid, codeLen, u32(p.id))
			if s.refine {
				e.encodeInt(&iari, 0, false)
			}
			curs += p.span
		}
		e.encodeInt(&iads, 0, true) // OOB: end of strip
	}
	return append(b, e.flush()...)
}

// testImage draws a deterministic picture with enough structure to
// exercise all context bits: a frame, diagonals, a filled disc, a
// checkerboard patch and a few repeated rows for TPGDON.
func testImage(w, h int) *bitmap {
	bm, err := newBitmap(w, h)
	if err != nil {
		panic(err)
	}
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < w && y < h {
			bm.data[y*bm.stride+x>>3] |= 0x80 >> uint(x&7)
		}
	}
	for x := range w {
		set(x, 0)
		set(x, h-1)
		set(x, x*h/max(w, 1))
	}
	for y := range h {
		set(0, y)
		set(w-1, y)
		set(w-1-y*w/max(h, 1), y)
	}
	cx, cy, r := w/3, h/2, min(w, h)/4
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				set(cx+x, cy+y)
			}
		}
	}
	for y := h / 4; y < h/2; y++ {
		for x := 2 * w / 3; x < w-2; x++ {
			if (x+y)%2 == 0 {
				set(x, y)
			}
		}
	}
	for y := 3 * h / 4; y < 3*h/4+3 && y < h; y++ {
		for x := 3; x < w/2; x += 2 {
			set(x, y)
		}
	}
	return bm
}

// glyph renders a small distinct symbol for index i.
func glyph(i, w, h int) *bitmap {
	bm, err := newBitmap(w, h)
	if err != nil {
		panic(err)
	}
	for y := range h {
		for x := range w {
			var on bool
			switch i % 4 {
			case 0:
				on = x == 0 || y == 0 || x == w-1 || y == h-1 || x == y
			case 1:
				on = (x+y+i)%3 == 0
			case 2:
				on = x*h < y*w
			default:
				on = (x-w/2)*(x-w/2)+(y-h/2)*(y-h/2) <= (min(w, h)/2)*(min(w, h)/2)
			}
			if on {
				bm.data[y*bm.stride+x>>3] |= 0x80 >> uint(x&7)
			}
		}
	}
	return bm
}

// Checked narrowing conversions for header fields; a test stream that
// overflows one is a bug in the test itself.
func u32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic(fmt.Sprintf("value %d does not fit in uint32", n))
	}
	return uint32(n)
}

func u16(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		panic(fmt.Sprintf("value %d does not fit in uint16", n))
	}
	return uint16(n)
}

func u8(n int) byte {
	if n < 0 || n > math.MaxUint8 {
		panic(fmt.Sprintf("value %d does not fit in a byte", n))
	}
	return byte(n)
}

// i8 encodes a signed AT coordinate as its two's complement byte.
func i8(n int) byte {
	if n < math.MinInt8 || n > math.MaxInt8 {
		panic(fmt.Sprintf("AT coordinate %d does not fit in int8", n))
	}
	return u8(n & 0xFF)
}

func pbmBytes(bm *bitmap) []byte {
	return append([]byte(fmt.Sprintf("P4\n%d %d\n", bm.w, bm.h)), bm.data...)
}
