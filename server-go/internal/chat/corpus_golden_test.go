package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// TestGoldenCorpusMatchesNode runs the corpus tools (search_kb,
// list_kb_dir, list_libraries) and the studio-rag retrieval on both
// servers over a GUFI index of internal/gufi's test tree, built into each
// server's INDEX_BASE, with a second library beside the main one. It needs
// the GUFI on this host (STUDIO_GUFI_BIN, default /opt/gufi/bin).

func corpusGufiBin(t *testing.T) string {
	bin := os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	for _, tool := range []string{"gufi_dir2index", gufi.ToolQuery, gufi.ToolSQLite3} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("no %s in %s", tool, bin)
		}
	}
	return bin
}

var corpusEpoch = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

// copyTree copies from into to.
func copyTree(t *testing.T, from, to string) {
	var files []string
	defer func() {
		// Distinct modification times, an hour apart in path order, so a
		// query ordered by time has one answer.
		sort.Strings(files)
		for i, f := range files {
			at := corpusEpoch.Add(time.Duration(i) * time.Hour)
			os.Chtimes(f, at, at)
		}
	}()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, dst)
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// indexTree indexes src into parent/<basename src> and adds the words
// table with each file's text, as the enrich step does.
func indexTree(t *testing.T, bin, src, parent string) string {
	os.MkdirAll(parent, 0o755)
	if out, err := exec.Command(filepath.Join(bin, "gufi_dir2index"), src, parent).CombinedOutput(); err != nil {
		t.Fatalf("gufi_dir2index: %v %s", err, out)
	}
	root := filepath.Join(parent, filepath.Base(src))
	local := gufi.NewLocal(bin)
	ctx := context.Background()
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() != "db.db" {
			return err
		}
		res, err := local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{"-d", "x", p, "SELECT inode, name FROM entries WHERE type = 'f';"}})
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		sql := []string{"CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf);"}
		for _, r := range gufi.ParseRows(string(res.Stdout), 2) {
			text, err := os.ReadFile(filepath.Join(src, rel, r[1]))
			if err != nil || strings.HasSuffix(r[1], ".bin") {
				continue
			}
			sql = append(sql, fmt.Sprintf("INSERT INTO words VALUES (%s, %s, %s);", q(r[0]), q(r[1]), q(string(text))))
		}
		res, err = local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: append([]string{p}, sql...)})
		if err == nil && res.Stderr != "" {
			err = errors.New(res.Stderr)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGoldenCorpusMatchesNode(t *testing.T) {
	if os.Getenv("STUDIO_GOLDEN_NODE") == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	bin := corpusGufiBin(t)
	g := startGoldenWith(t, func(kb string, bases []string) {
		copyTree(t, "../gufi/testdata/tree", kb)
		docs := filepath.Join(filepath.Dir(kb), "docs")
		copyTree(t, "../gufi/testdata/tree/reports", docs)
		os.WriteFile(filepath.Join(docs, "budget notes.md"), []byte("Budget notes for the docs library.\n"), 0o644)
		docsIndex := indexTree(t, bin, docs, filepath.Join(filepath.Dir(kb), "docs-ix"))
		for _, base := range bases {
			indexTree(t, bin, kb, filepath.Join(base, "gufi"))
			defs, _ := json.Marshal([]any{map[string]any{"id": "docs", "label": "Docs", "indexRoot": docsIndex, "sourceRoot": docs}})
			os.WriteFile(filepath.Join(base, "libraries.json"), defs, 0o644)
			// No embedding worker here: meaning search off for both.
			os.WriteFile(filepath.Join(base, "indexing.json"), []byte(`{"kb":{"depth":"text"},"docs":{"depth":"text"}}`), 0o644)
		}
	})
	long := strings.Repeat("The budget forecast covers the quarter. ", 5)
	for i, sc := range []scenario{
		{name: "libraries", text: "which libraries?", replies: []reply{call("list_libraries", `{}`), text(long)}},
		{name: "labels then a labelled search", text: "label the budget", replies: []reply{
			call("apply_labels", `{"paths":"reports/2026/budget-q1.md","add":"finance"}`),
			call("search_kb", `{"query":"budget","tags":"finance"}`), text(long)}},
		{name: "search the main library", text: "find the budget", replies: []reply{call("search_kb", `{"query":"budget"}`), call("search_kb", `{"query":"budget","limit":"2"}`), text(long)}},
		{name: "search by name and text", text: "refine", replies: []reply{
			call("search_kb", `{"name":"budget"}`), call("search_kb", `{"text":"forecast","match":"all"}`), call("search_kb", `{"name":"report","text":"annual"}`), text(long)}},
		{name: "nothing to search for", text: "search", replies: []reply{call("search_kb", `{"query":"   "}`), call("search_kb", `{"text":"\"unbalanced"}`), text(long)}},
		{name: "no matches", text: "find zebra", replies: []reply{call("search_kb", `{"query":"zebraquagga"}`), text(long)}},
		{name: "search another library and all", text: "everywhere", replies: []reply{
			call("search_kb", `{"query":"budget","library":"docs"}`), call("search_kb", `{"query":"budget","library":"all"}`), call("search_kb", `{"query":"budget","library":"nope"}`), text(long)}},
		{name: "a label scope", text: "scoped search", extra: map[string]any{"labelScope": []any{"finance"}}, replies: []reply{call("search_kb", `{"query":"budget","tags":"other"}`), text(long)}},
		{name: "queries", text: "what is big?", replies: []reply{
			call("query_corpus", `{"canned":"largest","n":3}`), call("query_corpus", `{"canned":"newest","subtree":"reports"}`), call("query_corpus", `{"canned":"recent","days":"3650"}`),
			call("query_corpus", `{"canned":"big_directories","library":"docs"}`),
			call("query_corpus", `{"canned":"oldest","n":500}`), call("query_corpus", `{"canned":"nope"}`), call("query_corpus", `{"canned":"largest","library":"nope"}`), call("query_corpus", `{"canned":"largest","subtree":"../x"}`), text(long)}},
		// Across libraries Node runs one gufi_vt statement where this
		// host's GUFI has it; Go merges each library's rows, as its
		// /api/query does. The rows the model reads are the same.
		{name: "a query across libraries", text: "by type", requestsOnly: true, replies: []reply{
			call("query_corpus", `{"canned":"by_extension","library":"all"}`), call("query_corpus", `{"canned":"largest","n":4,"library":"all"}`), text(long)}},
		{name: "listings", text: "list", replies: []reply{
			call("list_kb_dir", `{}`), call("list_kb_dir", `{"path":"reports"}`), call("list_kb_dir", `{"library":"docs"}`),
			call("list_kb_dir", `{"library":"all"}`), call("list_kb_dir", `{"library":"nope"}`), call("list_kb_dir", `{"path":"../x"}`), call("list_kb_dir", `{"path":"missing"}`), text(long)}},
	} {
		t.Run(sc.name, func(t *testing.T) { runScenario(t, g, 100+i, sc) })
	}
	bearer := map[string]string{"Authorization": "Bearer caller-key"}
	whole := reply{whole: true, body: map[string]any{"id": "up-1", "model": "test-model",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "Grounded."}, "finish_reason": "stop"}}}}
	for _, c := range []httpCase{
		{name: "rag with context", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"What does the budget forecast say for the quarter?"}]}`, replies: []reply{whole}},
		{name: "rag, an over-specified question", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"budget forecast quarterly zebra onboarding details"}]}`, replies: []reply{whole}},
		{name: "rag, nothing found", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"zebraquagga"}]}`, replies: []reply{whole}},
		{name: "rag, one word", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"budget"}]}`, replies: []reply{whole}},
		{name: "rag, labels and top k", method: "POST", path: "/v1/chat/completions", headers: map[string]string{"Authorization": "Bearer caller-key", "X-RAG-Tags": "finance", "X-RAG-Top-K": "2"},
			body: `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"budget"}]}`, replies: []reply{whole}},
		{name: "the call log", method: "GET", path: "/api/rag/calls"},
	} {
		t.Run(c.name, func(t *testing.T) { runHTTPCase(t, g, c) })
	}
}
