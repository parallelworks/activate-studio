package userlibs

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// needACLs skips where setfacl is missing or the temporary folder's file
// system refuses ACLs.
func needACLs(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("no setfacl")
	}
	f := filepath.Join(dir, ".probe")
	os.WriteFile(f, nil, 0o600)
	defer os.Remove(f)
	if out, err := exec.Command("setfacl", "-m", "u:nobody:r", f).CombinedOutput(); err != nil {
		t.Skipf("no ACLs here: %s", out)
	}
}

func acl(t *testing.T, p string) string {
	t.Helper()
	out, err := exec.Command("getfacl", "-cpE", p).CombinedOutput()
	if err != nil {
		t.Fatalf("getfacl %s: %v %s", p, err, out)
	}
	return string(out)
}

func runShareSh(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"-c", shareSh, "_"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestShareSetsAndTakesBackACLs(t *testing.T) {
	base := t.TempDir()
	needACLs(t, base)
	space := filepath.Join(base, "space")
	sub := filepath.Join(space, "proj", "sub")
	os.MkdirAll(sub, 0o700)
	os.Chmod(space, 0o700)
	write(t, filepath.Join(sub, "a.txt"), "a")
	write(t, filepath.Join(space, "other.txt"), "o")
	proj := filepath.Join(space, "proj")
	// An ACL whose mask keeps the owning group out: sharing leaves it so.
	os.Chmod(proj, 0o750)
	exec.Command("setfacl", "-m", "u:root:r", proj).Run()
	os.Chmod(proj, 0o700)

	out, err := runShareSh(t, "", "apply", proj, "user", "nobody", "r", space)
	if err != nil || !strings.Contains(out, "shared") {
		t.Fatalf("apply: %v %s", err, out)
	}
	for p, want := range map[string]string{
		sub: "user:nobody:r-x", filepath.Join(sub, "a.txt"): "user:nobody:r--", proj: "default:user:nobody:r-x",
		space: "user:nobody:--x", base: "user:nobody:--x",
	} {
		if !strings.Contains(acl(t, p), want+"\n") {
			t.Errorf("%s: no %s in\n%s", p, want, acl(t, p))
		}
	}
	if strings.Contains(acl(t, filepath.Join(space, "other.txt")), "nobody") {
		t.Error("a file beside the shared folder was opened")
	}
	if g := acl(t, proj); !strings.Contains(g, "group::---\n") {
		t.Errorf("the owning group was widened:\n%s", g)
	}
	// A file made in the shared folder later takes the default ACL.
	write(t, filepath.Join(sub, "new.txt"), "n")
	if !strings.Contains(acl(t, filepath.Join(sub, "new.txt")), "user:nobody:r") {
		t.Error("a new file did not inherit the share")
	}

	// Taken back, with another share of theirs keeping space reachable.
	if out, err := runShareSh(t, space+"\n"+base, "revoke", proj, "user", "nobody", "r", space); err != nil {
		t.Fatalf("revoke: %v %s", err, out)
	}
	if strings.Contains(acl(t, sub), "nobody") || strings.Contains(acl(t, proj), "nobody") {
		t.Errorf("left after the revoke:\n%s", acl(t, sub))
	}
	if !strings.Contains(acl(t, space), "user:nobody:--x") {
		t.Error("a folder another share needs was closed")
	}
	runShareSh(t, "", "revoke", proj, "user", "nobody", "r", space)
	if strings.Contains(acl(t, space), "nobody") || strings.Contains(acl(t, base), "nobody") {
		t.Error("the way in stayed open with no share needing it")
	}

	if out, err := runShareSh(t, "", "apply", proj, "user", "no-such-account-x", "r", space); err == nil || !strings.Contains(out, "There is no account no-such-account-x there.") {
		t.Fatalf("unknown account: %v %s", err, out)
	}
	if out, err := runShareSh(t, "", "apply", base, "user", "nobody", "r", space); err == nil || !strings.Contains(out, "is not in the space") {
		t.Fatalf("outside the space: %v %s", err, out)
	}
	if out, err := runShareSh(t, "", "apply", proj, "everyone", "", "r", space); err != nil {
		t.Fatalf("everyone: %v %s", err, out)
	}
	if perm(t, filepath.Join(sub, "a.txt"))&0o004 == 0 || perm(t, space)&0o001 == 0 {
		t.Error("every account cannot read it")
	}
	if perm(t, filepath.Join(space, "other.txt"))&0o004 != 0 && perm(t, space)&0o004 != 0 {
		t.Error("the space was opened for listing")
	}
}

// Two people on one Studio: one shares a folder of their space with a
// group the other belongs to, for writing; the other adds it, indexes it
// as themselves, and writes in it; taking the share back removes their
// library and their index of it.
func TestSharingAFolderWithAnotherPerson(t *testing.T) {
	h, homes := buildHarness(t)
	needACLs(t, h.dir)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	SpaceEvery = 50 * time.Millisecond
	t.Cleanup(func() { SpaceEvery = 20 * time.Second })
	home := homes["cluster-a"]
	h.auth = true
	var mu sync.Mutex
	keys := map[string]string{"ada": "key-ada", "bob": "key-bob"}
	h.m.OwnerKey = func(owner, _ string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		k, ok := keys[owner]
		return k, ok
	}
	ada := &auth.User{ID: "ada", Username: "ada"}
	bob := &auth.User{ID: "bob", Username: "bob"}
	group, _ := exec.Command("id", "-gn").Output()
	grp := strings.TrimSpace(string(group))
	account, _ := exec.Command("id", "-un").Output()

	h.user = ada
	sp := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/studio", "create": true, "depth": "names"})
	if sp.Status != 200 {
		t.Fatalf("%v", sp.Body)
	}
	space := sp.Body["id"].(string)
	h.waitFor(space)
	write(t, filepath.Join(home, "studio", "proj", "plan.md"), "# plan\n")
	if r := h.post("/api/user-libraries/"+space+"/shares", map[string]any{"path": "proj", "kind": "user", "name": strings.TrimSpace(string(account))}); r.Status != 400 {
		t.Fatalf("with oneself: %v", r.Body)
	}
	sh := h.post("/api/user-libraries/"+space+"/shares", map[string]any{"path": "proj", "kind": "group", "name": grp, "access": "write"})
	if sh.Status != 200 {
		t.Fatalf("share: %v", sh.Body)
	}
	if !strings.Contains(acl(t, filepath.Join(home, "studio", "proj")), "group:"+grp+":rwx") {
		t.Fatalf("not set:\n%s", acl(t, filepath.Join(home, "studio", "proj")))
	}
	if l := h.do(http.MethodGet, "/api/user-libraries/"+space+"/shares", nil); len(l.Body["shares"].([]any)) != 1 {
		t.Fatalf("listed: %v", l.Body)
	}
	if mine := h.do(http.MethodGet, "/api/user-libraries/shared-with-me", nil); len(mine.Body["shares"].([]any)) != 0 {
		t.Fatalf("one's own shares are not shared with one: %v", mine.Body)
	}

	h.user = bob
	got := h.do(http.MethodGet, "/api/user-libraries/shared-with-me", nil)
	list, _ := got.Body["shares"].([]any)
	if len(list) != 1 {
		t.Fatalf("shared with bob: %v", got.Body)
	}
	share := list[0].(map[string]any)
	eq(t, []any{share["label"], share["access"], share["path"]}, []any{"proj", "write", "proj"}, "the share")
	add := h.post("/api/user-libraries/shared-with-me/"+share["id"].(string)+"/add", map[string]any{"depth": "names"})
	if add.Status != 200 {
		t.Fatalf("add: %v", add.Body)
	}
	lib := add.Body["id"].(string)
	if s, _ := add.Body["shared"].(map[string]any); s == nil || s["access"] != "write" {
		t.Fatalf("shared: %v", add.Body["shared"])
	}
	built := h.waitFor(lib)
	eq(t, []any{built["state"], fmt.Sprint(built["files"])}, []any{"ready", "1"}, "indexed as bob")
	if again := h.post("/api/user-libraries/shared-with-me/"+share["id"].(string)+"/add", nil); again.Status != 409 {
		t.Fatalf("twice: %v", again.Body)
	}

	// Bob writes in it, as himself; ada's space is indexed again.
	s := h.m.SpaceFiles()
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil), bob)
	klib := kb.Library{ID: lib, IndexRoot: libraries.RemoteRootOf("bob", lib), Space: true}
	if err := s.Put(req, klib, "notes.md", strings.NewReader("from bob"), 100); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "studio", "proj", "notes.md")); string(b) != "from bob" {
		t.Fatalf("written: %q", b)
	}
	s.Changed(req, klib, []string{"notes.md"})
	h.user = ada
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && fmt.Sprint(h.waitFor(space)["files"]) != "2" {
		time.Sleep(100 * time.Millisecond)
	}
	eq(t, fmt.Sprint(h.waitFor(space)["files"]), "2", "the owner's index caught up")

	// Taken back: bob's library and his index of it go.
	h.user = bob
	indexDir := ""
	for _, d := range h.m.Users.Load("bob") {
		if d.ID == lib && d.Build != nil {
			indexDir = d.Build.IndexDir
		}
	}
	h.user = ada
	if r := h.do(http.MethodDelete, "/api/user-libraries/"+space+"/shares/"+share["id"].(string), nil); r.Status != 200 {
		t.Fatalf("revoke: %v", r.Body)
	}
	if strings.Contains(acl(t, filepath.Join(home, "studio", "proj")), "group:"+grp) {
		t.Fatalf("still set:\n%s", acl(t, filepath.Join(home, "studio", "proj")))
	}
	if d := find(h.m.load("bob"), lib); d != nil {
		t.Fatal("bob's library of the share is still there")
	}
	if _, err := os.Stat(indexDir); indexDir == "" || err == nil {
		t.Fatalf("bob's index of it is still there: %q", indexDir)
	}
	h.user = bob
	if l := h.do(http.MethodGet, "/api/user-libraries/shared-with-me", nil); len(l.Body["shares"].([]any)) != 0 {
		t.Fatalf("after the revoke: %v", l.Body)
	}
}
