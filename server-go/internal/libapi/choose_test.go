package libapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// What Add a library asks of this computer before anything is added: its
// folders, what one holds, and whether one is an index.
func TestChooseHere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	dir := t.TempDir()
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d
	}
	t.Setenv("HOME", dir)
	put(t, filepath.Join(dir, "data", "runs", "a.csv"), "1,2\n")
	put(t, filepath.Join(dir, "data", "b.md"), "# b\n")
	put(t, filepath.Join(dir, "index", "db.db"), "")
	put(t, filepath.Join(dir, "kb", "main.md"), "# main\n")
	env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f := &buildFixture{t: t, dir: dir, a: app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil), mux: http.NewServeMux()}
	f.s = New(f.a)
	Register(f.mux, f.a)
	t.Cleanup(func() { f.s.builds.stop() })

	code, l := f.call("GET", "/api/libraries/folders?path="+url.QueryEscape("~/data"), nil)
	es, _ := l["entries"].([]any)
	if code != 200 || l["path"] != filepath.Join(dir, "data") || len(es) != 1 || es[0].(map[string]any)["name"] != "runs" {
		t.Errorf("folders: %d %v", code, l)
	}
	if code, l = f.call("GET", "/api/libraries/folders?path=/none", nil); code != 400 {
		t.Errorf("none: %d %v", code, l)
	}
	_, c := f.call("POST", "/api/libraries/build/count", map[string]any{"sourceRoot": "~/data"})
	if n, _ := c["count"].(map[string]any); n == nil || n["files"] != 2.0 || n["bytes"] != 8.0 {
		t.Errorf("count: %v", c)
	}
	if _, c = f.call("POST", "/api/libraries/build/count", map[string]any{"sourceRoot": "~/none"}); c["count"] != nil {
		t.Errorf("count of none: %v", c)
	}

	for _, tc := range []struct {
		index, source string
		ok            bool
		why           string
	}{
		{"~/index", "", true, ""},
		{"~/index", "~/data", true, ""},
		{"~/index", "~/none", false, filepath.Join(dir, "none") + " is not a folder."},
		{"~/data", "", false, filepath.Join(dir, "data") + " is not the top of a GUFI index: it has no db.db. Choose the folder gufi_dir2index made."},
		{"~/data/b.md", "", false, filepath.Join(dir, "data", "b.md") + " is a file, not a folder."},
		{"~/none", "", false, filepath.Join(dir, "none") + " does not exist."},
	} {
		_, r := f.call("POST", "/api/libraries/check", map[string]any{"indexRoot": tc.index, "sourceRoot": tc.source})
		if r["ok"] != tc.ok || firstFailed(r) != tc.why {
			t.Errorf("%s %s: %v", tc.index, tc.source, r)
		}
	}
	if code, _ := f.call("POST", "/api/libraries/check", map[string]any{"indexRoot": ""}); code != 400 {
		t.Errorf("no folder: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "ix", "libraries.json")); err == nil {
		t.Error("a check adds nothing")
	}
}
