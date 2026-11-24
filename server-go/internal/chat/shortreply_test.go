package chat

import (
	"strings"
	"testing"
)

// A short reply after reading tools that says what it will do next is
// taken for a stub and a final answer is asked for. After a call that
// changed something it is a confirmation, and stands.
func TestShortReplyAfterAChangeStands(t *testing.T) {
	for _, c := range []struct {
		tool   string
		forced bool
	}{
		{"list_kb_dir", true},
		{"copy_kb_files", false},
		{"transfer_files", false},
	} {
		t.Run(c.tool, func(t *testing.T) {
			_, srv, gw, _ := newTestModule(t, nil)
			gw.load([]reply{call(c.tool, `{}`), text("Let me check the rest next."), text("The final answer.")})
			sse := post(t, srv.URL, map[string]any{"model": "test-model", "conversationId": "c-" + c.tool, "userMessageId": "u1",
				"messages": []any{map[string]any{"role": "user", "content": "do it"}}})
			n := 2
			if c.forced {
				n = 3
			}
			if len(gw.requests) != n || strings.Contains(sse, "The final answer.") != c.forced || !strings.Contains(sse, `"text":"Let me check the rest next.`) {
				t.Errorf("%d model requests, stream %q", len(gw.requests), sse)
			}
		})
	}
}

// A short reply after reading tools that answers is the answer: no
// second, forced answer follows the one already shown. "Let me know"
// offers more, and does not say what the model will do next.
func TestShortAnswerAfterReadingStands(t *testing.T) {
	for _, answer := range []string{
		"The notes folder contains 1 file: notes/readme.md",
		"The first line of notes/readme.md is # Test notes.",
		"It holds 1 file, notes/readme.md. Let me know if you want it opened.",
	} {
		_, srv, gw, _ := newTestModule(t, nil)
		gw.load([]reply{call("list_kb_dir", `{}`), text(answer), text("I could verify one file, but not the total count.")})
		sse := post(t, srv.URL, map[string]any{"model": "test-model", "conversationId": "c-short", "userMessageId": "u1",
			"messages": []any{map[string]any{"role": "user", "content": "How many files are in the notes folder of my library? List them."}}})
		if len(gw.requests) != 2 || strings.Contains(sse, "could verify") || !strings.Contains(sse, `"content":"`+answer+`"`) {
			t.Errorf("%q: %d model requests, stream %q", answer, len(gw.requests), sse)
		}
	}
}

// A short reply that shows something, such as a session's card, is the
// answer: no second answer is asked for, which repeated the card.
func TestShortReplyThatShowsSomethingStands(t *testing.T) {
	_, srv, gw, _ := newTestModule(t, nil)
	card := `Your desktop is running.\n\n![desk](/?embed=session&user=alex&name=desk)`
	gw.load([]reply{call("list_sessions", `{}`), text(card), text("The final answer.")})
	sse := post(t, srv.URL, map[string]any{"model": "test-model", "conversationId": "c-card", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "show me my desktop"}}})
	if len(gw.requests) != 2 || strings.Contains(sse, "The final answer.") || !strings.Contains(sse, "embed=session") {
		t.Errorf("%d model requests, stream %q", len(gw.requests), sse)
	}
}
