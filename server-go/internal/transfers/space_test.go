package transfers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// spaceRig is the rig with a person's space: a library on a host reached
// with the viewer's key (here, through localRunner), written as them, and
// recorders for the build it queues and the account check.
type spaceRig struct {
	*rig
	space   string
	mu      sync.Mutex
	changed []string
	checked []string
	refuse  error
}

const spaceRoot = libraries.RemotePrefix + "0123456789abcdef/mine"

func newSpaceRig(t *testing.T) *spaceRig {
	r := &spaceRig{rig: newRig(t)}
	r.space = filepath.Join(r.dir, "space")
	os.MkdirAll(r.space, 0o700)
	r.more = []LibraryLoc{{ID: "mine", Label: "My library", Writable: true, Indexed: true, Space: spaceRoot,
		Refresh: "when Studio builds it again, shortly after the transfer ends",
		FS:      &RemoteFS{R: r.runner, SiteID: "cluster-a", Dir: r.space, Name: "cluster-a", Private: true}}}
	r.s.d.SpaceChanged = func(v Viewer, root string) {
		r.mu.Lock()
		r.changed = append(r.changed, v.ID+"|"+root)
		r.mu.Unlock()
	}
	r.s.d.SpaceAccount = func(_ context.Context, v Viewer, root string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.checked = append(r.checked, v.ID+"|"+root)
		return r.refuse
	}
	return r
}

func (r *spaceRig) heard() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.changed...), append([]string{}, r.checked...)
}

// TestCopyIntoASpace: a folder of the main library copied into a person's
// space lands there closed to other accounts (the space's folder is), the
// key's account is checked first, the space's index build is queued once,
// and the plan says when its index shows the copy.
func TestCopyIntoASpace(t *testing.T) {
	r := newSpaceRig(t)
	write(t, filepath.Join(r.kb, "shared", "guide.md"), "guide")
	write(t, filepath.Join(r.kb, "shared", "deep", "notes.txt"), "notes")
	q := req(ActionCopy, "library:kb", []string{"shared"}, "library:mine", "")
	q.Options.Mover = MoverCopy
	p := r.mustPlan(single, q)
	if p.Files != 2 || len(p.IndexNotes) != 1 || !strings.Contains(p.IndexNotes[0], "when Studio builds it again") {
		t.Fatalf("plan %+v", p)
	}
	tr := r.run(single, q)
	if tr.State != StateSucceeded {
		t.Fatalf("copy %+v", tr)
	}
	if read(t, filepath.Join(r.space, "shared", "deep", "notes.txt")) != "notes" {
		t.Fatal("not copied")
	}
	// The original stays in the main library.
	read(t, filepath.Join(r.kb, "shared", "guide.md"))
	for _, f := range []string{"shared/guide.md", "shared/deep/notes.txt"} {
		fi, err := os.Stat(filepath.Join(r.space, f))
		if err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: %v %v", f, err, fi.Mode().Perm())
		}
	}
	noStaging(t, r.space)
	changed, checked := r.heard()
	if len(changed) != 1 || changed[0] != "|"+spaceRoot || len(checked) < 1 {
		t.Fatalf("space heard changed %v checked %v", changed, checked)
	}
	// The main library's index is not told about a landing elsewhere.
	if len(r.ix.landed) != 0 {
		t.Fatalf("main index heard %v", r.ix.landed)
	}
}

// TestSpaceAccountRefusesThePlan: a key that reaches another account than
// the space's is refused before anything is offered; copying out of a
// space does not ask, since nothing is written there.
func TestSpaceAccountRefusesThePlan(t *testing.T) {
	r := newSpaceRig(t)
	write(t, filepath.Join(r.kb, "a.txt"), "a")
	write(t, filepath.Join(r.space, "mine.txt"), "mine")
	r.refuse = errors.New("Your session reaches cluster-a as another account")
	if _, err := r.plan(single, req(ActionCopy, "library:kb", []string{"a.txt"}, "library:mine", "")); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("into the space: %v", err)
	}
	if _, err := r.plan(single, req(ActionMove, "library:mine", []string{"mine.txt"}, "library:kb", "")); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("a move out of the space: %v", err)
	}
	if _, err := r.plan(single, req(ActionCopy, "library:mine", []string{"mine.txt"}, "library:kb", "")); err != nil {
		t.Fatalf("a copy out of the space: %v", err)
	}
}

// TestMoveOutOfASpace: the originals leave the space and its index build
// is queued; the main library is re-indexed where the files landed.
func TestMoveOutOfASpace(t *testing.T) {
	r := newSpaceRig(t)
	write(t, filepath.Join(r.space, "drafts", "d.txt"), "draft")
	q := req(ActionMove, "library:mine", []string{"drafts"}, "library:kb", "")
	q.Options.Mover = MoverCopy
	tr := r.run(single, q)
	if tr.State != StateSucceeded {
		t.Fatalf("move %+v", tr)
	}
	if read(t, filepath.Join(r.kb, "drafts", "d.txt")) != "draft" {
		t.Fatal("not moved")
	}
	gone(t, filepath.Join(r.space, "drafts", "d.txt"))
	changed, _ := r.heard()
	if len(changed) != 1 || changed[0] != "|"+spaceRoot {
		t.Fatalf("space heard %v", changed)
	}
	if len(r.ix.landed) == 0 {
		t.Fatal("the main index was not told")
	}
}

func TestLibraryRefreshOfASpace(t *testing.T) {
	if got := libraryRefresh(libraries.Library{ID: "mine", Space: true, Site: "cluster-a"}); !strings.Contains(got, "when Studio builds it again") {
		t.Fatalf("space: %q", got)
	}
	if got := libraryRefresh(libraries.Library{ID: "x", Site: "cluster-a"}); !strings.Contains(got, "next rebuild") {
		t.Fatalf("other: %q", got)
	}
}
