package transfers

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

func savedIDs(m map[string]any) []string {
	var out []string
	for _, l := range saved(m) {
		out = append(out, l["id"].(string))
	}
	return out
}

// The reported loss: Settings, Transfers read the section, a location was
// saved elsewhere, and a Save from the old read wrote the list without it.
// That PUT is now refused, and the location stays.
func TestSettingsStalePutRefused(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	_, page := a.do("GET", "/api/transfers/settings", "", nil)
	rev, _ := page["revision"].(string)
	if rev == "" {
		t.Fatalf("no revision %v", page)
	}
	// The same section reads with the same revision.
	if _, again := a.do("GET", "/api/transfers/settings", "", nil); again["revision"] != rev {
		t.Fatalf("revision moved %v %v", rev, again["revision"])
	}
	// Saved from the Transfers page.
	if st, out := a.do("POST", "/api/transfers/settings/locations", "", map[string]any{"label": "Drop", "host": "user@host-c", "root": r.hostB}); st != 200 {
		t.Fatalf("add %d %v", st, out)
	}
	// The page's Save, from its old read, with one switch changed.
	m := clone(page)
	m["movers"].(map[string]any)["rclone"].(map[string]any)["enabled"] = false
	st, out := a.do("PUT", "/api/transfers/settings", "", m)
	if st != 409 || out["error"] != staleSettings {
		t.Fatalf("stale PUT %d %v", st, out)
	}
	_, now := a.do("GET", "/api/transfers/settings", "", nil)
	if ids := savedIDs(now); len(ids) != 3 || ids[2] != "drop" {
		t.Fatalf("location lost %v", ids)
	}
	if now["revision"] == rev {
		t.Fatal("the add kept the revision")
	}
	// A PUT with no revision is refused too, and names the field.
	delete(m, "revision")
	if st, out := a.do("PUT", "/api/transfers/settings", "", m); st != 400 || out["error"].(string)[:9] != "revision:" {
		t.Fatalf("no revision %d %v", st, out)
	}
	// From the new read it saves, keeps the location and returns the next
	// revision.
	m = clone(now)
	m["movers"].(map[string]any)["rclone"].(map[string]any)["enabled"] = false
	st, out = a.do("PUT", "/api/transfers/settings", "", m)
	if st != 200 || len(savedIDs(out)) != 3 || out["revision"] == now["revision"] || out["revision"] == "" {
		t.Fatalf("fresh PUT %d %v", st, out)
	}
	if r.set.S.Movers.Rclone.Enabled {
		t.Fatal("not saved")
	}
}

// slowSettings widens the time between a check of the stored section
// and its write, so a PUT and an add that did not share the lock would
// interleave: an add would land inside a PUT's write and be lost.
type slowSettings struct{ SettingsStore }

func (s slowSettings) Save(set Settings) error {
	time.Sleep(3 * time.Millisecond)
	return s.SettingsStore.Save(set)
}

// Adds and PUTs at the same time: every add is kept, and every PUT either
// saves (from the section as it then was) or is refused, never writing
// over an add or another PUT it did not see.
func TestSettingsAddAndPutRace(t *testing.T) {
	r := newRig(t)
	r.s.d.Settings = slowSettings{r.set}
	ctx := context.Background()
	const adds, puts = 12, 12
	var wg sync.WaitGroup
	errs := make(chan error, adds+puts)
	for i := 0; i < adds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.s.AddSavedLocation(ctx, single, SavedLocation{Label: fmt.Sprintf("Race %d", i), Host: "user@host-c", Root: r.hostA})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	okPuts := make(chan int, puts)
	for i := 0; i < puts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Read, change, write back, as the page does; a 409 is read
			// again and tried again.
			for try := 0; try < 100; try++ {
				cur := r.set.Load()
				in := SettingsPut{Settings: cur, Revision: cur.revision()}
				in.HostLocations.Hidden = append(append([]string{}, cur.HostLocations.Hidden...), fmt.Sprintf("host-%d", i))
				err := r.s.saveIfCurrent(in)
				if err == nil {
					okPuts <- i
					return
				}
				if statusOf(err) != 409 {
					errs <- err
					return
				}
			}
			errs <- fmt.Errorf("put %d never saved", i)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := len(okPuts); n != puts {
		t.Fatalf("%d of %d PUTs saved", n, puts)
	}
	set := r.set.Load()
	if len(set.SavedLocations) != 2+adds {
		t.Fatalf("%d saved locations, want %d: %+v", len(set.SavedLocations), 2+adds, set.SavedLocations)
	}
	// Each PUT added one hidden host to what it read; none was written
	// over by another PUT or an add.
	if len(set.HostLocations.Hidden) != puts {
		t.Fatalf("hidden %v, want %d", set.HostLocations.Hidden, puts)
	}
	for i := 0; i < adds; i++ {
		found := false
		for _, l := range set.SavedLocations {
			found = found || l.Label == fmt.Sprintf("Race %d", i)
		}
		if !found {
			t.Fatalf("Race %d lost", i)
		}
	}
}

// The chat's save (TestAndSave) writes where GET reads, as the app keeps
// the section: in the settings file, through the settings store.
func TestTestAndSaveShowsInGet(t *testing.T) {
	r := newRig(t)
	store := settings.Open(filepath.Join(r.dir, "ixbase"), r.kb, false, func(string) (string, bool) { return "", false })
	r.s.d.Settings = FileSettings{store}
	a := newAPI(t, r)
	_, before := a.do("GET", "/api/transfers/settings", "", nil)
	if n := len(saved(before)); n != 0 {
		t.Fatalf("%d saved before", n)
	}
	got, err := r.s.TestAndSave(context.Background(), single, SavedLocation{Label: "Drop", Host: "user@host-c", Root: r.hostB})
	if err != nil || got.Location == nil {
		t.Fatalf("saved %+v %v", got, err)
	}
	_, after := a.do("GET", "/api/transfers/settings", "", nil)
	if ids := savedIDs(after); len(ids) != 1 || ids[0] != "drop" {
		t.Fatalf("GET after TestAndSave %v", ids)
	}
	if after["revision"] == before["revision"] {
		t.Fatal("revision did not change")
	}
	// A second store over the same file, as after a restart, reads it too.
	again := FileSettings{settings.Open(filepath.Join(r.dir, "ixbase"), r.kb, false, func(string) (string, bool) { return "", false })}
	if l := again.Load().SavedLocations; len(l) != 1 || l[0].ID != "drop" {
		t.Fatalf("reread %+v", l)
	}
	// And a page that read before the save cannot write it away.
	m := clone(before)
	if st, _ := a.do("PUT", "/api/transfers/settings", "", m); st != 409 {
		t.Fatalf("stale PUT %d", st)
	}
	if l := (FileSettings{store}).Load().SavedLocations; len(l) != 1 {
		t.Fatalf("after stale PUT %+v", l)
	}
}
