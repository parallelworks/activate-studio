package kb

import (
	"archive/zip"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/xuri/excelize/v2"
)

// A workbook's sheet as the Studio's spreadsheet grid draws it, read from
// the file itself (no LibreOffice): each cell's value as the file formats
// it, its raw value and formula, the file's column widths, row heights,
// merged cells, frozen panes and the cheap parts of its styles.

// What one sheet read holds at most: a sheet past it is cut, and says so.
var (
	sheetMaxRows  = 10000
	sheetMaxCols  = 256
	sheetMaxCells = 300000
)

// sheetMaxBytes is the largest workbook read into a grid; a larger one is
// refused, and the print view or Download stays.
const sheetMaxBytes = 64 << 20

// GridSuffixes are the workbooks the grid reads.
var GridSuffixes = set(".xlsx", ".xlsm", ".xltx", ".xltm")

// GridSheetInfo names a sheet of the workbook.
type GridSheetInfo struct {
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"`
}

// GridStyle is a cell style the grid draws: only what differs from a
// plain cell.
type GridStyle struct {
	Bold      bool    `json:"b,omitempty"`
	Italic    bool    `json:"i,omitempty"`
	Underline bool    `json:"u,omitempty"`
	Strike    bool    `json:"s,omitempty"`
	Color     string  `json:"color,omitempty"`
	Fill      string  `json:"fill,omitempty"`
	Align     string  `json:"h,omitempty"`
	VAlign    string  `json:"v,omitempty"`
	Wrap      bool    `json:"wrap,omitempty"`
	Size      float64 `json:"size,omitempty"`
	Indent    int     `json:"indent,omitempty"`
}

// GridRow is one row's cells, each [column, text, style, formula, raw,
// kind] with what is empty at its end left off; style is an index into
// the sheet's styles, -1 for none, and kind is n (number), d (a date or
// time), b (true or false), e (an error) or empty (text).
type GridRow struct {
	R int     `json:"r"`
	C [][]any `json:"c"`
}

// GridSheet is one sheet of a workbook, as the grid draws it.
type GridSheet struct {
	Sheets     []GridSheetInfo `json:"sheets"`
	Index      int             `json:"index"`
	Name       string          `json:"name"`
	Rows       int             `json:"rows"`
	Cols       int             `json:"cols"`
	TotalRows  int             `json:"totalRows"`
	TotalCols  int             `json:"totalCols"`
	ColWidths  []int           `json:"colWidths"`
	RowHeights map[int]int     `json:"rowHeights,omitempty"`
	FrozenRows int             `json:"frozenRows,omitempty"`
	FrozenCols int             `json:"frozenCols,omitempty"`
	Merges     [][4]int        `json:"merges,omitempty"`
	Styles     []GridStyle     `json:"styles,omitempty"`
	Cells      []GridRow       `json:"cells"`
	Charts     int             `json:"charts,omitempty"`
	Pictures   int             `json:"pictures,omitempty"`
}

// Grid reads workbooks into sheets, keeping the last 12 sheets read by
// file, size, modification time and sheet.
type Grid struct {
	mu    sync.Mutex
	cache map[string]*GridSheet
	order []string
}

// Sheet reads sheet `want` of the workbook at abs (the active sheet when
// want is negative).
func (g *Grid) Sheet(abs string, want int) (*GridSheet, error) {
	if !GridSuffixes[strings.ToLower(ExtName(abs))] {
		return nil, Error(415, "Only Excel workbooks (.xlsx) are read into the grid here.")
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.Size() > sheetMaxBytes {
		return nil, Error(413, "This workbook is larger than %d MB, too large to read into the grid; use Print view or Download.", sheetMaxBytes>>20)
	}
	key := fmt.Sprintf("%s\x00%d\x00%v\x00%d", abs, st.Size(), mtimeMs(st), want)
	g.mu.Lock()
	hit := g.cache[key]
	g.mu.Unlock()
	if hit != nil {
		return hit, nil
	}
	out, err := readGridSheet(abs, want)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cache == nil {
		g.cache = map[string]*GridSheet{}
	}
	if _, ok := g.cache[key]; !ok {
		g.order = append(g.order, key)
	}
	g.cache[key] = out
	for len(g.order) > 12 {
		delete(g.cache, g.order[0])
		g.order = g.order[1:]
	}
	return out, nil
}

func readGridSheet(abs string, want int) (out *GridSheet, err error) {
	f, err := excelize.OpenFile(abs, excelize.Options{UnzipSizeLimit: 1 << 30, UnzipXMLSizeLimit: 256 << 20})
	if err != nil {
		if missing(err) {
			return nil, err
		}
		return nil, Error(422, "This file could not be read as a workbook: %v", err)
	}
	defer f.Close()
	// A damaged part of a workbook can panic deep in the reader; it is a
	// file that cannot be read, not a server failure.
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, Error(422, "This file could not be read as a workbook.")
		}
	}()
	names := f.GetSheetList()
	if len(names) == 0 {
		return nil, Error(422, "This workbook has no sheets.")
	}
	out = &GridSheet{Cells: []GridRow{}}
	for _, n := range names {
		vis, _ := f.GetSheetVisible(n)
		out.Sheets = append(out.Sheets, GridSheetInfo{Name: n, Hidden: !vis})
	}
	idx := want
	if idx < 0 || idx >= len(names) {
		idx = f.GetActiveSheetIndex()
		if idx < 0 || idx >= len(names) || out.Sheets[idx].Hidden {
			idx = 0
			for i, s := range out.Sheets {
				if !s.Hidden {
					idx = i
					break
				}
			}
		}
	}
	sheet := names[idx]
	out.Index, out.Name = idx, sheet

	// Values as the file formats them, up to the caps.
	rows, err := f.Rows(sheet)
	if err != nil {
		return nil, Error(422, "This sheet could not be read: %v", err)
	}
	type cell struct {
		c    int
		text string
	}
	var texts [][]cell
	r, cells := 0, 0
	for rows.Next() {
		r++
		if r > sheetMaxRows || cells >= sheetMaxCells {
			// Count the rest without reading it.
			continue
		}
		cols, err := rows.Columns()
		if err != nil {
			break
		}
		if len(cols) > out.TotalCols {
			out.TotalCols = len(cols)
		}
		var row []cell
		for c, v := range cols {
			if c >= sheetMaxCols {
				break
			}
			if v != "" {
				row = append(row, cell{c, v})
			}
		}
		cells += len(row)
		for len(texts) < r-1 {
			texts = append(texts, nil)
		}
		texts = append(texts, row)
	}
	_ = rows.Close()
	out.TotalRows = r
	// The sheet's own extent, where it says one, covers styled cells past
	// the last value too.
	if dim, err := f.GetSheetDimension(sheet); err == nil && dim != "" {
		if _, end, ok := strings.Cut(dim, ":"); ok {
			if c, rr, err := excelize.CellNameToCoordinates(end); err == nil {
				out.TotalCols = max(out.TotalCols, c)
				out.TotalRows = max(out.TotalRows, rr)
			}
		}
	}
	out.Rows = min(len(texts), sheetMaxRows)
	out.Cols = min(out.TotalCols, sheetMaxCols)

	// Merged cells, which also widen the extent to their ends.
	if merges, err := f.GetMergeCells(sheet, true); err == nil {
		for _, m := range merges {
			c1, r1, e1 := excelize.CellNameToCoordinates(m.GetStartAxis())
			c2, r2, e2 := excelize.CellNameToCoordinates(m.GetEndAxis())
			if e1 != nil || e2 != nil || r1 > sheetMaxRows || c1 > sheetMaxCols {
				continue
			}
			r2, c2 = min(r2, sheetMaxRows), min(c2, sheetMaxCols)
			out.Merges = append(out.Merges, [4]int{r1 - 1, c1 - 1, r2 - 1, c2 - 1})
			out.Rows, out.Cols = max(out.Rows, r2), max(out.Cols, c2)
		}
	}

	// Styles, formulas, raw values and kinds of the cells with a value.
	styleIdx, same := map[int]int{}, map[GridStyle]int{}
	dated := map[int]bool{}
	styleOf := func(id int) int {
		if id <= 0 {
			return -1
		}
		if i, ok := styleIdx[id]; ok {
			return i
		}
		i := -1
		if s, err := f.GetStyle(id); err == nil {
			dated[id] = dateFormat(s)
			if gs, ok := gridStyle(s); ok {
				if j, ok := same[gs]; ok {
					i = j
				} else {
					i = len(out.Styles)
					out.Styles = append(out.Styles, gs)
					same[gs] = i
				}
			}
		}
		styleIdx[id] = i
		return i
	}
	raw := excelize.Options{RawCellValue: true}
	for ri, row := range texts {
		if len(row) == 0 {
			continue
		}
		gr := GridRow{R: ri}
		for _, c := range row {
			ref, _ := excelize.CoordinatesToCellName(c.c+1, ri+1)
			sid, _ := f.GetCellStyle(sheet, ref)
			formula, _ := f.GetCellFormula(sheet, ref)
			rv, _ := f.GetCellValue(sheet, ref, raw)
			typ, _ := f.GetCellType(sheet, ref)
			kind := ""
			switch typ {
			case excelize.CellTypeBool:
				kind = "b"
			case excelize.CellTypeError:
				kind = "e"
			case excelize.CellTypeSharedString, excelize.CellTypeInlineString:
			default:
				if _, err := strconv.ParseFloat(rv, 64); err == nil {
					kind = "n"
				}
			}
			st := styleOf(sid)
			if kind == "n" && dated[sid] {
				// A date or time: the serial number behind it says nothing.
				kind, rv = "d", ""
			}
			if rv == c.text {
				rv = ""
			}
			if formula != "" {
				formula = "=" + formula
			}
			v := []any{c.c, c.text, st, formula, rv, kind}
			for len(v) > 2 && (v[len(v)-1] == "" || v[len(v)-1] == -1) {
				v = v[:len(v)-1]
			}
			gr.C = append(gr.C, v)
		}
		out.Cells = append(out.Cells, gr)
	}
	// A merge's top-left cell with no value still carries its style (a
	// filled title band), and so do the other cells of a styled header
	// row that are empty: those are left plain, which is cheap and reads
	// the same.

	// Column widths and hidden columns, in pixels as Excel draws them.
	out.ColWidths = make([]int, out.Cols)
	for c := 0; c < out.Cols; c++ {
		name, _ := excelize.ColumnNumberToName(c + 1)
		if vis, err := f.GetColVisible(sheet, name); err == nil && !vis {
			continue
		}
		w, err := f.GetColWidth(sheet, name)
		if err != nil || w <= 0 {
			w = 9.140625
		}
		out.ColWidths[c] = max(16, int(math.Round(w*7+5)))
	}
	// Row heights the file sets, and hidden rows.
	def := 15.0
	if p, err := f.GetSheetProps(sheet); err == nil && p.DefaultRowHeight != nil && *p.DefaultRowHeight > 0 {
		def = *p.DefaultRowHeight
	}
	for ri := 0; ri < out.Rows; ri++ {
		if vis, err := f.GetRowVisible(sheet, ri+1); err == nil && !vis {
			if out.RowHeights == nil {
				out.RowHeights = map[int]int{}
			}
			out.RowHeights[ri] = 0
			continue
		}
		h, err := f.GetRowHeight(sheet, ri+1)
		if err == nil && h > 0 && math.Abs(h-def) > 0.5 {
			if out.RowHeights == nil {
				out.RowHeights = map[int]int{}
			}
			out.RowHeights[ri] = int(math.Round(h * 4 / 3))
		}
	}
	if p, err := f.GetPanes(sheet); err == nil && p.Freeze {
		out.FrozenRows, out.FrozenCols = min(p.YSplit, out.Rows), min(p.XSplit, out.Cols)
	}
	out.Charts, out.Pictures = sheetDrawings(abs, idx)
	return out, nil
}

// gridStyle is the part of a cell style the grid draws, and whether any
// of it differs from a plain cell.
func gridStyle(s *excelize.Style) (GridStyle, bool) {
	var g GridStyle
	if ft := s.Font; ft != nil {
		g.Bold, g.Italic, g.Strike = ft.Bold, ft.Italic, ft.Strike
		g.Underline = ft.Underline != "" && ft.Underline != "none"
		g.Color = rgb(ft.Color)
		if ft.Size > 0 && math.Abs(ft.Size-11) > 0.1 {
			g.Size = ft.Size
		}
	}
	if s.Fill.Type == "pattern" && s.Fill.Pattern == 1 && len(s.Fill.Color) > 0 {
		g.Fill = rgb(s.Fill.Color[0])
	}
	if a := s.Alignment; a != nil {
		switch a.Horizontal {
		case "left", "center", "right", "justify":
			g.Align = a.Horizontal
		case "centerContinuous", "distributed":
			g.Align = "center"
		}
		switch a.Vertical {
		case "top", "center":
			g.VAlign = a.Vertical
		}
		g.Wrap = a.WrapText
		g.Indent = a.Indent
	}
	return g, g != GridStyle{}
}

// dateFormat reports whether a cell style shows its number as a date or
// a time: Excel's built-in date formats, or a format of its own with a
// day, month, year, hour or second outside quotes and brackets.
func dateFormat(s *excelize.Style) bool {
	switch n := s.NumFmt; {
	case n >= 14 && n <= 22, n >= 45 && n <= 47, n >= 27 && n <= 36, n >= 50 && n <= 58:
		return true
	}
	if s.CustomNumFmt == nil {
		return false
	}
	quoted, bracket := false, false
	for _, ch := range strings.ToLower(*s.CustomNumFmt) {
		switch {
		case ch == '"':
			quoted = !quoted
		case quoted:
		case ch == '[':
			bracket = true
		case ch == ']':
			bracket = false
		case bracket:
		case strings.ContainsRune("dmyhs", ch):
			return true
		}
	}
	return false
}

// rgb is an ARGB or RGB colour from a workbook as #rrggbb, or empty.
func rgb(c string) string {
	c = strings.TrimPrefix(c, "#")
	if len(c) == 8 {
		c = c[2:]
	}
	if len(c) != 6 {
		return ""
	}
	if _, err := strconv.ParseUint(c, 16, 32); err != nil {
		return ""
	}
	return "#" + strings.ToLower(c)
}

// sheetDrawings counts the charts and pictures drawn on sheet idx of the
// workbook at abs, which the grid does not draw: the print view does.
func sheetDrawings(abs string, idx int) (charts, pictures int) {
	z, err := zip.OpenReader(abs)
	if err != nil {
		return 0, 0
	}
	defer z.Close()
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	read := func(name string, v any) bool {
		f := files[name]
		if f == nil || f.UncompressedSize64 > 8<<20 {
			return false
		}
		rc, err := f.Open()
		if err != nil {
			return false
		}
		defer rc.Close()
		return xml.NewDecoder(io.LimitReader(rc, 8<<20)).Decode(v) == nil
	}
	type rel struct {
		ID     string `xml:"Id,attr"`
		Type   string `xml:"Type,attr"`
		Target string `xml:"Target,attr"`
	}
	rels := func(part string) []rel {
		var x struct {
			Rels []rel `xml:"Relationship"`
		}
		dir, base := path.Split(part)
		read(dir+"_rels/"+base+".rels", &x)
		for i, r := range x.Rels {
			if strings.HasPrefix(r.Target, "/") {
				x.Rels[i].Target = strings.TrimPrefix(r.Target, "/")
			} else {
				x.Rels[i].Target = path.Clean(dir + r.Target)
			}
		}
		return x.Rels
	}
	var wb struct {
		Sheets []struct {
			RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if !read("xl/workbook.xml", &wb) || idx >= len(wb.Sheets) {
		return 0, 0
	}
	sheetPart := ""
	for _, r := range rels("xl/workbook.xml") {
		if r.ID == wb.Sheets[idx].RID {
			sheetPart = r.Target
		}
	}
	for _, r := range rels(sheetPart) {
		if !strings.HasSuffix(r.Type, "/drawing") {
			continue
		}
		for _, d := range rels(r.Target) {
			switch {
			case strings.HasSuffix(d.Type, "/chart"):
				charts++
			case strings.HasSuffix(d.Type, "/image"):
				pictures++
			}
		}
	}
	return charts, pictures
}

// sheetGrid is GET /api/kb/sheet?path=&sheet=N: one sheet of a workbook
// for the grid, with the workbook's sheet names. Tagged with the file's
// version, so a browser holding it is answered 304.
func (m *Module) sheetGrid(w http.ResponseWriter, r *http.Request) error {
	_, _, abs, err := m.previewSource(r, true)
	if err != nil {
		return m.handled(w, r, err)
	}
	want := -1
	if s := qs(r, "sheet"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return Error(400, "sheet is a sheet's number, from 0")
		}
		want = n
	}
	if fresh(w, r, abs, "sheet"+strconv.Itoa(want)) {
		return nil
	}
	out, err := m.Grid.Sheet(abs, want)
	if err != nil {
		w.Header().Del("ETag")
		w.Header().Del("Cache-Control")
		return notFound(err)
	}
	body, err := marshal(out)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Add("Vary", "Accept-Encoding")
	// A long sheet is most of a megabyte of JSON, which compresses tenfold.
	if len(body) > 8<<10 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		_, _ = zw.Write(body)
		return zw.Close()
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(200)
	_, err = w.Write(body)
	return err
}
