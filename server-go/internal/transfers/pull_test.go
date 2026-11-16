package transfers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pullAll is every transfer the viewer has, active and finished.
func pullAll(t *testing.T, s *Service) []Transfer {
	t.Helper()
	pg, err := s.List(single, ListQuery{State: "all", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return pg.Transfers
}

// A copy library's pull: the first brings the folder, one with no change
// leaves no transfer behind, and one after changes brings new and changed
// files and removes those the source no longer has. Times are kept, hidden
// names are left out, and no staging folder stays.
func TestPullKeepsACopy(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "share", "a.txt"), "alpha")
	write(t, filepath.Join(r.hostA, "share", "sub", "b.txt"), "bravo")
	write(t, filepath.Join(r.hostA, "share", ".hidden"), "h")
	old := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(r.hostA, "share", "a.txt"), old, old)
	dir := filepath.Join(r.dir, "copy")
	os.MkdirAll(dir, 0o755)
	in := PullRequest{Location: "saved:a", Path: "share", LibraryID: "share", Label: "Share", Dir: dir}
	var seen []string
	in.Progress = func(t Transfer) { seen = append(seen, t.State) }
	ctx := context.Background()

	res, err := r.s.Pull(ctx, single, in)
	if err != nil || res.Transfer == "" || res.Files != 2 {
		t.Fatalf("first pull %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "a.txt")) != "alpha" || read(t, filepath.Join(dir, "sub", "b.txt")) != "bravo" {
		t.Fatal("the copy does not hold the folder")
	}
	gone(t, filepath.Join(dir, ".hidden"))
	if st, _ := os.Stat(filepath.Join(dir, "a.txt")); !st.ModTime().Equal(old) {
		t.Fatalf("a.txt's time %v, want %v", st.ModTime(), old)
	}
	noStaging(t, dir)
	if len(seen) == 0 || seen[len(seen)-1] != StateSucceeded {
		t.Fatalf("progress saw %v", seen)
	}
	all := pullAll(t, r.s)
	if len(all) != 1 || all[0].StartedBy != ByLibrary || all[0].Action != ActionSync || all[0].Destination.Location != "library:share" {
		t.Fatalf("transfers %+v", all)
	}

	// Nothing changed: no transfer.
	res, err = r.s.Pull(ctx, single, in)
	if err != nil || res.Transfer != "" || res.Files != 0 || res.Deleted != 0 {
		t.Fatalf("pull with no change %+v %v", res, err)
	}
	if n := len(pullAll(t, r.s)); n != 1 {
		t.Fatalf("a pull with no change left a transfer: %d", n)
	}

	// A file changed, one removed, one added in a new folder.
	write(t, filepath.Join(r.hostA, "share", "a.txt"), "alpha, longer now")
	os.Remove(filepath.Join(r.hostA, "share", "sub", "b.txt"))
	write(t, filepath.Join(r.hostA, "share", "new", "c.txt"), "charlie")
	res, err = r.s.Pull(ctx, single, in)
	if err != nil || res.Transfer == "" || res.Files != 2 || res.Deleted != 1 {
		t.Fatalf("pull after changes %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "a.txt")) != "alpha, longer now" || read(t, filepath.Join(dir, "new", "c.txt")) != "charlie" {
		t.Fatal("the changes did not arrive")
	}
	gone(t, filepath.Join(dir, "sub", "b.txt"))
	noStaging(t, dir)
}

// A pull is refused with sign-in on, and one whose location is gone says so.
func TestPullRefusals(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.dir, "copy")
	os.MkdirAll(dir, 0o755)
	in := PullRequest{Location: "saved:a", LibraryID: "x", Label: "X", Dir: dir}
	if _, err := r.s.Pull(context.Background(), Viewer{ID: "u1", AuthOn: true}, in); statusOf(err) != 403 {
		t.Fatalf("sign-in on: %v", err)
	}
	in.Location = "saved:gone"
	if _, err := r.s.Pull(context.Background(), single, in); statusOf(err) != 404 || !strings.HasPrefix(messageOf(err), "no location") {
		t.Fatalf("a location that is gone: %v", err)
	}
	if v, ok := r.s.OwnerViewer(); !ok || v.AuthOn {
		t.Fatalf("owner viewer %+v %v", v, ok)
	}
}

// A copy of a folder on an account that takes SFTP only: Studio's own copy
// reads each file over SFTP, and a removal there is removed from the copy.
func TestPullOverSFTP(t *testing.T) {
	_, run, dir := sftpFS(t)
	drop := filepath.Join(dir, "drop")
	write(t, filepath.Join(drop, "upload", "one.txt"), "one")
	write(t, filepath.Join(drop, "upload", "deep", "two.txt"), "two")
	set := DefaultSettings()
	set.SavedLocations = []SavedLocation{{ID: "drop", Label: "Drop", Host: "drop@files.example.com", Root: drop}}
	shell := func(site, key string) ([]string, []string, error) { return []string{"false"}, nil, nil }
	s := New(Deps{IndexBase: filepath.Join(dir, "ix"), Getenv: func(string) string { return "" }, Runner: run, Shell: shell,
		Libraries: func(context.Context, Viewer) []LibraryLoc { return nil }, Settings: &MemorySettings{S: set}, Index: &recIndex{}})
	cp := filepath.Join(dir, "copy")
	os.MkdirAll(cp, 0o755)
	in := PullRequest{Location: "saved:drop", Path: "upload", LibraryID: "drop", Label: "Drop", Dir: cp}
	res, err := s.Pull(context.Background(), single, in)
	if err != nil || res.Files != 2 {
		t.Fatalf("pull %+v %v", res, err)
	}
	if read(t, filepath.Join(cp, "deep", "two.txt")) != "two" {
		t.Fatal("the copy does not hold the drop's folder")
	}
	tr, err := s.Get(single, res.Transfer)
	if err != nil || tr.Mover != MoverCopy {
		t.Fatalf("transfer %+v %v", tr, err)
	}
	os.Remove(filepath.Join(drop, "upload", "one.txt"))
	if res, err = s.Pull(context.Background(), single, in); err != nil || res.Deleted != 1 {
		t.Fatalf("pull after a removal %+v %v", res, err)
	}
	gone(t, filepath.Join(cp, "one.txt"))
	noStaging(t, cp)
}
