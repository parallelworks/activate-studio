package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// With no model in Settings, every new pw code chat asked for one again.
func TestANewPWCodeChatStartsOnTheModelThePersonChoseLast(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	start := func(chose map[string]any) map[string]any {
		t.Helper()
		c := m.Conv.Create(nil, "")
		startMessage(t, base, c.ID, "u1", chose)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.created[len(f.created)-1]
	}
	if made := start(nil); made["model"] != nil {
		t.Fatalf("nothing chosen: %v", made["model"])
	}
	if made := start(map[string]any{"pwcodeModel": "org:openai/gpt-5", "pwcodeAllocation": "a1"}); made["model"] != "org:openai/gpt-5" {
		t.Fatalf("picked: %v", made["model"])
	}
	// Another chat, as another browser starts it, after a restart.
	m.pw.mu.Lock()
	m.pw.last = nil
	m.pw.mu.Unlock()
	if made := start(nil); made["model"] != "org:openai/gpt-5" || made["allocation"] != "a1" {
		t.Errorf("the next chat: %v %v", made["model"], made["allocation"])
	}
	if out := get(t, base+"/api/chat/engine"); out["model"] != "" || out["lastModel"] != "org:openai/gpt-5" || out["lastAllocation"] != "a1" {
		t.Errorf("engine: %v", out)
	}
	if out := get(t, base+"/api/chat/pwcode/model"); out["needed"] != false {
		t.Errorf("needed: %v", out)
	}
	// Another pick is that chat's, and the next ones'.
	start(map[string]any{"pwcodeModel": "inference-server/lab/m1"})
	if made := start(nil); made["model"] != "inference-server/lab/m1" || made["allocation"] != nil {
		t.Errorf("after another: %v %v", made["model"], made["allocation"])
	}
}

func TestTheModelSettingsNamesWinsOverTheLastOne(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, map[string]string{"PWCODE_MODEL": "claude-code/haiku"})
	m.choseModel(httptest.NewRequest("GET", "/", nil), "me:openai/gpt-5", nil, false)
	c := m.Conv.Create(nil, "")
	startMessage(t, base, c.ID, "u1", nil)
	if f.created[0]["model"] != "claude-code/haiku" {
		t.Errorf("made on %v", f.created[0]["model"])
	}
	if out := get(t, base+"/api/chat/engine"); out["model"] != "claude-code/haiku" || out["lastModel"] != "" {
		t.Errorf("engine: %v", out)
	}
}

func TestAModelChosenInASessionIsTheLastOne(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	s := base + "/api/code/machines/m2/sessions/s1/settings"
	if code, out, _ := send(t, http.MethodPatch, s, `{"effort":"low"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	r := httptest.NewRequest("GET", "/", nil)
	if l := m.startModel(r); l.Model != "" {
		t.Errorf("from an effort: %+v", l)
	}
	if code, out, _ := send(t, http.MethodPatch, s, `{"model":"org:openai/gpt-5","allocation":"a1"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if l := m.startModel(r); l.Model != "org:openai/gpt-5" || l.Allocation != "a1" {
		t.Errorf("from a session: %+v", l)
	}
	// A chat on another machine starts with it too, and Claude's only where
	// that machine runs Claude Code.
	canStart(plat, nil)
	elsewhere := func() map[string]any {
		t.Helper()
		c := m.Conv.Create(nil, "")
		startMessage(t, base, c.ID, "u1", map[string]any{"pwcodeMachine": "m2", "pwcodeFolder": "/home/me/solver"})
		return remote.created[len(remote.created)-1]
	}
	if made := elsewhere(); made["model"] != "org:openai/gpt-5" || made["allocation"] != "a1" {
		t.Errorf("on another machine: %v", made)
	}
	// A model that machine has of its own says nothing of the others', and
	// one of the Studio's machine is not asked of it.
	if code, out, _ := send(t, http.MethodPatch, s, `{"model":"inference-server/lab/m1","allocation":""}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if l := m.startModel(r); l.Model != "org:openai/gpt-5" {
		t.Errorf("from another machine's own: %+v", l)
	}
	m.choseModel(r, "inference-server/here/m1", nil, false)
	if made := elsewhere(); made["model"] != nil {
		t.Errorf("the Studio's machine's own: %v", made)
	}
	m.choseModel(r, "claude-code/sonnet", nil, false)
	if made := elsewhere(); made["model"] != nil {
		t.Errorf("Claude where it does not run: %v", made)
	}
	canStart(plat, map[string]any{"claudeInstalled": true, "claudeCodeAllowed": true})
	if made := elsewhere(); made["model"] != "claude-code/sonnet" {
		t.Errorf("Claude: %v", made)
	}
}

// A model that is gone must not keep a person from starting chats.
func TestALastModelPWCodeRefusesIsForgotten(t *testing.T) {
	f := &fakePW{script: helloTurn}
	var asked []any
	every := ""
	f.routes = map[string]http.HandlerFunc{"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		asked = append(asked, b["model"])
		switch {
		case every != "":
			refuse(w, "workspace_invalid", every)
		case b["model"] != nil:
			refuse(w, "model_invalid", "unknown model")
		default:
			refuse(w, "model_invalid", tuiWords)
		}
	}}
	m, base := pwModule(t, f, nil)
	r := httptest.NewRequest("GET", "/", nil)
	m.choseModel(r, "me:openai/gone", nil, false)
	// Refused for something else: kept.
	every = "no such folder"
	out := post(t, base, first(m.Conv.Create(nil, "").ID, "Hi", true))
	if !strings.Contains(out, "no such folder") || m.startModel(r).Model != "me:openai/gone" {
		t.Errorf("another refusal: %+v %s", m.startModel(r), out)
	}
	every, asked = "", nil
	out = post(t, base, first(m.Conv.Create(nil, "").ID, "Hi", true))
	if len(asked) != 2 || asked[0] != "me:openai/gone" || asked[1] != nil || !strings.Contains(out, pwcode.NoModel) {
		t.Errorf("asked %v: %s", asked, out)
	}
	if l := m.startModel(r); l.Model != "" {
		t.Errorf("kept: %+v", l)
	}
}

func TestTheLastModelIsEachPersonsOwn(t *testing.T) {
	m, _, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks"})
	as := func(id string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if id == "" {
			return r
		}
		return r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{ID: id, Username: id}))
	}
	m.choseModel(as("ada"), "me:openai/a", nil, false)
	m.choseModel(as("bob"), "me:openai/b", nil, false)
	m.choseModel(as(""), "me:openai/nobody", nil, false)
	if a, b, n, c := m.startModel(as("ada")), m.startModel(as("bob")), m.startModel(as("")), m.startModel(as("carol")); a.Model != "me:openai/a" || b.Model != "me:openai/b" || n.Model != "" || c.Model != "" {
		t.Errorf("ada %+v, bob %+v, nobody %+v, carol %+v", a, b, n, c)
	}
}
