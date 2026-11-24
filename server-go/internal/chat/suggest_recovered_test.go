package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

func suggestionTexts(t *testing.T, parts ...conversations.Part) []string {
	t.Helper()
	answer := "The workbook is written."
	c := &conversations.Conversation{Messages: []conversations.Message{
		{ID: "u1", Role: "user"},
		{ID: "a1", Role: "assistant", Content: &answer, Parts: parts},
	}}
	b, err := json.Marshal(SuggestNext(c))
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Suggestions []struct{ Text string } }
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range v.Suggestions {
		out = append(out, s.Text)
	}
	return out
}

func toolPart(name, args, status string) conversations.Part {
	r := "result"
	return conversations.Part{Kind: "tool_call", ID: name + status + args, Name: name, Args: args, Status: status, Result: &r}
}

// A step that failed and that the turn then did again on the same file is
// not offered for a retry; one that stayed failed is, named in plain words
// and never by its tool's name.
func TestARecoveredStepIsNotOfferedForARetry(t *testing.T) {
	for _, s := range suggestionTexts(t,
		toolPart("write_kb_file", `{"path":"emu.xlsx","content":"{bad"}`, "error"),
		toolPart("write_kb_file", `{"path":"emu.xlsx","content":"ok"}`, "ok")) {
		if strings.Contains(s, "Retry") || strings.Contains(s, "failed") {
			t.Errorf("recovered: offered %q", s)
		}
	}
	got := suggestionTexts(t,
		toolPart("write_kb_file", `{"path":"emu.xlsx"}`, "error"),
		toolPart("write_kb_file", `{"path":"other.xlsx"}`, "ok"))
	want := "Retry the step that failed (writing emu.xlsx) and explain what went wrong"
	if !hasText(got, want) {
		t.Errorf("not recovered: %q, want %q", got, want)
	}
	for _, s := range append(got, suggestionTexts(t, toolPart("hpc_status", `{}`, "error"))...) {
		if strings.Contains(s, "_kb_") || strings.Contains(s, "hpc_status") {
			t.Errorf("a tool's name in %q", s)
		}
	}
	if got := suggestionTexts(t, toolPart("hpc_status", `{}`, "error")); !hasText(got, "Retry the step that failed and explain what went wrong") {
		t.Errorf("no plain words: %q", got)
	}
}

func hasText(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
