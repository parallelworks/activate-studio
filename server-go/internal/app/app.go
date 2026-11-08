// Package app is the Go server's process: what main.ts, lifecycle.ts,
// instanceLock.ts, stateStart.ts and seed.ts do around the routes. Other
// packages use it for start-up hooks, background jobs, the restart contract
// with the launcher (exit 75 and 76), the index folder lock, a busy check
// for automatic updates, and the providers /api/config and the setup
// routes read from modules other tracks own.
//
// The API, for a package that registers routes:
//
//	a.OnStart("libraries: probe", func(ctx context.Context) { ... })   // once, after listen
//	a.OnIndexWork("index: sweep", func(ctx context.Context) { ... })   // once, only with the index lock
//	a.Go("fleet", func(ctx context.Context) { ... })                    // a background job until stop
//	a.OnStop("index: cancel jobs", func() int { ... })                  // at stop, before draining
//	done := a.Stream(); defer done()                                    // an answer that is streaming
//	a.AddBusy(func() bool { ... })                                      // an update waits while true
//	a.RequestRestart(app.RelaunchExitCode)                              // after an update
//	a.Providers.Libraries = myLibraries                                 // fill a provider
//
// Hooks run in registration order; each runs in its own goroutine for
// OnStart and OnIndexWork, so a slow one does not hold up the rest.
package app

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// App is the server process shared by every module.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Settings *settings.Store
	// Node calls the Node server behind the Go server, nil in Node-free
	// mode (STUDIO_NODE=off) or with no Node.
	Node *Node
	// Providers are the values modules of other tracks own (providers.go).
	Providers Providers
	// Lock is the index folder lock (instancelock.go). Held only where
	// this process does index work: in Node-free mode. With Node behind
	// the Go server, Node holds it.
	Lock *IndexLock

	mu       sync.Mutex
	stopper  func(code int)
	starts   []hook
	indexers []hook
	stops    []stopHook
	busy     []func() bool
	started  bool
	ctx      context.Context
	jobs     sync.WaitGroup
	streams  atomic.Int64
}

type hook struct {
	name string
	fn   func(ctx context.Context)
}

type stopHook struct {
	name string
	fn   func() int
}

// New is the process for cfg. node is nil without a Node server.
func New(cfg config.Config, log *slog.Logger, store *settings.Store, node *Node) *App {
	if store == nil {
		store = settings.Open(cfg.IndexBase, cfg.KBRoot, cfg.AuthEnabled(), nil)
	}
	a := &App{Cfg: cfg, Log: log, Settings: store, Node: node, ctx: context.Background()}
	a.Lock = NewIndexLock(cfg.IndexBase, cfg.Port)
	a.Providers = defaultProviders(a)
	return a
}

// NodeFree reports whether the Go server runs with no Node server: every
// route is Go's, and one not ported yet answers 501.
func (a *App) NodeFree() bool { return a.Node == nil }

// OnStart runs fn once the server listens, in its own goroutine, with a
// context that ends at stop. Registered after the start, it runs at once.
func (a *App) OnStart(name string, fn func(ctx context.Context)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		a.spawn(hook{name, fn})
		return
	}
	a.starts = append(a.starts, hook{name, fn})
}

// OnIndexWork is OnStart for work that changes the index folder (the
// sweep timer, library sync): it runs only where this process holds the
// index lock (main.ts: acquireIndexLock, then startSweepTimer and
// startLibrarySync).
func (a *App) OnIndexWork(name string, fn func(ctx context.Context)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		if a.Lock.Held() {
			a.spawn(hook{name, fn})
		}
		return
	}
	a.indexers = append(a.indexers, hook{name, fn})
}

// Go runs a background job until the server stops; stop waits for it
// (up to its drain time) after cancelling its context.
func (a *App) Go(name string, fn func(ctx context.Context)) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	a.jobs.Add(1)
	go func() {
		defer a.jobs.Done()
		defer func() {
			if p := recover(); p != nil {
				// main.ts keeps the deployment up after an uncaught error:
				// a failed job costs the job, not the server.
				a.Log.Error("background job panicked; the server stays up", "job", name, "panic", p)
			}
		}()
		fn(ctx)
	}()
}

func (a *App) spawn(h hook) { a.Go(h.name, h.fn) }

// OnStop runs fn when the server stops, before open streams are drained;
// it returns how many programs it stopped (main.ts logs the total).
func (a *App) OnStop(name string, fn func() int) {
	a.mu.Lock()
	a.stops = append(a.stops, stopHook{name, fn})
	a.mu.Unlock()
}

// AddBusy adds a check an automatic update waits on (main.ts:
// setUpdateBusyCheck: an answer streaming, an index job running).
func (a *App) AddBusy(fn func() bool) {
	a.mu.Lock()
	a.busy = append(a.busy, fn)
	a.mu.Unlock()
}

// Busy reports whether any busy check, or a stream in flight, says so.
func (a *App) Busy() bool {
	if a.streams.Load() > 0 {
		return true
	}
	a.mu.Lock()
	checks := append([]func() bool(nil), a.busy...)
	a.mu.Unlock()
	for _, c := range checks {
		if c() {
			return true
		}
	}
	return false
}

// Stream counts an answer in flight until the returned function is called;
// stop drains them for up to 20 seconds (main.ts's stop).
func (a *App) Stream() (done func()) {
	a.streams.Add(1)
	var once sync.Once
	return func() { once.Do(func() { a.streams.Add(-1) }) }
}

// StreamsInFlight is how many answers are streaming.
func (a *App) StreamsInFlight() int { return int(a.streams.Load()) }

// Start runs the start hooks, and takes the index lock first where this
// process does index work (lockIndex) and runs the index hooks only when
// it got it. ctx ends at stop.
func (a *App) Start(ctx context.Context, lockIndex bool) {
	a.mu.Lock()
	a.ctx = ctx
	a.started = true
	starts, indexers := a.starts, a.indexers
	a.mu.Unlock()
	if lockIndex {
		if a.Lock.Acquire(ctx, func(m string) { a.Log.Warn(m) }, 20*time.Second) {
			for _, h := range indexers {
				a.spawn(h)
			}
		}
	}
	for _, h := range starts {
		a.spawn(h)
	}
}

// Drain is main.ts's stop before the listener closes: the stop hooks
// (indexing programs end), then up to grace for streams to finish. It
// logs as Node does.
func (a *App) Drain(reason string, grace time.Duration) {
	a.Log.Info(reason + ": draining in-flight stream(s)")
	a.mu.Lock()
	stops := append([]stopHook(nil), a.stops...)
	a.mu.Unlock()
	programs := 0
	for _, s := range stops {
		func() {
			defer func() {
				if p := recover(); p != nil {
					a.Log.Error("stop hook panicked", "hook", s.name, "panic", p)
				}
			}()
			programs += s.fn()
		}()
	}
	if programs > 0 {
		a.Log.Info(reason+": stopped indexing job(s)", "programs", programs)
	}
	deadline := time.Now().Add(grace)
	for a.streams.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	a.Lock.Release()
}

// Wait waits for the background jobs, up to d, after their context ended.
func (a *App) Wait(d time.Duration) {
	done := make(chan struct{})
	go func() { a.jobs.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		a.Log.Warn("background jobs still running at exit")
	}
}
