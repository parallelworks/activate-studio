package transfers

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Viewer is who asks: the signed-in user, or the one user of a Studio with
// sign-in off. Every location, plan and transfer is resolved for a viewer,
// and with sign-in on a viewer sees and acts on their own transfers only.
type Viewer struct {
	// ID is the user's id; "" with sign-in off.
	ID string
	// AuthOn: people sign in to this Studio.
	AuthOn bool
	// Key is the platform key clusters are reached with: the viewer's own,
	// or with sign-in off the deployment's; "" for none. Never stored.
	Key string
}

// owner is the id a transfer is kept under.
func (v Viewer) owner() string {
	if !v.AuthOn {
		return ""
	}
	return v.ID
}

// LibraryLoc is a library as transfers see it: where its files are.
type LibraryLoc struct {
	ID, Label string
	// Writable: a transfer may copy into the library and move out of it.
	// It follows who reaches the files (libraryWritable), not whether
	// Studio edits the library's index; the file system's permissions
	// decide the rest, checked before a plan is offered.
	Writable bool
	// ReadOnly is why a library whose files are reachable is not writable,
	// for the location's Detail; "" otherwise.
	ReadOnly string
	// Indexed: the library has an index, so it is searchable. The
	// location's Indexed shows it.
	Indexed bool
	// Reindexed: Studio updates the index after a transfer here: a landing
	// re-indexes the paths it wrote, and a move out drops the originals
	// from the index (Index). Only the main library is. A Reindexed library
	// is also Indexed.
	Reindexed bool
	// Refresh is when the index of a library that is not Reindexed shows a
	// transfer's change, finishing "The index of <label> shows this
	// change ..."; "" says "after its next refresh".
	Refresh string
	// FS is where its files are, nil when Studio cannot reach them (Why).
	FS  FS
	Why string
	// Site and Folder are where its files are, as far as Studio knows,
	// whether or not it reaches them: the ssh site of their host ("" when
	// not known or not an ssh host) and the folder there ("" when not
	// known). They prefill a saved location for them (Location.Suggest).
	Site, Folder string
}

// Index is the index work around a transfer: the paths a transfer wrote in
// a library, and the originals a move removed from one.
type Index interface {
	Landed(ctx context.Context, libID string, files []string)
	Removed(ctx context.Context, libID string, files, dirs []string)
}

// Deps are what the service reaches outside itself. Of fills them from the
// App; tests fill them by hand.
type Deps struct {
	IndexBase string
	Getenv    func(string) string
	Log       *slog.Logger
	// Runner reaches hosts (the shared ssh pool).
	Runner Runner
	// Shell is how rsync reaches a host; nil: no rsync over ssh.
	Shell func(site, key string) (argv, env []string, err error)
	// Libraries are the viewer's libraries.
	Libraries func(ctx context.Context, v Viewer) []LibraryLoc
	// Clusters are the viewer's active clusters, by name.
	Clusters func(ctx context.Context, v Viewer) ([]string, error)
	Settings SettingsStore
	Index    Index
	// Conduit is the CONDUIT service caller; nil: none.
	Conduit ConduitCaller
	// Home is this computer's home folder, for Downloads, Desktop and
	// Documents; "" leaves them out.
	Home string
}

// Service is transfers for one server.
type Service struct {
	d      Deps
	store  *store
	movers []Mover

	mu        sync.Mutex
	plans     map[string]*planRec
	listeners []func(Transfer)
	homes     map[string]homeEntry
	clusters  map[string]clusterEntry
	slots     chan struct{}
	runs      map[string]*run
	now       func() time.Time
	// viewerFor is the viewer a transfer's owner acts as with no request
	// (clearing staging after a restart); nil: the owner without a key.
	viewerFor func(owner string) Viewer
	rc        *rcloneState
	// settingsMu makes each change to the section one read, check and
	// write: AddSavedLocation and SaveSettings both hold it, so an add
	// cannot fall between a PUT's revision check and its write.
	settingsMu sync.Mutex
}

type homeEntry struct {
	at   time.Time
	home string
	err  error
}

type clusterEntry struct {
	at    time.Time
	names []string
	err   error
}

// New is a service over deps, its job list read from INDEX_BASE.
func New(d Deps) *Service {
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Settings == nil {
		d.Settings = &MemorySettings{}
	}
	if d.Index == nil {
		d.Index = noIndex{}
	}
	s := &Service{d: d, plans: map[string]*planRec{}, homes: map[string]homeEntry{}, clusters: map[string]clusterEntry{},
		slots: make(chan struct{}, 3), runs: map[string]*run{}, now: time.Now,
		rc: &rcloneState{remotes: map[string]rcloneRemotesEntry{}, features: map[string]rcloneFeaturesEntry{}}}
	s.store = openStore(d.IndexBase, d.Log)
	s.movers = []Mover{newConduitMover(s), newRsyncMover(s), newRcloneMover(s), newCopyMover(s)}
	return s
}

type noIndex struct{}

func (noIndex) Landed(context.Context, string, []string)            {}
func (noIndex) Removed(context.Context, string, []string, []string) {}

// OnEnd calls fn with each transfer as it ends (succeeded, failed,
// cancelled or interrupted), once, on its own goroutine; the chat uses it
// for a note in the conversation that started the transfer.
func (s *Service) OnEnd(fn func(Transfer)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Service) ended(t Transfer) {
	s.mu.Lock()
	ls := append([]func(Transfer){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range ls {
		go fn(t)
	}
}
