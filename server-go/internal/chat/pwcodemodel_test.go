package chat

import (
	"strings"
	"testing"
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
		if got := pickPWCodeModel(c.current, c.chosen, offered); got != c.want {
			t.Errorf("pick(%q, %q) = %q, want %q", c.current, c.chosen, got, c.want)
		}
	}
	if pickPWCodeModel("acct:anthropic/claude-x", "", nil) != "" {
		t.Error("an empty listing changes nothing")
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
