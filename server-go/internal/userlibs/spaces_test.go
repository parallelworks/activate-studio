package userlibs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// fakeBundle is a GUFI bundle as the packages carry one (bin, lib and a
// manifest), whose tools hand over to the GUFI installed here.
func fakeBundle(t *testing.T, real string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "gufi")
	for _, tool := range []string{"gufi_query", "gufi_dir2index", "gufi_sqlite3"} {
		exe(t, filepath.Join(d, "bin", tool), "#!/bin/sh\nexec "+filepath.Join(real, tool)+" \"$@\"\n")
	}
	write(t, filepath.Join(d, "lib", "libstandin.so"), "x")
	if err := os.Symlink("libstandin.so", filepath.Join(d, "lib", "libstandin.so.1")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d, "manifest.json"), `{"gufi":{"version":"test"}}`)
	write(t, filepath.Join(d, "licenses", "LICENSE.txt"), "license")
	return filepath.Join(d, "bin")
}

func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestSpaceCheckRefusesWhatAnotherAccountCouldChange(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home := homes["cluster-a"]

	st := h.do(http.MethodGet, "/api/user-libraries/space", nil)
	eq(t, []any{st.Body["available"], st.Body["cluster"], st.Body["suggested"]}, []any{true, "cluster-a", "~/studio"}, "status")

	r := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/studio"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	match(t, stepDetail(r, "folder"), `does not exist yet\. It is made, open to you alone`, "missing")
	match(t, stepDetail(r, "gufi"), `copies its GUFI`, "gufi sent")
	eq(t, r.Body["create"], []any{home + "/studio"}, "create")
	if _, err := os.Stat(filepath.Join(home, "studio")); err == nil {
		t.Error("a check makes nothing")
	}

	match(t, firstFailed(h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~"})), `That is your home folder`, "home")
	match(t, firstFailed(h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/.activate-studio/x"})), `the Studio's own`, "studio")

	open := filepath.Join(home, "open")
	os.MkdirAll(open, 0o755)
	os.Chmod(open, 0o777)
	match(t, firstFailed(h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open"})), `Every account there can write in .*/open \(mode 777\)`, "open folder")

	// A folder of the person's own, under one every account may write in:
	// someone else could rename it away and put theirs in its place.
	os.MkdirAll(filepath.Join(open, "mine"), 0o700)
	match(t, firstFailed(h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/mine"})), `/open can be written by every account, so another account could move or replace`, "unsafe path")
	os.Chmod(open, 0o777|os.ModeSticky)
	r = h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/mine"})
	if r.Body["ok"] != true {
		t.Fatalf("the sticky bit keeps others from renaming it: %v", r.Body)
	}

	shared := filepath.Join(home, "shared")
	os.MkdirAll(shared, 0o755)
	r = h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/shared"})
	eq(t, r.Body["ok"], true, "readable by others")
	match(t, stepDetail(r, "folder"), `Other accounts can open .*/shared now \(mode 755\)`, "warned")
}

func TestSpaceIsMadeAndWrittenAsThePerson(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home := homes["cluster-a"]

	refused := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/studio"})
	eq(t, refused.Status, 409, "confirm")
	match(t, refused.Body["error"], `studio does not exist yet\. Confirm to create it\.`, "confirm")

	b := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/studio", "create": true, "depth": "names"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	id := b.Body["id"].(string)
	eq(t, []any{b.Body["label"], b.Body["site"], b.Body["state"]}, []any{"My library", "cluster-a", "building"}, "started")
	if sp, _ := b.Body["space"].(map[string]any); sp == nil || sp["account"] == "" {
		t.Fatalf("space: %v", b.Body["space"])
	}
	eq(t, perm(t, filepath.Join(home, "studio")), os.FileMode(0o700), "made for the person alone")
	eq(t, perm(t, filepath.Join(home, ".activate-studio")), os.FileMode(0o700), "the index folder is theirs alone")
	_, version := h.m.gufiBundle()
	own := filepath.Join(home, ".activate-studio", "gufi", version)
	if fi, err := os.Stat(filepath.Join(own, "bin", "gufi_query")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("their own GUFI: %v %v", err, fi)
	}
	if l, err := os.Readlink(filepath.Join(own, "lib", "libstandin.so.1")); err != nil || l != "libstandin.so" {
		t.Errorf("links in the bundle: %q %v", l, err)
	}
	got := h.waitFor(id)
	if got["state"] != "ready" {
		t.Fatalf("%v", got)
	}
	// What the build wrote (index, logs, records) is closed to every other
	// account, below the closed ~/.activate-studio too.
	filepath.WalkDir(filepath.Join(home, ".activate-studio"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 || strings.Contains(p, "/gufi/") {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Mode().Perm()&0o077 != 0 && !strings.Contains(p, "/indexes/") {
			t.Errorf("%s is %v", p, fi.Mode().Perm())
		} else if err == nil && !d.IsDir() && fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v", p, fi.Mode().Perm())
		}
		return nil
	})
	defs := h.m.Users.Load("single")
	if defs[0].Space == nil || defs[0].Build == nil || !strings.HasPrefix(defs[0].Build.GufiBin, own) {
		t.Fatalf("stored: %+v", defs[0])
	}
	again := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/studio"})
	match(t, again.Body["error"], `is already your library My library`, "once")

	// The Library's writes, as the person, in the space's folder.
	SpaceEvery = 50 * time.Millisecond
	t.Cleanup(func() { SpaceEvery = 20 * time.Second })
	s := h.m.SpaceFiles()
	lib := kb.Library{ID: id, Label: "My library", IndexRoot: libraries.RemoteRootOf("single", id), Space: true, Site: "cluster-a"}
	req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
	dir := filepath.Join(home, "studio")
	if err := s.Put(req, lib, "notes/a.txt", strings.NewReader("hello"), 100); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); string(b) != "hello" {
		t.Fatalf("written: %q", b)
	}
	if err := s.Put(req, lib, "notes/a.txt", strings.NewReader("bye"), 100); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); string(b) != "bye" {
		t.Fatalf("replaced: %q", b)
	}
	if err := s.Put(req, lib, "big.bin", bytes.NewReader(make([]byte, 101)), 100); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("over the limit: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".studio-upload-*"))
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil || len(left) > 0 {
		t.Fatalf("a refused upload left %v", left)
	}
	if err := s.Mkdir(req, lib, "docs"); err != nil {
		t.Fatal(err)
	}
	// A path that climbs out of the space is refused before it reaches the
	// cluster, the same way whether or not the name exists there.
	for _, p := range []string{"..", "../studio", "notes/../../x", "docs/.."} {
		if _, err := s.List(req, lib, p); err == nil || !strings.Contains(err.Error(), "leads outside") {
			t.Errorf("list %q: %v", p, err)
		}
	}
	if err := s.Mkdir(req, lib, "../escape"); err == nil {
		t.Error("a folder outside the space was made")
	}
	if _, err := os.Stat(filepath.Join(home, "escape")); err == nil {
		t.Error("escape exists")
	}
	if _, err := s.Rename(req, lib, "docs", "../moved"); err == nil {
		t.Error("a rename out of the space was taken")
	}
	list, err := s.List(req, lib, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, []any{len(list), list[0].Name, list[0].Type, list[1].Name}, []any{2, "docs", "dir", "notes"}, "listed")
	to, err := s.Rename(req, lib, "notes/a.txt", "b.txt")
	if err != nil || to != "notes/b.txt" {
		t.Fatalf("renamed: %q %v", to, err)
	}
	moved, skipped, err := s.Move(req, lib, []string{"notes/b.txt", "docs"}, "docs")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(moved), 1, "moved")
	eq(t, moved[0], kb.Moved{From: "notes/b.txt", To: "docs/b.txt"}, "moved")
	eq(t, len(skipped), 1, "a folder into itself")
	_, skipped, _ = s.Move(req, lib, []string{"docs/b.txt"}, "docs")
	if len(skipped) != 1 || skipped[0].Reason != "already there" {
		t.Fatalf("already there: %v", skipped)
	}
	os.WriteFile(filepath.Join(dir, "notes", "b.txt"), []byte("again"), 0o644)
	_, skipped, _ = s.Move(req, lib, []string{"notes/b.txt"}, "docs")
	if len(skipped) != 1 || skipped[0].Reason != "an item of that name is already there" {
		t.Fatalf("over another: %v", skipped)
	}
	deleted, skipped, err := s.Delete(req, lib, []string{"docs", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, []any{deleted, len(skipped)}, []any{[]string{"docs"}, 1}, "deleted")
	if _, err := os.Stat(filepath.Join(dir, "docs")); err == nil {
		t.Fatal("the folder is still there")
	}

	// A change builds the index again once things settle.
	// The index was built of an empty folder; notes/b.txt is there now.
	if got := h.waitFor(id); fmt.Sprint(got["files"]) != "0" {
		t.Fatalf("before: %v", got)
	}
	s.Changed(req, lib, []string{"notes"})
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.waitFor(id); fmt.Sprint(got["files"]) == "1" && got["state"] == "ready" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := h.waitFor(id); fmt.Sprint(got["files"]) != "1" {
		t.Fatalf("not indexed again: %v", got)
	}

	// A transfer that changed the space builds it again the same way; one
	// for a viewer whose space it is not changes nothing.
	os.WriteFile(filepath.Join(dir, "notes", "c.txt"), []byte("from a transfer"), 0o600)
	h.m.SpaceTransferred("someone-else", true, "k", lib.IndexRoot)
	h.m.mu.Lock()
	queued := h.m.rebuilds["single\x00"+id]
	h.m.mu.Unlock()
	if queued != nil {
		t.Fatal("another viewer's transfer queued a build")
	}
	h.m.SpaceTransferred("", false, "k", lib.IndexRoot)
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.waitFor(id); fmt.Sprint(got["files"]) == "2" && got["state"] == "ready" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := h.waitFor(id); fmt.Sprint(got["files"]) != "2" {
		t.Fatalf("not indexed after a transfer: %v", got)
	}
	if err := h.m.SpaceAccount(context.Background(), "", false, "k", lib.IndexRoot); err != nil {
		t.Fatalf("the space's own account: %v", err)
	}

	// A session that reaches another account than the space's is refused,
	// for the Library's writes and for a transfer's.
	h.m.Users.Update("single", id, func(d *libraries.UserDef) { d.Space.UID++ })
	h.m.accounts = nil
	if err := s.Mkdir(req, lib, "x"); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("another account: %v", err)
	}
	if err := h.m.SpaceAccount(context.Background(), "", false, "k", lib.IndexRoot); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("a transfer as another account: %v", err)
	}
	if err := h.m.SpaceAccount(context.Background(), "someone-else", true, "k", lib.IndexRoot); err != nil {
		t.Fatalf("not that viewer's space: %v", err)
	}
}

// TestSessionErrSaysToSignIn: a session the platform refused says to sign
// in with ACTIVATE, or to sign in again when the key came from the
// sign-in; any other failure keeps what pw ssh said.
func TestSessionErrSaysToSignIn(t *testing.T) {
	m := &Module{FromSignIn: func(k string) bool { return k == "from-sign-in" }}
	refused := errors.New("pw://alice/cluster-a: 2026-11-20T03:14:37Z [ERROR] Unauthorized")
	if got := m.sessionErr("cluster-a", "pasted", refused).Error(); !strings.Contains(got, "refused the credential") || !strings.Contains(got, "Sign in with ACTIVATE") {
		t.Errorf("a pasted key: %q", got)
	}
	if got := m.sessionErr("cluster-a", "from-sign-in", refused).Error(); !strings.Contains(got, "refused your ACTIVATE sign-in") || !strings.Contains(got, "sign in again") {
		t.Errorf("the sign-in: %q", got)
	}
	if got := m.sessionErr("cluster-a", "k", errors.New("connection reset")).Error(); got != "Your session to cluster-a did not open: connection reset" {
		t.Errorf("other: %q", got)
	}
}
