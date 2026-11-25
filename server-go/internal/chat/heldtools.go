package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
)

// The actions that were arguments of tools that read: launching a Status
// Monitor (hpc_status) and starting pw code's daemon on a site
// (agent_sites). Each is a tool of its own whose calls wait for the
// user's approval in the Studio's chat, as the platform's changes do; a
// pw code session and the RAG proxy are not listed held tools.

const (
	launchMonitorTool = "launch_status_monitor"
	startDaemonTool   = "start_agent_daemon"
)

var actionAsksFirst = map[string]bool{launchMonitorTool: true, startDaemonTool: true}

func isActionAsksFirst(name string) bool { return actionAsksFirst[name] }

func init() {
	for name := range actionAsksFirst {
		n := name
		goExecutors[n] = func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error) {
			return &Outcome{Result: n + " runs only after the user approves the call in the Studio chat. Nothing ran.", Summary: "needs the user's approval"}, nil
		}
	}
}

// withoutStart runs a tool that reads so that reaching a site's pw code
// never starts its daemon.
func withoutStart(fn agentTool) agentTool {
	return func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
		return fn(m, r.WithContext(agentsites.WithoutStart(r.Context())), args, ctx)
	}
}

// prepareActionArgs is a held action's arguments in canonical form.
func prepareActionArgs(name string, raw any) (*jsonobj.Object, string, string) {
	text, _ := raw.(string)
	if b, ok := raw.(json.RawMessage); ok {
		text = string(b)
	}
	if jsTrim(text) == "" {
		text = "{}"
	}
	var given map[string]any
	if json.Unmarshal([]byte(text), &given) != nil || given == nil {
		return nil, "", "The arguments were not valid JSON."
	}
	args := jsonobj.New()
	if name == startDaemonTool {
		site, _ := given["site"].(string)
		if site = jsTrim(site); site == "" {
			return nil, "", "The site argument is required."
		}
		args.SetValue("site", site)
	} else if v, ok := given["inputs"]; ok && v != nil && v != "" {
		// A JSON object, or its text; shown to the user as it will be sent.
		s, isText := v.(string)
		if !isText {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		var o map[string]any
		if json.Unmarshal([]byte(s), &o) != nil || o == nil {
			return nil, "", "The inputs argument must be a JSON object."
		}
		args.SetValue("inputs", s)
	}
	b, _ := jsonobj.Marshal(args)
	return args, string(b), ""
}

// runAction runs an approved action as the person who approved it.
func (m *Module) runAction(r *http.Request, name string, args *jsonobj.Object, key string) (string, bool) {
	ctx := ToolCtx{UserID: strOrNil(userID(r))}
	if key != "" {
		ctx.UserKey = sp(key)
	}
	if name == launchMonitorTool {
		wf := m.eff().HpcStatusWorkflow
		cli := []string{"workflows", "run"}
		if s, ok := jsonobj.String(args.Get("inputs")); ok {
			cli = append(cli, "-i", s)
		}
		out, err := m.cli(r, ctx, append(cli, wf), 180*time.Second)
		if err != nil {
			return "The Status Monitor was not launched: " + jsval.Slice(err.Error(), 600), true
		}
		return "Status Monitor launch submitted via workflow " + wf + ".\n" + jsval.Slice(jsTrim(out), 1500) +
			"\nWatch it with workflow_runs; once it serves, set its URL as the Status Monitor URL in Settings so hpc_status can read it.", false
	}
	site, _ := jsonobj.String(args.Get("site"))
	s, err := m.sites()
	if err != nil {
		return err.Error(), true
	}
	st, err := s.ProbeSite(r.Context(), site, m.agentKey(s, ctx), m.A.Cfg.AuthEnabled())
	if err != nil {
		return site + ": " + jsval.Slice(err.Error(), 600), true
	}
	if !truthy(prop(st, "usable")) {
		return site + ": not usable, " + tmpl(prop(st, "why")), true
	}
	n := prop(st, "sessions")
	one := "s"
	if n == 1.0 {
		one = ""
	}
	return strings.TrimSpace(site + ": pw code " + tmpl(prop(st, "version")) + " runs on " + tmpl(prop(st, "hostname")) + ", " + tmpl(n) + " session" + one + "."), false
}
