package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

func first(id, text string, opens bool, more ...any) map[string]any {
	b := map[string]any{"conversationId": id, "userMessageId": "u1", "opensSession": opens,
		"messages": []any{map[string]any{"role": "user", "content": text}}}
	for i := 0; i+1 < len(more); i += 2 {
		b[more[i].(string)] = more[i+1]
	}
	return b
}

func refuse(w http.ResponseWriter, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(map[string]any{"type": "/problems/pwcode/" + code, "status": 422, "detail": detail, "code": code})
}

// A new chat's session was sent a command as a message, which the daemon
// refuses: the chat had a session by then, whose view showed neither the
// line nor the refusal.
func TestACommandAsTheFirstMessageIsLeftToThePage(t *testing.T) {
	for _, text := range []string{"/plan say the tide", "/compact", "/help me", "/nosuch", "  /goal\nfind the tide"} {
		f := &fakePW{script: helloTurn}
		m, base := pwModule(t, f, nil)
		c := m.Conv.Create(nil, "")
		out := post(t, base, first(c.ID, text, true, "labelScope", []string{"tides"}))
		want, _ := json.Marshal(text)
		if !strings.Contains(out, "event: pwcode\n") || !strings.Contains(out, "event: pwcode_first\ndata: {\"text\":"+string(want)+",\"refused\":\"\"}") || !strings.Contains(out, "event: done") || strings.Contains(out, "event: error") {
			t.Fatalf("%q: %s", text, out)
		}
		if len(f.messages) != 0 {
			t.Errorf("%q was sent as a message: %q", text, f.messages)
		}
		now := m.Conv.Get(c.ID)
		if now.PWCode == nil || now.PWCode.Session != "s1" || len(now.Messages) != 0 || now.ActiveBranchID != nil {
			t.Errorf("%q: the chat has %+v, %+v, branch %v", text, now.PWCode, now.Messages, now.ActiveBranchID)
		}
		// The session has not been told of the chat's scope, which its tool
		// calls run in all the same.
		if l := now.PWCode; len(l.LabelScope) != 1 || l.ScopeNote != "" {
			t.Errorf("%q: scope %v, told %q", text, l.LabelScope, l.ScopeNote)
		}
	}
}

// Text that only starts with a slash is a message, and so is a command from
// a page that does not run it.
func TestOtherFirstMessagesAreSent(t *testing.T) {
	for _, c := range []struct {
		text  string
		opens bool
	}{{"/etc/hosts what is it?", true}, {"!ls", true}, {"Say /plan", true}, {"/plan say the tide", false}} {
		f := &fakePW{script: helloTurn}
		m, base := pwModule(t, f, nil)
		conv := m.Conv.Create(nil, "")
		out := post(t, base, first(conv.ID, c.text, c.opens))
		if len(f.messages) != 1 || f.messages[0] != c.text || strings.Contains(out, "pwcode_first") {
			t.Errorf("%q: sent %q: %s", c.text, f.messages, out)
		}
	}
}

// A command after turns the session has not seen runs in it all the same,
// and the turns go with the first message sent there.
func TestACommandAfterOtherTurnsLeavesThemOwed(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	m.Conv.RecordUserTurn(c.ID, "u0", "What is the budget?", nil, "")
	m.Conv.RecordAssistantTurn(c.ID, conversations.Message{ID: "a0", Role: "assistant", Content: sp("Nine."), ParentID: sp("u0")})
	out := post(t, base, first(c.ID, "/plan spend it", true, "parentMessageId", "a0", "engine", "pwcode"))
	now := m.Conv.Get(c.ID)
	if len(f.messages) != 0 || !strings.Contains(out, "pwcode_first") || len(now.Messages) != 2 || now.PWCode.Owed == nil || *now.PWCode.Owed != (conversations.PWCodeOwed{Upto: "a0"}) {
		t.Fatalf("sent %q, kept %+v: %s", f.messages, now.PWCode, out)
	}
	for _, text := range []string{"/diagnose-run 7", "spend it", "and more"} {
		b, _ := json.Marshal(map[string]any{"text": text})
		res, err := http.Post(base+"/api/code/machines/studio/sessions/s1/messages", "application/json", strings.NewReader(string(b)))
		if err != nil || res.StatusCode != 200 {
			t.Fatal(res, err)
		}
		res.Body.Close()
	}
	want := "Earlier in this conversation, before you joined it:\n\nThe person: What is the budget?\n\nThe Studio assistant: Nine.\n\nThe person's new message:\n\nspend it"
	if len(f.messages) != 3 || f.messages[0] != "/diagnose-run 7" || f.messages[1] != want || f.messages[2] != "and more" || m.Conv.Get(c.ID).PWCode.Owed != nil {
		t.Errorf("sent %q", f.messages)
	}
	// A page that does not run commands has it sent with them, as before.
	d := m.Conv.Create(nil, "")
	m.Conv.RecordUserTurn(d.ID, "u0", "What is the budget?", nil, "")
	if out = post(t, base, first(d.ID, "/plan spend it", false, "parentMessageId", "u0")); !strings.Contains(f.messages[len(f.messages)-1], "What is the budget?") || strings.Contains(out, "pwcode_first") {
		t.Errorf("sent %q: %s", f.messages, out)
	}
}

// A session that answered here before is owed the turns since, whatever
// it does with the command, and has them where the Studio sends the next.
func TestTurnsSinceTheSessionsReplyAreOwed(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	post(t, base, first(c.ID, "hello", true))
	l := m.Conv.Get(c.ID).PWCode
	if l.LastMessage == "" {
		t.Fatal("no reply kept")
	}
	m.Conv.RecordUserTurn(c.ID, "u2", "What is the budget?", sp(l.LastMessage), "")
	m.Conv.RecordAssistantTurn(c.ID, conversations.Message{ID: "a2", Role: "assistant", Content: sp("Nine."), ParentID: sp("u2")})
	b := first(c.ID, "/goal spend it", true, "parentMessageId", "a2", "userMessageId", "u3")
	post(t, base, b)
	if o := m.Conv.Get(c.ID).PWCode.Owed; o == nil || *o != (conversations.PWCodeOwed{After: l.LastMessage, Upto: "a2"}) {
		t.Fatalf("owed %+v", o)
	}
	// The goal's own reply is not a turn it missed.
	m.Conv.RecordAssistantTurn(c.ID, conversations.Message{ID: "pwc-9", Role: "assistant", Content: sp("On it."), ParentID: sp("a2")})
	if got := owedContext(m.Conv.Get(c.ID), "more"); got != "In this conversation since your last reply:\n\nThe person: What is the budget?\n\nThe Studio assistant: Nine.\n\nThe person's new message:\n\n" {
		t.Errorf("owed %q", got)
	}
	post(t, base, first(c.ID, "more", true, "parentMessageId", "pwc-9", "userMessageId", "u4"))
	if last := f.messages[len(f.messages)-1]; !strings.Contains(last, "What is the budget?") || m.Conv.Get(c.ID).PWCode.Owed != nil {
		t.Errorf("sent %q, owed %+v", last, m.Conv.Get(c.ID).PWCode.Owed)
	}
}

// A first message its session refuses goes back to the page with why.
func TestARefusedFirstMessageGoesBackToThePage(t *testing.T) {
	const why = "the session is being moved"
	run := func(opens bool) (string, int, string) {
		f := &fakePW{script: helloTurn}
		f.routes = map[string]http.HandlerFunc{"POST /v1/sessions/s1/messages": func(w http.ResponseWriter, r *http.Request) { refuse(w, "session_moving", why) }}
		m, base := pwModule(t, f, nil)
		c := m.Conv.Create(nil, "")
		out := post(t, base, first(c.ID, "What is the budget?", opens))
		return out, len(m.Conv.Get(c.ID).Messages), c.ID
	}
	out, kept, _ := run(true)
	if !strings.Contains(out, `event: pwcode_first`+"\n"+`data: {"text":"What is the budget?","refused":"`+why+`"}`) || strings.Contains(out, "event: error") || kept != 0 {
		t.Errorf("%d turns kept: %s", kept, out)
	}
	if out, kept, _ = run(false); !strings.Contains(out, "event: error") || kept != 1 {
		t.Errorf("an older page: %d turns kept: %s", kept, out)
	}
}

// The scope a command's session has not heard goes with the first message
// sent to it from its view, once; a skill's line is sent as it is.
func TestTheScopeReachesASessionACommandStarted(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	post(t, base, first(c.ID, "/plan say the tide", true, "labelScope", []string{"tides"}))
	for _, text := range []string{"/diagnose-run 7", "say the tide", "and the wind"} {
		b, _ := json.Marshal(map[string]any{"text": text})
		res, err := http.Post(base+"/api/code/machines/studio/sessions/s1/messages", "application/json", strings.NewReader(string(b)))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode >= 300 {
			t.Fatalf("%q: %d", text, res.StatusCode)
		}
	}
	f.mu.Lock()
	sent := f.messages
	f.mu.Unlock()
	note := relayInstructionsHead + "The person has scoped this conversation to material labeled: tides. search_kb is restricted to those labels."
	if len(sent) != 3 || sent[0] != "/diagnose-run 7" || sent[1] != "say the tide"+note || sent[2] != "and the wind" {
		t.Errorf("sent %q", sent)
	}
}

// A chat made with a command is called after its session, not the command,
// and never names the session after it.
func TestACommandIsNotAChatsName(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	title := "/help me"
	m.Conv.Rename(c.ID, &title)
	post(t, base, first(c.ID, title, true))
	if !m.Conv.SessionNamed(c.ID, "s1", "", "Tide tables") {
		t.Error("the chat kept the command as its name")
	}
	if got := deref(m.Conv.Get(c.ID).Title); got != "Tide tables" {
		t.Errorf("the chat is called %q", got)
	}
}

const tuiWords = "model is required: pass one or set a default with the interactive TUI"

// Without a model anywhere, the box was open and the daemon's words sent
// the person to its terminal.
func TestANewChatNeedsAModel(t *testing.T) {
	for _, c := range []struct {
		name    string
		env     map[string]string
		answers func(w http.ResponseWriter)
		needed  bool
		asked   int
	}{
		{"no default", nil, func(w http.ResponseWriter) { refuse(w, "model_invalid", tuiWords) }, true, 1},
		{"a default", nil, func(w http.ResponseWriter) { refuse(w, "effort_invalid", "unknown effort") }, false, 1},
		{"a model in Settings", map[string]string{"PWCODE_MODEL": "bob:stub/m1"}, func(w http.ResponseWriter) { refuse(w, "model_invalid", tuiWords) }, false, 0},
		{"a daemon that makes a session", nil, func(w http.ResponseWriter) { fmt.Fprint(w, `{"id":"s9"}`) }, false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakePW{}
			asked, deleted := 0, 0
			f.routes = map[string]http.HandlerFunc{
				"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
					var req map[string]any
					json.NewDecoder(r.Body).Decode(&req)
					if req["model"] != nil || req["effort"] != probeEffort {
						t.Errorf("asked %v", req)
					}
					asked++
					c.answers(w)
				},
				"DELETE /v1/sessions/s9": func(w http.ResponseWriter, r *http.Request) { deleted++; w.WriteHeader(204) },
			}
			_, base := pwModule(t, f, c.env)
			for range 2 {
				res, err := http.Get(base + "/api/chat/pwcode/model")
				if err != nil {
					t.Fatal(err)
				}
				var got struct{ Needed bool }
				json.NewDecoder(res.Body).Decode(&got)
				res.Body.Close()
				if got.Needed != c.needed {
					t.Errorf("needed %v", got.Needed)
				}
			}
			if asked != c.asked || (c.name == "a daemon that makes a session") != (deleted == 1) {
				t.Errorf("asked %d, deleted %d", asked, deleted)
			}
		})
	}
}

// Where the daemon's refusal still arrives, it is in the Studio's words.
func TestNoModelIsSaidInTheStudiosWords(t *testing.T) {
	f := &fakePW{script: helloTurn}
	f.routes = map[string]http.HandlerFunc{"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) { refuse(w, "model_invalid", tuiWords) }}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	out := post(t, base, first(c.ID, "What is the budget?", true))
	if !strings.Contains(out, "event: error") || !strings.Contains(out, pwcode.NoModel) || strings.Contains(out, "TUI") {
		t.Error(out)
	}
	// The line stays in the thread, which keeps what was typed.
	if n := len(m.Conv.Get(c.ID).Messages); n != 1 {
		t.Errorf("%d turns", n)
	}
}
