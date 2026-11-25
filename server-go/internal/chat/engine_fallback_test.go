package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// signInStudio is a Studio with sign-in, its own ACTIVATE account
// (PW_API_KEY) and its own pw code, where cy has signed in with ACTIVATE
// and bob has not.
func signInStudio(t *testing.T, f *fakePW, env ...string) *Module {
	t.Helper()
	e := map[string]string{"STUDIO_HOME_CLUSTER": "testcluster", "AUTH_JWKS_URL": "https://id.platform.example/jwks"}
	for i := 0; i+1 < len(env); i += 2 {
		e[env[i]] = env[i+1]
	}
	m, _ := pwModule(t, f, e)
	m.A.Cfg.FrontAuth = true
	m.Convs.AuthEnabled = func() bool { return true }
	m.pf.SignInKey = func(owner string) string {
		if owner == "cy" {
			return "cy-own-key"
		}
		return ""
	}
	// bob's model credential is an OpenAI-compatible endpoint of his own,
	// which the Studio assistant answers with; it is no ACTIVATE sign-in.
	m.accessMu.Lock()
	gw := m.access.GatewayBase
	m.accessMu.Unlock()
	if err := m.Keys.Set("bob", "sk-bobs-endpoint", &gw, nil); err != nil {
		t.Fatal(err)
	}
	m.forgetPWCodeAvailability("")
	return m
}

func as(id, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return auth.WithUser(r, &auth.User{ID: id, Username: id})
}

// sendAs posts one message to the chat stream as id, and returns the
// stream's body.
func sendAs(t *testing.T, m *Module, id, convID, msgID, parent, extra string) string {
	t.Helper()
	p := ""
	if parent != "" {
		p = `"parentMessageId":"` + parent + `",`
	}
	body := `{"conversationId":"` + convID + `","userMessageId":"` + msgID + `",` + p + extra + `"model":"m","messages":[{"role":"user","content":"hello"}]}`
	rec := httptest.NewRecorder()
	if err := m.stream(rec, as(id, "POST", "/api/chat/stream", body)); err != nil {
		t.Fatalf("%s's message: %v", id, err)
	}
	return rec.Body.String()
}

// notesOf is the fallback notes stored in a conversation.
func notesOf(c *conversations.Conversation) []conversations.Message {
	var out []conversations.Message
	for _, mm := range c.Messages {
		if strings.HasPrefix(mm.ID, "note-engine-") {
			out = append(out, mm)
		}
	}
	return out
}

// pwChat is a chat of owner's that pw code answered.
func pwChat(m *Module, owner string) *conversations.Conversation {
	ask, said := "a question", "pw code's answer"
	return m.Conv.CreateWith(nil, owner, []conversations.Message{
		{ID: "u1", Role: "user", Content: &ask},
		{ID: "pwc-1", Role: "assistant", Content: &said, ParentID: sp("u1")},
	}, nil)
}

// With sign-in on, a person with no ACTIVATE sign-in of their own can
// neither choose pw code nor reach it, though the Studio has its own
// account and its own pw code answers: the Studio's pw code and key are
// never used for them. A person signed in with ACTIVATE is offered it.
func TestPWCodeIsNeverTheStudiosOwnForAPersonWithoutActivate(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m := signInStudio(t, f)

	// The menu: pw code is not offered to bob, and a new chat of his
	// starts on the Studio assistant.
	v := m.engineState(as("bob", "GET", "/api/chat/engine", ""), nil, enginePWCode)
	if !strings.Contains(v.why, "runs as you") || v.newChat != engineStudio || v.current != engineStudio {
		t.Errorf("bob is offered pw code: %+v", v)
	}
	// A message that picks pw code is refused.
	c := m.Conv.CreateWith(nil, "bob", nil, nil)
	if out := sendAs(t, m, "bob", c.ID, "u1", "", `"engine":"pwcode",`); !strings.Contains(out, "runs as you") || !strings.Contains(out, "event: error") {
		t.Errorf("bob's pick of pw code was not refused: %.300s", out)
	}
	// Nor the target, the daemon or the session view's routes.
	var ae *apierr.Error
	if _, err := m.pwcodeTarget(context.Background(), as("bob", "POST", "/", ""), nil); !errors.As(err, &ae) || ae.Status != 409 {
		t.Errorf("target: %v", err)
	}
	if _, _, err := m.pwcodeDaemon(as("bob", "GET", "/", "")); !errors.As(err, &ae) || ae.Status != 409 {
		t.Errorf("daemon: %v", err)
	}
	// Signed out entirely.
	anon := httptest.NewRequest("GET", "/", nil)
	if why := m.pwcodeUnavailable(anon.Context(), anon); !strings.Contains(why, "Sign in") {
		t.Errorf("anonymous: %q", why)
	}
	if f.calls != 0 || len(f.created) != 0 || len(f.messages) != 0 {
		t.Errorf("the Studio's own pw code was reached: %d calls, %d sessions, %q", f.calls, len(f.created), f.messages)
	}

	// cy, signed in with ACTIVATE, is offered pw code, on the Studio's
	// cluster.
	if v := m.engineState(as("cy", "GET", "/api/chat/engine", ""), nil, ""); v.why != "" || v.newChat != enginePWCode {
		t.Errorf("cy is not offered pw code: %+v", v)
	}
	if _, site, err := m.pwcodeDaemon(as("cy", "GET", "/", "")); err != nil || site != "testcluster" {
		t.Errorf("cy daemon: %s %v", site, err)
	}
}

// A chat pw code answered, or a new one whose last choice was pw code, is
// answered by the Studio assistant for a person pw code is not for here,
// with one note after the reply that says why; the chat then stays with
// the Studio assistant until they switch it back.
func TestAPersonPWCodeIsNotForGetsTheStudioAssistant(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m := signInStudio(t, f)

	c := pwChat(m, "bob")
	out := sendAs(t, m, "bob", c.ID, "u2", "pwc-1", "")
	if strings.Contains(out, "event: error") || !strings.Contains(out, "event: engine_fallback") || !strings.Contains(out, "event: done") {
		t.Fatalf("bob's pw code chat was not answered by the Studio assistant: %.600s", out)
	}
	got := m.Conv.Get(c.ID)
	notes := notesOf(got)
	if len(notes) != 1 {
		t.Fatalf("notes: %d", len(notes))
	}
	note, last := notes[0], got.Messages[len(got.Messages)-1]
	if last.ID != note.ID || note.ParentID == nil || strings.HasPrefix(*note.ParentID, "pwc-") || *note.ParentID == "u2" {
		t.Errorf("the note is not right after the Studio assistant's reply: %+v", got.Messages)
	}
	text := note.Text()
	if want := "pw code is not available to you here, so the Studio answered. Sign in with ACTIVATE under [Settings, Accounts](#view=settings:accounts?open=activate&note=engine) to switch back."; text != want {
		t.Errorf("note:\n got %q\nwant %q", text, want)
	}
	if got.Engine != engineStudio || got.AnsweredBy() != engineStudio {
		t.Errorf("the chat is kept as %q (answered by %q)", got.Engine, got.AnsweredBy())
	}
	// The menu says the same: the Studio assistant answers, pw code is
	// greyed with its reason.
	if v := m.engineState(as("bob", "GET", "/", ""), got, ""); v.current != engineStudio || v.why == "" {
		t.Errorf("menu: %+v", v)
	}
	// The next message is the Studio assistant's, with no second note.
	last = got.Messages[len(got.Messages)-1]
	if out := sendAs(t, m, "bob", c.ID, "u3", last.ID, ""); strings.Contains(out, "engine_fallback") || strings.Contains(out, "event: error") {
		t.Errorf("second message: %.300s", out)
	}
	if n := len(notesOf(m.Conv.Get(c.ID))); n != 1 {
		t.Errorf("notes after a second message: %d", n)
	}

	// A new chat whose last choice was pw code: the same.
	fresh := m.Conv.CreateWith(nil, "bob", nil, nil)
	if out := sendAs(t, m, "bob", fresh.ID, "u1", "", `"newChatEngine":"pwcode",`); !strings.Contains(out, "engine_fallback") || strings.Contains(out, "event: error") {
		t.Errorf("new chat, pw code chosen last: %.300s", out)
	}
	if n := len(notesOf(m.Conv.Get(fresh.ID))); n != 1 {
		t.Errorf("new chat notes: %d", n)
	}

	// A message that picks pw code still gets the error, and no note.
	c2 := pwChat(m, "bob")
	if out := sendAs(t, m, "bob", c2.ID, "u2", "pwc-1", `"engine":"pwcode",`); !strings.Contains(out, "event: error") || strings.Contains(out, "engine_fallback") {
		t.Errorf("an explicit pick of pw code: %.300s", out)
	}
	if n := len(notesOf(m.Conv.Get(c2.ID))); n != 0 {
		t.Errorf("explicit pick notes: %d", n)
	}
	// So does a switch to pw code.
	r := as("bob", "PUT", "/", `{"engine":"pwcode"}`)
	if why, _ := m.pwcodeWhyFor(r, m.Conv.Get(c2.ID)); why == "" {
		t.Errorf("bob may switch to pw code")
	}
	if f.calls != 0 || len(f.created) != 0 {
		t.Errorf("pw code was reached for bob: %d calls, %d sessions", f.calls, len(f.created))
	}

	// cy, signed in with ACTIVATE: her pw code chat goes to pw code as
	// before, as her (this test reaches no cluster for the session).
	cc := pwChat(m, "cy")
	if e, note, err := m.engineFor(as("cy", "POST", "/", ""), "", "", cc.ID); e != enginePWCode || note != "" || err != nil {
		t.Errorf("cy's chat: %q %q %v", e, note, err)
	}
	if out := sendAs(t, m, "cy", cc.ID, "u2", "pwc-1", ""); strings.Contains(out, "engine_fallback") || strings.Contains(out, "runs as you") {
		t.Errorf("cy's chat: %.300s", out)
	}
	if len(notesOf(m.Conv.Get(cc.ID))) != 0 || m.Conv.Get(cc.ID).Engine != enginePWCode {
		t.Errorf("cy's chat is not pw code's: %q", m.Conv.Get(cc.ID).Engine)
	}
}

// Where the deployment sets pw code to answer new chats, a new chat of a
// person pw code is not for starts on the Studio assistant, with the note.
func TestTheSettingOfPWCodeFallsBackForAPersonItIsNotFor(t *testing.T) {
	m := signInStudio(t, &fakePW{script: helloTurn}, "CHAT_ENGINE", "pwcode")
	if v := m.engineState(as("bob", "GET", "/", ""), nil, ""); v.newChat != engineStudio || v.current != engineStudio {
		t.Errorf("bob's menu: %+v", v)
	}
	if v := m.engineState(as("cy", "GET", "/", ""), nil, ""); v.newChat != enginePWCode {
		t.Errorf("cy's menu: %+v", v)
	}
	fresh := m.Conv.CreateWith(nil, "bob", nil, nil)
	if out := sendAs(t, m, "bob", fresh.ID, "u1", "", ""); !strings.Contains(out, "engine_fallback") || strings.Contains(out, "event: error") {
		t.Errorf("new chat, pw code the setting: %.300s", out)
	}
}

// A relayed chat's note says the session on its machine runs on without
// this reply.
func TestARelayedChatsNoteSaysTheSessionRunsOn(t *testing.T) {
	m := signInStudio(t, &fakePW{script: helloTurn})
	c := pwChat(m, "bob")
	m.Conv.SetPWCode(c.ID, &conversations.PWCodeLink{Session: "s9", Relay: true, Machine: "m2", MachineName: "lab-box"})
	out := sendAs(t, m, "bob", c.ID, "u2", "pwc-1", "")
	if !strings.Contains(out, "engine_fallback") {
		t.Fatalf("relayed chat: %.300s", out)
	}
	notes := notesOf(m.Conv.Get(c.ID))
	if len(notes) != 1 || !strings.Contains(notes[0].Text(), "The pw code session on lab-box keeps running there, and does not see this reply.") {
		t.Errorf("relayed note: %+v", notes)
	}
}

// On a Studio of one with no ACTIVATE, a pw code chat is answered by the
// Studio assistant with the note too; where pw code is only down, a
// message in a pw code chat gets the error, as before.
func TestASingleUserStudioWithoutActivateFallsBackButNotWhenPWCodeIsDown(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, _ := pwModule(t, f, nil)
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return false }
	m.forgetPWCodeAvailability("")
	c := pwChat(m, "")
	if out := sendAs(t, m, "", c.ID, "u2", "pwc-1", ""); !strings.Contains(out, "engine_fallback") || strings.Contains(out, "event: error") {
		t.Fatalf("no ACTIVATE: %.300s", out)
	}
	if notes := notesOf(m.Conv.Get(c.ID)); len(notes) != 1 || !strings.Contains(notes[0].Text(), "Sign in with ACTIVATE under [Settings, Accounts]") {
		t.Errorf("no ACTIVATE note: %+v", notes)
	}

	// pw code did not start: not a fallback.
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return true }
	notOffered(m, "pw code did not start here: no daemon")
	c2 := pwChat(m, "")
	if out := sendAs(t, m, "", c2.ID, "u2", "pwc-1", ""); !strings.Contains(out, "event: error") || strings.Contains(out, "engine_fallback") {
		t.Errorf("pw code down: %.300s", out)
	}
	if n := len(notesOf(m.Conv.Get(c2.ID))); n != 0 {
		t.Errorf("pw code down notes: %d", n)
	}
}

// pw code is not told of a fallback note: it is about pw code, not for it.
func TestPWCodeIsNotToldTheFallbackNote(t *testing.T) {
	ask, said, note := "a question", "Studio's answer", "pw code is not available to you here, so the Studio answered."
	c := &conversations.Conversation{Messages: []conversations.Message{
		{ID: "u1", Role: "user", Content: &ask},
		{ID: "a1", Role: "assistant", Content: &said},
		{ID: "note-engine-x-y", Role: "assistant", Content: &note},
	}}
	if got := pwcodeContext(c, true, ""); strings.Contains(got, "not available to you") || !strings.Contains(got, said) {
		t.Errorf("context: %q", got)
	}
}
