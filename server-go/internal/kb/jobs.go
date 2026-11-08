package kb

import (
	"context"
	"sync"
	"time"
)

// JobIDBase is where the Go server's job ids start. Node numbers its own
// jobs (jobs.ts) from 1 in the same id space, which GET /api/jobs/:id
// serves for both while Node runs behind the Go server: ids from here up
// are Go's, the rest Node's.
const JobIDBase = 1 << 20

// Job is jobs.ts's Job, in memory: only interesting while the page that
// started it is open.
type Job struct {
	ID         int
	Kind       string // move, delete or index
	Phase      string
	State      string // running, done or error
	Done       int
	Total      int
	Current    string
	Result     any
	Error      *string
	StartedAt  string
	FinishedAt *string
	MS         *int64
}

func (j *Job) obj() *Obj {
	return O("id", j.ID, "kind", j.Kind, "phase", j.Phase, "state", j.State, "done", j.Done, "total", j.Total,
		"current", j.Current, "result", j.Result, "error", j.Error, "startedAt", j.StartedAt,
		"finishedAt", j.FinishedAt, "ms", j.MS)
}

// Progress patches a job's progress; nil fields are left as they are.
type Progress struct {
	Phase   *string
	Done    *int
	Total   *int
	Current *string
}

// Jobs keeps the jobs started here.
type Jobs struct {
	mu   sync.Mutex
	seq  int
	jobs map[int]*Job
	ord  []int
}

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// JobHandle reports a job's progress.
type JobHandle struct {
	j    *Jobs
	job  *Job
}

// Progress is handle.progress: Object.assign of the given fields.
func (h *JobHandle) Progress(phase string, done int, current string) {
	h.j.mu.Lock()
	defer h.j.mu.Unlock()
	h.job.Phase, h.job.Done, h.job.Current = phase, done, current
}

// Phase sets the phase and the current item only.
func (h *JobHandle) Phase(phase, current string) {
	h.j.mu.Lock()
	defer h.j.mu.Unlock()
	h.job.Phase, h.job.Current = phase, current
}

// Start is startJob: the job is registered, first runs sync (the work Node
// does before its first await, so the answer shows the same phase), then
// run goes on in the background. The returned object is the job as it is
// when the route answers.
func (j *Jobs) Start(ctx context.Context, kind string, total int, first func(h *JobHandle), run func(ctx context.Context, h *JobHandle) (any, error)) *Obj {
	j.mu.Lock()
	if j.jobs == nil {
		j.jobs = map[int]*Job{}
	}
	j.seq++
	job := &Job{ID: JobIDBase + j.seq, Kind: kind, Phase: "starting", State: "running", Total: total, StartedAt: isoNow()}
	j.jobs[job.ID] = job
	j.ord = append(j.ord, job.ID)
	// Finished jobs are kept only long enough to be read once or twice.
	keep := j.ord[:0]
	for _, id := range j.ord {
		if len(j.jobs) > 30 && j.jobs[id].State != "running" {
			delete(j.jobs, id)
			continue
		}
		keep = append(keep, id)
	}
	j.ord = keep
	j.mu.Unlock()

	h := &JobHandle{j: j, job: job}
	if first != nil {
		first(h)
	}
	snapshot := j.snapshot(job)
	t0 := time.Now()
	go func() {
		res, err := run(ctx, h)
		j.mu.Lock()
		defer j.mu.Unlock()
		if err != nil {
			msg := SliceUTF16(errMessage(err), 400)
			job.Error, job.State, job.Phase = &msg, "error", "error"
		} else {
			job.Result, job.State, job.Phase = res, "done", "done"
		}
		ms := time.Since(t0).Milliseconds()
		at := isoNow()
		job.MS, job.FinishedAt = &ms, &at
	}()
	return snapshot
}

func (j *Jobs) snapshot(job *Job) *Obj {
	j.mu.Lock()
	defer j.mu.Unlock()
	return job.obj()
}

// Get is getJob, nil when the id is not one of this server's.
func (j *Jobs) Get(id int) *Obj {
	j.mu.Lock()
	job := j.jobs[id]
	j.mu.Unlock()
	if job == nil {
		return nil
	}
	return j.snapshot(job)
}

// Running reports whether a job is running, for the update busy check.
func (j *Jobs) Running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.jobs {
		if job.State == "running" {
			return true
		}
	}
	return false
}
