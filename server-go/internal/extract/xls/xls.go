// Package xls extracts the text of Excel 97-2003 workbooks (BIFF8, and
// the BIFF5 of Excel 5.0 and 95) for the indexer, in the layout of
// indexer/enrich.py's xlrd-based extract_xls: a "Sheet: <name>" line per
// worksheet, then its rows as tab-separated cells, trailing empty cells
// trimmed and empty rows skipped.
//
// Cells print as enrich.py prints them. Numbers are the integer when
// integral, else '%.15g'. A number whose cell format (XF to FORMAT) is a
// date or time format prints as an ISO date, with the time when it is not
// midnight, in the workbook's 1900 or 1904 date system. Booleans are TRUE
// and FALSE; errors and blanks are empty. Formula cells give their cached
// result: number, text (from the STRING record after the formula),
// boolean or error, including shared and array formulas.
//
// The reader is its own: the OLE container is read by
// github.com/richardlehane/mscfb and the BIFF records here. It does not
// evaluate formulas, so a cell shows the result the writing program saved.
package xls

import (
	"bytes"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding"
)

var (
	// ErrNotXLS means the input is not an OLE container with a BIFF5 or
	// BIFF8 workbook stream (nor a bare BIFF5/BIFF8 stream).
	ErrNotXLS = errors.New("xls: not an Excel 97-2003 workbook")
	// ErrEncrypted means the workbook is password protected (FILEPASS).
	ErrEncrypted = errors.New("xls: workbook is encrypted")
	// ErrCorrupt means the workbook could not be read.
	ErrCorrupt = errors.New("xls: corrupt workbook")
)

// maxStream bounds the workbook stream read into memory.
const maxStream = 1 << 30

// Text returns the workbook's text. It stops after the line that brings
// the text to maxText characters or more (counted as Unicode code
// points, one more for each line break), as enrich.py does; maxText <= 0
// means no limit. It never panics: on input it cannot read it returns ""
// and an error.
func Text(r io.ReaderAt, size int64, maxText int) (text string, err error) {
	defer func() {
		if recover() != nil {
			text, err = "", ErrCorrupt
		}
	}()
	stream, err := workbookStream(r, size)
	if err != nil {
		return "", err
	}
	return streamText(stream, maxText)
}

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// workbookStream returns the BIFF stream: the "Workbook" stream (BIFF8),
// else "Book" (BIFF5), of an OLE container, or the file itself when it
// is a bare BIFF5/BIFF8 stream starting with a BOF record.
func workbookStream(r io.ReaderAt, size int64) ([]byte, error) {
	head := make([]byte, 8)
	if n, _ := r.ReadAt(head, 0); n < 8 {
		return nil, ErrNotXLS
	}
	if !bytes.Equal(head, oleMagic) {
		if le16(head, 0) == recBOF && size <= maxStream {
			b := make([]byte, size)
			if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
				return nil, err
			}
			return b, nil
		}
		return nil, ErrNotXLS
	}
	doc, err := mscfb.New(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, ErrNotXLS
	}
	var book *mscfb.File
	for e, err := doc.Next(); err == nil; e, err = doc.Next() {
		if len(e.Path) != 0 {
			continue
		}
		if strings.EqualFold(e.Name, "Workbook") {
			book = e
			break
		}
		if book == nil && strings.EqualFold(e.Name, "Book") {
			book = e
		}
	}
	if book == nil {
		return nil, ErrNotXLS
	}
	// A stream cannot be larger than the file holding it.
	if book.Size < 0 || book.Size > size || book.Size > maxStream {
		return nil, ErrCorrupt
	}
	buf := bytes.NewBuffer(make([]byte, 0, book.Size))
	if _, err := io.Copy(buf, io.LimitReader(book, book.Size)); err != nil && buf.Len() == 0 {
		return nil, ErrCorrupt
	}
	return buf.Bytes(), nil
}

// book holds what the workbook globals say about the sheets.
type book struct {
	biff8    bool
	date1904 bool
	dec      *encoding.Decoder
	formats  map[uint16]string // FORMAT records by id
	xfDate   []bool            // per XF: is its format a date format
	sst      []string
	sheets   []sheetRef
}

type sheetRef struct {
	name string
	pos  int
}

// streamText parses the globals and then each worksheet, emitting lines.
func streamText(stream []byte, maxText int) (string, error) {
	bk, err := parseGlobals(stream)
	if err != nil {
		return "", err
	}
	var out []string
	size := 0
	for _, sh := range bk.sheets {
		cells, ok := bk.sheetCells(stream, sh.pos)
		if !ok {
			continue
		}
		out = append(out, "Sheet: "+sh.name)
		done := eachLine(cells, func(line string) bool {
			out = append(out, line)
			size += utf8.RuneCountInString(line) + 1
			return maxText <= 0 || size < maxText
		})
		if !done {
			break
		}
	}
	return strings.Join(out, "\n"), nil
}

func parseGlobals(stream []byte) (*book, error) {
	// The globals BOF is at 0; failing that, take the first BOF in the
	// stream. BOUNDSHEET offsets count from it.
	base := 0
	if len(stream) < 4 || le16(stream, 0) != recBOF {
		base = bytes.Index(stream, []byte{0x09, 0x08})
	}
	if base < 0 || base+8 > len(stream) {
		return nil, ErrNotXLS
	}
	recs := records{b: stream, pos: base}
	rc, bof, ok := recs.next()
	if !ok || rc != recBOF || len(bof) < 4 {
		return nil, ErrNotXLS
	}
	bk := &book{formats: map[uint16]string{}}
	switch le16(bof, 0) {
	case 0x0600:
		bk.biff8 = true
	case 0x0500:
	default:
		return nil, ErrNotXLS
	}
	if dt := le16(bof, 2); dt != 0x0005 {
		return nil, ErrNotXLS // not workbook globals
	}
	var xfFmt []uint16
	for {
		rc, body, ok := recs.next()
		if !ok || rc == recEOF {
			break
		}
		switch rc {
		case recFilepass:
			return nil, ErrEncrypted
		case recCodepage:
			if len(body) >= 2 && !bk.biff8 {
				bk.dec = decoder(le16(body, 0))
			}
		case recDatemode:
			if len(body) >= 2 {
				bk.date1904 = le16(body, 0) == 1
			}
		case recFormat:
			if len(body) < 3 {
				continue
			}
			id := le16(body, 0)
			if bk.biff8 {
				bk.formats[id] = unicodeAt(body, 2, 2)
			} else {
				bk.formats[id] = bk.string8(body, 2, 1)
			}
		case recXF:
			if len(body) >= 4 {
				xfFmt = append(xfFmt, le16(body, 2))
			}
		case recSST:
			bk.sst = parseSST(recs.withContinues(body))
		case recBoundshet:
			if len(body) < 7 {
				continue
			}
			pos := int(int32(le32(body, 0))) + base
			if body[5] != 0 { // 0 is a worksheet; skip charts, macros, VB
				continue
			}
			var name string
			if bk.biff8 {
				name = unicodeAt(body, 6, 1)
			} else {
				name = bk.string8(body, 6, 1)
			}
			bk.sheets = append(bk.sheets, sheetRef{name: name, pos: pos})
		}
	}
	bk.xfDate = make([]bool, len(xfFmt))
	for i, f := range xfFmt {
		if s, ok := bk.formats[f]; ok {
			bk.xfDate[i] = isDateFormat(s)
		} else {
			bk.xfDate[i] = builtinDate(f)
		}
	}
	return bk, nil
}

// string8 reads a BIFF5 byte string with a length of lenBytes at off.
func (bk *book) string8(body []byte, off, lenBytes int) string {
	if off+lenBytes > len(body) {
		return ""
	}
	n := int(body[off])
	if lenBytes == 2 {
		n = int(le16(body, off))
	}
	p := body[off+lenBytes:]
	if n < len(p) {
		p = p[:n]
	}
	return bk.decode8(p)
}

// parseSST reads the shared strings: total and unique counts, then the
// unique strings, which may run on into CONTINUE records.
func parseSST(segs [][]byte) []string {
	s := segments{segs: segs}
	if _, ok := s.u32(); !ok {
		return nil
	}
	n, ok := s.u32()
	if !ok {
		return nil
	}
	total := 0
	for _, g := range segs {
		total += len(g)
	}
	// Each string takes at least 3 bytes, which bounds a bad count.
	out := make([]string, 0, min(n, total/3+1))
	for i := 0; i < n; i++ {
		str, ok := s.unicode(2)
		if !ok {
			if str != "" {
				out = append(out, str)
			}
			break
		}
		out = append(out, str)
	}
	return out
}

// cell is one non-blank cell of a sheet, already rendered.
type cell struct {
	row, col int
	text     string
	kind     kind
}

// kind records what a cell came from, for the tests and the conformance
// report.
type kind uint8

const (
	kNumber kind = iota
	kDate
	kText
	kBool
	kError
	kFormulaNumber
	kFormulaDate
	kFormulaText
	kFormulaBool
	kFormulaError
)

func (bk *book) isDate(xf int) bool {
	return xf >= 0 && xf < len(bk.xfDate) && bk.xfDate[xf]
}

func (bk *book) number(v float64, xf int) (string, bool) {
	if bk.isDate(xf) {
		return formatDate(v, bk.date1904), true
	}
	return formatNumber(v), false
}

// sheetCells reads the worksheet substream at pos. It returns false when
// there is no worksheet BOF there.
func (bk *book) sheetCells(stream []byte, pos int) ([]cell, bool) {
	if pos < 0 || pos+4 > len(stream) {
		return nil, false
	}
	recs := records{b: stream, pos: pos}
	rc, _, ok := recs.next()
	if !ok || rc != recBOF {
		return nil, false
	}
	var cells []cell
	add := func(row, col int, text string, k kind) {
		cells = append(cells, cell{row: row, col: col, text: text, kind: k})
	}
	addNum := func(row, col, xf int, v float64, formula bool) {
		s, date := bk.number(v, xf)
		k := kNumber
		if date {
			k = kDate
		}
		if formula {
			k += kFormulaNumber
		}
		add(row, col, s, k)
	}
	// A FORMULA with a text result is followed by its STRING record, with
	// SHRFMLA, ARRAY or TABLE possibly in between.
	pendingRow, pendingCol := -1, -1
	depth := 0 // nested substreams (embedded charts)
	for {
		rc, body, ok := recs.next()
		if !ok {
			break
		}
		if depth > 0 {
			switch rc {
			case recBOF:
				depth++
			case recEOF:
				depth--
			}
			continue
		}
		if pendingRow >= 0 && rc != recString && rc != recShrFmla && rc != recArray && rc != recTable {
			add(pendingRow, pendingCol, "", kFormulaText)
			pendingRow = -1
		}
		switch rc {
		case recEOF:
			return cells, true
		case recBOF:
			depth++
		case recNumber:
			if len(body) >= 14 {
				addNum(int(le16(body, 0)), int(le16(body, 2)), int(le16(body, 4)),
					math.Float64frombits(uint64(le32(body, 6))|uint64(le32(body, 10))<<32), false)
			}
		case recRK:
			if len(body) >= 10 {
				addNum(int(le16(body, 0)), int(le16(body, 2)), int(le16(body, 4)), rk(le32(body, 6)), false)
			}
		case recMulRK:
			if len(body) < 6 {
				continue
			}
			row, col := int(le16(body, 0)), int(le16(body, 2))
			for off := 4; off+6 <= len(body); off += 6 {
				addNum(row, col, int(le16(body, off)), rk(le32(body, off+2)), false)
				col++
			}
		case recLabelSST:
			if len(body) >= 10 {
				i := int(le32(body, 6))
				s := ""
				if i < len(bk.sst) {
					s = bk.sst[i]
				}
				add(int(le16(body, 0)), int(le16(body, 2)), pyStrip(s), kText)
			}
		case recLabel, recRString:
			if len(body) < 8 {
				continue
			}
			var s string
			if bk.biff8 {
				s = unicodeAt(body, 6, 2)
			} else {
				s = bk.string8(body, 6, 2)
			}
			add(int(le16(body, 0)), int(le16(body, 2)), pyStrip(s), kText)
		case recBoolErr:
			if len(body) < 8 {
				continue
			}
			row, col := int(le16(body, 0)), int(le16(body, 2))
			if body[7] != 0 {
				add(row, col, "", kError)
			} else if body[6] != 0 {
				add(row, col, "TRUE", kBool)
			} else {
				add(row, col, "FALSE", kBool)
			}
		case recFormula, recFormula3, recFormula4:
			if len(body) < 14 {
				continue
			}
			row, col, xf := int(le16(body, 0)), int(le16(body, 2)), int(le16(body, 4))
			res := body[6:14]
			if le16(res, 6) != 0xFFFF {
				addNum(row, col, xf, math.Float64frombits(uint64(le32(res, 0))|uint64(le32(res, 4))<<32), true)
				continue
			}
			switch res[0] {
			case 0:
				pendingRow, pendingCol = row, col
			case 1:
				if res[2] != 0 {
					add(row, col, "TRUE", kFormulaBool)
				} else {
					add(row, col, "FALSE", kFormulaBool)
				}
			case 2:
				add(row, col, "", kFormulaError)
			default: // 3 is an empty string
				add(row, col, "", kFormulaText)
			}
		case recString:
			if pendingRow < 0 {
				continue
			}
			segs := recs.withContinues(body)
			var s string
			if bk.biff8 {
				g := segments{segs: segs}
				s, _ = g.unicode(2)
			} else {
				s = bk.string8(bytes.Join(segs, nil), 0, 2)
			}
			add(pendingRow, pendingCol, pyStrip(s), kFormulaText)
			pendingRow = -1
		}
	}
	return cells, true
}

// eachLine sorts the cells by row and column, keeps the last of any
// duplicates, and calls emit with each row that has text, as
// tab-separated cells up to its last non-empty one. It stops when emit
// returns false and reports whether it ran to the end.
func eachLine(cells []cell, emit func(string) bool) bool {
	if !slices.IsSortedFunc(cells, cmpCell) {
		slices.SortStableFunc(cells, cmpCell)
	}
	var row []string
	flush := func() bool {
		for len(row) > 0 && row[len(row)-1] == "" {
			row = row[:len(row)-1]
		}
		if len(row) == 0 {
			return true
		}
		ok := emit(strings.Join(row, "\t"))
		row = row[:0]
		return ok
	}
	cur := -1
	for i, c := range cells {
		if i+1 < len(cells) && cells[i+1].row == c.row && cells[i+1].col == c.col {
			continue // a later record for the same cell wins
		}
		if c.row != cur {
			if !flush() {
				return false
			}
			row, cur = row[:0], c.row
		}
		for len(row) < c.col {
			row = append(row, "")
		}
		row = append(row, c.text)
	}
	return flush()
}

func cmpCell(a, b cell) int {
	if a.row != b.row {
		return a.row - b.row
	}
	return a.col - b.col
}
