package chat

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

func TestAlsoFormats(t *testing.T) {
	cases := []struct {
		in   any
		want string
		bad  bool
	}{
		{nil, "", false},
		{[]any{"pdf", ".DOCX", "pdf"}, "pdf|docx", false},
		{"pdf, word", "pdf|docx", false},
		{[]any{"pptx"}, "", true},
		{7, "", true},
	}
	for _, c := range cases {
		got, bad := alsoFormats(c.in)
		if strings.Join(got, "|") != c.want || (bad != "") != c.bad {
			t.Errorf("alsoFormats(%v) = %v %q", c.in, got, bad)
		}
	}
}

func TestApplyEdits(t *testing.T) {
	text := "# Report\n\nUtilization rose in each\nmonth of the quarter.\n\nThe queue was long. The queue was slow.\n"
	cases := []struct {
		name    string
		edits   []textEdit
		want    string // a substring of the new text
		changed int
		why     string // a substring of the refusal
	}{
		{"exact", []textEdit{{"# Report", "# Quarterly report", false}}, "# Quarterly report\n", 1, ""},
		{"spacing ignored", []textEdit{{"rose in each month of the quarter", "fell", false}}, "Utilization fell.\n", 1, ""},
		{"removed", []textEdit{{" The queue was slow.", "", false}}, "The queue was long.\n", 1, ""},
		{"all", []textEdit{{"The queue", "The line", true}}, "The line was long. The line was slow.", 2, ""},
		{"in order", []textEdit{{"long", "short", false}, {"was short", "is short", false}}, "The queue is short.", 2, ""},
		{"twice without all", []textEdit{{"The queue", "The line", false}}, "", 0, "2 times"},
		{"not there", []textEdit{{"# Report", "# R", false}, {"no such text", "x", false}}, "", 0, "Edit 2 of 2 did not apply"},
	}
	for _, c := range cases {
		got, changed, why := applyEdits(text, c.edits)
		if c.why != "" {
			if !strings.Contains(why, c.why) || !strings.Contains(why, "Nothing was changed") || got != "" {
				t.Errorf("%s: %q %q", c.name, got, why)
			}
			continue
		}
		if why != "" || changed != c.changed || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q changed %d, %q", c.name, got, changed, why)
		}
	}
	// A file with Windows line ends keeps them.
	got, n, why := applyEdits("one\r\ntwo\r\nthree\r\n", []textEdit{{"one\ntwo", "1\n2", false}})
	if got != "1\r\n2\r\nthree\r\n" || n != 1 || why != "" {
		t.Errorf("crlf: %q %d %q", got, n, why)
	}
}

// TestReportWrittenOnceThenEdited writes a report as Markdown with a PDF
// and a Word file made beside it, then edits the Markdown: the documents
// are made again, one that was replaced since is left alone, and a made
// document is not edited itself.
func TestReportWrittenOnceThenEdited(t *testing.T) {
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
	m := Default
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	read := func(p string) []byte { b, _ := os.ReadFile(filepath.Join(kb, p)); return b }
	call := func(tool string, args map[string]any) *Outcome {
		t.Helper()
		b, _ := json.Marshal(args)
		out, ok := m.executeInGo(r, tool, string(b), ToolCtx{})
		if !ok {
			t.Fatalf("%s is not run here", tool)
		}
		return out
	}
	report := "---\nalign: justify\n---\n# Site survey\n\nThe survey covered three rooms.\n\n| Room | Racks |\n|---|--:|\n| A | 12 |\n"

	out := call("write_kb_file", map[string]any{"path": "reports/survey.md", "content": report, "also": []any{"pdf", "docx"}})
	if out.Failed || !strings.Contains(out.Result, "made from it: reports/survey.pdf (a PDF of 1 page") || !strings.Contains(out.Result, "reports/survey.docx (a Word document") ||
		!strings.Contains(out.Result, "#open=file:reports/survey.docx") || !strings.Contains(out.Result, "edit_kb_file on reports/survey.md") {
		t.Fatalf("write with also: %+v", out)
	}
	pdf1, docx1 := read("reports/survey.pdf"), read("reports/survey.docx")
	if !bytes.HasPrefix(pdf1, []byte("%PDF")) || !bytes.HasPrefix(docx1, []byte("PK")) || string(read("reports/survey.md")) != report {
		t.Fatalf("files: pdf %d, docx %d bytes", len(pdf1), len(docx1))
	}
	if got := m.made.from("reports/survey.md"); strings.Join(got, "|") != "reports/survey.docx|reports/survey.pdf" {
		t.Errorf("made from the Markdown: %v", got)
	}
	// Nothing is written when one of the files is already there.
	out = call("write_kb_file", map[string]any{"path": "reports/survey2.md", "content": report, "also": []any{"pdf"}})
	os.WriteFile(filepath.Join(kb, "reports/taken.pdf"), []byte("x"), 0o644)
	out = call("write_kb_file", map[string]any{"path": "reports/taken.md", "content": report, "also": []any{"pdf"}})
	if !strings.Contains(out.Result, "reports/taken.pdf already exists") || read("reports/taken.md") != nil {
		t.Errorf("also over a file that is there: %+v", out)
	}
	if out = call("write_kb_file", map[string]any{"path": "reports/notes.txt", "content": "x", "also": []any{"pdf"}}); !strings.Contains(out.Result, "is not one") {
		t.Errorf("also beside a file that is not Markdown: %+v", out)
	}

	// An edit to the Markdown makes both documents again.
	out = call("edit_kb_file", map[string]any{"path": "reports/survey.md", "edits": []any{
		map[string]any{"find": "three rooms", "replace": "four rooms"},
		map[string]any{"find": "| A | 12 |", "replace": "| A | 12 |\n| B | 9 |"},
	}})
	if out.Failed || !strings.Contains(out.Result, "2 places changed") || !strings.Contains(out.Result, "Made again from it: reports/survey.docx (a Word document") ||
		!strings.Contains(out.Result, "reports/survey.pdf (a PDF of 1 page") || !strings.Contains(out.Result, "#open=file:reports/survey.pdf") {
		t.Fatalf("edit: %+v", out)
	}
	pdf2, docx2 := read("reports/survey.pdf"), read("reports/survey.docx")
	if bytes.Equal(pdf1, pdf2) || bytes.Equal(docx1, docx2) || !strings.Contains(string(read("reports/survey.md")), "four rooms.\n\n| Room | Racks |\n|---|--:|\n| A | 12 |\n| B | 9 |\n") {
		t.Errorf("the documents were not made again")
	}
	// A made document is not edited itself; the result names its source.
	if out = call("edit_kb_file", map[string]any{"path": "reports/survey.pdf", "find": "four", "replace": "five"}); !out.Failed || !strings.Contains(out.Result, "is made from reports/survey.md") {
		t.Errorf("edit of a made PDF: %+v", out)
	}
	// An edit that does not apply changes nothing.
	out = call("edit_kb_file", map[string]any{"path": "reports/survey.md", "edits": []any{
		map[string]any{"find": "four rooms", "replace": "five rooms"}, map[string]any{"find": "not in the file", "replace": "x"}}})
	if !out.Failed || !strings.Contains(out.Result, "Edit 2 of 2 did not apply") || !strings.Contains(string(read("reports/survey.md")), "four rooms") || !bytes.Equal(read("reports/survey.pdf"), pdf2) {
		t.Errorf("an edit that does not apply: %+v", out)
	}
	// A document replaced since it was made is left alone, once.
	os.WriteFile(filepath.Join(kb, "reports/survey.docx"), []byte("someone's own file"), 0o644)
	out = call("edit_kb_file", map[string]any{"path": "reports/survey.md", "append": "## Next steps\n\nOrder two racks."})
	if out.Failed || !strings.Contains(out.Result, "text added at the end") || !strings.Contains(out.Result, "Left as they are") || !strings.Contains(out.Result, "reports/survey.docx") ||
		string(read("reports/survey.docx")) != "someone's own file" || bytes.Equal(read("reports/survey.pdf"), pdf2) || !strings.HasSuffix(string(read("reports/survey.md")), "| B | 9 |\n## Next steps\n\nOrder two racks.\n") {
		t.Errorf("edit beside a replaced document: %+v", out)
	}
	if got := m.made.from("reports/survey.md"); strings.Join(got, "|") != "reports/survey.pdf" {
		t.Errorf("after the replaced document was left: %v", got)
	}

	// source makes a document from a Markdown file that is there, and an
	// edit to the Markdown makes it again wherever it is.
	out = call("write_kb_file", map[string]any{"path": "out/survey-landscape.pdf", "source": "reports/survey.md"})
	if out.Failed || !strings.Contains(out.Result, "from reports/survey.md") || !bytes.HasPrefix(read("out/survey-landscape.pdf"), []byte("%PDF")) {
		t.Fatalf("write from source: %+v", out)
	}
	before := read("out/survey-landscape.pdf")
	out = call("edit_kb_file", map[string]any{"path": "reports/survey.md", "find": "Order two racks.", "replace": "Order three racks."})
	if out.Failed || !strings.Contains(out.Result, "out/survey-landscape.pdf") || bytes.Equal(before, read("out/survey-landscape.pdf")) {
		t.Errorf("edit with a document elsewhere: %+v", out)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"path": "out/x.pdf", "source": "reports/nope.md"}, "is not in the library"},
		{map[string]any{"path": "out/x.pdf", "source": "reports/survey.pdf"}, "source is a text file"},
		{map[string]any{"path": "out/x.md", "source": "reports/survey.md"}, "copy_kb_files"},
		{map[string]any{"path": "out/x.pdf", "source": "reports/survey.md", "content": "# x"}, "not both"},
		{map[string]any{"path": "out/x.pdf"}, "path and content are required"},
	} {
		if out = call("write_kb_file", c.args); !strings.Contains(out.Result, c.want) {
			t.Errorf("%v: %+v", c.args, out)
		}
	}
	// A document written from content again is no longer one made from a file.
	call("write_kb_file", map[string]any{"path": "out/survey-landscape.pdf", "content": "# Other", "overwrite": true})
	if _, ok := m.made.get("out/survey-landscape.pdf"); ok {
		t.Errorf("a document written from content is still recorded as made from a file")
	}
	// The record is read back from beside the index.
	again := newRenderStore(filepath.Join(dir, "ix"))
	if rec, ok := again.get("reports/survey.pdf"); !ok || rec.Source != "reports/survey.md" || rec.Sum != sumOf(read("reports/survey.pdf")) {
		t.Errorf("the record read back: %+v %v", rec, ok)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"path": "reports/survey.md"}, "Give edits"},
		{map[string]any{"path": "reports/none.md", "find": "a", "replace": "b"}, "is not in the library"},
		{map[string]any{"path": ".git/config", "find": "a", "replace": "b"}, "is excluded"},
		{map[string]any{"path": "reports/taken.pdf", "find": "a", "replace": "b"}, "not edited in place"},
		{map[string]any{"path": "reports/survey.md", "edits": []any{map[string]any{"replace": "b"}}}, "each edit needs find"},
	} {
		if out = call("edit_kb_file", c.args); !out.Failed || !strings.Contains(out.Result, c.want) {
			t.Errorf("%v: %+v", c.args, out)
		}
	}
	if out = call("edit_kb_file", map[string]any{"path": "reports/survey.md", "find": "Order three racks.", "replace": "Order three racks."}); out.Failed || !strings.Contains(out.Result, "nothing was changed") {
		t.Errorf("an edit that changes nothing: %+v", out)
	}
}
