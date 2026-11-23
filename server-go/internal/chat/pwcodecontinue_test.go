package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// fakeCodePlatform is the platform's Code listing for the viewer: one
// machine, whose pw code daemon has the id daemonID, and its sessions.
type fakeCodePlatform struct {
	mu       sync.Mutex
	reads    int
	daemonID string
	hostname string
	sessions []map[string]any
	// others are more of the viewer's machines; relay stands in for the
	// platform's relay to each machine's daemon, by machine id, as
	// /api/code/machines/{m}/sessions/{id}... and the events WebSocket.
	others []map[string]any
	relay  map[string]http.Handler
	// relayed records each relayed request as "METHOD path auth"; origins
	// the Origin of each events handshake.
	relayed []string
	origins []string
	// listDown makes the sessions listing fail, as a platform that cannot
	// be read.
	listDown bool
}

func (p *fakeCodePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rest, ok := cutAny(r.URL.Path, "/api/code/machines/", "/ws/code/machines/"); ok {
		machine, path, _ := strings.Cut(rest, "/sessions/")
		p.mu.Lock()
		p.relayed = append(p.relayed, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			p.origins = append(p.origins, r.Header.Get("Origin"))
		}
		h := p.relay[machine]
		p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		if h == nil {
			w.WriteHeader(404)
			return
		}
		r.URL.Path = "/v1/sessions/" + path
		h.ServeHTTP(w, r)
		return
	}
	if r.URL.Path != "/api/code/sessions" {
		w.WriteHeader(404)
		return
	}
	if r.Header.Get("Authorization") != "Bearer k" {
		w.WriteHeader(401)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listDown {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	p.reads++
	w.Header().Set("Content-Type", "application/json")
	machines := []any{map[string]any{"id": "m1", "name": "gpu-box", "kind": "existing", "status": "connected", "daemonId": p.daemonID, "hostname": p.hostname}}
	for _, o := range p.others {
		machines = append(machines, o)
	}
	json.NewEncoder(w).Encode(map[string]any{"machines": machines, "sessions": p.sessions})
}

func cutAny(s string, prefixes ...string) (string, bool) {
	for _, p := range prefixes {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest, true
		}
	}
	return "", false
}

// continueModule is a test Studio whose pw code is f, under a platform
// that lists session s1 on machine m1 as the viewer's.
func continueModule(t *testing.T, f *fakePW, daemonID string, env ...string) (*Module, string, *fakeCodePlatform) {
	m, base, plat, _ := continueModuleAt(t, f, daemonID, env...)
	return m, base, plat
}

// continueModuleAt is continueModule with the platform's address.
func continueModuleAt(t *testing.T, f *fakePW, daemonID string, env ...string) (*Module, string, *fakeCodePlatform, string) {
	plat := &fakeCodePlatform{daemonID: daemonID, hostname: "gpu-box.example", sessions: []map[string]any{
		{"machineId": "m1", "id": "s1", "autoName": "Fix the build", "workspace": "/home/me/repo", "status": "idle", "messages": 2},
	}}
	ps := httptest.NewServer(plat)
	t.Cleanup(ps.Close)
	if f.status == nil {
		f.status = map[string]any{"machineId": "d1", "hostname": "gpu-box.example"}
	}
	e := map[string]string{"PW_GATEWAY_URL": ps.URL + "/api/openai/v1"}
	for i := 0; i+1 < len(env); i += 2 {
		e[env[i]] = env[i+1]
	}
	m, base := pwModule(t, f, e)
	return m, base, plat, ps.URL
}

var earlierTurns = []any{
	map[string]any{"kind": "user", "text": "why does the build fail?"},
	map[string]any{"kind": "thinking", "text": "hmm"},
	map[string]any{"kind": "tool_call", "callId": "t1", "toolName": "Bash", "toolArgs": `{"command":"make"}`},
	map[string]any{"kind": "tool_result", "callId": "t1", "text": "Error: missing header"},
	map[string]any{"kind": "assistant", "text": "A header is missing."},
	map[string]any{"kind": "turn_end"},
}

func TestContinueAPWCodeSessionInAChat(t *testing.T) {
	f := &fakePW{script: helloTurn, detail: map[string]any{"history": earlierTurns, "model": "m1", "workspace": "/home/me/repo"}}
	m, base, _ := continueModule(t, f, "d1")

	if out := get(t, base+"/api/chat/pwcode/here"); out["available"] != true || out["daemonId"] != "d1" || out["hostname"] != "gpu-box.example" {
		t.Errorf("here: %v", out)
	}
	// The first call asks; nothing is made until the person says yes.
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1"}`)
	if code != 200 || !strings.Contains(out["confirm"].(string), "also open in a terminal") || out["conversationId"] != nil || f.attached != 0 {
		t.Fatalf("ask first: %d %v", code, out)
	}
	code, out, _ = send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if code != 200 || id == "" || f.attached != 1 || len(f.created) != 0 {
		t.Fatalf("continue: %d %v (attached %d, made %d)", code, out, f.attached, len(f.created))
	}
	if tools, _ := out["tools"].(map[string]any); tools["connected"] != false || !strings.Contains(tools["note"].(string), "cannot add them") {
		t.Errorf("tools: %v", out["tools"])
	}
	c := m.Conv.Get(id)
	if c == nil || c.PWCode == nil || !c.PWCode.Continued || c.PWCode.Session != "s1" || c.PWCode.Machine != "m1" || c.PWCode.Workspace != "/home/me/repo" || *c.Title != "Fix the build" {
		t.Fatalf("conversation: %+v %+v", c, c.PWCode)
	}
	if len(c.Messages) != 3 || !strings.Contains(*c.Messages[0].Content, "[See the session on the Code page](#view=code?machine=m1&session=s1)") ||
		*c.Messages[1].Content != "why does the build fail?" || *c.Messages[2].Content != "A header is missing." ||
		len(c.Messages[2].Parts) != 1 || c.Messages[2].Parts[0].Status != "error" || *c.Messages[2].ParentID != c.Messages[1].ID ||
		c.PWCode.LastMessage != c.Messages[2].ID || *c.ActiveBranchID != c.Messages[2].ID {
		t.Errorf("history: %+v", c.Messages)
	}
	if st := get(t, base+"/api/chat/pwcode/"+id); st["continued"] != true || st["codePage"] != "#view=code?machine=m1&session=s1" {
		t.Errorf("status: %v", st)
	}

	// The next message goes to that session, with nothing repeated, even
	// though the session lacks the Studio's tools; it is never replaced.
	sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u9", "parentMessageId": c.Messages[2].ID,
		"messages": []any{map[string]any{"role": "user", "content": "add it"}}})
	if !strings.Contains(sse, `"content":"Hello"`) || len(f.messages) != 1 || f.messages[0] != "add it" || len(f.created) != 0 || f.archived != 0 {
		t.Errorf("message: sent %q, made %d, archived %d\n%s", f.messages, len(f.created), f.archived, sse)
	}

	// Asked again, the Code page opens the same chat.
	if _, again, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1"}`); again["conversationId"] != id || again["existing"] != true {
		t.Errorf("again: %v", again)
	}
	if out := get(t, base+"/api/chat/pwcode/here"); !strings.Contains(mustJSON(out["chats"]), `"conversationId":"`+id+`"`) {
		t.Errorf("chats: %v", out["chats"])
	}
}

func TestContinueStaysReadOnlyWhereTheStudioCannotDriveIt(t *testing.T) {
	// The platform's machine is another daemon's.
	f := &fakePW{}
	_, base, plat := continueModule(t, f, "d2")
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
	if code != 404 || !strings.Contains(out["error"].(string), "does not reach this session on gpu-box") || !strings.Contains(out["error"].(string), "Approve or Deny") || f.attached != 0 {
		t.Errorf("other machine, relay not reaching it: %d %v", code, out)
	}
	// A session the platform does not list as the viewer's.
	plat.mu.Lock()
	plat.daemonID, plat.sessions = "d1", nil
	plat.mu.Unlock()
	if code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); code != 404 || !strings.Contains(out["error"].(string), "not list") {
		t.Errorf("not theirs: %d %v", code, out)
	}
	// An archived one.
	plat.mu.Lock()
	plat.sessions = []map[string]any{{"machineId": "m1", "id": "s1", "status": "idle"}}
	plat.mu.Unlock()
	f.detail = map[string]any{"archivedAt": "2026-11-20T00:00:00Z"}
	if code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); code != 409 || !strings.Contains(out["error"].(string), "archived") || !strings.Contains(out["error"].(string), "still read it") {
		t.Errorf("archived: %d %v", code, out)
	}
	// Open on another host: the question says it is taken over.
	f.detail = map[string]any{"status": "live_elsewhere", "liveHost": "laptop.example"}
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1"}`); !strings.Contains(out["confirm"].(string), "open on laptop.example") || !strings.Contains(out["confirm"].(string), "takes it over") {
		t.Errorf("elsewhere: %v", out)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"session":"../x"}`); code != 400 {
		t.Errorf("bad ids: %d", code)
	}
}

func TestAContinuedSessionTakenElsewhereFailsClosed(t *testing.T) {
	f := &fakePW{script: helloTurn, mcp: "fail"}
	m, base, _ := continueModule(t, f, "d1")
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if id == "" || f.archived != 0 || len(f.created) != 0 {
		t.Fatalf("continue: %v", out)
	}
	if tools, _ := out["tools"].(map[string]any); tools["connected"] != false || !strings.Contains(tools["note"].(string), "did not work") {
		t.Errorf("a studio server that fails: %v", out["tools"])
	}
	f.detail = map[string]any{"status": "live_elsewhere", "liveHost": "laptop.example"}
	sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u1", "messages": []any{map[string]any{"role": "user", "content": "go on"}}})
	if !strings.Contains(sse, "now open on laptop.example") || len(f.messages) != 0 || f.archived != 0 || len(f.created) != 0 {
		t.Errorf("taken elsewhere: sent %q, archived %d\n%s", f.messages, f.archived, sse)
	}
	f.detail = map[string]any{"archivedAt": "2026-11-21T00:00:00Z"}
	if sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u2", "messages": []any{map[string]any{"role": "user", "content": "go on"}}}); !strings.Contains(sse, "was archived") || len(f.created) != 0 {
		t.Errorf("archived since: %s", sse)
	}
	// The person's choice of the Studio assistant still holds.
	if e, _ := m.engineFor(httptest.NewRequest("GET", "/", nil), engineStudio, id); e != engineStudio {
		t.Errorf("engine: %s", e)
	}
	if e, _ := m.engineFor(httptest.NewRequest("GET", "/", nil), "", id); e != enginePWCode {
		t.Errorf("a continued chat answers in pw code: %s", e)
	}
}

func TestContinueFailsClosedWithoutTheViewersOwnSignIn(t *testing.T) {
	// Sign-in is on and the request carries no person: nothing is read as
	// the Studio's account, and no chat is made.
	f := &fakePW{}
	m, base, plat := continueModule(t, f, "d1", "AUTH_JWKS_URL", "http://127.0.0.1:9/jwks")
	if out := get(t, base+"/api/chat/pwcode/here"); out["available"] != false || !strings.Contains(out["reason"].(string), "Sign in") {
		t.Errorf("here: %v", out)
	}
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
	if code == 200 || plat.reads != 0 || f.attached != 0 || len(m.Conv.List()) != 0 {
		t.Errorf("signed out: %d %v, platform read %d times, attached %d", code, out, plat.reads, f.attached)
	}
}

func TestSameMachineAndHistory(t *testing.T) {
	h := &pwHere{daemonID: "d1", hostname: "Box.example"}
	for _, c := range []struct {
		m    codesessions.Machine
		want bool
	}{
		{codesessions.Machine{DaemonID: "d1"}, true},
		{codesessions.Machine{DaemonID: "d2", Hostname: "box.example"}, false},
		{codesessions.Machine{Hostname: "box.example"}, true},
		{codesessions.Machine{Hostname: "other.example"}, false},
		{codesessions.Machine{}, false},
	} {
		if got := sameMachine(&c.m, h); got != c.want {
			t.Errorf("%+v: %v", c.m, got)
		}
	}
	if sameMachine(&codesessions.Machine{Hostname: "box.example"}, &pwHere{hostname: "box.example"}) {
		t.Error("a host name alone matched a daemon that gave no id")
	}
	var items []any
	for i := 0; i < continueTurns+5; i++ {
		items = append(items, map[string]any{"kind": "user", "text": "q"}, map[string]any{"kind": "assistant", "text": "a"})
	}
	decoded, _ := jsonv.DecodeString(mustJSON(items))
	msgs, dropped := continuedHistory(decoded.([]any), "m", "now")
	if dropped != 5 || len(msgs) != 2*continueTurns || *msgs[1].Model != "pw code · m" || msgs[0].ParentID != nil || *msgs[1].ParentID != msgs[0].ID {
		t.Errorf("dropped %d, %d messages", dropped, len(msgs))
	}
	if note := continueNote(&codesessions.View{MachineID: "m1", ID: "s1", Title: "a *b*", Machine: "box"}, 5); !strings.Contains(note, "**a b**") || !strings.Contains(note, "5 earlier turns") {
		t.Errorf("note: %s", note)
	}
	if got := shortWorkspace("/home/me/work/repo"); got != "…/work/repo" {
		t.Errorf("workspace: %s", got)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
