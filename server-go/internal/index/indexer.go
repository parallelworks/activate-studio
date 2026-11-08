// Package index is the main folder's index in Go: indexing.ts, jobs.ts,
// indexRoutes.ts and the modules around them. It owns the index job queue
// (one job at a time), the background sync, the record of runs and the
// status every view reads, and it builds an index in four phases:
//
//	names      gufi_dir2index: names, sizes and dates of every file and folder
//	documents  the extraction pass (`studio-server extract`, a child process)
//	text       the words tables, written here (enrich.py's write step)
//	meaning    the embedder (`studio-server embed`, a child process)
//
// The files it keeps under INDEX_BASE and INDEX_DIR are Node's, in Node's
// formats, so either server reads what the other wrote.
//
// The API other tracks use (the files track for uploads, moves, copies,
// renames and deletes; the libraries track for builds):
//
//	ix := index.Of(a)                         the process's indexer (Register makes it)
//	ix.Enqueue(rel) *IndexJob                 startIndexJob(rel): the folder covering rel, at once
//	ix.ReindexForFile(ctx, rel) error         the subtree (or the root db) covering a file, waited on
//	ix.ReindexForDir(ctx, rel) error          the same for a folder
//	ix.IndexDir(ctx, rel) (ms, error)         incrementalIndexDir
//	ix.StartBuild() IndexJob                  the whole folder
//	ix.Running() bool                         a job is queued or running
//	ix.RequestSweep(delay)                    a sync soon
//	ix.Job(id) (IndexJob, bool)               getIndexJob
//	ix.Jobs                                   jobs.ts: Start(kind, total, work), Get(id)
//	ix.Settings                               indexSettings.ts: Get, Built, Save, Threads, ForgetLibrary
//	ix.OnChange(func(rel string))             after the index changed (afterChange)
//	ix.Hooks                                  what other tracks provide (requireWritable, the map rebuild)
//	ix.Names / Documents / Text / Meaning     the phases, for a library build over its own paths
package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// Paths are the files and folders the index lives in (config.ts).
type Paths struct {
	KBRoot, IndexBase, IndexDir, GufiIndex, GufiBin, ProjectRoot string
	ExtractCache, EmbedCache, SemanticMapDir                     string
	SkipFile, SweepState, DepthState, EmbedModel, RunsFile       string
	RootMark, OldRootMark, Stash                                 string
	LogDir                                                       string
}

// PathsOf are the paths for a configuration.
func PathsOf(c config.Config) Paths {
	p := Paths{KBRoot: c.KBRoot, IndexBase: c.IndexBase, IndexDir: c.IndexDir, GufiIndex: c.GufiIndex, GufiBin: c.GufiBin, ProjectRoot: c.ProjectRoot}
	p.ExtractCache = filepath.Join(c.IndexDir, "extract")
	p.EmbedCache = filepath.Join(c.IndexDir, "embed-cache.db")
	p.SemanticMapDir = filepath.Join(c.IndexDir, "semantic-map")
	p.SkipFile = filepath.Join(c.IndexBase, "skip-dirs.txt")
	p.SweepState = filepath.Join(c.IndexBase, "sweep-state.json")
	p.DepthState = filepath.Join(c.IndexBase, "index-depth.json")
	p.EmbedModel = filepath.Join(c.IndexBase, "models", "minilm384.gguf")
	p.RunsFile = filepath.Join(c.IndexBase, "index-runs.json")
	p.RootMark = filepath.Join(c.IndexDir, "indexed-root")
	p.OldRootMark = filepath.Join(c.IndexBase, "indexed-root")
	p.Stash = filepath.Join(c.IndexDir, "folders")
	p.LogDir = app.LogDir(c.IndexBase)
	return p
}

// Hooks are what other modules provide; each has a default that serves
// until its owner sets it.
type Hooks struct {
	// RequireWritable refuses an index action on a library that is not
	// the main folder's, or not writable (libraries.ts: getLibrary and
	// requireWritable). The default allows the main folder only.
	RequireWritable func(r *http.Request, library string) error
	// ScheduleMapRebuild asks for the semantic map to be rebuilt after
	// delay (semanticMap.ts). The default builds the main folder's map in
	// this process.
	ScheduleMapRebuild func(delay time.Duration)
	// RequestAllLibrarySyncs is libraryBuild.ts's: the libraries built
	// here follow the main folder's speed of search by meaning.
	RequestAllLibrarySyncs func()
	// SofficeFound is LibreOffice's soffice, or "" (extractors.ts).
	SofficeFound func() string
	// InvalidateContext drops the assistant's cached view of the folder
	// (chat/context.ts), when a first build's text is in.
	InvalidateContext func()
}

// Indexer is the main folder's index jobs and sync.
type Indexer struct {
	App      *app.App
	P        Paths
	Settings *SettingsStore
	Log      *Log
	Jobs     *Jobs
	Hooks    Hooks
	History  *History

	mu           sync.Mutex
	seq          int
	recs         map[int]*jobRec
	order        []int
	queue        []*jobRec
	current      *jobRec
	lastFinished *IndexJob
	status       sweepStatus

	sweeping     bool
	sweepWanted  bool
	sweepTimerOn bool
	kickTimer    *time.Timer
	kbDenied     []string

	changeMu sync.Mutex
	onChange []func(rel string)

	reconcileMu sync.Mutex
	mapState    mapState

	// holdFile, when set, holds each job at the start of its text phase
	// while the file exists (STUDIO_INDEX_HOLD_FILE): for tests that stop
	// a running job, which Node's held through a slow Python.
	holdFile string
}

type sweepStatus struct {
	LastSweepAt      *string  `json:"lastSweepAt"`
	LastSweepMs      *int64   `json:"lastSweepMs"`
	LastChanges      []string `json:"lastChanges"`
	LastError        *string  `json:"lastError"`
	SweepIntervalSec float64  `json:"sweepIntervalSec"`
}

var (
	instMu sync.Mutex
	inst   = map[*app.App]*Indexer{}
)

// Of is the indexer of a, made on first use.
func Of(a *app.App) *Indexer {
	instMu.Lock()
	defer instMu.Unlock()
	if ix := inst[a]; ix != nil {
		return ix
	}
	ix := New(a, PathsOf(a.Cfg))
	inst[a] = ix
	return ix
}

// New is an indexer over p.
func New(a *app.App, p Paths) *Indexer {
	ix := &Indexer{App: a, P: p, Settings: NewSettingsStore(p.IndexBase), Log: NewLog(p.LogDir), Jobs: NewJobs(), recs: map[int]*jobRec{}}
	ix.status.LastChanges = []string{}
	ix.status.SweepIntervalSec = 300
	if v := os.Getenv("SWEEP_INTERVAL_SEC"); v != "" {
		var f float64
		if _, err := fmt.Sscan(v, &f); err == nil {
			ix.status.SweepIntervalSec = f
		}
	}
	ix.holdFile = os.Getenv("STUDIO_INDEX_HOLD_FILE")
	ix.Hooks = Hooks{
		RequireWritable: func(_ *http.Request, library string) error {
			if library == "" || library == "kb" {
				return nil
			}
			return &httpError{404, "no such library"}
		},
		RequestAllLibrarySyncs: func() {},
		SofficeFound:           findSoffice,
		InvalidateContext:      func() {},
	}
	ix.Hooks.ScheduleMapRebuild = ix.scheduleMap
	return ix
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string   { return e.msg }
func (e *httpError) StatusCode() int { return e.status }

// OnChange adds fn, called after each change to the index with the path
// below the main folder that changed ("" for all of it): tags reapply
// their overlay, search drops its caches.
func (ix *Indexer) OnChange(fn func(rel string)) {
	ix.changeMu.Lock()
	ix.onChange = append(ix.onChange, fn)
	ix.changeMu.Unlock()
}

func (ix *Indexer) afterChange(rel string) {
	ix.changeMu.Lock()
	fns := make([]func(string), len(ix.onChange))
	copy(fns, ix.onChange)
	ix.changeMu.Unlock()
	for _, fn := range fns {
		func() {
			defer func() { recover() }()
			fn(rel)
		}()
	}
}

// ---- jobs ----

// Phase is one phase of an index job, in Node's key order.
type Phase struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Unit  string  `json:"unit"`
	State string  `json:"state"`
	Done  int     `json:"done"`
	Total int     `json:"total"`
	Step  *string `json:"step"`
	MS    *int64  `json:"ms"`
	Note  *string `json:"note"`
}

// Progress is the summary pages written before phases read.
type Progress struct {
	Phase   string   `json:"phase"`
	Done    int      `json:"done"`
	Total   int      `json:"total"`
	Current *string  `json:"current"`
	Failed  []string `json:"failed"`
}

// IndexJob is an index job as the API shows it.
type IndexJob struct {
	ID           int       `json:"id"`
	Kind         string    `json:"kind"`
	Dir          string    `json:"dir"`
	Label        string    `json:"label"`
	State        string    `json:"state"`
	StartedAt    string    `json:"startedAt"`
	RunningAt    *string   `json:"runningAt"`
	FinishedAt   *string   `json:"finishedAt"`
	MS           *int64    `json:"ms"`
	Error        *string   `json:"error"`
	Threads      int       `json:"threads"`
	Phases       []*Phase  `json:"phases"`
	SearchableAt *string   `json:"searchableAt"`
	Warnings     []string  `json:"warnings"`
	WarningCount int       `json:"warningCount"`
	Progress     *Progress `json:"progress,omitempty"`
}

// clone is a deep copy, for reading outside the lock.
func (j *IndexJob) clone() IndexJob {
	c := *j
	c.Phases = make([]*Phase, len(j.Phases))
	for i, p := range j.Phases {
		x := *p
		c.Phases[i] = &x
	}
	c.Warnings = append([]string{}, j.Warnings...)
	if j.Progress != nil {
		p := *j.Progress
		p.Failed = append([]string{}, j.Progress.Failed...)
		c.Progress = &p
	}
	return c
}

var phaseDefs = []struct{ id, label, unit string }{
	{"names", "Names and folders", "folders"},
	{"documents", "Documents (PDF, Word, PowerPoint, Excel)", "documents"},
	{"text", "Text, images and full-text search", "items"},
	{"meaning", "Search by meaning", "passages"},
}

func newPhases() []*Phase {
	out := make([]*Phase, len(phaseDefs))
	for i, d := range phaseDefs {
		out[i] = &Phase{ID: d.id, Label: d.label, State: "pending", Unit: d.unit}
	}
	return out
}

// ErrCancelled is a job that was stopped.
var ErrCancelled = errors.New("stopped")

type jobRec struct {
	pub    *IndexJob
	run    func(c *jobCtx) error
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	key    string
}

// jobCtx is what a running job's work gets.
type jobCtx struct {
	ix       *Indexer
	rec      *jobRec
	ctx      context.Context
	threads  int
	settings Settings
}

func (c *jobCtx) log(msg string) {
	c.ix.mu.Lock()
	id, kind := c.rec.pub.ID, c.rec.pub.Kind
	c.ix.mu.Unlock()
	c.ix.Log.Write(fmt.Sprintf("[%d %s] %s", id, kind, msg))
}

// with runs fn on the job under the lock.
func (c *jobCtx) with(fn func(j *IndexJob)) {
	c.ix.mu.Lock()
	fn(c.rec.pub)
	c.ix.mu.Unlock()
}

func (c *jobCtx) aborted() bool { return c.ctx.Err() != nil }

func (ix *Indexer) requireIndexWork() error {
	if ix.App != nil && ix.App.Lock != nil {
		return ix.App.Lock.RequireIndexWork()
	}
	return nil
}

func (ix *Indexer) enqueue(kind, dir, label string, run func(c *jobCtx) error, key string) (*jobRec, error) {
	if err := ix.requireIndexWork(); err != nil {
		return nil, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if key != "" {
		for _, r := range ix.queue {
			if r.key == key {
				return r, nil
			}
		}
	}
	ix.seq++
	phases := newPhases()
	if kind == "delete" || kind == "move" {
		phases = []*Phase{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rec := &jobRec{
		pub: &IndexJob{
			ID: ix.seq, Kind: kind, Dir: dir, Label: label, State: "queued", StartedAt: isoNow(),
			Threads: ix.Settings.Threads("kb"), Phases: phases, Warnings: []string{},
			Progress: &Progress{Phase: "queued", Failed: []string{}},
		},
		run: run, ctx: ctx, cancel: cancel, done: make(chan struct{}), key: key,
	}
	ix.recs[rec.pub.ID] = rec
	ix.order = append(ix.order, rec.pub.ID)
	// Old jobs are only interesting while a page is still polling them.
	for _, id := range append([]int(nil), ix.order...) {
		r := ix.recs[id]
		if len(ix.recs) > 30 && r.pub.State != "running" && r.pub.State != "queued" {
			delete(ix.recs, id)
			ix.order = remove(ix.order, id)
		}
	}
	ix.queue = append(ix.queue, rec)
	go ix.pump()
	return rec, nil
}

func (ix *Indexer) pump() {
	ix.mu.Lock()
	if ix.current != nil || len(ix.queue) == 0 {
		ix.mu.Unlock()
		return
	}
	rec := ix.queue[0]
	ix.queue = ix.queue[1:]
	ix.current = rec
	job := rec.pub
	job.State = "running"
	now := isoNow()
	job.RunningAt = &now
	job.Threads = ix.Settings.Threads("kb")
	c := &jobCtx{ix: ix, rec: rec, ctx: rec.ctx, threads: job.Threads, settings: ix.Settings.Get("kb")}
	label := job.Label
	ix.mu.Unlock()
	t0 := time.Now()
	c.log(fmt.Sprintf("%s: started on %d threads", label, c.threads))
	go func() {
		err := safeErr(func() error { return rec.run(c) })
		if err == nil && rec.ctx.Err() != nil {
			err = ErrCancelled
		}
		ix.mu.Lock()
		switch {
		case err == nil:
			job.State = "done"
		case rec.ctx.Err() != nil || errors.Is(err, ErrCancelled):
			job.State = "cancelled"
			for _, p := range job.Phases {
				if p.State == "running" {
					p.State = "failed"
					p.Note = strp("stopped")
				}
			}
		default:
			job.State = "error"
			e := truncRunes(err.Error(), 600)
			job.Error = &e
			for _, p := range job.Phases {
				if p.State == "running" {
					p.State = "failed"
				}
			}
		}
		ms := time.Since(t0).Milliseconds()
		f := isoNow()
		job.MS, job.FinishedAt = &ms, &f
		if job.Progress != nil {
			if job.State == "done" {
				job.Progress.Phase = "done"
			} else {
				job.Progress.Phase = job.State
			}
			job.Progress.Current = nil
		}
		line := fmt.Sprintf("%s: %s in %s", job.Label, job.State, fmtMs(ms))
		if job.Error != nil {
			line += ": " + *job.Error
		}
		if job.WarningCount > 0 {
			line += fmt.Sprintf(" (%d files could not be read)", job.WarningCount)
		}
		routine := job.Kind == "folder" && job.State == "done"
		var lines []string
		lines = append(lines, line)
		if !routine {
			for _, p := range job.Phases {
				if p.MS != nil || p.State == "skipped" {
					l := "  " + p.ID + ": " + p.State
					if p.MS != nil {
						l += " in " + fmtMs(*p.MS)
					}
					if p.Total > 0 {
						l += fmt.Sprintf(", %d of %d %s", p.Done, p.Total, p.Unit)
					}
					if p.Note != nil {
						l += " (" + *p.Note + ")"
					}
					lines = append(lines, l)
				}
			}
		}
		snap := job.clone()
		ix.lastFinished = &snap
		if job.State == "error" {
			e := job.Label + ": " + *job.Error
			ix.status.LastError = &e
		} else if job.State == "done" && (job.Kind == "build" || job.Kind == "delete") {
			ix.status.LastError = nil
		}
		ix.current = nil
		if job.State != "done" {
			if job.State == "cancelled" {
				rec.err = ErrCancelled
			} else {
				rec.err = errors.New(*job.Error)
			}
		}
		wantSweep := ix.sweepWanted && len(ix.queue) == 0
		ix.mu.Unlock()
		for _, l := range lines {
			c.log(l)
		}
		if !routine {
			ix.recordRun(snap)
		}
		close(rec.done)
		go ix.pump()
		if wantSweep {
			go ix.runWantedSweep()
		}
	}()
}

func safeErr(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("internal error: %v", p)
		}
	}()
	return fn()
}

func strp(s string) *string { return &s }

// fmtMs is indexing.ts's fmtMs.
func fmtMs(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%d ms", ms)
	case ms < 120_000:
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	}
	return fmt.Sprintf("%d min %d s", ms/60_000, jsRound(float64(ms%60_000)/1000))
}

func jsRound(f float64) int64 {
	return int64(f + 0.5)
}

// phase is a job's bookkeeping for one phase.
type phaseH struct {
	c  *jobCtx
	p  *Phase
	t0 time.Time
}

func (c *jobCtx) phase(id string) *phaseH {
	c.ix.mu.Lock()
	defer c.ix.mu.Unlock()
	for _, p := range c.rec.pub.Phases {
		if p.ID == id {
			return &phaseH{c: c, p: p}
		}
	}
	// A job without phases (a sweep's job gets new ones per folder).
	p := &Phase{ID: id}
	return &phaseH{c: c, p: p}
}

// sync is the summary pages written before phases read.
func (h *phaseH) sync() {
	j := h.c.rec.pub
	if j.Progress != nil {
		j.Progress.Phase = "directories"
		l := h.p.Label
		j.Progress.Current = &l
		j.Progress.Done = h.p.Done
		j.Progress.Total = h.p.Total
	}
}

func (h *phaseH) start(total int) error {
	if h.c.aborted() {
		return ErrCancelled
	}
	h.c.with(func(*IndexJob) {
		h.t0 = time.Now()
		h.p.State, h.p.Done, h.p.Total, h.p.Step, h.p.MS, h.p.Note = "running", 0, total, nil, nil, nil
		h.sync()
	})
	return nil
}

func (h *phaseH) progress(done, total int, step *string) {
	h.c.with(func(*IndexJob) {
		h.p.Done, h.p.Total = done, total
		if step != nil {
			if *step == "" {
				h.p.Step = nil
			} else {
				s := *step
				h.p.Step = &s
			}
		}
		h.sync()
	})
}

func (h *phaseH) finish(note string) {
	h.c.with(func(*IndexJob) {
		h.p.State = "done"
		ms := time.Since(h.t0).Milliseconds()
		h.p.MS = &ms
		h.p.Step = nil
		if h.p.Total > 0 {
			h.p.Done = h.p.Total
		}
		if note != "" {
			h.p.Note = &note
		}
		h.sync()
	})
}

func (h *phaseH) skip(note string) {
	h.c.with(func(*IndexJob) {
		h.p.State, h.p.Note, h.p.MS, h.p.Step = "skipped", &note, nil, nil
	})
}

// Job is getIndexJob.
func (ix *Indexer) Job(id int) (IndexJob, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	r := ix.recs[id]
	if r == nil {
		return IndexJob{}, false
	}
	return r.pub.clone(), true
}

// Running is indexJobsRunning.
func (ix *Indexer) Running() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.current != nil || len(ix.queue) > 0
}

// Cancel is cancelIndexJob.
func (ix *Indexer) Cancel(id int) (IndexJob, bool) {
	ix.mu.Lock()
	rec := ix.recs[id]
	if rec == nil {
		ix.mu.Unlock()
		return IndexJob{}, false
	}
	var msg string
	if i := indexOf(ix.queue, rec); i >= 0 {
		ix.queue = append(ix.queue[:i], ix.queue[i+1:]...)
		now := isoNow()
		var zero int64
		rec.pub.State, rec.pub.FinishedAt, rec.pub.MS = "cancelled", &now, &zero
		msg = fmt.Sprintf("[%d %s] %s: dropped from the queue", rec.pub.ID, rec.pub.Kind, rec.pub.Label)
		rec.err = ErrCancelled
		close(rec.done)
	} else if rec == ix.current && rec.ctx.Err() == nil {
		msg = fmt.Sprintf("[%d %s] %s: stopping", rec.pub.ID, rec.pub.Kind, rec.pub.Label)
		rec.cancel()
	}
	snap := rec.pub.clone()
	ix.mu.Unlock()
	if msg != "" {
		ix.Log.Write(msg)
	}
	return snap, true
}

func indexOf(q []*jobRec, r *jobRec) int {
	for i, x := range q {
		if x == r {
			return i
		}
	}
	return -1
}

// CancelAll is cancelAllIndexJobs: the running job and every queued one.
func (ix *Indexer) CancelAll() int {
	ix.mu.Lock()
	ids := make([]int, 0, len(ix.queue)+1)
	for _, r := range ix.queue {
		ids = append(ids, r.pub.ID)
	}
	if ix.current != nil {
		ids = append(ids, ix.current.pub.ID)
	}
	ix.mu.Unlock()
	for _, id := range ids {
		ix.Cancel(id)
	}
	return len(ids)
}

// wait waits for a job and returns its ms or its error.
func (rec *jobRec) wait(ctx context.Context) (int64, error) {
	select {
	case <-rec.done:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if rec.err != nil {
		return 0, rec.err
	}
	var ms int64
	if rec.pub.MS != nil {
		ms = *rec.pub.MS
	}
	return ms, nil
}

// ---- the record of runs ----

type runsFile struct {
	LastBuild json.RawMessage   `json:"lastBuild"`
	Runs      []json.RawMessage `json:"runs"`
}

func (ix *Indexer) loadRuns() runsFile {
	var r runsFile
	b, err := os.ReadFile(ix.P.RunsFile)
	if err != nil || json.Unmarshal(b, &r) != nil {
		return runsFile{LastBuild: json.RawMessage("null"), Runs: []json.RawMessage{}}
	}
	if len(r.LastBuild) == 0 {
		r.LastBuild = json.RawMessage("null")
	}
	if r.Runs == nil {
		r.Runs = []json.RawMessage{}
	}
	return r
}

// runRecord is a job as the record keeps it: the first five warnings, the
// folder, no progress.
func runRecord(j IndexJob, root string) json.RawMessage {
	j.Progress = nil
	if len(j.Warnings) > 5 {
		j.Warnings = j.Warnings[:5]
	}
	b, _ := json.Marshal(j)
	r, _ := json.Marshal(root)
	// {...job, root}: root last.
	return append(append(b[:len(b)-1], []byte(`,"root":`)...), append(r, '}')...)
}

func (ix *Indexer) recordRun(j IndexJob) {
	r := ix.loadRuns()
	kept := runRecord(j, ix.P.KBRoot)
	r.Runs = append([]json.RawMessage{kept}, r.Runs...)
	if len(r.Runs) > 25 {
		r.Runs = r.Runs[:25]
	}
	if j.Kind == "build" || j.Kind == "delete" {
		r.LastBuild = kept
	}
	os.MkdirAll(filepath.Dir(ix.P.RunsFile), 0o755)
	b, _ := json.Marshal(r)
	os.WriteFile(ix.P.RunsFile, b, 0o644)
}

type runHead struct {
	Kind  string  `json:"kind"`
	State string  `json:"state"`
	Root  *string `json:"root"`
}

func (ix *Indexer) mine(raw json.RawMessage) bool {
	var h runHead
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &h) != nil {
		return false
	}
	return h.Root == nil || *h.Root == "" || *h.Root == ix.P.KBRoot
}

// PrimaryBuilt is setPrimaryBuiltSource's answer: the main folder's last
// whole build finished.
func (ix *Indexer) PrimaryBuilt() bool {
	r := ix.loadRuns()
	if !ix.mine(r.LastBuild) {
		return false
	}
	var h runHead
	json.Unmarshal(r.LastBuild, &h)
	return h.Kind == "build" && h.State == "done"
}

// PrimaryIndexing is setPrimaryIndexingSource's answer: the running job
// is the main folder's first build.
func (ix *Indexer) PrimaryIndexing() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.current != nil && ix.current.pub.Kind == "build"
}

// ---- the status every view reads ----

// Status is indexStatus(), in Node's key order.
func (ix *Indexer) Status() map[string]any {
	r := ix.loadRuns()
	lastBuild := json.RawMessage("null")
	if ix.mine(r.LastBuild) {
		lastBuild = r.LastBuild
	}
	runs := []json.RawMessage{}
	for _, x := range r.Runs {
		if ix.mine(x) && len(runs) < 10 {
			runs = append(runs, x)
		}
	}
	var lb runHead
	json.Unmarshal(lastBuild, &lb)
	ix.mu.Lock()
	var job any = nil
	if ix.current != nil {
		c := ix.current.pub.clone()
		job = c
	}
	queued := []map[string]any{}
	for _, q := range ix.queue {
		queued = append(queued, map[string]any{"id": q.pub.ID, "kind": q.pub.Kind, "label": q.pub.Label})
	}
	var last any = nil
	if ix.lastFinished != nil {
		last = ix.lastFinished
	}
	st := ix.status
	st.LastChanges = append([]string{}, st.LastChanges...)
	running := ix.current != nil || len(ix.queue) > 0
	ix.mu.Unlock()
	_, err := os.Stat(filepath.Join(ix.P.GufiIndex, "db.db"))
	return ordered{
		{"lastSweepAt", st.LastSweepAt}, {"lastSweepMs", st.LastSweepMs}, {"lastChanges", st.LastChanges},
		{"lastError", st.LastError}, {"sweepIntervalSec", st.SweepIntervalSec},
		{"running", running}, {"job", job}, {"queued", queued}, {"last", last}, {"lastBuild", lastBuild}, {"runs", runs},
		{"indexed", err == nil}, {"incomplete", string(lastBuild) != "null" && lb.Kind == "build" && lb.State != "done"},
		{"threads", ix.Settings.Threads("kb")}, {"folder", ix.P.KBRoot}, {"indexDir", ix.P.IndexDir},
	}.Map()
}

// ordered is a JSON object with its keys in order.
type ordered [][2]any

// Map is the object, for an encoder that keeps the order (MarshalJSON).
func (o ordered) Map() map[string]any { return map[string]any{"\x00ordered": o} }

func (o ordered) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv[0])
		b.Write(k)
		b.WriteByte(':')
		v, err := marshal(kv[1])
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// marshal is json.Marshal without HTML escaping, and with ordered maps.
func marshal(v any) ([]byte, error) {
	if m, ok := v.(map[string]any); ok {
		if o, ok := m["\x00ordered"].(ordered); ok {
			return o.MarshalJSON()
		}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

func randomInt(n int) int { return rand.Intn(n) }
