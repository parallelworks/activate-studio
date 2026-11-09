package xls

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the testdata .txt files from the reader")

// TestFixtures reads each testdata workbook and compares it with its .txt.
// The .txt files were written by -update and then checked cell by cell
// against xlrd (enrich.py's extract_xls) and LibreOffice; see
// testdata/README.md for the run and the differences it found.
func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.xls")
	more, _ := filepath.Glob("testdata/poi/*.xls")
	files = append(files, more...)
	if len(files) < 9 {
		t.Fatalf("found only %d fixtures", len(files))
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			got := fileText(t, path)
			golden := strings.TrimSuffix(path, ".xls") + ".txt"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("text differs from %s (rerun with -update only after checking against the oracles)", golden)
			}
		})
	}
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Text(f, st.Size(), 0)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	return s
}

// TestFixtureCells pins the cases each fixture exists for, so that a
// regenerated .txt cannot silently lose them. Each entry must start a
// line.
func TestFixtureCells(t *testing.T) {
	cases := []struct {
		file  string
		lines []string
	}{
		// LibreOffice: text formula results from STRING records (one of
		// 9000 characters, split by CONTINUE), shared and array formulas,
		// boolean and error formulas, dates in custom formats.
		{"testdata/lo_cases.xls", []string{
			"Sheet: Formulas",
			"phase-one\t\t\t\t3\t30",
			"over budget",
			"FALSE",
			strings.Repeat("abc", 3000),
			"12\t24\tr12",
			"Sheet: Bools and errors",
			"TRUE\tFALSE",
			"yyyy-mm-dd\t2026-11-07",
			"yyyy-mm-dd hh:mm:ss\t2026-11-07 13:45:30",
			"hh:mm:ss\t06:30:00",
			"[$-409]mmmm d, yyyy;@\t2026-11-07",
			`0.00" days"` + "\t46333",
			"date formula\t2026-11-07 12:00:00",
			"yyyy-mm-dd\t1900-02-28",
		}},
		// The same workbook in the 1904 system.
		{"testdata/lo_cases_1904.xls", []string{"yyyy-mm-dd\t2026-11-07", "yyyy-mm-dd\t1900-03-01", "date formula\t2026-11-07 12:00:00"}},
		// xlwt: MULRK runs, SST strings past one record, 1904.
		{"testdata/xlwt_cases.xls", []string{
			"Sheet: Текст 文字",
			"1200\t1200\t3600\t\t1200",
			"date serial\t1900-02-28",
			"date serial\t1900-03-01",
			"emoji 🚀 here\t\tEMOJI 🚀 HERE",
		}},
		{"testdata/xlwt_cases_1904.xls", []string{"date\t2026-11-07", "date serial\t1904-03-01"}},
		// Excel, from Apache POI: a 1904 workbook, a BIFF5 (Excel 95)
		// workbook with a codepage, shared formulas, array and table
		// formulas.
		{"testdata/poi/1904DateWindowing.xls", []string{"2000-01-01\t2000-01-01"}},
		{"testdata/poi/testEXCEL_95.xls", []string{"Sheet: Feuil1", "\t15\t225\t\t1253.82", "Sheet: Feuil3"}},
	}
	for _, c := range cases {
		text := "\n" + fileText(t, c.file) + "\n"
		for _, l := range c.lines {
			if !strings.Contains(text, "\n"+l) {
				t.Errorf("%s: no line starting %q", c.file, l)
			}
		}
	}
}
