//go:build !windows

package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// placeLibrary answers one library on another system, and its folders.
type placeLibrary struct {
	kbpkg.Resolver
	site string
	at   func(r *http.Request, rel string) (kbpkg.FolderAt, error)
}

func (p *placeLibrary) Library(_ *http.Request, id string) (kbpkg.Library, error) {
	if id != "u-work" {
		return kbpkg.Library{}, kbpkg.Error(404, "no library named %s", id)
	}
	return kbpkg.Library{ID: id, Label: "Work", IndexRoot: "remote:me/u-work", Site: p.site}, nil
}

func (p *placeLibrary) Folder(r *http.Request, lib kbpkg.Library, rel string) (kbpkg.FolderAt, error) {
	if p.at != nil {
		return p.at(r, rel)
	}
	return kbpkg.FolderAt{Lib: lib, Rel: rel, Site: p.site, Root: "/home/me", Dir: "/home/me/" + rel}, nil
}

// withLibraries registers m's libraries, with p beside the main one.
func withLibraries(t *testing.T, m *Module, p *placeLibrary) {
	t.Helper()
	d, r, f, w := kbpkg.Default, kbpkg.LibraryResolver, kbpkg.Folders, kbpkg.MainWriter
	t.Cleanup(func() { kbpkg.Default, kbpkg.LibraryResolver, kbpkg.Folders, kbpkg.MainWriter = d, r, f, w })
	kbpkg.Register(http.NewServeMux(), m.A)
	kbpkg.MainWriter = nil
	if p != nil {
		kbpkg.LibraryResolver, kbpkg.Folders = p, p
	}
}

func askPlace(t *testing.T, base, library, at string) (int, map[string]any) {
	t.Helper()
	code, out, _ := send(t, http.MethodGet, base+"/api/chat/pwcode/start/library?library="+url.QueryEscape(library)+"&path="+url.QueryEscape(at), "")
	return code, out
}

// A chat started at a folder of the main library works in it, on the
// Studio's own machine with the Studio's tools, and nowhere else.
func TestAChatStartsInTheLibrarysFolder(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	withLibraries(t, m, nil)
	root := m.A.Cfg.KBRoot
	os.MkdirAll(filepath.Join(root, "runs"), 0o755)
	os.WriteFile(filepath.Join(root, "runs", "plan.md"), []byte("p"), 0o644)

	code, out := askPlace(t, base, "kb", "runs/plan.md")
	if code != 200 || out["machine"] != "studio" || out["folder"] != filepath.Join(root, "runs") || out["path"] != "runs" || out["file"] != "runs/plan.md" || out["library"] != "kb" {
		t.Fatalf("%d %v", code, out)
	}
	c := m.Conv.Create(nil, "")
	sse := startMessage(t, base, c.ID, "u1", map[string]any{"engine": "pwcode", "pwcodeFolder": out["folder"], "pwcodeLibrary": "kb", "pwcodePath": "runs/plan.md"})
	if len(f.created) != 1 || f.created[0]["workspace"] != filepath.Join(root, "runs") || !strings.Contains(startAgents(f, 0), `/api/mcp"`) {
		t.Fatalf("created %v\n%s", f.created, sse)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Relay || l.Workspace != filepath.Join(root, "runs") {
		t.Fatalf("link %+v", l)
	}
	// The library itself, where a chat works by default.
	if code, out = askPlace(t, base, "kb", ""); code != 200 || out["folder"] != root {
		t.Errorf("%d %v", code, out)
	}

	// Anything but the folder found is refused, and nothing starts: another
	// folder, another machine, no folder, one that went, one not a library's.
	for what, chose := range map[string]map[string]any{
		"another folder":  {"pwcodeFolder": t.TempDir(), "pwcodeLibrary": "kb", "pwcodePath": "runs"},
		"another machine": {"pwcodeMachine": "m2", "pwcodeFolder": filepath.Join(root, "runs"), "pwcodeLibrary": "kb", "pwcodePath": "runs"},
		"no folder":       {"pwcodeLibrary": "kb", "pwcodePath": "runs"},
		"gone":            {"pwcodeFolder": filepath.Join(root, "gone"), "pwcodeLibrary": "kb", "pwcodePath": "gone"},
		"out of it":       {"pwcodeFolder": filepath.Dir(root), "pwcodeLibrary": "kb", "pwcodePath": ".."},
		"no such library": {"pwcodeFolder": root, "pwcodeLibrary": "nolib", "pwcodePath": ""},
	} {
		chose["engine"] = "pwcode"
		c := m.Conv.Create(nil, "")
		if sse := startMessage(t, base, c.ID, "u1", chose); len(f.created) != 1 || strings.Contains(sse, `"content":"Hello"`) || !strings.Contains(sse, "event: error") {
			t.Errorf("%s: made %d\n%s", what, len(f.created), sse)
		}
	}
	if code, _ = askPlace(t, base, "nolib", ""); code != 404 {
		t.Errorf("no such library: %d", code)
	}
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(root, "runs"), 0o555)
		defer os.Chmod(filepath.Join(root, "runs"), 0o755)
		if code, out = askPlace(t, base, "kb", "runs"); code != 403 || !strings.Contains(out["detail"].(string), "cannot write") {
			t.Errorf("read only: %d %v", code, out)
		}
	}
}

// A library on another system: the person's pw code machine that is that
// system, in the folder there, through ACTIVATE; or the reason.
func TestAChatStartsOnTheMachineThatHoldsTheLibrary(t *testing.T) {
	local, remotePW := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remotePW)
	p := &placeLibrary{site: "pw://me/other-box"}
	withLibraries(t, m, p)
	canStart(plat, map[string]any{"user": "me"})
	owner := func(v string) {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		for _, k := range []string{"owner", "user", "ownerUsername"} {
			delete(plat.others[0], k)
		}
		if v != "" {
			plat.others[0]["owner"] = v
		}
	}
	owner("me")
	m.bridgeClustersFn = func(*http.Request, string) ([]bridgeCluster, error) { return nil, nil }

	code, out := askPlace(t, base, "u-work", "solver")
	if code != 200 || out["machine"] != "m2" || out["machineName"] != "other-box" || out["folder"] != "/home/me/solver" {
		t.Fatalf("%d %v", code, out)
	}
	c := m.Conv.Create(nil, "")
	sse := startMessage(t, base, c.ID, "u1", map[string]any{"engine": "pwcode", "pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver", "pwcodeMode": "accept-edits", "pwcodeLibrary": "u-work", "pwcodePath": "solver"})
	if len(local.created) != 0 || len(remotePW.created) != 1 || remotePW.created[0]["workspace"] != "/home/me/solver" || !strings.Contains(sse, `"content":"Hello"`) {
		t.Fatalf("made %d here, %d there\n%s", len(local.created), len(remotePW.created), sse)
	}

	// Set to start here instead, it is refused: never another machine.
	refused := func(what, word string) {
		t.Helper()
		if word != "" {
			code, out := askPlace(t, base, "u-work", "solver")
			if why, _ := out["detail"].(string); code != 409 || !strings.Contains(why, word) {
				t.Errorf("%s: %d %v", what, code, out)
			}
		}
		for _, machine := range []string{"m2", "studio"} {
			if word == "" && machine == "m2" {
				continue
			}
			c := m.Conv.Create(nil, "")
			sse := startMessage(t, base, c.ID, "u1", map[string]any{"engine": "pwcode", "pwcodeMachine": machine, "pwcodeFolder": "/home/me/solver", "pwcodeMode": "accept-edits", "pwcodeLibrary": "u-work", "pwcodePath": "solver"})
			if len(local.created) != 0 || len(remotePW.created) != 1 || !strings.Contains(sse, "event: error") {
				t.Errorf("%s on %s: made %d here, %d there\n%s", what, machine, len(local.created), len(remotePW.created), sse)
			}
		}
	}
	refused("set to start here", "")

	// Another person's cluster of the same name is not the library's.
	owner("eve")
	refused("another's", "has no pw code connected to ACTIVATE as you")
	// The listing names no owner: the person's cluster listing does, alone.
	owner("")
	m.bridgeClustersFn = func(*http.Request, string) ([]bridgeCluster, error) {
		return []bridgeCluster{{Name: "other-box", Type: "existing", User: "eve"}}, nil
	}
	refused("listed as another's", "has no pw code connected to ACTIVATE as you")
	m.bridgeClustersFn = func(*http.Request, string) ([]bridgeCluster, error) {
		return []bridgeCluster{{Name: "other-box", Type: "existing", User: "me"}, {Name: "other-box", Type: "existing", User: "eve"}}, nil
	}
	refused("listed twice", "has no pw code connected to ACTIVATE as you")
	m.bridgeClustersFn = func(*http.Request, string) ([]bridgeCluster, error) {
		return []bridgeCluster{{Name: "other-box", Type: "existing", User: "me"}}, nil
	}
	if code, out := askPlace(t, base, "u-work", "solver"); code != 200 || out["machine"] != "m2" {
		t.Errorf("by the cluster listing: %d %v", code, out)
	}
	// A computer that took the name is not the cluster.
	canStart(plat, map[string]any{"kind": "local"})
	refused("a computer by that name", "has no pw code connected to ACTIVATE as you")
	canStart(plat, map[string]any{"kind": "existing", "status": "disconnected"})
	refused("off", "does not take new sessions")
	canStart(plat, map[string]any{"status": "connected", "remoteStart": false})
	refused("not taking sessions", "does not take new sessions")
	canStart(plat, nil)
	p.at = func(*http.Request, string) (kbpkg.FolderAt, error) {
		return kbpkg.FolderAt{}, kbpkg.ErrFolderReadOnly("Work is read only for you")
	}
	if code, out := askPlace(t, base, "u-work", "solver"); code != 403 {
		t.Errorf("read only: %d %v", code, out)
	}
	refused("read only", "")

	// A library named by the cluster alone: the machine whose folder it is.
	seen := ""
	m.bridgeRunFn = func(_ context.Context, site, _ string, _ []string, _ time.Duration) (remote.Result, error) {
		seen = site
		return remote.Result{Stdout: "studio-access file= write=1 ino=77\n"}, nil
	}
	ino := "77"
	p.at = func(_ *http.Request, rel string) (kbpkg.FolderAt, error) {
		return kbpkg.FolderAt{Lib: kbpkg.Library{ID: "u-work", Label: "Work"}, Rel: rel, Site: "other-box", Root: "/home/me", Dir: "/home/me/" + rel, Ino: ino}, nil
	}
	if code, out := askPlace(t, base, "u-work", "solver"); code != 200 || out["machine"] != "m2" || seen != "pw://me/other-box" {
		t.Errorf("by its name: %d %v, looked on %s", code, out, seen)
	}
	ino = "78"
	refused("another folder there", "has no pw code connected to ACTIVATE as you")

	// A host over ssh is the machine whose daemon runs there, by no name.
	daemon := "d2"
	p.at = func(_ *http.Request, rel string) (kbpkg.FolderAt, error) {
		return kbpkg.FolderAt{Lib: kbpkg.Library{ID: "u-work", Label: "Work"}, Rel: rel, Site: "ssh://me@elsewhere", Dir: "/home/me/" + rel, SSH: true, Daemon: daemon}, nil
	}
	if code, out := askPlace(t, base, "u-work", "solver"); code != 200 || out["machine"] != "m2" {
		t.Errorf("over ssh: %d %v", code, out)
	}
	daemon = "d1"
	if code, out := askPlace(t, base, "u-work", "solver"); code != 200 || out["machine"] != "studio" {
		t.Errorf("over ssh to this host: %d %v", code, out)
	}
	// This host needs no platform to name it.
	plat.mu.Lock()
	plat.listDown = true
	plat.mu.Unlock()
	if code, out := askPlace(t, base, "u-work", "solver"); code != 200 || out["machine"] != "studio" {
		t.Errorf("over ssh to this host, with no listing: %d %v", code, out)
	}
	plat.mu.Lock()
	plat.listDown = false
	plat.mu.Unlock()
	daemon = "d9"
	refused("a daemon not listed", "has no pw code connected to ACTIVATE as you")
	daemon = ""
	refused("no pw code there", "runs no pw code for your account")
}

// On a Studio people sign in to, a library on its host is looked at as the
// person on the cluster their pw code runs on, whoever they are here.
func TestUnderSignInALibrarysFolderIsThePersonsOwnToOpen(t *testing.T) {
	f := &fakePW{script: helloTurn, status: map[string]any{"machineId": "d9", "hostname": "login1"}}
	m := signInStudio(t, f)
	sock := pwcodetest.Socket(t, f)
	pwSites().Daemon.SetBridgeSpawn(func(resource, key string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestStartBridgeHelper")
		cmd.Env = append(os.Environ(), "START_BRIDGE_SOCK="+sock)
		return cmd
	})
	p := &placeLibrary{site: "testcluster"}
	withLibraries(t, m, p)
	root := m.A.Cfg.KBRoot
	os.MkdirAll(filepath.Join(root, "runs"), 0o755)
	var ran []string
	// there stands for the library's folder on the cluster.
	there := root
	m.bridgeRunFn = func(ctx context.Context, site, key string, argv []string, _ time.Duration) (remote.Result, error) {
		ran = append(ran, site+" "+key)
		argv = append([]string{}, argv...)
		argv[4] = there
		out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
		return remote.Result{Stdout: string(out)}, err
	}
	call := func(who, library, at string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		if err := m.startInLibrary(rec, as(who, http.MethodGet, "/api/chat/pwcode/start/library?library="+library+"&path="+url.QueryEscape(at), "")); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := call("cy", "kb", "runs")
	if code != 200 || out["machine"] != "studio" || out["machineName"] != "login1" || out["folder"] != filepath.Join(root, "runs") {
		t.Fatalf("%d %v", code, out)
	}
	for _, as := range ran {
		if as != "testcluster cy-own-key" {
			t.Errorf("looked as %s", as)
		}
	}
	c := m.Conv.Create(nil, "cy")
	body := `"engine":"pwcode","pwcodeFolder":"` + filepath.Join(root, "runs") + `","pwcodeLibrary":"kb","pwcodePath":"runs",`
	if sse := sendAs(t, m, "cy", c.ID, "u1", "", body); len(f.created) != 1 || f.created[0]["workspace"] != filepath.Join(root, "runs") {
		t.Fatalf("created %v\n%s", f.created, sse)
	}

	refused := func(what, word string) {
		t.Helper()
		code, out := call("cy", "kb", "runs")
		if why, _ := out["detail"].(string); code == 200 || !strings.Contains(why, word) {
			t.Errorf("%s: %d %v", what, code, out)
		}
		c := m.Conv.Create(nil, "cy")
		if sse := sendAs(t, m, "cy", c.ID, "u1", "", body); len(f.created) != 1 || !strings.Contains(sse, "event: error") {
			t.Errorf("%s: made %d\n%s", what, len(f.created), sse)
		}
	}
	// Not there for their account, another folder at that path, not theirs to
	// write, and the main library read only to them.
	there = t.TempDir()
	refused("not there", "cannot open")
	os.MkdirAll(filepath.Join(there, "runs"), 0o755)
	refused("another at that path", "is not the one")
	there = root
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(root, "runs"), 0o555)
		refused("not writable", "cannot write")
		os.Chmod(filepath.Join(root, "runs"), 0o755)
	}
	// Who may change the main library is the Studio's to say, an
	// administrator's account on the cluster or not.
	kbpkg.MainWriter = func(*http.Request) (bool, string) { return false, "" }
	refused("read only here", "read only for you")
	kbpkg.MainWriter = nil

	// No ACTIVATE sign-in of their own: nothing is asked as anyone.
	n := len(ran)
	if code, out := call("bob", "kb", "runs"); code != 409 || len(ran) != n {
		t.Errorf("bob: %d %v", code, out)
	}
	if code, _ := call("", "kb", "runs"); code == 200 || len(ran) != n {
		t.Errorf("signed out: %d", code)
	}

	// Their own library on the cluster: there, as they reach it. One another
	// person's cluster of the name holds is on that cluster.
	if code, out := call("cy", "u-work", "solver"); code != 200 || out["machine"] != "studio" || out["folder"] != "/home/me/solver" {
		t.Errorf("own library: %d %v", code, out)
	}
	p.site = "pw://eve/testcluster"
	if code, out := call("cy", "u-work", "solver"); code == 200 {
		t.Errorf("another's cluster: %v", out)
	}
}
