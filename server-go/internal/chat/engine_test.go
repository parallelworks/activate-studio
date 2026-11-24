package chat

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// notOffered keeps "pw code is not offered here" for every viewer, as a
// minute-old read of this Studio's own pw code would.
func notOffered(m *Module, why string) {
	m.pw.mu.Lock()
	m.pw.avail = map[string]pwAvail{"": {why: why, at: time.Now(), ttl: time.Hour}}
	m.pw.mu.Unlock()
}

// The chat bar's menu judges the conversation it is in by the rule its
// next message is sent by: a relayed chat needs the relay, not this
// Studio's own pw code.
func TestTheMenuJudgesARelayedChatAsItsMessagesAre(t *testing.T) {
	local := &fakePW{script: helloTurn}
	remote := &fakePW{script: helloTurn, detail: map[string]any{"history": earlierTurns, "workspace": "/home/me/solver"}}
	m, base, _, _ := relayModule(t, local, remote)
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if id == "" {
		t.Fatalf("continue: %v", out)
	}
	notOffered(m, "pw code is not set up on this Studio.")
	menu := get(t, base+"/api/chat/engine?conversation="+id)
	if menu["pwcode"] != true || menu["current"] != "pwcode" || menu["reason"] != nil {
		t.Errorf("menu: %v", menu)
	}
	sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "go on"}}})
	if len(remote.messages) != 1 || strings.Contains(sse, "not set up") {
		t.Errorf("send: remote %q\n%s", remote.messages, sse)
	}
	// A new chat is judged by this Studio's own pw code.
	if menu := get(t, base+"/api/chat/engine"); menu["pwcode"] != false || menu["current"] != "studio" {
		t.Errorf("new chat: %v", menu)
	}
}

// An ACTIVATE sign-in changes whether pw code is offered: the next read
// asks again rather than keep the minute-old answer.
func TestASignInIsSeenByTheNextRead(t *testing.T) {
	m, base := pwModule(t, &fakePW{script: helloTurn}, nil)
	notOffered(m, "pw code needs ACTIVATE: sign the pw CLI in.")
	if menu := get(t, base+"/api/chat/engine"); menu["pwcode"] != false {
		t.Fatalf("before: %v", menu)
	}
	if err := m.pf.AccessChanged(false); err != nil {
		t.Fatal(err)
	}
	if menu := get(t, base+"/api/chat/engine"); menu["pwcode"] != true || menu["default"] != "pwcode" {
		t.Errorf("after the Studio's sign-in: %v", menu)
	}
	// With sign-in on, a person's own sign-in clears only theirs.
	m.pw.mu.Lock()
	m.pw.avail = map[string]pwAvail{"u1": {why: "no", at: time.Now(), ttl: time.Hour}, "u2": {why: "no", at: time.Now(), ttl: time.Hour}}
	m.pw.mu.Unlock()
	m.A.Providers.ForgetCredential("u1")
	m.pw.mu.Lock()
	_, u1 := m.pw.avail["u1"]
	_, u2 := m.pw.avail["u2"]
	m.pw.mu.Unlock()
	if u1 || !u2 {
		t.Errorf("kept: u1 %v, u2 %v", u1, u2)
	}
}

// A chat answers with the engine that answered it last, from any browser,
// until the person switches it; the default and the last choice are for
// new chats.
func TestAChatKeepsTheEngineThatAnsweredIt(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	studio, pw := "Studio's answer", "pw code's answer"
	ask := "a question"
	// An old Studio assistant chat, from before the engine was kept.
	old := m.Conv.CreateWith(nil, "", []conversations.Message{
		{ID: "u1", Role: "user", Content: &ask},
		{ID: "a1", Role: "assistant", Content: &studio, ParentID: sp("u1")},
	}, nil)
	if menu := get(t, base+"/api/chat/engine?conversation="+old.ID); menu["current"] != "studio" || menu["default"] != "pwcode" || menu["answered"] != true {
		t.Errorf("old Studio chat: %v", menu)
	}
	if c := get(t, base+"/api/chat/conversations/"+old.ID); c["engine"] != "studio" {
		t.Errorf("old Studio chat's record: %v", c["engine"])
	}
	post(t, base, map[string]any{"conversationId": old.ID, "userMessageId": "u2", "parentMessageId": "a1", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "more"}}})
	if len(f.created) != 0 {
		t.Errorf("pw code answered an old Studio assistant chat")
	}
	// An old pw code chat, where the Studio assistant is the default.
	f2 := &fakePW{script: helloTurn}
	m, base = pwModule(t, f2, map[string]string{"CHAT_ENGINE": "studio"})
	f = f2
	pwChat := m.Conv.CreateWith(nil, "", []conversations.Message{
		{ID: "u1", Role: "user", Content: &ask},
		{ID: "pwc-1", Role: "assistant", Content: &pw, ParentID: sp("u1")},
	}, nil)
	if menu := get(t, base+"/api/chat/engine?conversation="+pwChat.ID); menu["current"] != "pwcode" || menu["default"] != "studio" {
		t.Errorf("old pw code chat: %v", menu)
	}
	post(t, base, map[string]any{"conversationId": pwChat.ID, "userMessageId": "u2", "parentMessageId": "pwc-1", "newChatEngine": "studio",
		"messages": []any{map[string]any{"role": "user", "content": "more"}}})
	if len(f.created) != 1 {
		t.Fatalf("the old pw code chat was not answered by pw code: %d sessions", len(f.created))
	}
	// A switch is the conversation's, and refused while a reply runs.
	done := m.Conv.BeginAnswer(pwChat.ID)
	if code, out, _ := send(t, http.MethodPut, base+"/api/chat/conversations/"+pwChat.ID+"/engine", `{"engine":"studio"}`); code != 409 || !strings.Contains(out["error"].(string), "switch when it ends") {
		t.Errorf("switch while answering: %d %v", code, out)
	}
	done()
	if code, _, _ := send(t, http.MethodPut, base+"/api/chat/conversations/"+pwChat.ID+"/engine", `{"engine":"studio"}`); code != 200 {
		t.Fatalf("switch: %d", code)
	}
	if menu := get(t, base+"/api/chat/engine?conversation="+pwChat.ID); menu["current"] != "studio" {
		t.Errorf("after the switch: %v", menu)
	}
}

// A new chat starts with the last choice made here, then keeps what
// answered it.
func TestANewChatStartsWithTheLastChoice(t *testing.T) {
	m, base := pwModule(t, &fakePW{script: helloTurn}, nil)
	if menu := get(t, base+"/api/chat/engine?last=studio"); menu["current"] != "studio" || menu["default"] != "pwcode" {
		t.Errorf("new chat: %v", menu)
	}
	fresh := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": fresh.ID, "userMessageId": "u1", "newChatEngine": "studio", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if c := m.Conv.Get(fresh.ID); c.Engine != engineStudio {
		t.Errorf("new chat kept %q", c.Engine)
	}
	if menu := get(t, base+"/api/chat/engine?conversation="+fresh.ID+"&last=pwcode"); menu["current"] != "studio" {
		t.Errorf("a chat the Studio assistant answered, with pw code chosen last elsewhere: %v", menu)
	}
}

// pw code is told what it missed in the words that fit: what came before
// a new session, what came since its last reply for one that answered.
func TestPWCodeIsToldWhatCameSinceItsLastReply(t *testing.T) {
	q, a, q2, b := "first", "pw code said", "second", "the Studio assistant said"
	pwm := "pw code · m"
	c := &conversations.Conversation{Messages: []conversations.Message{
		{ID: "u1", Role: "user", Content: &q},
		{ID: "pwc-1", Role: "assistant", Content: &a, Model: &pwm},
		{ID: "u2", Role: "user", Content: &q2},
		{ID: "a2", Role: "assistant", Content: &b},
	}, PWCode: &conversations.PWCodeLink{Session: "s1", LastMessage: "pwc-1"}}
	got := pwcodeContext(c, false, "u3")
	if !strings.HasPrefix(got, "In this conversation since your last reply:") || strings.Contains(got, "pw code said") || !strings.Contains(got, "The Studio assistant: the Studio assistant said") {
		t.Errorf("since:\n%s", got)
	}
	if got := pwcodeContext(c, true, "u3"); !strings.HasPrefix(got, "Earlier in this conversation, before you joined it:") {
		t.Errorf("new session:\n%s", got)
	}
}

// A relayed chat moves to a new session on its machine, which has the
// tools pw code there has now, and its next message carries the chat over.
func TestARelayedChatStartsANewSessionThere(t *testing.T) {
	local := &fakePW{script: helloTurn}
	remote := &fakePW{script: helloTurn, detail: map[string]any{"history": earlierTurns, "workspace": "/home/me/solver", "permissionMode": "accept-edits"}}
	m, base, _, _ := relayModule(t, local, remote)
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if st := get(t, base+"/api/chat/pwcode/"+id); st["tools"].(map[string]any)["machineHas"] != false {
		t.Errorf("tools: %v", st["tools"])
	}
	before := *m.Conv.Get(id).PWCode
	time.Sleep(2 * time.Millisecond)
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/new-session", `{}`)
	if code != 200 || len(remote.created) != 1 || len(local.created) != 0 {
		t.Fatalf("new session: %d %v, remote %d local %d", code, out, len(remote.created), len(local.created))
	}
	if mk := remote.created[0]; mk["workspace"] != "/home/me/solver" || mk["permissionMode"] != "accept-edits" {
		t.Errorf("made: %s", remote.created[0])
	}
	c := m.Conv.Get(id)
	// (The fake names every session it makes s1.)
	if c.PWCode.Session != out["session"] || !c.PWCode.Relay || c.PWCode.LastMessage != "" || before.LastMessage == "" || c.PWCode.CreatedAt == before.CreatedAt {
		t.Fatalf("link: %+v", c.PWCode)
	}
	post(t, base, map[string]any{"conversationId": id, "userMessageId": "u9", "parentMessageId": c.Messages[len(c.Messages)-1].ID,
		"messages": []any{map[string]any{"role": "user", "content": "go on"}}})
	if len(remote.messages) != 1 || !strings.Contains(remote.messages[0], "before you joined it") || !strings.HasSuffix(remote.messages[0], "go on") {
		t.Errorf("message: %q", remote.messages)
	}
}
