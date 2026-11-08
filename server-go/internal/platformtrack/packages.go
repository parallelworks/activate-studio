package platformtrack

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/ragendpoint"
	"github.com/parallelworks/activate-studio/server-go/internal/tasks"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
)

// registerPackages registers each package of the track, one line each,
// in its own slot.
func registerPackages(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	_, _, _ = mux, a, p

	registerAccessSessions(mux, a, p)

	workflows.Register(mux, a, p) // workflows, runs

	tasks.Register(mux, a, p) // agent sites, pwcode, tasks, fleet

	ragendpoint.Register(mux, a, p)
}
