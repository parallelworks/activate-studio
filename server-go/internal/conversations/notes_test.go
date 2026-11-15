package conversations

import (
	"strings"
	"testing"
)

func parentOf(c *Conversation, id string) string {
	for _, m := range c.Messages {
		if m.ID == id && m.ParentID != nil {
			return *m.ParentID
		}
	}
	return ""
}

// A note added after the page loaded (a transfer that finished) is followed
// by the next question, not left on a branch beside it.
func TestUserTurnFollowsNotesAddedSince(t *testing.T) {
	s, _ := storeWith(t, "[]")
	s.RecordUserTurn("c1", "u1", "copy the reports", nil, "")
	s.RecordAssistantTurn("c1", Message{ID: "a1", Role: "assistant", Content: strp("started"), ParentID: strp("u1")})
	if !s.AppendNote("c1", "Copy of 4 files finished.") || !s.AppendNote("c1", "Sync finished.") {
		t.Fatal("notes not added")
	}
	c := s.Get("c1")
	last := c.Messages[len(c.Messages)-1].ID
	if !strings.HasPrefix(last, "note-") {
		t.Fatalf("last message %q is not a note", last)
	}
	// The page still thinks a1 is the last message.
	s.RecordUserTurn("c1", "u2", "thanks", strp("a1"), "")
	if got := parentOf(s.Get("c1"), "u2"); got != last {
		t.Fatalf("u2 follows %q, want the last note %q", got, last)
	}
	// A deliberate branch from an earlier message is left alone.
	s.RecordUserTurn("c1", "u3", "copy them again", strp("u1"), "")
	if got := parentOf(s.Get("c1"), "u3"); got != "u1" {
		t.Fatalf("u3 follows %q, want u1", got)
	}
	// Notes that already have a reply below them are not followed again.
	s.RecordUserTurn("c1", "u4", "an edit", strp("a1"), "")
	if got := parentOf(s.Get("c1"), "u4"); got != "a1" {
		t.Fatalf("u4 follows %q, want a1", got)
	}
}
