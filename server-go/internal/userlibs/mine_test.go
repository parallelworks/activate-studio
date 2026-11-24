//go:build !windows

package userlibs

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TestOwnLibraryIsWrittenAsItsOwner: a library a person built on one of
// their systems over pw ssh is written in the Library as their account,
// learned on the first write and checked after; what lands takes their
// umask, not a space's closing; no write goes through a link; and a change
// builds its index again.
func TestOwnLibraryIsWrittenAsItsOwner(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	h, homes := buildHarness(t)
	home := homes["cluster-a"]
	b := h.post("/api/user-libraries/pw/build", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/work", "depth": "names"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	id := b.Body["id"].(string)
	got := h.waitFor(id)
	eq(t, []any{got["state"], got["mine"]}, []any{"ready", true}, "built and written as the person")
	if d := find(h.m.load("single"), id); !d.Guard || d.Own == nil || d.Own.UID != os.Getuid() || d.ReadOnly != nil {
		t.Fatalf("what the check found: %+v", d)
	}
	// One added before that check has none of it, and stays as it was.
	h.m.Users.Update("single", id, func(d *libraries.UserDef) { d.Guard, d.Own = false, nil })

	SpaceEvery = 50 * time.Millisecond
	t.Cleanup(func() { SpaceEvery = 20 * time.Second })
	s := h.m.SpaceFiles()
	lib := kb.Library{ID: id, Label: "work", IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
	req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
	if err := s.Put(req, lib, "new/a.txt", strings.NewReader("hello"), 100); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "work")
	if b, _ := os.ReadFile(filepath.Join(dir, "new", "a.txt")); string(b) != "hello" {
		t.Fatalf("written: %q", b)
	}
	eq(t, perm(t, filepath.Join(dir, "new", "a.txt")), os.FileMode(0o644), "the person's umask, not a space's")
	eq(t, perm(t, filepath.Join(dir, "new")), os.FileMode(0o755), "a folder made with the person's umask")
	var def *libraries.UserDef
	for _, d := range h.m.Users.Load("single") {
		if d.ID == id {
			d := d
			def = &d
		}
	}
	if def == nil || def.Writer == nil || def.Writer.UID != os.Getuid() || def.Writer.Account == "" {
		t.Fatalf("the account written as was not kept: %+v", def)
	}

	// A link someone made in the library does not carry a write elsewhere.
	os.MkdirAll(filepath.Join(home, "private"), 0o700)
	if err := os.Symlink(filepath.Join(home, "private"), filepath.Join(dir, "door")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(req, lib, "door/x.txt", strings.NewReader("x"), 100); err == nil {
		t.Fatal("a write through a link was taken")
	}
	if err := s.Mkdir(req, lib, "door/sub"); err == nil {
		t.Fatal("a folder through a link was made")
	}
	if left, _ := os.ReadDir(filepath.Join(home, "private")); len(left) != 0 {
		t.Fatalf("private holds %v", left)
	}
	for _, p := range []string{"..", "../private", "new/../../private"} {
		if _, err := s.List(req, lib, p); err == nil || !strings.Contains(err.Error(), "leads outside") {
			t.Errorf("list %q: %v", p, err)
		}
	}
	if to, err := s.Rename(req, lib, "new/a.txt", "b.txt"); err != nil || to != "new/b.txt" {
		t.Fatalf("rename: %q %v", to, err)
	}
	if done, _, err := s.Delete(req, lib, []string{"door"}); err != nil || len(done) != 1 {
		t.Fatalf("removing the link: %v %v", done, err)
	}

	// A change builds the index again: notes.txt and new/b.txt.
	s.Changed(req, lib, []string{"new"})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if l := h.library(id); l != nil && fmt.Sprint(l["files"]) == "2" && l["state"] == "ready" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if l := h.library(id); fmt.Sprint(l["files"]) != "2" {
		t.Fatalf("not built again: %v", l)
	}

	// A session that reaches another account than the one kept is refused.
	h.m.Users.Update("single", id, func(d *libraries.UserDef) { d.Writer.UID++ })
	h.m.mu.Lock()
	h.m.accounts = nil
	h.m.mu.Unlock()
	if err := s.Put(req, lib, "c.txt", strings.NewReader("c"), 100); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("another account: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err == nil {
		t.Fatal("written as another account")
	}
}

// TestWhichLibrariesAreWrittenAsTheViewer: only a library the Studio built
// from a folder it knows is one of the person's to write in; over plain
// ssh, only on a Studio one person uses with sign-in off.
func TestWhichLibrariesAreWrittenAsTheViewer(t *testing.T) {
	src := "/home/ada/work"
	built := libraries.UserDef{ID: "u-w", Transport: "pw", Site: "cluster-a", Build: &libraries.RemoteBuild{SourceRoot: src}}
	connected := libraries.UserDef{ID: "u-c", Transport: "pw", Site: "cluster-a", Existing: ptr(true), SourceRoot: &src}
	noFolder := libraries.UserDef{ID: "u-n", Transport: "pw", Site: "cluster-a", Build: &libraries.RemoteBuild{SourceRoot: "~/work"}}
	space := built
	space.Space = &libraries.SpaceInfo{Account: "ada", UID: 1001}
	eq(t, []bool{built.Mine(), connected.Mine(), noFolder.Mine(), space.Mine()}, []bool{true, false, false, false}, "mine")
	eq(t, []bool{built.WrittenAsOwner(), connected.WrittenAsOwner()}, []bool{true, false}, "written as owner")
	if _, _, ok := built.Account(); ok {
		t.Fatal("an account before the first write")
	}

	var m Module
	overSSH := built
	overSSH.Transport, overSSH.Destination = "ssh", ptr("ada@box")
	if _, _, err := m.writeSession(who{ID: "ada", Auth: true, Own: "key"}, "ada", &overSSH); err == nil {
		t.Fatal("a library over ssh was written on a Studio with sign-in on")
	}
	if _, _, err := m.writeSession(who{ID: "ada", Auth: true}, "ada", &built); err == nil {
		t.Fatal("written without the person's own key")
	}
	if site, key, err := m.writeSession(who{ID: "ada", Auth: true, Own: "key-ada"}, "ada", &built); err != nil || site != "cluster-a" || key != "key-ada" {
		t.Fatalf("over pw ssh with their key: %q %q %v", site, key, err)
	}
}
