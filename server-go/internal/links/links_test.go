package links

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func f(ino string) Same { return Same{Dev: "1", Ino: ino, Birth: "b" + ino} }

func make1(t *testing.T, s *Store, owner, lib, rel string, same Same) Record {
	t.Helper()
	r, err := s.Make(Place{owner, lib, rel}, false, same, "ada")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func at(t *testing.T, s *Store, id string) Record {
	t.Helper()
	r, ok, err := s.Get(id)
	if err != nil || !ok {
		t.Fatalf("%s: %v %v", id, ok, err)
	}
	return r
}

func TestAnIDFollowsItsItem(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	a := make1(t, s, "", "kb", "plans/budget.md", f("10"))
	if !ValidID(a.ID) || strings.Contains(a.ID, ".") {
		t.Fatalf("id %q", a.ID)
	}
	if again := make1(t, s, "", "kb", "plans/budget.md", f("10")); again.ID != a.ID {
		t.Error("one item has one ID")
	}
	for _, other := range []Place{{"", "kb", "plans/budget.md2"}, {"ada", "kb", "plans/budget.md"}, {"", "docs", "plans/budget.md"}} {
		if make1(t, s, other.Owner, other.Lib, other.Rel, f("11")).ID == a.ID {
			t.Errorf("%v has the same ID", other)
		}
	}
	// A rename of it, of a folder above it, and of one beside it.
	s.Moved("", "kb", "plans/budget.md", "plans/2027.md")
	s.Moved("", "kb", "plans", "archive/plans")
	s.Moved("", "kb", "archive/pla", "x")
	if got := at(t, s, a.ID); got.Rel != "archive/plans/2027.md" || got.Deleted != "" {
		t.Errorf("%+v", got)
	}
	// Kept over a start.
	s = New(dir)
	if got := at(t, s, a.ID); got.Rel != "archive/plans/2027.md" || !got.Same.Is(f("10")) {
		t.Errorf("after a start: %+v", got)
	}
	// Written anew through the Studio, it goes on; by anything else it does
	// not, and a link made then is another item's.
	p := Place{"", "kb", "archive/plans/2027.md"}
	s.Rewrote(p, f("10"), f("20"))
	if got := at(t, s, a.ID); got.Same != f("20") {
		t.Errorf("rewrote: %+v", got)
	}
	s.Rewrote(p, f("99"), f("30"))
	if got := at(t, s, a.ID); got.Same != f("20") {
		t.Errorf("another file's: %+v", got)
	}
	b := make1(t, s, "", "kb", "archive/plans/2027.md", f("30"))
	if b.ID == a.ID {
		t.Error("another file at the path has its ID")
	}
	// Deleted with the folder it is in; what comes there later is another.
	s.Deleted("", "kb", "archive")
	if got := at(t, s, a.ID); got.Deleted == "" {
		t.Error("not ended")
	}
	if c := make1(t, s, "", "kb", "archive/plans/2027.md", f("20")); c.ID == a.ID || c.ID == b.ID {
		t.Error("a deleted item's ID is given again")
	}
	// A move does not go over something, so what had an ID there is gone.
	d := make1(t, s, "", "kb", "d.md", f("40"))
	make1(t, s, "", "kb", "e.md", f("41"))
	s.Moved("", "kb", "e.md", "d.md")
	if got := at(t, s, d.ID); got.Deleted == "" {
		t.Error("an item moved over is still there")
	}
	s.DropLibrary("", "kb")
	if _, ok, _ := s.Get(d.ID); ok {
		t.Error("a removed library's ID is kept")
	}
	if st, _ := os.Stat(filepath.Join(dir, "links.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("links.json is %v", st.Mode())
	}
}

func TestSame(t *testing.T) {
	for _, c := range []struct {
		a, b Same
		want bool
	}{
		{Same{"1", "5", "x"}, Same{"1", "5", "x"}, true},
		{Same{"1", "5", "x"}, Same{"1", "6", "x"}, false},
		// An inode used again for another file.
		{Same{"1", "5", "x"}, Same{"1", "5", "y"}, false},
		// The same file system on another host, or mounted again.
		{Same{"1", "5", "x"}, Same{"2", "5", "x"}, true},
		{Same{"1", "5", ""}, Same{"2", "5", "x"}, true},
		{Same{}, Same{"1", "5", "x"}, true},
	} {
		if got := c.a.Is(c.b); got != c.want {
			t.Errorf("%v %v: %v", c.a, c.b, got)
		}
	}
}

func TestManyPeopleMakeLinksAtOnce(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	ids := make([]string, 60)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Make(Place{"", "kb", fmt.Sprint("f", i%20)}, false, f(fmt.Sprint(i%20)), fmt.Sprint("p", i))
			if err != nil {
				t.Error(err)
			}
			ids[i] = r.ID
			s.Moved("", "kb", fmt.Sprint("none", i), "x")
		}()
	}
	wg.Wait()
	for i, id := range ids {
		if id != ids[i%20] {
			t.Errorf("%d: two IDs for one item", i)
		}
	}
	// Whole on the disk, with nothing left beside it.
	var got file
	b, _ := os.ReadFile(filepath.Join(dir, "links.json"))
	if json.Unmarshal(b, &got) != nil || len(got.Links) != 20 {
		t.Errorf("%d links kept", len(got.Links))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".links-*")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

func TestLinksFileThatCannotBeReadOrWritten(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "links.json")
	os.WriteFile(file, []byte(`{"version":1,"links":[{"id":`), 0o600)
	s := New(dir)
	var warned string
	s.Warn = func(m string, _ ...any) { warned = m }
	a := make1(t, s, "", "kb", "a", f("1"))
	aside, _ := filepath.Glob(file + ".unreadable-*")
	if len(aside) != 1 || warned == "" {
		t.Errorf("put aside %v, %q", aside, warned)
	}
	// What cannot be written is not given out.
	os.Remove(file)
	os.Mkdir(file, 0o755)
	os.WriteFile(filepath.Join(file, "x"), nil, 0o644)
	if _, err := s.Make(Place{"", "kb", "b"}, false, f("2"), "ada"); err == nil {
		t.Error("a link that is not kept was given")
	}
	if s.At(Place{"", "kb", "b"}) || !s.At(Place{"", "kb", "a"}) {
		t.Error("what is kept in memory is not what was before")
	}
	// A file that is there and cannot be read is not written over.
	s = New(dir)
	if _, err := s.Make(Place{"", "kb", "b"}, false, f("2"), "ada"); err == nil {
		t.Error("made over a file that could not be read")
	}
	if _, _, err := s.Get(a.ID); err == nil {
		t.Error("an unreadable file reads as no such link")
	}
}

func TestHowManyOnePersonMakes(t *testing.T) {
	s := New(t.TempDir())
	s.load()
	s.mine["ada"] = MaxEach
	if _, err := s.Make(Place{"", "kb", "a"}, false, f("1"), "ada"); !errors.Is(err, ErrTooMany) {
		t.Error(err)
	}
	if _, err := s.Make(Place{"", "kb", "a"}, false, f("1"), "bob"); err != nil {
		t.Error(err)
	}
}

func TestNoStore(t *testing.T) {
	var s *Store
	s.Moved("", "kb", "a", "b")
	s.Deleted("", "kb", "a")
	s.Rewrote(Place{}, f("1"), f("2"))
	s.DropLibrary("", "kb")
	if s.At(Place{}) {
		t.Error("found")
	}
}

func TestAuditNamesNoFile(t *testing.T) {
	dir := t.TempDir()
	a := NewAudit(dir)
	a.Now = func() time.Time { return time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC) }
	id := newID()
	a.Add(Entry{What: "made", ID: id, User: "ada", Outcome: "made"})
	a.Add(Entry{What: "opened", ID: "plans/budget.md", User: "bob", Outcome: "404"})
	b, err := os.ReadFile(filepath.Join(dir, "link-log", "2026-11.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-11-30T09:00:00Z","what":"made","id":"` + id + `","user":"ada","outcome":"made"}
{"time":"2026-11-30T09:00:00Z","what":"opened","id":"invalid","user":"bob","outcome":"404"}
`
	if string(b) != want {
		t.Errorf("%s", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "link-log", "2026-11.jsonl")); st.Mode().Perm() != 0o600 {
		t.Errorf("%v", st.Mode())
	}
}

func TestBase(t *testing.T) {
	const cb = "/api/auth/callback"
	for _, c := range [][3]string{
		{"https://studio.example.com/", "", "https://studio.example.com"},
		{"https://studio.example.com/me/session/ada/studio/", "", "https://studio.example.com/me/session/ada/studio"},
		{"http://studio.example.com", "https://docs.example.com/api/auth/callback", "https://docs.example.com"},
		{"", "https://docs.example.com/base/api/auth/callback", "https://docs.example.com/base"},
		{"", "https://localhost:8443/api/auth/callback", ""},
		{"https://127.0.0.1:4000", "", ""},
		{"https://[::1]:4000", "", ""},
		{"https://studio.example.com/?x=1", "", ""},
		{"https://me:pw@studio.example.com", "", ""},
		{"", "https://docs.example.com/elsewhere", ""},
		{"", "", ""},
	} {
		if got := Base(c[0], c[1], cb); got != c[2] {
			t.Errorf("%v: %q", c, got)
		}
	}
}
