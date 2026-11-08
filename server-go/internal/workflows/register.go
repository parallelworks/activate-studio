package workflows

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
)

var current atomic.Pointer[Routes]

// Current is the registered routes' state (the run registry, the schema,
// workflow definitions), for other packages; nil before Register.
func Current() *Routes { return current.Load() }

// Register serves the workflow routes, the run registry's Node channel
// (POST /internal/platform/runs) and, where this process owns the track's
// jobs, reattaches the watcher to every unfinished run at start.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) *Routes {
	r := NewRoutes(a, p)
	r.Runs.Go = a.Go
	r.register(mux)
	mux.HandleFunc("GET /api/kb/dag", platform.Handler(p.Log(), r.kbDag))
	mux.HandleFunc("POST /internal/platform/runs", platform.Handler(p.Log(), r.recordFromNode))
	if p.OwnsJobs() {
		a.OnStart("run watcher", func(context.Context) {
			if n := r.Runs.WatchRegistered(); n > 0 {
				p.Log().Info("watching " + strconv.Itoa(n) + " unfinished workflow runs")
			}
		})
	}
	current.Store(r)
	return r
}

// recordFromNode is Node's runs.recordRun with STUDIO_GO_PLATFORM: the
// RunRecord it made, registered and watched here.
func (r *Routes) recordFromNode(w http.ResponseWriter, req *http.Request) (any, error) {
	if err := platform.RequireSecret(req); err != nil {
		return nil, err
	}
	var in runs.Record
	if err := platform.Decode(req, &in); err != nil {
		return nil, err
	}
	if in.Slug == "" || in.Workflow == "" {
		return nil, platform.KbError(400, "slug and workflow are required")
	}
	rec := r.Runs.RecordRun(runs.RecordInput{Slug: in.Slug, Workflow: in.Workflow, Resource: in.Resource,
		ConversationID: in.ConversationID, Owner: in.Owner, LaunchedAt: in.LaunchedAt})
	return jsonv.NewObject("ok", true, "run", rec.Object()), nil
}
