package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// The Studio's tools in a pw code chat. A pw code chat session reaches
// this Studio's MCP server with a chat token, which lists the Studio's own
// tools (the index, the library, transfers, platform workflows), not only
// the read tools other MCP clients get. Each call runs as the person the
// token is for, with the conversation's scope and their own platform key,
// as the Studio assistant's call would. The calls the Studio holds for an
// answer in its own chat (deletes, cluster changes, transfers) are left
// out, as are the agent tools, since pw code is the agent.
//
// The Studio makes sure the session has the tools before a message goes to
// it: it reads the session's MCP servers from the daemon, reconnects the
// studio server once when it failed or lists other tools, and otherwise
// starts the session again at the next address this Studio may be reached
// at. When no address works, the Studio assistant answers that message.

// studioMCPName is the studio server's name in a session.
const studioMCPName = "studio"

// toolsDownFor is how long auto keeps to the Studio assistant after pw code
// could not reach the Studio's tools.
const toolsDownFor = 10 * time.Minute

// chatReadTools are the tools a pw code chat may call without asking:
// each reads (the index, the platform, transfer state) or returns a link.
// A tool not named here asks, as pw code's permission mode decides.
var chatReadTools = map[string]bool{
	"search_kb": true, "read_kb_file": true, "read_original": true, "list_kb_dir": true, "list_libraries": true,
	"query_corpus": true, "get_labels": true, "studio_docs": true, "show_in_viewer": true, "suggest_labels": true,
	"transfer_locations": true, "browse_location": true, "transfer_status": true,
	"list_workflows": true, "list_github_workflows": true, "get_workflow": true, "pw_help": true, "compose_workflow": true,
	"validate_workflow": true, "workflow_configs": true, "hpc_environments": true, "watch_run": true, "workflow_runs": true,
	"workflow_run_detail": true, "list_sessions": true, "list_clusters": true, "list_storage": true, "hpc_status": true,
}

// chatLeftOut: a Studio tool a pw code chat does not get. A held call
// waits for an answer in the Studio's own chat, and pw code is itself the
// agent the agent tools start.
func chatLeftOut(name string) bool {
	return asksFirst(name) || localAgentTools[name] || toolGroupOf(name) == "agents" ||
		name == "delegate" || name == "task_status" || name == MoreTools
}

// chatTools is the Studio tools a pw code chat of r's viewer gets, in
// catalog order: the built-in tools this deployment offers that viewer,
// less those turned off and those left out above.
func (m *Module) chatTools(r *http.Request) []ToolSpec {
	disabled := map[string]bool{}
	for _, n := range m.eff().DisabledTools {
		disabled[n] = true
	}
	platform := m.A.Providers.PlatformAvailable != nil && m.A.Providers.PlatformAvailable(r, userID(r))
	signedIn := m.A.Cfg.AuthEnabled()
	var out []ToolSpec
	for _, s := range builtinSpecs {
		n := s.Name
		if disabled[n] || chatLeftOut(n) || (signedIn && signedInOnlyOff[n]) || (!platform && needsPlatform(n)) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func specNames(specs []ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// chatToolJSON is a tool as tools/list gives it to a pw code chat.
func chatToolJSON(s ToolSpec) json.RawMessage {
	b, _ := jsonobj.Marshal(reqbody.O("name", s.Name, "description", s.Description, "inputSchema", s.Parameters,
		"annotations", reqbody.O("readOnlyHint", chatReadTools[s.Name], "destructiveHint", false, "openWorldHint", false)))
	return b
}

// mcpURLs is where a pw code daemon may reach this Studio's MCP server, in
// the order to try. STUDIO_MCP_URL, when set, is the only one. Otherwise:
// the loopback address when the server listens on every address or on
// loopback (a daemon on this machine, as on a deployment's own cluster),
// else the one address it listens on; and for a daemon on another machine,
// this machine's name.
func (m *Module) mcpURLs(remote bool) []string {
	if u := strings.TrimSpace(os.Getenv("STUDIO_MCP_URL")); u != "" {
		return []string{u}
	}
	port := strconv.Itoa(m.A.Cfg.Port)
	at := func(host string) string { return "http://" + net.JoinHostPort(host, port) + "/api/mcp" }
	host := strings.Trim(strings.TrimSpace(m.A.Cfg.Host), "[]")
	ip := net.ParseIP(host)
	every := host == "" || (ip != nil && ip.IsUnspecified())
	var urls []string
	switch {
	case every:
		urls = append(urls, at("127.0.0.1"))
	case host == "localhost" || (ip != nil && ip.IsLoopback()):
		return []string{at(host)}
	default:
		return []string{at(host)}
	}
	if remote {
		if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
			urls = append(urls, at(h))
		}
	}
	return urls
}

// mcpCheck is how a session's studio server stands.
type mcpCheck struct {
	OK bool
	// Unknown: the daemon cannot say (it has no MCP route), so the session
	// is used as it is.
	Unknown bool
	Tools   int
	Detail  string
}

// studioServer is the studio server among a session's MCP servers, or nil.
func studioServer(v any) *jsonv.Object {
	list, ok := v.([]any)
	if !ok {
		list = jsonv.Arr(jsonv.Path(v, "servers"))
	}
	for _, x := range list {
		if o := jsonv.Obj(x); o != nil && jsonv.String(jsonv.Path(o, "name")) == studioMCPName {
			return o
		}
	}
	return nil
}

// serverTools is the tool names a session's MCP server lists, and how
// many; a daemon that gives only a count gives no names.
func serverTools(s *jsonv.Object) ([]string, int) {
	v, _ := s.Get("tools")
	if n, ok := jsonv.Num(v); ok {
		return nil, int(n)
	}
	var names []string
	for _, x := range jsonv.Arr(v) {
		if o := jsonv.Obj(x); o != nil {
			if n := jsonv.String(jsonv.Path(o, "name")); n != "" {
				names = append(names, n)
			}
		} else if s, ok := x.(string); ok {
			names = append(names, s)
		}
	}
	return names, len(names)
}

// studioMCPState reads the studio server's state once.
func studioMCPState(ctx context.Context, c pwcode.Client, id string) (s *jsonv.Object, err error) {
	v, err := pwcode.SessionMCP(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return studioServer(v), nil
}

// mcpWaitStep is how often ensureStudioMCP looks while the server connects.
var mcpWaitStep = 250 * time.Millisecond

// ensureStudioMCP makes sure session id has the studio server connected
// with the tools in want: it waits while the server connects, and
// reconnects it once when it failed or lists other tools (a Studio updated
// since the session began).
func ensureStudioMCP(ctx context.Context, c pwcode.Client, id string, want []string) mcpCheck {
	reconnected := false
	until := time.Now().Add(15 * time.Second)
	for {
		s, err := studioMCPState(ctx, c, id)
		if err != nil {
			// A daemon without the route: nothing to check.
			var de *pwcode.DaemonError
			if errors.As(err, &de) && de.Status == http.StatusNotFound {
				return mcpCheck{OK: true, Unknown: true}
			}
			return mcpCheck{Detail: "pw code did not say how its MCP servers stand: " + err.Error()}
		}
		if s == nil {
			return mcpCheck{Detail: "the session has no studio MCP server"}
		}
		truthy := func(k string) bool { v, _ := s.Get(k); return jsonv.Truthy(v) }
		failure := jsonv.String(jsonv.Path(s, "err"))
		names, count := serverTools(s)
		if truthy("connected") && failure == "" {
			missing := 0
			for _, w := range want {
				if names != nil && !slices.Contains(names, w) {
					missing++
				}
			}
			if names == nil && count < len(want) {
				missing = len(want) - count
			}
			if missing == 0 {
				return mcpCheck{OK: true, Tools: count}
			}
			failure = fmt.Sprintf("it lists %d of the Studio's %d tools", len(want)-missing, len(want))
		} else if failure == "" && (truthy("connecting") || !truthy("disabled")) && time.Now().Before(until) {
			select {
			case <-ctx.Done():
				return mcpCheck{Detail: ctx.Err().Error()}
			case <-time.After(mcpWaitStep):
			}
			continue
		}
		if failure == "" {
			failure = "it did not connect"
			if truthy("disabled") {
				failure = "it is switched off in the session"
				_, _ = pwcode.SetMCPEnabled(ctx, c, id, studioMCPName, true)
			}
		}
		if reconnected {
			return mcpCheck{Detail: failure}
		}
		if _, err := pwcode.ReconnectMCP(ctx, c, id, studioMCPName); err != nil {
			return mcpCheck{Detail: failure}
		}
		reconnected = true
		until = time.Now().Add(15 * time.Second)
	}
}

// pwSite is the site a pw code chat of this Studio runs on.
func (m *Module) pwSite() string {
	if m.A.Cfg.AuthEnabled() {
		return userlibs.HomeCluster()
	}
	return agentsites.Local
}

// toolsDown says why pw code could not reach the Studio's tools at site
// in the last few minutes, or "".
func (m *Module) toolsDown(site string) string {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if a, ok := m.pw.down[site]; ok && time.Since(a.at) < toolsDownFor {
		return a.why
	}
	return ""
}

func (m *Module) setToolsDown(site, why string) {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if why == "" {
		delete(m.pw.down, site)
		return
	}
	if m.pw.down == nil {
		m.pw.down = map[string]pwAvail{}
	}
	m.pw.down[site] = pwAvail{why: why, at: time.Now()}
}

// studioToolsError: pw code could not reach this Studio's tools, so the
// Studio assistant answers in its place.
type studioToolsError struct{ detail string }

func (e *studioToolsError) Error() string {
	return "pw code could not reach this Studio's tools at " + e.detail
}

// shortMCPError is the cause at the end of a daemon's MCP error, such as
// "connection refused" from its chain of "connect: calling ...: dial tcp
// ...: connect: connection refused"; the server log keeps the whole.
func shortMCPError(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		return s[i+2:]
	}
	return s
}

// pwScope is what a pw code chat's tool calls take from the conversation:
// its libraries and labels as of the latest message, and the kinds of
// tools the person left on (nil for all).
func (m *Module) pwScope(r *http.Request, body *streamBody, link *conversations.PWCodeLink) (note string) {
	labels := labelScopeOf(body.LabelScope)
	library := m.chatLibraries(body.Library, libraries.ViewerOf(userID(r)))
	link.Library, link.LabelScope = library, labels
	link.Tools = nil
	if set := readerSet(rawOf(body.Tools)); set != nil {
		on := []string{}
		for id := range set {
			on = append(on, id)
		}
		slices.Sort(on)
		link.Tools = &on
	}
	if len(labels) > 0 {
		note = "The person has scoped this conversation to material labeled: " + strings.Join(labels, ", ") + ". search_kb is restricted to those labels."
	}
	return strings.TrimSpace(note + libraryScopeNote(library))
}

// chatToolCtx is the context a pw code chat's tool call runs in.
func (m *Module) chatToolCtx(r *http.Request, conv string) (ToolCtx, *conversations.PWCodeLink) {
	tc := ToolCtx{ConversationID: strOrNil(conv), UserID: strOrNil(userID(r))}
	var link *conversations.PWCodeLink
	if c := m.Conv.Get(conv); c != nil && c.PWCode != nil {
		link = c.PWCode
		tc.Library, tc.LabelScope = link.Library, link.LabelScope
	}
	if cred := m.cred(r); cred != nil && cred.Base() == "" {
		tc.UserKey = sp(cred.Key)
	}
	return tc, link
}

// chatToolOff says why the person's chat does not let pw code call name
// (a kind of tool they switched off), or "".
func (m *Module) chatToolOff(name string, link *conversations.PWCodeLink) string {
	if link == nil || link.Tools == nil {
		return ""
	}
	if opt := m.toolOptionOf(name); !slices.Contains(*link.Tools, opt) {
		return "The person switched off these tools (" + opt + ") for this chat under +, then Tools, so " + name + " did not run. Say so, and that they can switch them on."
	}
	return ""
}

// rawOf is v as JSON.
func rawOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
