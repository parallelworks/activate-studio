package xls

import (
	"bytes"
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// A small BIFF and OLE writer for the tests: enough to build workbooks
// with exactly the records, splits and flags a case needs, which no
// writer at hand produces on demand (STRING records split by CONTINUE,
// SST strings that switch between compressed and UTF-16 mid-string,
// BIFF5 in a codepage, embedded chart substreams).

func p16(v int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(v)) }
func p32(v int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(v)) }
func pf64(v float64) []byte {
	return binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func rec(rc int, parts ...[]byte) []byte {
	body := cat(parts...)
	return cat(p16(rc), p16(len(body)), body)
}

// wide reports whether s needs UTF-16 (any code unit over 0xFF).
func wide(s string) bool {
	for _, u := range utf16.Encode([]rune(s)) {
		if u > 0xFF {
			return true
		}
	}
	return false
}

func encChars(s string, w bool) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		if w {
			b = binary.LittleEndian.AppendUint16(b, u)
		} else {
			b = append(b, byte(u))
		}
	}
	return b
}

// ustr is a BIFF8 XLUnicodeString with a lenBytes-wide count.
func ustr(s string, lenBytes int) []byte {
	w := wide(s)
	n := len(utf16.Encode([]rune(s)))
	var hdr []byte
	if lenBytes == 1 {
		hdr = []byte{byte(n)}
	} else {
		hdr = p16(n)
	}
	flag := byte(0)
	if w {
		flag = 1
	}
	return cat(hdr, []byte{flag}, encChars(s, w))
}

// splitter writes a record body that is cut into CONTINUE records every
// limit bytes, with the flag byte restarting split character data.
type splitter struct {
	limit int
	recs  [][]byte
	cur   []byte
}

func (s *splitter) room() int { return s.limit - len(s.cur) }

func (s *splitter) next() {
	s.recs = append(s.recs, s.cur)
	s.cur = nil
}

// fixed appends bytes that may be cut anywhere (run lists, extensions).
func (s *splitter) fixed(b []byte) {
	for len(b) > 0 {
		if s.room() == 0 {
			s.next()
		}
		n := min(len(b), s.room())
		s.cur = append(s.cur, b[:n]...)
		b = b[n:]
	}
}

// header appends bytes that must stay in one record.
func (s *splitter) header(b []byte) {
	if s.room() < len(b) {
		s.next()
	}
	s.cur = append(s.cur, b...)
}

// chars appends character data; each continuation chooses its own width
// from flip: with flip set it alternates, else it keeps the string's.
func (s *splitter) chars(str string, w, flip bool) {
	units := utf16.Encode([]rune(str))
	first := true
	for len(units) > 0 {
		if !first {
			s.next()
			if flip {
				w = !w
			}
			for _, u := range units {
				if u > 0xFF {
					w = true
				}
			}
			f := byte(0)
			if w {
				f = 1
			}
			s.cur = append(s.cur, f)
		}
		first = false
		size := 1
		if w {
			size = 2
		}
		n := min(len(units), s.room()/size)
		for _, u := range units[:n] {
			if w {
				s.cur = binary.LittleEndian.AppendUint16(s.cur, u)
			} else {
				s.cur = append(s.cur, byte(u))
			}
		}
		units = units[n:]
	}
}

// records returns the first record with rc and the rest as CONTINUE.
func (s *splitter) records(rc int) []byte {
	if len(s.cur) > 0 || len(s.recs) == 0 {
		s.next()
	}
	out := rec(rc, s.recs[0])
	for _, r := range s.recs[1:] {
		out = append(out, rec(recContinue, r)...)
	}
	return out
}

type sstString struct {
	s         string
	runs      int  // rich-text runs to add
	ext       int  // extension bytes to add
	splitRuns bool // let a CONTINUE cut a 4-byte run, which Excel does not do
}

// sstRecords builds an SST and its CONTINUE records, cut every limit
// bytes, flipping the width of split character data when flip is set.
func sstRecords(strs []sstString, limit int, flip bool) []byte {
	sp := &splitter{limit: limit}
	sp.header(cat(p32(len(strs)), p32(len(strs))))
	for _, st := range strs {
		w := wide(st.s)
		flags := byte(0)
		if w {
			flags |= 1
		}
		hdr := p16(len(utf16.Encode([]rune(st.s))))
		var opt []byte
		if st.runs > 0 {
			flags |= 0x08
			opt = append(opt, p16(st.runs)...)
		}
		if st.ext > 0 {
			flags |= 0x04
			opt = append(opt, p32(st.ext)...)
		}
		sp.header(cat(hdr, []byte{flags}, opt))
		sp.chars(st.s, w, flip)
		for i := 0; i < st.runs; i++ {
			if st.splitRuns {
				sp.fixed([]byte{0xAB, 0xAB, 0xAB, 0xAB})
			} else {
				sp.header([]byte{0xAB, 0xAB, 0xAB, 0xAB})
			}
		}
		sp.fixed(bytes.Repeat([]byte{0xCD}, st.ext))
	}
	return sp.records(recSST)
}

// stringRecords is a BIFF8 STRING record for a formula's text result,
// cut into CONTINUE records every limit bytes.
func stringRecords(str string, limit int, flip bool) []byte {
	sp := &splitter{limit: limit}
	w := wide(str)
	f := byte(0)
	if w {
		f = 1
	}
	sp.header(cat(p16(len(utf16.Encode([]rune(str)))), []byte{f}))
	sp.chars(str, w, flip)
	return sp.records(recString)
}

func bof(biff8 bool, dt int) []byte {
	if biff8 {
		return rec(recBOF, p16(0x0600), p16(dt), p16(0x0DBB), p16(0x07CC), p32(0), p32(6))
	}
	return rec(recBOF, p16(0x0500), p16(dt), p16(0x0DBB), p16(0x07CC))
}

// xf is a 20-byte BIFF8 XF (BIFF5's is 16) with the given format id.
func xf(biff8 bool, fmtID int, style bool) []byte {
	typ := 0x0001 // locked
	if style {
		typ |= 0x0004 | 0xFFF0
	}
	n := 10
	if biff8 {
		n = 14
	}
	return rec(recXF, p16(0), p16(fmtID), p16(typ), make([]byte, n))
}

type sheetDef struct {
	name string
	kind int // BOUNDSHEET type: 0 worksheet, 2 chart
	recs []byte
}

type workbookDef struct {
	biff8    bool
	codepage int
	date1904 bool
	formats  [][2]any // id, format string
	xfFmts   []int    // cell XFs after the 16 style XFs, by format id
	sst      []byte   // SST and CONTINUE records, BIFF8
	extra    []byte   // more globals records
	sheets   []sheetDef
}

// cellXF is the XF index of the n-th entry of xfFmts.
func cellXF(n int) int { return 16 + n }

func (w workbookDef) name8(s string) []byte {
	if w.biff8 {
		return ustr(s, 1)
	}
	return cat([]byte{byte(len(s))}, []byte(s))
}

// stream lays out the globals and then each sheet substream, with the
// BOUNDSHEET offsets filled in.
func (w workbookDef) stream() []byte {
	globals := func(offsets []int) []byte {
		g := bof(w.biff8, 0x0005)
		if w.codepage != 0 {
			g = append(g, rec(recCodepage, p16(w.codepage))...)
		}
		d := 0
		if w.date1904 {
			d = 1
		}
		g = append(g, rec(recDatemode, p16(d))...)
		for _, f := range w.formats {
			id, s := f[0].(int), f[1].(string)
			if w.biff8 {
				g = append(g, rec(recFormat, p16(id), ustr(s, 2))...)
			} else {
				g = append(g, rec(recFormat, p16(id), []byte{byte(len(s))}, []byte(s))...)
			}
		}
		for i := 0; i < 16; i++ {
			g = append(g, xf(w.biff8, 0, true)...)
		}
		for _, id := range w.xfFmts {
			g = append(g, xf(w.biff8, id, false)...)
		}
		g = append(g, w.extra...)
		for i, sh := range w.sheets {
			g = append(g, rec(recBoundshet, p32(offsets[i]), []byte{0, byte(sh.kind)}, w.name8(sh.name))...)
		}
		g = append(g, w.sst...)
		return append(g, rec(recEOF)...)
	}
	offsets := make([]int, len(w.sheets))
	pos := len(globals(offsets))
	var body []byte
	for i, sh := range w.sheets {
		offsets[i] = pos + len(body)
		dt := 0x0010
		if sh.kind == 2 {
			dt = 0x0020
		}
		body = append(body, bof(w.biff8, dt)...)
		if sh.kind == 0 {
			// DIMENSIONS, which LibreOffice wants before the cells.
			if w.biff8 {
				body = append(body, rec(0x0200, p32(0), p32(256), p16(0), p16(256), p16(0))...)
			} else {
				body = append(body, rec(0x0200, p16(0), p16(256), p16(0), p16(256), p16(0))...)
			}
		}
		body = append(body, sh.recs...)
		body = append(body, rec(recEOF)...)
	}
	return append(globals(offsets), body...)
}

// file wraps the stream in an OLE container as "Workbook" (BIFF8) or
// "Book" (BIFF5).
func (w workbookDef) file() []byte {
	name := "Book"
	if w.biff8 {
		name = "Workbook"
	}
	return cfb(w.stream(), name)
}

// Cell records.
func number(r, c, x int, v float64) []byte { return rec(recNumber, p16(r), p16(c), p16(x), pf64(v)) }
func rkRec(r, c, x int, v uint32) []byte   { return rec(recRK, p16(r), p16(c), p16(x), p32(int(v))) }
func labelSST(r, c, x, i int) []byte       { return rec(recLabelSST, p16(r), p16(c), p16(x), p32(i)) }
func boolErr(r, c, v, isErr int) []byte {
	return rec(recBoolErr, p16(r), p16(c), p16(15), []byte{byte(v), byte(isErr)})
}

// formula is a FORMULA record with an 8-byte cached result and a dummy
// one-token expression (ptgInt 0).
func formula(r, c, x int, result []byte) []byte {
	return rec(recFormula, p16(r), p16(c), p16(x), result, p16(0), p32(0), p16(3), []byte{0x1E, 0, 0})
}

func fNum(v float64) []byte { return pf64(v) }

// fSpecial is a non-numeric result: 0 text (STRING follows), 1 boolean,
// 2 error, 3 empty text.
func fSpecial(typ, val int) []byte { return []byte{byte(typ), 0, byte(val), 0, 0, 0, 0xFF, 0xFF} }

// cfb writes a version 3 compound file holding one stream. The stream
// is padded to 4096 bytes so it lives in regular sectors and the file
// needs no mini stream.
func cfb(stream []byte, name string) []byte {
	const sec = 512
	data := append([]byte(nil), stream...)
	if len(data) < 4096 {
		data = append(data, make([]byte, 4096-len(data))...)
	}
	size := len(data)
	if pad := len(data) % sec; pad != 0 {
		data = append(data, make([]byte, sec-pad)...)
	}
	nData := len(data) / sec
	nFAT := 1
	for (nData+1+nFAT+127)/128 > nFAT {
		nFAT++
	}
	dirSec := nData
	const (
		endChain = 0xFFFFFFFE
		free     = 0xFFFFFFFF
		fatSect  = 0xFFFFFFFD
		noStream = 0xFFFFFFFF
	)
	fat := make([]uint32, nFAT*128)
	for i := range fat {
		fat[i] = free
	}
	for i := 0; i < nData; i++ {
		fat[i] = uint32(i + 1)
	}
	fat[nData-1] = endChain
	fat[dirSec] = endChain
	for i := 0; i < nFAT; i++ {
		fat[dirSec+1+i] = fatSect
	}
	hdr := make([]byte, sec)
	copy(hdr, oleMagic)
	binary.LittleEndian.PutUint16(hdr[24:], 0x003E)
	binary.LittleEndian.PutUint16(hdr[26:], 0x0003)
	binary.LittleEndian.PutUint16(hdr[28:], 0xFFFE)
	binary.LittleEndian.PutUint16(hdr[30:], 9)
	binary.LittleEndian.PutUint16(hdr[32:], 6)
	binary.LittleEndian.PutUint32(hdr[44:], uint32(nFAT))
	binary.LittleEndian.PutUint32(hdr[48:], uint32(dirSec))
	binary.LittleEndian.PutUint32(hdr[56:], 4096)
	binary.LittleEndian.PutUint32(hdr[60:], endChain)
	binary.LittleEndian.PutUint32(hdr[68:], endChain)
	for i := 0; i < 109; i++ {
		v := uint32(free)
		if i < nFAT {
			v = uint32(dirSec + 1 + i)
		}
		binary.LittleEndian.PutUint32(hdr[76+4*i:], v)
	}
	entry := func(name string, typ byte, child, start uint32, size int) []byte {
		e := make([]byte, 128)
		if name != "" {
			u := utf16.Encode([]rune(name))
			for i, c := range u {
				binary.LittleEndian.PutUint16(e[2*i:], c)
			}
			binary.LittleEndian.PutUint16(e[64:], uint16(2*len(u)+2))
		}
		e[66] = typ
		e[67] = 1 // black
		binary.LittleEndian.PutUint32(e[68:], noStream)
		binary.LittleEndian.PutUint32(e[72:], noStream)
		binary.LittleEndian.PutUint32(e[76:], child)
		binary.LittleEndian.PutUint32(e[116:], start)
		binary.LittleEndian.PutUint32(e[120:], uint32(size))
		return e
	}
	dir := cat(
		entry("Root Entry", 5, 1, endChain, 0),
		entry(name, 2, noStream, 0, size),
		entry("", 0, noStream, 0, 0),
		entry("", 0, noStream, 0, 0),
	)
	fatBytes := make([]byte, 0, 4*len(fat))
	for _, v := range fat {
		fatBytes = binary.LittleEndian.AppendUint32(fatBytes, v)
	}
	return cat(hdr, data, dir, fatBytes)
}
