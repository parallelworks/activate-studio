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
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The read-through route and viewer cases of server/test/readOriginal.test.mjs
// ("the connection test runs ssh ...", "saving needs a passing test ...",
// "a library over ssh keeps the host ...", "the main folder ... take no
// host"). Each ssh host is a local shell reached through the pool's Spawn
// seam with a home of its own, as the Node test's setRemoteSpawn does.
func TestReadThroughRoutesAndViewer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	kbRoot, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	xferFiles := filepath.Join(dir, "xfer", "data")
	put(t, filepath.Join(xferFiles, "readme.md"), "readme on the transfer host\n")
	put(t, filepath.Join(xferFiles, "sub", "deep.md"), "deep original\n")
	put(t, filepath.Join(xferFiles, "sub", "with space.md"), "contents of with space.md\n")
	put(t, filepath.Join(dir, "xfer", "etc", "passwd"), "not the library\n")
	docsFiles := filepath.Join(dir, "docs-files")
	put(t, filepath.Join(docsFiles, "notes", "plan.md"), "plan\n")
	indexOnly := filepath.Join(dir, "indexonly-index")
	os.MkdirAll(indexOnly, 0o755)
	os.MkdirAll(kbRoot, 0o755)
	b, _ := json.Marshal([]map[string]any{
		{"id": "docs", "label": "Docs", "indexRoot": filepath.Join(dir, "docs-index"), "sourceRoot": docsFiles},
		{"id": "idxonly", "label": "Index only", "indexRoot": indexOnly, "sourceRoot": nil},
	})
	put(t, filepath.Join(ix, "libraries.json"), string(b))
	projFiles := filepath.Join(dir, "gufihost", "proj")
	h := sha256.Sum256([]byte("single"))
	userDir := filepath.Join(ix, "users", hex.EncodeToString(h[:])[:16])
	libsFile := filepath.Join(userDir, "libraries.json")
	ub, _ := json.Marshal([]map[string]any{{"id": "u-plain", "label": "Proj u-plain", "transport": "ssh", "site": "me@gufi-host", "destination": "me@gufi-host",
		"port": nil, "resource": "x", "sourceDir": projFiles, "sourceRoot": projFiles, "indexRoot": filepath.Join(dir, "gufihost", "idx"), "gufiBin": "",
		"shell": false, "fullText": true, "state": "ready", "run": nil, "error": nil, "builtAt": time.Now().UTC().Format(time.RFC3339), "directories": 2, "files": 3, "scheduler": nil}})
	put(t, libsFile, string(ub))
	put(t, filepath.Join(userDir, "owner"), "single")

	const queryOnly = `case "$CMD" in
  gufi_query|"gufi_query "*) eval "$CMD" ;;
  */*) echo "restricted: cannot specify '/' in command names" >&2; exit 1 ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac`
	hosts := map[string]string{
		"me@gufi-host":       queryOnly,
		"me@xfer":            `eval "$CMD"`,
		"me@restricted-xfer": queryOnly,
	}
	down := `echo "ssh: connect to host xfer-down port 22: Connection refused" >&2; exit 255`
	t.Setenv("HOME", dir)
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0", "HOST": "127.0.0.1"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	// The one pool every package of this App takes (remote.Shared), with
	// the stand-in hosts.
	pool, _ := remote.Shared(a, func() remote.Options {
		return remote.Options{IndexBase: ix, Getenv: func(string) string { return "" },
			Spawn: func(site, key, command string) *exec.Cmd {
				script := down
				if sh, err := remote.ParseSSHSite(site); err == nil && sh != nil {
					if s, ok := hosts[sh.Destination]; ok {
						script = s
					}
				}
				c := exec.Command("/bin/sh", "-c", script)
				c.Env = []string{"HOME=" + filepath.Join(dir, "home"), "PATH=/usr/bin:/bin", "CMD=" + command}
				return c
			},
			SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} },
		}
	})
	t.Cleanup(pool.Close)
	mux := http.NewServeMux()
	kb.Register(mux, a)
	Register(mux, a)
	call := func(method, u string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			bb, _ := json.Marshal(body)
			rd = bytes.NewReader(bb)
		}
		r := httptest.NewRequest(method, u, rd)
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	obj := func(raw []byte) map[string]any {
		var m map[string]any
		json.Unmarshal(raw, &m)
		return m
	}
	type step struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	probe := func(id string, body any) (int, bool, []step) {
		t.Helper()
		code, raw := call("POST", "/api/libraries/"+id+"/read-through/test", body)
		var r struct {
			OK    bool   `json:"ok"`
			Steps []step `json:"steps"`
		}
		json.Unmarshal(raw, &r)
		return code, r.OK, r.Steps
	}
	match := func(what, re, s string) {
		t.Helper()
		if !regexp.MustCompile(re).MatchString(s) {
			t.Fatalf("%s: %q does not match %s", what, s, re)
		}
	}

	// The connection test.
	code, ok, steps := probe("docs", map[string]any{"destination": "me@xfer", "root": xferFiles})
	if code != 200 || !ok || len(steps) != 2 || steps[0].Name != "ssh" || !steps[0].OK || steps[1].Name != "source" || !steps[1].OK {
		t.Fatalf("ok: %d %v %+v", code, ok, steps)
	}
	match("ssh step", `^Connected to me@xfer`, steps[0].Detail)
	match("source step", "^"+regexp.QuoteMeta(xferFiles)+` is readable on me@xfer\.`, steps[1].Detail)
	_, ok, steps = probe("docs", map[string]any{"destination": "me@xfer", "root": filepath.Join(xferFiles, "nowhere")})
	if ok {
		t.Fatal("a missing folder passes")
	}
	match("no folder", `is not a directory on me@xfer`, steps[len(steps)-1].Detail)
	_, ok, steps = probe("docs", map[string]any{"destination": "me@xfer-down", "root": xferFiles})
	if ok {
		t.Fatal("a host that is down passes")
	}
	match("down", `Nothing accepted the connection`, steps[0].Detail)
	_, ok, steps = probe("docs", map[string]any{"destination": "me@restricted-xfer", "root": xferFiles})
	if ok {
		t.Fatal("a query-only host passes")
	}
	match("restricted", `runs only gufi_query for this login \(no shell\), so it cannot open files`, steps[0].Detail)
	_, _, steps = probe("idxonly", map[string]any{"destination": "me@xfer"})
	match("no own folder", `give the folder on that host`, steps[len(steps)-1].Detail)
	_, ok, steps = probe("docs", map[string]any{"destination": "me@xfer"})
	if !ok {
		t.Fatalf("the library's own folder at the same path: %+v", steps)
	}
	match("own folder", `is readable on me@xfer \(the same path as the library's own\)\.$`, steps[len(steps)-1].Detail)
	if code, _, _ := probe("docs", map[string]any{"destination": "-oProxyCommand=id"}); code != 400 {
		t.Fatalf("an option as a host: %d", code)
	}

	// Saving, the stored setting, and the viewer's file and download.
	code, raw := call("PUT", "/api/libraries/idxonly/read-through", map[string]any{"destination": "me@xfer-down", "root": xferFiles})
	if code != 400 {
		t.Fatalf("a failing test saves: %d %s", code, raw)
	}
	match("refused", `^The connection test did not pass\. Nothing accepted the connection`, obj(raw)["error"].(string))
	if code, _ := call("PUT", "/api/libraries/idxonly/read-through", map[string]any{"destination": "-oProxyCommand=id", "root": xferFiles}); code != 400 {
		t.Fatalf("an option as a host saves: %d", code)
	}
	code, raw = call("PUT", "/api/libraries/idxonly/read-through", map[string]any{"destination": "me@xfer", "root": xferFiles})
	if code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	var saved struct {
		ReadThrough json.RawMessage `json:"readThrough"`
	}
	json.Unmarshal(raw, &saved)
	if want, _ := json.Marshal(map[string]any{"destination": "me@xfer", "port": nil, "sshOptions": nil, "root": xferFiles}); !jsonEq(saved.ReadThrough, want) {
		t.Fatalf("saved %s", saved.ReadThrough)
	}
	stored := func(file, id string) map[string]any {
		var defs []map[string]any
		b, _ := os.ReadFile(file)
		json.Unmarshal(b, &defs)
		for _, d := range defs {
			if d["id"] == id {
				return d
			}
		}
		return nil
	}
	d := stored(filepath.Join(ix, "libraries.json"), "idxonly")
	if rt, _ := json.Marshal(d["readThrough"]); !jsonEq(rt, mustJSON(map[string]any{"destination": "me@xfer", "port": nil, "root": xferFiles})) || d["sourceRoot"] != nil {
		t.Fatalf("stored %v", d)
	}
	code, raw = call("GET", "/api/kb/file?library=idxonly&path=readme.md", nil)
	if code != 200 || obj(raw)["content"] != "readme on the transfer host\n" {
		t.Fatalf("file %d %s", code, raw)
	}
	code, raw = call("GET", "/api/kb/download?library=idxonly&path=sub/with%20space.md", nil)
	if code != 200 || string(raw) != "contents of with space.md\n" {
		t.Fatalf("download %d %q", code, raw)
	}
	code, raw = call("GET", "/api/kb/file?library=idxonly&path=nope.md", nil)
	if code != 404 {
		t.Fatalf("missing %d %s", code, raw)
	}
	match("missing", `^nope\.md could not be opened\. Tried this computer: this library has no files here; me@xfer: `, obj(raw)["error"].(string))
	if code, raw := call("GET", "/api/kb/file?library=idxonly&path=../etc/passwd", nil); code != 400 {
		t.Fatalf("outside %d %s", code, raw)
	}
	code, raw = call("PUT", "/api/libraries/idxonly/read-through", map[string]any{"destination": ""})
	if code != 200 || string(bytes.TrimSpace(raw)) != `{"readThrough":null}` {
		t.Fatalf("clear %d %s", code, raw)
	}
	if _, has := stored(filepath.Join(ix, "libraries.json"), "idxonly")["readThrough"]; has {
		t.Fatal("cleared, it is still stored")
	}
	if code, _ := call("GET", "/api/kb/file?library=idxonly&path=readme.md", nil); code != 409 {
		t.Fatalf("after clearing: %d", code)
	}

	// A library over ssh keeps the host with its other settings.
	code, raw = call("PUT", "/api/libraries/u-plain/read-through", map[string]any{"destination": "me@xfer", "root": xferFiles})
	if code != 200 {
		t.Fatalf("u-plain %d %s", code, raw)
	}
	if u := stored(libsFile, "u-plain"); u["transport"] != "ssh" || u["readThrough"].(map[string]any)["destination"] != "me@xfer" {
		t.Fatalf("stored %v", u)
	}
	code, raw = call("GET", "/api/kb/file?library=u-plain&path=sub/deep.md", nil)
	if code != 200 || obj(raw)["content"] != "deep original\n" {
		t.Fatalf("u-plain file %d %s", code, raw)
	}
	code, raw = call("GET", "/api/kb/file?library=all&path=u-plain/readme.md", nil)
	if code != 200 || obj(raw)["content"] != "readme on the transfer host\n" || obj(raw)["path"] != "u-plain/readme.md" {
		t.Fatalf("all %d %s", code, raw)
	}
	if code, _ := call("PUT", "/api/libraries/u-plain/read-through", map[string]any{"destination": ""}); code != 200 {
		t.Fatalf("clear u-plain %d", code)
	}

	// The main folder and one that does not exist take no host.
	if code, _ := call("PUT", "/api/libraries/kb/read-through", map[string]any{"destination": "me@xfer", "root": xferFiles}); code != 409 {
		t.Fatalf("kb %d", code)
	}
	if code, _ := call("PUT", "/api/libraries/no-such/read-through", map[string]any{"destination": "me@xfer", "root": xferFiles}); code != 404 {
		t.Fatalf("no-such %d", code)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func jsonEq(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return string(mustJSON(x)) == string(mustJSON(y))
}
