package chat

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The Studio's tools in a person's pw code sessions on other machines.
// A pw code session the Studio starts gets them through its own studio
// MCP server and chat token (pwcodetools.go); one on another machine,
// relayed through ACTIVATE or not, has no such server, and the platform
// does not relay one. So the person adds the Studio once on that machine,
// in pw code's user scope, as an HTTP MCP server at the Studio's session
// URL with an ACTIVATE API key of theirs (platform scope) in its header:
//
//	pw code mcp add --scope user --transport http --header 'Authorization: Bearer <key>' studio https://<platform>/me/session/<owner>/<name>/api/mcp
//
// The key goes in as it is: pw code fills a ${VAR} in a header from its
// daemon's environment, which a variable set later reaches only once the
// daemon starts again.
//
// The platform's session proxy accepts the key, drops it, and passes the
// Studio its signed token naming the person, which the Studio verifies as
// it does for every request (internal/auth). Such a request gets the tools
// a pw code chat of theirs gets, as them: the libraries they may use, and
// their own ACTIVATE credential for the platform, never the Studio's.
// GET /api/mcp/bridge is the chat's setup card: the command for this
// Studio, and when a session of theirs last reached the tools.

// bridgeKeyHole is where the person's key goes in the command.
const bridgeKeyHole = "<your ACTIVATE API key>"

// personGrant is whether r is a person's own pw code session reaching
// the Studio through the session proxy: sign-in on, the proxy's verified
// token, no bearer of the Studio's own, and not a browser page, which
// sends an Origin with every POST and is never an MCP client of this
// endpoint (a page on another site cannot use a person's platform
// sign-in to call the Studio's tools as them).
func (m *Module) personGrant(r *http.Request, token string) bool {
	u := user(r)
	return m.A.Cfg.AuthEnabled() && u != nil && u.Source == "proxy" && token == "" &&
		r.Header.Get("Origin") == "" && r.Header.Get("Sec-Fetch-Site") == ""
}

// personCtx is the person's tool context: their own platform key, the
// main library by default, no chat of the Studio's.
func (m *Module) personCtx(r *http.Request) ToolCtx {
	tc := ToolCtx{UserID: strOrNil(userID(r))}
	if cred := m.cred(r); cred != nil && cred.Base() == "" && cred.Key != "" {
		tc.UserKey = sp(cred.Key)
	}
	return tc
}

// personTools is the tools a person's own pw code session gets: those of a
// pw code chat of theirs, less save_chat_to_library, since there is no
// Studio chat to save, and less the platform's tools when they have no
// ACTIVATE credential of their own here, so the Studio's account is never
// used for them.
func (m *Module) personTools(r *http.Request, tc ToolCtx) []ToolSpec {
	own := deref(tc.UserKey) != ""
	var out []ToolSpec
	for _, s := range m.chatTools(r, true) {
		n := s.Name
		if n == "save_chat_to_library" {
			continue
		}
		if !own && (isPlatformTool(n) || (needsPlatform(n) && !chatDelegateTools[n]) || n == "show_in_viewer") {
			// show_in_viewer reads a workflow with the caller's key too
			// (workflow_dag), which empty is the Studio's own.
			continue
		}
		out = append(out, s)
	}
	return out
}

// bridgeVisit is when a person's session last reached the tools.
type bridgeVisit struct {
	At     time.Time
	Method string
	Client string
}

// bridgeSeen records a person's session reaching the tools.
func (m *Module) bridgeSeen(r *http.Request, method string) {
	id := userID(r)
	if id == "" {
		return
	}
	client := strings.Map(func(c rune) rune {
		if unicode.IsPrint(c) {
			return c
		}
		return -1
	}, r.UserAgent())
	if len(client) > 80 {
		client = client[:80]
	}
	m.bridgeMu.Lock()
	defer m.bridgeMu.Unlock()
	if m.bridgeVisits == nil {
		m.bridgeVisits = map[string]bridgeVisit{}
	}
	m.bridgeVisits[id] = bridgeVisit{At: time.Now().UTC(), Method: method, Client: client}
}

var (
	bridgeHost   = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	bridgePrefix = regexp.MustCompile(`^(/[A-Za-z0-9._~@%+-]+)*$`)
)

// bridgeURL is the Studio's MCP endpoint as the session proxy serves it
// to r's viewer, or "" when the request did not come through it in a
// form a command can carry as is. STUDIO_SELF_URL, the Studio's address
// on the platform, names it where the Studio cannot tell.
func bridgeURL(r *http.Request) string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("STUDIO_SELF_URL")), "/"); u != "" {
		if p, err := url.Parse(u); err == nil && (p.Scheme == "https" || p.Scheme == "http") && bridgeHost.MatchString(p.Host) && bridgePrefix.MatchString(p.Path) && p.RawQuery == "" {
			return p.Scheme + "://" + p.Host + p.Path + "/api/mcp"
		}
		return ""
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	if (proto != "https" && proto != "http") || !bridgeHost.MatchString(host) || !bridgePrefix.MatchString(prefix) {
		return ""
	}
	return proto + "://" + host + prefix + "/api/mcp"
}

// bridgeCommand is the command a person runs once on another machine.
func bridgeCommand(url string) string {
	return "pw code mcp add --scope user --transport http --header 'Authorization: Bearer " + bridgeKeyHole + "' " + studioMCPName + " " + url
}

// bridgeRoute is GET /api/mcp/bridge.
func (m *Module) bridgeRoute(w http.ResponseWriter, r *http.Request) error {
	out := reqbody.O("available", false, "keyHole", bridgeKeyHole, "name", studioMCPName)
	if o := m.pf.Origin(); o != "" {
		out.SetValue("keyPage", o+"/settings/keys")
	}
	u := user(r)
	switch {
	case !m.A.Cfg.AuthEnabled() || u == nil || u.Source != "proxy":
		out.SetValue("why", "This Studio is not open through the ACTIVATE session proxy, so a pw code session on another machine cannot reach its tools this way.")
		return reqbody.OK(w, out)
	case !m.eff().McpEnabled:
		out.SetValue("why", "MCP access is off for this Studio. An administrator turns it on under Settings, External access.")
		return reqbody.OK(w, out)
	}
	url := bridgeURL(r)
	if url == "" {
		out.SetValue("why", "The Studio could not tell its address on ACTIVATE from this request.")
		return reqbody.OK(w, out)
	}
	tc := m.personCtx(r)
	tools := m.personTools(r, tc)
	out.SetValue("available", true)
	out.SetValue("url", url)
	out.SetValue("command", bridgeCommand(url))
	out.SetValue("tools", len(tools))
	out.SetValue("ownKey", deref(tc.UserKey) != "")
	out.SetValue("platformTools", slices.ContainsFunc(tools, func(s ToolSpec) bool { return isPlatformTool(s.Name) }))
	m.bridgeMu.Lock()
	v, ok := m.bridgeVisits[u.ID]
	m.bridgeMu.Unlock()
	if ok {
		out.SetValue("seen", reqbody.O("at", v.At.Format(time.RFC3339), "method", v.Method, "client", v.Client))
	} else {
		out.SetValue("seen", nil)
	}
	return reqbody.OK(w, out)
}
