package chat

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// bobsWorkspace is Bob's user workspace as the platform's pw code listing
// gives it to him: always his own, by the id, name and kind workspace.
var bobsWorkspace = bridgeMach{ID: "workspace", Kind: "workspace", Name: "workspace", Owner: "bob", Status: "connected"}

// withMachines has the platform list these machines to the viewer, by id.
func withMachines(m *Module, list ...bridgeMach) {
	m.bridgeMachineFn = func(r *http.Request, id string) (bridgeMach, error) {
		for _, mach := range list {
			if mach.ID == id {
				return mach, nil
			}
		}
		return bridgeMach{}, platform.KbError(404, "not yours")
	}
	m.bridgeMachinesFn = func(r *http.Request) ([]bridgeMach, error) { return list, nil }
}

// The Studio adds its tools to pw code in the person's user workspace over
// pw ssh as them, as on their clusters: at the site pw ssh knows the
// workspace by, with their own key, never pw://owner/workspace.
func TestTheUserWorkspaceIsSetUpOverPwSSH(t *testing.T) {
	m, f, _ := setupStudio(t)
	withMachines(m, bobsWorkspace)
	o := offersOf(t, m, bobProxy)["workspace"]
	if o.State != "offer" || !o.Available || o.Command != "" {
		t.Fatalf("offer: %+v", o)
	}
	code, out := setupAs(t, m, bobProxy, `{"machineId":"workspace"}`)
	if code != 200 || out["worked"] != true || f.adds != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if len(f.calls) == 0 {
		t.Fatal("nothing ran")
	}
	for _, c := range f.calls {
		if c != "workspace pwt_bobs.key" {
			t.Errorf("ran at %q, not in his workspace with his own key", c)
		}
	}
	if o := offersOf(t, m, bobProxy)["workspace"]; o.State != "set" || !o.Available {
		t.Fatalf("after: %+v", o)
	}
	// Remove takes them out the same way.
	f.calls = nil
	r := setupReq(bobProxy)
	r.Body = io.NopCloser(strings.NewReader(`{"machineId":"workspace"}`))
	if err := m.bridgeRemove(httptest.NewRecorder(), r); err != nil || f.entry != nil {
		t.Fatalf("remove: %v %v", err, f.entry)
	}
	for _, c := range f.calls {
		if c != "workspace pwt_bobs.key" {
			t.Errorf("removed at %q", c)
		}
	}
}

// A session started in the workspace adds them first, once chosen.
func TestTheWorkspaceGetsThemAsASessionStarts(t *testing.T) {
	const url = "https://platform.example/me/session/ada/studio/api/mcp"
	yes := true
	m, f, _ := setupStudio(t)
	withMachines(m, bobsWorkspace)
	if has, why := m.bridgeAtStart(setupReq(bobProxy), bobsWorkspace, url, &yes); !has || why != "" || f.adds != 1 {
		t.Fatalf("%v %q, %d adds", has, why, f.adds)
	}
	for _, c := range f.calls {
		if c != "workspace pwt_bobs.key" {
			t.Errorf("ran at %q", c)
		}
	}
}

// Only the person's own workspace is set up: one listed as another's, or as
// no one's, or under an id that is not the workspace, is refused before
// anything runs.
func TestOnlyThePersonsOwnWorkspaceIsSetUp(t *testing.T) {
	m, f, _ := setupStudio(t)
	adas := bobsWorkspace
	adas.Owner = "ada"
	noOne := bobsWorkspace
	noOne.Owner = ""
	for what, c := range map[string]struct {
		mach bridgeMach
		want int
		says string
	}{
		"ada's":            {adas, 403, "not yours"},
		"no one's":         {noOne, 403, "not yours"},
		"not by its id":    {bridgeMach{ID: "w-1", Kind: "workspace", Name: "workspace", Owner: "bob", Status: "connected"}, 409, "cannot name"},
		"stopped":          {bridgeMach{ID: "workspace", Kind: "workspace", Name: "workspace", Owner: "bob", Status: "stopped"}, 409, "not running"},
		"starting":         {bridgeMach{ID: "workspace", Kind: "workspace", Name: "workspace", Owner: "bob", Status: "starting"}, 409, "is starting"},
		"a computer still": {bridgeMach{ID: "workspace", Kind: "local", Name: "workspace", Owner: "bob", Status: "connected"}, 409, "cannot open a terminal"},
	} {
		withMachines(m, c.mach)
		code, out := setupAs(t, m, bobProxy, `{"machineId":"`+c.mach.ID+`"}`)
		if code != c.want || !strings.Contains(out["error"].(string), c.says) {
			t.Errorf("%s: %d %v", what, code, out)
		}
	}
	if len(f.calls) != 0 || f.adds != 0 {
		t.Fatalf("ran %v", f.calls)
	}
	// A workspace that is not running is said to be so, and not offered.
	withMachines(m, bridgeMach{ID: "workspace", Kind: "workspace", Name: "workspace", Owner: "bob", Status: "stopped"})
	if o := offersOf(t, m, bobProxy)["workspace"]; o.State != "unavailable" || o.Available || !strings.Contains(o.Why, "not running") || o.Command != "" {
		t.Errorf("stopped: %+v", o)
	}
}

// The site and the reach rule by kind: the workspace by the word alone, a
// resource by its address, and a computer that connected itself by hand.
func TestTheWorkspaceSiteIsTheWordPwSSHTakes(t *testing.T) {
	m, _, _ := setupStudio(t)
	site, key, err := m.bridgeSite(setupReq(bobProxy), bobsWorkspace)
	if err != nil || site != "workspace" || key != "pwt_bobs.key" {
		t.Fatalf("%q %q %v", site, key, err)
	}
	for _, c := range []struct {
		mach  bridgeMach
		reach bool
	}{
		{bobsWorkspace, true},
		{bridgeMach{ID: "m", Kind: "cloud", Name: "c", Owner: "bob"}, true},
		{bridgeMach{ID: "m", Kind: "local", Name: "laptop", Owner: "bob"}, false},
		{bridgeMach{ID: "m", Kind: "pod", Name: "sandbox", Owner: "bob"}, false},
		{bridgeMach{ID: "m", Kind: "", Name: "x", Owner: "bob"}, false},
	} {
		if got := bridgeByHand(c.mach) == ""; got != c.reach {
			t.Errorf("%s: reached %v, want %v", c.mach.Kind, got, c.reach)
		}
	}
}

// The command for a computer the Studio does not reach is one short line
// that fetches the setup from the Studio with that machine's own pw
// sign-in: the token goes to curl on its input, and what comes back is the
// same entry Set up for me writes.
func TestTheShortCommandFetchesTheSetupWithTheMachinesSignIn(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	const url = "https://platform.example/me/session/ada/studio/api/mcp"
	line := bridgeFetch(url, "m-laptop")
	if strings.Contains(line, "\n") || len(line) > 260 {
		t.Fatalf("not one short line (%d): %s", len(line), line)
	}
	dir := t.TempDir()
	argvFile, inFile := filepath.Join(dir, "argv"), filepath.Join(dir, "stdin")
	pw := "#!/bin/sh\nif [ \"$1 $2 $3\" = 'auth token --print' ]; then echo 'renewing'; echo tok-123; exit 0; fi\nprintf '%s\\000' \"$@\" >" + filepath.Join(dir, "pwargs") + "\n"
	// A curl that keeps its arguments and input, and answers the script.
	m, _, _ := setupStudio(t)
	r := httptest.NewRequest(http.MethodGet, bridgeScriptPath+"?machine=m-laptop", nil)
	r.Header.Set("X-Forwarded-Host", "platform.example")
	r.Header.Set("X-Forwarded-Prefix", "/me/session/ada/studio")
	r = asViewer(r, bobProxy)
	w := httptest.NewRecorder()
	if err := m.bridgeScript(w, r); err != nil || w.Code != 200 {
		t.Fatalf("%d %v", w.Code, err)
	}
	script := filepath.Join(dir, "script")
	if err := os.WriteFile(script, w.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	curl := "#!/bin/sh\nprintf '%s\\000' \"$@\" >" + argvFile + "\ncat >" + inFile + "\ncat " + script + "\n"
	for name, body := range map[string]string{"pw": pw, "curl": curl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "has this Studio's tools") {
		t.Fatalf("%v: %s", err, out)
	}
	argv, _ := os.ReadFile(argvFile)
	in, _ := os.ReadFile(inFile)
	if strings.Contains(string(argv), "tok-123") || strings.TrimSpace(string(in)) != "Authorization: Bearer tok-123" {
		t.Errorf("curl argv %q, input %q", argv, in)
	}
	if !strings.Contains(string(argv), "https://platform.example/me/session/ada/studio/api/mcp/bridge/setup.sh?machine=m-laptop") {
		t.Errorf("fetched %q", argv)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "pwargs"))
	args := strings.Split(strings.TrimSuffix(string(got), "\x00"), "\x00")
	if len(args) < 13 || args[2] != "add" || args[10] != bridgeShim || args[12] != url+"?machine=m-laptop" {
		t.Fatalf("pw got %q", args)
	}
	if strings.Contains(w.Body.String(), "pwt_") {
		t.Error("the script carries a key")
	}
}

// The script is only for a request through the session proxy, never a page
// of another site, and takes only a machine id.
func TestTheSetupScriptIsServedOnlyThroughTheProxy(t *testing.T) {
	m, _, _ := setupStudio(t)
	get := func(u *auth.User, q string, h map[string]string) int {
		r := httptest.NewRequest(http.MethodGet, bridgeScriptPath+q, nil)
		r.Header.Set("X-Forwarded-Host", "platform.example")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		r = asViewer(r, u)
		w := httptest.NewRecorder()
		var ae *apierr.Error
		if err := m.bridgeScript(w, r); errors.As(err, &ae) {
			return ae.Status
		} else if err != nil {
			t.Fatal(err)
		}
		return w.Code
	}
	if c := get(bobProxy, "", nil); c != 200 {
		t.Errorf("through the proxy: %d", c)
	}
	if c := get(&auth.User{ID: "bob", Username: "bob", Source: "oidc"}, "", nil); c != 409 {
		t.Errorf("not through the proxy: %d", c)
	}
	if c := get(bobProxy, "", map[string]string{"Sec-Fetch-Site": "cross-site"}); c != 403 {
		t.Errorf("from another site: %d", c)
	}
	if c := get(bobProxy, "?machine=../x'", nil); c != 400 {
		t.Errorf("a machine that is no id: %d", c)
	}
}
