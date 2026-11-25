package transfers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ended adds a finished transfer of owner's to the service's history, as
// a transfer that ends does, copying paths from host A to the main
// library. The history is newest first, so a test adds them oldest first.
func ended(s *Service, owner, id, state string, paths []string, end time.Time) {
	r := &rec{Owner: owner}
	r.ID, r.State, r.Action, r.StartedBy = id, state, ActionCopy, ByUser
	r.Label = "results " + id
	r.Source = End{Location: "saved:a", Label: "Host A", Paths: paths}
	p := "incoming"
	r.Destination = End{Location: "library:kb", Label: "Knowledge base", Path: &p}
	r.RequestedAt, r.EndedAt = iso(end.Add(-time.Second)), iso(end)
	s.store.mu.Lock()
	s.store.finishLocked(r, end)
	s.store.mu.Unlock()
}

func failedNow(t *testing.T, s *Service, v Viewer) int {
	t.Helper()
	return s.Summary(v).FailedLast24h
}

// A failure counts until the Transfers page is opened, and one that ends
// after that counts again until its details are opened. What was seen
// is kept beside the history, readable by its owner only, and a new
// service over the same INDEX_BASE reads it.
func TestSeenFailures(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	s := New(Deps{IndexBase: base})
	ended(s, "", "f0", StateFailed, []string{"run0"}, now.Add(-25*time.Hour))
	ended(s, "", "f1", StateFailed, []string{"run1"}, now.Add(-time.Hour))
	if n := failedNow(t, s, single); n != 1 {
		t.Fatalf("failed before seen: %d", n)
	}
	sum, err := s.Seen(single, "")
	if err != nil || sum.FailedLast24h != 0 {
		t.Fatalf("seen: %+v %v", sum, err)
	}
	// Two more fail after the page was open; one's details are opened.
	ended(s, "", "f2", StateFailed, []string{"run2"}, time.Now().Add(time.Second))
	ended(s, "", "f3", StateFailed, []string{"run3"}, time.Now().Add(2*time.Second))
	if n := failedNow(t, s, single); n != 2 {
		t.Fatalf("failed after the page: %d", n)
	}
	// Failures is the transfers that number counts, newest first.
	if fs := s.Failures(single); len(fs) != 2 || fs[0].ID != "f3" || fs[1].ID != "f2" {
		t.Fatalf("failures: %+v", fs)
	}
	if sum, err := s.Seen(single, "f2"); err != nil || sum.FailedLast24h != 1 {
		t.Fatalf("seen f2: %+v %v", sum, err)
	}
	if _, err := s.Seen(single, "nope"); statusOf(err) != 404 {
		t.Fatalf("an unknown transfer: %v", err)
	}
	fi, err := os.Stat(filepath.Join(base, "transfers", "seen.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("seen.json: %v %v", fi, err)
	}
	again := New(Deps{IndexBase: base})
	if n := failedNow(t, again, single); n != 1 {
		t.Fatalf("after a restart: %d", n)
	}
	if sum, _ := again.Seen(single, "f3"); sum.FailedLast24h != 0 {
		t.Fatalf("seen f3 after a restart: %+v", sum)
	}
}

// A later success of the same transfer (same source location and paths,
// same destination location and path) clears its failure; a success of
// another, or a failure after the success, does not.
func TestLaterSuccessClearsFailure(t *testing.T) {
	s := New(Deps{IndexBase: t.TempDir()})
	now := time.Now()
	ended(s, "", "f1", StateFailed, []string{"b", "a"}, now.Add(-3*time.Hour))
	ended(s, "", "o1", StateSucceeded, []string{"other"}, now.Add(-2*time.Hour))
	if n := failedNow(t, s, single); n != 1 {
		t.Fatalf("a success of another cleared it: %d", n)
	}
	ended(s, "", "s1", StateSucceeded, []string{"a", "b"}, now.Add(-time.Hour))
	if n := failedNow(t, s, single); n != 0 {
		t.Fatalf("after a later success: %d", n)
	}
	ended(s, "", "f2", StateFailed, []string{"a", "b"}, now.Add(-time.Minute))
	if n := failedNow(t, s, single); n != 1 {
		t.Fatalf("a failure after the success: %d", n)
	}
	// Another viewer's success clears nothing of this one's.
	ended(s, "bob", "s2", StateSucceeded, []string{"a", "b"}, now)
	if n := failedNow(t, s, single); n != 1 {
		t.Fatalf("after another's success: %d", n)
	}
}

// With sign-in on, what one person saw is theirs: POST /api/transfers/seen
// clears their mark only, and another's transfer is a 404.
func TestSeenRoutePerViewer(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	ended(r.s, "alice", "fa", StateFailed, []string{"run1"}, now.Add(-time.Hour))
	ended(r.s, "bob", "fb", StateFailed, []string{"run1"}, now.Add(-time.Hour))
	a := newAPI(t, r)
	for _, u := range []string{"alice", "bob"} {
		if st, out := a.do("GET", "/api/transfers/summary", u, nil); st != 200 || out["failedLast24h"].(float64) != 1 {
			t.Fatalf("%s before: %d %v", u, st, out)
		}
	}
	if st, out := a.do("POST", "/api/transfers/seen", "alice", map[string]any{"id": "fb"}); st != 404 || out["error"] == nil {
		t.Fatalf("bob's transfer as alice: %d %v", st, out)
	}
	if st, out := a.do("POST", "/api/transfers/seen", "alice", map[string]any{}); st != 200 || out["failedLast24h"].(float64) != 0 {
		t.Fatalf("alice seen: %d %v", st, out)
	}
	if _, out := a.do("GET", "/api/transfers/summary", "bob", nil); out["failedLast24h"].(float64) != 1 {
		t.Fatalf("bob after alice saw hers: %v", out)
	}
	if st, out := a.do("POST", "/api/transfers/seen", "bob", map[string]any{"id": "fb"}); st != 200 || out["failedLast24h"].(float64) != 0 {
		t.Fatalf("bob seen fb: %d %v", st, out)
	}
	// Sign-in off is one viewer, apart from both.
	ended(r.s, "", "f", StateFailed, []string{"run1"}, now.Add(-time.Hour))
	if _, out := a.do("GET", "/api/transfers/summary", "", nil); out["failedLast24h"].(float64) != 1 {
		t.Fatalf("sign-in off: %v", out)
	}
}
