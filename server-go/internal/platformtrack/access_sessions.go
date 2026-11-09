package platformtrack

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/access"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// registerAccessSessions registers Model access (accessRoutes.ts), the
// platform sessions (sessions.ts), the run pane and the session relay
// (runPane.ts).
func registerAccessSessions(mux *http.ServeMux, a *app.App, p *platform.Platform) {
	access.Register(mux, a, p)
	sessions.Register(mux, a, p)
}
