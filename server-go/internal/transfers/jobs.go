package transfers

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// run is a running job's handle.
type run struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	mover     Mover
	ref       string
	cancelled bool
}

// Started-by values.
const (
	ByUser      = "user"
	ByAssistant = "assistant"
)

// Start queues the planned transfer and returns at once: the plan is
// listed again and refused (409) if it expired or grew. The job runs on
// its own goroutine, apart from the request, so a reply that ends or a page
// that closes does not stop it.
func (s *Service) Start(ctx context.Context, v Viewer, planID, startedBy, conversationID string) (Transfer, error) {
	old, err := s.takePlan(v, planID, true)
	if err != nil {
		return Transfer{}, err
	}
	p, err := s.confirm(ctx, v, old)
	if err != nil {
		return Transfer{}, err
	}
	s.dropPlan(planID)
	r := s.newRec(v, p, startedBy, conversationID, StateQueued)
	s.store.mu.Lock()
	r.event(s.now(), "Queued: %s, %s, by %s", count(max(p.Files, 0), "file", "files"), humanBytes(max(p.Bytes, 0)), p.Mover)
	s.store.active[r.ID] = r
	s.store.saveActiveLocked(true, s.now())
	t := r.view(false)
	s.store.mu.Unlock()
	s.launch(r)
	return t, nil
}

// Hold records a transfer the assistant asked for, waiting for the
// person's approval in the chat: it appears in the list as
// awaiting-approval with its conversation, and Approve or Deny settles it.
func (s *Service) Hold(ctx context.Context, v Viewer, planID, conversationID string) (Transfer, error) {
	p, err := s.takePlan(v, planID, true)
	if err != nil {
		return Transfer{}, err
	}
	s.dropPlan(planID)
	r := s.newRec(v, p, ByAssistant, conversationID, StateAwaiting)
	s.store.mu.Lock()
	r.event(s.now(), "Waiting for approval in the conversation")
	s.store.active[r.ID] = r
	s.store.saveActiveLocked(true, s.now())
	t := r.view(false)
	s.store.mu.Unlock()
	return t, nil
}

// Approve starts a held transfer: its plan is listed again and refused if
// it grew, as a plan confirmed on the page is (there is no 15 minute
// limit, since the approval card holds it).
func (s *Service) Approve(ctx context.Context, v Viewer, id string) (Transfer, error) {
	r, err := s.mine(v, id)
	if err != nil {
		return Transfer{}, err
	}
	s.store.mu.Lock()
	state, plan := r.State, r.plan
	s.store.mu.Unlock()
	if state != StateAwaiting || plan == nil {
		return Transfer{}, apierr.New(409, "this transfer is not waiting for approval")
	}
	p, err := s.confirm(ctx, v, plan)
	if err != nil {
		s.finish(r, StateCancelled, messageOf(err))
		return Transfer{}, err
	}
	s.store.mu.Lock()
	r.plan, r.viewer, r.State = p, v, StateQueued
	r.Files.Total, r.Bytes.Total = int64(p.Files), p.Bytes
	r.event(s.now(), "Approved; queued")
	s.store.saveActiveLocked(true, s.now())
	t := r.view(false)
	s.store.mu.Unlock()
	s.launch(r)
	return t, nil
}

// Deny settles a held transfer as cancelled.
func (s *Service) Deny(v Viewer, id string) error {
	r, err := s.mine(v, id)
	if err != nil {
		return err
	}
	s.store.mu.Lock()
	state := r.State
	s.store.mu.Unlock()
	if state != StateAwaiting {
		return apierr.New(409, "this transfer is not waiting for approval")
	}
	s.finish(r, StateCancelled, "declined in the conversation")
	return nil
}

func (s *Service) dropPlan(id string) {
	s.mu.Lock()
	delete(s.plans, id)
	s.mu.Unlock()
}

func (s *Service) newRec(v Viewer, p *planRec, by, conv, state string) *rec {
	if by != ByAssistant && by != ByLibrary {
		by = ByUser
	}
	r := &rec{Owner: v.owner(), plan: p, viewer: v}
	r.ID = newID()
	r.Label, r.Action, r.Mover = p.Label, p.Action, p.Mover
	r.Source, r.Destination = p.Source, p.Destination
	r.State, r.StartedBy, r.ConversationID = state, by, conv
	r.RequestedAt = iso(s.now())
	r.Files = Count{0, int64(p.Files)}
	r.Bytes = Count{0, p.Bytes}
	r.SkippedCount = p.Skipped.Hidden + p.Skipped.Links
	r.IndexNotes = p.IndexNotes
	r.Stage = join(p.dstPath, stagingPrefix+r.ID)
	return r
}

// mine is a transfer of the viewer's.
func (s *Service) mine(v Viewer, id string) (*rec, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	r := s.store.active[id]
	if r == nil || r.Owner != v.owner() {
		return nil, apierr.New(404, "no transfer %q", short(id))
	}
	return r, nil
}

// Get is one transfer of the viewer's, active or finished, with its
// events.
func (s *Service) Get(v Viewer, id string) (Transfer, error) {
	t, ok := s.store.get(v.owner(), id)
	if !ok {
		return Transfer{}, apierr.New(404, "no transfer %q", short(id))
	}
	return t, nil
}

// List is a page of the viewer's transfers: the active ones (never paged)
// first, then finished ones newest first by when they ended.
func (s *Service) List(v Viewer, q ListQuery) (Page, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	p, err := s.store.list(v.owner(), q)
	if err != nil {
		return p, apierr.New(400, "%s", err.Error())
	}
	return p, nil
}

// Summary is the viewer's counts: active (waiting for approval included),
// waiting for approval, and failed in the last 24 hours.
func (s *Service) Summary(v Viewer) Summary {
	return s.store.summary(v.owner(), s.now())
}

// Cancel stops a transfer of the viewer's: one waiting or queued at once,
// one running through its mover, which may say it cannot.
func (s *Service) Cancel(v Viewer, id string) (bool, string) {
	r, err := s.mine(v, id)
	if err != nil {
		if _, ok := s.store.get(v.owner(), id); ok {
			return false, "it has already ended"
		}
		return false, messageOf(err)
	}
	s.store.mu.Lock()
	state := r.State
	s.store.mu.Unlock()
	switch state {
	case StateAwaiting:
		s.finish(r, StateCancelled, "cancelled before approval")
		return true, ""
	case StateVerifying:
		return false, "the files have arrived and are being checked and put in place; it ends in a moment"
	case StateQueued, StateRunning:
	default:
		return false, "it has already ended"
	}
	s.mu.Lock()
	h := s.runs[id]
	s.mu.Unlock()
	if h == nil {
		return false, "it has already ended"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mover != nil && h.ref != "" && !h.mover.Cancel(h.ref) {
		return false, h.mover.Name() + " has no way to stop a transfer once started; it will be checked when it ends"
	}
	h.cancelled = true
	h.cancel()
	return true, ""
}

// Delete removes a finished transfer from the list.
func (s *Service) Delete(v Viewer, id string) error {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if r := s.store.active[id]; r != nil && r.Owner == v.owner() {
		return apierr.New(409, "it has not ended; cancel it first")
	}
	if d := s.store.byID[id]; d == nil || d.owner != v.owner() {
		return apierr.New(404, "no transfer %q", short(id))
	}
	s.store.removeLocked(id, s.now())
	return nil
}

// Active reports whether any transfer is queued or running (an automatic
// update waits).
func (s *Service) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs) > 0
}

// Stop is the server stopping: nothing more is written, and running jobs
// are told to stop (their movers' programs end with them). The next start
// marks them interrupted and clears their staging folders. It returns how
// many jobs it stopped.
func (s *Service) Stop() int {
	s.store.mu.Lock()
	s.store.saveActiveLocked(true, s.now())
	s.store.frozen = true
	s.store.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.runs {
		h.mu.Lock()
		if h.mover != nil && h.ref != "" {
			h.mover.Cancel(h.ref)
		}
		h.cancel()
		h.mu.Unlock()
	}
	return len(s.runs)
}

// launch runs the job on its own goroutine, after at most two others.
func (s *Service) launch(r *rec) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &run{cancel: cancel}
	s.mu.Lock()
	s.runs[r.ID] = h
	s.mu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.d.Log.Error("transfers: job panicked", "id", r.ID, "panic", p)
				s.finish(r, StateFailed, "the transfer stopped on an internal error")
			}
			cancel()
			s.mu.Lock()
			delete(s.runs, r.ID)
			s.mu.Unlock()
		}()
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			s.finish(r, StateCancelled, "")
			return
		}
		s.execute(ctx, r, h)
	}()
}

func (s *Service) set(r *rec, fn func()) {
	s.store.mu.Lock()
	fn()
	s.store.saveActiveLocked(true, s.now())
	s.store.mu.Unlock()
}

// finish ends a transfer once, and tells those listening.
func (s *Service) finish(r *rec, state, why string) {
	s.store.mu.Lock()
	if finished(r.State) {
		s.store.mu.Unlock()
		return
	}
	r.State = state
	r.EndedAt = iso(s.now())
	r.Error = why
	switch state {
	case StateSucceeded:
		r.event(s.now(), "Done")
	case StateCancelled:
		if why != "" {
			r.event(s.now(), "Cancelled: %s", why)
		} else {
			r.event(s.now(), "Cancelled")
		}
		r.Error = ""
	default:
		r.event(s.now(), "%s: %s", strings.ToUpper(state[:1])+state[1:], why)
	}
	r.plan = nil
	r.viewer.Key = ""
	t := r.view(false)
	s.store.finishLocked(r, s.now())
	s.store.mu.Unlock()
	s.ended(t)
}

// execute is one job: the mover fills the staging folder, the Studio
// checks it, the files take their names, then deletions (sync) or the
// originals (move) go, and the indexes hear of it.
func (s *Service) execute(ctx context.Context, r *rec, h *run) {
	s.store.mu.Lock()
	p := r.plan
	s.store.mu.Unlock()
	if p == nil {
		s.finish(r, StateFailed, "its plan was lost")
		return
	}
	var mover Mover
	for _, m := range s.movers {
		if m.Name() == p.Mover {
			mover = m
		}
	}
	dst := p.dst.fs
	s.set(r, func() {
		r.State, r.StartedAt = StateRunning, iso(s.now())
		r.event(s.now(), "Started with %s: %s", p.Mover, p.MoverWhy)
	})
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := dst.RemoveStaging(c, r.Stage); err != nil {
			s.set(r, func() { r.event(s.now(), "The staging folder %s could not be removed: %s", r.Stage, messageOf(err)) })
		}
	}
	fail := func(why string) {
		cleanup()
		s.finish(r, StateFailed, why)
	}
	stage := r.Stage
	if p.direct {
		// Written straight to the final names: no staging folder to make or
		// clear, and what was written stays if the job fails.
		stage = p.dstPath
		cleanup = func() {}
		fail = func(why string) { s.finish(r, StateFailed, why) }
	} else if err := dst.Mkdir(ctx, r.Stage); err != nil {
		s.finish(r, StateFailed, "the staging folder could not be made: "+messageOf(err))
		return
	}
	job := &MoveJob{ID: r.ID, Src: p.src.fs, Dst: dst, SrcLoc: p.src, DstLoc: p.dst, Files: p.files, Items: p.items,
		Stage: stage, Preserve: p.req.Options.preserve(), Set: p.set, Direct: p.direct, Replace: p.replaceSet}
	if len(p.files) > 0 || p.bare {
		ref, err := mover.Start(ctx, job)
		if err != nil {
			fail(p.Mover + " did not start: " + messageOf(err))
			return
		}
		h.mu.Lock()
		h.mover, h.ref = mover, ref
		h.mu.Unlock()
		if state, why := s.follow(ctx, r, mover, ref); state != "" {
			cleanup()
			s.finish(r, state, why)
			return
		}
	}
	s.set(r, func() {
		r.State = StateVerifying
		r.event(s.now(), "Checking what arrived")
	})
	vctx := context.WithoutCancel(ctx)
	if err := s.verify(vctx, r, p); err != nil {
		fail(messageOf(err))
		return
	}
	if err := s.land(vctx, r, p); err != nil {
		cleanup()
		s.finish(r, StateFailed, messageOf(err))
		return
	}
	cleanup()
	s.finish(r, StateSucceeded, "")
}

// follow polls the mover until it ends: "" when it reports done, else
// the state the job ends in and why.
func (s *Service) follow(ctx context.Context, r *rec, m Mover, ref string) (string, string) {
	misses := 0
	tick := time.NewTicker(m.PollEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// Cancelled here, and the mover told by Cancel: wait a moment for
			// it to stop writing before the staging folder goes.
			for i := 0; i < 100; i++ {
				if st, _ := m.Status(context.Background(), ref); st.State != "running" {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			return StateCancelled, ""
		case <-tick.C:
		}
		st, err := m.Status(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			misses++
			if misses >= 40 {
				return StateFailed, m.Name() + " stopped answering about the transfer: " + messageOf(err)
			}
			continue
		}
		misses = 0
		s.store.mu.Lock()
		r.progress(s.now(), st.Files, st.Bytes)
		s.store.saveActiveLocked(false, s.now())
		s.store.mu.Unlock()
		switch st.State {
		case "running":
			continue
		case "done":
			s.set(r, func() { r.event(s.now(), "%s reports the transfer done", m.Name()) })
			return "", ""
		case "cancelled":
			return StateCancelled, ""
		case "lost":
			return StateInterrupted, m.Name() + " no longer knows the transfer"
		default:
			msg := st.Message
			if msg == "" {
				msg = m.Name() + " reported a failure"
			}
			return StateFailed, msg
		}
	}
}

// Movers is which movers can reach from one location to the other, before
// any paths are chosen: the same list a plan gives, for a copy between the
// two locations' top folders.
func (s *Service) Movers(ctx context.Context, v Viewer, srcID, dstID string) ([]MoverChoice, error) {
	src, err := s.resolve(ctx, v, srcID)
	if err != nil {
		return nil, err
	}
	dst, err := s.resolve(ctx, v, dstID)
	if err != nil {
		return nil, err
	}
	p := &planRec{owner: v.owner(), src: src, dst: dst, items: []string{""}, set: s.settingsFor(v)}
	p.Action = ActionCopy
	if dst.fs == nil {
		p.dst = &loc{Location: dst.Location}
	}
	off := p.set.off()
	out := []MoverChoice{}
	for _, m := range s.movers {
		ok, why := false, "turned off in Settings, Transfers"
		if dst.fs == nil {
			why = "Studio cannot read " + dst.Label + " to check what lands"
		} else if !off[m.Name()] {
			ok, why = m.CanReach(ctx, p)
		}
		out = append(out, MoverChoice{Name: m.Name(), OK: ok, Why: why})
	}
	return out, nil
}
