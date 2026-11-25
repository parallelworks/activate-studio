//go:build !windows

package conversations

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestTheFileIsTheStudiosAlone: the chats are in a file other accounts of
// the host cannot read, one an earlier version left open too.
func TestTheFileIsTheStudiosAlone(t *testing.T) {
	mode := func(f string) os.FileMode {
		t.Helper()
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		return st.Mode().Perm()
	}
	title := "x"
	t.Run("new", func(t *testing.T) {
		ix := t.TempDir()
		New(ix, t.TempDir(), Hooks{}).Create(&title, "ada")
		if m := mode(filepath.Join(ix, "conversations.json")); m != 0o600 {
			t.Errorf("%o", m)
		}
	})
	t.Run("from an earlier version", func(t *testing.T) {
		ix := t.TempDir()
		f, left := filepath.Join(ix, "conversations.json"), filepath.Join(ix, "conversations.json.77.tmp")
		other := filepath.Join(ix, "settings.json")
		for _, x := range []string{f, left, other} {
			os.WriteFile(x, []byte(`[{"id":"c1","owner":"ada","messages":[]}]`), 0o644)
			os.Chmod(x, 0o664)
		}
		s := New(ix, t.TempDir(), Hooks{Log: func(m string) { t.Error(m) }})
		if mode(f) != 0o600 || mode(left) != 0o600 || mode(other) != 0o664 {
			t.Errorf("on start: %o %o %o", mode(f), mode(left), mode(other))
		}
		if s.Get("c1") == nil {
			t.Error("the chat is gone")
		}
		s.Create(&title, "ada")
		if mode(f) != 0o600 || len(s.List()) != 2 {
			t.Errorf("written: %o, %d chats", mode(f), len(s.List()))
		}
	})
	t.Run("a mode narrower than that stays", func(t *testing.T) {
		ix := t.TempDir()
		f := filepath.Join(ix, "conversations.json")
		os.WriteFile(f, []byte(`[]`), 0o400)
		if New(ix, t.TempDir(), Hooks{}); mode(f) != 0o400 {
			t.Errorf("%o", mode(f))
		}
	})
	t.Run("a write cut short with this process's id", func(t *testing.T) {
		ix := t.TempDir()
		s := New(ix, t.TempDir(), Hooks{})
		tmp := filepath.Join(ix, "conversations.json."+strconv.Itoa(os.Getpid())+".tmp")
		os.WriteFile(tmp, []byte("["), 0o644)
		s.Create(&title, "ada")
		if m := mode(filepath.Join(ix, "conversations.json")); m != 0o600 {
			t.Errorf("%o", m)
		}
	})
	t.Run("a link is left as it is", func(t *testing.T) {
		ix := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		os.WriteFile(target, []byte(`[]`), 0o644)
		os.Symlink(target, filepath.Join(ix, "conversations.json"))
		if New(ix, t.TempDir(), Hooks{}); mode(target) != 0o644 {
			t.Errorf("%o", mode(target))
		}
	})
}
