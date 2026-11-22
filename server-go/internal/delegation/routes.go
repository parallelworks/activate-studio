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
	// The sessions the viewer delegated to are theirs on the Code page.
	a.Providers.OwnSessions = append(a.Providers.OwnSessions, func(r *http.Request) []string {
		out := []string{}
		for _, rec := range s.Mine(r) {
			out = append(out, rec.Session)
		}
		return out
	})
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
