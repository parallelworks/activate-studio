package chat

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// A large file is written in parts: append=true adds text to the end of a
// text file, and rows to a workbook's sheet of the same name without its
// header again; a .pdf is refused with how to make it from parts.
func TestWriteKBFileInParts(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	run := func(args string) string {
		out, ok := Default.executeInGo(r, "write_kb_file", args, ToolCtx{})
		if !ok {
			t.Fatalf("%s: not run", args)
		}
		return out.Result
	}

	run(`{"path":"notes/log.md","content":"# Log\n\nfirst\n"}`)
	if got := run(`{"path":"notes/log.md","content":"second\n","append":true}`); !strings.Contains(got, "Added to notes/log.md") {
		t.Fatal(got)
	}
	if b, _ := os.ReadFile(filepath.Join(kb, "notes", "log.md")); string(b) != "# Log\n\nfirst\nsecond\n" {
		t.Fatalf("%q", b)
	}
	// Without append, an existing file is still refused unless overwritten.
	if got := run(`{"path":"notes/log.md","content":"x"}`); !strings.Contains(got, "append=true") {
		t.Fatal(got)
	}

	// The result says what each sheet holds, so the model can check it
	// against what was asked.
	if got := run(`{"path":"reports/items.xlsx","content":"{\"sheets\":[{\"name\":\"Items\",\"rows\":[[\"Name\",\"Count\"],[\"bolt\",4]]}]}"}`); !strings.Contains(got, "Items, 1 row after the header") || !strings.Contains(got, "send the next rows now with write_kb_file, the same path, append=true") {
		t.Fatal(got)
	}
	got := run(`{"path":"reports/items.xlsx","append":true,"content":"{\"sheets\":[{\"name\":\"Items\",\"rows\":[[\"Name\",\"Count\"],[\"nut\",\"12\"],[\"washer\",\"=1+1\"]]},{\"name\":\"Notes\",\"rows\":[[\"Note\"],[\"checked\"]]}]}"}`)
	if !strings.Contains(got, "4 rows") || !strings.Contains(got, "Items, 3 rows after the header; Notes, 1 row after the header") || !strings.Contains(got, "send the next part now the same way (append=true)") {
		t.Fatal(got)
	}
	f, err := excelize.OpenFile(filepath.Join(kb, "reports", "items.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, _ := f.GetRows("Items")
	if len(rows) != 4 || rows[0][0] != "Name" || rows[1][0] != "bolt" || rows[2][0] != "nut" {
		t.Errorf("Items %q", rows)
	}
	// A formula stays a formula, and a number a number, once the rows
	// are written again.
	if fm, _ := f.GetCellFormula("Items", "B4"); fm != "1+1" {
		t.Errorf("formula %q", fm)
	}
	if v, _ := f.GetCellType("Items", "B2"); v == excelize.CellTypeSharedString || v == excelize.CellTypeInlineString {
		t.Errorf("B2 is text")
	}
	if notes, _ := f.GetRows("Notes"); len(notes) != 2 {
		t.Errorf("Notes %q", notes)
	}

	if got := run(`{"path":"reports/r.pdf","content":"# R","append":true}`); !strings.Contains(got, "source") {
		t.Fatal(got)
	}
}

// What models send to write_kb_file: append as the text "true", a bare
// row, a workbook replaced in full (the result says what it held), and a
// write that fails (its row in the chat says why).
func TestWriteKBFileTakesWhatModelsSend(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	run := func(args string) *Outcome {
		out, ok := Default.executeInGo(r, "write_kb_file", args, ToolCtx{})
		if !ok {
			t.Fatalf("%s: not run", args)
		}
		return out
	}
	run(`{"path":"r/sales.xlsx","content":"{\"sheets\":[{\"name\":\"Sales\",\"rows\":[[\"Date\",\"Region\"],[\"2026-01-21\",\"North\"]]}]}"}`)
	if o := run(`{"path":"r/sales.xlsx","append":"true","content":"[\"2026-01-22\",\"South\"]"}`); !strings.Contains(o.Result, "Added to r/sales.xlsx") || !strings.Contains(o.Result, "Sales, 2 rows after the header") || !strings.Contains(o.Summary, "added to r/sales.xlsx: Sales, 2 rows after the header") {
		t.Fatalf("append as text with a bare row: %s", o.Result)
	}
	o := run(`{"path":"r/sales.xlsx","overwrite":true,"content":"{\"sheets\":[{\"name\":\"Sales\",\"rows\":[[\"Date\",\"Region\"]]}]}"}`)
	if !strings.Contains(o.Result, "which held Sales, 2 rows after the header") {
		t.Errorf("replace: %s", o.Result)
	}
	o = run(`{"path":"r/sales.xlsx","overwrite":true,"content":"{\"sheets\":[{\"name\":\"Sales\",\"rows\":[[\"a\"]]}]] x"}`)
	if !o.Failed || !strings.Contains(o.Summary, "reading the JSON") {
		t.Errorf("a failed write: %+v", o)
	}
}

// Parts appended to one workbook at once all land: one write to a file
// runs at a time.
func TestAppendsAtOnceAllLand(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	if out, ok := Default.executeInGo(r, "write_kb_file", `{"path":"r/items.xlsx","content":"{\"sheets\":[{\"name\":\"Items\",\"rows\":[[\"Name\"]]}]}"}`, ToolCtx{}); !ok || out.Failed {
		t.Fatal(out)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Default.executeInGo(r, "write_kb_file", `{"path":"r/items.xlsx","append":true,"content":"{\"sheets\":[{\"name\":\"Items\",\"rows\":[[\"item `+strconv.Itoa(i)+`\"]]}]}"}`, ToolCtx{})
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(kb, "r", "items.xlsx"))
	if h := docwrite.Held(b); len(h) != 1 || h[0] != "Items, 12 rows after the header" {
		t.Fatalf("%v", h)
	}
}

// A workbook asked for with a count of rows holds no more than that: the
// parts a model sends at once stop at the count, and each result says the
// rows past it were not written.
func TestPartsSentAtOnceStopAtTheRowsAsked(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	tctx := ToolCtx{RowsAsked: 250, RowsFile: "sales.xlsx"}
	rows := func(from, n int) string {
		var b strings.Builder
		b.WriteString(`[["Date","Units"]`)
		for i := from; i < from+n; i++ {
			b.WriteString(`,["2026-01-01",` + strconv.Itoa(i) + `]`)
		}
		return b.String() + "]"
	}
	run := func(args string) string {
		out, ok := Default.executeInGo(r, "write_kb_file", args, tctx)
		if !ok {
			t.Fatalf("%s: not run", args)
		}
		return out.Result
	}
	held := func() int {
		f, err := excelize.OpenFile(filepath.Join(kb, "reports", "sales.xlsx"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got, _ := f.GetRows(f.GetSheetList()[0])
		if last := got[len(got)-1][1]; len(got) > 1 && last != strconv.Itoa(len(got)-1) {
			t.Errorf("last row %q of %d", last, len(got))
		}
		return len(got) - 1
	}
	// A first part over the count is cut to it.
	got := run(`{"path":"reports/sales.xlsx","content":` + strconv.Quote(rows(1, 300)) + `}`)
	if held() != 250 || !strings.Contains(got, "the last 50 rows of this part were not written") {
		t.Fatalf("held %d: %s", held(), got)
	}
	// A replace and the parts after it, sent in one round, end at 250.
	run(`{"path":"reports/sales.xlsx","overwrite":true,"content":` + strconv.Quote(rows(1, 100)) + `}`)
	from := 101
	var results []string
	for _, n := range []int{50, 50, 49, 50, 50} {
		results = append(results, run(`{"path":"reports/sales.xlsx","append":true,"content":`+strconv.Quote(rows(from, n))+`}`))
		from += n
	}
	if held() != 250 || !strings.Contains(results[2], "send the next part") || !strings.Contains(results[3], "the last 49 rows of this part were not written") || !strings.Contains(results[3], "250 rows after the header") || !strings.Contains(results[4], "this part's 50 rows were not written") {
		t.Fatalf("held %d: %q", held(), results)
	}
	// The chat row says what was left out, and that the workbook is done.
	if out, _ := Default.executeInGo(r, "write_kb_file", `{"path":"reports/sales.xlsx","append":true,"content":`+strconv.Quote(rows(251, 20))+`}`, tctx); !strings.HasSuffix(out.Summary, "250 rows after the header; 20 rows past the 250 asked were not written, so the workbook is finished") {
		t.Errorf("summary %q", out.Summary)
	}
	// Another file, and a workbook of two sheets, are not limited.
	run(`{"path":"reports/other.xlsx","content":` + strconv.Quote(rows(1, 300)) + `}`)
	if f, err := excelize.OpenFile(filepath.Join(kb, "reports", "other.xlsx")); err != nil {
		t.Fatal(err)
	} else {
		got, _ := f.GetRows(f.GetSheetList()[0])
		f.Close()
		if len(got) != 301 {
			t.Errorf("other.xlsx held %d rows", len(got)-1)
		}
	}
}
