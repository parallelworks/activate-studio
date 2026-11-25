package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
)

// A search with no GUFI here passes over the Studio's own AGENTS.md too,
// and it does not count toward the limit.
func TestGrepFallbackPassesOverTheStudiosOwnAgentsFile(t *testing.T) {
	if _, err := exec.LookPath("grep"); err != nil {
		t.Skip("needs grep")
	}
	root := t.TempDir()
	const body = "Use the quokkafish index first."
	own := ownfile.Begin(body) + "\n\n" + body + "\n\n<!-- ACTIVATE Studio instructions studio_tools: end -->\n"
	for rel, text := range map[string]string{"AGENTS.md": own, "a/AGENTS.md": own + "\nA quokkafish of mine.\n", "a/b/AGENTS.md": own} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(GrepFallback(context.Background(), "quokkafish", root, 1), " "); got != "a/AGENTS.md" {
		t.Errorf("found %q", got)
	}
}
