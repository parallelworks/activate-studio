package notices

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/chat"
)

// Limits of what is kept of one person.
const (
	keepDismissed = 90 * 24 * time.Hour
	maxDismissed  = 1000
)

// seenReply is the last reply of a chat its person has seen.
type seenReply struct {
	ID string `json:"id"`
	At string `json:"at"`
}

// record is what is kept of one person. Baseline is when their notices
// began: a reply or an ended session from before it is not news.
type record struct {
	Baseline  string               `json:"baseline"`
	Seen      map[string]seenReply `json:"seen,omitempty"`
	Dismissed map[string]string    `json:"dismissed,omitempty"`
	// First is when an item with no time of its own was first listed.
	First map[string]string `json:"first,omitempty"`

	changed bool
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// unread: c has a reply its person has not seen.
func (r *record) unread(c chat.ChatState) bool {
	if c.Reply.ID == "" || !after(c.Reply.At, r.Baseline) {
		return false
	}
	s, ok := r.Seen[c.ID]
	return !ok || (s.ID != c.Reply.ID && after(c.Reply.At, s.At))
}

// see records that the last reply of c was seen.
func (r *record) see(c chat.ChatState) {
	if c.Reply.ID == "" || r.Seen[c.ID].ID == c.Reply.ID {
		return
	}
	if r.Seen == nil {
		r.Seen = map[string]seenReply{}
	}
	r.Seen[c.ID] = seenReply{c.Reply.ID, c.Reply.At}
	r.changed = true
}

func (r *record) dismiss(id string, now time.Time) {
	if r.Dismissed == nil {
		r.Dismissed = map[string]string{}
	}
	r.Dismissed[id] = iso(now)
	r.changed = true
	for k, at := range r.Dismissed {
		if now.Sub(parse(at)) > keepDismissed {
			delete(r.Dismissed, k)
		}
	}
	for len(r.Dismissed) > maxDismissed {
		oldest := ""
		for k, at := range r.Dismissed {
			if oldest == "" || at < r.Dismissed[oldest] {
				oldest = k
			}
		}
		delete(r.Dismissed, oldest)
	}
}

func (r *record) first(id string, now time.Time) string {
	if at := r.First[id]; at != "" {
		return at
	}
	if r.First == nil {
		r.First = map[string]string{}
	}
	r.First[id] = iso(now)
	r.changed = true
	return r.First[id]
}

// keepFirst forgets the times of the items no longer listed.
func (r *record) keepFirst(listed map[string]bool) {
	for id := range r.First {
		if !listed[id] {
			delete(r.First, id)
			r.changed = true
		}
	}
}

// store keeps each person's record in its own file, read once.
type store struct {
	dir string
	mu  sync.Mutex
	rec map[string]*record
}

func (s *store) file(owner string) string {
	h := sha256.Sum256([]byte(owner))
	return filepath.Join(s.dir, hex.EncodeToString(h[:12])+".json")
}

// with runs fn on owner's record, and writes it when fn changed it. The
// first call for a person sets their baseline.
func (s *store) with(owner string, now time.Time, fn func(r *record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rec == nil {
		s.rec = map[string]*record{}
	}
	r := s.rec[owner]
	if r == nil {
		r = &record{}
		if b, err := os.ReadFile(s.file(owner)); err == nil {
			_ = json.Unmarshal(b, r)
		}
		s.rec[owner] = r
	}
	if parse(r.Baseline).IsZero() {
		r.Baseline, r.changed = iso(now), true
	}
	fn(r)
	if !r.changed {
		return nil
	}
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".notices-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.file(owner))
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	r.changed = false
	return nil
}
