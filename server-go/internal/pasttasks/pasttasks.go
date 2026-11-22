// Package pasttasks keeps the tasks of the retired task engine readable.
// Before delegation moved to pw code sessions, the Studio split a request
// into subtasks and ran an agent for each; each task's last snapshot is
// INDEX_BASE/tasks/<id>/state.json, its board board.jsonl, and each
// agent's output work/<agent>/live.log, while its results are files in
// the library under tasks/<id>/. Nothing here runs or changes a task: the
// routes read those files, and a task or agent that was still working
// when the engine was retired is shown as stopped.
package pasttasks

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Stopped is the note on an agent that was still working.
const Stopped = "stopped when the task engine was retired"

// Archive is the folder of past tasks.
type Archive struct{ Dir string }

// Default is the archive Register serves.
var Default *Archive

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func alive(state string) bool { return state == "working" || state == "input-required" }

// Task is one task's last snapshot, with whatever was running shown as
// stopped; nil when there is none.
func (a *Archive) Task(id string) *jsonv.Object {
	if a == nil || !nameRe.MatchString(id) {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(a.Dir, id, "state.json"))
	if err != nil {
		return nil
	}
	v, err := jsonv.Decode(b)
	snap := jsonv.Obj(v)
	if err != nil || snap == nil || jsonv.String(prop(snap, "id")) != id {
		return nil
	}
	if alive(jsonv.String(prop(snap, "state"))) {
		snap.Set("state", "canceled")
	}
	for _, x := range jsonv.Arr(prop(snap, "agents")) {
		ag := jsonv.Obj(x)
		if ag == nil {
			continue
		}
		if alive(jsonv.String(prop(ag, "state"))) {
			ag.Set("state", "canceled")
			ag.Set("note", Stopped)
		}
		ag.Set("approvals", nil)
	}
	return snap
}

func prop(o *jsonv.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// List is the past tasks, newest first, as the Tasks page lists them.
func (a *Archive) List() []any {
	out := []any{}
	if a == nil {
		return out
	}
	ents, err := os.ReadDir(a.Dir)
	if err != nil {
		return out
	}
	type row struct {
		at string
		o  *jsonv.Object
	}
	var rows []row
	for _, d := range ents {
		t := a.Task(d.Name())
		if t == nil {
			continue
		}
		agents := jsonv.Arr(prop(t, "agents"))
		// How the agents ended: done is completed, so a task whose agent
		// failed does not read as done.
		done, failed, stopped := 0, 0, 0
		for _, x := range agents {
			switch jsonv.String(prop(jsonv.Obj(x), "state")) {
			case "completed":
				done++
			case "failed":
				failed++
			case "canceled":
				stopped++
			}
		}
		rows = append(rows, row{jsonv.String(prop(t, "createdAt")), jsonv.NewObject(
			"id", prop(t, "id"), "objective", prop(t, "objective"), "state", prop(t, "state"), "createdAt", prop(t, "createdAt"),
			"usage", prop(t, "usage"), "execution", prop(t, "execution"), "resource", prop(t, "resource"), "runtime", prop(t, "runtime"),
			"agents", len(agents), "running", 0, "waiting", 0, "done", done, "failed", failed, "stopped", stopped)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at > rows[j].at })
	for _, r := range rows {
		out = append(out, r.o)
	}
	return out
}

// Board is a task's board messages after seq since, the last 500.
func (a *Archive) Board(id string, since float64) []any {
	out := []any{}
	f, err := os.Open(filepath.Join(a.Dir, id, "board.jsonl"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		v, err := jsonv.Decode(sc.Bytes())
		o := jsonv.Obj(v)
		if err != nil || o == nil {
			continue
		}
		if seq, _ := prop(o, "seq").(float64); seq > since {
			out = append(out, o)
		}
	}
	if len(out) > 500 {
		out = out[len(out)-500:]
	}
	return out
}

// Output is the last 16 KB an agent of the task wrote; ok is false when
// there is no such task or agent.
func (a *Archive) Output(id, agent string) (string, bool) {
	t := a.Task(id)
	if t == nil || !nameRe.MatchString(agent) {
		return "", false
	}
	found := false
	for _, x := range jsonv.Arr(prop(t, "agents")) {
		if jsonv.String(prop(jsonv.Obj(x), "name")) == agent {
			found = true
		}
	}
	if !found {
		return "", false
	}
	f, err := os.Open(filepath.Join(a.Dir, id, "work", agent, "live.log"))
	if err != nil {
		return "", true
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", true
	}
	want := min(st.Size(), 16*1024)
	buf := make([]byte, want)
	n, _ := f.ReadAt(buf, st.Size()-want)
	return strings.ToValidUTF8(string(buf[:n]), "�"), true
}

// stoppedMark is the file that says the sessions left running were stopped.
const stoppedMark = ".left-running-stopped"

// LeftRunning is the pw code sessions of the agents that were still
// working when the engine was retired, which the page shows as stopped.
// The engine ran each on this host's daemon.
func (a *Archive) LeftRunning() []string {
	var out []string
	if a == nil {
		return out
	}
	ents, err := os.ReadDir(a.Dir)
	if err != nil {
		return out
	}
	for _, d := range ents {
		if !nameRe.MatchString(d.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(a.Dir, d.Name(), "state.json"))
		if err != nil {
			continue
		}
		v, err := jsonv.Decode(b)
		if err != nil {
			continue
		}
		for _, x := range jsonv.Arr(prop(jsonv.Obj(v), "agents")) {
			ag := jsonv.Obj(x)
			if id := jsonv.String(prop(ag, "sessionId")); id != "" && alive(jsonv.String(prop(ag, "state"))) {
				out = append(out, id)
			}
		}
	}
	return out
}

// StopLeftRunning interrupts, once, the sessions LeftRunning names on
// this host's daemon, so the agents the page shows as stopped are. It is
// tried again at the next start while the daemon cannot be reached.
func (a *Archive) StopLeftRunning(ctx context.Context, c pwcode.Client) (stopped int) {
	if a == nil || c == nil {
		return 0
	}
	if _, err := os.Stat(filepath.Join(a.Dir, stoppedMark)); err == nil {
		return 0
	}
	ids := a.LeftRunning()
	if len(ids) == 0 {
		return 0
	}
	for _, id := range ids {
		_, err := pwcode.InterruptSession(ctx, c, id)
		var de *pwcode.DaemonError
		switch {
		case err == nil:
			stopped++
		case errors.As(err, &de) && (de.Status == http.StatusNotFound || de.Status == http.StatusConflict):
			// Not there any more, or not running: nothing to stop. Any
			// other answer (a refusal, an error) is tried again next start.
		default:
			return stopped
		}
	}
	os.WriteFile(filepath.Join(a.Dir, stoppedMark), []byte("the sessions left running by the retired task engine were stopped\n"), 0o600)
	return stopped
}

// AdminsOnly is why a person who signs in, and does not administer the
// Studio, is shown no earlier tasks.
const AdminsOnly = "On a Studio with sign-in, earlier tasks are kept for its administrators: the task engine did not record who started each task."

// MayRead says whether the person r is from may read the earlier tasks.
// The retired engine never recorded who started a task, so on a Studio
// with sign-in nobody can be shown only their own: they are kept for its
// administrators. A Studio without sign-in has one person, who reads them.
func MayRead(a *app.App, r *http.Request) bool {
	if !a.Cfg.AuthEnabled() {
		return true
	}
	return mainlib.Admin(mainlib.User(true, auth.UserOf(r)))
}

// Register serves the past tasks read-only: GET /api/tasks,
// /api/tasks/{id} (with its board), and /api/tasks/{id}/agents/{name}/output.
func Register(mux *http.ServeMux, a *app.App) {
	ar := &Archive{Dir: filepath.Join(a.Cfg.IndexBase, "tasks")}
	Default = ar
	h := func(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, fn)
	}
	mux.HandleFunc("GET /api/tasks", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !MayRead(a, r) {
			return jsonv.NewObject("tasks", []any{}, "adminsOnly", AdminsOnly), nil
		}
		return jsonv.NewObject("tasks", ar.List()), nil
	}))
	mux.HandleFunc("GET /api/tasks/{id}", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !MayRead(a, r) {
			return nil, apierr.New(404, "no such task")
		}
		t := ar.Task(r.PathValue("id"))
		if t == nil {
			return nil, apierr.New(404, "no such task")
		}
		since := 0.0
		if q, ok := platform.Query(r, "since"); ok {
			if n, err := strconv.ParseFloat(strings.TrimSpace(q), 64); err == nil {
				since = n
			}
		}
		t.Set("board", ar.Board(r.PathValue("id"), since))
		return t, nil
	}))
	mux.HandleFunc("GET /api/tasks/{id}/agents/{name}/output", h(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if !MayRead(a, r) {
			return nil, apierr.New(404, "no such task or agent")
		}
		tail, ok := ar.Output(r.PathValue("id"), r.PathValue("name"))
		if !ok {
			return nil, apierr.New(404, "no such task or agent")
		}
		return jsonv.NewObject("tail", tail), nil
	}))
}
