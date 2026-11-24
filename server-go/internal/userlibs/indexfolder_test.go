//go:build !windows

package userlibs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

func warned(r answer, name string) string {
	for _, s := range r.Body["steps"].([]any) {
		m := s.(map[string]any)
		if m["name"] == name && m["warn"] == true && m["ok"] == true {
			return m["detail"].(string)
		}
	}
	return ""
}

// A folder another account could write in, owns or could move is indexed
// and read as the person, with a warning that says the consequence and the
// fix, and is not written in: the check, the library, the Library's file
// work, sharing and a transfer all say so. Closing the folder and
// refreshing turns writing on; opening it again is caught by the write
// itself, before any refresh.
func TestAFolderOthersCouldChangeIsIndexedReadOnly(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home := homes["cluster-a"]
	open := filepath.Join(home, "open")
	write(t, filepath.Join(open, "mine", "a.txt"), "a")
	os.Chmod(open, 0o777)

	r := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	match(t, warned(r, "folder"), `^Every account there can write in .*/open \(mode 777\)\. It is indexed and read as you, and the Library does not write in it, .*run chmod go-w`, "warned")
	eq(t, []any{r.Body["owned"], r.Body["canShare"]}, []any{true, false}, "owned, not shared while open")
	match(t, r.Body["shareWhy"], `not shared until the warning above`, "why not shared")
	match(t, r.Body["myLibraryWhy"], `has to be a folder the Library writes in`, "not My library")

	// Under it, someone else could rename a folder of the person's away.
	p := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/mine"})
	eq(t, p.Body["ok"], true, "indexed")
	match(t, warned(p, "path"), `/open can be written by every account, so another account could move or replace a folder under it\..*does not write in it`, "unsafe path")
	// Said of a folder to be made there too, before it is made.
	n := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/new"})
	eq(t, []any{n.Body["ok"], n.Body["canShare"]}, []any{true, false}, "a new folder there")
	match(t, warned(n, "path"), `/open can be written by every account`, "a new folder there")
	os.Chmod(open, 0o777|os.ModeSticky)
	p = h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/mine"})
	if p.Body["ok"] != true || p.Body["readOnly"] != nil || p.Body["canShare"] != true {
		t.Fatalf("the sticky bit keeps others from renaming it: %v", p.Body)
	}
	os.Chmod(open, 0o777)

	// Another account's folder, root's.
	if os.Getuid() != 0 {
		o := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "/usr/share"})
		eq(t, []any{o.Body["ok"], o.Body["owned"], o.Body["canShare"]}, []any{true, false, false}, "root's folder")
		match(t, warned(o, "folder"), `belongs to another account \(uid 0\)\. It is indexed and read as you, and the Library does not write`, "owned by another")
		match(t, o.Body["shareWhy"], `only its owner can share it`, "why not shared")
	}

	for name, body := range map[string]map[string]any{
		"/space":    {"sourceRoot": "~/open", "depth": "names"},
		"/pw/build": {"cluster": "cluster-a", "sourceRoot": "~/open", "depth": "names", "indexRoot": "~/idx-two", "create": true},
	} {
		if name == "/pw/build" && len(gufiBinDir()) == 0 {
			continue
		}
		b := h.post("/api/user-libraries"+name, body)
		if b.Status != 200 {
			t.Fatalf("%s: %v", name, b.Body)
		}
		id := b.Body["id"].(string)
		got := h.waitFor(id)
		eq(t, []any{got["state"], got["mine"], got["space"]}, []any{"ready", nil, nil}, name+": indexed, not written")
		match(t, got["readOnlyWhy"], `^Every account there can write in .*, so it is read only here: .*run chmod go-w`, name)
		if a := got["access"].(map[string]any); a["write"] != false || a["readAs"] != "you" {
			t.Fatalf("%s: %v", name, a)
		}

		s := h.m.SpaceFiles()
		lib := kb.Library{ID: id, Label: "open", IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
		req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
		if err := s.Put(req, lib, "x.txt", strings.NewReader("x"), 10); err == nil || !strings.Contains(err.Error(), "read only here") {
			t.Fatalf("%s: written: %v", name, err)
		}
		if sh := h.post("/api/user-libraries/"+id+"/shares", map[string]any{"kind": "everyone"}); sh.Status != 409 || !strings.Contains(sh.Body["error"].(string), "read only here") {
			t.Fatalf("%s: shared: %v", name, sh.Body)
		}
		os.Chmod(open, 0o755)
		if rf := h.post("/api/user-libraries/"+id+"/refresh", nil); rf.Status != 200 {
			t.Fatalf("%s: %v", name, rf.Body)
		}
		got = h.waitFor(id)
		eq(t, []any{got["state"], got["mine"], got["readOnlyWhy"]}, []any{"ready", true, nil}, name+": written once closed")
		if err := s.Put(req, lib, name[1:]+"/x.txt", strings.NewReader("x"), 10); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		os.Chmod(open, 0o777)
		if err := s.Put(req, lib, "y.txt", strings.NewReader("y"), 10); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("%s: opened again: %v", name, err)
		}
		if _, _, err := s.Delete(req, lib, []string{"mine"}); err == nil {
			t.Fatalf("%s: deleted", name)
		}
		if _, err := os.Stat(filepath.Join(open, "y.txt")); err == nil {
			t.Fatalf("%s: written while open", name)
		}
		if b, _ := os.ReadFile(filepath.Join(open, "mine", "a.txt")); string(b) != "a" {
			t.Fatalf("%s: the file went", name)
		}
		h.post("/api/user-libraries/"+id+"/refresh", nil)
		match(t, h.waitFor(id)["readOnlyWhy"], `read only here`, name+": found again")
		if rm := h.do(http.MethodDelete, "/api/user-libraries/"+id, nil); rm.Status != 200 {
			t.Fatalf("%s: %v", name, rm.Body)
		}
	}
}

// On a Studio people sign in to, a folder is My library only when asked
// for, and only one the Library writes in; any other of the person's own
// is named by its folder, can be shared, and is not where chats are saved.
func TestAFolderOfOnesOwnBesideMyLibrary(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	h.m.Users.AuthEnabled = true
	h.auth = true
	h.user = &auth.User{ID: "ada", Username: "ada"}
	h.m.OwnerKey = func(owner, _ string) (string, bool) { return "key-" + owner, true }
	home := homes["cluster-a"]

	first := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/results", "create": true, "depth": "names", "myLibrary": false})
	if first.Status != 200 {
		t.Fatalf("%v", first.Body)
	}
	id := first.Body["id"].(string)
	got := h.waitFor(id)
	eq(t, []any{got["label"], got["space"], got["mine"]}, []any{"results", nil, true}, "named by its folder")
	if o, _ := got["own"].(map[string]any); o == nil || o["account"] == "" {
		t.Fatalf("own: %v", got["own"])
	}
	eq(t, perm(t, filepath.Join(home, "results")), os.FileMode(0o700), "made closed to other accounts")
	if site, folder := h.m.SpaceFolder("ada"); site != "" || folder != "" {
		t.Fatalf("a folder they only index is where pw code runs: %s %s", site, folder)
	}
	// What the Studio writes in it stays closed, as the folder is.
	s := h.m.SpaceFiles()
	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil), h.user)
	lib := kb.Library{ID: id, IndexRoot: libraries.RemoteRootOf("ada", id), Mine: true, Site: "cluster-a"}
	if err := s.Put(req, lib, "a.txt", strings.NewReader("a"), 10); err != nil {
		t.Fatal(err)
	}
	eq(t, perm(t, filepath.Join(home, "results", "a.txt")), os.FileMode(0o600), "closed as its folder is")

	my := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/studio", "create": true, "depth": "names", "myLibrary": true})
	if my.Status != 200 || my.Body["label"] != "My library" || my.Body["space"] == nil {
		t.Fatalf("%v", my.Body)
	}
	if _, folder := h.m.SpaceFolder("ada"); folder != filepath.Join(home, "studio") {
		t.Fatalf("My library: %s", folder)
	}
	// A request that does not say is My library, as it was.
	old := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/before", "create": true, "depth": "names"})
	if old.Status != 200 || old.Body["space"] == nil {
		t.Fatalf("%v", old.Body)
	}
	os.MkdirAll(filepath.Join(home, "loose"), 0o755)
	os.Chmod(filepath.Join(home, "loose"), 0o777)
	no := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/loose", "depth": "names", "myLibrary": true})
	if no.Status != 409 || !strings.Contains(no.Body["error"].(string), "My library is where your chats are saved") {
		t.Fatalf("%v", no.Body)
	}
	// A GUFI another account could change runs as the person: they may name
	// it for a folder they index, warned, and never for My library.
	theirs := filepath.Join(h.dir, "their-gufi")
	os.MkdirAll(theirs, 0o755)
	for _, tool := range []string{"gufi_query", "gufi_dir2index", "gufi_sqlite3"} {
		os.Symlink(filepath.Join(gufiBinDir(), tool), filepath.Join(theirs, tool))
	}
	os.Chmod(theirs, 0o777)
	g := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/fresh", "gufiBin": theirs})
	eq(t, g.Body["ok"], true, "a GUFI others can change")
	match(t, warned(g, "gufi"), `It can be written by every account, and it runs as you\.$`, "a GUFI others can change")
	match(t, g.Body["myLibraryWhy"], `a GUFI no other account can change`, "not for My library")
	if r := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/fresh", "create": true, "gufiBin": theirs, "myLibrary": true}); r.Status != 409 {
		t.Fatalf("My library with a GUFI others can change: %v", r.Body)
	}
	if r := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/fresh", "create": true, "indexRoot": "~/elsewhere", "myLibrary": true}); r.Status != 409 || !strings.Contains(r.Body["error"].(string), "keeps its index in ~/.activate-studio") {
		t.Fatalf("My library with its index elsewhere: %v", r.Body)
	}
	again := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/results", "myLibrary": false})
	match(t, again.Body["error"], `is already your library results`, "once")
}

// Shared, chosen when a folder is added, is set with the share code: an
// account or group to read or read and write, or every account to read.
// What cannot be shared is refused before anything is made; the owner's
// own writes go on with the share there; and it ends with the library.
func TestAddingAFolderShared(t *testing.T) {
	h, homes := buildHarness(t)
	needACLs(t, h.dir)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home := homes["cluster-a"]
	grp := func() string { out, _ := exec.Command("id", "-gn").Output(); return strings.TrimSpace(string(out)) }()
	add := func(folder string, share map[string]any) answer {
		return h.post("/api/user-libraries/space", map[string]any{"cluster": "cluster-a", "sourceRoot": folder, "create": true, "depth": "names", "share": share})
	}
	for said, share := range map[string]map[string]any{
		"Every account can be given read only": {"kind": "everyone", "access": "write"},
		"kind: user, group or everyone":        {"kind": "world"},
		"name: an account or group":            {"kind": "user", "name": "nobody;id"},
		"access: read or write":                {"kind": "user", "name": "nobody", "access": "own"},
	} {
		if r := add("~/refused", share); r.Status == 200 || !strings.Contains(r.Body["error"].(string), said) {
			t.Errorf("%v: %v", share, r.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "refused")); err == nil {
		t.Error("a refused share made the folder")
	}
	os.MkdirAll(filepath.Join(home, "loose"), 0o755)
	os.Chmod(filepath.Join(home, "loose"), 0o777)
	if r := add("~/loose", map[string]any{"kind": "everyone"}); r.Status != 409 {
		t.Fatalf("a folder others can write in was shared: %v", r.Body)
	}
	if r := h.post("/api/user-libraries/space", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/mine", "create": true, "share": map[string]any{"kind": "everyone"}}); r.Status != 409 || !strings.Contains(r.Body["error"].(string), "nobody here to share it with") {
		t.Fatalf("over ssh: %v", r.Body)
	}

	r := add("~/team", map[string]any{"kind": "group", "name": grp, "access": "write"})
	if r.Status != 200 || r.Body["shareError"] != nil {
		t.Fatalf("%v", r.Body)
	}
	id := r.Body["id"].(string)
	h.waitFor(id)
	if a := acl(t, filepath.Join(home, "team")); !strings.Contains(a, "group:"+grp+":rwx") {
		t.Fatalf("not set:\n%s", a)
	}
	shares := h.do(http.MethodGet, "/api/user-libraries/"+id+"/shares", nil).Body["shares"].([]any)
	if len(shares) != 1 || shares[0].(map[string]any)["access"] != "write" {
		t.Fatalf("%v", shares)
	}
	// The share's own entry is not another account's hold on the folder.
	s := h.m.SpaceFiles()
	lib := kb.Library{ID: id, IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
	req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
	if err := s.Put(req, lib, "a.txt", strings.NewReader("a"), 10); err != nil {
		t.Fatalf("the owner's write: %v", err)
	}
	h.post("/api/user-libraries/"+id+"/refresh", nil)
	if got := h.waitFor(id); got["readOnlyWhy"] != nil {
		t.Fatalf("read only for its own share: %v", got["readOnlyWhy"])
	}
	// Someone else's entry is.
	exec.Command("setfacl", "-m", "u:nobody:rwx", filepath.Join(home, "team")).Run()
	if err := s.Put(req, lib, "b.txt", strings.NewReader("b"), 10); err == nil || !strings.Contains(err.Error(), "by its ACL") {
		t.Fatalf("another account's entry: %v", err)
	}
	exec.Command("setfacl", "-x", "u:nobody", filepath.Join(home, "team")).Run()
	if rm := h.do(http.MethodDelete, "/api/user-libraries/"+id, nil); rm.Status != 200 {
		t.Fatalf("%v", rm.Body)
	}
	if a := acl(t, filepath.Join(home, "team")); strings.Contains(a, "group:"+grp) {
		t.Fatalf("the share outlived the library:\n%s", a)
	}

	// An account the system does not have: the library is there, and what
	// the share said comes with it.
	bad := add("~/lonely", map[string]any{"kind": "user", "name": "no-such-account-x"})
	if bad.Status != 200 || !strings.Contains(bad.Body["shareError"].(string), "There is no account no-such-account-x there") {
		t.Fatalf("%v", bad.Body)
	}
}
