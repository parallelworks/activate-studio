package transfers

import (
	"context"
	"errors"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// A copy library is a library whose folder on this computer holds a copy
// of a folder in a location: an SFTP or ssh host, an rclone remote, a
// cluster (docs/LIBRARIES.md, Copies of a location). Pull brings the copy
// in step with a sync that deletes what the source no longer has, planned
// and run as any transfer: it takes the movers the location allows, lands
// through a staging folder and checks what arrives. A pull that would
// change nothing leaves no transfer behind.

// ByLibrary is a transfer Studio started to bring a copy library in step.
const ByLibrary = "library"

// pullPoll is how often Pull reads its transfer while it runs.
var pullPoll = 500 * time.Millisecond

// PullRequest is the copy Pull brings in step.
type PullRequest struct {
	// Location is the location the files come from; Path the folder in it,
	// "" for its top.
	Location, Path string
	// LibraryID and Label name the copy library; Dir is its folder here,
	// which must exist.
	LibraryID, Label, Dir string
	// Progress, when set, is given the transfer while it runs.
	Progress func(Transfer)
}

// PullResult is what a pull did.
type PullResult struct {
	// Transfer is the transfer's id, "" when nothing had changed.
	Transfer       string
	Files, Deleted int
	Bytes          int64
}

// OwnerViewer is who a pull acts as with no request: the Studio's single
// owner, with the platform key their clusters are reached with. False with
// sign-in on, where a copy library is not offered.
func (s *Service) OwnerViewer() (Viewer, bool) {
	v := Viewer{}
	if s.viewerFor != nil {
		v = s.viewerFor("")
	}
	return v, !v.AuthOn
}

// Pull brings in.Dir in step with in.Location's folder in.Path and waits
// for the transfer to end. Cancelling ctx cancels the transfer.
func (s *Service) Pull(ctx context.Context, v Viewer, in PullRequest) (PullResult, error) {
	if v.AuthOn {
		return PullResult{}, apierr.New(403, "a copy library is kept with the Studio account's own access, which a Studio with sign-in on does not lend")
	}
	// The copy's folder as the destination. Its library location is read
	// only for transfers a person or the assistant asks for, since a pull
	// would delete what they put there.
	into := &loc{Location: Location{ID: KindLibrary + ":" + in.LibraryID, Kind: KindLibrary, Label: in.Label,
		Writable: true, Browsable: true, Available: true}, fs: &LocalFS{Dir: in.Dir}, libID: in.LibraryID,
		refresh: "as soon as this update ends"}
	q := Request{Action: ActionSync}
	q.Source.Location, q.Source.Paths = in.Location, []string{in.Path}
	q.Destination.Location = into.ID
	q.Options.DeleteExtra = true
	q.Options.Label = "Update " + in.Label
	q.into = into
	p, err := s.Plan(ctx, v, q)
	if err != nil {
		return PullResult{}, err
	}
	out := PullResult{Files: max(p.Files, 0), Deleted: p.DeletesCount, Bytes: max(p.Bytes, 0)}
	if p.Files == 0 && p.DeletesCount == 0 {
		s.dropPlan(p.ID)
		return out, nil
	}
	t, err := s.Start(ctx, v, p.ID, ByLibrary, "")
	if err != nil {
		return out, err
	}
	out.Transfer = t.ID
	for !finished(t.State) {
		if in.Progress != nil {
			in.Progress(t)
		}
		select {
		case <-ctx.Done():
			s.Cancel(v, t.ID)
			return out, ctx.Err()
		case <-time.After(pullPoll):
		}
		if t, err = s.Get(v, t.ID); err != nil {
			return out, err
		}
	}
	if in.Progress != nil {
		in.Progress(t)
	}
	if t.State != StateSucceeded {
		why := t.Error
		if why == "" {
			why = "it ended " + t.State
		}
		return out, errors.New(why)
	}
	return out, nil
}
