package transfers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocationsForOneUser(t *testing.T) {
	r := newRig(t)
	r.env["STUDIO_TRANSFER_FOLDERS"] = r.dir + "/missing"
	ls := r.s.Locations(context.Background(), Viewer{Key: "k"})
	by := map[string]Location{}
	for _, l := range ls {
		by[l.ID] = l
	}
	for _, id := range []string{"library:kb", "library:ro", "library:far", "computer:Downloads", "saved:a", "saved:b", "cluster:gpu-"} {
		if _, ok := by[id]; !ok {
			t.Fatalf("no %s in %v", id, ls)
		}
	}
	// Sign-in off: a second library in a folder Studio reaches is writable
	// for transfers, though Studio does not index it after one.
	if !by["library:kb"].Writable || !by["library:kb"].Indexed || !by["library:ro"].Writable || by["library:ro"].Indexed {
		t.Fatalf("library flags %+v %+v", by["library:kb"], by["library:ro"])
	}
	if f := by["library:far"]; f.Available || f.Browsable || f.Why == "" {
		t.Fatalf("index-only library %+v", f)
	}
	if m := by["computer:missing"]; m.Available || m.Why == "" {
		t.Fatalf("a missing folder from the environment %+v", m)
	}
	if _, ok := by["computer:Desktop"]; ok {
		t.Fatal("a Desktop that is not there is offered")
	}
	if !by["saved:a"].Available {
		t.Fatalf("saved %+v", by["saved:a"])
	}
	// A folder that is home itself, or holds the Studio's data, is refused.
	if why := r.s.refusedFolder("/"); why == "" {
		t.Fatal("the whole file system offered")
	}
	if why := r.s.refusedFolder(r.dir); why == "" {
		t.Fatal("a folder holding INDEX_BASE offered")
	}
}

func TestBrowse(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "data", "x.txt"), "x")
	write(t, filepath.Join(r.hostA, "data", ".secret"), "s")
	os.Symlink("/etc", filepath.Join(r.hostA, "etc-link"))
	ctx := context.Background()
	p := "data"
	l, err := r.s.Browse(ctx, single, "saved:a", &p)
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Name != "x.txt" || l.Parent == nil || *l.Parent != "" {
		t.Fatalf("browse %+v %v", l, err)
	}
	for path, want := range map[string]int{"nope": 404, "../x": 400, "/etc": 400, "etc-link": 400, ".secret": 400} {
		p := path
		if _, err := r.s.Browse(ctx, single, "saved:a", &p); statusOf(err) != want {
			t.Fatalf("browse %q: %v, want %d", path, err, want)
		}
	}
	if _, err := r.s.Browse(ctx, single, "library:far", nil); statusOf(err) != 400 {
		t.Fatalf("index-only library: %v", err)
	}
	if _, err := r.s.Browse(ctx, single, "saved:zz", nil); statusOf(err) != 404 {
		t.Fatalf("unknown location: %v", err)
	}
	// A saved host that cannot be reached is 502.
	r.set.S.SavedLocations[0].Root = r.dir + "/no-such-root"
	if _, err := r.s.Browse(ctx, single, "saved:a", nil); statusOf(err) != 502 {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestPlanCounts(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "proj", "a.txt"), "aaaa")
	write(t, filepath.Join(r.hostA, "proj", "sub", "b.txt"), "bb")
	write(t, filepath.Join(r.hostA, "proj", ".git", "HEAD"), "ref")
	os.Symlink("/etc/passwd", filepath.Join(r.hostA, "proj", "pw"))
	write(t, filepath.Join(r.hostA, "notes.md"), "n")
	p := r.mustPlan(single, req(ActionCopy, "saved:a", []string{"proj", "notes.md"}, "library:kb", ""))
	if p.Files != 3 || p.Bytes != 7 || p.Dirs != 2 || p.Skipped.Hidden != 1 || p.Skipped.Links != 1 {
		t.Fatalf("counts %+v", p)
	}
	if p.ReplacesCount != 0 || len(p.Replaces) != 0 || p.DeletesCount != 0 || p.FreeBytes == nil {
		t.Fatalf("lists %+v", p)
	}
	if p.Mover == "" || len(p.Movers) != 3 || p.ExpiresAt == "" || len(p.Warnings) < 2 {
		t.Fatalf("mover %+v", p)
	}
	if p.Destination.Path == nil || *p.Destination.Path != "" || p.Source.Label != "Host A" {
		t.Fatalf("ends %+v %+v", p.Source, p.Destination)
	}
	// Refusals a person can act on.
	for name, q := range map[string]Request{
		"traversal":   req(ActionCopy, "saved:a", []string{"../etc"}, "saved:b", ""),
		"absolute":    req(ActionCopy, "saved:a", []string{"/etc/passwd"}, "saved:b", ""),
		"hidden":      req(ActionCopy, "saved:a", []string{"proj/.git"}, "saved:b", ""),
		"nested":      req(ActionCopy, "saved:a", []string{"proj", "proj/a.txt"}, "saved:b", ""),
		"into itself": req(ActionCopy, "saved:a", []string{"proj"}, "saved:a", "proj/sub"),
		"no dest":     req(ActionCopy, "saved:a", []string{"proj"}, "saved:b", "nope"),
		"sync two":    req(ActionSync, "saved:a", []string{"proj", "notes.md"}, "saved:b", ""),
		"sync file":   req(ActionSync, "saved:a", []string{"notes.md"}, "saved:b", ""),
		"delete extra": func() Request {
			q := req(ActionCopy, "saved:a", []string{"proj"}, "saved:b", "")
			q.Options.DeleteExtra = true
			return q
		}(),
		"link item":   req(ActionCopy, "saved:a", []string{"proj/pw"}, "saved:b", ""),
		"same folder": req(ActionCopy, "saved:a", []string{"proj/a.txt"}, "saved:a", "proj"),
		"index only":  req(ActionCopy, "library:far", []string{"x"}, "saved:b", ""),
		"bad action":  req("delete", "saved:a", []string{"proj"}, "saved:b", ""),
	} {
		if _, err := r.plan(single, q); err == nil || statusOf(err) < 400 || statusOf(err) == 404 && name != "no dest" {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestReplaceAndConflicts(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "proj", "a.txt"), "new a")
	write(t, filepath.Join(r.hostA, "proj", "sub", "b.txt"), "new b")
	write(t, filepath.Join(r.hostB, "proj", "a.txt"), "old")
	write(t, filepath.Join(r.hostB, "proj", "keep.txt"), "keep")
	q := req(ActionCopy, "saved:a", []string{"proj"}, "saved:b", "")
	if _, err := r.plan(single, q); statusOf(err) != 400 || !strings.Contains(messageOf(err), "proj is already in") {
		t.Fatalf("existing name without replace: %v", err)
	}
	q.Options.Replace = true
	p := r.mustPlan(single, q)
	if p.ReplacesCount != 1 || p.Replaces[0] != "proj/a.txt" || p.Files != 2 {
		t.Fatalf("replace plan %+v", p)
	}
	tr := r.run(single, q)
	if tr.State != StateSucceeded || tr.Replaced != 1 {
		t.Fatalf("replace run %+v", tr)
	}
	if read(t, filepath.Join(r.hostB, "proj", "a.txt")) != "new a" || read(t, filepath.Join(r.hostB, "proj", "keep.txt")) != "keep" ||
		read(t, filepath.Join(r.hostB, "proj", "sub", "b.txt")) != "new b" {
		t.Fatal("replace landed wrong")
	}
	noStaging(t, r.hostB)
	// A file where a folder must go, and a folder where a file must go.
	write(t, filepath.Join(r.hostA, "x", "sub"), "file")
	os.MkdirAll(filepath.Join(r.hostB, "x", "sub"), 0o755)
	q = req(ActionCopy, "saved:a", []string{"x"}, "saved:b", "")
	q.Options.Replace = true
	if _, err := r.plan(single, q); statusOf(err) != 400 {
		t.Fatalf("folder replaced by a file: %v", err)
	}
}

func TestSyncChangesAndDeleteExtra(t *testing.T) {
	r := newRig(t)
	old := time.Unix(1600000000, 0)
	write(t, filepath.Join(r.hostA, "src", "same.txt"), "same")
	write(t, filepath.Join(r.hostA, "src", "changed.txt"), "changed now")
	write(t, filepath.Join(r.hostA, "src", "touched.txt"), "tttt")
	write(t, filepath.Join(r.hostA, "src", "new", "n.txt"), "n")
	os.Chtimes(filepath.Join(r.hostA, "src", "same.txt"), old, old)
	write(t, filepath.Join(r.kb, "mirror", "same.txt"), "same")
	os.Chtimes(filepath.Join(r.kb, "mirror", "same.txt"), old, old)
	write(t, filepath.Join(r.kb, "mirror", "changed.txt"), "before")
	write(t, filepath.Join(r.kb, "mirror", "touched.txt"), "tttt")
	os.Chtimes(filepath.Join(r.kb, "mirror", "touched.txt"), old, old)
	write(t, filepath.Join(r.kb, "mirror", "extra.txt"), "extra")
	write(t, filepath.Join(r.kb, "mirror", "olddir", "o.txt"), "o")
	write(t, filepath.Join(r.kb, "mirror", ".keep"), "hidden stays")

	real := r.s.movers
	r.s.movers = append([]Mover{&liar{}}, real...)
	q := req(ActionSync, "saved:a", []string{"src"}, "library:kb", "mirror")
	q.Options.Mover = "liar"
	p := r.mustPlan(single, q)
	if p.Files != 3 || p.ReplacesCount != 2 || p.DeletesCount != 0 {
		t.Fatalf("sync without deleteExtra %+v", p)
	}
	q.Options.DeleteExtra = true
	p = r.mustPlan(single, q)
	if p.DeletesCount != 2 || strings.Join(p.Deletes, ",") != "mirror/extra.txt,mirror/olddir/o.txt" {
		t.Fatalf("deletes %+v", p.Deletes)
	}
	// Checksum: same size and time differ in content only when hashed.
	qc := q
	qc.Options.Checksum = yes
	pc := r.mustPlan(single, qc)
	if pc.Files != 2 { // touched.txt has the same bytes; changed and new go
		t.Fatalf("checksum sync %+v", pc)
	}
	// A file extra by the time of confirming is a deletion the plan did not
	// show: refused. Gone again, the same plan starts.
	write(t, filepath.Join(r.kb, "mirror", "later.txt"), "later")
	if _, err := r.s.Start(context.Background(), single, p.ID, ByUser, ""); statusOf(err) != 409 {
		t.Fatalf("a new deletion at confirm: %v", err)
	}
	os.Remove(filepath.Join(r.kb, "mirror", "later.txt"))
	// One that appears while the job runs is not deleted: only what the
	// plan listed goes.
	r.s.movers = []Mover{&liar{write: func(j *MoveJob) {
		copyFiles(context.Background(), j, &procJob{})
		write(t, filepath.Join(r.kb, "mirror", "later.txt"), "later")
	}}}
	tr, err := r.s.Start(context.Background(), single, p.ID, ByUser, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	tr = r.wait(single, tr.ID)
	if tr.State != StateSucceeded || tr.Deleted != 2 || tr.Replaced != 2 {
		t.Fatalf("sync %+v", tr)
	}
	if read(t, filepath.Join(r.kb, "mirror", "changed.txt")) != "changed now" || read(t, filepath.Join(r.kb, "mirror", "new", "n.txt")) != "n" {
		t.Fatal("sync did not send the changes")
	}
	gone(t, filepath.Join(r.kb, "mirror", "extra.txt"))
	gone(t, filepath.Join(r.kb, "mirror", "olddir"))
	read(t, filepath.Join(r.kb, "mirror", "later.txt"))
	read(t, filepath.Join(r.kb, "mirror", ".keep"))
	if len(r.ix.landed) == 0 || len(r.ix.removed) != 2 {
		t.Fatalf("index heard landed %v removed %v", r.ix.landed, r.ix.removed)
	}
	// Synced again: nothing to do.
	r.s.movers = real
	p = r.mustPlan(single, req(ActionSync, "saved:a", []string{"src"}, "library:kb", "mirror"))
	if p.Files != 0 {
		t.Fatalf("second sync %+v", p)
	}
}

func TestPlanExpiryAndGrowth(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.hostA, "d", "a.txt"), "a")
	q := req(ActionCopy, "saved:a", []string{"d"}, "saved:b", "")
	p := r.mustPlan(single, q)
	now := time.Now()
	r.s.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := r.s.Start(context.Background(), single, p.ID, ByUser, ""); statusOf(err) != 409 {
		t.Fatalf("expired plan: %v", err)
	}
	r.s.now = time.Now
	p = r.mustPlan(single, q)
	write(t, filepath.Join(r.hostA, "d", "b.txt"), "b")
	if _, err := r.s.Start(context.Background(), single, p.ID, ByUser, ""); statusOf(err) != 409 || !strings.Contains(messageOf(err), "2 files") {
		t.Fatalf("grown plan: %v", err)
	}
	// A replacement the plan did not show refuses too.
	write(t, filepath.Join(r.hostB, "d", "c.txt"), "old")
	write(t, filepath.Join(r.hostA, "d", "c.txt"), "new")
	q.Options.Replace = true
	p = r.mustPlan(single, q)
	write(t, filepath.Join(r.hostB, "d", "a.txt"), "now there")
	os.Remove(filepath.Join(r.hostA, "d", "b.txt"))
	if _, err := r.s.Start(context.Background(), single, p.ID, ByUser, ""); statusOf(err) != 409 || !strings.Contains(messageOf(err), "replaced") {
		t.Fatalf("new replacement: %v", err)
	}
	if _, err := r.s.Start(context.Background(), single, "nope", ByUser, ""); statusOf(err) != 404 {
		t.Fatalf("unknown plan: %v", err)
	}
}

// liar is a mover that says it is done, having written what it likes.
type liar struct {
	inProcess
	write func(j *MoveJob)
}

func (l *liar) Name() string                                      { return "liar" }
func (l *liar) PollEvery() time.Duration                          { return 10 * time.Millisecond }
func (l *liar) CanReach(context.Context, *planRec) (bool, string) { return true, "it says so" }
func (l *liar) Start(ctx context.Context, j *MoveJob) (string, error) {
	return l.start(func(ctx context.Context, _ *procJob) error { l.write(j); return nil }), nil
}
func (l *liar) Status(_ context.Context, ref string) (MoverStatus, error) { return l.status(ref), nil }
func (l *liar) Cancel(ref string) bool                                    { return l.cancel(ref) }

func TestALyingMoverFailsTheJob(t *testing.T) {
	for name, w := range map[string]func(r *rig, j *MoveJob){
		"nothing written": func(r *rig, j *MoveJob) {},
		"short file": func(r *rig, j *MoveJob) {
			for _, f := range j.Files {
				j.Dst.Write(context.Background(), join(j.Stage, f.Out), strings.NewReader("x"), 0, 0o644)
			}
		},
		"extra file": func(r *rig, j *MoveJob) {
			copyFiles(context.Background(), j, &procJob{})
			j.Dst.Write(context.Background(), join(j.Stage, "d/planted.sh"), strings.NewReader("x"), 0, 0o644)
		},
		"same size other bytes": func(r *rig, j *MoveJob) {
			for _, f := range j.Files {
				j.Dst.Write(context.Background(), join(j.Stage, f.Out), strings.NewReader(strings.Repeat("z", int(f.Size))), 0, 0o644)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			write(t, filepath.Join(r.kb, "d", "a.txt"), "alpha")
			write(t, filepath.Join(r.kb, "d", "b.txt"), "bravo")
			r.s.movers = []Mover{&liar{write: func(j *MoveJob) { w(r, j) }}}
			// A move checks sha256, so even same-size lies fail.
			tr := r.run(single, req(ActionMove, "library:kb", []string{"d"}, "saved:b", ""))
			if tr.State != StateFailed || tr.Error == "" {
				t.Fatalf("%+v", tr)
			}
			gone(t, filepath.Join(r.hostB, "d"))
			noStaging(t, r.hostB)
			if read(t, filepath.Join(r.kb, "d", "a.txt")) != "alpha" {
				t.Fatal("the original went")
			}
			if len(r.ix.removed) != 0 {
				t.Fatal("the index dropped originals of a failed move")
			}
		})
	}
}

func TestRestartMarksInterrupted(t *testing.T) {
	r := newRig(t)
	stage := filepath.Join(r.hostB, "in", stagingPrefix+"abc")
	write(t, filepath.Join(stage, "part.bin"), "partial")
	held := Transfer{ID: "held", State: StateAwaiting, RequestedAt: iso(time.Now())}
	running := Transfer{ID: "abc", State: StateRunning, RequestedAt: iso(time.Now()), Destination: End{Location: "saved:b"}}
	b, _ := json.Marshal(activeFile{[]*rec{{Transfer: running, Stage: "in/" + stagingPrefix + "abc"}, {Transfer: held}}})
	os.MkdirAll(filepath.Join(r.dir, "ix", "transfers"), 0o755)
	os.WriteFile(filepath.Join(r.dir, "ix", "transfers", "active.json"), b, 0o600)
	var mu sync.Mutex
	var ended []string
	s := New(r.s.d)
	done := make(chan struct{}, 2)
	s.OnEnd(func(t Transfer) {
		mu.Lock()
		ended = append(ended, t.ID+":"+t.State)
		mu.Unlock()
		done <- struct{}{}
	})
	s.Restore(context.Background())
	tr, err := s.Get(single, "abc")
	if err != nil || tr.State != StateInterrupted || tr.EndedAt == "" {
		t.Fatalf("%+v %v", tr, err)
	}
	if h, _ := s.Get(single, "held"); h.State != StateCancelled {
		t.Fatalf("held %+v", h)
	}
	gone(t, stage)
	<-done
	<-done
	sort.Strings(ended)
	if strings.Join(ended, ",") != "abc:interrupted,held:cancelled" {
		t.Fatalf("ended %v", ended)
	}
	// And it was saved.
	s2 := New(r.s.d)
	if tr, _ := s2.Get(single, "abc"); tr.State != StateInterrupted {
		t.Fatalf("not saved: %+v", tr)
	}
}

func TestPerViewerIsolation(t *testing.T) {
	r := newRig(t)
	alice := Viewer{ID: "alice", AuthOn: true, Key: "alice-key"}
	bob := Viewer{ID: "bob", AuthOn: true, Key: "bob-key"}
	r.env["STUDIO_TRANSFER_FOLDERS"] = filepath.Join(r.dir, "shared")
	os.MkdirAll(filepath.Join(r.dir, "shared"), 0o755)
	write(t, filepath.Join(r.kb, "a.txt"), "alice's")
	ls := r.s.Locations(context.Background(), alice)
	ids := map[string]bool{}
	for _, l := range ls {
		ids[l.ID] = true
	}
	if ids["saved:a"] || ids["computer:Downloads"] || !ids["computer:shared"] || !ids["cluster:gpu-alice"] || ids["cluster:gpu-bob"] {
		t.Fatalf("alice's locations %v", ids)
	}
	if _, err := r.s.Browse(context.Background(), alice, "cluster:gpu-bob", strPtr("")); statusOf(err) != 404 {
		t.Fatalf("alice browsed bob's cluster: %v", err)
	}
	p := r.mustPlan(alice, req(ActionCopy, "library:kb", []string{"a.txt"}, "computer:shared", ""))
	if _, err := r.s.Start(context.Background(), bob, p.ID, ByUser, ""); statusOf(err) != 404 {
		t.Fatalf("bob started alice's plan: %v", err)
	}
	tr, err := r.s.Start(context.Background(), alice, p.ID, ByUser, "")
	if err != nil {
		t.Fatal(err)
	}
	r.wait(alice, tr.ID)
	if len(list(r.s, bob, "all")) != 0 || len(list(r.s, alice, "all")) != 1 {
		t.Fatal("lists are not per viewer")
	}
	if _, err := r.s.Get(bob, tr.ID); statusOf(err) != 404 {
		t.Fatalf("bob read alice's transfer: %v", err)
	}
	if ok, _ := r.s.Cancel(bob, tr.ID); ok {
		t.Fatal("bob cancelled alice's transfer")
	}
	if err := r.s.Delete(bob, tr.ID); statusOf(err) != 404 {
		t.Fatalf("bob deleted alice's transfer: %v", err)
	}
	if err := r.s.Delete(alice, tr.ID); err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }

func TestHoldApproveDeny(t *testing.T) {
	r := newRig(t)
	write(t, filepath.Join(r.kb, "x.txt"), "x")
	ends := make(chan Transfer, 4)
	r.s.OnEnd(func(t Transfer) { ends <- t })
	p := r.mustPlan(single, req(ActionCopy, "library:kb", []string{"x.txt"}, "saved:a", ""))
	held, err := r.s.Hold(context.Background(), single, p.ID, "conv-1")
	if err != nil || held.State != StateAwaiting || held.ConversationID != "conv-1" || held.StartedBy != ByAssistant {
		t.Fatalf("hold %+v %v", held, err)
	}
	if l := list(r.s, single, "active"); len(l) != 1 || l[0].State != StateAwaiting {
		t.Fatalf("active %+v", l)
	}
	if _, err := os.Stat(filepath.Join(r.hostA, "x.txt")); err == nil {
		t.Fatal("ran before approval")
	}
	if _, err := r.s.Approve(context.Background(), single, held.ID); err != nil {
		t.Fatal(err)
	}
	tr := r.wait(single, held.ID)
	if tr.State != StateSucceeded || read(t, filepath.Join(r.hostA, "x.txt")) != "x" {
		t.Fatalf("%+v", tr)
	}
	if e := <-ends; e.ID != held.ID || e.ConversationID != "conv-1" || e.State != StateSucceeded {
		t.Fatalf("end %+v", e)
	}
	p = r.mustPlan(single, req(ActionCopy, "library:kb", []string{"x.txt"}, "saved:b", ""))
	held, _ = r.s.Hold(context.Background(), single, p.ID, "conv-2")
	if err := r.s.Deny(single, held.ID); err != nil {
		t.Fatal(err)
	}
	if tr, _ := r.s.Get(single, held.ID); tr.State != StateCancelled {
		t.Fatalf("%+v", tr)
	}
}

func list(s *Service, v Viewer, state string) []Transfer {
	p, err := s.List(v, ListQuery{State: state})
	if err != nil {
		panic(err)
	}
	return p.Transfers
}

// A stopping server writes nothing more: the next start finds the job as
// it was, running, and settles it.
func TestStopFreezesTheStore(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(r.kb, "many", "f"+itoa(i)+".txt"), "data")
	}
	r.s.movers = append(r.s.movers, &slow{})
	q := req(ActionCopy, "library:kb", []string{"many"}, "saved:a", "")
	q.Options.Mover = "slow"
	p := r.mustPlan(single, q)
	tr, err := r.s.Start(context.Background(), single, p.ID, ByUser, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := r.s.Stop(); n != 1 {
		t.Fatalf("stopped %d", n)
	}
	r.wait(single, tr.ID)
	s2 := New(r.s.d)
	if got, _ := s2.Get(single, tr.ID); got.State != StateRunning {
		t.Fatalf("on disk after stop: %+v", got)
	}
	s2.Restore(context.Background())
	if got, _ := s2.Get(single, tr.ID); got.State != StateInterrupted {
		t.Fatalf("after restart: %+v", got)
	}
	noStaging(t, r.hostA)
}
