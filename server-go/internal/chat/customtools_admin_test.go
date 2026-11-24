package chat

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// A custom tool runs a command as the Studio's own account. Where people
// sign in it runs only as an administrator last saved it: a list that
// reached the settings file any other way waits for an administrator.
func TestCustomToolsRunOnlyAsAnAdministratorSavedThem(t *testing.T) {
	m, _, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks"})
	tools := []any{map[string]any{"name": "greet", "description": "Says hi", "command": "echo hi"}}
	if err := m.A.Settings.Update(map[string]any{"customTools": tools}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	if out, ok := m.executeInGo(r, "greet", `{}`, ToolCtx{}); ok {
		t.Fatalf("an unapproved tool ran: %v", out)
	}
	for _, s := range customToolSpecs(m.eff(), m.Ext) {
		if s.Name == "greet" {
			t.Fatal("an unapproved tool is offered to the model")
		}
	}

	// A digest of another list approves nothing.
	other := settings.CustomToolsDigest(nil)
	m.A.Settings.Update(map[string]any{settings.CustomToolsApprovedKey: other})
	if _, ok := m.executeInGo(r, "greet", `{}`, ToolCtx{}); ok {
		t.Fatal("a stale approval ran the tool")
	}

	// As an administrator saved it, it runs.
	m.A.Settings.Update(map[string]any{settings.CustomToolsApprovedKey: settings.CustomToolsDigest(m.A.Settings.Effective().CustomTools)})
	out, ok := m.executeInGo(r, "greet", `{}`, ToolCtx{})
	if !ok || strings.TrimSpace(out.Result) != "hi" {
		t.Fatalf("%v %v", ok, out)
	}
	// A changed command is no longer the approved one.
	m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "greet", "description": "Says hi", "command": "id"}}})
	if _, ok := m.executeInGo(r, "greet", `{}`, ToolCtx{}); ok {
		t.Fatal("a changed tool ran")
	}
}

func TestCustomToolsRunWithoutSignInAsBefore(t *testing.T) {
	m, _, _, _ := newTestModule(t, nil)
	m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "greet", "description": "Says hi", "command": "echo hi"}}})
	out, ok := m.executeInGo(httptest.NewRequest("POST", "/api/chat/stream", nil), "greet", `{}`, ToolCtx{})
	if !ok || strings.TrimSpace(out.Result) != "hi" {
		t.Fatalf("%v %v", ok, out)
	}
}
