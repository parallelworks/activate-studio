package agentsites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
)

// shSSH is a pw whose ssh runs the command here, after a login message, as
// the person's shell on the machine would.
func shSSH(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(p, []byte(`#!/bin/sh
[ "$1" = ssh ] || exit 0
[ "$PW_API_KEY" = user-key ] || { echo "wrong key" >&2; exit 1; }
shift; [ "$1" = --platform-host ] && shift 2; shift
echo 'Welcome to the cluster.'
sh -c "$*"
`), 0o755)
	return p
}

// The Studio's section goes into ~/studio-chat as the person, over pw ssh:
// their text is kept, a file that has it already is left, a link is refused
// and so is a file that changed after it was read.
func TestOwnFolderTakesTheStudiosSection(t *testing.T) {
	f := newFixture(t, map[string]string{"PW_CLI": shSSH(t)})
	dir := filepath.Join(t.TempDir(), "it's mine", "studio-chat")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "AGENTS.md")
	own := func() *OwnFolder { return f.s.OwnFolder(context.Background(), "cluster-a", "user-key", dir) }
	read := func() string { b, _ := os.ReadFile(file); return string(b) }

	if err := agentsmd.ApplyStudio(own(), "Search first: it's \"quicker\" than $(find) `here`."); err != nil {
		t.Fatal(err)
	}
	first := read()
	if !strings.Contains(first, "it's \"quicker\" than $(find) `here`.") || !strings.Contains(first, "studio_tools: begin") {
		t.Fatalf("%q", first)
	}
	// The person's own text, and their mode.
	os.WriteFile(file, []byte("# Mine\n\n"+first+"\nAfter.\n"), 0o600)
	os.Chmod(file, 0o600)
	if err := agentsmd.ApplyStudio(own(), "Search first, and again."); err != nil {
		t.Fatal(err)
	}
	got := read()
	if !strings.HasPrefix(got, "# Mine\n\n") || !strings.HasSuffix(got, "\nAfter.\n") || !strings.Contains(got, "and again.") || strings.Contains(got, "quicker") {
		t.Fatalf("%q", got)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if err := agentsmd.ApplyStudio(own(), "Search first, and again."); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(file); !now.ModTime().Equal(st.ModTime()) {
		t.Error("written again with nothing to change")
	}

	// Changed between the read and the write.
	o := own()
	if _, _, err := o.Read(agentsmd.FileName); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte("theirs now\n"), 0o600)
	if err := o.Write(agentsmd.FileName, []byte("ours\n")); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("%v", err)
	}
	if read() != "theirs now\n" {
		t.Errorf("%q", read())
	}

	// A link, and a file too large.
	os.Remove(file)
	to := filepath.Join(t.TempDir(), "to.md")
	os.WriteFile(to, []byte("linked\n"), 0o644)
	if err := os.Symlink(to, file); err != nil {
		t.Skip(err)
	}
	if err := agentsmd.ApplyStudio(own(), "x"); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("%v", err)
	}
	if b, _ := os.ReadFile(to); string(b) != "linked\n" {
		t.Errorf("%q", b)
	}
	os.Remove(file)
	os.WriteFile(file, []byte(strings.Repeat("x", maxOwnFile+1)), 0o644)
	if err := agentsmd.ApplyStudio(own(), "x"); err == nil {
		t.Error("a large file was changed")
	}
	if e, _ := os.ReadDir(dir); len(e) != 1 {
		t.Errorf("left %v", e)
	}
}
