package transfers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// Register serves the /api/transfers routes, clears what a stop left once
// the server is up, and holds automatic updates while a transfer runs.
func Register(mux *http.ServeMux, a *app.App) {
	s := Of(a)
	s.Routes(mux, a.Log, func(r *http.Request) (Viewer, error) { return ViewerOf(a, r) })
	a.AddBusy(s.Active)
	a.OnStart("transfers: interrupted jobs", func(ctx context.Context) { s.Restore(ctx) })
	a.OnStop("transfers", s.Stop)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		apierr.Write(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

func decode(r *http.Request, out any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return apierr.New(413, "the request is too large")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return apierr.New(400, "the request has no body")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return apierr.New(400, "the request is not valid JSON of the expected shape: %s", err.Error())
	}
	return nil
}

// Routes registers the routes; viewer is who a request is.
func (s *Service) Routes(mux *http.ServeMux, log *slog.Logger, viewer func(*http.Request) (Viewer, error)) {
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v, err := viewer(r)
			if err == nil {
				var out any
				out, err = fn(w, r, v)
				if err == nil {
					writeJSON(w, http.StatusOK, out)
					return
				}
			}
			apierr.Write(w, log, err)
		})
	}
	h("GET /api/transfers/locations", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		ls := s.Locations(r.Context(), v)
		if ls == nil {
			ls = []Location{}
		}
		return map[string]any{"locations": ls}, nil
	})
	h("GET /api/transfers/browse", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		q := r.URL.Query()
		var p *string
		if vs, ok := q["path"]; ok {
			p = &vs[0]
		}
		return s.Browse(r.Context(), v, q.Get("location"), p)
	})
	h("POST /api/transfers/plan", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		var req Request
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		p, err := s.Plan(r.Context(), v, req)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plan": p}, nil
	})
	h("POST /api/transfers", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		var body struct {
			PlanID string `json:"planId"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		t, err := s.Start(r.Context(), v, body.PlanID, ByUser, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"transfer": t}, nil
	})
	h("GET /api/transfers", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		state := r.URL.Query().Get("state")
		switch state {
		case "":
			state = "all"
		case "active", "done", "all":
		default:
			return nil, apierr.New(400, "state: active, done or all")
		}
		q := ListQuery{State: state, Before: r.URL.Query().Get("before"), Q: r.URL.Query().Get("q"),
			Outcome: r.URL.Query().Get("outcome"), StartedBy: r.URL.Query().Get("startedBy")}
		if l := r.URL.Query().Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n < 1 {
				return nil, apierr.New(400, "limit: a number from 1 to 200")
			}
			q.Limit = n
		}
		switch q.Outcome {
		case "", StateSucceeded, StateFailed, StateCancelled, StateInterrupted:
		default:
			return nil, apierr.New(400, "outcome: succeeded, failed, cancelled or interrupted")
		}
		switch q.StartedBy {
		case "", ByUser, ByAssistant, ByLibrary:
		default:
			return nil, apierr.New(400, "startedBy: user, assistant or library")
		}
		if t := r.URL.Query().Get("since"); t != "" {
			at, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return nil, apierr.New(400, "since: an RFC 3339 time, such as 2026-11-15T10:00:00Z")
			}
			q.Since = at
		}
		return s.List(v, q)
	})
	h("GET /api/transfers/summary", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		return s.Summary(v), nil
	})
	h("POST /api/transfers/seen", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		var body struct {
			ID string `json:"id"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return s.Seen(v, body.ID)
	})
	h("GET /api/transfers/movers", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		ms, err := s.Movers(r.Context(), v, r.URL.Query().Get("source"), r.URL.Query().Get("destination"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"movers": ms}, nil
	})
	h("GET /api/transfers/settings", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		return s.SettingsView(r.Context(), v), nil
	})
	h("PUT /api/transfers/settings", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		if v.AuthOn {
			return s.SaveSettings(r.Context(), v, SettingsPut{})
		}
		var in SettingsPut
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.SaveSettings(r.Context(), v, in)
	})
	h("POST /api/transfers/settings/locations", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		var in SavedLocation
		if !v.AuthOn {
			if err := decode(r, &in); err != nil {
				return nil, err
			}
		}
		l, err := s.AddSavedLocation(r.Context(), v, in)
		if err != nil {
			return nil, err
		}
		return map[string]any{"location": l, "id": KindSaved + ":" + l.ID}, nil
	})
	h("POST /api/transfers/settings/test", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		var in TestRequest
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return s.TestSettings(r.Context(), v, in)
	})
	h("GET /api/transfers/{id}", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		t, err := s.Get(v, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"transfer": t}, nil
	})
	h("POST /api/transfers/{id}/cancel", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		if _, err := s.Get(v, r.PathValue("id")); err != nil {
			return nil, err
		}
		ok, why := s.Cancel(v, r.PathValue("id"))
		out := map[string]any{"cancelled": ok}
		if why != "" {
			out["why"] = why
		}
		return out, nil
	})
	h("DELETE /api/transfers/{id}", func(w http.ResponseWriter, r *http.Request, v Viewer) (any, error) {
		if err := s.Delete(v, r.PathValue("id")); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	})
}
