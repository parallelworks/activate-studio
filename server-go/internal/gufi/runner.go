package gufi

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// ErrBusy is a run refused because the queue is full.
var ErrBusy = apierr.New(503, "the index is busy; try again shortly")

// Runner admits GUFI runs: at most Concurrency at once, the rest waiting
// in arrival order, up to QueueMax of them. Each run has a timeout, and a
// run whose request ends is killed (or leaves the queue). Every run logs
// its queue depth on arrival, its wait and its run time.
type Runner struct {
	exec        Executor
	log         *slog.Logger
	concurrency int
	queueMax    int
	timeout     time.Duration
	threads     int

	mu      sync.Mutex
	active  int
	waiters []*waiter
	stats   Stats
}

type waiter struct {
	ready   chan struct{}
	granted bool
}

// Stats are a runner's counts since it started.
type Stats struct {
	Running   int     `json:"running"`
	Queued    int     `json:"queued"`
	Runs      int64   `json:"runs"`
	Refused   int64   `json:"refused"`
	Cancelled int64   `json:"cancelled"`
	TimedOut  int64   `json:"timedOut"`
	MaxQueued int     `json:"maxQueued"`
	WaitMS    float64 `json:"waitMsTotal"`
	MaxWaitMS float64 `json:"waitMsMax"`
	RunMS     float64 `json:"runMsTotal"`
}

// RunnerOptions configure a Runner; zero values take the defaults.
type RunnerOptions struct {
	Concurrency int           // default 2
	QueueMax    int           // default 256
	Timeout     time.Duration // default 60s
	// Threads caps each run's -n; 0 leaves it.
	Threads int
}

// OptionsFrom are the runner's options from the server's configuration.
func OptionsFrom(c config.Gufi) RunnerOptions {
	return RunnerOptions{Concurrency: c.Concurrency, QueueMax: c.QueueMax, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond, Threads: c.Threads}
}

// NewRunner admits runs on e.
func NewRunner(e Executor, log *slog.Logger, o RunnerOptions) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Runner{exec: e, log: log, concurrency: o.Concurrency, queueMax: o.QueueMax, timeout: o.Timeout, threads: o.Threads}
	if r.concurrency <= 0 {
		r.concurrency = 2
	}
	if r.queueMax <= 0 {
		r.queueMax = 256
	}
	if r.timeout <= 0 {
		r.timeout = 60 * time.Second
	}
	return r
}

// Stats is a snapshot of the runner's counts.
func (r *Runner) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Running, s.Queued = r.active, len(r.waiters)
	return s
}

// acquire waits for a slot in arrival order. It returns the queue depth
// it found.
func (r *Runner) acquire(ctx context.Context) (int, error) {
	r.mu.Lock()
	depth := len(r.waiters)
	if r.active < r.concurrency && depth == 0 {
		r.active++
		r.mu.Unlock()
		return 0, nil
	}
	if depth >= r.queueMax {
		r.stats.Refused++
		r.mu.Unlock()
		return depth, ErrBusy
	}
	w := &waiter{ready: make(chan struct{})}
	r.waiters = append(r.waiters, w)
	if len(r.waiters) > r.stats.MaxQueued {
		r.stats.MaxQueued = len(r.waiters)
	}
	r.mu.Unlock()
	select {
	case <-w.ready:
		return depth, nil
	case <-ctx.Done():
		r.mu.Lock()
		defer r.mu.Unlock()
		if w.granted {
			// The slot came as the request ended: pass it on.
			r.releaseLocked()
		} else {
			for i, x := range r.waiters {
				if x == w {
					r.waiters = append(r.waiters[:i], r.waiters[i+1:]...)
					break
				}
			}
		}
		r.stats.Cancelled++
		return depth, ctx.Err()
	}
}

func (r *Runner) releaseLocked() {
	if len(r.waiters) > 0 {
		w := r.waiters[0]
		r.waiters = r.waiters[1:]
		w.granted = true
		close(w.ready)
		return
	}
	r.active--
}

func (r *Runner) release() {
	r.mu.Lock()
	r.releaseLocked()
	r.mu.Unlock()
}

// capThreads lowers a -n above the runner's thread budget.
func (r *Runner) capThreads(args []string) []string {
	if r.threads <= 0 {
		return args
	}
	var out []string
	for i, a := range args {
		if i > 0 && args[i-1] == "-n" {
			if n, err := strconv.Atoi(a); err == nil && n > r.threads {
				if out == nil {
					out = append([]string(nil), args...)
				}
				out[i] = strconv.Itoa(r.threads)
			}
		}
	}
	if out == nil {
		return args
	}
	return out
}

// Run admits c, runs it, and logs it.
func (r *Runner) Run(ctx context.Context, c Command) (Result, error) {
	t0 := time.Now()
	depth, err := r.acquire(ctx)
	wait := time.Since(t0)
	if err != nil {
		r.log.Warn("gufi run not admitted", "tool", c.Tool, "queue_depth", depth, "wait_ms", ms(wait), "err", err.Error())
		return Result{}, err
	}
	defer r.release()
	if c.Timeout <= 0 {
		c.Timeout = r.timeout
	}
	c.Args = r.capThreads(c.Args)
	res, err := r.exec.Exec(ctx, c)
	r.mu.Lock()
	r.stats.Runs++
	r.stats.WaitMS += ms(wait)
	r.stats.MaxWaitMS = max(r.stats.MaxWaitMS, ms(wait))
	r.stats.RunMS += ms(res.Elapsed)
	if errors.Is(err, ErrTimeout) {
		r.stats.TimedOut++
	} else if ctx.Err() != nil {
		r.stats.Cancelled++
	}
	r.mu.Unlock()
	attrs := []any{"tool", c.Tool, "queue_depth", depth, "wait_ms", ms(wait), "run_ms", ms(res.Elapsed), "exit", res.ExitCode, "stdout_bytes", len(res.Stdout)}
	if res.Signal != "" {
		attrs = append(attrs, "signal", res.Signal)
	}
	if err != nil {
		r.log.Warn("gufi run", append(attrs, "err", err.Error())...)
	} else {
		r.log.Info("gufi run", attrs...)
	}
	return res, err
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
