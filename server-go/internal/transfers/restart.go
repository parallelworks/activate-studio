package transfers

import (
	"context"
	"time"
)

// Restore settles what a stop left: a transfer queued, running or being
// checked is marked interrupted and its staging folder cleared (resuming an
// rsync or CONDUIT job is for a later round); one waiting for approval is
// cancelled, since the approval card went with the process. It runs once at
// start.
func (s *Service) Restore(ctx context.Context) {
	s.store.mu.Lock()
	var all []*rec
	for _, r := range s.store.active {
		all = append(all, r)
	}
	s.store.mu.Unlock()
	for _, r := range all {
		switch r.State {
		case StateQueued, StateRunning, StateVerifying:
			s.clearStaging(ctx, r)
			s.finish(r, StateInterrupted, "the Studio stopped while it ran")
		case StateAwaiting:
			s.finish(r, StateCancelled, "the Studio restarted before approval")
		}
	}
}

func (s *Service) clearStaging(ctx context.Context, r *rec) {
	if r.Stage == "" {
		return
	}
	v := Viewer{ID: r.Owner, AuthOn: r.Owner != ""}
	if s.viewerFor != nil {
		v = s.viewerFor(r.Owner)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	l, err := s.resolve(c, v, r.Destination.Location)
	if err == nil && l.fs == nil {
		err = errNoFS
	}
	if err == nil {
		err = l.fs.RemoveStaging(c, r.Stage)
	}
	s.store.mu.Lock()
	if err != nil {
		r.event(s.now(), "The staging folder %s could not be cleared: %s", r.Stage, messageOf(err))
	} else {
		r.event(s.now(), "The staging folder was cleared")
	}
	s.store.mu.Unlock()
}

type noFS struct{}

func (noFS) Error() string { return "the location cannot be reached" }

var errNoFS error = noFS{}
