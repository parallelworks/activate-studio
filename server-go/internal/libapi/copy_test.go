package libapi

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// A copy library end to end: added from a folder in a Transfers location,
// its first build copies the folder into the Studio's data and indexes it;
// a look for changes brings what changed at the location and the sync
// indexes it; its Transfers location is read only; its settings say where
// it comes from and what the last look found; removing it removes the copy.
// The location is one of this computer's folders, which the product does
// not offer for copies (a folder here is built in place), standing in for
// a host. Needs GUFI.
func TestCopyLibrary(t *testing.T) {
	f := newBuildFixture(t)
	saved := copyKinds
	copyKinds = append(slices.Clone(copyKinds), transfers.KindComputer)
	t.Cleanup(func() { copyKinds = saved })
	share := filepath.Join(f.dir, "Downloads", "share")
	put(t, filepath.Join(share, "intro.md"), "# Introduction\nThe zanzibar turbine study.\n")
	put(t, filepath.Join(share, "data", "runs.csv"), "a,b\n1,2\n")

	// Refusals before anything is kept.
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"location": "library:kb"}, "location: a saved location"},
		{map[string]any{"location": "computer:Downloads", "path": "nowhere"}, ""},
		{map[string]any{"location": "computer:Downloads", "path": "share", "every": 7}, "every: 0, 5, 15, 60, 360, 1440 minutes"},
		{map[string]any{"location": "computer:Downloads", "path": "../x"}, ""},
	} {
		code, b := f.call("POST", "/api/libraries/copy", c.body)
		if code < 400 || (c.want != "" && !strings.Contains(b["error"].(string), c.want)) {
			t.Fatalf("%v: %d %v", c.body, code, b)
		}
	}
	if l := f.library("share"); l != nil {
		t.Fatalf("a refused copy was kept: %v", l)
	}

	code, b := f.call("POST", "/api/libraries/copy", map[string]any{"location": "computer:Downloads", "path": "share", "every": 60, "depth": "text"})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	lib := b["library"].(map[string]any)
	id := lib["id"].(string)
	if id != "share" || lib["copyOf"].(map[string]any)["location"] != "computer:Downloads" {
		t.Fatalf("added %v", lib)
	}
	l := f.waitReady(id)
	if l["build"].(map[string]any)["state"] != "ready" {
		t.Fatalf("build %v", l["build"])
	}
	copyDir := filepath.Join(f.base, "libraries", id, "files")
	if got, _ := os.ReadFile(filepath.Join(copyDir, "intro.md")); !strings.Contains(string(got), "zanzibar") {
		t.Fatalf("the copy holds %q", got)
	}
	if h := f.hits("zanzibar", id); !slices.Equal(h, []string{"intro.md"}) {
		t.Fatalf("hits %v", h)
	}
	co := l["copyOf"].(map[string]any)
	if co["every"] != float64(60) || co["checkedAt"] == nil || co["changedAt"] == nil || co["files"] != float64(2) || co["error"] != nil {
		t.Fatalf("copyOf after the first build: %v", co)
	}

	// Its Transfers location takes no transfers.
	xs := transfers.Of(f.a)
	v, _ := xs.OwnerViewer()
	for _, loc := range xs.Locations(context.Background(), v) {
		if loc.ID == "library:"+id && (loc.Writable || !strings.Contains(loc.ReadOnly, "a copy of Downloads, share")) {
			t.Fatalf("the copy's location %+v", loc)
		}
	}

	// A change at the location: a look brings it and the sync indexes it.
	put(t, filepath.Join(share, "notes", "later.md"), "The quokka appendix.\n")
	os.Remove(filepath.Join(share, "data", "runs.csv"))
	f.s.builds.update(context.Background(), id)
	if h := f.hits("quokka", id); !slices.Equal(h, []string{"notes/later.md"}) {
		t.Fatalf("hits after the update %v", h)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "data", "runs.csv")); err == nil {
		t.Fatal("a file removed at the location is still in the copy")
	}
	st := f.s.Reg.CopyState(id)
	if st.Files != 1 || st.Deleted != 1 || st.Transfer == nil || st.Error != nil {
		t.Fatalf("state after the update %+v", st)
	}
	// Due by the interval, and not before.
	c := f.s.Reg.AddedDef(id).CopyOf
	at, _ := time.Parse(time.RFC3339Nano, *st.CheckedAt)
	if copyDue(c, st, at.Add(59*time.Minute)) || !copyDue(c, st, at.Add(60*time.Minute)) {
		t.Fatal("copyDue does not follow the interval")
	}
	if copyDue(&libraries.CopyOf{Every: 0}, libraries.CopyState{}, at) {
		t.Fatal("every 0 is only when asked")
	}

	// The interval can change; Update now answers at once.
	if code, b := f.call("PUT", "/api/libraries/"+id+"/copy", map[string]any{"every": 0}); code != 200 || b["library"].(map[string]any)["copyOf"].(map[string]any)["every"] != float64(0) {
		t.Fatalf("every: %d %v", code, b)
	}
	// Its answer, and each read until the look ends, say a look is on.
	if code, b := f.call("POST", "/api/libraries/"+id+"/update", nil); code != 200 || b["started"] != true || b["library"].(map[string]any)["copyOf"].(map[string]any)["looking"] != true {
		t.Fatalf("update now: %d %v", code, b)
	}
	for end := time.Now().Add(20 * time.Second); f.library(id)["copyOf"].(map[string]any)["looking"] == true; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the look never ended")
		}
	}
	f.waitReady(id)

	// A location that is gone is said plainly, and the copy stays.
	f.s.Reg.ModifyAdded(id, func(d *libraries.Def) { c := *d.CopyOf; c.Location = "computer:Gone"; d.CopyOf = &c })
	f.s.builds.update(context.Background(), id)
	if st := f.s.Reg.CopyState(id); st.Error == nil || !strings.Contains(*st.Error, "is no longer in Transfers") {
		t.Fatalf("state with the location gone %+v", st)
	}
	if h := f.hits("zanzibar", id); len(h) != 1 {
		t.Fatalf("the copy went with its location: %v", h)
	}

	// Removing the library removes the copy.
	if code, b := f.call("DELETE", "/api/libraries/"+id, nil); code != 200 {
		t.Fatalf("remove: %d %v", code, b)
	}
	if _, err := os.Stat(copyDir); err == nil {
		t.Fatal("the copy outlived its library")
	}
}
