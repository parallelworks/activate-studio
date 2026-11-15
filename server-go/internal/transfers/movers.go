package transfers

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Mover names.
const (
	MoverRsync   = "rsync"
	MoverCopy    = "studio-copy"
	MoverConduit = "conduit"
)

// MoveJob is what a mover is asked to do: put each file at Stage/Out on
// the destination. A mover writes nowhere else, and its word that it is
// done is checked by the Studio before anything takes its name.
type MoveJob struct {
	ID       string
	Src, Dst FS
	// SrcLoc and DstLoc are the locations, for a mover that maps them
	// (CONDUIT).
	SrcLoc, DstLoc *loc
	Files          []item
	// Items are the source paths as chosen (folders included), for a mover
	// given folders; for a typed CONDUIT source, its own paths.
	Items    []string
	Stage    string
	Preserve bool
	// Set is the Transfers settings the plan was made under.
	Set Settings
}

// MoverStatus is where a mover's transfer stands.
type MoverStatus struct {
	// State: running, done, failed, cancelled, or lost (the mover no
	// longer knows the transfer).
	State   string
	Files   int64
	Bytes   int64
	Message string
}

// Mover moves bytes. The registry asks movers in order (conduit, rsync,
// studio-copy) and takes the first that can reach both ends.
type Mover interface {
	Name() string
	// PollEvery is how often the job asks Status.
	PollEvery() time.Duration
	// CanReach says whether this mover can do the planned transfer, and
	// why, or why not, in words for the plan.
	CanReach(ctx context.Context, p *planRec) (bool, string)
	// Start begins the transfer and returns its reference at once.
	Start(ctx context.Context, j *MoveJob) (string, error)
	Status(ctx context.Context, ref string) (MoverStatus, error)
	// Cancel asks the transfer to stop; false when this mover cannot.
	Cancel(ref string) bool
}

// inProcess runs a mover's work on a goroutine of this server and keeps
// its status for Status, for movers that are code here (rsync,
// studio-copy). After a restart Status says lost.
type inProcess struct {
	mu   sync.Mutex
	jobs map[string]*procJob
}

type procJob struct {
	files, bytes atomic.Int64
	mu           sync.Mutex
	state, msg   string
	cancel       context.CancelFunc
}

func (p *inProcess) start(fn func(ctx context.Context, j *procJob) error) string {
	ref := newID()
	ctx, cancel := context.WithCancel(context.Background())
	j := &procJob{state: "running", cancel: cancel}
	p.mu.Lock()
	if p.jobs == nil {
		p.jobs = map[string]*procJob{}
	}
	p.jobs[ref] = j
	p.mu.Unlock()
	go func() {
		err := fn(ctx, j)
		j.mu.Lock()
		switch {
		case ctx.Err() != nil:
			j.state = "cancelled"
		case err != nil:
			j.state, j.msg = "failed", messageOf(err)
		default:
			j.state = "done"
		}
		j.mu.Unlock()
		cancel()
		time.AfterFunc(10*time.Minute, func() { p.mu.Lock(); delete(p.jobs, ref); p.mu.Unlock() })
	}()
	return ref
}

func (p *inProcess) status(ref string) MoverStatus {
	p.mu.Lock()
	j := p.jobs[ref]
	p.mu.Unlock()
	if j == nil {
		return MoverStatus{State: "lost"}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return MoverStatus{State: j.state, Message: j.msg, Files: j.files.Load(), Bytes: j.bytes.Load()}
}

func (p *inProcess) cancel(ref string) bool {
	p.mu.Lock()
	j := p.jobs[ref]
	p.mu.Unlock()
	if j == nil {
		return false
	}
	j.cancel()
	return true
}

// ---- studio-copy ----

// copyMover is studio-copy: the Studio reads each file (over ssh with cat,
// over SFTP for an account that takes SFTP only, or here) and writes it
// itself, up to four at once. It needs nothing but ssh, sh and cat on a
// host, or SFTP, so it is the fallback every transfer can use.
// A file name is always one quoted argument of a fixed script (fs_remote.go),
// and the Studio opens every file it writes from a list it checked.
type copyMover struct {
	s *Service
	inProcess
}

func newCopyMover(s *Service) *copyMover { return &copyMover{s: s} }

func (c *copyMover) Name() string             { return MoverCopy }
func (c *copyMover) PollEvery() time.Duration { return 250 * time.Millisecond }

func (c *copyMover) CanReach(ctx context.Context, p *planRec) (bool, string) {
	if p.src.fs == nil {
		return false, "it reads files through Studio, and Studio cannot read " + p.src.Label
	}
	_, srcRemote := p.src.fs.(Remote)
	_, dstRemote := p.dst.fs.(Remote)
	switch {
	case srcRemote && dstRemote:
		return true, "Studio reads each file from " + p.src.fs.Where() + " and writes it to " + p.dst.fs.Where()
	case srcRemote:
		return true, "Studio reads each file from " + p.src.fs.Where() + overWhat(ctx, p.src.fs)
	case dstRemote:
		return true, "Studio writes each file to " + p.dst.fs.Where() + overWhat(ctx, p.dst.fs)
	}
	return true, "both ends are on this computer"
}

// overWhat is how studio-copy reaches a host.
func overWhat(ctx context.Context, f FS) string {
	if isSFTPOnly(ctx, f) {
		return " over SFTP"
	}
	return " over ssh"
}

// streams is how many files studio-copy moves at once: the settings'
// parallel, 1 to 16.
func streams(set Settings) int {
	n := set.Defaults.Parallel
	if n < 1 {
		return 4
	}
	return min(n, 16)
}

// errTooLong: a file sent more than it was listed with.
var errTooLong = errors.New("sent more than the size it was listed with")

func (c *copyMover) Start(ctx context.Context, j *MoveJob) (string, error) {
	return c.start(func(ctx context.Context, pj *procJob) error {
		return copyFiles(ctx, j, pj)
	}), nil
}

// copyFiles copies each file through this process, a few at a time; the
// first failure stops the rest.
func copyFiles(ctx context.Context, j *MoveJob, pj *procJob) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var first error
	var once sync.Once
	var wg sync.WaitGroup
	for w := 0; w < min(streams(j.Set), max(len(j.Files), 1)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= len(j.Files) {
					return
				}
				if err := copyOne(ctx, j, j.Files[i], pj); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
				pj.files.Add(1)
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

func copyOne(ctx context.Context, j *MoveJob, f item, pj *procJob) error {
	r, err := j.Src.Read(ctx, f.Rel)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { r.Close() })
	defer stop()
	var mt int64
	if j.Preserve {
		mt = f.Mtime
	}
	cr := &countReader{r: io.LimitReader(r, f.Size+1), n: &pj.bytes, max: f.Size}
	n, werr := j.Dst.Write(ctx, join(j.Stage, f.Out), cr, mt, f.Mode)
	cerr := r.Close()
	switch {
	case cr.over:
		return errors.New(f.Rel + ": " + errTooLong.Error())
	case werr != nil:
		return werr
	case cerr != nil:
		return errors.New(f.Rel + ": " + cerr.Error())
	case n != f.Size:
		return errors.New(f.Rel + ": " + strconv.FormatInt(n, 10) + " bytes arrived where the listing had " + strconv.FormatInt(f.Size, 10) + "; it changed while it was copied")
	}
	return nil
}

type countReader struct {
	r    io.Reader
	n    *atomic.Int64
	got  int64
	max  int64
	over bool
}

func (c *countReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.got += int64(k)
	if c.got > c.max {
		c.over = true
		return 0, errTooLong
	}
	c.n.Add(int64(k))
	return k, err
}

func (c *copyMover) Status(ctx context.Context, ref string) (MoverStatus, error) {
	return c.status(ref), nil
}

func (c *copyMover) Cancel(ref string) bool { return c.cancel(ref) }
