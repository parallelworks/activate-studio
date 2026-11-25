//go:build linux

package userlibs

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// looseLib is a library of a folder every account can write in, in a home
// with a private folder beside it.
func looseLib(t *testing.T) (h *harness, home, id string, s kb.SpaceFS, lib kb.Library, req *http.Request) {
	t.Helper()
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home = homes["cluster-a"]
	write(t, filepath.Join(home, "open", "mine", "a.txt"), "a")
	write(t, filepath.Join(home, "private", "a.txt"), "private")
	os.Chmod(filepath.Join(home, "open"), 0o777)
	b := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/open", "depth": "names"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	id = b.Body["id"].(string)
	h.waitFor(id)
	SpaceEvery = time.Hour
	t.Cleanup(func() { SpaceEvery = 20 * time.Second })
	return h, home, id, h.m.SpaceFiles(),
		kb.Library{ID: id, Label: "open", IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"},
		httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
}

// A folder another account can write in is written in, with a warning that
// says who can change it, by every kind of write; a link put in it, or in
// its place, takes none of them elsewhere.
func TestAFolderOthersCanWriteInIsWritten(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	home := homes["cluster-a"]
	open := filepath.Join(home, "open")
	write(t, filepath.Join(open, "mine", "a.txt"), "a")
	os.Chmod(open, 0o777)
	r := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open"})
	match(t, warned(r, "folder"), `^Every account there can write in .*/open \(mode 777\)\. They can add, change and remove what is in it\. The Library writes in it as you, and a write cannot be sent outside it\. .*run chmod go-w`, "warned")
	eq(t, []any{r.Body["ok"], r.Body["canShare"], r.Body["readOnly"]}, []any{true, true, nil}, "written and shared")
	match(t, r.Body["myLibraryWhy"], `a folder no other account can change`, "not My library")
	os.Chmod(open, 0o775)
	r = h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open"})
	if w := warned(r, "folder"); !strings.Contains(w, "Members of the group") && !strings.Contains(w, "Other accounts can open") {
		t.Errorf("a group's folder: %q", w)
	}
	os.Chmod(open, 0o777)
	p := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/open/mine"})
	match(t, warned(p, "path"), `/open can be written by every account, so another account could move or replace a folder under it\. If this folder is replaced, the Library stops writing until you add it again\.`, "unsafe path")
	if os.Getuid() != 0 {
		o := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "/usr/share"})
		eq(t, []any{o.Body["ok"], o.Body["readOnly"], o.Body["canShare"]}, []any{true, nil, false}, "root's folder")
		match(t, warned(o, "folder"), `^/usr/share belongs to another account \(uid 0\)\. .*Your account cannot write in the folder itself, so the Library writes only in what it holds`, "owned by another")
	}
}

func TestEveryWriteInAFolderOthersCanWriteIn(t *testing.T) {
	h, home, id, s, lib, req := looseLib(t)
	open, private := filepath.Join(home, "open"), filepath.Join(home, "private")
	got := h.library(id)
	eq(t, []any{got["mine"], got["readOnlyWhy"]}, []any{true, nil}, "written")
	match(t, got["othersWrite"], `^Every account there can write in .*/open \(mode 777\)\.$`, "who else can")
	if d := find(h.m.load("single"), id); d.Pin == "" || d.Loose == nil || d.ReadOnly != nil {
		t.Fatalf("kept: %+v", d)
	}

	put := func(rel string) error { return s.Put(req, lib, rel, strings.NewReader("x"), 100) }
	if err := put("new/x.txt"); err != nil {
		t.Fatal(err)
	}
	if err := put("mine/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir(req, lib, "made"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename(req, lib, "new/x.txt", "y.txt"); err != nil {
		t.Fatal(err)
	}
	if m, sk, err := s.Move(req, lib, []string{"new/y.txt"}, "made"); err != nil || len(m) != 1 {
		t.Fatalf("%v %v %v", m, sk, err)
	}
	if d, _, err := s.Delete(req, lib, []string{"mine"}); err != nil || len(d) != 1 {
		t.Fatalf("%v %v", d, err)
	}
	if st, err := os.Stat(filepath.Join(home, ".activate-studio", "write")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the helper: %v", err)
	}

	// Links planted in it.
	os.Symlink(private, filepath.Join(open, "door"))
	os.Symlink(filepath.Join(private, "a.txt"), filepath.Join(open, "made", "l.txt"))
	if err := put("door/x.txt"); err == nil || !strings.Contains(err.Error(), "goes through a link") {
		t.Errorf("through a link: %v", err)
	}
	put("made/l.txt")
	s.Mkdir(req, lib, "door/sub")
	s.Rename(req, lib, "door/a.txt", "b.txt")
	s.Move(req, lib, []string{"made/y.txt"}, "door")
	s.Delete(req, lib, []string{"door/a.txt"})
	if b, err := s.Get(req, lib, "door/a.txt", 100); err == nil {
		t.Errorf("read through a link: %q", b)
	}

	if es, _ := os.ReadDir(private); len(es) != 1 || perm(t, filepath.Join(private, "a.txt")) != 0o644 {
		t.Errorf("outside: %v", es)
	}
	if b, _ := os.ReadFile(filepath.Join(private, "a.txt")); string(b) != "private" {
		t.Errorf("outside: %q", b)
	}
}

// The folder itself replaced, under one another account can write in: by a
// link, by a folder of the person's own moved there, and by the folder above
// it being a link. Nothing is written, with the helper or without.
func TestTheLibrarysFolderReplaced(t *testing.T) {
	for _, why := range []string{"", "none here"} {
		h, homes := buildHarness(t)
		t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
		h.m.GufiBin = fakeBundle(t, gufiBinDir())
		private := filepath.Join(homes["cluster-a"], "private")
		pub := filepath.Join(h.dir, "pub")
		team := filepath.Join(pub, "team")
		open := filepath.Join(team, "open")
		write(t, filepath.Join(private, "open", "a.txt"), "private")
		write(t, filepath.Join(private, "a.txt"), "private")
		write(t, filepath.Join(open, "a.txt"), "a")
		os.Chmod(pub, 0o777)
		os.Chmod(team, 0o777)
		os.Chmod(private, 0o700)
		b := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": open, "depth": "names"})
		if b.Status != 200 {
			t.Fatalf("%v", b.Body)
		}
		id := b.Body["id"].(string)
		h.waitFor(id)
		s := h.m.SpaceFiles()
		lib := kb.Library{ID: id, Label: "open", IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
		req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
		if err := s.Mkdir(req, lib, "one"); err != nil {
			t.Fatal(err)
		}
		pin := find(h.m.load("single"), id).Pin
		h.m.toolsMu.Lock()
		h.m.noWriteTool, h.m.tools = why, nil
		h.m.toolsMu.Unlock()
		before := func() string { out, _ := exec.Command("ls", "-lanR", private).Output(); return string(out) }
		was := before()
		os.Rename(team, team+".was")
		os.Mkdir(team, 0o777)
		os.Chmod(team, 0o777)
		for name, swap := range map[string]func(){
			"a link":           func() { os.Symlink(private, open) },
			"their own folder": func() { os.Remove(open); os.Mkdir(open, 0o755) },
			"a link above it":  func() { os.Remove(open); os.Remove(team); os.Symlink(private, team) },
		} {
			os.Remove(open)
			if fi, err := os.Lstat(team); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(team)
				os.Mkdir(team, 0o777)
				os.Chmod(team, 0o777)
			}
			swap()
			for op, err := range map[string]error{
				"put":   s.Put(req, lib, "z.txt", strings.NewReader("z"), 10),
				"mkdir": s.Mkdir(req, lib, "z"),
				"delete": func() error {
					_, _, err := s.Delete(req, lib, []string{"a.txt"})
					return err
				}(),
			} {
				if err == nil {
					t.Errorf("%q, %s, %s: taken", why, name, op)
				}
			}
			h.m.recheck(t.Context(), "single", find(h.m.load("single"), id), "cluster-a", "")
		}
		if now := before(); now != was {
			t.Errorf("%q: outside changed:\n%s\nwas\n%s", why, now, was)
		}
		if now := find(h.m.load("single"), id).Pin; now != pin {
			t.Errorf("%q: pinned to %s, was %s", why, now, pin)
		}
	}
}

// A folder the person made again is taken where no other account could
// have put it there.
func TestAFolderMadeAgain(t *testing.T) {
	h, home, id, s, lib, req := looseLib(t)
	open := filepath.Join(home, "open")
	put := func(rel string) error { return s.Put(req, lib, rel, strings.NewReader("x"), 100) }
	if err := put("one.txt"); err != nil {
		t.Fatal(err)
	}
	was := find(h.m.load("single"), id).Pin
	os.Rename(open, open+".was")
	os.Mkdir(open, 0o777)
	if err := s.Mkdir(req, lib, "two"); err != nil {
		t.Fatal(err)
	}
	if now := find(h.m.load("single"), id).Pin; now == was {
		t.Error("the person's own new folder was not taken")
	}
}

// What an earlier version kept as read only is written again, and older
// libraries get the same check on their first write.
func TestOlderLibrariesAreWrittenThroughTheHelper(t *testing.T) {
	h, home, id, s, lib, req := looseLib(t)
	was := "Every account there can write in /x (mode 777), so it is read only here: another account could send what the Studio writes there elsewhere. To write in it here, run chmod go-w on it."
	h.m.Users.Update("single", id, func(d *libraries.UserDef) { d.Before, d.Loose, d.Pin = &was, nil, "" })
	got := h.library(id)
	eq(t, []any{got["mine"], got["readOnlyWhy"], got["othersWrite"]}, []any{true, nil, "Every account there can write in /x (mode 777)."}, "kept read only before")
	if err := s.Put(req, lib, "x.txt", strings.NewReader("x"), 100); err != nil {
		t.Fatal(err)
	}
	h.m.Users.Update("single", id, func(d *libraries.UserDef) { d.Guard, d.Loose, d.Own, d.Pin = false, nil, nil, "" })
	h.m.toolsMu.Lock()
	h.m.tools = nil
	h.m.toolsMu.Unlock()
	if err := s.Put(req, lib, "y.txt", strings.NewReader("y"), 100); err != nil {
		t.Fatal(err)
	}
	for range 200 {
		if d := find(h.m.load("single"), id); d.Guard && d.Loose != nil && d.Pin != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := find(h.m.load("single"), id); !d.Guard || d.Loose == nil || d.Pin == "" || d.Own == nil {
		t.Fatalf("not checked: %+v", d)
	}
	// With no helper for its system, the same library is read only, and a
	// write is refused before that is recorded.
	h.m.toolsMu.Lock()
	h.m.tools, h.m.noWriteTool = nil, "The Studio's write helper is not built for Plan9."
	h.m.toolsMu.Unlock()
	if err := s.Put(req, lib, "z.txt", strings.NewReader("z"), 100); err == nil || !strings.Contains(err.Error(), "not built for Plan9") {
		t.Fatalf("%v", err)
	}
	for range 200 {
		if d := find(h.m.load("single"), id); d.ReadOnly != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got = h.library(id)
	match(t, got["readOnlyWhy"], `so it is read only here: .*not built for Plan9`, "found read only")
	if _, err := os.Stat(filepath.Join(home, "open", "z.txt")); err == nil {
		t.Error("written")
	}
	// And written again once it can run.
	h.m.toolsMu.Lock()
	h.m.tools, h.m.noWriteTool = nil, ""
	h.m.toolsMu.Unlock()
	h.post("/api/user-libraries/"+id+"/refresh", nil)
	eq(t, h.waitFor(id)["readOnlyWhy"], nil, "once it can run")
}

// The helper runs from a folder no other account can change, or not at all.
func TestAHelperAnotherAccountCouldReplaceIsNotRun(t *testing.T) {
	h, home, id, s, lib, req := looseLib(t)
	if err := s.Mkdir(req, lib, "one"); err != nil {
		t.Fatal(err)
	}
	os.Chmod(home, 0o777)
	defer os.Chmod(home, 0o755)
	h.m.toolsMu.Lock()
	h.m.tools = nil
	h.m.toolsMu.Unlock()
	err := s.Mkdir(req, lib, "two")
	if err == nil || !strings.Contains(err.Error(), "runs from your ~/.activate-studio") {
		t.Fatalf("%s: %v", id, err)
	}
	// The check says so, and a rebuild has the library say so too.
	r := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": filepath.Join(home, "open")})
	match(t, r.Body["readOnly"], `so it is read only here: .*runs from your ~/\.activate-studio on .*can be written by every account`, "the check")
	h.post("/api/user-libraries/"+id+"/refresh", nil)
	match(t, h.waitFor(id)["readOnlyWhy"], `runs from your ~/\.activate-studio`, "the library")
	// Closed again, a rebuild has it written, without waiting.
	if err := s.Mkdir(req, lib, "three"); err == nil {
		t.Fatal("made")
	}
	os.Chmod(home, 0o755)
	h.post("/api/user-libraries/"+id+"/refresh", nil)
	eq(t, h.waitFor(id)["readOnlyWhy"], nil, "closed again")
	if err := s.Mkdir(req, lib, "four"); err != nil {
		t.Fatal(err)
	}
}

// While another account swaps a folder of the library for a link as fast
// as it can, every kind of write through the Library stays in it.
func TestLibraryWritesUnderARace(t *testing.T) {
	_, home, _, s, lib, req := looseLib(t)
	open, private := filepath.Join(home, "open"), filepath.Join(home, "private")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		os.Symlink(private, filepath.Join(open, "trap"))
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Rename(filepath.Join(open, "mine"), filepath.Join(open, "away"))
			os.Rename(filepath.Join(open, "trap"), filepath.Join(open, "mine"))
			time.Sleep(200 * time.Microsecond)
			os.Rename(filepath.Join(open, "mine"), filepath.Join(open, "trap"))
			os.Rename(filepath.Join(open, "away"), filepath.Join(open, "mine"))
			time.Sleep(200 * time.Microsecond)
		}
	}()
	refused, took := 0, 0
	for i := range 40 {
		rel := fmt.Sprintf("mine/f%d.txt", i)
		for _, err := range []error{
			s.Put(req, lib, rel, strings.NewReader("x"), 100),
			s.Put(req, lib, "mine/a.txt", strings.NewReader("y"), 100),
			s.Mkdir(req, lib, fmt.Sprintf("mine/d%d", i)),
			func() error { _, err := s.Rename(req, lib, rel, "r"+filepath.Base(rel)); return err }(),
			func() error { _, _, err := s.Delete(req, lib, []string{"mine/a.txt"}); return err }(),
		} {
			if err != nil {
				refused++
			} else {
				took++
			}
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("%d writes taken, %d refused", took, refused)
	st, err := os.Stat(filepath.Join(private, "a.txt"))
	if err != nil || st.Mode().Perm() != 0o644 || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Fatalf("outside: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(private, "a.txt")); string(b) != "private" {
		t.Errorf("outside: %q", b)
	}
	if es, _ := os.ReadDir(private); len(es) != 1 {
		t.Errorf("outside: %v", es)
	}
	if refused == 0 || took == 0 {
		t.Error("the race did not meet")
	}
}

// Sharing a folder others can write in changes what the owner has in it,
// and nothing a link in it, or a file with another name elsewhere, leads to.
func TestSharingAFolderOthersCanWriteIn(t *testing.T) {
	h, home, id, _, _, _ := looseLib(t)
	needACLs(t, h.dir)
	open, private := filepath.Join(home, "open"), filepath.Join(home, "private")
	os.Chmod(private, 0o700)
	os.Chmod(filepath.Join(private, "a.txt"), 0o600)
	write(t, filepath.Join(private, "b.txt"), "b")
	os.Chmod(filepath.Join(private, "b.txt"), 0o600)
	os.Symlink(private, filepath.Join(open, "door"))
	os.Symlink(filepath.Join(private, "a.txt"), filepath.Join(open, "mine", "l.txt"))
	os.Link(filepath.Join(private, "b.txt"), filepath.Join(open, "hard.txt"))
	os.Chmod(filepath.Join(open, "mine"), 0o700)
	os.Chmod(filepath.Join(open, "mine", "a.txt"), 0o600)
	before := acl(t, filepath.Join(open, "mine"))

	grp := func() string { out, _ := exec.Command("id", "-gn").Output(); return strings.TrimSpace(string(out)) }()
	for _, c := range []struct {
		share map[string]any
		file  string
	}{
		{map[string]any{"kind": "everyone"}, "other::r--"},
		{map[string]any{"kind": "user", "name": "nobody", "access": "write"}, "user:nobody:rw-"},
		{map[string]any{"kind": "group", "name": grp}, "group:" + grp + ":r--"},
	} {
		if sh := h.post("/api/user-libraries/"+id+"/shares", c.share); sh.Status != 200 {
			t.Fatalf("%v: %v", c.share, sh.Body)
		}
		if a := acl(t, filepath.Join(open, "mine", "a.txt")); !strings.Contains(a, c.file) {
			t.Errorf("%v: not set:\n%s", c.share, a)
		}
		for _, p := range []string{"", "a.txt", "b.txt"} {
			if a := acl(t, filepath.Join(private, p)); strings.Contains(a, "nobody") || strings.Contains(a, "group:"+grp) || !strings.Contains(a, "other::---") {
				t.Errorf("%v: outside changed, %s:\n%s", c.share, p, a)
			}
		}
		list := h.do(http.MethodGet, "/api/user-libraries/"+id+"/shares", nil).Body["shares"].([]any)
		sid := list[0].(map[string]any)["id"].(string)
		if rm := h.do(http.MethodDelete, "/api/user-libraries/"+id+"/shares/"+sid, nil); rm.Status != 200 {
			t.Fatalf("%v: %v", c.share, rm.Body)
		}
		if c.share["kind"] != "everyone" {
			if a := acl(t, filepath.Join(open, "mine")); a != before {
				t.Errorf("%v: not as it was:\n%s\nwas\n%s", c.share, a, before)
			}
		} else {
			os.Chmod(filepath.Join(open, "mine"), 0o700)
		}
		os.Chmod(open, 0o777)
	}
}

// With the helper, text is opened, made and saved there, through no link;
// in another folder put in its place it is not, and that is the answer.
func TestTextInAFolderOthersCanWriteIn(t *testing.T) {
	h, home, id, sf, lib, req := looseLib(t)
	s := sf.(kb.TextFS)
	open := filepath.Join(home, "open")
	if err := s.CreateText(req, lib, "new.md", []byte("a")); err != nil {
		t.Fatal(id, err)
	}
	if b, err := s.OpenText(req, lib, "new.md", 100); err != nil || string(b) != "a" {
		t.Fatalf("%q %v", b, err)
	}
	if err := s.SaveText(req, lib, "new.md", []byte("b"), kb.TextVersion([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	var c *kb.TextConflict
	if err := s.SaveText(req, lib, "new.md", []byte("c"), kb.TextVersion([]byte("a"))); !errors.As(err, &c) || c.State != kb.TextChanged || c.Version != kb.TextVersion([]byte("b")) {
		t.Fatalf("out of date: %v", err)
	}
	if err := s.CreateText(req, lib, "new.md", nil); !errors.As(err, &c) || c.State != kb.TextExists {
		t.Fatalf("taken: %v", err)
	}
	private := filepath.Join(home, "private")
	write(t, filepath.Join(private, "new.md"), "mine")
	os.Symlink(private, filepath.Join(open, "door"))
	os.Symlink(filepath.Join(private, "new.md"), filepath.Join(open, "link.md"))
	refused := func(where string, rel string) {
		t.Helper()
		_, opened := s.OpenText(req, lib, rel, 100)
		for op, err := range map[string]error{
			"open": opened,
			"save": s.SaveText(req, lib, rel, []byte("x"), kb.TextVersion([]byte("mine"))),
			"make": s.CreateText(req, lib, filepath.Join(filepath.Dir(rel), "made.md"), []byte("x")),
		} {
			if op == "make" && rel == "link.md" {
				os.Remove(filepath.Join(open, "made.md"))
				continue
			}
			if err == nil || errors.As(err, &c) {
				t.Errorf("%s, %s: %v", where, op, err)
			}
		}
	}
	refused("through a link", "door/new.md")
	refused("a link", "link.md")
	// One under a folder others can write in, where another account can put
	// a link in its place.
	team := filepath.Join(home, "team")
	write(t, filepath.Join(team, "open", "new.md"), "mine")
	os.Chmod(team, 0o777)
	b := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/team/open", "depth": "names"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	id = b.Body["id"].(string)
	h.waitFor(id)
	lib.ID, lib.IndexRoot = id, libraries.RemoteRootOf("single", id)
	if err := s.SaveText(req, lib, "new.md", []byte("mine"), kb.TextVersion([]byte("mine"))); err != nil {
		t.Fatal(err)
	}
	os.Rename(filepath.Join(team, "open"), filepath.Join(team, "was"))
	os.Symlink(private, filepath.Join(team, "open"))
	refused("the folder swapped for a link", "new.md")
	es, _ := os.ReadDir(private)
	if b, _ := os.ReadFile(filepath.Join(private, "new.md")); string(b) != "mine" || len(es) != 2 {
		t.Fatalf("%q %v", b, es)
	}
}
