package userlibs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The cases of the Node tests of these routes (sshLibraries, pwLibraries,
// pwSystems, sshOptions, remoteBuild, restrictedRemote, userLibraries),
// against the Go routes. The remote systems are stand-ins, as there: each
// ssh or pw ssh session runs in a local bash whose HOME is that system's,
// so the scripts really run, with this machine's GUFI and Python. Nothing
// reaches a platform or a host.

const gufiDir = "/opt/gufi/bin"

func gufiBinDir() string {
	if v := os.Getenv("GUFI_BIN"); v != "" {
		return v
	}
	return gufiDir
}

func needGufi(t *testing.T) string {
	t.Helper()
	b := gufiBinDir()
	for _, tool := range []string{"gufi_query", "gufi_dir2index"} {
		if _, err := os.Stat(filepath.Join(b, tool)); err != nil {
			t.Skip("GUFI not installed")
		}
	}
	return b
}

func write(t *testing.T, f, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exe(t *testing.T, f, body string) {
	t.Helper()
	write(t, f, body)
	os.Chmod(f, 0o755)
}

type session struct{ site, key string }

type harness struct {
	t        *testing.T
	dir      string
	m        *Module
	mux      *http.ServeMux
	env      map[string]string
	homes    map[string]string
	failing  map[string]string // destination or cluster: what its session prints
	failCode int
	path     map[string]string // extra PATH entries first, by destination
	mu       sync.Mutex
	sessions []session
	auth     bool
	user     *auth.User
	cookie   string
}

var noNetwork = []string{"https_proxy=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9"}

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, homes: map[string]string{}, failing: map[string]string{}, failCode: 255, path: map[string]string{}}
	h.env = map[string]string{
		"PW_GATEWAY_URL":     "https://platform.test/api/openai/v1",
		"PW_CREDENTIALS_DIR": filepath.Join(dir, "creds"),
		"XDG_RUNTIME_DIR":    filepath.Join(dir, "run"),
		"PATH":               "/usr/bin:/bin",
	}
	os.MkdirAll(h.env["XDG_RUNTIME_DIR"], 0o700)
	for k, v := range env {
		h.env[k] = v
	}
	ix := filepath.Join(dir, "ix")
	os.MkdirAll(ix, 0o755)
	lookup := func(k string) (string, bool) { v, ok := h.env[k]; return v, ok }
	getenv := func(k string) string { return h.env[k] }
	p := platform.New(platform.Options{IndexBase: ix, KBRoot: filepath.Join(dir, "kb"), LookupEnv: lookup, Home: filepath.Join(dir, "localhome"),
		AuthEnabled: func() bool { return h.auth }})
	pool := remote.New(remote.Options{IndexBase: ix, Getenv: getenv, PwCLI: p.CLI, GatewayBase: p.GatewayBase,
		Spawn: h.spawn,
		// No ssh on this computer is asked anything (ssh -G, a master).
		SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} },
	})
	t.Cleanup(pool.Close)
	root, _ := filepath.Abs("../../..")
	h.m = New(p, pool, nil, ix, root, nil)
	h.m.PollEvery = 50 * time.Millisecond
	h.m.offline = func(string) string { return "offline" } // nothing is downloaded here
	jobs := &kb.Jobs{}
	h.m.Jobs = func() *kb.Jobs { return jobs }
	h.mux = http.NewServeMux()
	h.m.Routes(h.mux, nil)
	return h
}

func (h *harness) spawn(site, key, command string) *exec.Cmd {
	name := site
	if remote.IsSSH(site) {
		if s, err := remote.ParseSSHSite(site); err == nil && s != nil {
			name = s.Destination
		}
	}
	h.mu.Lock()
	h.sessions = append(h.sessions, session{site, key})
	h.mu.Unlock()
	if msg, ok := h.failing[name]; ok {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf("echo %q >&2; exit %d", msg, h.failCode))
	}
	home := h.homes[name]
	if home == "" {
		return exec.Command("/bin/sh", "-c", `echo "ssh: Could not resolve hostname" >&2; exit 255`)
	}
	cmd := exec.Command("bash", "-c", command)
	pathv := "/usr/bin:/bin"
	if extra := h.path[name]; extra != "" {
		pathv = extra + ":" + pathv
	}
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + pathv}, noNetwork...)
	return cmd
}

func (h *harness) sawSession(site, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.site == site && s.key == key {
			return true
		}
	}
	return false
}

type answer struct {
	Status int
	Body   map[string]any
}

func (h *harness) do(method, url string, body any) answer {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, url, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if h.cookie != "" {
		r.Header.Set("Cookie", h.cookie)
	}
	if h.user != nil {
		r = auth.WithUser(r, h.user)
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("%s %s: %d %q", method, url, w.Code, w.Body.String())
	}
	return answer{w.Code, out}
}

func (h *harness) post(url string, body any) answer { return h.do(http.MethodPost, url, body) }
func (h *harness) listing() map[string]any {
	return h.do(http.MethodGet, "/api/user-libraries", nil).Body
}

func steps(a answer) [][2]any {
	var out [][2]any
	for _, s := range a.Body["steps"].([]any) {
		m := s.(map[string]any)
		out = append(out, [2]any{m["name"], m["ok"]})
	}
	return out
}

func stepDetail(a answer, name string) string {
	for _, s := range a.Body["steps"].([]any) {
		m := s.(map[string]any)
		if m["name"] == name {
			return m["detail"].(string)
		}
	}
	return ""
}

func firstFailed(a answer) string {
	for _, s := range a.Body["steps"].([]any) {
		m := s.(map[string]any)
		if m["ok"] == false {
			return m["detail"].(string)
		}
	}
	return ""
}

func eq(t *testing.T, got, want any, what string) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("%s: got %s, want %s", what, g, w)
	}
}

func match(t *testing.T, s any, re, what string) {
	t.Helper()
	str, _ := s.(string)
	if !regexp.MustCompile(re).MatchString(str) {
		t.Errorf("%s: %q does not match %s", what, str, re)
	}
}

func (h *harness) library(id string) map[string]any {
	for _, l := range h.listing()["libraries"].([]any) {
		if m := l.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func (h *harness) waitFor(id string, states ...string) map[string]any {
	h.t.Helper()
	if len(states) == 0 {
		states = []string{"ready", "failed"}
	}
	for i := 0; i < 1200; i++ {
		if l := h.library(id); l != nil {
			for _, s := range states {
				if l["state"] == s {
					return l
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("%s did not finish: %v", id, h.library(id))
	return nil
}

// ---- over plain ssh (sshLibraries.test.mjs) ----

func sshHarness(t *testing.T) (*harness, string, string) {
	gufi := needGufi(t)
	h := newHarness(t, nil)
	home := filepath.Join(h.dir, "home")
	write(t, filepath.Join(home, "proj", "readme.txt"), "hello from the remote\n")
	write(t, filepath.Join(home, "proj", "data", "huge.bin"), strings.Repeat("b", 5000))
	write(t, filepath.Join(home, "proj", "data", "notes.md"), "# notes\n")
	write(t, filepath.Join(home, "holder", "sub", "db.db"), "")
	os.MkdirAll(filepath.Join(home, "idx"), 0o755)
	if out, err := exec.Command(filepath.Join(gufi, "gufi_dir2index"), "-x", filepath.Join(home, "proj"), filepath.Join(home, "idx")).CombinedOutput(); err != nil {
		t.Fatalf("gufi_dir2index: %v %s", err, out)
	}
	for _, d := range []string{"me@linux-box", "me@mac-box", "me@slow-box"} {
		h.homes[d] = home
	}
	h.failing = map[string]string{
		"me@nokey":   "me@nokey: Permission denied (publickey,password).",
		"me@nohost":  "ssh: Could not resolve hostname nohost: Name or service not known",
		"me@newhost": "Host key verification failed.",
		"me@closed":  "ssh: connect to host closed port 22: Connection refused",
	}
	// The macOS host: uname says Darwin, and its GUFI is found on the PATH.
	macBin := filepath.Join(h.dir, "mac-bin")
	exe(t, filepath.Join(macBin, "uname"), "#!/bin/sh\necho Darwin\n")
	macGufi := filepath.Join(h.dir, "mac-gufi", "bin")
	exe(t, filepath.Join(macGufi, "gufi_query"), "#!/bin/sh\nexec "+gufi+"/gufi_query \"$@\"\n")
	h.path["me@mac-box"] = macBin + ":" + macGufi
	// A host with three processors.
	cpus := filepath.Join(h.dir, "cpus-3")
	exe(t, filepath.Join(cpus, "nproc"), "#!/bin/sh\necho 3\n")
	h.path["me@slow-box"] = cpus
	return h, home, filepath.Join(home, "idx", "proj")
}

func TestSSHConnectionTestStepByStep(t *testing.T) {
	h, home, _ := sshHarness(t)
	r := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "me@linux-box", "port": 2222, "indexRoot": "~/idx/proj", "sourceRoot": "~/proj"})
	if r.Body["ok"] != true {
		t.Fatalf("%d %v", r.Status, r.Body)
	}
	eq(t, steps(r), [][2]any{{"ssh", true}, {"gufi", true}, {"index", true}, {"source", true}}, "steps")
	match(t, stepDetail(r, "ssh"), `^Connected to me@linux-box on port 2222, a Linux host\.$`, "ssh step")
	eq(t, r.Body["gufiBin"], gufiBinDir(), "found where GUFI installs")
	match(t, r.Body["gufiVersion"], `^\S+$`, "version")
	eq(t, r.Body["directories"], 2.0, "directories")
	eq(t, r.Body["shell"], true, "shell")
	match(t, stepDetail(r, "index"), `2 directories and 3 files`, "index step")
	eq(t, len(r.Body), 7, "the test's fields")
	if !h.sawSession("ssh://me@linux-box:2222", "") {
		t.Error("the session is the user's own ssh, on the port given")
	}
	_ = home
}

func TestSSHFailingStepsSayWhatToChange(t *testing.T) {
	h, home, index := sshHarness(t)
	for dest, re := range map[string]string{
		"me@nokey":   `asked for a password.*Set up key access \(for example ssh-copy-id me@nokey\)`,
		"me@nohost":  `does not resolve`,
		"me@newhost": `host key of me@newhost is not known here yet`,
		"me@closed":  `Nothing accepted the connection on port 22`,
	} {
		r := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": dest, "indexRoot": index})
		eq(t, r.Body["ok"], false, dest)
		eq(t, steps(r), [][2]any{{"ssh", false}}, dest)
		match(t, stepDetail(r, "ssh"), re, dest)
	}
	linux := map[string]any{"destination": "me@linux-box", "port": 2222}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range linux {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	noGufi := h.post("/api/user-libraries/ssh/test", with(map[string]any{"indexRoot": index, "gufiBin": "/nowhere/bin"}))
	eq(t, steps(noGufi), [][2]any{{"ssh", true}, {"gufi", false}, {"index", true}}, "no gufi")
	match(t, stepDetail(noGufi, "gufi"), `There is no gufi_query in /nowhere/bin`, "no gufi")
	match(t, stepDetail(noGufi, "index"), `has a db\.db; its directories are counted once gufi_query runs`, "no gufi index")
	match(t, stepDetail(h.post("/api/user-libraries/ssh/test", with(map[string]any{"indexRoot": "/nowhere/index"})), "index"), `/nowhere/index is not a directory on me@linux-box`, "not a dir")
	match(t, stepDetail(h.post("/api/user-libraries/ssh/test", with(map[string]any{"indexRoot": "~/holder"})), "index"), `no db\.db.*one level down: `+regexp.QuoteMeta(filepath.Join(home, "holder", "sub")), "one up")
	noSource := h.post("/api/user-libraries/ssh/test", with(map[string]any{"indexRoot": index, "sourceRoot": "/nowhere/src"}))
	eq(t, noSource.Body["ok"], false, "no source")
	match(t, stepDetail(noSource, "source"), `not a directory.*names only`, "no source")
	bad := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "-oProxyCommand=sh", "indexRoot": index})
	eq(t, bad.Status, 400, "bad destination")
	match(t, bad.Body["error"], `^destination`, "bad destination")
}

func TestSSHLibraryAddListRefreshRemove(t *testing.T) {
	h, home, index := sshHarness(t)
	linux := map[string]any{"destination": "me@linux-box", "port": 2222}
	refused := h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@linux-box", "port": 2222, "indexRoot": "/nowhere/index"})
	eq(t, refused.Status, 400, "refused")
	match(t, refused.Body["error"], `did not pass.*not a directory`, "refused")

	added := h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@linux-box", "port": 2222, "label": "Remote project", "indexRoot": "~/idx/proj", "sourceRoot": "~/proj"})
	if added.Status != 200 {
		t.Fatalf("%v", added.Body)
	}
	d := added.Body
	eq(t, []any{d["transport"], d["state"], d["destination"], d["port"], d["site"], d["label"], d["existing"]}, []any{"ssh", "ready", "me@linux-box", 2222, "me@linux-box", "Remote project", true}, "added")
	eq(t, []any{d["indexRoot"], d["sourceRoot"], d["directories"], d["files"], d["run"]}, []any{index, filepath.Join(home, "proj"), 2, 3, nil}, "added paths")
	if d["builtAt"] == nil {
		t.Error("builtAt")
	}
	id := d["id"].(string)
	eq(t, id, "u-linux-box-proj", "id")
	l := h.listing()
	eq(t, l["ssh"], true, "ssh offered")
	eq(t, l["available"], true, "available")
	eq(t, len(l["libraries"].([]any)), 1, "listed")
	eq(t, h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@linux-box", "port": 2222, "indexRoot": index}).Status, 409, "the same index twice")
	_ = linux

	// Stored as Node stores it, for the libraries track and Node to read.
	defs := h.m.Users.Load("single")
	if len(defs) != 1 || defs[0].Resource != "ssh://me@linux-box:2222" || deref(defs[0].GufiBin) != gufiBinDir() {
		t.Fatalf("stored: %+v", defs)
	}

	again := h.post("/api/user-libraries/"+id+"/refresh", map[string]any{})
	eq(t, again.Status, 200, "refresh")
	eq(t, []any{again.Body["state"], again.Body["error"], again.Body["directories"]}, []any{"ready", nil, 2}, "refreshed")

	// macOS: GUFI on the PATH, and the label from the index folder's name.
	mac := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "me@mac-box", "indexRoot": index})
	eq(t, mac.Body["ok"], true, "mac")
	match(t, stepDetail(mac, "ssh"), `a macOS host`, "mac")
	eq(t, mac.Body["gufiBin"], filepath.Join(h.dir, "mac-gufi", "bin"), "mac gufi")

	// The count runs with the host's processors, and the add takes the
	// test that just passed.
	slow := map[string]any{"destination": "me@slow-box", "label": "Slow index", "indexRoot": index}
	eq(t, h.post("/api/user-libraries/ssh/test", slow).Body["ok"], true, "slow test")
	slowAdded := h.post("/api/user-libraries/ssh", slow)
	eq(t, slowAdded.Status, 200, "slow add")
	eq(t, slowAdded.Body["label"], "Slow index", "label")

	warm := h.post("/api/user-libraries/warm", map[string]any{})
	eq(t, warm.Body["hosts"], 2, "warm: one per host")

	del := h.do(http.MethodDelete, "/api/user-libraries/"+id, nil)
	eq(t, del.Status, 200, "delete")
	eq(t, del.Body["ok"], true, "delete")
	eq(t, h.do(http.MethodDelete, "/api/user-libraries/"+id, nil).Status, 404, "gone")
	eq(t, len(h.listing()["libraries"].([]any)), 1, "one left")
}

func TestSignInKeepsSSHOut(t *testing.T) {
	h := newHarness(t, nil)
	h.auth = true
	l := h.listing()
	eq(t, []any{l["available"], l["ssh"], l["pw"], l["libraries"], l["workflow"]}, []any{false, false, false, []any{}, true}, "no viewer")
	eq(t, l["pwWhy"], "Sign in to add your own libraries.", "why")
	h.user = &auth.User{ID: "someone", Username: "someone"}
	l = h.listing()
	eq(t, []any{l["available"], l["ssh"]}, []any{true, false}, "signed in")
	r := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "me@box", "indexRoot": "/x"})
	eq(t, r.Status, 403, "ssh test")
	match(t, r.Body["error"], `sign-in off`, "ssh test")
	eq(t, h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@box", "indexRoot": "/x"}).Status, 403, "ssh add")
	eq(t, h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@box", "sourceRoot": "/x"}).Status, 403, "ssh build")
	eq(t, h.post("/api/user-libraries/warm", nil).Body["hosts"], 0, "warm")
	// No personal key: nothing reached with the deployment's.
	pw := h.post("/api/user-libraries/pw/test", map[string]any{"cluster": "c", "indexRoot": "/x"})
	eq(t, pw.Status, 409, "pw test")
	match(t, pw.Body["error"], `your own platform key`, "pw test")
	add := h.post("/api/user-libraries", map[string]any{"site": "c"})
	eq(t, add.Status, 409, "cluster add")
	h.user = nil
	eq(t, h.post("/api/user-libraries", map[string]any{"site": "c"}).Status, 401, "no viewer")
	eq(t, h.do(http.MethodDelete, "/api/user-libraries/u-x", nil).Status, 404, "no viewer")
}

// ---- over pw ssh (pwLibraries.test.mjs, pwSystems.test.mjs) ----

func pwHarness(t *testing.T) (*harness, string) {
	h := newHarness(t, map[string]string{"PW_API_KEY": "cli-key"})
	home := filepath.Join(h.dir, "home")
	bin := filepath.Join(home, ".activate-studio", "gufi", "abc123", "bin")
	exe(t, filepath.Join(bin, "gufi_query"), `#!/bin/sh
args="$*"
case "$args" in
  *--version*) echo "gufi_query 0.6.9" ;;
  *gvec*novec*) echo "2|3|0" ;;
  *gvec*) echo "2|3|1" ;;
  *"-G "*) echo "2|3" ;;
esac
`)
	write(t, filepath.Join(home, "idx", "proj", "db.db"), "")
	write(t, filepath.Join(home, "idx", "novec", "db.db"), "")
	write(t, filepath.Join(home, "holder", "sub", "db.db"), "")
	write(t, filepath.Join(home, "proj", "readme.txt"), "hello from the cluster\n")
	pwLog := filepath.Join(h.dir, "pw.log")
	exe(t, filepath.Join(h.dir, "pw"), "#!/bin/sh\necho \"$@\" >> "+pwLog+"\n")
	h.env["PW_CLI"] = filepath.Join(h.dir, "pw")
	h.m.P.FindCLI()
	h.homes["cluster-a"] = home
	h.failing = map[string]string{
		"stopped-one": "Error: resource stopped-one not found",
		"expired-one": "Error: authentication has expired, please authenticate",
	}
	h.failCode = 1
	return h, home
}

func TestPwLibraryOverPwSSH(t *testing.T) {
	h, home := pwHarness(t)
	l := h.listing()
	eq(t, []any{l["pw"], l["ssh"], l["pwWhy"]}, []any{true, true, nil}, "offered")
	bin := filepath.Join(home, ".activate-studio", "gufi", "abc123", "bin")
	index := filepath.Join(home, "idx", "proj")

	r := h.post("/api/user-libraries/pw/test", map[string]any{"cluster": "cluster-a", "indexRoot": "~/idx/proj", "sourceRoot": "~/proj"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	eq(t, steps(r), [][2]any{{"ssh", true}, {"gufi", true}, {"index", true}, {"source", true}}, "steps")
	eq(t, stepDetail(r, "ssh"), "Connected to cluster-a, a Linux host.", "ssh")
	eq(t, []any{r.Body["gufiBin"], r.Body["gufiVersion"], r.Body["directories"], r.Body["vectors"]}, []any{bin, "gufi_query 0.6.9", 2, true}, "found")
	match(t, stepDetail(r, "index"), `2 directories and 3 files.*with vectors`, "index")
	if !h.sawSession("cluster-a", "cli-key") {
		t.Error("pw ssh to the cluster with this computer's key")
	}
	nov := h.post("/api/user-libraries/pw/test", map[string]any{"cluster": "cluster-a", "indexRoot": "~/idx/novec"})
	eq(t, nov.Body["vectors"], false, "no vectors")
	match(t, stepDetail(nov, "index"), `It has no vectors, so search by meaning does not cover it\.$`, "no vectors")

	for c, re := range map[string]string{
		"stopped-one": `^stopped-one is not one of your running clusters\. Start it on ACTIVATE, or choose another\.$`,
		"expired-one": `sign-in is missing or has expired\. Run pw auth`,
	} {
		f := h.post("/api/user-libraries/pw/test", map[string]any{"cluster": c, "indexRoot": index})
		eq(t, steps(f), [][2]any{{"ssh", false}}, c)
		match(t, stepDetail(f, "ssh"), re, c)
	}
	bad := h.post("/api/user-libraries/pw/test", map[string]any{"cluster": "-oProxyCommand=sh", "indexRoot": index})
	eq(t, bad.Status, 400, "bad cluster")
	match(t, bad.Body["error"], `^cluster:`, "bad cluster")
	eq(t, h.post("/api/user-libraries/pw/test", map[string]any{"cluster": "cluster-a", "indexRoot": "idx"}).Status, 400, "relative")

	added := h.post("/api/user-libraries/pw", map[string]any{"cluster": "cluster-a", "indexRoot": "~/idx/proj", "sourceRoot": "~/proj"})
	if added.Status != 200 {
		t.Fatalf("%v", added.Body)
	}
	d := added.Body
	eq(t, []any{d["transport"], d["existing"], d["state"], d["site"], d["label"], d["run"], d["vectors"]}, []any{"pw", true, "ready", "cluster-a", "proj", nil, true}, "added")
	eq(t, []any{d["indexRoot"], d["sourceRoot"], d["directories"], d["files"]}, []any{index, filepath.Join(home, "proj"), 2, 3}, "added paths")
	eq(t, h.post("/api/user-libraries/pw", map[string]any{"cluster": "cluster-a", "indexRoot": index}).Status, 409, "twice")
	// Refresh checks again: nothing is built.
	again := h.post("/api/user-libraries/"+d["id"].(string)+"/refresh", nil)
	eq(t, []any{again.Status, again.Body["state"], again.Body["error"]}, []any{200, "ready", nil}, "refresh")
	if b, _ := os.ReadFile(filepath.Join(h.dir, "pw.log")); len(b) != 0 {
		t.Errorf("the pw CLI was asked: %s", b)
	}
}

func TestPwSystemsListAndWhy(t *testing.T) {
	h := newHarness(t, nil)
	pwBin := filepath.Join(h.dir, "bin", "pw")
	h.env["PW_CLI"] = pwBin
	r := h.listing()
	eq(t, []any{r["pw"], r["pwOffered"]}, []any{false, true}, "no CLI")
	match(t, r["pwWhy"], `does not find the pw CLI`, "no CLI")
	if !strings.Contains(r["pwWhy"].(string), pwBin) {
		t.Error("the reason names PW_CLI")
	}
	s := h.do(http.MethodGet, "/api/user-libraries/pw/systems", nil).Body
	eq(t, []any{s["usable"], s["systems"], s["error"], s["why"]}, []any{false, []any{}, nil, r["pwWhy"]}, "no CLI systems")

	log := filepath.Join(h.dir, "pw.log")
	fail := filepath.Join(h.dir, "fail")
	exe(t, pwBin, `#!/bin/sh
echo "$PW_API_KEY $*" >> `+log+`
if [ -f `+fail+` ]; then echo "Error: unauthorized: the token has expired" >&2; exit 1; fi
case "$*" in
  "clusters ls"*) echo "checking for updates"; echo '[{"name":"beta","displayName":"Beta","status":"off","type":"aws-slurm"},{"name":"alpha","status":"active","type":"existing"},{"name":"bad name!","status":"active"},{"name":"gamma","displayName":"Gamma cluster","status":"active"}]' ;;
esac
`)
	r = h.listing()
	eq(t, []any{r["pw"], r["pwOffered"]}, []any{false, true}, "not signed in")
	match(t, r["pwWhy"], `not signed in`, "not signed in")
	eq(t, h.do(http.MethodGet, "/api/user-libraries/pw/systems", nil).Body["usable"], false, "not signed in")
	if _, err := os.Stat(log); err == nil {
		t.Error("the CLI is not asked for systems without a sign-in")
	}

	write(t, filepath.Join(h.dir, "creds", "credentials"), `{"currentIdentity":"test","identities":{"test":{"apikey":"cli-key","server":"platform.test"}}}`)
	r = h.listing()
	eq(t, []any{r["pw"], r["pwWhy"]}, []any{true, nil}, "signed in")
	s = h.do(http.MethodGet, "/api/user-libraries/pw/systems", nil).Body
	eq(t, []any{s["usable"], s["error"]}, []any{true, nil}, "signed in")
	eq(t, s["systems"], []any{
		map[string]any{"name": "alpha", "label": "alpha", "status": "active", "running": true, "type": "existing"},
		map[string]any{"name": "gamma", "label": "Gamma cluster", "status": "active", "running": true, "type": nil},
		map[string]any{"name": "beta", "label": "Beta", "status": "off", "running": false, "type": "aws-slurm"},
	}, "systems")
	b, _ := os.ReadFile(log)
	eq(t, strings.TrimSpace(string(b)), "cli-key clusters ls -o json --platform-host platform.test", "the CLI's arguments")

	write(t, fail, "")
	s = h.do(http.MethodGet, "/api/user-libraries/pw/systems", nil).Body
	eq(t, []any{s["usable"], s["systems"]}, []any{true, []any{}}, "refused")
	match(t, s["error"], `unauthorized: the token has expired`, "refused")
}

// ---- ssh options (sshOptions.test.mjs) ----

func TestSSHOptionsChange(t *testing.T) {
	h, _, index := sshHarness(t)
	added := h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@linux-box", "indexRoot": index, "sshOptions": "-J me@jump"})
	if added.Status != 200 {
		t.Fatalf("%v", added.Body)
	}
	eq(t, []any{added.Body["sshOptions"], added.Body["jump"]}, []any{"-J me@jump", "me@jump"}, "options")
	id := added.Body["id"].(string)
	if !h.sawSession("ssh://me@linux-box#-J,me@jump", "") {
		h.mu.Lock()
		t.Logf("sessions: %v", h.sessions)
		h.mu.Unlock()
	}
	bad := h.do(http.MethodPut, "/api/user-libraries/"+id+"/ssh-options", map[string]any{"sshOptions": "-o ProxyCommand=sh"})
	eq(t, bad.Status, 400, "refused option")
	match(t, bad.Body["error"], `ProxyCommand`, "refused option")
	changed := h.do(http.MethodPut, "/api/user-libraries/"+id+"/ssh-options", map[string]any{"sshOptions": "-o ServerAliveInterval=5"})
	eq(t, changed.Status, 200, "changed")
	eq(t, []any{changed.Body["sshOptions"], changed.Body["jump"]}, []any{"-o ServerAliveInterval=5", nil}, "changed")
	cleared := h.do(http.MethodPut, "/api/user-libraries/"+id+"/ssh-options", map[string]any{"sshOptions": ""})
	eq(t, []any{cleared.Status, cleared.Body["sshOptions"]}, []any{200, nil}, "cleared")
	defs := h.m.Users.Load("single")
	if len(defs[0].SSHOptions) != 0 || defs[0].Resource != "ssh://me@linux-box" {
		t.Errorf("stored: %+v", defs[0])
	}
	eq(t, h.do(http.MethodPut, "/api/user-libraries/u-nope/ssh-options", map[string]any{"sshOptions": ""}).Status, 404, "no such library")
}

// ---- building there (remoteBuild.test.mjs) ----

func buildHarness(t *testing.T) (*harness, map[string]string) {
	needGufi(t)
	h := newHarness(t, map[string]string{"PW_API_KEY": "cli-key"})
	homes := map[string]string{"me@linux-box": filepath.Join(h.dir, "box"), "cluster-a": filepath.Join(h.dir, "cluster")}
	for k, v := range homes {
		h.homes[k] = v
	}
	project := filepath.Join(homes["me@linux-box"], "project")
	write(t, filepath.Join(project, "readme.md"), "# Project\nThe wind tunnel campaign.\n")
	write(t, filepath.Join(project, "runs", "case-1", "input.yaml"), "mach: 0.8\n")
	write(t, filepath.Join(project, "runs", "case-1", "out.dat"), strings.Repeat("\x07", 2048))
	write(t, filepath.Join(homes["cluster-a"], "work", "notes.txt"), "cluster notes\n")
	exe(t, filepath.Join(h.dir, "pw"), "#!/bin/sh\nexit 0\n")
	h.env["PW_CLI"] = filepath.Join(h.dir, "pw")
	h.m.P.FindCLI()
	return h, homes
}

func TestRemoteBuildCheckAndBuild(t *testing.T) {
	h, homes := buildHarness(t)
	box := homes["me@linux-box"]
	l := h.listing()
	eq(t, []any{l["ssh"], l["pw"], l["workflow"]}, []any{true, true, false}, "offered")

	r := h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/inbox"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	eq(t, steps(r), [][2]any{{"ssh", true}, {"gufi", true}, {"source", true}, {"index", true}, {"extract", true}}, "steps")
	match(t, stepDetail(r, "ssh"), `^Connected to me@linux-box on port 2222`, "ssh")
	eq(t, r.Body["sourceRoot"], box+"/inbox", "source")
	match(t, r.Body["indexDir"], `^`+regexp.QuoteMeta(box)+`/\.activate-studio/indexes/inbox-[0-9a-f]{8}$`, "index dir")
	eq(t, r.Body["create"], []any{box + "/inbox"}, "create")
	match(t, stepDetail(r, "extract"), `Text is extracted there with Python 3\.\d+`, "extract")
	if _, err := os.Stat(box + "/inbox"); err == nil {
		t.Error("a check makes nothing")
	}
	if !h.sawSession("ssh://me@linux-box:2222", "") {
		t.Error("the user's own ssh on the port written after the host")
	}

	g := h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "gufiBin": "/nowhere/bin"})
	eq(t, g.Body["ok"], false, "no gufi")
	match(t, stepDetail(g, "gufi"), `^GUFI is not installed on me@linux-box: there is no gufi_dir2index and gufi_query in /nowhere/bin\. Install GUFI there`, "no gufi")
	match(t, firstFailed(h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "indexRoot": "~/project/idx"})), `inside the folder being indexed`, "inside")
	write(t, filepath.Join(box, "taken", "file.txt"), "x")
	match(t, firstFailed(h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "indexRoot": "~/taken"})), `holds files the Studio did not write`, "taken")
	host := h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@elsewhere", "sourceRoot": "~/x"})
	eq(t, steps(host), [][2]any{{"ssh", false}}, "elsewhere")
	match(t, stepDetail(host, "ssh"), `does not resolve`, "elsewhere")

	refused := h.post("/api/user-libraries/ssh/build", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "indexRoot": "~/indexes/project"})
	eq(t, refused.Status, 409, "confirm")
	match(t, refused.Body["error"], `do not exist yet: .*/indexes/project\. Confirm to create them`, "confirm")

	b := h.post("/api/user-libraries/ssh/build", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "label": "Project", "depth": "text"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	eq(t, []any{b.Body["state"], b.Body["built"], b.Body["existing"], b.Body["port"], b.Body["buildingDepth"], b.Body["builtDepth"]}, []any{"building", true, false, 2222, "text", nil}, "started")
	job, _ := b.Body["job"].(float64)
	if job == 0 {
		t.Fatal("a job follows it")
	}
	if j := h.m.Jobs().Get(int(job)); j == nil {
		t.Fatal("the job is the files track's")
	}
	id := b.Body["id"].(string)
	got := h.waitFor(id)
	if got["state"] != "ready" {
		t.Fatalf("%v", got)
	}
	eq(t, []any{got["directories"], got["files"], got["fullText"], got["progress"], got["sourceRoot"], got["builtDepth"], got["buildingDepth"], got["depth"]},
		[]any{3, 3, true, nil, filepath.Join(box, "project"), "text", nil, "text"}, "ready")
	match(t, got["indexRoot"], `\.activate-studio/indexes/project-[0-9a-f]{8}/project$`, "index root")
	states, _ := os.ReadDir(filepath.Join(box, ".activate-studio", "builds"))
	eq(t, len(states), 1, "one build there")

	// A rebuild replaces the index when complete; one that fails keeps the old one.
	write(t, filepath.Join(box, "project", "later.md"), "# later\n")
	again := h.post("/api/user-libraries/"+id+"/refresh", nil)
	eq(t, []any{again.Status, again.Body["state"]}, []any{200, "updating"}, "rebuild")
	got = h.waitFor(id)
	eq(t, []any{got["state"], got["error"], got["files"]}, []any{"ready", nil, 4}, "rebuilt")
	os.Rename(filepath.Join(box, "project"), filepath.Join(box, "project-moved"))
	h.post("/api/user-libraries/"+id+"/refresh", nil)
	time.Sleep(100 * time.Millisecond)
	got = h.waitFor(id, "ready")
	match(t, got["error"], `project is not a folder\.`, "failed rebuild")
	eq(t, got["files"], 4, "the index it had stays")

	// Over pw ssh, with this computer's key.
	pw := h.post("/api/user-libraries/pw/build", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/work", "depth": "names"})
	if pw.Status != 200 {
		t.Fatalf("%v", pw.Body)
	}
	eq(t, []any{pw.Body["transport"], pw.Body["site"], pw.Body["port"], pw.Body["destination"]}, []any{"pw", "cluster-a", nil, nil}, "pw build")
	got = h.waitFor(pw.Body["id"].(string))
	eq(t, []any{got["state"], got["files"], got["fullText"]}, []any{"ready", 1, false}, "pw built")
	if !h.sawSession("cluster-a", "cli-key") {
		t.Error("pw ssh with this computer's key")
	}
	bad := h.post("/api/user-libraries/pw/build/test", map[string]any{"cluster": "-x", "sourceRoot": "~/work"})
	eq(t, bad.Status, 400, "bad system")
	match(t, bad.Body["error"], `^system:`, "bad system")
	noSrc := h.post("/api/user-libraries/pw/build/test", map[string]any{"cluster": "cluster-a"})
	match(t, noSrc.Body["error"], `^source folder:`, "no source")
}

// ---- a login that runs only gufi_query (restrictedRemote.test.mjs) ----

func TestQueryOnlyHost(t *testing.T) {
	gufi := needGufi(t)
	h := newHarness(t, nil)
	home := filepath.Join(h.dir, "home")
	write(t, filepath.Join(home, "proj", "readme.txt"), "hello\n")
	os.MkdirAll(filepath.Join(home, "idx"), 0o755)
	if out, err := exec.Command(filepath.Join(gufi, "gufi_dir2index"), "-x", filepath.Join(home, "proj"), filepath.Join(home, "idx")).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	index := filepath.Join(home, "idx", "proj")
	// The login runs a command only when it is gufi_query, as a forced
	// command does; anything else is refused.
	h.homes["me@locked"] = home
	h.m.Pool = remote.New(remote.Options{IndexBase: h.m.IndexBase, Getenv: func(k string) string { return h.env[k] },
		SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} },
		Spawn: func(site, key, command string) *exec.Cmd {
			cmd := exec.Command("bash", "-c", `case "$CMD" in gufi_query|"gufi_query "*) eval "$CMD" ;; *) echo "This account runs only gufi_query." >&2; exit 1 ;; esac`)
			cmd.Env = []string{"HOME=" + home, "PATH=" + gufi + ":/usr/bin:/bin", "CMD=" + command}
			return cmd
		}})
	t.Cleanup(h.m.Pool.Close)
	h.m.eng = nil
	h.m.Runners = &remote.Runners{Pool: h.m.Pool}
	r := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "me@locked", "indexRoot": index, "sourceRoot": "~/proj"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	eq(t, steps(r), [][2]any{{"ssh", true}, {"gufi", true}, {"index", true}, {"source", true}}, "steps")
	match(t, stepDetail(r, "ssh"), `This host runs only gufi_query`, "ssh")
	match(t, stepDetail(r, "gufi"), `on the login's PATH\.$`, "gufi")
	eq(t, []any{r.Body["shell"], r.Body["gufiBin"], r.Body["directories"]}, []any{false, "", 1}, "query only")
	tilde := h.post("/api/user-libraries/ssh/test", map[string]any{"destination": "me@locked", "indexRoot": "~/idx/proj"})
	match(t, stepDetail(tilde, "index"), `runs no shell, so ~ cannot be resolved there`, "tilde")
	added := h.post("/api/user-libraries/ssh", map[string]any{"destination": "me@locked", "indexRoot": index})
	eq(t, added.Status, 200, "added")
	defs := h.m.Users.Load("single")
	if defs[0].Shell == nil || *defs[0].Shell {
		t.Errorf("shell false is stored: %+v", defs[0])
	}
	build := h.post("/api/user-libraries/ssh/build/test", map[string]any{"destination": "me@locked", "sourceRoot": "~/proj"})
	eq(t, steps(build), [][2]any{{"ssh", false}}, "no build there")
	eq(t, stepDetail(build, "ssh"), onlyGufiQuery, "no build there")
}

// ---- the index workflow on a cluster (userLibraries.test.mjs) ----

func TestClusterLibraryWithTheIndexWorkflow(t *testing.T) {
	h := newHarness(t, map[string]string{"PW_API_KEY": "cli-key", "PW_PLATFORM_HOST": "platform.test"})
	home := filepath.Join(h.dir, "home")
	gufiHome := filepath.Join(home, ".activate-studio", "gufi", "deadbeef01", "bin")
	os.MkdirAll(gufiHome, 0o755)
	h.homes["cluster-a"] = home
	log := filepath.Join(h.dir, "pw.log")
	state := filepath.Join(h.dir, "state")
	exe(t, filepath.Join(h.dir, "pw"), `#!/bin/sh
echo "$PW_API_KEY $*" >> `+log+`
case "$*" in
  "workflows get"*) exit 1 ;;
  "workflows create"*) echo created ;;
  "workflows runs view"*) if [ -f `+state+` ]; then echo '{"status":"completed"}'; else echo '{"status":"running"}'; fi ;;
  "workflows runs logs"*) printf 'noise\nSTUDIO_INDEX_BEGIN\n{"index":"~/idx/work","source":"$HOME/work","gufiCommit":"deadbeef01","directories":4,"files":9,"builtAt":"2026-01-01T00:00:00.000Z"}\nSTUDIO_INDEX_END\n' ;;
  "clusters ls"*) echo '[{"name":"cluster-a","user":{"username":"jdoe"}}]' ;;
  "workflows run "*) echo 'starting'; echo '{"run":{"slug":"run-1"}}' ;;
esac
`)
	h.env["PW_CLI"] = filepath.Join(h.dir, "pw")
	h.m.P.FindCLI()
	l := h.listing()
	eq(t, l["workflow"], true, "offered on a platform-hosted Studio")
	eq(t, h.post("/api/user-libraries", map[string]any{"site": "bad site"}).Status, 400, "bad site")
	eq(t, h.post("/api/user-libraries", map[string]any{"site": "cluster-a", "sourceDir": "../x"}).Status, 400, "bad dir")
	r := h.post("/api/user-libraries", map[string]any{"site": "cluster-a", "scheduler": map[string]any{"partition": "gpu;rm", "time": "2:00:00"}})
	if r.Status != 200 {
		t.Fatalf("%v", r.Body)
	}
	eq(t, []any{r.Body["id"], r.Body["label"], r.Body["state"], r.Body["sourceDir"], r.Body["scheduler"]},
		[]any{"u-cluster-a-workdir", "cluster-a $WORKDIR", "building", "$WORKDIR", map[string]any{"partition": "gpurm", "time": "2:00:00"}}, "added")
	run := r.Body["run"].(map[string]any)
	eq(t, []any{run["slug"], run["mode"]}, []any{"run-1", "build"}, "run")
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	match(t, lines[0], `^cli-key clusters ls --status active -o json --platform-host platform\.test$`, "resource")
	match(t, lines[2], `^cli-key workflows create --yaml .*deploy/gufi-index\.yaml --display-name Studio library index studio-gufi-index-[0-9a-f]{8} --platform-host platform\.test$`, "create")
	match(t, lines[3], `"resource":"pw://jdoe/cluster-a","cluster":\{"scheduler":true,"slurm":\{"partition":"gpurm","time":"2:00:00"\}\},"index":\{"source_dir":"\$WORKDIR","index_dir":"","mode":"build","threads":4\}`, "inputs")

	// Running: the listing looks at most every 15 seconds.
	h.listing()
	write(t, state, "")
	defs := h.m.Users.Load("single")
	defs[0].Run.CheckedAt = ""
	h.m.Users.Save("single", defs)
	got := h.library("u-cluster-a-workdir")
	eq(t, []any{got["state"], got["indexRoot"], got["sourceRoot"], got["directories"], got["files"], got["builtAt"]},
		[]any{"ready", home + "/idx/work", home + "/work", 4, 9, "2026-01-01T00:00:00.000Z"}, "ready")
	if d := h.m.Users.Load("single")[0]; deref(d.GufiBin) != gufiHome {
		t.Errorf("gufiBin %v", deref(d.GufiBin))
	}
	upd := h.post("/api/user-libraries/u-cluster-a-workdir/refresh", nil)
	eq(t, []any{upd.Status, upd.Body["state"], upd.Body["run"].(map[string]any)["mode"]}, []any{200, "updating", "update"}, "update")
}
