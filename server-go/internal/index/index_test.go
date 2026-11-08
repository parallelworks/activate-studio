package index

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/cli"
)

// The test binary stands in for `studio-server extract` when it is run with
// INDEX_TEST_CHILD=extract (through a small wrapper script).
func TestMain(m *testing.M) {
	if os.Getenv("INDEX_TEST_CHILD") == "extract" {
		os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const gufiBin = "/opt/gufi/bin"

func haveGufi() bool {
	_, err := os.Stat(filepath.Join(gufiBin, "gufi_dir2index"))
	_, err2 := os.Stat(filepath.Join(gufiBin, "gufi_query"))
	return err == nil && err2 == nil
}

type fixture struct {
	t        *testing.T
	kb, base string
	a        *app.App
	ix       *Indexer
	mux      *http.ServeMux
}

func write(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if runtime.GOOS == "windows" || !haveGufi() {
		t.Skip("needs GUFI's tools and a POSIX shell")
	}
	dir := t.TempDir()
	f := &fixture{t: t, kb: filepath.Join(dir, "kb"), base: filepath.Join(dir, "ix")}
	write(t, filepath.Join(f.kb, "README.md"), "# top\nthe quokkafish lives here\n")
	for _, d := range []string{"alpha", "beta", "beta/inner"} {
		write(t, filepath.Join(f.kb, d, "note.md"), "notes in "+d+" about a wombatfish\n")
	}
	write(t, filepath.Join(f.kb, "node_modules", "x", "skip.md"), "never indexed\n")
	// The extraction child: this test binary, by a wrapper.
	exe, _ := os.Executable()
	wrapper := filepath.Join(dir, "extract")
	write(t, wrapper, "#!/bin/sh\nINDEX_TEST_CHILD=extract exec '"+exe+"' \"$@\"\n")
	os.Chmod(wrapper, 0o755)
	t.Setenv("STUDIO_EXTRACT_BIN", wrapper)
	t.Setenv("STUDIO_EMBED_BIN", filepath.Join(dir, "no-embedder"))
	env := map[string]string{"KB_ROOT": f.kb, "INDEX_BASE": f.base, "GUFI_BIN": gufiBin, "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f.a = app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	f.mux = http.NewServeMux()
	Register(f.mux, f.a)
	f.ix = Of(f.a)
	// No search by meaning here: the embedder is tested in internal/embed.
	if _, err := f.ix.Settings.Save("kb", map[string]any{"depth": "text"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) do(method, url string, body any) (int, map[string]any, string) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, url, rd)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Body.String()
}

func (f *fixture) finished(id int) IndexJob {
	f.t.Helper()
	end := time.Now().Add(60 * time.Second)
	for time.Now().Before(end) {
		if j, ok := f.ix.Job(id); ok && j.State != "queued" && j.State != "running" {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("job did not finish")
	return IndexJob{}
}

func words(t *testing.T, db string) [][3]string {
	t.Helper()
	c, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rows, err := c.Query("SELECT tinode, fname, wordf FROM words ORDER BY fname")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		rows.Scan(&r[0], &r[1], &r[2])
		out = append(out, r)
	}
	return out
}

func TestBuildSyncAndDelete(t *testing.T) {
	f := newFixture(t)
	code, body, _ := f.do("POST", "/api/index/build", map[string]any{})
	if code != 200 || body["kind"] != "build" || body["state"] != "queued" {
		t.Fatalf("%d %v", code, body)
	}
	job := f.finished(int(body["id"].(float64)))
	if job.State != "done" {
		t.Fatalf("%s: %v", job.State, job.Error)
	}
	var ids []string
	for _, p := range job.Phases {
		ids = append(ids, p.ID+":"+p.State)
	}
	if want := []string{"names:done", "documents:done", "text:done", "meaning:skipped"}; !slices.Equal(ids, want) {
		t.Fatalf("phases %v", ids)
	}
	if job.Phases[0].Total != 4 || job.Phases[0].Done != 4 {
		t.Fatalf("names %+v", job.Phases[0])
	}
	if n := *job.Phases[2].Note; n != "4 files with text" {
		t.Fatalf("text note %q", n)
	}
	root := f.ix.P.GufiIndex
	w := words(t, filepath.Join(root, "beta", "inner", "db.db"))
	if len(w) != 1 || w[0][1] != "note.md" || w[0][2] != "notes in beta/inner about a wombatfish\n" {
		t.Fatalf("words %q", w)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules")); err == nil {
		t.Fatal("node_modules indexed")
	}
	for _, f := range []string{"sweep-state.json", "index-depth.json", "index-runs.json", "skip-dirs.txt"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "..", f)); err != nil {
			t.Fatal(f, err)
		}
	}
	if !exists(filepath.Join(root, textCleanMark)) {
		t.Fatal("no text-clean mark")
	}

	// The status, in Node's key order.
	_, _, raw := f.do("GET", "/api/index/status", nil)
	var keys []string
	for _, k := range objectKeys([]byte(raw)) {
		keys = append(keys, k)
	}
	want := []string{"lastSweepAt", "lastSweepMs", "lastChanges", "lastError", "sweepIntervalSec", "running", "job", "queued", "last", "lastBuild", "runs", "indexed", "incomplete", "threads", "folder", "indexDir", "log"}
	if !slices.Equal(keys, want) {
		t.Fatalf("status keys %v", keys)
	}

	// Nothing changed: a sync does nothing.
	_, body, _ = f.do("POST", "/api/index/sweep", nil)
	if fmt.Sprint(body["changed"]) != "[]" {
		t.Fatalf("sweep %v", body)
	}
	// A new file in one folder, and one removed: that folder alone.
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(f.kb, "alpha", "more.md"), "the numbat\n")
	os.RemoveAll(filepath.Join(f.kb, "beta", "inner"))
	_, body, _ = f.do("POST", "/api/index/sweep", nil)
	if fmt.Sprint(body["changed"]) != "[alpha beta]" {
		t.Fatalf("sweep %v", body)
	}
	st := f.ix.Status()["\x00ordered"].(ordered)
	if fmt.Sprint(st[2][1]) != "[alpha beta beta/inner (removed)]" {
		t.Fatalf("lastChanges %v", st[2][1])
	}
	if len(words(t, filepath.Join(root, "alpha", "db.db"))) != 2 {
		t.Fatal("alpha's new file not indexed")
	}
	if exists(filepath.Join(root, "beta", "inner")) {
		t.Fatal("removed folder still indexed")
	}

	// One folder, through the API, at once.
	_, body, _ = f.do("POST", "/api/index/job", map[string]any{"path": "/alpha/more.md"})
	if body["dir"] != "alpha" || body["label"] != "Updating alpha" {
		t.Fatalf("job %v", body)
	}
	if j := f.finished(int(body["id"].(float64))); j.State != "done" {
		t.Fatalf("%v", j.Error)
	}
	code, body, _ = f.do("GET", "/api/index/job/12345", nil)
	if code != 404 || body["error"] != "no such job" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body, _ = f.do("POST", "/api/index/dir", map[string]any{}); code != 200 || body["error"] != "path required" {
		t.Fatalf("%d %v", code, body)
	}

	// The log names the jobs.
	_, _, log := f.do("GET", "/api/index/log?lines=200", nil)
	if !strings.Contains(log, "[1 build] Building the index: started on") || !strings.Contains(log, "] Syncing changes: done in") {
		t.Fatalf("log:\n%s", log)
	}

	// Deleting needs confirming, and leaves the files.
	if code, body, _ = f.do("DELETE", "/api/index", nil); code != 400 || !strings.Contains(body["error"].(string), "confirm=1") {
		t.Fatalf("%d %v", code, body)
	}
	_, body, _ = f.do("DELETE", "/api/index?confirm=1", nil)
	if j := f.finished(int(body["id"].(float64))); j.State != "done" {
		t.Fatal(j.Error)
	}
	if exists(root) || !exists(filepath.Join(f.kb, "alpha", "more.md")) {
		t.Fatal("delete")
	}
}

func TestCancelQueuedAndRunning(t *testing.T) {
	f := newFixture(t)
	hold := filepath.Join(t.TempDir(), "hold")
	write(t, hold, "")
	f.ix.holdFile = hold
	b, _ := f.ix.StartBuild()
	if again, _ := f.ix.StartBuild(); again.ID != b.ID {
		t.Fatal("a second build request gets the first")
	}
	q, _ := f.ix.Enqueue("alpha")
	end := time.Now().Add(30 * time.Second)
	for {
		j, _ := f.ix.Job(b.ID)
		if j.Phases[2].State == "running" {
			break
		}
		if time.Now().After(end) {
			t.Fatal("not held")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j, _ := f.ix.Cancel(q.ID); j.State != "cancelled" {
		t.Fatalf("queued: %s", j.State)
	}
	if n := f.ix.CancelAll(); n != 1 {
		t.Fatalf("stopped %d", n)
	}
	j := f.finished(b.ID)
	if j.State != "cancelled" || *j.Phases[2].Note != "stopped" {
		t.Fatalf("%s %+v", j.State, j.Phases[2])
	}
	st := f.ix.Status()["\x00ordered"].(ordered)
	m := map[string]any{}
	for _, kv := range st {
		m[kv[0].(string)] = kv[1]
	}
	if m["incomplete"] != true || m["lastError"].(*string) != nil {
		t.Fatalf("status %v", m)
	}
	os.Remove(hold)
	if j := f.finished(mustBuild(t, f.ix).ID); j.State != "done" || j.Label != "Updating the index" {
		t.Fatalf("%s %s", j.State, j.Label)
	}
}

func mustBuild(t *testing.T, ix *Indexer) IndexJob {
	j, err := ix.StartBuild()
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestHistory(t *testing.T) {
	f := newFixture(t)
	if j := f.finished(mustBuild(t, f.ix).ID); j.State != "done" {
		t.Fatal(j.Error)
	}
	_, body, _ := f.do("POST", "/api/kb/history/capture", nil)
	c := body["captured"].(map[string]any)
	if c["files"].(float64) != 4 {
		t.Fatalf("%v", c)
	}
	_, body, _ = f.do("GET", "/api/kb/history/level?dir=beta", nil)
	if body["available"] != true || fmt.Sprint(body["totals"]) != "map[bytes:72 files:2]" {
		t.Fatalf("%v", body)
	}
	_, body, _ = f.do("GET", "/api/kb/history/diff", nil)
	if body["available"] != false {
		t.Fatalf("%v", body)
	}
	if code, body, _ := f.do("DELETE", "/api/kb/history/snapshot?id=x/..", nil); code != 400 || body["error"] != "snapshot id required" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestDepthChange(t *testing.T) {
	r := func(d Depth, ocr bool, speed string) DepthRecord { return DepthRecord{d, ocr, speed} }
	cases := []struct {
		was, now DepthRecord
		want     string
	}{
		{r("names", false, "standard"), r("text", false, "standard"), "true false text"},
		{r("text", false, "standard"), r("ocr", true, "standard"), "true false OCR"},
		{r("names", false, "standard"), r("meaning-documents", true, "standard"), "true true text and OCR and search by meaning"},
		{r("ocr", true, "standard"), r("meaning-documents", true, "standard"), "false true search by meaning"},
		{r("meaning-full", true, "standard"), r("text", false, "standard"), "false false "},
		{r("meaning-documents", true, "standard"), r("meaning-documents", true, "faster"), "false true search by meaning"},
	}
	for _, c := range cases {
		ch := DepthChangeOf(c.was, c.now)
		if got := fmt.Sprintf("%v %v %s", ch.Text, ch.Meaning, ch.Label); got != c.want {
			t.Errorf("%v to %v: %s, want %s", c.was, c.now, got, c.want)
		}
	}
	if DepthChangeOf(r("text", false, "standard"), r("text", false, "standard")) != nil {
		t.Error("same record")
	}
}

func TestSweepStateKeyOrder(t *testing.T) {
	s := sweepState{m: map[string]float64{}}
	for _, k := range []string{".", "b", "2024", "a", "10", "007"} {
		s.set(k, 1)
	}
	b, _ := s.MarshalJSON()
	// JSON.stringify writes array-index keys first, ascending.
	if string(b) != `{"10":1,"2024":1,".":1,"b":1,"a":1,"007":1}` {
		t.Fatal(string(b))
	}
}

func TestDeniedLines(t *testing.T) {
	err := "Error: Could not open directory \"/kb/private\": Permission denied\nmkdir /ix/db.db failure: 13 Permission denied\n"
	if got := DeniedPaths(err); fmt.Sprint(got) != "[/kb/private]" {
		t.Fatal(got)
	}
	if !OnlyDenied(err) || OnlyDenied(err+"Error: disk full\n") {
		t.Fatal("onlyDenied")
	}
	if got := unmadeIndexDirs("mkdir /ix/a/b/db.db failure: 13 Permission denied\n"); fmt.Sprint(got) != "[/ix/a/b]" {
		t.Fatal(got)
	}
}

func TestFmtMs(t *testing.T) {
	for ms, want := range map[int64]string{5: "5 ms", 1500: "1.5 s", 119_999: "120.0 s", 125_400: "2 min 5 s"} {
		if got := fmtMs(ms); got != want {
			t.Errorf("%d: %q, want %q", ms, got, want)
		}
	}
	if enUS(1234567) != "1,234,567" || enUS(12) != "12" {
		t.Error(enUS(1234567))
	}
}
