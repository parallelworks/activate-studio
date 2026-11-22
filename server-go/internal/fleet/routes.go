package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Template is a starting point for a new agent.
type Template struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Persona   string  `json:"persona"`
	Every     string  `json:"every"`
	OnRunEnd  bool    `json:"onRunEnd"`
	WatchPath *string `json:"watchPath"`
	Goal      string  `json:"goal"`
}

// Templates are the agents the Fleet page offers to start from.
var Templates = []Template{
	{Key: "benchmark-campaign", Name: "Nightly benchmark campaign", Persona: "campaign_runner", Every: "1d", OnRunEnd: true,
		Goal: `Keep a small CPU benchmark campaign running on the system named in AGENTS.md or, if none, ask for one (NEEDS DECISION). Each round: submit the activatebatch workflow on the debug partition with a 20 minute walltime, one node, and a Python script that times 40 rounds of a 2000x2000 numpy matrix multiply and prints one CSV line per round (round,seconds,gflops). Follow the run to completion, read its output, and append the rounds to results.md in your folder with the date, run id, node, and median GFLOPS. Report the trend across rounds in one paragraph. If a run fails for a mechanical reason (partition, walltime, GRES) correct it once and resubmit; otherwise record the failure and stop.`},
	{Key: "queue-watcher", Name: "Queue and run watcher", Persona: "watcher", Every: "30m", OnRunEnd: true,
		Goal: `Watch the connected systems and the runs on this account. Each round: look at the systems' status and the workflow runs; for any run that ended since your last round, read its detail and classify the outcome in one sentence with the line that shows it; note systems that are degraded or have long queue waits. Keep status.md in your folder: one table of systems (state, free nodes, queue wait) and one of recent runs (id, workflow, system, state, one-line cause for failures). End with what changed since the last round. Do not resubmit anything; recommend, with the evidence, and mark it NEEDS DECISION when a person must choose.`},
	{Key: "results-reviewer", Name: "Results reviewer", Persona: "reviewer", Every: "manual", OnRunEnd: false, WatchPath: sp("agents"),
		Goal: `Whenever new or changed files appear under agents/, read the new results and write or update a short summary beside them: what was run, the numbers that matter in a small table, figures embedded with the library's embed links, and every anomaly named with the file that shows it. Check convergence, ranges, missing cases, and runs that finished with no output. Update the same summary file on later rounds rather than writing new ones, and say what changed.`},
	{Key: "daily-digest", Name: "Daily digest", Persona: "reporter", Every: "1d", OnRunEnd: false,
		Goal: `Once a day, write the digest of what the agents did: read the folders under agents/ (delegated work and the standing agents' folders under agents/fleet/), the workflow runs, and their results and status files. Lead with outcomes, then what is still running and when it should finish, then failures and what was done about them, with numbers in one short table and a link for every claim. Write it to reports/digest-YYYY-MM-DD.md in the library and end your status with the first paragraph.`},
}

// Default is the fleet Register made.
var Default *Fleet

// Register serves /api/fleet over f, and runs its scheduler where this
// process owns the platform's jobs.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform, f *Fleet) {
	Default = f
	h := func(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, fn)
	}
	gate := func() error {
		if !a.Settings.Effective().DelegationEnabled {
			return apierr.New(403, "delegation is disabled on this deployment")
		}
		return nil
	}
	mux.HandleFunc("GET /api/fleet/templates", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"templates": Templates}, nil
	}))
	mux.HandleFunc("GET /api/fleet", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		f.Seen(auth.UserOf(r))
		f.Follow(r.Context())
		return map[string]any{"agents": f.For(r)}, nil
	}))
	mux.HandleFunc("POST /api/fleet", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var in struct {
			Name, Goal, Model, Every      string
			Persona, Resource, WatchPath  *string
			ConversationID                *string
			OnRunEnd                      bool
			MaxTicks, MaxTokens, MaxTurns *float64
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			return nil, apierr.New(400, "a JSON body is required")
		}
		ag, err := f.Create(Input{Name: in.Name, Goal: in.Goal, Persona: str(in.Persona), Model: in.Model, Resource: str(in.Resource), Every: in.Every,
			WatchPath: str(in.WatchPath), ConversationID: str(in.ConversationID), OnRunEnd: in.OnRunEnd,
			MaxTicks: in.MaxTicks, MaxTokens: in.MaxTokens, MaxTurns: in.MaxTurns}, auth.UserOf(r))
		if err != nil {
			return nil, apierr.New(400, "%s", err.Error())
		}
		return map[string]any{"agent": Shown(*ag)}, nil
	}))
	mux.HandleFunc("GET /api/fleet/{id}", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		// Another person's agent is answered as no agent at all.
		if f.GetFor(r, r.PathValue("id")) == nil {
			return nil, apierr.New(404, "no such agent")
		}
		f.Follow(r.Context(), r.PathValue("id"))
		ag := f.GetFor(r, r.PathValue("id"))
		if ag == nil {
			return nil, apierr.New(404, "no such agent")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		session := map[string]any{"id": ag.Session, "earlier": ag.Sessions, "recent": f.Recent(ctx, ag, 40)}
		return map[string]any{"agent": ag, "journal": f.Journal(ag.ID, 60), "session": session}, nil
	}))
	act := func(name string, fn func(id string, text string) bool) {
		mux.HandleFunc("POST /api/fleet/{id}/"+name, h(func(w http.ResponseWriter, r *http.Request) (any, error) {
			if err := gate(); err != nil {
				return nil, err
			}
			if f.GetFor(r, r.PathValue("id")) == nil {
				return nil, apierr.New(404, "no such agent")
			}
			var b struct{ Text string }
			json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b)
			if !fn(r.PathValue("id"), b.Text) {
				return nil, apierr.New(409, "cannot %s this agent in its current state", name)
			}
			return map[string]any{"ok": true}, nil
		}))
	}
	act("tick", func(id, _ string) bool { return f.Tick(id, "the person who runs you asked for a wake-up now", true) })
	act("pause", func(id, _ string) bool { return f.Pause(id) })
	act("resume", func(id, _ string) bool { return f.Resume(id) })
	act("retire", func(id, _ string) bool { return f.Retire(id) })
	act("message", func(id, t string) bool {
		if len([]rune(t)) > 2000 {
			t = string([]rune(t)[:2000])
		}
		return t != "" && f.Message(id, t)
	})
	if p.OwnsJobs() {
		every := 30 * time.Second
		if n, err := strconv.ParseFloat(p.Getenv("STUDIO_FLEET_TICK_MS"), 64); err == nil && n > 0 {
			every = time.Duration(n * float64(time.Millisecond))
		}
		a.OnStart("fleet scheduler", func(ctx context.Context) { f.Run(ctx, every) })
	}
}

// Local is this host's daemon from the pw code service d.
func Local(d *pwcode.Service) func() pwcode.Client {
	return func() pwcode.Client {
		if d == nil {
			return nil
		}
		return d.Local()
	}
}
