package delegation

import (
	"context"
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Register serves GET /api/delegated, the person's delegated sessions
// (the Code page marks them), and at start watches again the local
// sessions still working.
func Register(mux *http.ServeMux, a *app.App) {
	s := New(a)
	Default = s
	a.OnIndexWork("delegation: watch sessions still working", func(context.Context) { s.Resume() })
	mux.HandleFunc("GET /api/delegated", platform.Handler(a.Log, func(w http.ResponseWriter, r *http.Request) (any, error) {
		rs := s.Mine(r)
		for _, rec := range rs {
			// A local session still working that nothing watches (its
			// watcher gave up, or this process did not start it) is
			// watched again.
			if (rec.State == "working" || rec.State == "waiting") && rec.Site == agentsites.Local {
				s.watch(rec.Session, "")
			}
		}
		return map[string]any{"sessions": rs}, nil
	}))
}
