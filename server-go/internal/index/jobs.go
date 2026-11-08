package index

import (
	"sync"
	"time"
)

// Job is jobs.ts's job: background work with progress that a page
// watches (a bulk move, the libraries' builds), kept in memory.
type Job struct {
	ID         int     `json:"id"`
	Kind       string  `json:"kind"`
	Phase      string  `json:"phase"`
	State      string  `json:"state"`
	Done       int     `json:"done"`
	Total      int     `json:"total"`
	Current    string  `json:"current"`
	Result     any     `json:"result"`
	Error      *string `json:"error"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
	MS         *int64  `json:"ms"`
}

// JobHandle lets a job's work report progress.
type JobHandle struct {
	j  *Jobs
	ID int
}

// Progress sets the fields a job's work moves; nil leaves one as it is.
func (h JobHandle) Progress(phase *string, done, total *int, current *string) {
	h.j.mu.Lock()
	defer h.j.mu.Unlock()
	job := h.j.byID[h.ID]
	if job == nil {
		return
	}
	if phase != nil {
		job.Phase = *phase
	}
	if done != nil {
		job.Done = *done
	}
	if total != nil {
		job.Total = *total
	}
	if current != nil {
		job.Current = *current
	}
}

// Jobs is jobs.ts: startJob and getJob.
type Jobs struct {
	mu    sync.Mutex
	seq   int
	byID  map[int]*Job
	order []int
}

// NewJobs is an empty set.
func NewJobs() *Jobs { return &Jobs{byID: map[int]*Job{}} }

// Start runs work in the background as a job of kind, total items.
func (j *Jobs) Start(kind string, total int, work func(h JobHandle) (any, error)) Job {
	j.mu.Lock()
	j.seq++
	job := &Job{ID: j.seq, Kind: kind, Phase: "starting", State: "running", Total: total, StartedAt: isoNow()}
	j.byID[job.ID] = job
	j.order = append(j.order, job.ID)
	// Finished jobs are kept only long enough to be read once or twice.
	for _, id := range append([]int(nil), j.order...) {
		if len(j.byID) > 30 && j.byID[id].State != "running" {
			delete(j.byID, id)
			j.order = remove(j.order, id)
		}
	}
	snap := *job
	j.mu.Unlock()
	t0 := time.Now()
	go func() {
		res, err := safeRun(func() (any, error) { return work(JobHandle{j, snap.ID}) })
		j.mu.Lock()
		defer j.mu.Unlock()
		if err != nil {
			e := truncRunes(err.Error(), 400)
			job.Error, job.State, job.Phase = &e, "error", "error"
		} else {
			job.Result, job.State, job.Phase = res, "done", "done"
		}
		ms := time.Since(t0).Milliseconds()
		f := isoNow()
		job.MS, job.FinishedAt = &ms, &f
	}()
	return snap
}

// Get is getJob: a copy, or false.
func (j *Jobs) Get(id int) (Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.byID[id]
	if job == nil {
		return Job{}, false
	}
	return *job, true
}

func remove(xs []int, x int) []int {
	for i, v := range xs {
		if v == x {
			return append(xs[:i], xs[i+1:]...)
		}
	}
	return xs
}

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func safeRun(fn func() (any, error)) (v any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicError{p}
		}
	}()
	return fn()
}

type panicError struct{ p any }

func (e panicError) Error() string { return "internal error" }

// truncRunes is String(x).slice(0, n) over UTF-16 code units, close
// enough as code points for the messages here.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
