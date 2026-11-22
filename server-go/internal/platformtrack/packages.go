package platformtrack

import (
	"context"
	"errors"
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/fleet"
	"github.com/parallelworks/activate-studio/server-go/internal/pasttasks"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/ragendpoint"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
)

// registerPackages registers each package of the track, one line each,
// in its own slot.
func registerPackages(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	_, _, _ = mux, a, p

	registerAccessSessions(mux, a, p)

	workflows.Register(mux, a, p) // workflows, runs

	// One pw code daemon access for agent sites, delegation and the fleet.
	daemon := pwcode.New(p)
	daemon.Start(a)
	agentsites.Register(mux, a, p, daemon)
	// The retired task engine's tasks, read-only; the sessions it left
	// running are stopped once, where this process owns the jobs.
	pasttasks.Register(mux, a)
	if p.OwnsJobs() {
		a.OnStart("earlier tasks: stop the sessions left running", func(ctx context.Context) {
			if n := pasttasks.Default.StopLeftRunning(ctx, daemon.Local()); n > 0 {
				a.Log.Info("earlier tasks: stopped the sessions the retired task engine left running", "count", n)
			}
		})
	}
	// Standing agents are pw code sessions on this host, with the Studio's
	// tools through the chat module (registered later, so looked up when a
	// session starts).
	fl := fleet.New(a, fleet.Local(daemon))
	fl.Start = func(ctx context.Context, c pwcode.Client, owner *auth.User, grant, workspace, model, mode string, allow []string) (string, error) {
		if chat.Default == nil {
			return "", errors.New("the Studio's tools are not set up")
		}
		return chat.Default.StudioSession(ctx, c, owner, grant, workspace, model, mode, allow)
	}
	fleet.Register(mux, a, p, fl)
	// A standing agent's session is its owner's on the Code page.
	a.Providers.OwnSessions = append(a.Providers.OwnSessions, func(r *http.Request) []string {
		if u := auth.UserOf(r); u != nil && u.ID != "" {
			return fl.SessionsOf(u.ID)
		}
		return nil
	})
	// The fleet hears the run watcher's ended runs.
	runs.OnRunEnded(func(r runs.RunEnded) {
		res := ""
		if r.Resource != nil {
			res = *r.Resource
		}
		fl.NotifyRunEnded(fleet.RunEnded{Slug: r.Slug, Workflow: r.Workflow, Resource: res, State: r.State})
	})

	ragendpoint.Register(mux, a, p)
}
