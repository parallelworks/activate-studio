// Package platformtrack registers the platform track's routes, background
// work and providers (docs/GO-PORT.md, "The platform track"): access,
// sessions and the session relay, workflows and the run watcher, agent
// sites, tasks and the fleet, and the RAG endpoint. Every package of the
// track shares one *platform.Platform, made here.
package platformtrack

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Register is the track's one line in internal/server/tracks.go.
func Register(mux *http.ServeMux, a *app.App) {
	p := platform.FromApp(a)
	// Node's bridge is the Go server's alone; a client never reaches it.
	mux.HandleFunc(platform.BridgePrefix, platform.RefuseBridge)
	a.Providers.PlatformAvailable = func(r *http.Request, viewer string) bool {
		v, err := p.ViewerOf(r)
		if err != nil {
			return false
		}
		return p.PlatformAvailable(v)
	}
	registerPackages(mux, a, p)
}
