package chat

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

func TestChatStates(t *testing.T) {
	m, _, _, _ := newTestModule(t, nil)
	c := m.Conv.Create(nil, "")
	m.Conv.RecordAssistantTurn(c.ID, conversations.Message{ID: "r1", Role: "assistant", Content: sp("Hello"), Timestamp: "2026-11-24T12:00:00.000Z"})
	m.Conv.AppendNoteKind(c.ID, "engine", "The Studio assistant answers here.")
	r := httptest.NewRequest("GET", "/", nil)
	one := func() ChatState {
		t.Helper()
		ss := m.ChatStates(r)
		if len(ss) != 1 {
			t.Fatalf("chats: %+v", ss)
		}
		return ss[0]
	}
	// Which engine answered is said beside a reply: it is not one.
	if s := one(); s.Reply.ID != "r1" || s.Reply.Note || s.Working || len(s.Asks) != 0 {
		t.Errorf("at rest: %+v", s)
	}
	m.Conv.AppendNote(c.ID, "The run ended.")
	if s := one(); !s.Reply.Note {
		t.Errorf("a note: %+v", s)
	}
	done := m.Conv.BeginAnswer(c.ID)
	if s := one(); !s.Working {
		t.Errorf("answering: %+v", s)
	}
	done()

	// A pw code turn's open approvals, until they are answered.
	turn := &pwTurn{m: m, convID: c.ID, link: &conversations.PWCodeLink{Session: "s1"}}
	m.follow(turn)
	ev := func(pairs ...any) { turn.handle(jsonv.NewObject(pairs...)) }
	ask := jsonv.NewObject("id", "ap1", "kind", "confirm", "tool", "bash")
	ev("type", "approval_request", "approval", ask)
	ev("type", "approval_request", "approval", ask)
	ev("type", "approval_request", "approval", jsonv.NewObject("id", "ap2", "kind", "ask"))
	if s := one(); !s.Working || len(s.Asks) != 2 || s.Asks[0].ID != "ap1" {
		t.Errorf("asked: %+v", s)
	}
	ev("type", "approval_resolved", "approvalId", "ap1")
	if s := one(); len(s.Asks) != 1 || s.Asks[0].ID != "ap2" {
		t.Errorf("answered: %+v", s)
	}
	m.unfollow(turn)
	if s := one(); s.Working || len(s.Asks) != 0 {
		t.Errorf("ended: %+v", s)
	}

	// A call the Studio assistant holds, while it can still be answered.
	now := time.Now()
	m.held.hold(&HeldCall{ConversationID: c.ID, ToolCallID: "call1", Tool: "write_kb_file", HeldAt: nowISO(), ExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339)})
	if s := one(); len(s.Asks) != 1 || s.Asks[0].Tool != "write_kb_file" {
		t.Errorf("held: %+v", s)
	}
	m.held.now = func() time.Time { return now.Add(time.Hour) }
	if s := one(); len(s.Asks) != 0 {
		t.Errorf("expired: %+v", s)
	}
}

// With sign-in a person's notices are of their own chats, though shared
// history lets them read everyone's.
func TestChatStatesAreTheirOwn(t *testing.T) {
	m, _, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks"})
	if !m.A.Cfg.AuthEnabled() {
		t.Fatal("sign-in is not on")
	}
	mine := m.Conv.Create(nil, "ana")
	m.Conv.Create(nil, "ben")
	m.Conv.Create(nil, "")
	r := auth.WithUser(httptest.NewRequest("GET", "/", nil), &auth.User{ID: "u1", Username: "ana"})
	if ss := m.ChatStates(r); len(ss) != 1 || ss[0].ID != mine.ID {
		t.Errorf("ana: %+v", ss)
	}
	if ss := m.ChatStates(httptest.NewRequest("GET", "/", nil)); len(ss) != 0 {
		t.Errorf("nobody: %+v", ss)
	}
}
