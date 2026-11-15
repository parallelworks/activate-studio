package transfers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

var signedIn = Viewer{ID: "u1", AuthOn: true, Key: "key-u1"}

func TestLibraryWritableRule(t *testing.T) {
	for _, c := range []struct {
		name          string
		indexWritable bool
		by            Access
		authOn, want  bool
	}{
		{"main library, sign-in on", true, AccessServer, true, true},
		{"folder here, sign-in off", false, AccessServer, false, true},
		{"folder here, sign-in on", false, AccessServer, true, false},
		{"viewer's key, sign-in off", false, AccessViewer, false, true},
		{"viewer's key, sign-in on", false, AccessViewer, true, true},
		{"index only", false, AccessNone, false, false},
		{"index only, registry writable", true, AccessNone, true, false},
	} {
		got, why := LibraryWritable(c.indexWritable, c.by, c.authOn)
		if got != c.want || (!got && c.by != AccessNone) != (why != "") {
			t.Errorf("%s: %v %q, want %v", c.name, got, why, c.want)
		}
	}
}

func locByID(t *testing.T, r *rig, v Viewer, id string) Location {
	t.Helper()
	for _, l := range r.s.Locations(context.Background(), v) {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("no location %s", id)
	return Location{}
}

func hasNote(notes []string, label, when string) bool {
	for _, n := range notes {
		if n == "The index of "+label+" shows this change "+when {
			return true
		}
	}
	return false
}

func hasEvent(tr Transfer, text string) bool {
	for _, e := range tr.Events {
		if strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

// A library other than the main one, in a folder on this computer: with
// sign-in off, copies go into it and moves come out of it, and the index
// is said to catch up later; with sign-in on it is read only.
func TestLibraryFolderWritableByAccess(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "notes.md"), "n")
	write(t, filepath.Join(r.ro, "old", "a.txt"), "a")

	p := r.mustPlan(single, req(ActionCopy, "saved:a", []string{"notes.md"}, "library:ro", ""))
	if !hasNote(p.IndexNotes, "Reference", refNote) || len(p.IndexNotes) != 1 {
		t.Fatalf("plan notes %q", p.IndexNotes)
	}
	tr := r.run(single, req(ActionCopy, "saved:a", []string{"notes.md"}, "library:ro", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.ro, "notes.md")) != "n" {
		t.Fatalf("copy in %+v", tr)
	}
	if !hasNote(tr.IndexNotes, "Reference", refNote) {
		t.Fatalf("transfer notes %q", tr.IndexNotes)
	}
	got, _ := r.s.Get(single, tr.ID)
	if !hasEvent(got, "The index of Reference shows this change") {
		t.Fatalf("events %+v", got.Events)
	}
	tr = r.run(single, req(ActionMove, "library:ro", []string{"old"}, "saved:b", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.hostB, "old", "a.txt")) != "a" || !hasNote(tr.IndexNotes, "Reference", refNote) {
		t.Fatalf("move out %+v", tr)
	}
	gone(t, filepath.Join(r.ro, "old"))
	// Studio re-indexes only the main library.
	r.ix.mu.Lock()
	ix := append(append([]string{}, r.ix.landed...), r.ix.removed...)
	r.ix.mu.Unlock()
	for _, x := range ix {
		if strings.HasPrefix(x, "ro:") {
			t.Fatalf("the index was told about %s", x)
		}
	}
	// Into the main library: no note, the index updates itself.
	write(t, filepath.Join(r.hostA, "more.md"), "m")
	if p := r.mustPlan(single, req(ActionCopy, "saved:a", []string{"more.md"}, "library:kb", "")); len(p.IndexNotes) != 0 {
		t.Fatalf("main library notes %q", p.IndexNotes)
	}
	// A move from the main library into this one names this one only.
	write(t, filepath.Join(r.kb, "k.txt"), "k")
	if p := r.mustPlan(single, req(ActionMove, "library:kb", []string{"k.txt"}, "library:ro", "")); len(p.IndexNotes) != 1 {
		t.Fatalf("kb to ro notes %q", p.IndexNotes)
	}

	// Sign-in on: the folder is reached with the server account's access,
	// so it is read only, and the location says why.
	l := locByID(t, r, signedIn, "library:ro")
	if l.Writable || !strings.Contains(l.Detail, ServerAccountReadOnly) {
		t.Fatalf("signed-in location %+v", l)
	}
	write(t, filepath.Join(r.ro, "keep.txt"), "keep")
	_, err := r.plan(signedIn, req(ActionCopy, "library:kb", []string{"k.txt"}, "library:ro", ""))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "Reference is read only: "+ServerAccountReadOnly) {
		t.Fatalf("copy in, signed in: %v", err)
	}
	_, err = r.plan(signedIn, req(ActionMove, "library:ro", []string{"keep.txt"}, "library:kb", ""))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "nothing can be moved out of it") {
		t.Fatalf("move out, signed in: %v", err)
	}
	// A copy out is still fine.
	if _, err := r.plan(signedIn, req(ActionCopy, "library:ro", []string{"keep.txt"}, "library:kb", "")); err != nil {
		t.Fatalf("copy out, signed in: %v", err)
	}
}

// A library on another host reached with the viewer's own key is writable
// with sign-in on; one read through a host with the server account's keys
// is writable with sign-in off.
func TestLibraryRemoteWritableByAccess(t *testing.T) {
	r := newRig(t)
	site, err := remote.SSHSite("user@lib-host.example.com", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	libRoot := filepath.Join(r.dir, "remote-lib")
	write(t, filepath.Join(libRoot, "runs", "r1.txt"), "r1")
	r.moreFor = func(v Viewer) []LibraryLoc {
		mine, _ := LibraryWritable(false, AccessViewer, v.AuthOn)
		rt, why := LibraryWritable(false, AccessServer, v.AuthOn)
		out := []LibraryLoc{{ID: "mine", Label: "My runs", Writable: mine, Refresh: "after its next rebuild in Settings, Indexing, Libraries",
			FS: &RemoteFS{R: r.runner, SiteID: site, KeyText: v.Key, Dir: libRoot, Name: remote.SiteName(site)}}}
		if !v.AuthOn {
			out = append(out, LibraryLoc{ID: "rt", Label: "Read through", Writable: rt, ReadOnly: why,
				FS: &RemoteFS{R: r.runner, SiteID: site, Dir: filepath.Join(r.dir, "rt"), Name: remote.SiteName(site)}})
		}
		return out
	}
	write(t, filepath.Join(r.kb, "in.txt"), "in")
	tr := r.run(signedIn, req(ActionCopy, "library:kb", []string{"in.txt"}, "library:mine", "runs"))
	if tr.State != StateSucceeded || read(t, filepath.Join(libRoot, "runs", "in.txt")) != "in" ||
		!hasNote(tr.IndexNotes, "My runs", "after its next rebuild in Settings, Indexing, Libraries") {
		t.Fatalf("copy in through the viewer's key %+v", tr)
	}
	tr = r.run(signedIn, req(ActionMove, "library:mine", []string{"runs/r1.txt"}, "library:kb", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.kb, "r1.txt")) != "r1" {
		t.Fatalf("move out through the viewer's key %+v", tr)
	}
	gone(t, filepath.Join(libRoot, "runs", "r1.txt"))
	noStaging(t, libRoot)

	// Read through, sign-in off: writable, and the note is the general one.
	os.MkdirAll(filepath.Join(r.dir, "rt"), 0o755)
	if l := locByID(t, r, single, "library:rt"); !l.Writable {
		t.Fatalf("read-through location %+v", l)
	}
	tr = r.run(single, req(ActionCopy, "library:kb", []string{"in.txt"}, "library:rt", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.dir, "rt", "in.txt")) != "in" || !hasNote(tr.IndexNotes, "Read through", "after its next refresh") {
		t.Fatalf("copy into read-through %+v", tr)
	}
}

// lock makes dir read only for its owner until the test ends.
func lock(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes in a read-only folder")
	}
	os.MkdirAll(dir, 0o755)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

// Before a plan is offered, the folders it writes in are checked: the
// destination folder, and for a move the folders the originals leave.
func TestPreflightWriteCheck(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "notes.md"), "n")
	write(t, filepath.Join(r.ro, "locked", "a.txt"), "a")
	write(t, filepath.Join(r.hostB, "locked", "b.txt"), "b")
	write(t, filepath.Join(r.ro, "open", "c.txt"), "c")
	lock(t, filepath.Join(r.ro, "locked"))
	lock(t, filepath.Join(r.hostB, "locked"))

	// This computer.
	_, err := r.plan(single, req(ActionCopy, "saved:a", []string{"notes.md"}, "library:ro", "locked"))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "you cannot write in "+filepath.Join(r.ro, "locked")+" on this computer") {
		t.Fatalf("copy into a locked folder: %v", err)
	}
	_, err = r.plan(single, req(ActionMove, "library:ro", []string{"locked/a.txt"}, "saved:b", ""))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "originals could not be removed") {
		t.Fatalf("move out of a locked folder: %v", err)
	}
	// A folder named in a move is checked as well as the one holding it.
	_, err = r.plan(single, req(ActionMove, "library:ro", []string{"locked"}, "saved:b", ""))
	if statusOf(err) != 400 {
		t.Fatalf("move of a locked folder: %v", err)
	}
	if _, err := r.plan(single, req(ActionMove, "library:ro", []string{"open/c.txt"}, "saved:b", "")); err != nil {
		t.Fatalf("move out of an open folder: %v", err)
	}

	// A host with a shell (the stand-in runner).
	_, err = r.plan(single, req(ActionCopy, "saved:a", []string{"notes.md"}, "saved:b", "locked"))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "you cannot write in "+filepath.Join(r.hostB, "locked")+" on ") {
		t.Fatalf("copy into a locked folder on a host: %v", err)
	}
	_, err = r.plan(single, req(ActionMove, "saved:b", []string{"locked/b.txt"}, "library:kb", ""))
	if statusOf(err) != 400 {
		t.Fatalf("move out of a locked folder on a host: %v", err)
	}
	// A copy out of a locked folder needs no write access.
	if _, err := r.plan(single, req(ActionCopy, "saved:b", []string{"locked/b.txt"}, "library:kb", "")); err != nil {
		t.Fatalf("copy out of a locked folder: %v", err)
	}
	noStaging(t, r.dir)
}

// The same check on an account that takes SFTP only.
func TestPreflightWriteCheckSFTP(t *testing.T) {
	_, run, dir := sftpFS(t)
	drop := filepath.Join(dir, "drop")
	write(t, filepath.Join(drop, "locked", "x.txt"), "x")
	os.MkdirAll(filepath.Join(drop, "open"), 0o755)
	kb := filepath.Join(dir, "kb")
	write(t, filepath.Join(kb, "one.txt"), "one")
	lock(t, filepath.Join(drop, "locked"))
	set := DefaultSettings()
	set.SavedLocations = []SavedLocation{{ID: "drop", Label: "Drop", Host: "drop@files.example.com", Root: drop}}
	shell := func(site, key string) ([]string, []string, error) { return []string{"false"}, nil, nil }
	s := New(Deps{IndexBase: filepath.Join(dir, "ix"), Getenv: func(string) string { return "" }, Runner: run, Shell: shell,
		Libraries: func(context.Context, Viewer) []LibraryLoc {
			return []LibraryLoc{{ID: "kb", Label: "Knowledge base", Writable: true, Indexed: true, FS: &LocalFS{Dir: kb}}}
		},
		Settings: &MemorySettings{S: set}})
	ctx := context.Background()
	_, err := s.Plan(ctx, single, req(ActionCopy, "library:kb", []string{"one.txt"}, "saved:drop", "locked"))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "you cannot write in "+filepath.Join(drop, "locked")) {
		t.Fatalf("copy into a locked folder over SFTP: %v", err)
	}
	_, err = s.Plan(ctx, single, req(ActionMove, "saved:drop", []string{"locked/x.txt"}, "library:kb", ""))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "originals could not be removed") {
		t.Fatalf("move out of a locked folder over SFTP: %v", err)
	}
	if _, err := s.Plan(ctx, single, req(ActionCopy, "library:kb", []string{"one.txt"}, "saved:drop", "open")); err != nil {
		t.Fatalf("copy into an open folder over SFTP: %v", err)
	}
	noStaging(t, drop)
}
