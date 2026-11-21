package transfers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FailedFor is how long a failure counts in the summary at most.
const FailedFor = 24 * time.Hour

// seenRec is what one owner has seen of their failures: every one that
// ended up to UpTo (the Transfers page was open), and those named in IDs
// (their details were open).
type seenRec struct {
	UpTo time.Time `json:"upTo"`
	IDs  []string  `json:"ids,omitempty"`
}

// seenFile is INDEX_BASE/transfers/seen.json, by owner ("" with sign-in
// off).
type seenFile struct {
	Owners map[string]*seenRec `json:"owners"`
}

func (s *store) loadSeen() {
	s.seen = map[string]*seenRec{}
	if s.dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "seen.json"))
	if err != nil {
		return
	}
	var f seenFile
	if json.Unmarshal(b, &f) == nil {
		for o, r := range f.Owners {
			if r != nil {
				s.seen[o] = r
			}
		}
	}
}

// markSeenLocked records that owner saw their failures: all that ended up
// to now with id "", or the one with that id. It keeps only the ids that
// can still count, and writes the file.
func (s *store) markSeenLocked(owner, id string, now time.Time) {
	r := s.seen[owner]
	if r == nil {
		r = &seenRec{}
		s.seen[owner] = r
	}
	if id == "" {
		// End times are kept to the millisecond.
		if at := now.UTC().Truncate(time.Millisecond); at.After(r.UpTo) {
			r.UpTo = at
		}
	} else if d := s.byID[id]; d != nil && d.owner == owner && d.t.State == StateFailed {
		r.IDs = append(r.IDs, id)
	}
	var keep []string
	for _, x := range r.IDs {
		if d := s.byID[x]; d != nil && d.end.After(r.UpTo) && now.Sub(d.end) <= FailedFor && !contains(keep, x) {
			keep = append(keep, x)
		}
	}
	r.IDs = keep
	if s.dir == "" {
		return
	}
	b, err := json.MarshalIndent(seenFile{s.seen}, "", " ")
	if err != nil {
		return
	}
	if err := writeAtomic(filepath.Join(s.dir, "seen.json"), b); err != nil {
		s.log.Warn("transfers: saving what was seen", "error", err)
	}
}

// unseen says whether a failure of owner's still counts: it ended after
// what the owner saw, and they did not open it.
func (s *store) unseen(owner string, d *doneRec) bool {
	r := s.seen[owner]
	if r == nil {
		return true
	}
	return d.end.After(r.UpTo) && !contains(r.IDs, d.t.ID)
}

// sameKey names a transfer by what it moves: the source location and
// paths, and the destination location and path. A later success with the
// same key clears an earlier failure.
func sameKey(t Transfer) string {
	paths := append([]string{}, t.Source.Paths...)
	sort.Strings(paths)
	dst := ""
	if t.Destination.Path != nil {
		dst = *t.Destination.Path
	}
	return strings.Join(append([]string{t.Source.Location, t.Destination.Location, dst}, paths...), "\x00")
}
