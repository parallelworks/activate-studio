package personal

import (
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwhome"
)

// Module is the personal sign-ins of one server.
type Module struct {
	A       *app.App
	SignIns *SignIns
}

var (
	instMu sync.Mutex
	inst   = map[*app.App]*Module{}
)

// Of is a's module, made on first use.
func Of(a *app.App) *Module {
	instMu.Lock()
	defer instMu.Unlock()
	if m := inst[a]; m != nil {
		return m
	}
	m := &Module{A: a, SignIns: New(platform.Of(a), a.Cfg.IndexBase, a.Log)}
	inst[a] = m
	return m
}

// Register serves the /api/me/platform-sign-in routes and gives the rest
// of the server each person's sign-in as their platform credential.
func Register(mux *http.ServeMux, a *app.App) {
	m := Of(a)
	key := func(owner string) string {
		if !m.enabled() || owner == "" || owner == "single" {
			return ""
		}
		return m.SignIns.Key(owner)
	}
	a.Providers.SignInKey = key
	a.Providers.SignInOwner = m.SignIns.OwnerOfToken
	platform.Of(a).SignInKey = key
	pwhome.Set(func(k string) string {
		owner := m.SignIns.OwnerOfToken(k)
		if owner == "" {
			return ""
		}
		h := m.SignIns.Home(owner)
		if os.MkdirAll(h, 0o700) != nil {
			return ""
		}
		return h
	})
	m.Routes(mux)
}

// enabled: a Studio several people sign in to, which keeps personal
// credentials.
func (m *Module) enabled() bool {
	return m.A.Cfg.AuthEnabled() && os.Getenv("DISABLE_PERSONAL_KEYS") != "1"
}

// person is the verified person behind r, or why there is none.
func (m *Module) person(r *http.Request) (*auth.User, error) {
	if !m.A.Cfg.AuthEnabled() {
		return nil, apierr.New(409, "This Studio has one user, who signs the pw CLI in under Settings, Model access.")
	}
	if os.Getenv("DISABLE_PERSONAL_KEYS") == "1" {
		return nil, apierr.New(403, "This Studio keeps no personal credentials (DISABLE_PERSONAL_KEYS).")
	}
	u := auth.UserOf(r)
	if u == nil || u.ID == "" || u.Username == "" {
		return nil, apierr.New(401, "Open the Studio through the platform, or sign in, so it knows who you are.")
	}
	return u, nil
}

// Routes registers the four routes.
func (m *Module) Routes(mux *http.ServeMux) {
	h := func(fn func(r *http.Request, u *auth.User) (any, error)) http.HandlerFunc {
		return platform.Handler(m.A.Log, func(w http.ResponseWriter, r *http.Request) (any, error) {
			u, err := m.person(r)
			if err != nil {
				return nil, err
			}
			return fn(r, u)
		})
	}
	const base = "/api/me/platform-sign-in"
	mux.HandleFunc("GET "+base, h(func(r *http.Request, u *auth.User) (any, error) {
		return m.SignIns.Status(u.ID), nil
	}))
	mux.HandleFunc("POST "+base, h(func(r *http.Request, u *auth.User) (any, error) {
		st, err := m.SignIns.Start(u.ID, u.Username, 20*time.Second)
		if err != nil {
			return nil, apierr.New(409, "%s", err.Error())
		}
		return st, nil
	}))
	mux.HandleFunc("POST "+base+"/cancel", h(func(r *http.Request, u *auth.User) (any, error) {
		return m.SignIns.Cancel(u.ID), nil
	}))
	mux.HandleFunc("POST "+base+"/sign-out", h(func(r *http.Request, u *auth.User) (any, error) {
		return m.SignIns.SignOut(u.ID), nil
	}))
}
