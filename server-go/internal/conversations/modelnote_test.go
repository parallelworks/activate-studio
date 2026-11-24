package conversations

import (
	"encoding/json"
	"strings"
	"testing"
)

// A chat stored by an earlier build keeps its long model note, with the
// models' full ids; it is read the short way, the full id of the model the
// session was moved off kept for the hover.
func TestAnOldLongModelNoteIsReadTheShortWay(t *testing.T) {
	const other = " Choose another under Settings, External access, pw code model."
	cases := []struct{ old, want, from string }{
		{"pw code's own model, acct.one:anthropic/sonnet-x, is not enabled for your AI connection, so this chat runs on acct.one:codex/gpt-b-6.2, the first model it offers. Settings, External access, pw code model names another.",
			"Runs on gpt-b-6.2 because sonnet-x is not enabled for you." + other, "acct.one:anthropic/sonnet-x"},
		{"pw code's own model, acct.one:anthropic/sonnet-x, failed when you last used it, in the last ten minutes, so this chat runs on acct.one:codex/gpt-b, the model chosen for the Studio assistant. Settings, External access, pw code model names another.",
			"Runs on gpt-b because sonnet-x failed for you in the last ten minutes." + other, "acct.one:anthropic/sonnet-x"},
		{"pw code's own model, acct.one:anthropic/sonnet-x, is not enabled for your AI connection, and the Studio could not move this chat to acct.one:codex/gpt-b: the session is busy. Settings, External access, pw code model names another.",
			"sonnet-x is not enabled for you, and pw code did not take gpt-b in its place: the session is busy." + other, "acct.one:anthropic/sonnet-x"},
		{"pw code's model acct.one:anthropic/sonnet-x is not enabled for the AI connection, so the session runs on acct.one:codex/gpt-b",
			"Runs on gpt-b because sonnet-x is not enabled for you." + other, "acct.one:anthropic/sonnet-x"},
	}
	for _, c := range cases {
		stored, _ := json.Marshal(map[string]any{"id": "c1", "title": "t", "messages": []any{}, "pwcode": map[string]any{
			"site": "local", "session": "s1", "createdAt": "2026-11-01T00:00:00Z", "model": "acct.one:codex/gpt-b", "modelNote": c.old}})
		var conv Conversation
		if err := json.Unmarshal(stored, &conv); err != nil {
			t.Fatal(err)
		}
		if conv.PWCode.ModelNote != c.want || conv.PWCode.ModelFrom != c.from {
			t.Errorf("read %q\n got %q (from %q)\nwant %q (from %q)", c.old, conv.PWCode.ModelNote, conv.PWCode.ModelFrom, c.want, c.from)
		}
		// Served as read.
		out, _ := json.Marshal(conv.PWCode)
		if strings.Contains(string(out), "AI connection") {
			t.Errorf("served the old note: %s", out)
		}
	}
	// Today's note, and a note in no old form, are read as stored.
	for _, n := range []string{"Runs on gpt-b because sonnet-x is not enabled for you." + other, "Something else."} {
		var l PWCodeLink
		_ = json.Unmarshal([]byte(`{"modelNote":`+jsonString(n)+`}`), &l)
		if l.ModelNote != n || l.ModelFrom != "" {
			t.Errorf("%q read as %q", n, l.ModelNote)
		}
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
