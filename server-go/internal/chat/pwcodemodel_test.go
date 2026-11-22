package chat

import (
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

func TestPWCodeChatPicksAModelTheConnectionOffers(t *testing.T) {
	offered := []string{"acct:codex/gpt-a", "acct:codex/gpt-b", "test-model"}
	for _, c := range []struct{ current, chosen, want string }{
		{"acct:anthropic/claude-x", "acct:codex/gpt-b", "acct:codex/gpt-b"}, // the Studio assistant's model
		{"acct:anthropic/claude-x", "test-model", "acct:codex/gpt-a"},       // not a platform model: the first one offered
		{"acct:anthropic/claude-x", "", "acct:codex/gpt-a"},
		{"acct:codex/gpt-a", "acct:codex/gpt-b", ""},  // offered: kept
		{"claude-code/haiku", "acct:codex/gpt-b", ""}, // pw code's own backend: kept
		{"", "acct:codex/gpt-b", ""},
	} {
		if got := pickPWCodeModel(c.current, c.chosen, offered, nil); got != c.want {
			t.Errorf("pick(%q, %q) = %q, want %q", c.current, c.chosen, got, c.want)
		}
	}
	if pickPWCodeModel("acct:anthropic/claude-x", "", nil, nil) != "" {
		t.Error("an empty listing changes nothing")
	}
	// A model offered whose call just failed for this person is left for
	// one that did not fail; a failing chosen model is passed over.
	failing := map[string]bool{"acct:codex/gpt-a": true}
	if got := pickPWCodeModel("acct:codex/gpt-a", "acct:codex/gpt-b", offered, failing); got != "acct:codex/gpt-b" {
		t.Errorf("off a failing model: %q", got)
	}
	if got := pickPWCodeModel("acct:anthropic/claude-x", "acct:codex/gpt-a", offered, failing); got != "acct:codex/gpt-b" {
		t.Errorf("a failing choice: %q", got)
	}
	if got := pickPWCodeModel("acct:codex/gpt-a", "", []string{"acct:codex/gpt-a"}, failing); got != "" {
		t.Errorf("nothing else to move to: %q", got)
	}
}

// Review 1, finding 10: a model's failed call is remembered for the key
// that made it. Another person's failure leaves this chat where it is; the
// person's own moves it, and the note says it failed rather than that it
// is not enabled.
func TestPWCodeChatModelFailuresArePerKey(t *testing.T) {
	f := &fakePW{script: helloTurn, model: "acct:codex/gpt-a"}
	m, base, gw := pwModuleGW(t, f, nil)
	gw.models = `{"data":[{"id":"acct:codex/gpt-a","object":"model"},{"id":"acct:codex/gpt-b","object":"model"}]}`
	m.LLM.RecordOutcome("someone-elses-key", "acct:codex/gpt-a", &llm.ModelFailure{Message: "rate limited", Status: 429})
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "model": "acct:codex/gpt-b",
		"messages": []any{map[string]any{"role": "user", "content": "Hi"}}})
	if len(f.settings) != 0 || !strings.Contains(sse, `"modelMoved":false`) {
		t.Fatalf("another person's failure moved the chat: settings %q\n%s", f.settings, sse)
	}
	m.LLM.RecordOutcome("", "acct:codex/gpt-a", &llm.ModelFailure{Message: "rate limited", Status: 429})
	f.mcp = "ok"
	sse = post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2", "model": "acct:codex/gpt-b",
		"messages": []any{map[string]any{"role": "user", "content": "Again"}}})
	if len(f.settings) != 1 || f.settings[0] != `{"model":"acct:codex/gpt-b"}` {
		t.Fatalf("their own failure: settings %q", f.settings)
	}
	if !strings.Contains(sse, `"modelNote":"pw code's own model, acct:codex/gpt-a, failed when you last used it, in the last ten minutes, so this chat runs on acct:codex/gpt-b`) ||
		strings.Contains(sse, "not enabled") {
		t.Errorf("note:\n%s", sse)
	}
}

// Review 1, finding 21: a move that fails is said in the chat, not only
// logged, and the link keeps the model the session is on.
func TestPWCodeChatSaysWhenTheModelCannotMove(t *testing.T) {
	f := &fakePW{script: helloTurn, model: "acct:anthropic/claude-x", refuseSettings: true}
	m, base, gw := pwModuleGW(t, f, nil)
	gw.models = `{"data":[{"id":"acct:codex/gpt-a","object":"model"}]}`
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "Hi"}}})
	if !strings.Contains(sse, `"modelStuck":true`) || !strings.Contains(sse, "is not enabled for your AI connection, and the Studio could not move this chat to acct:codex/gpt-a: ") ||
		!strings.Contains(sse, `"modelMoved":false`) {
		t.Errorf("pwcode event:\n%s", sse)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Model != "acct:anthropic/claude-x" {
		t.Errorf("link: %+v", l)
	}
}

func TestPWCodeChatMovesOffAModelTheConnectionDoesNotOffer(t *testing.T) {
	f := &fakePW{script: helloTurn, model: "acct:anthropic/claude-x"}
	m, base, gw := pwModuleGW(t, f, nil)
	gw.models = `{"data":[{"id":"acct:codex/gpt-a","object":"model"},{"id":"acct:codex/gpt-b","object":"model"}]}`
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "model": "acct:codex/gpt-b",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	if len(f.settings) != 1 || f.settings[0] != `{"model":"acct:codex/gpt-b"}` {
		t.Fatalf("settings sent: %q", f.settings)
	}
	if !strings.Contains(sse, `"model":"acct:codex/gpt-b","modelNote":"pw code's own model, acct:anthropic/claude-x, is not enabled for your AI connection, so this chat runs on acct:codex/gpt-b, the model chosen for the Studio assistant.`) ||
		!strings.Contains(sse, `"modelMoved":true`) {
		t.Errorf("pwcode event:\n%s", sse)
	}
	l := m.Conv.Get(c.ID).PWCode
	if l == nil || l.Model != "acct:codex/gpt-b" || !strings.Contains(l.ModelNote, "Settings, External access, pw code model") {
		t.Fatalf("link: %+v", l)
	}
	// The next message finds the session on an offered model and leaves it.
	f.mcp = "ok"
	sse = post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2", "model": "acct:codex/gpt-a",
		"messages": []any{map[string]any{"role": "user", "content": "And last year?"}}})
	if len(f.settings) != 1 || !strings.Contains(sse, `"modelMoved":false`) {
		t.Errorf("second turn: settings %q\n%s", f.settings, sse)
	}
}

func TestPWCodeChatKeepsTheModelSettingsName(t *testing.T) {
	f := &fakePW{script: helloTurn, model: "acct:anthropic/claude-x"}
	m, base, gw := pwModuleGW(t, f, map[string]string{"PWCODE_MODEL": "acct:anthropic/claude-x"})
	gw.models = `{"data":[{"id":"acct:codex/gpt-a","object":"model"}]}`
	c := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "Hi"}}})
	if len(f.settings) != 0 || m.Conv.Get(c.ID).PWCode.ModelNote != "" {
		t.Errorf("settings sent: %q", f.settings)
	}
}
