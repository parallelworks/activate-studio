package xls

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpCorpus is the Go side of the local conformance run against xlrd
// and LibreOffice (see testdata/README.md). With XLS_DUMP_LIST naming a
// file of "id<TAB>path" lines and XLS_DUMP_OUT a directory, it writes
// <id>.go.json for each workbook: Text's output and error, and every
// cell with its row, column, text and kind. Panics are caught and
// recorded, since the point is to find them.
func TestDumpCorpus(t *testing.T) {
	list, out := os.Getenv("XLS_DUMP_LIST"), os.Getenv("XLS_DUMP_OUT")
	if list == "" || out == "" {
		t.Skip("XLS_DUMP_LIST and XLS_DUMP_OUT not set")
	}
	f, err := os.Open(list)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		id, path, _ := strings.Cut(sc.Text(), "\t")
		path, _, _ = strings.Cut(path, "\t")
		res := dumpOne(path)
		b, _ := json.Marshal(res)
		if err := os.WriteFile(filepath.Join(out, id+".go.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	t.Logf("dumped %d workbooks", n)
}

type dumpSheet struct {
	Name  string  `json:"name"`
	Cells [][]any `json:"cells"`
}

type dumpResult struct {
	Text   string      `json:"text"`
	Error  string      `json:"error,omitempty"`
	Panic  string      `json:"panic,omitempty"`
	Sheets []dumpSheet `json:"sheets"`
}

var kindNames = map[kind]string{
	kNumber: "number", kDate: "date", kText: "text", kBool: "bool", kError: "error",
	kFormulaNumber: "formula-number", kFormulaDate: "formula-date", kFormulaText: "formula-text",
	kFormulaBool: "formula-bool", kFormulaError: "formula-error",
}

func dumpOne(path string) (res dumpResult) {
	f, err := os.Open(path)
	if err != nil {
		res.Error = err.Error()
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	text, err := Text(f, st.Size(), 500_000) // enrich.py MAX_TEXT
	res.Text = text
	if err != nil {
		res.Error = err.Error()
	}
	// The cells again, without Text's recover, to see any panic.
	defer func() {
		if p := recover(); p != nil {
			res.Panic = fmt.Sprint(p)
		}
	}()
	stream, err := workbookStream(f, st.Size())
	if err != nil {
		return
	}
	bk, err := parseGlobals(stream)
	if err != nil {
		return
	}
	for _, sh := range bk.sheets {
		cells, ok := bk.sheetCells(stream, sh.pos)
		if !ok {
			continue
		}
		ds := dumpSheet{Name: sh.name}
		for _, c := range cells {
			if c.text != "" {
				ds.Cells = append(ds.Cells, []any{c.row, c.col, c.text, kindNames[c.kind]})
			}
		}
		res.Sheets = append(res.Sheets, ds)
	}
	return
}
