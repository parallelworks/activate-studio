package libapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// citedBed is a Studio of one with a main library, a second one here and
// one over ssh, whose host is a local shell.
type citedBed struct {
	t               *testing.T
	s               *Service
	mux             *http.ServeMux
	kb, docs, proj  string
	outside         string
	remoteCalls     atomic.Int32
	remoteArguments string
}

func newCitedBed(t *testing.T) *citedBed {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &citedBed{t: t, kb: filepath.Join(dir, "kb"), docs: filepath.Join(dir, "docs"), proj: filepath.Join(dir, "box", "proj"), outside: filepath.Join(dir, "outside")}
	ix := filepath.Join(dir, "ix")
	for _, f := range []string{"kb/notes.md", "kb/reports/2026/site survey.pdf", "kb/reports/plan.docx", "kb/agents/writer/AGENTS.md", "kb/agents/writer/draft.md", "kb/both.md", "kb/.env", "kb/.git/config", "kb/pkg/__pycache__/x.pyc",
		"docs/plan/one.md", "docs/only-here.csv", "docs/both.md", "docs/same.md", "box/proj/run/results.csv", "box/proj/same.md", "box/secret.txt", "outside/secret.txt", "work/notes.md", "work/src/main.py", "kb/src/main.py", "kb/src/other.py"} {
		put(t, filepath.Join(dir, f), "x\n")
	}
	os.Symlink(filepath.Join(b.outside, "secret.txt"), filepath.Join(b.kb, "leak.txt"))
	os.Symlink(b.outside, filepath.Join(b.kb, "leakdir"))
	os.Symlink(filepath.Join(b.kb, "notes.md"), filepath.Join(b.kb, "alias.md"))
	os.Symlink(filepath.Join(dir, "box", "secret.txt"), filepath.Join(b.proj, "leak.txt"))
	os.MkdirAll(filepath.Join(b.kb, "reports.md"), 0o755)
	lb, _ := json.Marshal([]map[string]any{{"id": "docs", "label": "Field docs", "indexRoot": filepath.Join(dir, "docs-index"), "sourceRoot": b.docs}})
	put(t, filepath.Join(ix, "libraries.json"), string(lb))
	put(t, filepath.Join(dir, "docs-index", "db.db"), "")
	h := sha256.Sum256([]byte("single"))
	userDir := filepath.Join(ix, "users", hex.EncodeToString(h[:])[:16])
	ub, _ := json.Marshal([]map[string]any{{"id": "u-runs", "label": "Runs", "transport": "ssh", "site": "me@box", "destination": "me@box",
		"port": nil, "resource": "x", "sourceDir": b.proj, "sourceRoot": b.proj, "indexRoot": filepath.Join(dir, "box", "idx"), "gufiBin": "",
		"fullText": true, "state": "ready", "run": nil, "error": nil, "builtAt": time.Now().UTC().Format(time.RFC3339), "directories": 2, "files": 3, "scheduler": nil}})
	put(t, filepath.Join(userDir, "libraries.json"), string(ub))
	put(t, filepath.Join(userDir, "owner"), "single")
	t.Setenv("HOME", dir)
	env := map[string]string{"KB_ROOT": b.kb, "INDEX_BASE": ix, "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0", "HOST": "127.0.0.1"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	pool, _ := remote.Shared(a, func() remote.Options {
		return remote.Options{IndexBase: ix, Getenv: func(string) string { return "" },
			Spawn: func(site, key, command string) *exec.Cmd {
				b.remoteCalls.Add(1)
				c := exec.Command("/bin/sh", "-c", command)
				c.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
				return c
			},
			SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} },
		}
	})
	t.Cleanup(pool.Close)
	b.mux = http.NewServeMux()
	kb.Register(b.mux, a)
	Register(b.mux, a)
	b.s = New(a)
	return b
}

func (b *citedBed) ask(body map[string]any) map[string]citedFile {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/libraries/cited", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	b.mux.ServeHTTP(w, r)
	var out struct{ Files map[string]citedFile }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		b.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return out.Files
}

func paths(ps ...string) []map[string]any {
	out := []map[string]any{}
	for _, p := range ps {
		out = append(out, map[string]any{"path": p})
	}
	return out
}

func (b *citedBed) want(body map[string]any, want map[string]citedFile) {
	b.t.Helper()
	got := b.ask(body)
	for p, f := range want {
		if got[p] != f {
			b.t.Errorf("%s: got %+v, want %+v", p, got[p], f)
		}
	}
	for p, f := range got {
		if _, ok := want[p]; !ok {
			b.t.Errorf("%s is linked to %+v", p, f)
		}
	}
}

// A path that names a file of a library is that file; one that names none,
// or one the Library does not show, or one outside, is nothing.
func TestCitedPathsThatAreLibraryFiles(t *testing.T) {
	b := newCitedBed(t)
	b.want(map[string]any{"paths": paths("notes.md", "./notes.md", "reports/2026/site survey.pdf", "alias.md", "Field docs/plan/one.md", "docs/plan/one.md", "docs:plan/one.md", "only-here.csv", filepath.Join(b.kb, "reports/plan.docx"), filepath.Join(b.docs, "plan/one.md"), "both.md",
		// nothing: not there, a folder, hidden, left out, led out
		"missing.md", "reports", "reports.md", "reports/", ".env", ".git/config", "pkg/__pycache__/x.pyc", "../outside/secret.txt", "leak.txt", "leakdir/secret.txt", filepath.Join(b.outside, "secret.txt"), filepath.Join(b.kb, "../outside/secret.txt"), "/etc/passwd", "a\nb", "")},
		map[string]citedFile{
			"notes.md": {Path: "notes.md"}, "./notes.md": {Path: "notes.md"}, "reports/2026/site survey.pdf": {Path: "reports/2026/site survey.pdf"}, "alias.md": {Path: "alias.md"},
			"Field docs/plan/one.md": {"plan/one.md", "docs"}, "docs/plan/one.md": {"plan/one.md", "docs"}, "docs:plan/one.md": {"plan/one.md", "docs"}, "only-here.csv": {"only-here.csv", "docs"},
			filepath.Join(b.kb, "reports/plan.docx"): {Path: "reports/plan.docx"}, filepath.Join(b.docs, "plan/one.md"): {"plan/one.md", "docs"},
			// The main library's, as a path with no library is.
			"both.md": {Path: "both.md"},
		})
	if n := b.remoteCalls.Load(); n != 0 {
		t.Errorf("%d connections were made for paths that name no other system", n)
	}
	// The chat's own library comes first.
	b.want(map[string]any{"paths": paths("both.md"), "libraries": []string{"docs"}}, map[string]citedFile{"both.md": {"both.md", "docs"}})
}

// A session's paths: from its folder, in full, and on its own machine only.
func TestCitedPathsOfASession(t *testing.T) {
	b := newCitedBed(t)
	studio := map[string]any{"id": "studio", "name": "here"}
	b.want(map[string]any{"machine": studio, "cwd": filepath.Join(b.kb, "agents/writer"), "paths": paths("draft.md", "AGENTS.md", "../../notes.md", "reports/plan.docx", "~/kb/notes.md", filepath.Join(b.kb, "agents/writer/draft.md"), filepath.Join(b.proj, "run/results.csv"))},
		map[string]citedFile{"draft.md": {Path: "agents/writer/draft.md"}, "AGENTS.md": {Path: "agents/writer/AGENTS.md"}, "../../notes.md": {Path: "notes.md"}, "reports/plan.docx": {Path: "reports/plan.docx"}, "~/kb/notes.md": {Path: "notes.md"},
			filepath.Join(b.kb, "agents/writer/draft.md"): {Path: "agents/writer/draft.md"}})

	// A folder no library holds: its own files are not a library's of the
	// same name.
	work := filepath.Join(filepath.Dir(b.kb), "work")
	b.want(map[string]any{"machine": studio, "cwd": work, "paths": paths("notes.md", "src/main.py", "src/other.py", "both.md", "Field docs/plan/one.md")},
		map[string]citedFile{"src/other.py": {Path: "src/other.py"}, "Field docs/plan/one.md": {"plan/one.md", "docs"}})

	// Another machine: what is in the library on it, and no file here that
	// has the same path.
	box := map[string]any{"id": "m2", "name": "box", "hostname": "box.example"}
	b.want(map[string]any{"machine": box, "cwd": filepath.Join(b.proj, "run"), "paths": paths("results.csv", "run/results.csv", "missing.csv", filepath.Join(b.proj, "run/results.csv"), filepath.Join(b.kb, "notes.md"), filepath.Join(b.proj, "leak.txt"), filepath.Join(b.proj, "../secret.txt"))},
		map[string]citedFile{"results.csv": {"run/results.csv", "u-runs"}, "run/results.csv": {"run/results.csv", "u-runs"}, filepath.Join(b.proj, "run/results.csv"): {"run/results.csv", "u-runs"}})
	if n := b.remoteCalls.Load(); n != 1 {
		t.Errorf("%d sessions for one host, want 1", n)
	}
	// A machine no library is on.
	b.want(map[string]any{"machine": map[string]any{"id": "m3", "name": "laptop"}, "cwd": b.kb, "paths": paths("notes.md", filepath.Join(b.kb, "notes.md"))}, map[string]citedFile{})
}

// A bare name is the file the chat's own steps gave in full under it, and
// nothing where two fit.
func TestCitedNamesFromTheChatsSteps(t *testing.T) {
	b := newCitedBed(t)
	also := func(p string, as ...map[string]any) map[string]any { return map[string]any{"path": p, "also": as} }
	b.want(map[string]any{"paths": []map[string]any{
		also("site survey.pdf", map[string]any{"path": "reports/2026/site survey.pdf"}),
		also("one.md", map[string]any{"path": "plan/one.md", "lib": "docs"}),
		also("plan.docx", map[string]any{"path": "reports/plan.docx"}, map[string]any{"path": "gone/plan.docx"}),
		also("same.md", map[string]any{"path": "same.md", "lib": "docs"}, map[string]any{"path": "same.md", "lib": "u-runs"}),
		also("secret.txt", map[string]any{"path": "../outside/secret.txt"}, map[string]any{"path": "secret.txt", "lib": "nope"}),
	}}, map[string]citedFile{"site survey.pdf": {Path: "reports/2026/site survey.pdf"}, "one.md": {"plan/one.md", "docs"}, "plan.docx": {Path: "reports/plan.docx"}})
}

// Someone else's library is never looked in, whatever names it.
func TestCitedPathsOnlyInTheViewersLibraries(t *testing.T) {
	b := newCitedBed(t)
	ps := []citedAsk{{Path: filepath.Join(b.proj, "run/results.csv")}, {Path: "Runs/run/results.csv"}, {Path: "u-runs:run/results.csv"}, {Path: "results.csv", Also: []citedAlso{{"run/results.csv", "u-runs"}}}}
	body := citedBody{Paths: ps, Machine: &citedMachine{ID: "m2", Name: "box"}, Cwd: filepath.Join(b.proj, "run")}
	if got := b.s.citing(body, libraries.ViewerOf("someone-else")).find(t.Context()); len(got) != 0 {
		t.Errorf("another person got %v", got)
	}
	if got := b.s.citing(body, libraries.Deployment).find(t.Context()); len(got) != 0 {
		t.Errorf("nobody got %v", got)
	}
	if n := b.remoteCalls.Load(); n != 0 {
		t.Errorf("%d connections for a library that is not theirs", n)
	}
	if got := b.s.citing(body, libraries.ViewerOf("")).find(t.Context()); len(got) != 4 {
		t.Errorf("its owner got %v", got)
	}
}
