package transfers

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A library with an index of its own is Indexed, so the page says it is
// searchable, though Studio updates only the main library's index after a
// transfer: a move out of it tells no index.
func TestIndexedMeansHasAnIndex(t *testing.T) {
	r := newRig(t)
	imp := filepath.Join(r.dir, "imported")
	write(t, filepath.Join(imp, "notes.txt"), "notes")
	r.more = []LibraryLoc{{ID: "imp", Label: "Imported", Writable: true, Indexed: true, Refresh: refNote, FS: &LocalFS{Dir: imp}}}
	by := map[string]Location{}
	for _, l := range r.s.Locations(context.Background(), single) {
		by[l.ID] = l
	}
	if !by["library:imp"].Indexed || !by["library:kb"].Indexed || by["library:ro"].Indexed {
		t.Fatalf("indexed flags %+v %+v %+v", by["library:imp"], by["library:kb"], by["library:ro"])
	}
	tr := r.run(single, req(ActionMove, "library:imp", []string{"notes.txt"}, "library:ro", ""))
	if tr.State != StateSucceeded {
		t.Fatalf("move %+v", tr)
	}
	if len(r.ix.removed) != 0 || len(r.ix.landed) != 0 {
		t.Fatalf("an index other than the main one was told: %v %v", r.ix.removed, r.ix.landed)
	}
}

// One file is "1 file" in the events, not "1 files"; a move out of the
// main library still tells its index.
func TestEventsCountOneFile(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.kb, "notes.txt"), "notes")
	tr := r.run(single, req(ActionMove, "library:kb", []string{"notes.txt"}, "library:ro", ""))
	if tr.State != StateSucceeded {
		t.Fatalf("move %+v", tr)
	}
	if strings.Join(r.ix.removed, ",") != "kb:notes.txt" {
		t.Fatalf("index removed %v", r.ix.removed)
	}
	got, _ := r.s.Get(single, tr.ID)
	var text []string
	for _, e := range got.Events {
		text = append(text, e.Text)
	}
	all := strings.Join(text, "\n")
	for _, bad := range []string{"1 files", "all 1 ", "1 originals"} {
		if strings.Contains(all, bad) {
			t.Fatalf("%q in events:\n%s", bad, all)
		}
	}
	for _, want := range []string{"Queued: 1 file,", "1 file in place", "1 original removed from Knowledge base"} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in events:\n%s", want, all)
		}
	}
}

func TestCount(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0 files"}, {1, "1 file"}, {2, "2 files"}} {
		if got := count(c.n, "file", "files"); got != c.want {
			t.Errorf("count(%d) = %q", c.n, got)
		}
	}
	if arrivedAll(1) != "The file arrived" || arrivedAll(3) != "All 3 files arrived" {
		t.Error(arrivedAll(1), arrivedAll(3))
	}
	if matchesAll(1) != "matches for the file" || matchesAll(3) != "matches for all 3 files" {
		t.Error(matchesAll(1), matchesAll(3))
	}
}
