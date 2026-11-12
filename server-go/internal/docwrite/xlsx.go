package docwrite

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Sheet is one worksheet's rows; a cell is a string, a number, a bool or
// nil.
type Sheet struct {
	Name string
	Rows [][]any
}

// Sheets reads the rows an .xlsx is written from, in the first form that
// fits: JSON ({"sheets":[{"name":..,"rows":[[..]]}]}, an array of rows, or
// an array of objects whose keys are the header), Markdown tables (one
// sheet each, named by the heading above it), or CSV/TSV text.
func Sheets(content string) ([]Sheet, error) {
	t := strings.TrimSpace(content)
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if s, err := jsonSheets(t); err == nil {
			return s, nil
		} else if strings.HasPrefix(t, "{") {
			return nil, err
		}
	}
	var sheets []Sheet
	heading := ""
	for _, b := range Parse(content) {
		switch b.Kind {
		case KHeading:
			heading = strings.TrimSpace(Plain(b.Runs))
		case KTable:
			var rows [][]any
			for _, r := range append([][][]Run{b.Header}, b.Rows...) {
				var row []any
				for _, c := range r {
					row = append(row, strings.TrimSpace(Plain(c)))
				}
				rows = append(rows, row)
			}
			sheets = append(sheets, Sheet{Name: heading, Rows: rows})
			heading = ""
		}
	}
	if len(sheets) > 0 {
		return sheets, nil
	}
	return csvSheet(content)
}

func csvSheet(content string) ([]Sheet, error) {
	first, _, _ := strings.Cut(content, "\n")
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\uFEFF")))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	switch {
	case strings.Count(first, "\t") > strings.Count(first, ","):
		r.Comma = '\t'
	case strings.Count(first, ";") > strings.Count(first, ","):
		r.Comma = ';'
	}
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading the rows as CSV: %v", err)
	}
	rows := make([][]any, len(recs))
	for i, rec := range recs {
		for _, v := range rec {
			rows[i] = append(rows[i], v)
		}
	}
	return []Sheet{{Rows: rows}}, nil
}

func jsonSheets(t string) ([]Sheet, error) {
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("reading the JSON: %v", err)
	}
	switch x := v.(type) {
	case map[string]any:
		raw, ok := x["sheets"].([]any)
		if !ok {
			return nil, fmt.Errorf(`JSON for a workbook is {"sheets":[{"name":"...","rows":[[...]]}]}`)
		}
		var out []Sheet
		for _, s := range raw {
			m, _ := s.(map[string]any)
			if m == nil {
				continue
			}
			name, _ := m["name"].(string)
			rows, err := rowsOf(m["rows"], m["columns"])
			if err != nil {
				return nil, err
			}
			out = append(out, Sheet{Name: name, Rows: rows})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("the JSON names no sheets")
		}
		return out, nil
	case []any:
		rows, err := rowsOf(x, nil)
		if err != nil {
			return nil, err
		}
		return []Sheet{{Rows: rows}}, nil
	}
	return nil, fmt.Errorf("the JSON is not rows")
}

// rowsOf takes rows as arrays, or as objects whose keys (in the order the
// first object gives them, or as columns lists them) become the header.
func rowsOf(v, columns any) ([][]any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("rows must be a list")
	}
	var keys []string
	if cs, ok := columns.([]any); ok {
		for _, c := range cs {
			keys = append(keys, fmt.Sprint(c))
		}
	}
	var out [][]any
	for _, r := range list {
		switch x := r.(type) {
		case []any:
			out = append(out, x)
		case map[string]any:
			if keys == nil {
				keys = sortedKeysLikeSource(x)
			}
			row := make([]any, len(keys))
			for i, k := range keys {
				row[i] = x[k]
			}
			out = append(out, row)
		default:
			out = append(out, []any{x})
		}
	}
	if len(keys) > 0 && len(out) > 0 {
		if _, objs := list[0].(map[string]any); objs {
			head := make([]any, len(keys))
			for i, k := range keys {
				head[i] = k
			}
			out = append([][]any{head}, out...)
		} else if columns != nil {
			head := make([]any, len(keys))
			for i, k := range keys {
				head[i] = k
			}
			out = append([][]any{head}, out...)
		}
	}
	return out, nil
}

// Go's maps lose the source order; the keys come back sorted, which is at
// least stable. A model wanting an order names it in "columns".
func sortedKeysLikeSource(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

var (
	plainNum   = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?$`)
	groupedNum = regexp.MustCompile(`^-?\d{1,3}(,\d{3})+(\.\d+)?$`)
	badSheet   = regexp.MustCompile(`[\[\]:*?/\\]`)
)

// cellValue types a value: numbers as numbers (not codes with leading
// zeros), the rest as text.
func cellValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case bool, float64:
		return x
	case string:
		s := strings.TrimSpace(x)
		if plainNum.MatchString(s) || groupedNum.MatchString(s) {
			if f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64); err == nil {
				return f
			}
		}
		return x
	}
	return fmt.Sprint(v)
}

func renderXLSX(content string) ([]byte, int, error) {
	sheets, err := Sheets(content)
	if err != nil {
		return nil, 0, err
	}
	f := excelize.NewFile()
	defer f.Close()
	used := map[string]bool{}
	head, _ := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		Fill:   excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F0F2F5"}},
		Border: []excelize.Border{{Type: "bottom", Color: "A0A0A0", Style: 1}},
	})
	for i, s := range sheets {
		name := strings.TrimSpace(badSheet.ReplaceAllString(s.Name, " "))
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		if utf8.RuneCountInString(name) > 31 {
			name = string([]rune(name)[:31])
		}
		base := name
		for n := 2; used[strings.ToLower(name)]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			name = string([]rune(base)[:min(len([]rune(base)), 31-len(suffix))]) + suffix
		}
		used[strings.ToLower(name)] = true
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return nil, 0, err
			}
		} else if _, err := f.NewSheet(name); err != nil {
			return nil, 0, err
		}
		cols := 0
		widths := map[int]int{}
		for r, row := range s.Rows {
			cols = max(cols, len(row))
			for c, v := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				if str, ok := v.(string); ok && strings.HasPrefix(str, "=") && len(str) > 1 {
					if err := f.SetCellFormula(name, cell, str[1:]); err != nil {
						return nil, 0, err
					}
					widths[c] = max(widths[c], 10)
					continue
				}
				val := cellValue(v)
				if err := f.SetCellValue(name, cell, val); err != nil {
					return nil, 0, err
				}
				widths[c] = max(widths[c], utf8.RuneCountInString(fmt.Sprint(val)))
			}
		}
		if cols == 0 {
			continue
		}
		// The first row is the header: bold, kept in view, filterable.
		last, _ := excelize.CoordinatesToCellName(cols, 1)
		_ = f.SetCellStyle(name, "A1", last, head)
		if len(s.Rows) > 1 {
			_ = f.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
			end, _ := excelize.CoordinatesToCellName(cols, len(s.Rows))
			_ = f.AutoFilter(name, "A1:"+end, nil)
		}
		for c := 0; c < cols; c++ {
			col, _ := excelize.ColumnNumberToName(c + 1)
			_ = f.SetColWidth(name, col, col, float64(min(max(widths[c]+2, 8), 60)))
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), len(sheets), nil
}
