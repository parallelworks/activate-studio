package updates

// The cases of server/test/updates.test.mjs, updateChannels.test.mjs and
// updatesLinux.test.mjs that test the updater itself, against a stand-in
// for GitHub's releases API on 127.0.0.1, with throwaway keys.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
	"github.com/parallelworks/activate-studio/server-go/internal/state"
)

type keys struct {
	pub  []byte
	priv ed25519.PrivateKey
}

func newKeys(t *testing.T) keys {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return keys{pub: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), priv: priv}
}

func write(t *testing.T, file, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// layout is what a copy of the app holds: Node's server, Go's, or Go's
// with the placeholders old Node updaters look for (the bridge).
type layout int

const (
	nodeLayout layout = iota
	goLayout
	bridgeLayout
)

// macApp writes an app bundle at dir.
func macApp(t *testing.T, dir, version string, pub []byte, l layout, shell string) {
	write(t, filepath.Join(dir, "Contents", "Info.plist"), `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>ACTIVATE Studio</string>
	<key>CFBundleIdentifier</key>
	<string>com.parallelworks.activate-studio</string>
</dict>
</plist>
`, 0o644)
	write(t, filepath.Join(dir, "Contents", "MacOS", "ACTIVATE Studio"), shell, 0o755)
	res := filepath.Join(dir, "Contents", "Resources")
	tree(t, res, version, pub, l)
}

// tree writes the shared part: the run loop, the server and build-info.json.
func tree(t *testing.T, res, version string, pub []byte, l layout) {
	write(t, filepath.Join(res, "studio-run.sh"), "#!/bin/sh\n", 0o755)
	if l == nodeLayout || l == bridgeLayout {
		write(t, filepath.Join(res, "node", "bin", "node"), "#!/bin/sh\n", 0o755)
		write(t, filepath.Join(res, "app", "server", "dist", "main.js"), "// server\n", 0o644)
	}
	if l == goLayout || l == bridgeLayout {
		write(t, filepath.Join(res, "app", "bin", "studio-server"), "#!/bin/sh\n", 0o755)
	}
	write(t, filepath.Join(res, "app", "build-info.json"), fmt.Sprintf("{\n \"version\": %q,\n \"builtAt\": \"2026-10-01T00:00:00Z\"\n}\n", version), 0o644)
	if pub != nil {
		write(t, filepath.Join(res, "update-signing.pub"), string(pub), 0o644)
	}
}

func linuxApp(t *testing.T, dir, version string, pub []byte, l layout, shell string) {
	tree(t, dir, version, pub, l)
	write(t, filepath.Join(dir, "manifest.json"), `{"version": "`+version+`", "shell": "bin/activate-studio-wk41"}`, 0o644)
	write(t, filepath.Join(dir, "bin", "activate-studio-wk41"), shell, 0o755)
	write(t, filepath.Join(dir, "bin", "activate-studio"), "#!/bin/sh\n", 0o755)
}

func archive(t *testing.T, build func(dir string), top string) []byte {
	work := t.TempDir()
	build(filepath.Join(work, top))
	var out bytes.Buffer
	cmd := exec.Command("tar", "-czf", "-", "-C", work, top)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// feed is the stand-in for GitHub.
type feed struct {
	mu       sync.Mutex
	srv      *httptest.Server
	files    map[string][]byte
	releases []map[string]any
	etag     string
	seq      int
}

func newFeed(t *testing.T) *feed {
	f := &feed{files: map[string][]byte{}, etag: `"r0"`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/repos/parallelworks/activate-studio/releases") {
			if r.Header.Get("If-None-Match") == f.etag {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("Etag", f.etag)
			json.NewEncoder(w).Encode(f.releases)
			return
		}
		b, ok := f.files[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type relOpts struct {
	manifest   map[string]any
	signWith   ed25519.PrivateKey
	prerelease bool
	corrupt    bool
}

func (f *feed) release(tag, asset string, arc []byte, o relOpts) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	sum := sha256.Sum256(arc)
	man := map[string]any{"version": tag, "asset": asset, "sha256": hex.EncodeToString(sum[:]), "size": len(arc), "builtAt": "2026-10-20T00:00:00Z"}
	for k, v := range o.manifest {
		if v == nil {
			delete(man, k)
		} else {
			man[k] = v
		}
	}
	mb, _ := json.MarshalIndent(man, "", " ")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(o.signWith, mb)) + "\n"
	manifestName := strings.TrimSuffix(asset, ".tar.gz") + ".manifest.json"
	body := arc
	if o.corrupt {
		body = append(append([]byte{}, arc...), 'x')
		body = body[1:]
	}
	var assets []map[string]any
	for name, b := range map[string][]byte{asset: body, manifestName: mb, manifestName + ".sig": []byte(sig)} {
		p := fmt.Sprintf("/dl/%d/%s", f.seq, name)
		f.files[p] = b
		assets = append(assets, map[string]any{"name": name, "size": len(b), "browser_download_url": f.srv.URL + p})
	}
	return map[string]any{"tag_name": tag, "draft": false, "prerelease": o.prerelease, "published_at": "2026-10-20T12:00:00Z",
		"body": "### Change in " + tag + " (#1)\n\nDetails.", "html_url": f.srv.URL + "/releases/" + tag, "assets": assets}
}

func (f *feed) set(rels ...map[string]any) {
	f.mu.Lock()
	f.releases = rels
	f.seq++
	f.etag = fmt.Sprintf(`"r%d"`, f.seq)
	f.mu.Unlock()
}

// fixture is one install with its data folder.
type fixture struct {
	t        *testing.T
	root     string
	app      string
	data     string
	env      map[string]string
	keys     keys
	feed     *feed
	u        *Updater
	restarts []int
	calls    string
	version  string
	mu       sync.Mutex
}

func (fx *fixture) getenv(k string) string { return fx.env[k] }

func newFixture(t *testing.T, flavor, version string, l layout) *fixture {
	root := t.TempDir()
	fx := &fixture{t: t, root: root, data: filepath.Join(root, "data"), keys: newKeys(t), feed: newFeed(t), version: version}
	fx.calls = filepath.Join(root, "calls")
	bin := filepath.Join(root, "bin")
	write(t, filepath.Join(bin, "launchctl"), "#!/bin/sh\necho \"launchctl $*\" >>\""+fx.calls+"\"\n", 0o755)
	write(t, filepath.Join(bin, "lsregister"), "#!/bin/sh\necho \"lsregister $*\" >>\""+fx.calls+"\"\n", 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	fx.env = map[string]string{"STUDIO_DATA_DIR": fx.data, "STUDIO_UPDATE_URL": fx.feed.srv.URL, "STUDIO_LSREGISTER": filepath.Join(bin, "lsregister")}
	projectRoot := ""
	if flavor == "macos" {
		fx.app = filepath.Join(root, "Applications", "ACTIVATE Studio.app")
		macApp(t, fx.app, version, fx.keys.pub, l, "#!/bin/sh\n# shell 1\n")
		fx.env["STUDIO_APP_BUNDLE"] = fx.app
	} else {
		fx.app = filepath.Join(root, "lib", "activate-studio")
		linuxApp(t, fx.app, version, fx.keys.pub, l, "#!/bin/sh\n# shell 1\n")
		write(t, filepath.Join(fx.app, "install-kind"), "user\n", 0o644)
		projectRoot = filepath.Join(fx.app, "app")
	}
	index := filepath.Join(fx.data, "index")
	os.MkdirAll(index, 0o755)
	store := settings.Open(index, filepath.Join(root, "kb"), false, func(k string) (string, bool) { v, ok := fx.env[k]; return v, ok })
	fx.u = New(Options{
		ProjectRoot: projectRoot, IndexBase: index, Getenv: fx.getenv, Settings: store, GOOS: "linux", GOARCH: "amd64",
		Restart: func(code int) bool { fx.mu.Lock(); fx.restarts = append(fx.restarts, code); fx.mu.Unlock(); return true },
		Version: func() (string, *string) { fx.mu.Lock(); defer fx.mu.Unlock(); return fx.version, nil },
		SystemVersion: func() string { return "14.5" },
		Timings:       Timings{FirstCheck: time.Hour, Interval: time.Hour, Soon: time.Hour, Poll: time.Hour, Idle: 50 * time.Millisecond, IdlePoll: 10 * time.Millisecond, Healthy: time.Hour},
	})
	t.Cleanup(fx.u.Stop)
	return fx
}

func (fx *fixture) asset() string {
	if fx.env["STUDIO_APP_BUNDLE"] != "" {
		return "activate-studio-macos-arm64.tar.gz"
	}
	return "activate-studio-linux-x86_64.tar.gz"
}

// build is the archive of a copy at version.
func (fx *fixture) build(version string, l layout, shell string) []byte {
	if fx.env["STUDIO_APP_BUNDLE"] != "" {
		return archive(fx.t, func(d string) { macApp(fx.t, d, version, fx.keys.pub, l, shell) }, "ACTIVATE Studio.app")
	}
	return archive(fx.t, func(d string) { linuxApp(fx.t, d, version, fx.keys.pub, l, shell) }, "activate-studio")
}

func (fx *fixture) rel(tag string, l layout, o relOpts) map[string]any {
	if o.signWith == nil {
		o.signWith = fx.keys.priv
	}
	return fx.feed.release(tag, fx.asset(), fx.build(tag, l, "#!/bin/sh\n# shell "+tag+"\n"), o)
}

func (fx *fixture) settle() Status {
	fx.t.Helper()
	end := time.Now().Add(15 * time.Second)
	for time.Now().Before(end) {
		fx.u.mu.Lock()
		busy := fx.u.op != nil
		fx.u.mu.Unlock()
		s := fx.u.Status()
		if !busy && (s.State == "restarting" || s.State == "error" || s.State == "idle" || s.State == "relaunch") {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	fx.t.Fatal("the install did not settle")
	return Status{}
}

func (fx *fixture) versionIn(dir string) string { return fx.u.versionOf(dir) }

func (fx *fixture) setPolicy(p map[string]any) {
	if err := fx.u.o.Settings.Update(map[string]any{"updates": p}); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *fixture) install(body map[string]any) (int, string) {
	return call(fx, "POST", "/api/updates/install", body)
}

func call(fx *fixture, method, path string, body map[string]any) (int, string) {
	mux := http.NewServeMux()
	Routes(fx.u, mux, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestVersions(t *testing.T) {
	if !IsNewer("v1.140", "v1.139") || !IsNewer("v1.139", "v1.99") || IsNewer("v1.137-6-g3140181", "v1.137") {
		t.Fatal("version order")
	}
	if !IsNewer("v1.1", "dev") || IsNewer("dev", "v1.1") {
		t.Fatal("dev is older than any release")
	}
	if !dottedAtMost("14.0", "14.5") || dottedAtMost("15", "14.5") || !dottedAtMost("2.28", "2.35") {
		t.Fatal("dottedAtMost")
	}
}

func TestCloneAndHosted(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	delete(fx.env, "STUDIO_APP_BUNDLE")
	fx.feed.set(fx.rel("v1.140", nodeLayout, relOpts{}))
	fx.u.Check()
	s := fx.u.Status()
	if s.Mode != "clone" || !s.Available || s.CanInstall || s.Command == nil || *s.Command != cloneCommand {
		t.Fatalf("clone: %+v", s)
	}
	if code, _ := fx.install(nil); code != 409 {
		t.Fatalf("a clone refuses to install: %d", code)
	}
	fx.env["PW_PLATFORM_HOST"] = "platform.example.com"
	if code, body := call(fx, "GET", "/api/updates", nil); code != 403 || !strings.Contains(body, "updated by its deployment") {
		t.Fatalf("hosted: %d %s", code, body)
	}
	if code, _ := call(fx, "GET", "/api/changelog", nil); code != 200 {
		t.Fatal("the change log answers in every mode")
	}
}

// The status's keys, in updates.ts's order: the web app and Node's tests
// read the object as Node writes it.
func TestStatusKeyOrder(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	_, body := call(fx, "GET", "/api/updates", nil)
	want := []string{"mode", "current", "policy", "latest", "releases", "available", "canInstall", "reason", "command", "commandNote",
		"package", "state", "progress", "error", "checkedAt", "offline", "previous", "reopen", "versions", "moveToStable", "stateFormat", "snapshots"}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.Token()
	var got []string
	for dec.More() {
		k, _ := dec.Token()
		got = append(got, k.(string))
		var skip json.RawMessage
		dec.Decode(&skip)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys\n got %v\nwant %v", got, want)
	}
	var s map[string]any
	json.Unmarshal([]byte(body), &s)
	if p := s["policy"].(map[string]any); p["channel"] != "stable" || p["install"] != "manual" || p["check"] != true {
		t.Fatalf("policy %v", p)
	}
}

func TestMacInstallKickstartsTheAgent(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	os.WriteFile(filepath.Join(fx.u.o.IndexBase, "settings.json"), []byte("{}\n"), 0o644)
	fx.feed.set(fx.rel("v1.140", bridgeLayout, relOpts{}))
	if code, body := fx.install(nil); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	s := fx.settle()
	if s.State != "restarting" {
		t.Fatalf("state %s error %v", s.State, deref(s.Error))
	}
	if v := fx.versionIn(fx.app); v != "v1.140" {
		t.Fatalf("app is %s", v)
	}
	if v := fx.versionIn(filepath.Join(filepath.Dir(fx.app), ".ACTIVATE Studio.app.previous")); v != "v1.139" {
		t.Fatalf("previous is %s", v)
	}
	if !exists(filepath.Join(fx.data, "updates", "pending.json")) || !exists(filepath.Join(fx.data, "updates", "reopen")) {
		t.Fatal("pending.json and reopen (the window's program changed)")
	}
	time.Sleep(200 * time.Millisecond)
	calls, _ := os.ReadFile(fx.calls)
	if !strings.Contains(string(calls), fmt.Sprintf("launchctl kickstart -k gui/%d/com.parallelworks.activate-studio", os.Getuid())) {
		t.Fatalf("calls: %s", calls)
	}
	if !strings.Contains(string(calls), "lsregister -f "+fx.app) {
		t.Fatalf("LaunchServices not told: %s", calls)
	}
	if len(fx.u.state.ListSnapshots()) != 1 {
		t.Fatal("a snapshot before the update")
	}
}

func TestDetachedRunLoopRestartsWith76(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	fx.env["STUDIO_SUPERVISOR"] = "detached"
	fx.feed.set(fx.rel("v1.140", nodeLayout, relOpts{}))
	fx.install(nil)
	fx.settle()
	if len(fx.restarts) != 1 || fx.restarts[0] != RelaunchExitCode {
		t.Fatalf("restarts %v", fx.restarts)
	}
}

func TestRefusals(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	stranger := newKeys(t)
	outside := archive(t, func(d string) { macApp(t, d, "v1.140", fx.keys.pub, nodeLayout, "x") }, "ACTIVATE Studio.app")
	_ = outside
	cases := []struct {
		name string
		rel  map[string]any
		want string
	}{
		{"signature", fx.rel("v1.140", nodeLayout, relOpts{signWith: stranger.priv}), "signature does not verify"},
		{"asset", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"asset": "other.tar.gz"}}), "names the asset other.tar.gz"},
		{"version", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"version": "v1.141"}}), "is for v1.141, but the release is v1.140"},
		{"size", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"size": 10}}), "larger than the 10 bytes"},
		{"sha", fx.rel("v1.140", nodeLayout, relOpts{corrupt: true}), "SHA-256 does not match"},
		{"macos", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"minMacos": "15.0"}}), "needs macOS 15.0; this Mac has 14.5"},
		{"preview on stable", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"channel": "preview"}}), "Preview release, but it was published as a stable release"},
		{"channel", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"channel": "beta"}}), "channel beta is not stable or preview"},
		{"stateFormat", fx.rel("v1.140", nodeLayout, relOpts{manifest: map[string]any{"stateFormat": 1.5}}), "stateFormat is not a whole number"},
	}
	// A copy with no server at all.
	noServer := fx.feed.release("v1.140", fx.asset(), archive(t, func(d string) {
		macApp(t, d, "v1.140", fx.keys.pub, goLayout, "x")
		os.Remove(filepath.Join(d, "Contents", "Resources", "app", "bin", "studio-server"))
	}, "ACTIVATE Studio.app"), relOpts{signWith: fx.keys.priv})
	cases = append(cases, struct {
		name string
		rel  map[string]any
		want string
	}{"no server", noServer, "has no Resources/node/bin/node"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx.u.mu.Lock()
			fx.u.st = status{State: "idle"}
			fx.u.mu.Unlock()
			fx.feed.set(c.rel)
			fx.u.Check()
			fx.install(nil)
			s := fx.settle()
			if s.State != "error" || !strings.Contains(deref(s.Error), c.want) {
				t.Fatalf("state %s error %q, want %q", s.State, deref(s.Error), c.want)
			}
			if v := fx.versionIn(fx.app); v != "v1.139" {
				t.Fatalf("app changed to %s", v)
			}
			if exists(filepath.Join(filepath.Dir(fx.app), ".ACTIVATE Studio.app.new")) {
				t.Fatal("left a staged copy")
			}
		})
	}
}

func TestRollbackAndAutoSkips(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	fx.feed.set(fx.rel("v1.140", goLayout, relOpts{}))
	fx.install(nil)
	fx.settle()
	fx.mu.Lock()
	fx.version = "v1.140"
	fx.mu.Unlock()
	fx.u.mu.Lock()
	fx.u.st = status{State: "idle"}
	fx.u.mu.Unlock()
	if code, body := call(fx, "POST", "/api/updates/rollback", nil); code != 200 {
		t.Fatalf("rollback %d %s", code, body)
	}
	if v := fx.versionIn(fx.app); v != "v1.139" {
		t.Fatalf("rolled back to %s", v)
	}
	var back map[string]any
	readJSON(filepath.Join(fx.data, "updates", "rolled-back.json"), &back)
	if back["from"] != "v1.140" || back["to"] != "v1.139" || back["noted"] != true {
		t.Fatalf("rolled-back.json %v", back)
	}
	if exists(filepath.Join(fx.data, "updates", "pending.json")) {
		t.Fatal("pending.json stays after a rollback")
	}
}

func TestAutoInstallWaitsForIdle(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	fx.setPolicy(map[string]any{"check": true, "install": "auto", "channel": "stable"})
	var busy sync.Mutex
	isBusy := true
	fx.u.o.Busy = func() bool { busy.Lock(); defer busy.Unlock(); return isBusy }
	fx.feed.set(fx.rel("v1.140", nodeLayout, relOpts{}))
	fx.u.Check()
	end := time.Now().Add(10 * time.Second)
	for {
		fx.u.mu.Lock()
		ready := fx.u.prepared != nil
		fx.u.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(end) {
			t.Fatal("never verified")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if v := fx.versionIn(fx.app); v != "v1.139" {
		t.Fatal("installed while busy")
	}
	busy.Lock()
	isBusy = false
	busy.Unlock()
	for time.Now().Before(end) && fx.versionIn(fx.app) != "v1.140" {
		time.Sleep(20 * time.Millisecond)
	}
	if v := fx.versionIn(fx.app); v != "v1.140" {
		t.Fatalf("not installed once idle: %s", v)
	}
}

func TestChannelsPickerAndMoveToStable(t *testing.T) {
	fx := newFixture(t, "macos", "v1.142", nodeLayout)
	fx.feed.set(
		fx.rel("v1.143", nodeLayout, relOpts{prerelease: true, manifest: map[string]any{"channel": "preview"}}),
		fx.rel("v1.141", nodeLayout, relOpts{}),
		fx.rel("v1.140", nodeLayout, relOpts{}),
	)
	fx.u.Check()
	s := fx.u.Status()
	if s.Available || s.MoveToStable == nil || s.MoveToStable.Version != "v1.141" || len(s.Versions) != 2 {
		t.Fatalf("stable: available %v move %v versions %v", s.Available, s.MoveToStable, s.Versions)
	}
	fx.setPolicy(map[string]any{"check": true, "install": "manual", "channel": "prerelease"})
	s = fx.u.Status()
	if s.Policy.Channel != "preview" || !s.Available || s.Latest.Version != "v1.143" || len(s.Versions) != 3 || s.MoveToStable != nil {
		t.Fatalf("preview (the older setting value): %+v", s)
	}
	// Inspect, then install an older version by choice.
	code, body := call(fx, "POST", "/api/updates/inspect", map[string]any{"version": "v1.140"})
	if code != 200 || !strings.Contains(body, `"older":true`) || !strings.Contains(body, `"readable":true`) {
		t.Fatalf("inspect %d %s", code, body)
	}
	if code, body := fx.install(map[string]any{"version": "v1.140"}); code != 200 {
		t.Fatalf("install older %d %s", code, body)
	}
	fx.settle()
	if v := fx.versionIn(fx.app); v != "v1.140" {
		t.Fatalf("installed %s", v)
	}
	snaps := fx.u.state.ListSnapshots()
	if len(snaps) == 0 || snaps[0].Reason != "before installing an older version" {
		t.Fatalf("snapshots %v", snaps)
	}
}

func TestStateNewerFormatNeedsSnapshot(t *testing.T) {
	fx := newFixture(t, "macos", "v1.150", nodeLayout)
	idx := fx.u.o.IndexBase
	write(t, filepath.Join(idx, "conversations.json"), `{"v":1}`, 0o644)
	write(t, filepath.Join(idx, "tasks", "m1", "board.json"), `{}`, 0o644)
	write(t, filepath.Join(idx, "tasks", "m1", "work", "agent", "live.log"), `scratch`, 0o644)
	write(t, filepath.Join(idx, "history", "snap.db"), `x`, 0o644)
	fx.u.state.WriteMarker(1, "v1.149")
	old, err := fx.u.state.TakeSnapshot(state.SnapshotOpts{From: "v1.149", To: strp("v1.150"), Reason: "before an update"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range old.Files {
		if strings.Contains(f, "work") || strings.HasPrefix(f, "history") {
			t.Fatalf("snapshot holds %s", f)
		}
	}
	write(t, filepath.Join(idx, "conversations.json"), `{"v":2}`, 0o644)
	fx.u.state.WriteMarker(2, "v1.150")
	fx.feed.set(fx.rel("v1.149", nodeLayout, relOpts{}))
	fx.u.Check()
	_, body := call(fx, "POST", "/api/updates/inspect", map[string]any{"version": "v1.149"})
	if !strings.Contains(body, `"readable":false`) || !strings.Contains(body, old.ID) {
		t.Fatalf("inspect %s", body)
	}
	fx.install(map[string]any{"version": "v1.149"})
	if s := fx.settle(); s.State != "error" || !strings.Contains(deref(s.Error), "has to be asked for") {
		t.Fatalf("without restoreState: %s %v", s.State, deref(s.Error))
	}
	fx.u.mu.Lock()
	fx.u.st = status{State: "idle"}
	fx.u.mu.Unlock()
	fx.install(map[string]any{"version": "v1.149", "restoreState": true})
	fx.settle()
	b, _ := os.ReadFile(filepath.Join(idx, "conversations.json"))
	if string(b) != `{"v":1}` || fx.u.state.StateFormat() != 1 {
		t.Fatalf("state %s format %d", b, fx.u.state.StateFormat())
	}
	// The newer data is kept in the snapshot taken when it was replaced.
	found := false
	for _, s := range fx.u.state.ListSnapshots() {
		if s.Format == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("the format 2 state was not kept in a snapshot")
	}
}

func TestRequiresRelaunch(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	fx.feed.set(fx.rel("v1.140", goLayout, relOpts{manifest: map[string]any{"requiresRelaunch": true}}))
	fx.install(nil)
	s := fx.settle()
	if s.State != "relaunch" || len(fx.restarts) != 0 || !exists(filepath.Join(fx.data, "updates", "relaunch.json")) {
		t.Fatalf("state %s restarts %v err %v", s.State, fx.restarts, deref(s.Error))
	}
}

// The bridge: a Go copy of the Linux payload, with the placeholders a
// Node updater looks for, and one without them, both install; the swap
// journal is gone and the window program's change is noted.
func TestLinuxGoPayloadInstalls(t *testing.T) {
	for _, l := range []layout{bridgeLayout, goLayout} {
		fx := newFixture(t, "linux", "v1.188", nodeLayout)
		if fx.u.Mode() != "app" {
			t.Fatalf("mode %s", fx.u.Mode())
		}
		fx.feed.set(fx.rel("v1.190", l, relOpts{manifest: map[string]any{"minGlibc": "2.28"}}))
		fx.install(nil)
		s := fx.settle()
		if s.State != "restarting" || len(fx.restarts) != 1 || fx.restarts[0] != 76 {
			t.Fatalf("layout %d: %s %v %v", l, s.State, deref(s.Error), fx.restarts)
		}
		if b, _ := os.ReadFile(filepath.Join(fx.app, "install-kind")); string(b) != "user\n" {
			t.Fatalf("install-kind %q", b)
		}
		if exists(filepath.Join(fx.data, "updates", "swap.json")) || !exists(filepath.Join(fx.data, "updates", "reopen")) {
			t.Fatal("swap.json left, or reopen missing")
		}
	}
}

func TestLinuxPackageInstallsDoNotUpdate(t *testing.T) {
	fx := newFixture(t, "linux", "v1.188", nodeLayout)
	write(t, filepath.Join(fx.app, "install-kind"), "deb\n", 0o644)
	fx.feed.set(fx.rel("v1.190", goLayout, relOpts{}))
	fx.u.Check()
	s := fx.u.Status()
	if s.Mode != "package" || s.Command == nil || !strings.Contains(*s.Command, "apt install --only-upgrade activate-studio") || s.Package.Kind != "deb" {
		t.Fatalf("%+v", s)
	}
	if code, _ := fx.install(nil); code != 409 {
		t.Fatal("a package install refuses")
	}
}

func TestChangelog(t *testing.T) {
	fx := newFixture(t, "macos", "v1.141", nodeLayout)
	cl := filepath.Join(fx.root, "CHANGELOG.md")
	write(t, cl, "# Change log\n\n## v1.140 (2026-10-01)\n\n### Fix a thing (#5)\n\nBody.\n\n```\n## v9.9 not a version\n```\n\n### Change log through v1.139 (#6)\n\n## v1.139\n\n_No pull requests; tag only._\n", 0o644)
	fx.env["STUDIO_CHANGELOG_FILE"] = cl
	fx.feed.set(fx.rel("v1.141", nodeLayout, relOpts{}), fx.rel("v1.142", nodeLayout, relOpts{}))
	fx.u.Check()
	c := fx.u.Changelog("")
	if !c.Included || len(c.Versions) != 2 || c.Versions[0].Version != "v1.141" || c.Versions[1].Entries[0].Title != "Fix a thing" {
		b, _ := json.Marshal(c)
		t.Fatalf("%s", b)
	}
	if !strings.Contains(c.Versions[1].Entries[0].Body, "## v9.9") {
		t.Fatal("a heading inside a code block is body text")
	}
	if c := fx.u.Changelog("v1.140"); len(c.Versions) != 1 {
		t.Fatal("after")
	}
}

func TestOfflineIsNotAnError(t *testing.T) {
	fx := newFixture(t, "macos", "v1.139", nodeLayout)
	fx.env["STUDIO_UPDATE_URL"] = "http://127.0.0.1:1"
	fx.u.Check()
	s := fx.u.Status()
	if s.State != "idle" || !s.Offline || s.Error != nil {
		t.Fatalf("%s %v %v", s.State, s.Offline, deref(s.Error))
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
