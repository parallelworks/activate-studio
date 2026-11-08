package platformtrack

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// registerPackages registers each package of the track, one line each.
func registerPackages(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	_, _, _ = mux, a, p
}
