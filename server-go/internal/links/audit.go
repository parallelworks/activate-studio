package links

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Audit is the record of links made and opened, one line each, in
// INDEX_BASE/link-log/<year>-<month>.jsonl: when, who, the ID and the
// outcome, and never a path or a name. Nothing here removes it. What an ID
// stands for is in links.json.
type Audit struct {
	dir string
	// Now is replaced in tests.
	Now func() time.Time

	mu sync.Mutex
}

// Entry is one line.
type Entry struct {
	Time string `json:"time"`
	// What is made or opened.
	What string `json:"what"`
	// ID is the link's, or "invalid": what is not an ID could hold a name.
	ID   string `json:"id,omitempty"`
	User string `json:"user,omitempty"`
	// Outcome is made, opened, replaced (opened, with a note that another
	// file is there now), deleted, 404, signed-out, unreachable or error.
	Outcome string `json:"outcome"`
}

// NewAudit is the log under indexBase.
func NewAudit(indexBase string) *Audit {
	return &Audit{dir: filepath.Join(indexBase, "link-log"), Now: time.Now}
}

// Add writes an entry, and answers why it could not.
func (a *Audit) Add(e Entry) error {
	if a == nil {
		return nil
	}
	if e.ID != "" && !ValidID(e.ID) {
		e.ID = "invalid"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.Now().UTC()
	e.Time = t.Format(time.RFC3339)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(a.dir, t.Format("2006-01")+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
