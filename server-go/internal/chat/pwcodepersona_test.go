package chat

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// A pw code chat with a persona works in agents/<persona>/, whose
// AGENTS.md carries the persona's text; dropping the persona starts a
// session where chats without one work, and the old one is archived.
func TestPWCodePersonaIsTheFoldersAgentsMD(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	root := m.A.Cfg.KBRoot
	os.MkdirAll(filepath.Join(root, ".agents"), 0o755)
	os.WriteFile(filepath.Join(root, ".agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: Reviews\n---\n\nCheck every claim against its source.\n"), 0o644)
	prevKB, prevSvc := kbpkg.Default, agentsmd.Default
	kbpkg.Register(http.NewServeMux(), m.A)
	agentsmd.Default = agentsmd.New(m.A)
	t.Cleanup(func() { kbpkg.Default, agentsmd.Default = prevKB, prevSvc })

	c := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "agent": "reviewer",
		"messages": []any{map[string]any{"role": "user", "content": "Review the budget."}}})
	want := filepath.Join(root, "agents", "reviewer")
	if len(f.created) != 1 || f.created[0]["workspace"] != want {
		t.Fatalf("sessions %v", f.created)
	}
	b, _ := os.ReadFile(filepath.Join(want, "AGENTS.md"))
	if !strings.Contains(string(b), "Check every claim against its source.") || !strings.Contains(string(b), "instructions reviewer: begin") {
		t.Fatalf("AGENTS.md:\n%s", b)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Persona != "reviewer" || l.Workspace != want {
		t.Fatalf("link %+v", l)
	}

	// The same persona keeps the session.
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2", "agent": "reviewer",
		"messages": []any{map[string]any{"role": "user", "content": "And the schedule?"}}})
	if len(f.created) != 1 {
		t.Fatalf("a second session for the same persona: %d", len(f.created))
	}

	// No persona: a session in the library's top, the old one archived.
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u3",
		"messages": []any{map[string]any{"role": "user", "content": "Plainly now."}}})
	if len(f.created) != 2 || f.created[1]["workspace"] != root || f.archived != 1 {
		t.Fatalf("after dropping the persona: %v, archived %d", f.created, f.archived)
	}
	if l := m.Conv.Get(c.ID).PWCode; l == nil || l.Persona != "" {
		t.Fatalf("link %+v", l)
	}
}
