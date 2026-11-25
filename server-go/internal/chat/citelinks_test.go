package chat

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// A pw code session's results carry each file's link to copy; the library is
// in it when it is not the main one, and spaces and brackets are encoded.
func TestResultsCarryTheFilesLinks(t *testing.T) {
	lib := "archive"
	files := []conversations.File{{Path: "manuals/pump map.pdf"}, {Path: "specs/a (1).md", Library: &lib}}
	got := withCiteLinks("search_kb", &Outcome{Result: "hits", Files: files})
	want := "hits\n\nTo cite these files, copy their links:\n[manuals/pump map.pdf](#open=file:manuals/pump%20map.pdf)\n[specs/a (1).md](#open=file:specs/a%20%281%29.md&lib=archive)"
	if got != want {
		t.Errorf("%q", got)
	}
	// One file read: its link is in the text's head, before the text.
	if got := withCiteLinks("read_kb_file", &Outcome{Result: "[source: index]\nText\nmore", Files: files[:1]}); got != "[source: index]\nTo cite this file, copy its link: [manuals/pump map.pdf](#open=file:manuals/pump%20map.pdf)\nText\nmore" {
		t.Errorf("%q", got)
	}
	// Several: each in its own head.
	got = withCiteLinks("read_kb_file", &Outcome{Result: "### manuals/pump map.pdf\n[source: index]\nA\n\n### specs/a (1).md\n[source: index]\nB", Files: files})
	if strings.Count(got, "copy its link: ") != 2 || !strings.Contains(got, "### specs/a (1).md\nTo cite this file, copy its link: [specs/a (1).md](#open=file:specs/a%20%281%29.md&lib=archive)\n[source: index]\nB") {
		t.Errorf("%q", got)
	}
	// At most maxCiteLinks, and none on a failure, another tool's result or
	// a result with no files.
	var many []conversations.File
	for i := 0; i < 40; i++ {
		many = append(many, files[0])
	}
	if got := withCiteLinks("list_kb_dir", &Outcome{Result: "x", Files: many}); strings.Count(got, "#open=file:") != maxCiteLinks || !strings.Contains(got, "Other files") {
		t.Errorf("%q", got)
	}
	for _, o := range []*Outcome{{Result: "x"}, {Result: "x", Files: files, Failed: true}} {
		if got := withCiteLinks("search_kb", o); got != "x" {
			t.Errorf("%q", got)
		}
	}
	if got := withCiteLinks("write_kb_file", &Outcome{Result: "x", Files: files}); got != "x" {
		t.Errorf("%q", got)
	}
}

// A query's rows that are files are cited, each in its row's library; rows
// that are folders or types are not.
func TestAQuerysFilesAreCited(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	f := m.queryFiles(&search.QueryResult{Columns: []string{"path", "bytes"}, Rows: [][]string{{"a/b.xlsx", "9"}}}, nil, nil)
	if len(f) != 1 || f[0].Path != "a/b.xlsx" || f[0].Library != nil {
		t.Errorf("%+v", f)
	}
	f = m.queryFiles(&search.QueryResult{Columns: []string{"library", "path", "bytes"}, Rows: [][]string{{"Plant library", "a/b.xlsx", "9"}, {"Archive", "c.md", "3"}}, RowLibraries: []string{"kb", "archive"}}, []string{"all"}, nil)
	if len(f) != 2 || f[0].Library != nil || deref(f[1].Library) != "archive" || f[1].Path != "c.md" {
		t.Errorf("%+v", f)
	}
	if f := m.queryFiles(&search.QueryResult{Columns: []string{"directory", "files"}, Rows: [][]string{{"a", "9"}}}, nil, nil); len(f) != 0 {
		t.Errorf("%+v", f)
	}
}

// Through MCP: a chat's session and a person's own get the links; another
// MCP client and the Studio assistant's own call get what they did.
func TestOnlyAPWCodeSessionGetsTheLinks(t *testing.T) {
	m := bridgeStudio(t)
	root := m.A.Cfg.KBRoot
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	os.WriteFile(filepath.Join(root, "notes", "pump plan.md"), []byte("cavitation\n"), 0o644)
	c := m.Conv.Create(nil, "bob")
	tok, err := m.issueChatToken(bobProxy, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	const link = "[notes/pump plan.md](#open=file:notes/pump%20plan.md)"
	call := func(who *auth.User, h map[string]string) string {
		t.Helper()
		_, out := mcpAs(t, m, who, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_kb_dir","arguments":{"path":"notes"}}}`)
		b, _ := json.Marshal(out)
		return string(b)
	}
	if got := call(bobProxy, map[string]string{"Authorization": "Bearer " + tok}); !strings.Contains(got, link) {
		t.Errorf("a chat's session: %s", got)
	}
	if got := call(bobProxy, nil); !strings.Contains(got, link) {
		t.Errorf("a person's own session: %s", got)
	}
	if got := call(bobOIDC, nil); !strings.Contains(got, "pump plan.md") || strings.Contains(got, "#open=file:") {
		t.Errorf("another MCP client: %s", got)
	}
	out, err := m.Tools.Execute(httptest.NewRequest("POST", "/api/chat/stream", nil), "list_kb_dir", `{"path":"notes"}`, ToolCtx{})
	if err != nil || strings.Contains(out.Result, "#open=file:") || !strings.Contains(out.Result, "pump plan.md") {
		t.Errorf("the Studio assistant's own call: %v %v", out, err)
	}
}
