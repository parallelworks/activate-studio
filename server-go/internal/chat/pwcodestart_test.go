package chat

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// startMessage posts a new chat's first message with what its composer
// chose.
func startMessage(t *testing.T, base, conv, id string, chose map[string]any) string {
	t.Helper()
	body := map[string]any{"conversationId": conv, "userMessageId": id, "messages": []any{map[string]any{"role": "user", "content": "Start here."}}}
	for k, v := range chose {
		body[k] = v
	}
	return post(t, base, body)
}

func startAgents(f *fakePW, i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, _ := f.created[i]["agentsJson"].(string)
	return s
}

func TestANewChatStartsInTheFolderChosen(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	folder := t.TempDir()

	// Chosen: the session works there, with the Studio's tools as before.
	c := m.Conv.Create(nil, "")
	startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeMachine": "studio", "pwcodeFolder": folder + "/"})
	if len(f.created) != 1 || f.created[0]["workspace"] != folder {
		t.Fatalf("created %v", f.created)
	}
	if a := startAgents(f, 0); !strings.Contains(a, `/api/mcp"`) {
		t.Errorf("no Studio tools: %s", a)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || !l.Picked || l.Workspace != folder || l.Relay || l.Continued {
		t.Fatalf("link %+v", l)
	}
	// The choice is the first message's: a later one goes to the session.
	startMessage(t, base, c.ID, "u2", map[string]any{"pwcodeFolder": t.TempDir()})
	if len(f.created) != 1 {
		t.Errorf("a second session: %v", f.created)
	}

	// Nothing chosen, or the default chosen: where it always worked.
	for _, chose := range []map[string]any{nil, {"pwcodeMachine": "studio"}, {"pwcodeFolder": m.A.Cfg.KBRoot}} {
		c := m.Conv.Create(nil, "")
		startMessage(t, base, c.ID, "u1", chose)
		if got := f.created[len(f.created)-1]["workspace"]; got != m.A.Cfg.KBRoot {
			t.Errorf("%v: %v", chose, got)
		}
		if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Picked {
			t.Errorf("%v: link %+v", chose, l)
		}
	}
	// Not a full path: said, and nothing starts.
	n := len(f.created)
	for _, bad := range []string{"work", "~/work", "/a\nb"} {
		c := m.Conv.Create(nil, "")
		if sse := startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeFolder": bad}); !strings.Contains(sse, "must be a full path") || len(f.created) != n {
			t.Errorf("%q: %s", bad, sse)
		}
	}
}

// A persona in a folder the person chose goes into the session's prompt:
// the folder is theirs, so the Studio writes nothing there, least of all
// over an AGENTS.md of their own.
func TestAPersonaWritesNothingIntoAFolderThePersonChose(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	root := m.A.Cfg.KBRoot
	os.MkdirAll(filepath.Join(root, ".agents"), 0o755)
	os.WriteFile(filepath.Join(root, ".agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: Reviews\n---\n\nCheck every claim against its source.\n"), 0o644)
	prevKB, prevSvc := kbpkg.Default, agentsmd.Default
	kbpkg.Register(http.NewServeMux(), m.A)
	agentsmd.Default = agentsmd.New(m.A)
	t.Cleanup(func() { kbpkg.Default, agentsmd.Default = prevKB, prevSvc })
	with, without := t.TempDir(), t.TempDir()
	mine := "# Mine\n\nBuild with make.\n"
	os.WriteFile(filepath.Join(with, "AGENTS.md"), []byte(mine), 0o644)

	for i, folder := range []string{with, without} {
		c := m.Conv.Create(nil, "")
		startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeFolder": folder, "agent": "reviewer"})
		if len(f.created) != 2*i+1 || f.created[2*i]["workspace"] != folder {
			t.Fatalf("created %v", f.created)
		}
		if a := startAgents(f, 2*i); !strings.Contains(a, "Check every claim against its source.") || !strings.Contains(a, "agent instructions reviewer") {
			t.Errorf("the persona is not in the session's prompt: %s", a)
		}
		if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Persona != "reviewer" || !l.Picked {
			t.Errorf("link %+v", l)
		}
		// Without the persona, another session in the same folder.
		startMessage(t, base, c.ID, "u2", nil)
		if len(f.created) != 2*i+2 || f.created[2*i+1]["workspace"] != folder || strings.Contains(startAgents(f, 2*i+1), "Check every claim") {
			t.Errorf("after dropping it: %v", f.created[len(f.created)-1])
		}
		if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Persona != "" || !l.Picked {
			t.Errorf("link %+v", l)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(with, "AGENTS.md")); string(b) != mine {
		t.Errorf("the person's AGENTS.md:\n%s", b)
	}
	if es, _ := os.ReadDir(without); len(es) != 0 {
		t.Errorf("written into the folder: %v", es)
	}
	if es, _ := os.ReadDir(with); len(es) != 1 {
		t.Errorf("written into the folder: %v", es)
	}
	if _, err := os.Stat(filepath.Join(root, "agents")); err == nil {
		t.Error("the persona's folder in the library was made for a session elsewhere")
	}
}

// canStart makes m2 of relayModule one that takes new sessions.
func canStart(plat *fakeCodePlatform, set map[string]any) {
	plat.mu.Lock()
	defer plat.mu.Unlock()
	plat.others[0]["remoteStart"] = true
	for k, v := range set {
		plat.others[0][k] = v
	}
}

func TestANewChatStartsOnAnotherMachineThroughActivate(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, origin := relayModule(t, local, remote)
	canStart(plat, nil)
	c := m.Conv.Create(nil, "")
	sse := startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver", "pwcodeMode": "accept-edits", "pwcodeModel": "me:openai/gpt-5", "pwcodeAllocation": "a1"})
	if len(local.created) != 0 || len(remote.created) != 1 || !strings.Contains(sse, `"content":"Hello"`) {
		t.Fatalf("made %d here, %d there\n%s", len(local.created), len(remote.created), sse)
	}
	req := remote.created[0]
	if req["workspace"] != "/home/me/solver" || req["permissionMode"] != "accept-edits" || req["model"] != "me:openai/gpt-5" || req["allocation"] != "a1" || req["agentsJson"] != nil || req["agent"] != nil {
		t.Errorf("request %v", req)
	}
	if len(remote.messages) != 1 || remote.messages[0] != "Start here." || len(local.messages) != 0 {
		t.Errorf("sent: %q there, %q here", remote.messages, local.messages)
	}
	l := m.Conv.Get(c.ID).PWCode
	if l == nil || !l.Relay || !l.Continued || l.Machine != "m2" || l.MachineName != "other-box" || l.Workspace != "/home/me/solver" || l.Site != pwcode.RelaySite("m2") || l.Session != "s1" || l.URL != origin+"/code/m2/s1" || l.LastMessage == "" {
		t.Fatalf("link %+v", l)
	}
	// The next message goes to the same session there, whatever is chosen.
	startMessage(t, base, c.ID, "u2", map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/tmp"})
	if len(remote.created) != 1 || len(remote.messages) != 2 || len(local.created) != 0 {
		t.Errorf("second message: made %d, sent %q", len(remote.created), remote.messages)
	}
	// As the person, every time.
	plat.mu.Lock()
	defer plat.mu.Unlock()
	for _, r := range plat.relayed {
		if !strings.HasSuffix(r, " Bearer k") {
			t.Errorf("relayed: %s", r)
		}
	}
}

func TestAChatWithEarlierTurnsBringsThemToTheOtherMachine(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	say := func(s string) *string { return &s }
	c := m.Conv.CreateWith(nil, "", []conversations.Message{{ID: "u0", Role: "user", Content: say("How long is the beam?")}, {ID: "srv-1", Role: "assistant", Content: say("Two metres."), ParentID: say("u0"), Model: say("stub")}}, nil)
	startMessage(t, base, c.ID, "u1", map[string]any{"parentMessageId": "srv-1", "pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver", "engine": "pwcode"})
	if len(remote.messages) != 1 || !strings.HasPrefix(remote.messages[0], "Earlier in this conversation, before you joined it:") || !strings.Contains(remote.messages[0], "Two metres.") {
		t.Errorf("sent %q", remote.messages)
	}
}

func TestAnotherMachinesSessionIsHeldToItsCeiling(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	start := func(chose map[string]any) string {
		t.Helper()
		c := m.Conv.Create(nil, "")
		body := map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver"}
		for k, v := range chose {
			body[k] = v
		}
		return startMessage(t, base, c.ID, "u1", body)
	}
	made := func() int { remote.mu.Lock(); defer remote.mu.Unlock(); return len(remote.created) }
	last := func() any {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		return remote.created[len(remote.created)-1]["permissionMode"]
	}
	// No ceiling of its own: accept-edits at most.
	for _, mode := range []string{"auto", "bypass-permissions"} {
		if sse := start(map[string]any{"pwcodeMode": mode}); !strings.Contains(sse, "other-box takes at most accept-edits") || made() != 0 {
			t.Errorf("%s: %s", mode, sse)
		}
	}
	if sse := start(map[string]any{"pwcodeMode": "plan"}); !strings.Contains(sse, "does not start a session in plan mode") || made() != 0 {
		t.Errorf("plan: %s", sse)
	}
	// A ceiling the Studio does not know allows read-only.
	canStart(plat, map[string]any{"remoteMaxPermissionMode": "plan"})
	if sse := start(map[string]any{"pwcodeMode": "accept-edits"}); !strings.Contains(sse, "at most read-only") || made() != 0 {
		t.Errorf("unknown ceiling: %s", sse)
	}
	// None asked for: read-only, the platform's own default.
	canStart(plat, map[string]any{"remoteMaxPermissionMode": "auto"})
	if start(nil); made() != 1 || last() != "read-only" {
		t.Errorf("no mode: %v", remote.created)
	}
	if start(map[string]any{"pwcodeMode": "auto"}); made() != 2 || last() != "auto" {
		t.Errorf("auto under auto: %v", remote.created)
	}
	if len(local.created) != 0 {
		t.Errorf("made here: %v", local.created)
	}
}

func TestAnotherMachineIsRefusedWithAReason(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	for _, c := range []struct {
		chose map[string]any
		set   map[string]any
		want  string
	}{
		{map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me", "agent": "reviewer"}, nil, "The reviewer persona works only on this Studio's own pw code machine"},
		{map[string]any{"pwcodeMachine": "m2"}, nil, "Choose the folder on other-box"},
		{map[string]any{"pwcodeMachine": "m7", "pwcodeFolder": "/home/me"}, nil, "does not list that machine as yours"},
		{map[string]any{"pwcodeMachine": "../m2", "pwcodeFolder": "/home/me"}, nil, "Not a machine"},
		{map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me"}, map[string]any{"status": "unreachable"}, "does not take new sessions"},
		{map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me"}, map[string]any{"status": "connected", "remoteStart": false}, "does not take new sessions"},
	} {
		canStart(plat, c.set)
		conv := m.Conv.Create(nil, "")
		if sse := startMessage(t, base, conv.ID, "u1", c.chose); !strings.Contains(sse, "event: error") || !strings.Contains(sse, c.want) {
			t.Errorf("%v: %s", c.chose, sse)
		}
		if l := m.Conv.Get(conv.ID).PWCode; l != nil {
			t.Errorf("%v: link %+v", c.chose, l)
		}
	}
	if len(local.created) != 0 || len(remote.created) != 0 {
		t.Errorf("made %v, %v", local.created, remote.created)
	}
}

func TestTheComposerIsToldWhereAChatStarts(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	prev := agentsmd.Default
	agentsmd.Default = agentsmd.New(m.A)
	t.Cleanup(func() { agentsmd.Default = prev })
	out := get(t, base+"/api/chat/pwcode/start")
	// The Studio's own machine once (the platform's m1 is its daemon),
	// first, then the others.
	var ids []string
	for _, x := range out["machines"].([]any) {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"studio", "m2"}) || out["machine"] != "studio" || out["folder"] != m.A.Cfg.KBRoot || out["available"] != true {
		t.Fatalf("start: %v", out)
	}
	own := out["machines"].([]any)[0].(map[string]any)
	if own["name"] != "gpu-box.example" || own["remoteStart"] != true || own["status"] != "connected" || own["directories"] != true {
		t.Errorf("own machine: %v", own)
	}
	if s := mustJSON(out["sessions"]); !strings.Contains(s, `"machineId":"studio"`) || !strings.Contains(s, `"workspace":"/home/me/repo"`) || !strings.Contains(s, `"machineId":"m2"`) || strings.Contains(s, "Fix the build") {
		t.Errorf("recent folders: %s", s)
	}
	if out := get(t, base+"/api/chat/pwcode/start?persona=Reviewer"); out["folder"] != filepath.Join(m.A.Cfg.KBRoot, "agents", "reviewer") {
		t.Errorf("with a persona: %v", out["folder"])
	}
	// Without ACTIVATE there is nothing to choose, and the platform is not
	// asked.
	plat.mu.Lock()
	reads := plat.reads
	plat.mu.Unlock()
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return false }
	m.forgetPWCodeAvailability("")
	if out := get(t, base+"/api/chat/pwcode/start"); out["available"] != false || len(out["machines"].([]any)) != 0 || !strings.Contains(out["reason"].(string), "ACTIVATE") {
		t.Errorf("no ACTIVATE: %v", out)
	}
	if code, _, _ := send(t, http.MethodGet, base+"/api/chat/pwcode/start/machines/studio/directories?path=/", ""); code != 409 {
		t.Errorf("folders with no ACTIVATE: %d", code)
	}
	plat.mu.Lock()
	defer plat.mu.Unlock()
	if plat.reads != reads {
		t.Errorf("the platform was read %d times", plat.reads-reads)
	}
}

func TestTheComposerListsFolders(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"alpha/.git", "beta", ".hidden"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), nil, 0o644)
	os.Symlink(filepath.Join(dir, "beta"), filepath.Join(dir, "link"))
	local := &fakePW{}
	remote := &fakePW{routes: map[string]http.HandlerFunc{"GET /v1/directories": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"path":"` + r.URL.Query().Get("path") + `","entries":[{"name":"there"}]}`))
	}}}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	list := func(machine, q string) (int, map[string]any) {
		code, out, _ := send(t, http.MethodGet, base+"/api/chat/pwcode/start/machines/"+machine+"/directories?"+q, "")
		return code, out
	}
	// This computer's, where its pw code does not list folders.
	code, out := list("studio", url.Values{"path": {dir}}.Encode())
	if got := mustJSON(out["entries"]); code != 200 || out["path"] != dir || got != `[{"name":".hidden"},{"name":"alpha","repository":true},{"name":"beta"},{"name":"link"}]` {
		t.Errorf("here: %d %v", code, out)
	}
	if _, out := list("studio", url.Values{"path": {dir}, "filter": {"BE"}}.Encode()); mustJSON(out["entries"]) != `[{"name":"beta"}]` {
		t.Errorf("filtered: %v", out)
	}
	home, _ := os.UserHomeDir()
	if _, out := list("studio", "path=~"); out["path"] != home || out["home"] != home {
		t.Errorf("home: %v", out)
	}
	if code, out := list("studio", url.Values{"path": {filepath.Join(dir, "none")}}.Encode()); code != 400 || !strings.Contains(out["detail"].(string), "is not a folder your account there can read") {
		t.Errorf("no such folder: %d %v", code, out)
	}
	// Another machine's, through the platform as the person.
	if code, out := list("m2", "path=/home/me"); code != 200 || mustJSON(out["entries"]) != `[{"name":"there"}]` {
		t.Errorf("there: %d %v", code, out)
	}
	plat.mu.Lock()
	relayed := strings.Join(plat.relayed, "\n")
	plat.mu.Unlock()
	if !strings.Contains(relayed, "GET /api/code/machines/m2/directories Bearer k") {
		t.Errorf("relayed: %s", relayed)
	}
	// This computer's pw code lists them once it can.
	local.mu.Lock()
	local.routes = map[string]http.HandlerFunc{"GET /v1/directories": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"path":"/d","entries":[]}`))
	}}
	local.status["directories"] = true
	local.mu.Unlock()
	m.pw.mu.Lock()
	m.pw.here = nil
	m.pw.mu.Unlock()
	if _, out := list("studio", "path=/d"); out["path"] != "/d" {
		t.Errorf("listed by pw code: %v", out)
	}
}

// TestStartBridgeHelper is the process that stands in for `pw ssh ... pw
// code daemon proxy`: it joins its stdin and stdout to a daemon's socket.
func TestStartBridgeHelper(t *testing.T) {
	sock := os.Getenv("START_BRIDGE_SOCK")
	if sock == "" {
		t.Skip("helper process")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(1)
	}
	go func() { io.Copy(c, os.Stdin); os.Exit(0) }()
	io.Copy(os.Stdout, c)
	os.Exit(0)
}

// On a Studio with sign-in, the cluster is reached as the person, with
// their own credential: its folders, and the session in the one they chose.
func TestUnderSignInTheClusterIsReachedAsThePerson(t *testing.T) {
	f := &fakePW{script: helloTurn, status: map[string]any{"machineId": "d9", "hostname": "login1"}}
	m := signInStudio(t, f)
	sock := pwcodetest.Socket(t, f)
	var keys []string
	pwSites().Daemon.SetBridgeSpawn(func(resource, key string) *exec.Cmd {
		keys = append(keys, resource+" "+key)
		cmd := exec.Command(os.Args[0], "-test.run=TestStartBridgeHelper")
		cmd.Env = append(os.Environ(), "START_BRIDGE_SOCK="+sock)
		return cmd
	})
	var ran []string
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "runs", ".git"), 0o755)
	m.bridgeRunFn = func(ctx context.Context, site, key string, argv []string, _ time.Duration) (remote.Result, error) {
		ran = append(ran, site+" "+key)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.Output()
		return remote.Result{Stdout: string(out)}, err
	}
	prev := m.A.Providers.SpaceFolder
	m.A.Providers.SpaceFolder = func(owner string) (string, string) { return "pw://cy/testcluster", "/home/" + owner + "/studio" }
	t.Cleanup(func() { m.A.Providers.SpaceFolder = prev })

	call := func(who, path string) (int, map[string]any) {
		t.Helper()
		r := as(who, http.MethodGet, path, "")
		if rest, ok := strings.CutPrefix(r.URL.Path, "/api/chat/pwcode/start/machines/"); ok {
			r.SetPathValue("machine", strings.TrimSuffix(rest, "/directories"))
		}
		rec := httptest.NewRecorder()
		h := m.startListing
		if strings.HasSuffix(r.URL.Path, "/directories") {
			h = m.startDirectories
		}
		if err := h(rec, r); err != nil {
			t.Fatalf("%s %s: %v", who, path, err)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	_, out := call("cy", "/api/chat/pwcode/start")
	if out["available"] != true || out["folder"] != "/home/cy/studio" {
		t.Fatalf("start: %v", out)
	}
	if own := out["machines"].([]any)[0].(map[string]any); own["id"] != "studio" || own["name"] != "login1" || own["kind"] != "existing" {
		t.Errorf("own: %v", own)
	}
	code, out := call("cy", "/api/chat/pwcode/start/machines/studio/directories?path=~")
	if code != 200 || out["home"] != home || mustJSON(out["entries"]) != `[{"name":"runs","repository":true}]` {
		t.Errorf("folders: %d %v", code, out)
	}
	// A path is a word to the script, never part of it.
	marker := filepath.Join(home, "marker")
	if code, _ := call("cy", "/api/chat/pwcode/start/machines/studio/directories?path="+url.QueryEscape("/nope; touch "+marker+" $(touch "+marker+")")); code != 400 {
		t.Errorf("odd path: %d", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the path ran")
	}

	c := m.Conv.Create(nil, "cy")
	sse := sendAs(t, m, "cy", c.ID, "u1", "", `"engine":"pwcode","pwcodeFolder":"/home/cy/runs",`)
	if len(f.created) != 1 || f.created[0]["workspace"] != "/home/cy/runs" {
		t.Fatalf("created %v\n%s", f.created, sse)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Site != "testcluster" || !l.Picked || l.Workspace != "/home/cy/runs" {
		t.Errorf("link %+v", l)
	}
	// The default is their own library, not marked as a choice.
	c = m.Conv.Create(nil, "cy")
	sendAs(t, m, "cy", c.ID, "u1", "", `"engine":"pwcode",`)
	if len(f.created) != 2 || f.created[1]["workspace"] != "/home/cy/studio" || m.Conv.Get(c.ID).PWCode.Picked {
		t.Errorf("default: %v", f.created[len(f.created)-1])
	}

	// bob has no ACTIVATE sign-in of his own: nothing runs as anyone else.
	n := len(ran)
	if _, out := call("bob", "/api/chat/pwcode/start"); out["available"] != false {
		t.Errorf("bob's start: %v", out)
	}
	if code, _ := call("bob", "/api/chat/pwcode/start/machines/studio/directories?path=~"); code != 409 {
		t.Errorf("bob's folders: %d", code)
	}
	if len(ran) != n {
		t.Errorf("ran for bob: %v", ran[n:])
	}
	for _, k := range append(ran, keys...) {
		if k != "testcluster cy-own-key" {
			t.Errorf("reached as %q", k)
		}
	}
}
