package transfers

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

func findLoc(ls []Location, id string) *Location {
	for i := range ls {
		if ls[i].ID == id {
			return &ls[i]
		}
	}
	return nil
}

// A library whose files Studio cannot reach or write carries a suggested
// saved location with sign-in off (what Studio knows, nothing guessed),
// and with sign-in on a hint at what can be written instead.
func TestLibrarySuggestions(t *testing.T) {
	r := newRig(t)
	site, err := remote.SSHSite("user@files.example.com", 2222, []string{"-o", "IdentityFile=~/.ssh/archive_key"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := remote.SSHSite("user@other.example.com", 0, nil)
	r.more = []LibraryLoc{
		{ID: "archive", Label: "Archive", Why: remote.NoShell(site), Site: site, Folder: "/srv/archive"},
		{ID: "nofolder", Label: "Project X", Why: "the library's folder on its host is not known", Site: other},
	}
	ctx := context.Background()
	ls := r.s.Locations(ctx, single)

	a := findLoc(ls, "library:archive")
	if a == nil || a.Suggest == nil || *a.Suggest != (Suggest{Label: "Archive files", Host: "user@files.example.com", Root: "/srv/archive", Port: 2222, SSHOptions: "-o IdentityFile=~/.ssh/archive_key"}) || a.Hint != "" {
		t.Fatalf("archive %+v %+v", a, a.Suggest)
	}
	// The folder is not known: left empty.
	if x := findLoc(ls, "library:nofolder"); x.Suggest == nil || *x.Suggest != (Suggest{Label: "Project X files", Host: "user@other.example.com"}) {
		t.Fatalf("no folder %+v", x.Suggest)
	}
	// An index only: nothing known but the label.
	if x := findLoc(ls, "library:far"); x.Suggest == nil || *x.Suggest != (Suggest{Label: "Index only files"}) {
		t.Fatalf("index only %+v", x.Suggest)
	}
	// Writable libraries get none.
	for _, id := range []string{"library:kb", "library:ro"} {
		if x := findLoc(ls, id); x.Suggest != nil || x.Hint != "" {
			t.Fatalf("%s %+v", id, x)
		}
	}

	// Sign-in on: no suggestion, since saved locations are off; a hint.
	on := Viewer{ID: "u1", AuthOn: true, Key: "k"}
	ls = r.s.Locations(ctx, on)
	for _, id := range []string{"library:archive", "library:far", "library:ro"} {
		x := findLoc(ls, id)
		if x == nil || x.Suggest != nil || !strings.Contains(x.Hint, "Your clusters, reached with your own key, can be written") {
			t.Fatalf("%s with sign-in on %+v", id, x)
		}
	}
	if x := findLoc(r.s.Locations(ctx, Viewer{ID: "u2", AuthOn: true}), "library:far"); !strings.Contains(x.Hint, "own platform key") {
		t.Fatalf("no key %+v", x)
	}
}

func TestAddSavedLocation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	l, err := r.s.AddSavedLocation(ctx, single, SavedLocation{ID: "ignored", Label: "A", Host: "user@host-c", Root: r.hostA + "/"})
	// The id comes from the label, and "a" is taken.
	if err != nil || l.ID != "a-2" || l.Root != r.hostA || l.Label != "A" {
		t.Fatalf("added %+v %v", l, err)
	}
	set := r.set.Load()
	if len(set.SavedLocations) != 3 || set.SavedLocations[0].ID != "a" || set.SavedLocations[2].ID != "a-2" {
		t.Fatalf("saved %+v", set.SavedLocations)
	}
	if !hasLoc(r.s.Locations(ctx, single), "saved:a-2") {
		t.Fatal("not a location")
	}
	// Field errors name the field alone; nothing is saved.
	if _, err := r.s.AddSavedLocation(ctx, single, SavedLocation{Host: "h", Root: "relative"}); statusOf(err) != 400 || !strings.HasPrefix(messageOf(err), "root:") {
		t.Fatalf("bad root %v", err)
	}
	if _, err := r.s.AddSavedLocation(ctx, Viewer{ID: "u1", AuthOn: true}, SavedLocation{Host: "h", Root: "/x"}); statusOf(err) != 403 {
		t.Fatalf("sign-in on %v", err)
	}
	if n := len(r.set.Load().SavedLocations); n != 3 {
		t.Fatalf("%d saved", n)
	}

	// Over the route.
	a := newAPI(t, r)
	st, out := a.do("POST", "/api/transfers/settings/locations", "", map[string]any{"label": "Archive files", "host": "user@host-b", "root": r.hostB})
	if st != 200 || out["id"] != "saved:archive-files" || out["location"].(map[string]any)["host"] != "user@host-b" {
		t.Fatalf("route %d %v", st, out)
	}
	if st, out := a.do("POST", "/api/transfers/settings/locations", "alice", map[string]any{"host": "h", "root": "/x"}); st != 403 {
		t.Fatalf("route with sign-in on %d %v", st, out)
	}
}

func TestTestAndSave(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	got, err := r.s.TestAndSave(ctx, single, SavedLocation{Label: "Drop", Host: "user@host-c", Root: r.hostB})
	if err != nil || !got.Test.OK || got.Location == nil || got.ID != "saved:drop" || got.Test.Writable == nil || !*got.Test.Writable {
		t.Fatalf("saved %+v %v", got, err)
	}
	noStaging(t, r.hostB)
	// A failed test saves nothing.
	got, err = r.s.TestAndSave(ctx, single, SavedLocation{Label: "Nowhere", Host: "user@host-c", Root: filepath.Join(r.dir, "nowhere")})
	if err != nil || got.Test.OK || got.Location != nil || got.ID != "" {
		t.Fatalf("failed test %+v %v", got, err)
	}
	if n := len(r.set.Load().SavedLocations); n != 3 {
		t.Fatalf("%d saved", n)
	}
	if _, err := r.s.TestAndSave(ctx, Viewer{ID: "u1", AuthOn: true}, SavedLocation{Host: "h", Root: "/x"}); statusOf(err) != 403 {
		t.Fatalf("sign-in on %v", err)
	}
}

func TestCheckSavedLocation(t *testing.T) {
	l, err := CheckSavedLocation(SavedLocation{Host: " user@files.example.com ", Root: "/srv/archive/"})
	if err != nil || l.Host != "user@files.example.com" || l.Root != "/srv/archive" || l.Label != "user@files.example.com:/srv/archive" || l.ID != "" {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := CheckSavedLocation(SavedLocation{Host: "-oProxyCommand=x", Root: "/a"}); !strings.HasPrefix(messageOf(err), "host:") {
		t.Fatalf("host %v", err)
	}
	if _, err := CheckSavedLocation(SavedLocation{Host: "h", Root: "/a", RsyncPath: "rsync; rm"}); !strings.HasPrefix(messageOf(err), "rsyncPath:") {
		t.Fatalf("rsync %v", err)
	}
}
