package kb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"

	"github.com/xuri/excelize/v2"
)

// exampleBook writes a workbook with what a budget sheet holds: a merged
// title, a styled header, dates, money, percents and thousands, formulas
// with their values, set widths, frozen rows, a chart, a second sheet and
// a hidden one.
func exampleBook(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.SetSheetName("Sheet1", "Summary"))
	_, err := f.NewSheet("Quarterly")
	must(err)
	_, err = f.NewSheet("Lookup")
	must(err)
	must(f.SetSheetVisible("Lookup", false))
	s := "Summary"
	must(f.MergeCell(s, "A1", "D1"))
	must(f.SetCellValue(s, "A1", "Example Co. budget"))
	title, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"DDEBF7"}}, Alignment: &excelize.Alignment{Horizontal: "center"}})
	must(err)
	must(f.SetCellStyle(s, "A1", "D1", title))
	head, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"305496"}}})
	must(err)
	must(f.SetSheetRow(s, "A2", &[]any{"Item", "Start", "Budget", "Used", "Units"}))
	must(f.SetCellStyle(s, "A2", "E2", head))
	date := "yyyy-mm-dd"
	money := `"$"#,##0.00`
	dateStyle, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &date})
	moneyStyle, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &money})
	pct, _ := f.NewStyle(&excelize.Style{NumFmt: 10})
	thousands, _ := f.NewStyle(&excelize.Style{NumFmt: 3})
	for i, item := range []string{"Compute", "Storage"} {
		r := i + 3
		must(f.SetCellValue(s, fmt.Sprintf("A%d", r), item))
		must(f.SetCellValue(s, fmt.Sprintf("B%d", r), 46147+i))
		must(f.SetCellStyle(s, fmt.Sprintf("B%d", r), fmt.Sprintf("B%d", r), dateStyle))
		must(f.SetCellValue(s, fmt.Sprintf("C%d", r), 44544.5+float64(i)))
		must(f.SetCellStyle(s, fmt.Sprintf("C%d", r), fmt.Sprintf("C%d", r), moneyStyle))
		must(f.SetCellValue(s, fmt.Sprintf("D%d", r), 0.5343))
		must(f.SetCellValue(s, fmt.Sprintf("F%d", r), 2))
		must(f.SetCellFormula(s, fmt.Sprintf("F%d", r), fmt.Sprintf("C%d/C5", r)))
		must(f.SetCellStyle(s, fmt.Sprintf("D%d", r), fmt.Sprintf("D%d", r), pct))
		must(f.SetCellValue(s, fmt.Sprintf("E%d", r), 1234567))
		must(f.SetCellStyle(s, fmt.Sprintf("E%d", r), fmt.Sprintf("E%d", r), thousands))
	}
	must(f.SetCellValue(s, "A6", true))
	must(f.SetColWidth(s, "A", "A", 22))
	must(f.SetRowHeight(s, 1, 30))
	must(f.SetPanes(s, &excelize.Panes{Freeze: true, YSplit: 2, TopLeftCell: "A3", ActivePane: "bottomLeft"}))
	must(f.AddChart(s, "G2", &excelize.Chart{Type: excelize.Col, Series: []excelize.ChartSeries{{Name: "Summary!$C$2", Categories: "Summary!$A$3:$A$4", Values: "Summary!$C$3:$C$4"}}}))
	must(f.SetCellValue("Quarterly", "A1", "Q1"))
	must(f.SetCellValue("Lookup", "A1", "secret-ish"))
	f.SetActiveSheet(0)
	p := filepath.Join(t.TempDir(), "budget.xlsx")
	must(f.SaveAs(p))
	return p
}

func cellAt(s *GridSheet, r, c int) []any {
	for _, row := range s.Cells {
		if row.R == r {
			for _, cell := range row.C {
				if cell[0] == c {
					return cell
				}
			}
		}
	}
	return nil
}

func TestASheetReadsAsTheFileFormatsIt(t *testing.T) {
	p := exampleBook(t)
	var g Grid
	s, err := g.Sheet(p, -1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Summary" || s.Index != 0 || len(s.Sheets) != 3 || !s.Sheets[2].Hidden || s.Sheets[1].Hidden {
		t.Fatalf("sheets: %+v %s %d", s.Sheets, s.Name, s.Index)
	}
	for _, c := range []struct {
		r, c       int
		text, kind string
		raw        string
	}{
		{2, 1, "2026-05-05", "d", ""},
		{2, 2, "$44,544.50", "n", "44544.5"},
		{2, 3, "53.43%", "n", "0.5343"},
		{2, 4, "1,234,567", "n", "1234567"},
		{5, 0, "TRUE", "b", "1"},
	} {
		got := cellAt(s, c.r, c.c)
		if got == nil || got[1] != c.text || at(got, 5) != c.kind || at(got, 4) != c.raw {
			t.Errorf("%d,%d: %v, want %q %q %q", c.r, c.c, got, c.text, c.kind, c.raw)
		}
	}
	if f := at(cellAt(s, 2, 5), 3); f != "=C3/C5" {
		t.Errorf("formula %q", f)
	}
	if s.FrozenRows != 2 || s.Charts != 1 {
		t.Errorf("frozen %d charts %d", s.FrozenRows, s.Charts)
	}
	if len(s.Merges) != 1 || s.Merges[0] != [4]int{0, 0, 0, 3} {
		t.Errorf("merges %v", s.Merges)
	}
	if s.ColWidths[0] != 159 || s.ColWidths[1] != 69 || s.RowHeights[0] != 40 {
		t.Errorf("widths %v heights %v", s.ColWidths, s.RowHeights)
	}
	title := s.Styles[cellAt(s, 0, 0)[2].(int)]
	if !title.Bold || title.Fill != "#ddebf7" || title.Align != "center" || title.Size != 14 {
		t.Errorf("title style %+v", title)
	}
	head := s.Styles[cellAt(s, 1, 0)[2].(int)]
	if !head.Bold || head.Color != "#ffffff" || head.Fill != "#305496" {
		t.Errorf("header style %+v", head)
	}
	// Another sheet by its number; a hidden one can still be asked for.
	q, err := g.Sheet(p, 1)
	if err != nil || q.Name != "Quarterly" || cellAt(q, 0, 0)[1] != "Q1" || q.Charts != 0 {
		t.Errorf("Quarterly: %v %+v", err, q)
	}
}

func at(cell []any, i int) string {
	if i >= len(cell) {
		return ""
	}
	s, _ := cell[i].(string)
	return s
}

// A sheet past the caps is cut to them and says how large it is.
func TestALargeSheetIsCutAndSaysSo(t *testing.T) {
	was := sheetMaxRows
	sheetMaxRows = 100
	t.Cleanup(func() { sheetMaxRows = was })
	f := excelize.NewFile()
	for r := 1; r <= 250; r++ {
		if err := f.SetSheetRow("Sheet1", fmt.Sprintf("A%d", r), &[]any{r, fmt.Sprintf("row %d", r)}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "log.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err := (&Grid{}).Sheet(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rows != 100 || s.TotalRows != 250 || len(s.Cells) != 100 || s.Cols != 2 {
		t.Errorf("rows %d of %d, %d read, %d cols", s.Rows, s.TotalRows, len(s.Cells), s.Cols)
	}
}

// A file that is not a workbook is said to be, not a server fault; other
// formats are not read into the grid.
func TestWhatIsNotAWorkbookIsRefused(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.xlsx")
	if err := writeFile(bad, "not a zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Grid{}).Sheet(bad, 0); err == nil || statusOf(err) != 422 {
		t.Errorf("bad: %v", err)
	}
	ods := filepath.Join(dir, "sheet.ods")
	_ = writeFile(ods, "x")
	if _, err := (&Grid{}).Sheet(ods, 0); err == nil || statusOf(err) != 415 {
		t.Errorf("ods: %v", err)
	}
}

func statusOf(err error) int {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func writeFile(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }
