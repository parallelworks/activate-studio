package people

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// Who has signed in is kept across restarts, in a file of the Studio's
// account alone, and answers for a platform account name with or without
// "user:", in any case. One the gate did not fully verify is not kept.
func TestPeopleWhoHaveSignedIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "people.json")
	s := New(path)
	if s.Known("ada") {
		t.Fatal("known before signing in")
	}
	s.Seen(nil)
	s.Seen(&auth.User{ID: "user:bob"})
	s.Seen(&auth.User{ID: "user:ada", Username: "ada"})
	again := New(path)
	for _, n := range []string{"ada", "user:ada", "ADA", " ada "} {
		if !again.Known(n) {
			t.Errorf("%q is not known", n)
		}
	}
	for _, n := range []string{"bob", "", "user:", "eve"} {
		if again.Known(n) {
			t.Errorf("%q is known", n)
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file: %v %v", fi, err)
	}
	// Seen again, nothing is written.
	os.Chmod(path, 0o400)
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	again.Seen(&auth.User{ID: "user:ada", Username: "ada"})
}
