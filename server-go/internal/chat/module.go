// Package chat is server/src/chat/routes.ts: the assistant's routes (the
// agent loop on POST /api/chat/stream, the model listing, the AI health
// line, the personal model key, next-step suggestions) over the
// conversations, model-key and gateway packages.
package chat

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// KeyHeader carries the viewer's personal key, sealed, on each request
// passed to Node while Node still reads keys (credentials.ts mirrorFromGo).
const KeyHeader = "X-Studio-Model-Key"

// Module is the track's state.
type Module struct {
	A     *app.App
	Keys  *modelkeys.Store
	LLM   *llm.Client
	Conv  *conversations.Store
	Tools Tools
	Convs *conversations.Routes

	accessMu sync.Mutex
	access   Access

	listings *listingCache
	noCap    *noTokenCap
}

// Default is the registered module.
var Default *Module

// New builds the module over a's configuration.
func New(a *app.App) *Module {
	m := &Module{A: a, Tools: nodeTools{a}}
	m.access = *envAccess()
	m.Keys = modelkeys.New(a.Cfg.IndexBase, a.Cfg.AuthEnabled)
	m.LLM = llm.New(m.gatewayBase, m.GatewayKey)
	m.Conv = conversations.New(a.Cfg.IndexBase, a.Cfg.KBRoot, conversations.Hooks{
		Label: func(ctx context.Context, paths, add []string) error {
			if a.Providers.ApplyLabels == nil {
				return nil
			}
			return a.Providers.ApplyLabels(ctx, nil, paths, add, nil)
		},
		Reindex: func(ctx context.Context, rel string) error {
			if a.Providers.IndexDirMS == nil {
				return nil
			}
			_, err := a.Providers.IndexDirMS(ctx, rel)
			return err
		},
		Log: func(msg string) { a.Log.Warn(msg) },
	})
	m.listings = newListingCache()
	m.noCap = loadNoTokenCap(a.Cfg.IndexBase)
	return m
}

func (m *Module) eff() settings.Effective { return m.A.Settings.Effective() }

// known is whether this server knows the viewer: with sign-in off there is
// one, and with the front verifying identity it is the front's.
func (m *Module) known() bool { return !m.A.Cfg.AuthEnabled() || m.A.Cfg.FrontAuth }

// user is req.user.
func user(r *http.Request) *auth.User { return auth.UserOf(r) }

func userID(r *http.Request) string {
	if u := user(r); u != nil {
		return u.ID
	}
	return ""
}

// viewerOf is approvals.ts's viewerOf.
func (m *Module) viewerOf(r *http.Request) *Viewer {
	if !m.A.Cfg.AuthEnabled() {
		name := os.Getenv("APP_USERNAME")
		if name == "" {
			name = "user"
		}
		return &Viewer{ID: "local", Name: name}
	}
	u := user(r)
	if u == nil || u.ID == "" {
		return nil
	}
	name := u.Username
	if name == "" {
		name = u.ID
	}
	return &Viewer{ID: u.ID, Name: name}
}

// cred is resolveUserCred(req.user?.id), after the key cookie's rehydration
// (main.ts's onRequest hook).
func (m *Module) cred(r *http.Request) *modelkeys.Cred {
	id := userID(r)
	if id != "" {
		m.Keys.Hydrate(id, r.Header.Get("Cookie"))
	}
	return m.Keys.Resolve(id)
}

/* ---- the gateway and the deployment credential ---- */

func (m *Module) setAccess(a *Access) {
	if a == nil {
		return
	}
	m.accessMu.Lock()
	m.access = *a
	m.accessMu.Unlock()
}

func (m *Module) gatewayBase() string {
	m.accessMu.Lock()
	defer m.accessMu.Unlock()
	return m.access.GatewayBase
}

func (m *Module) contextName() *string {
	m.accessMu.Lock()
	defer m.accessMu.Unlock()
	return m.access.ContextName
}

// GatewayKey is gateway.ts's gatewayKey: a shared personal key, else the
// deployment's (PW_API_KEY, else the pw CLI's credential).
func (m *Module) GatewayKey() string {
	if k := m.Keys.SharedKey(); k != "" {
		return k
	}
	m.accessMu.Lock()
	defer m.accessMu.Unlock()
	return m.access.DeploymentKey
}

// refreshAccess is ensureFreshAccess(ms) and a reread of the platform
// values Node holds.
func (m *Module) refreshAccess(r *http.Request, ms int) bool {
	a, err := m.Tools.Access(r, ms)
	m.setAccess(a)
	if err != nil || a == nil {
		return false
	}
	return a.Platform
}

func pwAPIKey() string {
	if k := os.Getenv("PW_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("OPENAI_API_KEY")
}

/* ---- registration ---- */

// Register adds the track's routes and providers.
func Register(mux *http.ServeMux, a *app.App) {
	m := New(a)
	Default = m
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(pattern, apierr.Handler(a.Log, fn))
	}
	m.Convs = &conversations.Routes{
		Store:         m.Conv,
		AuthEnabled:   a.Cfg.AuthEnabled,
		SharedHistory: func() bool { return m.eff().ChatSharedHistory },
		Held: func(r *http.Request, id string) []conversations.Holder {
			hs, _ := m.Tools.Held(r, id)
			return hs
		},
		Who: func(r *http.Request) (conversations.Who, bool) {
			if !m.known() {
				return conversations.Who{}, false
			}
			w := conversations.Who{}
			if u := user(r); u != nil {
				w.UserID, w.Username = u.ID, u.Username
			}
			if v := m.viewerOf(r); v != nil {
				w.ViewerID = v.ID
			}
			return w, true
		},
		ToNode: m.toNode,
	}
	m.Convs.Register(mux, h)
	m.keyRoutes(h)
	h("GET /api/chat/models", m.models)
	h("GET /api/ai/health", m.health)
	h("GET /api/chat/suggestions", m.suggestions)
	h("POST /api/chat/stream", m.stream)
	m.attachmentRoutes(h)

	a.Providers.GatewayConfigured = func(r *http.Request) bool { return m.GatewayKey() != "" }
	a.Providers.GatewayKey = m.GatewayKey
	// TODO(chat tools): the MCP listing is mcpClient.ts's, in Node, until the
	// tools are ported; the bridge's changed op drops it there.
	a.Providers.InvalidateMCP = func() { a.Providers.Changed(context.Background(), "mcp") }
	a.AddBusy(func() bool { return a.StreamsInFlight() > 0 })

	// Node reads the viewer's key from Go while routes that use it remain
	// there; only where the front verifies identity does Go hold sessions.
	if a.Cfg.AuthEnabled() && a.Cfg.FrontAuth {
		bridge.RegisterNodeEnv("STUDIO_GO_MODEL_KEYS", func(string) map[string]string {
			return map[string]string{"STUDIO_GO_MODEL_KEYS": "1"}
		})
	}
	app.OnToNode(func(in, out *http.Request) {
		out.Header.Del(KeyHeader)
		if !a.Cfg.AuthEnabled() || !a.Cfg.FrontAuth {
			return
		}
		id := userID(in)
		if id == "" {
			return
		}
		m.Keys.Hydrate(id, in.Header.Get("Cookie"))
		if blob := m.Keys.SealedSession(id); blob != "" {
			out.Header.Set(KeyHeader, blob)
		} else {
			out.Header.Set(KeyHeader, "-")
		}
	})

	a.OnStart("chat: personal key vault scrub", func(context.Context) {
		a.Log.Info("personal key vault scrub", "removed", m.Keys.ScrubPersonal())
	})
	// The platform values are Node's (access.ts); read them once Node is up
	// and then now and then, for the providers that have no request.
	a.OnStart("chat: platform values", func(ctx context.Context) {
		for {
			if a.NodeAPI != nil && !a.NodeFree() {
				r, _ := http.NewRequestWithContext(ctx, "GET", "/", nil)
				m.refreshAccess(r, 0)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	})
}

// toNode hands a request to Node, marked as meant.
func (m *Module) toNode(w http.ResponseWriter, r *http.Request, why string) {
	m.A.Log.Info("chat: to node", "path", r.URL.Path, "why", why)
	m.A.Node().ServeHTTP(w, app.HandOff(r, why))
}
