package chat

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
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
