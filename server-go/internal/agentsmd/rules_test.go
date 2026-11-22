package agentsmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadRules(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "kb")
	os.MkdirAll(lib, 0o755)
	link := filepath.Join(dir, "kb-link")
	if err := os.Symlink(lib, link); err != nil {
		t.Skip(err)
	}
	real, _ := filepath.EvalSymlinks(lib)
	rules := ReadRules(link + "/")
	for _, want := range []string{"Bash(pwd)", "Bash(ls -la)", "ReadFile(" + filepath.ToSlash(link) + "/**)", "GrepSearch(" + filepath.ToSlash(real) + "/**)"} {
		if !slices.Contains(rules, want) {
			t.Errorf("missing %s in %v", want, rules)
		}
	}
	// Only whole commands: no rule a command's text could stretch, and no
	// shell that runs anything.
	for _, r := range rules {
		if strings.HasPrefix(r, "Bash(") && strings.ContainsAny(r, "*?[") {
			t.Errorf("a pattern rule: %s", r)
		}
	}
	for _, none := range []string{"", ".", "relative/kb"} {
		if got := ReadRules(none); len(got) != len(folderCommands) || strings.Contains(strings.Join(got, " "), "ReadFile") {
			t.Errorf("ReadRules(%q) = %v", none, got)
		}
	}
}
