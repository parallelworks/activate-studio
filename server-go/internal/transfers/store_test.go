package transfers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// history writes n finished transfers into month files under base, one a
// minute back from now, and returns their ids newest first. Every third is
// the assistant's, every fifth failed, every seventh is bob's.
func history(t *testing.T, base string, n int, now time.Time) []string {
	t.Helper()
	dir := filepath.Join(base, "transfers")
	os.MkdirAll(dir, 0o755)
	files := map[string]*bufio.Writer{}
	var closers []*os.File
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		end := now.Add(-time.Duration(i+1) * time.Minute)
		r := &rec{Owner: ""}
		if i%7 == 0 {
			r.Owner = "bob"
		}
		r.ID = fmt.Sprintf("t%05d", i)
		ids[i] = r.ID
		r.Label = fmt.Sprintf("results run %d", i)
		r.Action, r.Mover, r.State, r.StartedBy = ActionCopy, MoverRsync, StateSucceeded, ByUser
		if i%3 == 0 {
			r.StartedBy = ByAssistant
		}
		if i%5 == 0 {
			r.State, r.Error = StateFailed, "it failed"
		}
		r.Source = End{Location: "saved:a", Label: "Host A", Paths: []string{fmt.Sprintf("data/run%d", i)}}
		p := ""
		r.Destination = End{Location: "library:kb", Label: "Knowledge base", Path: &p}
		r.RequestedAt, r.EndedAt = iso(end.Add(-time.Second)), iso(end)
		for j := 0; j < 5; j++ {
			r.event(end, "event %d", j)
		}
		month := end.UTC().Format("2006-01")
		w := files[month]
		if w == nil {
			f, err := os.OpenFile(filepath.Join(dir, month+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			closers = append(closers, f)
			w = bufio.NewWriter(f)
			files[month] = w
		}
		b, _ := json.Marshal(doneLine{rec: r})
		w.Write(append(b, '\n'))
	}
	for _, w := range files {
		w.Flush()
	}
	for _, f := range closers {
		f.Close()
	}
	return ids
}

func TestHistoryPagesAndFilters(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	ids := history(t, base, 300, now)
	st := openStore(base, quiet)
	var mine []string
	for i, id := range ids {
		if i%7 != 0 {
			mine = append(mine, id)
		}
	}
	// Page through all of it, 50 at a time, newest first, no repeats.
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		p, err := st.list("", ListQuery{State: "done", Limit: 50, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range p.Transfers {
			got = append(got, tr.ID)
			if tr.Events != nil {
				t.Fatal("a list row carries events")
			}
		}
		if p.Next == nil {
			break
		}
		cursor = *p.Next
		if pages > 10 {
			t.Fatal("paging does not end")
		}
	}
	if strings.Join(got, ",") != strings.Join(mine, ",") {
		t.Fatalf("paged %d of %d", len(got), len(mine))
	}
	// Filters.
	p, _ := st.list("", ListQuery{State: "done", Limit: 200, Outcome: StateFailed, StartedBy: ByAssistant})
	for _, tr := range p.Transfers {
		if tr.State != StateFailed || tr.StartedBy != ByAssistant {
			t.Fatalf("filter let through %+v", tr)
		}
	}
	if len(p.Transfers) == 0 {
		t.Fatal("no failed assistant transfers found")
	}
	p, _ = st.list("", ListQuery{State: "done", Limit: 50, Q: "DATA/RUN12"})
	for _, tr := range p.Transfers {
		if !strings.HasPrefix(tr.Source.Paths[0], "data/run12") {
			t.Fatalf("q let through %v", tr.Source.Paths)
		}
	}
	want := 0
	for i := range ids {
		if i%7 != 0 && strings.HasPrefix(fmt.Sprintf("data/run%d", i), "data/run12") {
			want++
		}
	}
	if len(p.Transfers) != want || p.Next != nil {
		t.Fatalf("q found %d", len(p.Transfers))
	}
	p, _ = st.list("", ListQuery{State: "done", Limit: 50, Since: now.Add(-10*time.Minute - time.Second)})
	if len(p.Transfers) != 8 {
		t.Fatalf("since found %d", len(p.Transfers))
	}
	if _, err := st.list("", ListQuery{State: "done", Before: "nonsense"}); err == nil {
		t.Fatal("a bad cursor was taken")
	}
	// Bob's are bob's; the detail reads the events from the file.
	p, _ = st.list("bob", ListQuery{State: "all", Limit: 200})
	if len(p.Transfers) != 43 {
		t.Fatalf("bob has %d", len(p.Transfers))
	}
	tr, ok := st.get("bob", ids[0])
	if !ok || len(tr.Events) != 5 || tr.Events[4].Text != "event 4" {
		t.Fatalf("detail %+v", tr)
	}
	if _, ok := st.get("", ids[0]); ok {
		t.Fatal("bob's transfer read by another")
	}
	// A deletion is kept across a restart.
	st.mu.Lock()
	st.removeLocked(ids[1], now)
	st.mu.Unlock()
	if _, ok := openStore(base, quiet).get("", ids[1]); ok {
		t.Fatal("deleted transfer came back")
	}
	sum := st.summary("", now)
	if sum.Active != 0 || sum.FailedLast24h == 0 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestHistoryRetention(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	dir := filepath.Join(base, "transfers")
	os.MkdirAll(dir, 0o755)
	old := now.Add(-KeepFinishedFor - 48*time.Hour)
	line := func(id string, end time.Time) string {
		r := &rec{}
		r.ID, r.State, r.EndedAt, r.RequestedAt = id, StateSucceeded, iso(end), iso(end)
		b, _ := json.Marshal(doneLine{rec: r})
		return string(b) + "\n"
	}
	// A month file wholly past the limit is removed; an old line in a live
	// month file is dropped.
	ancient := filepath.Join(dir, now.AddDate(-1, 0, 0).UTC().Format("2006-01")+".jsonl")
	os.WriteFile(ancient, []byte(line("ancient", now.AddDate(-1, 0, 0))), 0o600)
	os.WriteFile(filepath.Join(dir, old.UTC().Format("2006-01")+".jsonl"), []byte(line("old", old)+line("new", now.Add(-time.Hour))), 0o600)
	st := openStore(base, quiet)
	if _, ok := st.get("", "old"); ok {
		t.Fatal("kept past 180 days")
	}
	if _, ok := st.get("", "new"); !ok {
		t.Fatal("dropped a recent one")
	}
	if _, err := os.Stat(ancient); err == nil {
		t.Fatal("an expired month file is still there")
	}
}

// 10,000 finished transfers: loading under a second, a page and a filtered
// page under 50 ms each.
func TestHistoryAtScale(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	history(t, base, KeepFinishedMax+500, now)
	t0 := time.Now()
	st := openStore(base, quiet)
	load := time.Since(t0)
	if len(st.done) != KeepFinishedMax {
		t.Fatalf("kept %d", len(st.done))
	}
	t0 = time.Now()
	p, err := st.list("", ListQuery{State: "all", Limit: 50})
	page := time.Since(t0)
	if err != nil || len(p.Transfers) != 50 || p.Next == nil {
		t.Fatalf("page %d %v", len(p.Transfers), err)
	}
	t0 = time.Now()
	p2, _ := st.list("", ListQuery{State: "done", Limit: 50, Before: *p.Next, Q: "run 9", Outcome: StateSucceeded})
	filtered := time.Since(t0)
	if len(p2.Transfers) == 0 {
		t.Fatal("filtered page empty")
	}
	// The worst case: a filter nothing matches reads every record.
	t0 = time.Now()
	p3, _ := st.list("", ListQuery{State: "all", Limit: 50, Q: "matches nothing"})
	none := time.Since(t0)
	if len(p3.Transfers) != 0 || p3.Next != nil {
		t.Fatal("matched something")
	}
	t.Logf("load %v, page %v, filtered page %v, no match %v", load, page, filtered, none)
	if load > time.Second || page > 50*time.Millisecond || filtered > 50*time.Millisecond || none > 50*time.Millisecond {
		t.Fatalf("too slow: load %v, page %v, filtered %v", load, page, filtered)
	}
}

// A transfer written twice is listed once, and one finished on disk is not
// active again.
func TestHistoryWrittenTwice(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "transfers")
	os.MkdirAll(dir, 0o755)
	now := time.Now()
	r := &rec{}
	r.ID, r.State, r.RequestedAt, r.EndedAt = "dup", StateInterrupted, iso(now.Add(-time.Hour)), iso(now.Add(-time.Minute))
	b1, _ := json.Marshal(doneLine{rec: r})
	r.EndedAt = iso(now)
	b2, _ := json.Marshal(doneLine{rec: r})
	os.WriteFile(filepath.Join(dir, now.UTC().Format("2006-01")+".jsonl"), []byte(string(b1)+"\n"+string(b2)+"\n"), 0o600)
	a := *r
	a.State = StateRunning
	b, _ := json.Marshal(activeFile{[]*rec{&a}})
	os.WriteFile(filepath.Join(dir, "active.json"), b, 0o600)
	st := openStore(base, quiet)
	p, _ := st.list("", ListQuery{State: "all", Limit: 50})
	if len(p.Transfers) != 1 || p.Transfers[0].State != StateInterrupted || p.Transfers[0].EndedAt != iso(now) {
		t.Fatalf("%+v", p.Transfers)
	}
}
