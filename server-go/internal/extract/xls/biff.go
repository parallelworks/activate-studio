package xls

import (
	"encoding/binary"
	"math"
	"unicode/utf16"
)

// BIFF record types read here ([MS-XLS] 2.3; the BIFF5 ids are the same).
const (
	recFormula   = 0x0006
	recEOF       = 0x000A
	recDatemode  = 0x0022
	recFilepass  = 0x002F
	recContinue  = 0x003C
	recCodepage  = 0x0042
	recBoundshet = 0x0085
	recMulRK     = 0x00BD
	recMulBlank  = 0x00BE
	recRString   = 0x00D6
	recXF        = 0x00E0
	recSST       = 0x00FC
	recLabelSST  = 0x00FD
	recBlank     = 0x0201
	recNumber    = 0x0203
	recLabel     = 0x0204
	recBoolErr   = 0x0205
	recFormula3  = 0x0206
	recString    = 0x0207
	recArray     = 0x0221
	recTable     = 0x0236
	recRK        = 0x027E
	recFormula4  = 0x0406
	recFormat    = 0x041E
	recShrFmla   = 0x04BC
	recBOF       = 0x0809
)

func le16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off:]) }
func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

// records walks a BIFF stream record by record. A record whose length
// runs past the end of the stream is cut at the end, and the walk stops
// after it.
type records struct {
	b   []byte
	pos int
}

func (r *records) next() (rc uint16, body []byte, ok bool) {
	if r.pos < 0 || r.pos+4 > len(r.b) {
		return 0, nil, false
	}
	rc = le16(r.b, r.pos)
	n := int(le16(r.b, r.pos+2))
	start := r.pos + 4
	end := start + n
	if end > len(r.b) {
		end = len(r.b)
	}
	r.pos = end
	if start+n > len(r.b) {
		r.pos = len(r.b)
	}
	return rc, r.b[start:end], true
}

func (r *records) peek() (uint16, bool) {
	if r.pos < 0 || r.pos+4 > len(r.b) {
		return 0, false
	}
	return le16(r.b, r.pos), true
}

// withContinues returns body followed by the bodies of the CONTINUE
// records right after it, consuming them.
func (r *records) withContinues(body []byte) [][]byte {
	segs := [][]byte{body}
	for {
		rc, ok := r.peek()
		if !ok || rc != recContinue {
			return segs
		}
		_, b, _ := r.next()
		segs = append(segs, b)
	}
}

// segments reads across a record and its CONTINUE records. Fixed-size
// fields run straight on from one segment into the next; character data
// that is split restarts with a one-byte flag whose bit 0 says whether
// the rest is UTF-16 or compressed ([MS-XLS] 2.5.293 and 2.4.58).
type segments struct {
	segs [][]byte
	i    int // current segment
	off  int // offset in it
}

func (s *segments) ok() bool { return s.i < len(s.segs) }

// fix moves past exhausted segments.
func (s *segments) fix() {
	for s.i < len(s.segs) && s.off >= len(s.segs[s.i]) {
		s.off -= len(s.segs[s.i])
		s.i++
	}
}

func (s *segments) bytes(n int) ([]byte, bool) {
	if n <= 0 {
		return nil, true
	}
	s.fix()
	if !s.ok() {
		return nil, false
	}
	if cur := s.segs[s.i]; s.off+n <= len(cur) {
		p := cur[s.off : s.off+n]
		s.off += n
		return p, true
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		s.fix()
		if !s.ok() {
			return out, false
		}
		cur := s.segs[s.i]
		take := min(n-len(out), len(cur)-s.off)
		out = append(out, cur[s.off:s.off+take]...)
		s.off += take
	}
	return out, true
}

func (s *segments) u8() (int, bool) {
	p, ok := s.bytes(1)
	if !ok {
		return 0, false
	}
	return int(p[0]), true
}

func (s *segments) u16() (int, bool) {
	p, ok := s.bytes(2)
	if !ok {
		return 0, false
	}
	return int(le16(p, 0)), true
}

func (s *segments) u32() (int, bool) {
	p, ok := s.bytes(4)
	if !ok {
		return 0, false
	}
	return int(le32(p, 0)), true
}

func (s *segments) skip(n int) bool {
	if n <= 0 {
		return true
	}
	for n > 0 {
		s.fix()
		if !s.ok() {
			return false
		}
		take := min(n, len(s.segs[s.i])-s.off)
		s.off += take
		n -= take
	}
	return true
}

// chars reads cch characters, compressed (one byte each, Latin-1) or
// UTF-16LE as wide says. When the characters run to the end of a
// segment the next one starts with its own flag byte, as xlrd reads it.
// On running out of data it returns what it has and false.
func (s *segments) chars(cch int, wide bool) (string, bool) {
	units := make([]uint16, 0, min(cch, 1<<16))
	for len(units) < cch {
		if !s.ok() {
			return string(utf16.Decode(units)), false
		}
		cur := s.segs[s.i]
		if s.off >= len(cur) {
			s.i++
			s.off = 0
			if !s.ok() || len(s.segs[s.i]) == 0 {
				return string(utf16.Decode(units)), false
			}
			wide = s.segs[s.i][0]&1 != 0
			s.off = 1
			continue
		}
		left := cch - len(units)
		if wide {
			n := min(left, (len(cur)-s.off)/2)
			if n == 0 {
				// A lone byte at the end of a segment cannot hold a
				// UTF-16 unit; skip it.
				s.off = len(cur)
				continue
			}
			for k := 0; k < n; k++ {
				units = append(units, le16(cur, s.off+2*k))
			}
			s.off += 2 * n
		} else {
			n := min(left, len(cur)-s.off)
			for _, c := range cur[s.off : s.off+n] {
				units = append(units, uint16(c))
			}
			s.off += n
		}
	}
	return string(utf16.Decode(units)), true
}

// unicode reads a BIFF8 XLUnicodeString or XLUnicodeRichExtendedString:
// a character count (lenBytes wide), a flag byte, the optional run count
// and extension size, the characters, then the runs and the extension,
// which it skips.
func (s *segments) unicode(lenBytes int) (string, bool) {
	var cch int
	var ok bool
	if lenBytes == 1 {
		cch, ok = s.u8()
	} else {
		cch, ok = s.u16()
	}
	if !ok {
		return "", false
	}
	flags, ok := s.u8()
	if !ok {
		return "", false
	}
	runs, ext := 0, 0
	if flags&0x08 != 0 {
		if runs, ok = s.u16(); !ok {
			return "", false
		}
	}
	if flags&0x04 != 0 {
		if ext, ok = s.u32(); !ok {
			return "", false
		}
	}
	str, ok := s.chars(cch, flags&0x01 != 0)
	if !ok {
		return str, false
	}
	if !s.skip(4*runs) || !s.skip(ext) {
		return str, false
	}
	return str, true
}

// unicodeAt reads a BIFF8 string from a single record body at off.
func unicodeAt(body []byte, off, lenBytes int) string {
	if off > len(body) {
		return ""
	}
	s := segments{segs: [][]byte{body[off:]}}
	str, _ := s.unicode(lenBytes)
	return str
}

// rk decodes an RK number ([MS-XLS] 2.5.217): bit 1 marks a signed 30-bit
// integer, otherwise the top 30 bits of a double; bit 0 divides by 100.
func rk(v uint32) float64 {
	var f float64
	if v&2 != 0 {
		f = float64(int32(v) >> 2)
	} else {
		f = math.Float64frombits(uint64(v&0xFFFFFFFC) << 32)
	}
	if v&1 != 0 {
		f /= 100
	}
	return f
}
