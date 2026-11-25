package transfers

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// History limits: finished transfers are kept this long, and at most this
// many, the oldest dropped first.
const (
	KeepFinishedFor = 180 * 24 * time.Hour
	KeepFinishedMax = 10000
)

// rec is an active transfer: what the page sees, whose it is, and its
// staging folder, so a restart can clear it.
type rec struct {
	Transfer
	Owner string `json:"owner"`
	// Stage is the staging folder relative to the destination's root.
	Stage string `json:"stage,omitempty"`

	plan    *planRec
	viewer  Viewer
	samples []sample
}

type sample struct {
	at    time.Time
	bytes int64
}

// doneRec is a finished transfer as the list keeps it in memory: the
// transfer without its events, and where its full line is.
type doneRec struct {
	t     Transfer
	owner string
	end   time.Time
	text  string // lower case: label, location labels, paths
	file  string
	off   int64
	n     int
}

// store keeps active transfers in memory and in a small file
// (INDEX_BASE/transfers/active.json, written as they change), and writes
// each finished transfer once, as a line of INDEX_BASE/transfers/
// <YYYY-MM>.jsonl with its last 200 events. A deletion is a line of its
// own. At start the month files are read into an index of finished
// transfers, newest first; their events are read from the file on demand.
type store struct {
	mu         sync.Mutex
	dir        string
	log        *slog.Logger
	active     map[string]*rec
	done       []*doneRec // newest first
	byID       map[string]*doneRec
	lastActive time.Time
	// frozen: the server is stopping, and what is on disk stays as it is,
	// so the next start finds running jobs as they were and settles them.
	frozen bool
	// seen is what each owner has seen of their failures, kept in
	// INDEX_BASE/transfers/seen.json (seen.go).
	seen map[string]*seenRec
}

type activeFile struct {
	Transfers []*rec `json:"transfers"`
}

type doneLine struct {
	*rec
	Deleted string `json:"deleted,omitempty"`
}

func openStore(indexBase string, log *slog.Logger) *store {
	s := &store{active: map[string]*rec{}, byID: map[string]*doneRec{}, log: log, seen: map[string]*seenRec{}}
	if indexBase == "" {
		return s
	}
	s.dir = filepath.Join(indexBase, "transfers")
	s.loadSeen()
	if b, err := os.ReadFile(filepath.Join(s.dir, "active.json")); err == nil {
		var f activeFile
		if json.Unmarshal(b, &f) == nil {
			for _, r := range f.Transfers {
				if r != nil && r.ID != "" {
					s.active[r.ID] = r
				}
			}
		}
	}
	s.load(time.Now())
	// One already finished is not active any more.
	for id := range s.active {
		if s.byID[id] != nil {
			delete(s.active, id)
		}
	}
	return s
}

// load reads the month files, newest kept first, dropping what is past
// the limits; month files wholly past the time limit are removed.
func (s *store) load(now time.Time) {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.jsonl"))
	sort.Strings(files)
	deleted := map[string]bool{}
	var all []*doneRec
	for _, f := range files {
		month, err := time.Parse("2006-01", strings.TrimSuffix(filepath.Base(f), ".jsonl"))
		if err == nil && now.Sub(month.AddDate(0, 1, 0)) > KeepFinishedFor {
			os.Remove(f)
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		rd := bufio.NewReaderSize(fh, 1<<20)
		var off int64
		for {
			line, err := rd.ReadBytes('\n')
			if len(line) > 0 {
				s.readLine(line, f, off, deleted, &all)
			}
			off += int64(len(line))
			if err != nil {
				break
			}
		}
		fh.Close()
	}
	// A transfer written twice (a stop that raced a start) is kept once,
	// as its last line has it.
	last := map[string]*doneRec{}
	for _, d := range all {
		last[d.t.ID] = d
	}
	var kept []*doneRec
	for _, d := range all {
		if last[d.t.ID] == d && !deleted[d.t.ID] && now.Sub(d.end) <= KeepFinishedFor {
			kept = append(kept, d)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return newer(kept[i], kept[j]) })
	if len(kept) > KeepFinishedMax {
		kept = kept[:KeepFinishedMax]
	}
	s.done = kept
	for _, d := range kept {
		s.byID[d.t.ID] = d
	}
}

// lean is how a line is read at start: the events are skipped.
type lean struct {
	Transfer
	Events  json.RawMessage `json:"events"`
	Owner   string          `json:"owner"`
	Deleted string          `json:"deleted"`
}

func (s *store) readLine(line []byte, file string, off int64, deleted map[string]bool, all *[]*doneRec) {
	var l lean
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if l.Deleted != "" {
		deleted[l.Deleted] = true
		return
	}
	if l.ID == "" {
		return
	}
	t := l.Transfer
	t.Events = nil
	*all = append(*all, s.doneOf(t, l.Owner, file, off, len(line)))
}

func (s *store) doneOf(t Transfer, owner, file string, off int64, n int) *doneRec {
	end, err := time.Parse(time.RFC3339, t.EndedAt)
	if err != nil {
		end, _ = time.Parse(time.RFC3339, t.RequestedAt)
	}
	parts := []string{t.Label, t.Source.Label, t.Destination.Label}
	parts = append(parts, t.Source.Paths...)
	if t.Destination.Path != nil {
		parts = append(parts, *t.Destination.Path)
	}
	return &doneRec{t: t, owner: owner, end: end, text: strings.ToLower(strings.Join(parts, "\x00")), file: file, off: off, n: n}
}

func newer(a, b *doneRec) bool {
	if !a.end.Equal(b.end) {
		return a.end.After(b.end)
	}
	return a.t.ID > b.t.ID
}

// saveActiveLocked writes the active file now, or within a few seconds
// when only progress changed (force false).
func (s *store) saveActiveLocked(force bool, now time.Time) {
	if s.dir == "" || s.frozen || (!force && now.Sub(s.lastActive) < 5*time.Second) {
		return
	}
	list := make([]*rec, 0, len(s.active))
	for _, r := range s.active {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RequestedAt > list[j].RequestedAt })
	b, err := json.MarshalIndent(activeFile{list}, "", " ")
	if err != nil {
		return
	}
	if err := writeAtomic(filepath.Join(s.dir, "active.json"), b); err != nil {
		s.log.Warn("transfers: saving the active list", "error", err)
		return
	}
	s.lastActive = now
}

func writeAtomic(file string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// appendLine adds a line to the month file of t and says where it went.
func (s *store) appendLine(t time.Time, v any) (string, int64, int, error) {
	if s.dir == "" || s.frozen {
		return "", 0, 0, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", 0, 0, err
	}
	b = append(b, '\n')
	os.MkdirAll(s.dir, 0o755)
	file := filepath.Join(s.dir, t.UTC().Format("2006-01")+".jsonl")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	if _, err := f.Write(b); err != nil {
		return "", 0, 0, err
	}
	return file, st.Size(), len(b), nil
}

// finishLocked moves an ended transfer from the active list to the
// history: one line, written once.
func (s *store) finishLocked(r *rec, now time.Time) {
	delete(s.active, r.ID)
	if len(r.Events) > keepEvents {
		r.Events = r.Events[len(r.Events)-keepEvents:]
	}
	file, off, n, err := s.appendLine(now, doneLine{rec: r})
	if err != nil {
		s.log.Warn("transfers: saving a finished transfer", "error", err)
	}
	t := r.view(false)
	if old := s.byID[t.ID]; old != nil {
		for i, x := range s.done {
			if x == old {
				s.done = append(s.done[:i:i], s.done[i+1:]...)
				break
			}
		}
	}
	d := s.doneOf(t, r.Owner, file, off, n)
	if d.end.IsZero() {
		d.end = now
	}
	s.done = append([]*doneRec{d}, s.done...)
	s.byID[t.ID] = d
	if len(s.done) > KeepFinishedMax {
		for _, x := range s.done[KeepFinishedMax:] {
			delete(s.byID, x.t.ID)
		}
		s.done = s.done[:KeepFinishedMax]
	}
	s.saveActiveLocked(true, now)
}

// full is a finished transfer with its events, read from its line.
func (s *store) full(d *doneRec) Transfer {
	t := d.t
	t.Events = []Event{}
	if d.file == "" {
		return t
	}
	f, err := os.Open(d.file)
	if err != nil {
		return t
	}
	defer f.Close()
	b := make([]byte, d.n)
	if _, err := f.ReadAt(b, d.off); err != nil && err != io.EOF {
		return t
	}
	var x struct {
		Events []Event `json:"events"`
	}
	if json.Unmarshal(bytes.TrimSpace(b), &x) == nil && x.Events != nil {
		t.Events = x.Events
	}
	return t
}

// removeLocked drops a finished transfer, with a line that says so.
func (s *store) removeLocked(id string, now time.Time) {
	d := s.byID[id]
	if d == nil {
		return
	}
	delete(s.byID, id)
	for i, x := range s.done {
		if x == d {
			s.done = append(s.done[:i:i], s.done[i+1:]...)
			break
		}
	}
	s.appendLine(now, doneLine{Deleted: id})
}

func (r *rec) event(now time.Time, format string, a ...any) {
	r.Events = append(r.Events, Event{At: iso(now), Text: fmt.Sprintf(format, a...)})
	if len(r.Events) > keepEvents {
		r.Events = append([]Event(nil), r.Events[len(r.Events)-keepEvents:]...)
	}
}

// progress records how far it got, and the rate over the last 10 s.
func (r *rec) progress(now time.Time, files, bytes int64) {
	r.Files.Done, r.Bytes.Done = files, bytes
	r.samples = append(r.samples, sample{now, bytes})
	for len(r.samples) > 1 && now.Sub(r.samples[0].at) > 10*time.Second {
		r.samples = r.samples[1:]
	}
	if len(r.samples) < 2 {
		return
	}
	first := r.samples[0]
	dt := now.Sub(first.at).Seconds()
	if dt < 0.5 {
		return
	}
	rate := float64(bytes-first.bytes) / dt
	if rate < 0 {
		rate = 0
	}
	r.Rate = &rate
	r.EtaSeconds = nil
	if rate > 0 && r.Bytes.Total > 0 {
		eta := int64(float64(r.Bytes.Total-bytes)/rate + 0.5)
		if eta < 0 {
			eta = 0
		}
		r.EtaSeconds = &eta
	}
}

// view is the transfer as the page sees it; events only in the detail.
func (r *rec) view(events bool) Transfer {
	t := r.Transfer
	if events {
		t.Events = append([]Event{}, r.Events...)
	} else {
		t.Events = nil
	}
	if finished(t.State) {
		t.Rate, t.EtaSeconds = nil, nil
	}
	return t
}

// ListQuery is what the list route filters by.
type ListQuery struct {
	// State: active, done or all.
	State string
	Limit int
	// Before is the cursor a previous page gave as next.
	Before string
	// Q matches the label, the location labels and the paths, in any case.
	Q string
	// Outcome is one finished state; StartedBy user, assistant or library.
	Outcome, StartedBy string
	Since              time.Time
}

// Page is a page of the list: active transfers (never paged) and finished
// ones, newest first, with the cursor of the next older page.
type Page struct {
	Transfers []Transfer `json:"transfers"`
	Next      *string    `json:"next"`
}

func cursorOf(d *doneRec) string {
	return base64.RawURLEncoding.EncodeToString([]byte(d.end.UTC().Format(time.RFC3339Nano) + "|" + d.t.ID))
}

func parseCursor(c string) (time.Time, string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", false
	}
	ts, id, ok := strings.Cut(string(b), "|")
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, id, ok && err == nil
}

func (s *store) list(owner string, q ListQuery) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Page{Transfers: []Transfer{}}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	if q.State != "done" && q.Before == "" && q.Outcome == "" {
		var act []Transfer
		for _, r := range s.active {
			if r.Owner != owner || (q.StartedBy != "" && r.StartedBy != q.StartedBy) {
				continue
			}
			if needle != "" && !strings.Contains(s.doneOf(r.Transfer, "", "", 0, 0).text, needle) {
				continue
			}
			act = append(act, r.view(false))
		}
		sort.Slice(act, func(i, j int) bool {
			if act[i].RequestedAt != act[j].RequestedAt {
				return act[i].RequestedAt > act[j].RequestedAt
			}
			return act[i].ID > act[j].ID
		})
		out.Transfers = append(out.Transfers, act...)
	}
	if q.State == "active" {
		return out, nil
	}
	start := 0
	if q.Before != "" {
		t, id, ok := parseCursor(q.Before)
		if !ok {
			return out, fmt.Errorf("before is not a cursor this list gave")
		}
		mark := &doneRec{end: t, t: Transfer{ID: id}}
		// The first record older than the cursor's.
		start = sort.Search(len(s.done), func(i int) bool { return newer(mark, s.done[i]) })
	}
	var last *doneRec
	n := 0
	for i := start; i < len(s.done); i++ {
		d := s.done[i]
		if !q.Since.IsZero() && d.end.Before(q.Since) {
			break
		}
		if d.owner != owner || (q.Outcome != "" && d.t.State != q.Outcome) || (q.StartedBy != "" && d.t.StartedBy != q.StartedBy) ||
			(needle != "" && !strings.Contains(d.text, needle)) {
			continue
		}
		if n == q.Limit {
			c := cursorOf(last)
			out.Next = &c
			break
		}
		out.Transfers = append(out.Transfers, d.t)
		last, n = d, n+1
	}
	return out, nil
}

// summary is the counts the page header shows.
func (s *store) summary(owner string, now time.Time) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Summary
	for _, r := range s.active {
		if r.Owner != owner {
			continue
		}
		out.Active++
		if r.State == StateAwaiting {
			out.AwaitingApproval++
		}
	}
	out.FailedLast24h = len(s.failuresLocked(owner, now))
	return out
}

// Summary is GET /api/transfers/summary: active counts those waiting for
// approval too. FailedLast24h counts the failures of the last 24 hours
// the viewer has not seen (the Transfers page or the transfer's details
// was not open since) and that no later run of the same transfer
// succeeded.
type Summary struct {
	Active           int `json:"active"`
	AwaitingApproval int `json:"awaitingApproval"`
	FailedLast24h    int `json:"failedLast24h"`
}

// get is one transfer of owner's, active or finished, with its events.
func (s *store) get(owner, id string) (Transfer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.active[id]; r != nil && r.Owner == owner {
		return r.view(true), true
	}
	if d := s.byID[id]; d != nil && d.owner == owner {
		return s.full(d), true
	}
	return Transfer{}, false
}
