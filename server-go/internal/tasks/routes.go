package tasks

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

func sendError(w http.ResponseWriter, status int, msg string) (any, error) {
	platform.JSON(w, status, jsonv.NewObject("error", msg))
	return nil, nil
}

// Routes registers the eight /api/tasks routes and the board Node calls.
func (e *Engine) Routes(mux *http.ServeMux, a *app.App) {
	h := func(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, fn)
	}
	mux.HandleFunc("GET /api/tasks", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		return jsonv.NewObject("tasks", e.List()), nil
	}))
	mux.HandleFunc("GET /api/tasks/{id}", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		m := e.tasks[r.PathValue("id")]
		if m == nil {
			return sendError(w, 404, "no such task")
		}
		since := 0.0
		if q, ok := platform.Query(r, "since"); ok {
			since = jsNumber(q)
		}
		out := e.summary(m)
		board := []any{}
		for _, b := range m.board {
			if b.Seq > since { // false for NaN, as in JavaScript
				board = append(board, b.value())
			}
		}
		out.Set("board", board)
		return out, nil
	}))
	mux.HandleFunc("POST /api/tasks", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		g := func(k string) any { v, _ := b.Get(k); return v }
		agents, isList := g("agents").([]any)
		if !jsonv.Truthy(g("objective")) || !jsonv.Truthy(g("model")) || !isList || len(agents) == 0 {
			return sendError(w, 400, "objective, model, and at least one agent are required")
		}
		objective, ok := g("objective").(string)
		if !ok {
			return sendError(w, 400, "s.toLowerCase is not a function")
		}
		specs := make([]AgentSpec, 0, len(agents))
		for _, x := range agents {
			if x == nil {
				return sendError(w, 400, "Cannot read properties of null (reading 'persona')")
			}
			o := jsonv.Obj(x)
			specs = append(specs, AgentSpec{Persona: jsonv.String(jsonv.Path(o, "persona")), Objective: jsonv.String(jsonv.Path(o, "objective"))})
		}
		m, err := e.Create(CreateOptions{Objective: objective, Model: jsonv.String(g("model")), Agents: specs,
			MaxAgents: g("maxAgents"), MaxDepth: g("maxDepth"), Runtime: g("runtime")})
		if err != nil {
			return sendError(w, 400, err.Error())
		}
		return e.Summary(m.ID), nil
	}))
	mux.HandleFunc("GET /api/tasks/{id}/agents/{name}/output", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		tail, ok := e.AgentOutput(r.PathValue("id"), r.PathValue("name"))
		if !ok {
			return sendError(w, 404, "no such task or agent")
		}
		return jsonv.NewObject("tail", tail), nil
	}))
	mux.HandleFunc("GET /api/tasks/runtime", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		s := e.cfg.Daemon.Status(r.Context(), nil)
		reason := pwcode.UnusableReason(s)
		var local *jsonv.Object
		if s != nil {
			g := func(k string) any {
				v, ok := s.Get(k)
				if !ok {
					return jsonv.Undefined
				}
				return v
			}
			var why any
			if reason != "" {
				why = reason
			}
			local = jsonv.NewObject("available", reason == "", "reason", why, "version", g("version"), "protocol", g("protocolVersion"),
				"remoteStart", g("remoteStart"), "remoteMaxPermissionMode", g("remoteMaxPermissionMode"))
		} else {
			local = jsonv.NewObject("available", true, "reason", "no daemon is running yet; the first session agent starts one",
				"version", nil, "protocol", nil, "remoteStart", nil, "remoteMaxPermissionMode", nil)
		}
		return jsonv.NewObject("mode", e.cfg.AgentExecution(), "local", local), nil
	}))
	mux.HandleFunc("POST /api/tasks/{id}/agents/{name}/approvals/{approvalId}", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		allowed, _ := b.Get("allowed")
		deny, _ := b.Get("denyMessage")
		denyText := ""
		if jsonv.Truthy(deny) {
			denyText = jsonv.String(deny)
		}
		if err := e.AnswerApproval(r.Context(), r.PathValue("id"), r.PathValue("name"), r.PathValue("approvalId"), allowed == true, denyText); err != nil {
			return sendError(w, 409, err.Error())
		}
		return jsonv.NewObject("ok", true), nil
	}))
	mux.HandleFunc("POST /api/tasks/{id}/agents/{name}/direction", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		b, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		text, _ := b.Get("text")
		ok, err := e.Steer(r.Context(), r.PathValue("id"), r.PathValue("name"), jsonv.String(text))
		if err != nil {
			return sendError(w, 409, err.Error())
		}
		return jsonv.NewObject("delivered", ok), nil
	}))
	mux.HandleFunc("POST /api/tasks/{id}/stop", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !e.Stop(r.PathValue("id")) {
			return sendError(w, 404, "no such task")
		}
		return jsonv.NewObject("ok", true), nil
	}))

	// The board for Node's /api/mcp (mcp.ts, goOwnsPlatform): tool ""
	// asks whether the token names a task; otherwise the tool runs.
	mux.HandleFunc("POST /internal/platform/board", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := platform.RequireSecret(r); err != nil {
			return nil, err
		}
		b, err := platform.BodyObject(r)
		if err != nil {
			return nil, err
		}
		token := jsonv.String(jsonv.Path(b, "token"))
		agent := jsonv.String(jsonv.Path(b, "agent"))
		tool := jsonv.String(jsonv.Path(b, "tool"))
		found, out := e.BoardTool(token, agent, tool, jsonv.Obj(jsonv.Path(b, "args")))
		if tool == "" || !found {
			return jsonv.NewObject("found", found), nil
		}
		return jsonv.NewObject("found", true, "out", out), nil
	}))
}
