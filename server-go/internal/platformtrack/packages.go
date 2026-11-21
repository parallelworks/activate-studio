package platformtrack

import (
	"context"
	"errors"
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/fleet"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/ragendpoint"
	"github.com/parallelworks/activate-studio/server-go/internal/tasks"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
)

// registerPackages registers each package of the track, one line each,
// in its own slot.
func registerPackages(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	_, _, _ = mux, a, p

	registerAccessSessions(mux, a, p)

	workflows.Register(mux, a, p) // workflows, runs

	daemon := tasks.Register(mux, a, p) // agent sites, pwcode, tasks
	// Standing agents are pw code sessions on this host, with the Studio's
	// tools through the chat module (registered later, so looked up when a
	// session starts).
	fl := fleet.New(a, fleet.Local(daemon))
	fl.Start = func(ctx context.Context, c pwcode.Client, owner *auth.User, grant, workspace, model, mode string) (string, error) {
		if chat.Default == nil {
			return "", errors.New("the Studio's tools are not set up")
		}
		return chat.Default.StudioSession(ctx, c, owner, grant, workspace, model, mode)
	}
	fleet.Register(mux, a, p, fl)
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
