package agentsites

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Register serves the nine /api/agent-sites routes.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform, d *pwcode.Service) *Sites {
	s := &Sites{P: p, Daemon: d}
	s.Routes(mux, a)
	return s
}

// viewerKey is keyOf: the viewer's own platform key (a stored credential
// with no base URL) when signed in, passed through agentKey; and whether
// sign-in is on.
func (s *Sites) viewerKey(r *http.Request) (string, bool, error) {
	v, err := s.P.ViewerOf(r)
	if err != nil {
		return "", false, err
	}
	own := ""
	if v.UserID() != "" {
		own = v.PlatformKey()
	}
	return s.AgentKey(own, v.AuthEnabled), v.AuthEnabled, nil
}

// Routes registers the routes on mux.
func (s *Sites) Routes(mux *http.ServeMux, a *app.App) {
	h := func(fn func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, func(w http.ResponseWriter, r *http.Request) (any, error) {
			key, auth, err := s.viewerKey(r)
			if err != nil {
				return nil, err
			}
			return fn(w, r, key, auth)
		})
	}
	mux.HandleFunc("GET /api/agent-sites", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		sites, err := s.ListSites(r.Context(), key, auth)
		if err != nil {
			return nil, err
		}
		return jsonv.NewObject("sites", sites, "localOffered", s.LocalSiteAllowed(auth)), nil
	}))
	mux.HandleFunc("GET /api/agent-sites/{site}/status", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		return s.ProbeSite(r.Context(), r.PathValue("site"), key, auth)
	}))
	mux.HandleFunc("GET /api/agent-sites/{site}/sessions", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		rows, err := s.Sessions(r.Context(), r.PathValue("site"), key, auth)
		if err != nil {
			return nil, err
		}
		return jsonv.NewObject("sessions", rows), nil
	}))
	mux.HandleFunc("POST /api/agent-sites/{site}/sessions", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		x, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		return s.Start(r.Context(), StartAgent{
			Site: r.PathValue("site"), Key: key, Objective: field(x, "objective"),
			Goal: truthyString(x, "goal"), MaxTurns: orZero(JSNumber(get(x, "maxTurns"))),
			Workspace: truthyString(x, "workspace"), Model: truthyString(x, "model"),
			PermissionMode: truthyString(x, "permissionMode"),
		}, auth)
	}))
	mux.HandleFunc("GET /api/agent-sites/{site}/sessions/{id}", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		lines := 30.0
		if q, ok := platform.Query(r, "lines"); ok {
			if n := JSNumber(q); n != 0 && !math.IsNaN(n) {
				lines = n
			}
		}
		return s.Status(r.Context(), r.PathValue("site"), key, r.PathValue("id"), lines, auth)
	}))
	mux.HandleFunc("POST /api/agent-sites/{site}/sessions/{id}/goal", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		x, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		return s.Goal(r.Context(), r.PathValue("site"), key, r.PathValue("id"), field(x, "action"), truthyString(x, "condition"), orZero(JSNumber(get(x, "maxTurns"))), auth)
	}))
	mux.HandleFunc("POST /api/agent-sites/{site}/sessions/{id}/steer", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		x, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		mode := "direction"
		if m, _ := x.Get("mode"); m == "message" {
			mode = "message"
		}
		return s.Steer(r.Context(), r.PathValue("site"), key, r.PathValue("id"), field(x, "text"), mode, auth)
	}))
	mux.HandleFunc("POST /api/agent-sites/{site}/sessions/{id}/approvals/{approval}", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		x, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		allowed, _ := x.Get("allowed")
		return s.Approve(r.Context(), r.PathValue("site"), key, r.PathValue("id"), r.PathValue("approval"), allowed == true, truthyString(x, "denyMessage"), auth)
	}))
	mux.HandleFunc("POST /api/agent-sites/{site}/sessions/{id}/interrupt", h(func(w http.ResponseWriter, r *http.Request, key string, auth bool) (any, error) {
		return s.Stop(r.Context(), r.PathValue("site"), key, r.PathValue("id"), auth)
	}))
}

func get(o *jsonv.Object, k string) any { v, _ := o.Get(k); return v }

// field is String(x[k] ?? ”).
func field(o *jsonv.Object, k string) string { return jsonv.String(get(o, k)) }

// truthyString is x[k] ? String(x[k]) : null, null as "".
func truthyString(o *jsonv.Object, k string) string {
	v := get(o, k)
	if !jsonv.Truthy(v) {
		return ""
	}
	return jsonv.String(v)
}

// orZero is `n || undefined` read as 0 for "none".
func orZero(n float64) float64 {
	if math.IsNaN(n) {
		return 0
	}
	return n
}

// JSNumber is Number(v) for a JSON value or a query string.
func JSNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		t := trimJS(x)
		if t == "" {
			return 0
		}
		if strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X") {
			if n, err := strconv.ParseUint(t[2:], 16, 64); err == nil {
				return float64(n)
			}
			return math.NaN()
		}
		switch t {
		case "Infinity", "+Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		if strings.ContainsAny(t, "_pPxX") || strings.EqualFold(t, "nan") || strings.HasPrefix(strings.ToLower(strings.TrimLeft(t, "+-")), "inf") {
			return math.NaN()
		}
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return n
	case []any:
		switch len(x) {
		case 0:
			return 0
		case 1:
			return JSNumber(jsonv.String(x[0]))
		}
	}
	return math.NaN()
}
