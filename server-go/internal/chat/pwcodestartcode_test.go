package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	remotelib "github.com/parallelworks/activate-studio/server-go/internal/remote"
)

const testToolsURL = "https://platform.example/me/session/ada/studio/api/mcp"

// toolsReach makes the Studio's tools reachable from another machine, as
// behind the session proxy, and records the sessions they are attached to.
func toolsReach(m *Module, fail error) *[]string {
	m.bridgeReachFn = func(*http.Request) (string, string) { return testToolsURL, "" }
	attached := &[]string{}
	m.bridgeAttachFn = func(r *http.Request, key, machine, session, url string) error {
		*attached = append(*attached, strings.Join([]string{key, machine, session, url}, " "))
		return fail
	}
	return attached
}

// codeStart starts a session as the Code page's route does.
func codeStart(t *testing.T, m *Module, machine string, body *jsonv.Object) (*jsonv.Object, bool, error) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/code-page/machines/"+machine+"/sessions", nil)
	var mach *jsonv.Object
	if o, err := codesessions.PlatformMachine(r, m.pf, machine); err == nil {
		mach = o
	}
	// The reply is recorded behind the request.
	t.Cleanup(func() {
		waitFor(t, func() bool {
			for _, c := range m.Conv.List() {
				if m.following(c.ID) {
					return false
				}
			}
			return true
		})
	})
	return m.startFromCode(r, machine, mach, body)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("waited too long")
}

func chatOf(m *Module, id string) *conversations.Conversation {
	for _, c := range m.Conv.List() {
		if c.PWCode != nil && c.PWCode.Session == id {
			return c
		}
	}
	return nil
}

// A session started on the Code page on the machine the Studio drives is
// made as a chat's is: the studio agent, with the Studio's tools and
// instructions, and what the person chose.
func TestACodePageStartHereHasTheStudiosTools(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, _, _ := continueModule(t, f, "d1")
	folder := t.TempDir()
	v, ok, err := codeStart(t, m, "m1", jsonv.NewObject("workspace", folder, "permissionMode", "accept-edits", "effort", "high", "prompt", "Find the newest spreadsheet."))
	if err != nil || !ok || jsonv.String(jsonv.Path(v, "id")) != "s1" {
		t.Fatalf("%v %v %v", v, ok, err)
	}
	req := f.created[0]
	if req["workspace"] != folder || req["permissionMode"] != "accept-edits" || req["effort"] != "high" || req["agent"] != "studio" {
		t.Errorf("request %v", req)
	}
	if a := startAgents(f, 0); !strings.Contains(a, `/api/mcp"`) || !strings.Contains(a, "mcp__studio__query_corpus") {
		t.Errorf("no tools or instructions: %s", a)
	}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.messages) == 1 })
	if f.messages[0] != "Find the newest spreadsheet." {
		t.Errorf("sent %q", f.messages)
	}
	c := chatOf(m, "s1")
	if c == nil || c.PWCode.Relay || !c.PWCode.Picked || c.Title == nil || *c.Title != "Find the newest spreadsheet." || len(c.Messages) == 0 || *c.Messages[0].Content != "Find the newest spreadsheet." {
		t.Fatalf("chat %+v", c)
	}
}

// Its tools out of reach: the session starts, and its chat says why it has
// none; it keeps saying so.
func TestACodePageStartHereSaysItHasNoTools(t *testing.T) {
	f := &fakePW{script: helloTurn, mcp: "fail"}
	m, _, _ := continueModule(t, f, "d1")
	_, ok, err := codeStart(t, m, "m1", jsonv.NewObject("workspace", t.TempDir(), "permissionMode", "read-only"))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	last := f.created[len(f.created)-1]
	if last["agentsJson"] != nil || last["permissionMode"] != "read-only" {
		t.Errorf("request %v", last)
	}
	if c := chatOf(m, "s1"); c == nil || !strings.Contains(c.PWCode.ToolsError, "refused") {
		t.Fatalf("chat %+v", c)
	}
}

// On another machine whose pw code has the Studio's tools, the session is
// started through ACTIVATE with the Studio's instructions after the
// person's message; on one without them it gets neither.
func TestACodePageStartElsewhereBringsTheInstructions(t *testing.T) {
	for _, has := range []bool{true, false} {
		local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
		m, _, plat, origin := relayModule(t, local, remote)
		canStart(plat, nil)
		toolsReach(m, nil)
		if has {
			m.bridgePrefs().set(userID(httptest.NewRequest("GET", "/", nil)), "m2", bridgePref{URL: testToolsURL})
		}
		v, ok, err := codeStart(t, m, "m2", jsonv.NewObject("workspace", "/home/me/solver", "permissionMode", "read-only", "prompt", "Which folders are largest?"))
		if err != nil || !ok || len(local.created) != 0 || len(remote.created) != 1 {
			t.Fatalf("%v %v %v", v, ok, err)
		}
		prompt, _ := remote.created[0]["prompt"].(string)
		if !strings.HasPrefix(prompt, "Which folders are largest?") || strings.Contains(prompt, "mcp__studio__query_corpus") != has || strings.Contains(prompt, relayInstructionsHead) != has {
			t.Errorf("has %v: prompt %q", has, prompt)
		}
		l := chatOf(m, "s1")
		if l == nil || !l.PWCode.Relay || l.PWCode.HasTools != has || l.PWCode.Untold || l.PWCode.URL != origin+"/code/m2/s1" {
			t.Fatalf("has %v: link %+v", has, l)
		}
		if *l.Messages[0].Content != "Which folders are largest?" {
			t.Errorf("the chat's message: %q", *l.Messages[0].Content)
		}
	}
}

// Where the machine attaches servers, the session is given the tools
// before its first turn, as the person; when that fails the chat says so.
func TestASessionElsewhereIsGivenTheToolsBeforeItsFirstTurn(t *testing.T) {
	for _, fail := range []error{nil, pwErr(502, "no attach")} {
		local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
		m, _, plat, _ := relayModule(t, local, remote)
		canStart(plat, map[string]any{"sessionMCPAttach": true})
		attached := toolsReach(m, fail)
		_, ok, err := codeStart(t, m, "m2", jsonv.NewObject("workspace", "/home/me/solver", "permissionMode", "read-only", "prompt", "hello"))
		if err != nil || !ok || remote.created[0]["prompt"] != nil || len(remote.messages) != 1 {
			t.Fatalf("%v %v %v %q", ok, err, remote.created, remote.messages)
		}
		if len(*attached) != 1 || (*attached)[0] != "k m2 s1 "+testToolsURL {
			t.Errorf("attached %v", *attached)
		}
		l := chatOf(m, "s1").PWCode
		if l.HasTools != (fail == nil) || (l.ToolsError == "") != (fail == nil) || strings.Contains(remote.messages[0], relayInstructionsHead) != (fail == nil) || !strings.HasPrefix(remote.messages[0], "hello") {
			t.Errorf("fail %v: %+v, sent %q", fail, l, remote.messages[0])
		}
	}
}

// A new chat on another machine brings the instructions once, with its
// first message.
func TestANewChatElsewhereBringsTheInstructionsOnce(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	toolsReach(m, nil)
	m.bridgePrefs().set(userID(httptest.NewRequest("GET", "/", nil)), "m2", bridgePref{URL: testToolsURL})
	c := m.Conv.Create(nil, "")
	startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver"})
	startMessage(t, base, c.ID, "u2", nil)
	if len(remote.messages) != 2 || !strings.HasPrefix(remote.messages[0], "Start here."+relayInstructionsHead) || !strings.Contains(remote.messages[0], "mcp__studio__search_kb") || strings.Contains(remote.messages[1], relayInstructionsHead) {
		t.Fatalf("sent %q", remote.messages)
	}
	if l := m.Conv.Get(c.ID).PWCode; !l.HasTools || l.Untold {
		t.Errorf("link %+v", l)
	}
	if st := get(t, base+"/api/chat/pwcode/"+c.ID); st["tools"].(map[string]any)["has"] != true {
		t.Errorf("tools %v", st["tools"])
	}
}

// A start ACTIVATE refuses says why, and makes no chat.
func TestARefusedCodePageStartMakesNoChat(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, _, plat, _ := relayModule(t, local, remote)
	canStart(plat, nil)
	remote.routes = map[string]http.HandlerFunc{"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"detail":"that folder is not there"}`))
	}}
	_, _, err := codeStart(t, m, "m2", jsonv.NewObject("workspace", "/nowhere", "permissionMode", "read-only"))
	if err == nil || !strings.Contains(apiMessage(err), "that folder is not there") || len(m.Conv.List()) != 0 {
		t.Fatalf("%v, %d chats", err, len(m.Conv.List()))
	}
}

// A start the person ticked adds the tools on its machine first, so the
// session has them; unticked, or where they cannot be added, it starts
// without them and its chat says why.
func TestACodePageStartAddsTheToolsFirst(t *testing.T) {
	for what, c := range map[string]struct {
		tools any
		who   string
		adds  int
		has   bool
		says  string
	}{
		"ticked":                     {true, "me", 1, true, ""},
		"unticked":                   {false, "me", 0, false, ""},
		"not asked":                  {nil, "me", 0, false, ""},
		"pw there is someone else's": {true, "ada", 0, false, "signed in as ada"},
	} {
		local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
		m, _, plat, _ := relayModule(t, local, remote)
		canStart(plat, map[string]any{"kind": "existing", "owner": "me", "name": "other-box"})
		toolsReach(m, nil)
		me := &auth.User{ID: "me", Username: "me", Source: "proxy"}
		f := &fakeMachine{who: c.who, curl: true, m: m, as: me}
		m.bridgeRunFn = func(ctx context.Context, site, key string, argv []string, d time.Duration) (rr remotelib.Result, err error) {
			if len(remote.created) != 0 {
				t.Errorf("%s: ran on the machine after the session was made", what)
			}
			if len(argv) > 3 && argv[2] == bridgeTry {
				m.bridgeMu.Lock()
				m.bridgeVisits = map[string]bridgeVisit{"me": {At: time.Now()}}
				m.bridgeMu.Unlock()
				return remotelib.Result{Stdout: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{}]}}`}, nil
			}
			return f.run(ctx, site, key, argv, d)
		}
		body := jsonv.NewObject("workspace", "/home/me/solver", "permissionMode", "read-only", "prompt", "hello")
		if c.tools != nil {
			body.Set(codesessions.StartTools, c.tools)
		}
		viewerKeys = map[string]string{"me": "k"}
		r := asViewer(httptest.NewRequest("POST", "/api/code-page/machines/m2/sessions", nil), me)
		mach, err := codesessions.PlatformMachine(r, m.pf, "m2")
		if err != nil {
			t.Fatal(err)
		}
		_, ok, err := m.startFromCode(r, "m2", mach, body)
		if err != nil || !ok || len(remote.created) != 1 {
			t.Fatalf("%s: %v %v %v", what, ok, err, remote.created)
		}
		if _, sent := remote.created[0][codesessions.StartTools]; sent {
			t.Errorf("%s: sent to ACTIVATE: %v", what, remote.created[0])
		}
		l := chatOf(m, "s1").PWCode
		prompt, _ := remote.created[0]["prompt"].(string)
		if f.adds != c.adds || l.HasTools != c.has || strings.Contains(prompt, relayInstructionsHead) != c.has || !strings.Contains(l.ToolsError, c.says) || (c.says == "") != (l.ToolsError == "") {
			t.Errorf("%s: %d adds, %+v, prompt %q", what, f.adds, l, prompt)
		}
		waitFor(t, func() bool { return !m.following(chatOf(m, "s1").ID) })
	}
}

// Where a machine's pw code takes MCP servers with a new session, the
// Studio's go with it, with the tools that only read allowed; a platform
// that refuses the rules, or the server too, starts it without those.
func TestASessionElsewhereIsMadeWithTheTools(t *testing.T) {
	for refuses, want := range map[string]struct {
		creates int
		has     bool
	}{"": {1, true}, "allowedTools": {2, true}, "mcpServers": {3, false}} {
		local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
		m, _, plat, _ := relayModule(t, local, remote)
		canStart(plat, map[string]any{"sessionMCPCreate": true, "sessionMCPAttach": true})
		attached := toolsReach(m, nil)
		var sent []map[string]any
		if refuses != "" {
			remote.routes = map[string]http.HandlerFunc{"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				sent = append(sent, b)
				if b[refuses] != nil {
					w.WriteHeader(422)
					w.Write([]byte(`{"detail":"unexpected property"}`))
					return
				}
				remote.mu.Lock()
				remote.runTurn(b["prompt"].(string))
				remote.mu.Unlock()
				w.Write([]byte(`{"id":"s1"}`))
			}}
		}
		_, ok, err := codeStart(t, m, "m2", jsonv.NewObject("workspace", "/home/me/solver", "permissionMode", "read-only", "prompt", "hello"))
		if err != nil || !ok || len(*attached) != 0 {
			t.Fatalf("%v %v, attached %v", ok, err, *attached)
		}
		if refuses == "" {
			sent = remote.created
		}
		servers, _ := sent[0]["mcpServers"].([]any)
		if len(servers) != 1 || servers[0].(map[string]any)["url"] != testToolsURL || servers[0].(map[string]any)["platformAuth"] != true {
			t.Fatalf("sent %v", sent[0])
		}
		for _, a := range sent[0]["allowedTools"].([]any) {
			if !chatReadTools[strings.TrimPrefix(a.(string), studioToolPrefix)] {
				t.Errorf("allows %v", a)
			}
		}
		if last, _ := sent[len(sent)-1]["prompt"].(string); strings.Contains(last, relayInstructionsHead) != want.has || !strings.HasPrefix(last, "hello") {
			t.Errorf("refuses %q: prompt %q", refuses, last)
		}
		l := chatOf(m, "s1").PWCode
		if l.HasTools != want.has || (l.ToolsError == "") != want.has || len(sent) != want.creates {
			t.Errorf("refuses %q: %+v, %d creates", refuses, l, len(sent))
		}
	}
}
