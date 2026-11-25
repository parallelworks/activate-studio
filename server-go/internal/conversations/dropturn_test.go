package conversations

import "testing"

// A turn taken back leaves the chat as it was before it; one something
// follows, or that is not the person's, stays.
func TestDropUserTurn(t *testing.T) {
	s := New(t.TempDir(), t.TempDir(), Hooks{})
	c := s.Create(nil, "")
	s.RecordUserTurn(c.ID, "u1", "one", nil, "")
	s.RecordAssistantTurn(c.ID, Message{ID: "a1", Role: "assistant", ParentID: strp("u1")})
	s.RecordUserTurn(c.ID, "u2", "/plan", strp("a1"), "")
	has := func(want string, ids ...string) {
		t.Helper()
		got := s.Get(c.ID)
		if len(got.Messages) != len(ids) || *got.ActiveBranchID != want {
			t.Fatalf("%+v on %s", got.Messages, *got.ActiveBranchID)
		}
	}
	s.DropUserTurn(c.ID, "u2")
	has("a1", "u1", "a1")
	for _, id := range []string{"u1", "a1", "u2", ""} {
		s.DropUserTurn(c.ID, id)
	}
	s.DropUserTurn("gone", "u1")
	has("a1", "u1", "a1")
	s.RecordUserTurn(c.ID, "u2", "again", strp("a1"), "")
	has("u2", "u1", "a1", "u2")
}

func TestACommandIsADefaultTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"/plan say the tide": true, "/help": true, "/compact...": true, "/code-review…": true,
		"/etc/hosts what is it": false, "The tide": false, "/ plan": false,
	} {
		if got := IsDefaultTitle(title, "other words"); got != want {
			t.Errorf("%q: %v", title, got)
		}
	}
}
