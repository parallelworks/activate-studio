// Package platformtrack registers the platform track's routes, background
// work and providers (docs/GO-PORT.md, "The platform track"): access,
// sessions and the session relay, workflows and the run watcher, agent
// sites, tasks and the fleet, and the RAG endpoint. Every package of the
// track shares one *platform.Platform, made here.
package platformtrack

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// init runs before main starts Node, which needs this track's environment:
// the Go server owns runs, tasks, the fleet and the RAG endpoint, so Node
// neither watches nor reattaches them (server/src/frontBridge.ts). Node's
// bridge and this track's Go-only routes answer only the bridge's secret.
func init() {
	bridge.RegisterNodeEnv("platform", func(string) map[string]string {
		return map[string]string{"STUDIO_GO_PLATFORM": "1"}
	})
	bridge.RegisterPrivatePrefix(platform.BridgePrefix)
	bridge.RegisterPrivatePrefix("/internal/platform/")
}

// Register is the track's one line in internal/server/tracks.go.
func Register(mux *http.ServeMux, a *app.App) {
	p := platform.FromApp(a)
	a.Providers.PlatformAvailable = func(r *http.Request, viewer string) bool {
		v, err := p.ViewerOf(r)
		if err != nil {
			return false
		}
		return p.PlatformAvailable(v)
	}
	registerPackages(mux, a, p)
}
