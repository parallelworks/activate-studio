package libapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/cli"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

// The cases of libraryBuild.test.mjs and the build cases of
// omissions.test.mjs, against the Go routes in Node-free mode: the check,
// a build as an index job, the sync, the depth, a rebuild, removal, and a
// folder the Studio may not read.

func TestMain(m *testing.M) {
	if os.Getenv("LIBAPI_TEST_CHILD") == "extract" {
		os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const testGufi = "/opt/gufi/bin"

type buildFixture struct {
	t                 *testing.T
	dir, kb, base, ix string
	a                 *app.App
	mux               *http.ServeMux
	s                 *Service
}

func put(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newBuildFixture(t *testing.T) *buildFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	if _, err := os.Stat(filepath.Join(testGufi, "gufi_dir2index")); err != nil {
		t.Skip("GUFI not installed")
	}
	dir := t.TempDir()
	f := &buildFixture{t: t, dir: dir, kb: filepath.Join(dir, "kb"), base: filepath.Join(dir, "ix")}
	put(t, filepath.Join(f.kb, "main.md"), "# main\n")
	exe, _ := os.Executable()
	wrapper := filepath.Join(dir, "extract")
	put(t, wrapper, "#!/bin/sh\nLIBAPI_TEST_CHILD=extract exec '"+exe+"' \"$@\"\n")
	os.Chmod(wrapper, 0o755)
	t.Setenv("STUDIO_EXTRACT_BIN", wrapper)
	t.Setenv("STUDIO_EMBED_BIN", filepath.Join(dir, "no-embedder"))
	t.Setenv("HOME", dir)
	env := map[string]string{"KB_ROOT": f.kb, "INDEX_BASE": f.base, "GUFI_BIN": testGufi, "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f.a = app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	f.mux = http.NewServeMux()
	index.Register(f.mux, f.a)
	Register(f.mux, f.a)
	f.s = New(f.a)
	t.Cleanup(func() { f.s.builds.stop() })
	return f
}

func (f *buildFixture) call(method, u string, body any) (int, map[string]any) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, u, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (f *buildFixture) library(id string) map[string]any {
	_, b := f.call("GET", "/api/libraries", nil)
	for _, l := range b["libraries"].([]any) {
		if m := l.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func (f *buildFixture) waitReady(id string) map[string]any {
	f.t.Helper()
	end := time.Now().Add(60 * time.Second)
	for time.Now().Before(end) {
		if l := f.library(id); l != nil {
			if b, ok := l["build"].(map[string]any); ok && (b["state"] == "ready" || b["state"] == "failed") && b["progress"] == nil && f.s.builds.jobOf(id) == nil {
				return l
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("the build did not finish")
	return nil
}

func (f *buildFixture) hits(q, lib string) []string {
	f.t.Helper()
	_, b := f.call("GET", "/api/search?q="+url.QueryEscape(q)+"&library="+lib, nil)
	var out []string
	hs, _ := b["hits"].([]any)
	for _, h := range hs {
		out = append(out, h.(map[string]any)["path"].(string))
	}
	sort.Strings(out)
	return out
}

func steps(b map[string]any) [][2]any {
	var out [][2]any
	for _, s := range b["steps"].([]any) {
		m := s.(map[string]any)
		out = append(out, [2]any{m["name"], m["ok"]})
	}
	return out
}

func firstFailed(b map[string]any) string {
	for _, s := range b["steps"].([]any) {
		if m := s.(map[string]any); m["ok"] == false {
			return m["detail"].(string)
		}
	}
	return ""
}

func TestLibraryBuild(t *testing.T) {
	f := newBuildFixture(t)
	papers := filepath.Join(f.dir, "papers")
	put(t, filepath.Join(papers, "intro.md"), "# Introduction\nThe turbine blade study.\n")
	put(t, filepath.Join(papers, "data", "runs.csv"), "a,b\n1,2\n")
	put(t, filepath.Join(papers, "data", "big.bin"), strings.Repeat("\x01", 4096))

	if _, b := f.call("GET", "/api/libraries", nil); b["canBuild"] != true {
		t.Fatalf("canBuild: %v", b)
	}

	// The check lists the folders it would make, and refuses unusable ones.
	fresh := filepath.Join(f.dir, "new", "notes")
	idx := filepath.Join(f.dir, "indexes", "notes")
	_, c := f.call("POST", "/api/libraries/build/check", map[string]any{"sourceRoot": fresh, "indexRoot": idx})
	if c["ok"] != true || !slices.Equal(steps(c), [][2]any{{"gufi", true}, {"source", true}, {"index", true}, {"extract", true}}) {
		t.Fatalf("check: %v", c)
	}
	if got := c["create"].([]any); len(got) != 2 || got[0] != fresh || got[1] != idx {
		t.Fatalf("create: %v", got)
	}
	if !strings.Contains(c["steps"].([]any)[1].(map[string]any)["detail"].(string), "does not exist yet; it is created, empty") {
		t.Fatalf("source detail: %v", c["steps"])
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Fatal("a check makes nothing")
	}
	fail := func(body map[string]any, want string) {
		t.Helper()
		_, r := f.call("POST", "/api/libraries/build/check", body)
		if r["ok"] != false || !strings.Contains(firstFailed(r), want) {
			t.Fatalf("%v: %v", body, r)
		}
	}
	fail(map[string]any{"sourceRoot": papers, "indexRoot": filepath.Join(papers, "idx")}, "inside the folder being indexed")
	fail(map[string]any{"sourceRoot": f.kb}, "is the main folder")
	fail(map[string]any{"sourceRoot": filepath.Join(f.base, "x")}, "Studio's own data")
	put(t, filepath.Join(f.dir, "busy", "other.txt"), "x")
	fail(map[string]any{"sourceRoot": papers, "indexRoot": filepath.Join(f.dir, "busy")}, "holds files the Studio did not write")
	put(t, filepath.Join(f.dir, "afile"), "x")
	fail(map[string]any{"sourceRoot": filepath.Join(f.dir, "afile")}, "is a file, not a folder")

	// Missing folders are made only once confirmed.
	drop := filepath.Join(f.dir, "drop")
	code, r := f.call("POST", "/api/libraries/build", map[string]any{"sourceRoot": drop})
	if code != 409 || !strings.Contains(r["error"].(string), "do not exist yet: "+drop+". Confirm to create them") {
		t.Fatalf("unconfirmed: %d %v", code, r)
	}
	code, r = f.call("POST", "/api/libraries/build", map[string]any{"sourceRoot": drop, "label": "Drop box", "create": true})
	if code != 200 {
		t.Fatalf("confirmed: %d %v", code, r)
	}
	dropID := r["library"].(map[string]any)["id"].(string)
	l := f.waitReady(dropID)
	if b := l["build"].(map[string]any); b["state"] != "ready" || b["managed"] != true || b["files"] != 0.0 {
		t.Fatalf("drop box: %v", b)
	}

	// A build runs as an index job and adds a read-only, searchable library.
	pidx := filepath.Join(f.dir, "indexes", "papers")
	code, r = f.call("POST", "/api/libraries/build", map[string]any{"sourceRoot": papers, "indexRoot": pidx, "label": "Papers", "create": true})
	if code != 200 || r["library"].(map[string]any)["id"] != "papers" {
		t.Fatalf("build: %d %v", code, r)
	}
	if job := r["job"].(map[string]any); job["dir"] != "library:papers" || job["kind"] != "folder" {
		t.Fatalf("job: %v", job)
	}
	l = f.waitReady("papers")
	b := l["build"].(map[string]any)
	real, _ := filepath.EvalSymlinks(papers)
	if b["state"] != "ready" || l["writable"] != false || l["indexRoot"] != filepath.Join(pidx, "papers") || l["sourceRoot"] != real || b["files"] != 3.0 || b["dirs"] != 2.0 {
		t.Fatalf("papers: %v", l)
	}
	raw, err := os.ReadFile(filepath.Join(pidx, manifestName))
	if err != nil || !strings.Contains(string(raw), `"source": "`+real+`"`) {
		t.Fatalf("manifest: %s %v", raw, err)
	}
	if got := f.hits("turbine", "papers"); !slices.Contains(got, "intro.md") {
		t.Fatalf("turbine: %v", got)
	}
	if got := f.hits("runs", "papers"); !slices.Contains(got, "data/runs.csv") {
		t.Fatalf("runs: %v", got)
	}

	// The sync indexes a folder that changed, and drops one that is gone.
	put(t, filepath.Join(papers, "data", "later.md"), "# later\nA compressor note.\n")
	future := time.Now().Add(5 * time.Second)
	os.Chtimes(filepath.Join(papers, "data"), future, future)
	put(t, filepath.Join(papers, "extra", "x.txt"), "x")
	changed, err := f.s.builds.sync("papers")
	if err != nil || !slices.Contains(changed, "data") {
		t.Fatalf("sync: %v %v", changed, err)
	}
	if got := f.hits("compressor", "papers"); !slices.Equal(got, []string{"data/later.md"}) {
		t.Fatalf("compressor: %v", got)
	}
	if b := f.library("papers")["build"].(map[string]any); b["files"] != 5.0 {
		t.Fatalf("files after sync: %v", b)
	}
	if changed, _ := f.s.builds.sync("papers"); len(changed) != 0 {
		t.Fatalf("nothing new: %v", changed)
	}
	os.RemoveAll(filepath.Join(papers, "extra"))
	changed, _ = f.s.builds.sync("papers")
	if !slices.Contains(changed, "extra (removed)") {
		t.Fatalf("removed: %v", changed)
	}
	if _, err := os.Stat(filepath.Join(pidx, "papers", "extra")); err == nil {
		t.Fatal("the gone folder's index stays")
	}

	// The depth is set per library the Studio builds.
	if l := f.library("papers"); l["depth"] != "meaning-documents" || l["meaning"] != "documents" {
		t.Fatalf("starting depth: %v %v", l["depth"], l["meaning"])
	}
	code, r = f.call("PUT", "/api/libraries/papers/depth", map[string]any{"depth": "meaning-full"})
	if code != 200 || r["depth"] != "meaning-full" || r["meaning"] != "full" {
		t.Fatalf("depth: %d %v", code, r)
	}
	if code, _ := f.call("PUT", "/api/libraries/papers/depth", map[string]any{"depth": "deepest"}); code != 400 {
		t.Fatalf("bad depth: %d", code)
	}
	if code, _ := f.call("PUT", "/api/libraries/nothing-here/depth", map[string]any{"depth": "names"}); code != 404 {
		t.Fatalf("no library: %d", code)
	}
	code, r = f.call("POST", "/api/libraries", map[string]any{"id": "papers-copy", "indexRoot": filepath.Join(pidx, "papers")})
	if code != 200 || r["library"].(map[string]any)["depth"] != nil || r["library"].(map[string]any)["meaning"] != nil {
		t.Fatalf("add an index: %d %v", code, r)
	}
	if code, _ := f.call("PUT", "/api/libraries/papers-copy/depth", map[string]any{"depth": "names"}); code != 409 {
		t.Fatalf("read-only depth: %d", code)
	}
	if code, _ := f.call("PUT", "/api/libraries/papers-copy/meaning", map[string]any{"level": "full"}); code != 409 {
		t.Fatalf("read-only meaning: %d", code)
	}
	if code, _ := f.call("DELETE", "/api/libraries/papers-copy", nil); code != 200 {
		t.Fatalf("delete the copy: %d", code)
	}
	code, r = f.call("PUT", "/api/libraries/papers/meaning", map[string]any{"level": "off"})
	if code != 200 || r["depth"] != "ocr" || r["meaning"] != "off" {
		t.Fatalf("meaning off: %d %v", code, r)
	}
	if code, _ := f.call("PUT", "/api/libraries/papers/meaning", map[string]any{"level": "most"}); code != 400 {
		t.Fatalf("bad level: %d", code)
	}
	// At Names, a rebuild leaves no words to search; back at Text + OCR it reads them again.
	f.call("PUT", "/api/libraries/papers/depth", map[string]any{"depth": "names"})
	if code, _ := f.call("POST", "/api/libraries/papers/rebuild", map[string]any{}); code != 200 {
		t.Fatalf("rebuild: %d", code)
	}
	f.waitReady("papers")
	if got := f.hits("turbine", "papers"); slices.Contains(got, "intro.md") {
		t.Fatalf("words at Names: %v", got)
	}
	f.call("PUT", "/api/libraries/papers/depth", map[string]any{"depth": "ocr"})
	code, _ = f.call("POST", "/api/libraries/papers/rebuild", nil)
	if code != 200 {
		t.Fatalf("rebuild: %d", code)
	}
	if b := f.waitReady("papers")["build"].(map[string]any); b["state"] != "ready" || b["files"] != 4.0 {
		t.Fatalf("after rebuild: %v", b)
	}
	if got := f.hits("turbine", "papers"); !slices.Contains(got, "intro.md") {
		t.Fatalf("words back: %v", got)
	}
	if code, _ := f.call("POST", "/api/libraries/kb/rebuild", nil); code != 404 {
		t.Fatalf("rebuild the main folder: %d", code)
	}

	// Removing forgets the library and the Studio's own files for it, not a chosen index folder.
	own := f.s.Reg.BuiltLibraryDir(dropID)
	if _, err := os.Stat(own); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call("DELETE", "/api/libraries/"+dropID, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(own); err == nil {
		t.Fatal("the Studio's own folder stays")
	}
	if code, _ := f.call("DELETE", "/api/libraries/papers", nil); code != 200 {
		t.Fatalf("delete papers: %d", code)
	}
	if _, err := os.Stat(filepath.Join(pidx, "papers", "db.db")); err != nil {
		t.Fatal("the chosen index folder goes")
	}
}

// A folder the Studio may not read is named, its readable neighbors are
// all indexed (gufi_dir2index alone sometimes stops before them), and a
// sync keeps it as last indexed.
func TestBuildBesideAnUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	f := newBuildFixture(t)
	src := filepath.Join(f.dir, "perm")
	put(t, filepath.Join(src, "open", "a.md"), "platypusword in the open folder\n")
	put(t, filepath.Join(src, "shut", "b.md"), "platypusword in the shut folder\n")
	code, r := f.call("POST", "/api/libraries/build", map[string]any{"sourceRoot": src, "label": "Perm", "id": "perm", "depth": "text"})
	if code != 200 {
		t.Fatalf("build: %d %v", code, r)
	}
	f.waitReady("perm")
	if got := f.hits("platypusword", "perm"); !slices.Equal(got, []string{"open/a.md", "shut/b.md"}) {
		t.Fatalf("before: %v", got)
	}
	os.Chmod(filepath.Join(src, "shut"), 0)
	defer os.Chmod(filepath.Join(src, "shut"), 0o755)
	put(t, filepath.Join(src, "open", "c.md"), "platypusword, a new file\n")
	if _, err := f.s.builds.sync("perm"); err != nil {
		t.Fatal(err)
	}
	if got := f.hits("platypusword", "perm"); !slices.Equal(got, []string{"open/a.md", "open/c.md", "shut/b.md"}) {
		t.Fatalf("kept as last indexed: %v", got)
	}
	if d := f.s.Reg.ReadDenied("perm"); d == nil || !slices.Contains(d.Folders, filepath.Join(src, "shut")) {
		t.Fatalf("denied record: %+v", d)
	}
	// Rebuilt beside the unreadable folder, many times: the readable one is
	// always there.
	for i := 0; i < 8; i++ {
		if code, _ := f.call("POST", "/api/libraries/perm/rebuild", nil); code != 200 {
			t.Fatalf("rebuild: %d", code)
		}
		f.waitReady("perm")
		if got := f.hits("platypusword", "perm"); !slices.Equal(got, []string{"open/a.md", "open/c.md"}) {
			t.Fatalf("rebuild %d: %v", i, got)
		}
	}
}

func TestMissingIndexDirsSkipsWhatCouldNotBeRead(t *testing.T) {
	built := t.TempDir()
	put(t, filepath.Join(built, "db.db"), "")
	put(t, filepath.Join(built, "kept", "db.db"), "")
	sc := index.Scan{Order: []string{"", "kept", "open", "open/deep", "shut", "shut/in"}, Denied: []string{"shut"}}
	if got := index.MissingIndexDirs(built, sc); !slices.Equal(got, []string{"open", "open/deep"}) {
		t.Fatalf("missing: %v", got)
	}
	sub := subScan(index.Scan{Order: []string{"", "a", "a/b", "ab"}, Latest: map[string]int64{"": 1, "a": 2, "a/b": 3, "ab": 4}, Denied: []string{"a/b"}}, "a")
	if !slices.Equal(sub.Order, []string{"", "b"}) || sub.Latest["b"] != 3 || !slices.Equal(sub.Denied, []string{"b"}) {
		t.Fatalf("subScan: %+v", sub)
	}
}
