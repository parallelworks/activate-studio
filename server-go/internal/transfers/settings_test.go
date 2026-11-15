package transfers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

func hasLoc(ls []Location, id string) bool {
	for _, l := range ls {
		if l.ID == id {
			return true
		}
	}
	return false
}

func TestSettingsShape(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	st, out := a.do("GET", "/api/transfers/settings", "", nil)
	if st != 200 || out["managed"] != false {
		t.Fatalf("%d %v", st, out)
	}
	if n := len(out["savedLocations"].([]any)); n != 2 {
		t.Fatalf("saved %d", n)
	}
	cf := out["computerFolders"].([]any)
	if len(cf) != 3 {
		t.Fatalf("folders %v", cf)
	}
	for i, n := range BuiltInFolders {
		f := cf[i].(map[string]any)
		if f["label"] != n || f["builtIn"] != true || f["enabled"] != true || f["path"] != filepath.Join(r.home, n) {
			t.Fatalf("folder %v", f)
		}
	}
	mv := out["movers"].(map[string]any)
	rs := mv["rsync"].(map[string]any)
	cd := mv["conduit"].(map[string]any)
	if rs["enabled"] != true || rs["path"] != "rsync" || mv["studioCopy"].(map[string]any)["enabled"] != true || cd["enabled"] != false || cd["server"] != nil {
		t.Fatalf("movers %v", mv)
	}
	d := out["defaults"].(map[string]any)
	if d["mover"] != "auto" || d["checksum"] != false || d["preserveTimes"] != true || d["parallel"].(float64) != 4 {
		t.Fatalf("defaults %v", d)
	}
	hl := out["hostLocations"].(map[string]any)
	if hl["enabled"] != true || len(hl["hidden"].([]any)) != 0 {
		t.Fatalf("hostLocations %v", hl)
	}
	if out["mcpServers"] == nil {
		t.Fatal("no mcpServers")
	}
	det := out["detected"].(map[string]any)
	if det["rsyncLocal"] != true || det["rsyncVersion"] == "" || det["clusters"].(float64) != 1 || len(det["libraryHosts"].([]any)) != 0 {
		t.Fatalf("detected %v", det)
	}
}

func TestSettingsValidation(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	_, base := a.do("GET", "/api/transfers/settings", "", nil)
	file := filepath.Join(r.dir, "a-file")
	write(t, file, "x")
	for field, change := range map[string]func(m map[string]any){
		"savedLocations[0].root": func(m map[string]any) { saved(m)[0]["root"] = "relative/path" },
		"savedLocations[1].host": func(m map[string]any) { saved(m)[1]["host"] = "user@host b" },
		"savedLocations[0].host": func(m map[string]any) { saved(m)[0]["host"] = "-oProxyCommand=touch x" },
		"computerFolders[3].path": func(m map[string]any) {
			m["computerFolders"] = append(m["computerFolders"].([]any), map[string]any{"path": filepath.Join(r.dir, "missing"), "enabled": true})
		},
		"computerFolders[3].path ": func(m map[string]any) {
			m["computerFolders"] = append(m["computerFolders"].([]any), map[string]any{"path": file, "enabled": true})
		},
		"computerFolders[3].path  ": func(m map[string]any) {
			m["computerFolders"] = append(m["computerFolders"].([]any), map[string]any{"path": "/", "enabled": true})
		},
		"defaults.parallel":     func(m map[string]any) { m["defaults"].(map[string]any)["parallel"] = 17 },
		"defaults.mover":        func(m map[string]any) { m["defaults"].(map[string]any)["mover"] = "fast" },
		"movers.rsync.path":     func(m map[string]any) { m["movers"].(map[string]any)["rsync"].(map[string]any)["path"] = "rsync -e sh" },
		"movers.conduit.server": func(m map[string]any) { m["movers"].(map[string]any)["conduit"].(map[string]any)["enabled"] = true },
	} {
		m := clone(base)
		change(m)
		st, out := a.do("PUT", "/api/transfers/settings", "", m)
		if st != 400 || !strings.HasPrefix(out["error"].(string), strings.TrimSpace(field)+":") {
			t.Fatalf("%s: %d %v", field, st, out)
		}
	}
	// Nothing was stored by the refusals.
	if len(r.set.S.ComputerFolders) != 0 {
		t.Fatalf("stored %v", r.set.S.ComputerFolders)
	}
}

func saved(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, x := range m["savedLocations"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

func clone(m map[string]any) map[string]any {
	var out map[string]any
	b, _ := jsonMarshal(m)
	jsonUnmarshal(b, &out)
	return out
}

func TestSettingsApplyLive(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	extra := filepath.Join(r.dir, "extra")
	os.MkdirAll(extra, 0o755)
	write(t, filepath.Join(r.kb, "d", "f.txt"), "f")
	_, m := a.do("GET", "/api/transfers/settings", "", nil)
	m["computerFolders"].([]any)[0].(map[string]any)["enabled"] = false // Downloads
	m["computerFolders"] = append(m["computerFolders"].([]any), map[string]any{"path": extra, "label": "Extra", "enabled": true})
	m["movers"].(map[string]any)["rsync"].(map[string]any)["enabled"] = false
	m["defaults"] = map[string]any{"mover": "auto", "checksum": true, "preserveTimes": false, "parallel": 2}
	st, out := a.do("PUT", "/api/transfers/settings", "", m)
	if st != 200 {
		t.Fatalf("%d %v", st, out)
	}
	cf := out["computerFolders"].([]any)
	if len(cf) != 4 || cf[0].(map[string]any)["enabled"] != false || cf[3].(map[string]any)["path"] != extra {
		t.Fatalf("folders %v", cf)
	}
	ls := r.s.Locations(context.Background(), single)
	if hasLoc(ls, "computer:Downloads") || !hasLoc(ls, "computer:extra") {
		t.Fatalf("locations %v", ls)
	}
	// rsync is off: never chosen, even when named, and the plan says why.
	q := req(ActionCopy, "library:kb", []string{"d"}, "saved:b", "")
	p := r.mustPlan(single, q)
	if p.Mover != MoverCopy || !strings.Contains(p.Movers[1].Why, "turned off") {
		t.Fatalf("plan %+v", p.Movers)
	}
	q.Options.Mover = MoverRsync
	if _, err := r.plan(single, q); statusOf(err) != 400 {
		t.Fatalf("named rsync: %v", err)
	}
	// The defaults fill what a request leaves out.
	q.Options.Mover = ""
	pr, err := r.s.plan(context.Background(), single, q)
	if err != nil || !pr.req.Options.checksum() || pr.req.Options.preserve() || streams(pr.set) != 2 {
		t.Fatalf("defaults %+v %v", pr.req.Options, err)
	}
	// A default mover is preferred where it can; rsync on again for this.
	m["movers"].(map[string]any)["rsync"].(map[string]any)["enabled"] = true
	m["defaults"] = map[string]any{"mover": "studio-copy", "checksum": false, "preserveTimes": true, "parallel": 4}
	m["computerFolders"] = out["computerFolders"]
	if st, out := a.do("PUT", "/api/transfers/settings", "", m); st != 200 {
		t.Fatalf("%d %v", st, out)
	}
	if p := r.mustPlan(single, q); p.Mover != MoverCopy || !p.Movers[1].OK {
		t.Fatalf("default mover %+v", p)
	}
}

func TestSettingsTestRoute(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	st, out := a.do("POST", "/api/transfers/settings/test", "", map[string]any{"host": "user@host-a", "root": r.hostA})
	if st != 200 || out["ok"] != true || out["rsync"] != true || out["rsyncVersion"] == nil || out["writable"] != true || out["roundTripMs"] == nil || out["freeBytes"] == nil {
		t.Fatalf("%d %v", st, out)
	}
	noStaging(t, r.hostA)
	st, out = a.do("POST", "/api/transfers/settings/test", "", map[string]any{"host": "user@host-a", "root": r.hostA, "rsyncPath": "/no/such/rsync"})
	if st != 200 || out["ok"] != true || out["rsync"] != false {
		t.Fatalf("rsync path %d %v", st, out)
	}
	st, out = a.do("POST", "/api/transfers/settings/test", "", map[string]any{"host": "user@host-a", "root": filepath.Join(r.dir, "nowhere")})
	if st != 200 || out["ok"] != false || out["why"] == nil {
		t.Fatalf("missing root %d %v", st, out)
	}
	for body, field := range map[string]string{"-x|/tmp": "host", "a b|/tmp": "host", "h|tmp": "root"} {
		h, root, _ := strings.Cut(body, "|")
		st, out = a.do("POST", "/api/transfers/settings/test", "", map[string]any{"host": h, "root": root})
		if st != 400 || !strings.HasPrefix(out["error"].(string), field+":") {
			t.Fatalf("%s: %d %v", body, st, out)
		}
	}
	st, out = a.do("POST", "/api/transfers/settings/test", "", map[string]any{"conduit": "none"})
	if st != 200 || out["ok"] != false {
		t.Fatalf("conduit %d %v", st, out)
	}
}

func TestSettingsManagedWithSignIn(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	shared := filepath.Join(r.dir, "shared")
	os.MkdirAll(shared, 0o755)
	r.env["STUDIO_TRANSFER_FOLDERS"] = shared
	r.env["STUDIO_RSYNC"] = "off"
	r.env["STUDIO_TRANSFER_STREAMS"] = "8"
	st, out := a.do("GET", "/api/transfers/settings", "alice", nil)
	if st != 200 || out["managed"] != true || len(out["savedLocations"].([]any)) != 0 {
		t.Fatalf("%d %v", st, out)
	}
	cf := out["computerFolders"].([]any)
	if len(cf) != 1 || cf[0].(map[string]any)["path"] != shared {
		t.Fatalf("folders %v", cf)
	}
	if out["movers"].(map[string]any)["rsync"].(map[string]any)["enabled"] != false || out["defaults"].(map[string]any)["parallel"].(float64) != 8 {
		t.Fatalf("env values %v", out)
	}
	if out["hostLocations"].(map[string]any)["enabled"] != false {
		t.Fatal("host locations on with sign-in")
	}
	if st, _ := a.do("PUT", "/api/transfers/settings", "alice", out); st != 403 {
		t.Fatalf("put %d", st)
	}
	if st, _ := a.do("POST", "/api/transfers/settings/test", "alice", map[string]any{"host": "h", "root": "/"}); st != 403 {
		t.Fatalf("test %d", st)
	}
	alice := Viewer{ID: "alice", AuthOn: true, Key: "alice-key"}
	ls := r.s.Locations(context.Background(), alice)
	if !hasLoc(ls, "computer:shared") || hasLoc(ls, "computer:Downloads") || hasLoc(ls, "saved:a") {
		t.Fatalf("locations %v", ls)
	}
	write(t, filepath.Join(shared, "x.txt"), "x")
	p, err := r.plan(alice, req(ActionCopy, "computer:shared", []string{"x.txt"}, "library:kb", ""))
	if err != nil || p.Mover != MoverCopy || p.Movers[1].OK {
		t.Fatalf("plan %+v %v", p, err)
	}
}

func TestHostLocations(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	home := filepath.Join(r.dir, "lib-host-home")
	os.MkdirAll(filepath.Join(home, "results"), 0o755)
	write(t, filepath.Join(home, "results", "r.txt"), "result")
	r.runner.home = home
	site, err := remote.SSHSite("user@lib-host", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	libRoot := filepath.Join(r.dir, "remote-lib")
	os.MkdirAll(libRoot, 0o755)
	r.more = []LibraryLoc{{ID: "far2", Label: "On lib-host", FS: &RemoteFS{R: r.runner, SiteID: site, Dir: libRoot, Name: remote.SiteName(site)}}}
	name := remote.SiteName(site)
	var host Location
	for _, l := range r.s.Locations(context.Background(), single) {
		if l.Kind == KindHost {
			host = l
		}
	}
	if host.ID == "" || host.Label != name+" home" || !host.Available || !host.Browsable || host.Indexed || !strings.HasSuffix(host.Detail, home) {
		t.Fatalf("host location %+v", host)
	}
	ls, err := r.s.Browse(context.Background(), single, host.ID, nil)
	if err != nil || len(ls.Entries) != 1 || ls.Entries[0].Name != "results" {
		t.Fatalf("browse %+v %v", ls, err)
	}
	tr := r.run(single, req(ActionCopy, host.ID, []string{"results"}, "library:kb", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.kb, "results", "r.txt")) != "result" {
		t.Fatalf("%+v", tr)
	}
	_, out := a.do("GET", "/api/transfers/settings", "", nil)
	if lh := out["detected"].(map[string]any)["libraryHosts"].([]any); len(lh) != 1 || lh[0] != name {
		t.Fatalf("libraryHosts %v", lh)
	}
	// Hidden by name, or all switched off, or sign-in on: not offered.
	r.set.S.HostLocations.Hidden = []string{name}
	if hasLoc(r.s.Locations(context.Background(), single), host.ID) {
		t.Fatal("hidden host offered")
	}
	r.set.S.HostLocations = HostLocations{Enabled: false}
	if hasLoc(r.s.Locations(context.Background(), single), host.ID) {
		t.Fatal("switched off host offered")
	}
	r.set.S.HostLocations = HostLocations{Enabled: true}
	for _, l := range r.s.Locations(context.Background(), Viewer{ID: "u", AuthOn: true, Key: "k"}) {
		if l.Kind == KindHost {
			t.Fatal("host location with sign-in on")
		}
	}
}

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
