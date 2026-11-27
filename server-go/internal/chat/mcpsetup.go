package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// "Set up for me": the Studio adds itself to pw code on the machine a
// relayed chat's session runs on, so the person need not run the command
// themselves. It reaches the machine as remote libraries do, with pw ssh
// and the person's own ACTIVATE credential (never the Studio's account),
// and writes one entry in their pw code user settings.
//
// The entry needs no key of its own. It is a stdio server, bridgeShim,
// that asks the pw CLI on that machine for its access token at run time
// (pw auth token --print, which renews a sign-in about to expire) and
// sends each message to the Studio's MCP endpoint through the session
// proxy with it. So nothing secret is written to the machine, a revoked
// sign-in stops it, and the token never reaches a command line there: it
// goes to curl in a file only the person can read. ACTIVATE makes API
// keys only from a browser session on the platform, so the Studio could
// not make one as the person in any case.
//
// pw code reads MCP servers when a session starts: sessions started on the
// machine from then on have the tools, and one already open (the relayed
// session among them) keeps the tools it started with.

// bridgeShimName is the shim's $0, by which the entry is known as the
// Studio's.
const bridgeShimName = "studio-mcp"

// bridgeShim is the entry's command: sh -c bridgeShim studio-mcp <url> <pw>.
// It reads one JSON-RPC message a line, posts it to <url> with the pw
// CLI's token (fetched again every four minutes, and once more on a 401),
// and writes the Studio's answer as one line. A notification has no
// answer; a request the Studio did not answer gets a JSON-RPC error, so
// pw code is not left waiting. ${...} is not used, since pw code would
// read ${NAME} as its own.
const bridgeShim = `u=$1; p=$2
umask 077
d=$(mktemp -d 2>/dev/null) || { d="$TMPDIR"; [ -n "$d" ] || d=/tmp; d="$d/studio-mcp.$$"; mkdir -p "$d" || exit 1; }
trap 'rm -rf "$d"' EXIT
trap 'exit 0' HUP INT TERM
t=; a=0
tok() { t=$("$p" auth token --print 2>/dev/null | tail -n 1); a=$(date +%s); printf 'Authorization: Bearer %s\n' "$t" >"$d/h"; }
post() { curl -sS --max-time 900 -o "$d/o" -w '%{http_code}' -H @"$d/h" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data-binary @"$d/b" "$u" 2>"$d/e"; }
while IFS= read -r l || [ -n "$l" ]; do
  [ -n "$l" ] || continue
  printf '%s' "$l" >"$d/b"
  n=$(date +%s)
  if [ -z "$t" ] || [ $((n - a)) -ge 240 ]; then tok; fi
  c=$(post)
  if [ "$c" = 401 ]; then tok; c=$(post); fi
  case "$c" in
  200) tr -d '\r\n' <"$d/o"; echo ;;
  202|204) ;;
  *) i=$(sed -nE 's/^\{"jsonrpc":"2\.0","id":(-?[0-9]+|"[^"\\]*")[,}].*/\1/p' "$d/b")
     [ -z "$i" ] || printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"The Studio did not answer (HTTP %s). On this machine, pw auth token --print must give your own ACTIVATE sign-in."}}\n' "$i" "$c" ;;
  esac
done
`

// bridgeLook finds pw and curl on the machine, who its pw CLI is signed
// in as, and the studio entry in the person's pw code user settings.
const bridgeLook = `p=$(command -v pw 2>/dev/null)
if [ -z "$p" ]; then for c in "$HOME/pw/pw" "$HOME/.local/bin/pw" "$HOME/bin/pw" /usr/local/bin/pw /opt/homebrew/bin/pw; do if [ -x "$c" ]; then p=$c; break; fi; done; fi
printf 'pw=%s\n' "$p"
if command -v curl >/dev/null 2>&1; then echo curl=1; fi
[ -n "$p" ] || exit 0
printf 'who=%s\n' "$("$p" auth whoami 2>/dev/null | tail -n 1)"
printf 'entry=%s\n' "$("$p" code mcp get --scope user "$1" -o json 2>/dev/null | tr -d '\r\n')"
`

// bridgePaste is the command a person pastes in a terminal on the machine
// to do what Set up for me does there: the same entry, which uses that
// machine's own pw sign-in, so it needs no key. Made for one machine, the
// entry names it in its address, so the Studio knows which machine a
// session that starts there is on.
func bridgePaste(url, machine string) string {
	if pwcode.IsRouteID(machine) {
		url += bridgeMachineParam + machine
	}
	return "pw code mcp add --scope user --force " + studioMCPName + " -- sh -c " + bridgeQuote(bridgeShim) +
		" " + bridgeShimName + " " + bridgeQuote(url) + ` "$(command -v pw)"`
}

// bridgeMachineParam names the machine in an entry's address.
const bridgeMachineParam = "?machine="

// bridgeScriptPath is where the Studio serves bridgePaste as a script, for
// bridgeFetch.
const bridgeScriptPath = "/api/mcp/bridge/setup.sh"

// bridgeFetch is bridgePaste as one short line: it fetches the same command
// from the Studio, through the session proxy with the person's own pw
// sign-in on that machine, and runs it. The token goes to curl on its
// input (-H @-), never on a command line there. url is the Studio's MCP
// endpoint, as bridgeURL gives it.
func bridgeFetch(url, machine string) string {
	at := strings.TrimSuffix(url, "/api/mcp") + bridgeScriptPath
	if pwcode.IsRouteID(machine) {
		at += bridgeMachineParam + machine
	}
	return `sh -c "$(pw auth token --print | tail -n 1 | sed 's/^/Authorization: Bearer /' | curl -fsS -H @- ` + bridgeQuote(at) + `)"`
}

// bridgeScript is GET /api/mcp/bridge/setup.sh?machine=: bridgePaste for
// the person whose pw sign-in fetched it, to run once on that machine. It
// holds no key, and only a request through the session proxy gets it.
func (m *Module) bridgeScript(w http.ResponseWriter, r *http.Request) error {
	if bridgeCrossSite(r) {
		return pwErr(403, "Fetch this from a terminal, with your pw sign-in.")
	}
	url, why := m.bridgeReach(r)
	if why != "" {
		return pwErr(409, why)
	}
	machine := r.URL.Query().Get("machine")
	if machine != "" && !pwcode.IsRouteID(machine) {
		return pwErr(400, "machine: one of your machines")
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err := io.WriteString(w, "command -v pw >/dev/null 2>&1 || { echo 'pw is not on the PATH here.' >&2; exit 1; }\n"+
		"command -v curl >/dev/null 2>&1 || { echo 'curl is not installed here.' >&2; exit 1; }\n"+
		bridgePaste(url, machine)+" && echo \"pw code here has this Studio's tools from its next session.\"\n")
	return err
}

// bridgeUnpaste is the command that takes bridgePaste's entry out again.
const bridgeUnpaste = "pw code mcp remove --scope user " + studioMCPName

// bridgeQuote is s in single quotes for a POSIX shell.
func bridgeQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bridgeTry sends one tools/list through the shim, as a session would.
const bridgeTry = `printf '%s\n' "$1" | sh -c "$2" ` + bridgeShimName + ` "$3" "$4"`

// bridgeName is a machine or owner name pw ssh takes as it is.
var bridgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// bridgeRunner runs argv on site with key (remote.Pool.Run; a test seam).
type bridgeRunner func(ctx context.Context, site, key string, argv []string, timeout time.Duration) (remote.Result, error)

// bridgeMach is one of the viewer's machines as the platform lists it to
// them: what pw ssh needs to name it, and whether its pw code daemon
// attaches a server to a live session (sessionMCPAttach).
type bridgeMach struct {
	ID, Kind, Name, Owner string
	// Status is the machine's state in the platform's pw code listing, the
	// one the Code page shows: connected when it is on and its pw code
	// answers ACTIVATE.
	Status string
	Attach bool
	// Create: its pw code takes the Studio's tools in the request that makes
	// a session (sessionMCPCreate, asked for of ACTIVATE).
	Create bool
}

// gives: a session started on it from here is given the Studio's tools as
// it starts, with nothing set up on the machine.
func (b bridgeMach) gives() bool { return b.Attach || b.Create }

func machOf(o *jsonv.Object) bridgeMach {
	attach, _ := jsonv.Path(o, "sessionMCPAttach").(bool)
	create, _ := jsonv.Path(o, "sessionMCPCreate").(bool)
	return bridgeMach{ID: jsonv.String(jsonv.Path(o, "id")), Kind: jsonv.String(jsonv.Path(o, "kind")),
		Name: jsonv.String(jsonv.Path(o, "name")), Owner: jsonv.String(jsonv.Path(o, "owner")),
		Status: jsonv.String(jsonv.Path(o, "status")), Attach: attach, Create: create}
}

// bridgeOnline is whether mach is online as the Code page says it is: on,
// with its pw code connected to ACTIVATE. The Studio neither offers its
// tools on a machine that is not, nor opens pw ssh to one to add them.
func bridgeOnline(mach bridgeMach) error {
	switch {
	case mach.Status == "connected":
		return nil
	case mach.Kind == machineWorkspace && mach.Status == "starting":
		return pwErr(409, "Your user workspace is starting. Add the tools once it is on.")
	case mach.Kind == machineWorkspace:
		return pwErr(409, "Your user workspace is not running. Start it, then add the tools.")
	}
	return pwErr(409, firstNonEmpty(mach.Name, mach.ID)+" is not connected. Start it, then add the tools.")
}

// machineWorkspace is the kind, and the id, the platform's pw code listing
// gives the viewer's own user workspace, and the name pw ssh knows it by.
const machineWorkspace = "workspace"

// bridgeMachineFn is the viewer's machine as the platform lists it to them
// (a test seam).
type bridgeMachineFn func(r *http.Request, id string) (bridgeMach, error)

// bridgeMachinesFn is every machine the platform lists to the viewer (a
// test seam).
type bridgeMachinesFn func(r *http.Request) ([]bridgeMach, error)

// bridgeAttachFn attaches the Studio to one live session on machine,
// through the platform's relay as the viewer with key (a test seam).
type bridgeAttachFn func(r *http.Request, key, machine, session, url string) error

func (m *Module) bridgeRun() bridgeRunner {
	if m.bridgeRunFn != nil {
		return m.bridgeRunFn
	}
	return func(ctx context.Context, site, key string, argv []string, timeout time.Duration) (remote.Result, error) {
		pool, _ := libraries.SharedRemote(m.A)
		return pool.Run(ctx, site, key, argv, timeout)
	}
}

func (m *Module) bridgeMachine(r *http.Request, id string) (bridgeMach, error) {
	if m.bridgeMachineFn != nil {
		return m.bridgeMachineFn(r, id)
	}
	o, err := codesessions.PlatformMachine(r, m.pf, id)
	if err != nil {
		return bridgeMach{}, err
	}
	return machOf(o), nil
}

func (m *Module) bridgeMachines(r *http.Request) ([]bridgeMach, error) {
	if m.bridgeMachinesFn != nil {
		return m.bridgeMachinesFn(r)
	}
	machines, _, err := codesessions.PlatformListing(r, m.pf)
	if err != nil {
		return nil, err
	}
	out := make([]bridgeMach, 0, len(machines))
	for _, o := range machines {
		out = append(out, machOf(o))
	}
	return out, nil
}

// bridgeCheck is whether the viewer may have the Studio set up pw code on
// mach, and their own ACTIVATE credential to do it with: never the
// Studio's account, and only on a machine of theirs (an existing cluster
// they log in to, though an administrator shared it with them).
func (m *Module) bridgeCheck(r *http.Request, mach bridgeMach) (key string, err error) {
	name := firstNonEmpty(mach.Name, mach.ID)
	u := user(r)
	if u == nil || u.Username == "" {
		return "", pwErr(401, "Sign in to set up pw code.")
	}
	if why := bridgeByHand(mach); why != "" {
		return "", pwErr(409, why)
	}
	switch {
	case mach.Kind != "existing" && !strings.EqualFold(mach.Owner, u.Username):
		return "", pwErr(403, name+" is "+firstNonEmpty(mach.Owner, "someone else")+"'s, not yours: only its owner sets up pw code there.")
	}
	key = m.viewerPlatformKey(r)
	if key == "" {
		return "", pwErr(409, "The Studio reaches "+name+" with your own ACTIVATE sign-in, and has none for you: sign in with ACTIVATE under Settings, Accounts, or do it yourself.")
	}
	return key, nil
}

// bridgeByHand is why the Studio has no pw ssh to mach, whoever asks: the
// person adds the tools there with a command (bridgePaste) instead.
func bridgeByHand(mach bridgeMach) string {
	name := firstNonEmpty(mach.Name, mach.ID)
	switch {
	case mach.Kind == "local":
		return name + " is a computer that connected itself to ACTIVATE, and the Studio cannot open a terminal on it: do it yourself in a terminal there."
	case mach.Kind == machineWorkspace:
		// pw ssh names the viewer's own workspace by the word alone, and
		// the platform lists only the viewer's own (bridgeCheck holds the
		// owner to them).
		if mach.ID != machineWorkspace {
			return "The Studio cannot name " + name + " to pw ssh: do it yourself in a terminal there."
		}
		return ""
	case mach.Kind != "cloud" && mach.Kind != "instance" && mach.Kind != "existing":
		return "The Studio does not know how to reach " + name + " (an ACTIVATE machine of kind " + firstNonEmpty(mach.Kind, "unknown") + "): do it yourself in a terminal there."
	case mach.Name == "" || !bridgeName.MatchString(mach.Name) || mach.Owner != "" && !bridgeName.MatchString(mach.Owner):
		return "The Studio cannot name " + name + " to pw ssh: do it yourself in a terminal there."
	}
	return ""
}

// bridgeSite is the pw ssh site of the viewer's machine mach, and their own
// ACTIVATE credential. A resource's is always pw://<owner>/<name> (pw ssh
// finds no cluster by its bare name): an existing cluster's owner is the
// one the pw code listing gives; where it gives none, the one the viewer's
// own cluster listing gives for that name; and only then the viewer. The
// viewer's user workspace is the word workspace, which pw ssh takes for the
// workspace of the key's own user (pw://<owner>/workspace would be a
// cluster of that name), so with their key it can be no one else's.
func (m *Module) bridgeSite(r *http.Request, mach bridgeMach) (site, key string, err error) {
	key, err = m.bridgeCheck(r, mach)
	if err != nil {
		return "", "", err
	}
	if err := bridgeOnline(mach); err != nil {
		return "", "", err
	}
	if mach.Kind == machineWorkspace {
		return machineWorkspace, key, nil
	}
	owner := mach.Owner
	if owner == "" {
		if owner, err = m.bridgeOwner(r, key, mach.Name); err != nil {
			return "", "", err
		}
	}
	return "pw://" + owner + "/" + mach.Name, key, nil
}

// bridgeCluster is one cluster in the viewer's ACTIVATE cluster listing
// (/api/clusters): its owner is User, or the owner in its pw:// URI.
type bridgeCluster struct {
	Name, Type, User, URI string
}

// bridgeClustersFn is the clusters the platform lists to the viewer, asked
// with their own key (a test seam).
type bridgeClustersFn func(r *http.Request, key string) ([]bridgeCluster, error)

func (m *Module) bridgeClusters() bridgeClustersFn {
	if m.bridgeClustersFn != nil {
		return m.bridgeClustersFn
	}
	return func(r *http.Request, key string) ([]bridgeCluster, error) {
		res := m.pf.APIFetch(r.Context(), "/api/clusters", key, 30*time.Second)
		if res == nil || !res.OK {
			status := 0
			if res != nil {
				status = res.Status
			}
			return nil, fmt.Errorf("ACTIVATE did not list your clusters (HTTP %d)", status)
		}
		var raw []struct {
			Name string  `json:"name"`
			Type *string `json:"type"`
			User *string `json:"user"`
			URI  string  `json:"uri"`
		}
		if err := json.Unmarshal(res.Raw, &raw); err != nil {
			return nil, fmt.Errorf("ACTIVATE's cluster listing was not one: %w", err)
		}
		out := make([]bridgeCluster, 0, len(raw))
		for _, c := range raw {
			out = append(out, bridgeCluster{Name: c.Name, Type: deref(c.Type), User: deref(c.User), URI: c.URI})
		}
		return out, nil
	}
}

// bridgeOwner is the owner of the existing cluster named name, as the
// viewer's own cluster listing gives it: the one cluster of that name, or,
// where there is none or the listing cannot be read, the viewer. Two or
// more of that name are not guessed between.
func (m *Module) bridgeOwner(r *http.Request, key, name string) (string, error) {
	me := user(r).Username
	cs, err := m.bridgeClusters()(r, key)
	if err != nil {
		m.A.Log.Warn("mcp setup: the owner of an existing cluster is taken to be the viewer", "machine", name, "err", err.Error())
		return me, nil
	}
	var owners []string
	for _, c := range cs {
		if c.Name != name || c.Type != "" && c.Type != "existing" {
			continue
		}
		o := c.User
		if o == "" {
			if rest, ok := strings.CutPrefix(c.URI, "pw://"); ok {
				if who, n, ok := strings.Cut(rest, "/"); ok && n == name {
					o = who
				}
			}
		}
		if o != "" && bridgeName.MatchString(o) && !slices.Contains(owners, o) {
			owners = append(owners, o)
		}
	}
	switch len(owners) {
	case 0:
		return me, nil
	case 1:
		return owners[0], nil
	}
	sites := make([]string, len(owners))
	for i, o := range owners {
		sites[i] = "pw://" + o + "/" + name
	}
	return "", pwErr(409, "ACTIVATE lists more than one cluster named "+name+" to you ("+strings.Join(sites, ", ")+"), and the Studio does not guess which one this is: do it yourself in a terminal there.")
}

// bridgeChat is the viewer's own relayed chat id and its machine.
func (m *Module) bridgeChat(r *http.Request, id string) (*conversations.PWCodeLink, bridgeMach, error) {
	c, err := m.ownPWChat(r, id)
	if err != nil {
		return nil, bridgeMach{}, err
	}
	if c.PWCode == nil || !c.PWCode.Relay || c.PWCode.Machine == "" {
		return nil, bridgeMach{}, pwErr(409, "This chat's session is one the Studio reaches itself, and it has the Studio's tools already.")
	}
	mach, err := m.bridgeListed(r, c.PWCode.Machine, c.PWCode.MachineName)
	return c.PWCode, mach, err
}

// bridgeListed is machine id as the platform lists it to the viewer.
func (m *Module) bridgeListed(r *http.Request, id, name string) (bridgeMach, error) {
	if m.viewerPlatformKey(r) == "" {
		return bridgeMach{ID: id, Name: name}, pwErr(409, "The Studio reaches "+firstNonEmpty(name, id)+" with your own ACTIVATE sign-in, and has none for you: sign in with ACTIVATE under Settings, Accounts, or do it yourself.")
	}
	mach, err := m.bridgeMachine(r, id)
	if err != nil {
		return bridgeMach{ID: id, Name: name}, pwErr(409, "ACTIVATE did not list "+firstNonEmpty(name, id)+" as one of your machines ("+apiMessage(err)+").")
	}
	return mach, nil
}

// bridgeOfferT is what the Studio says of its tools on a machine for the
// viewer: set (pw code there has this Studio, as the Studio last found
// it), declined (they said not to add it there), offer, command (the
// Studio cannot reach it, and the person can with a command), or
// unavailable, with why.
type bridgeOfferT struct {
	State     string `json:"state"`
	Machine   string `json:"machine"`
	MachineID string `json:"machineId"`
	Why       string `json:"why,omitempty"`
	// Available is whether the Studio can set it up there (the card).
	Available bool `json:"available"`
	// Allows is the tools pw code there would run without asking.
	Allows []string `json:"allows,omitempty"`
	// Attach: the machine takes the Studio's tools into a session as it
	// starts, so one started from here has them without a setup.
	Attach bool `json:"attach,omitempty"`
	// Chosen: the person has said to add them there, so a session started
	// there adds them with no question.
	Chosen bool `json:"chosen,omitempty"`
	// Held: what kept the Studio from adding them as a session started
	// there; it does not try again until the person says to.
	Held string `json:"held,omitempty"`
	// Command adds the tools in a terminal on a machine the Studio does not
	// reach, and Uncommand takes them out.
	Command   string `json:"command,omitempty"`
	Uncommand string `json:"uncommand,omitempty"`
}

func (m *Module) bridgeOffer(r *http.Request, mach bridgeMach) bridgeOfferT {
	o := bridgeOfferT{Machine: firstNonEmpty(mach.Name, mach.ID), MachineID: mach.ID}
	url, why := m.bridgeReach(r)
	if why != "" {
		o.State, o.Why = "unavailable", why
		return o
	}
	p := m.bridgePrefs().get(userID(r), mach.ID)
	set := p.URL != "" && p.URL == url
	o.Attach = mach.gives() && m.viewerPlatformKey(r) != ""
	o.Chosen, o.Held = p.Add, p.Held
	if _, err := m.bridgeCheck(r, mach); err != nil {
		why = apiMessage(err)
	}
	if why != "" && !o.Attach {
		o.State, o.Why = "unavailable", why
		if bridgeByHand(mach) == "" || user(r) == nil || m.viewerPlatformKey(r) == "" {
			return o
		}
		o.State, o.Command, o.Uncommand = "command", bridgeFetch(url, mach.ID), bridgeUnpaste
		if set {
			o.State = "set"
		}
		return o
	}
	if set {
		// Set up already: so it stays while the machine is off, though it
		// is not checked again until it is on.
		o.State, o.Available, o.Allows = "set", why == "" && bridgeOnline(mach) == nil, m.bridgeAllows(r)
		if why != "" && bridgeByHand(mach) != "" {
			o.Command, o.Uncommand = bridgeFetch(url, mach.ID), bridgeUnpaste
		}
		return o
	}
	// Offered, declined or not, only on a machine that is on with its pw
	// code connected: one that comes on later is offered then.
	if err := bridgeOnline(mach); err != nil {
		o.State, o.Why = "unavailable", apiMessage(err)
		return o
	}
	o.Available, o.Allows = why == "", m.bridgeAllows(r)
	switch {
	case p.Declined:
		o.State = "declined"
	default:
		o.State = "offer"
	}
	return o
}

// bridgeSetupOffer is the card's offer for chat id.
func (m *Module) bridgeSetupOffer(r *http.Request, id string) bridgeOfferT {
	_, mach, err := m.bridgeChat(r, id)
	if err != nil {
		return bridgeOfferT{State: "unavailable", Why: apiMessage(err), Machine: firstNonEmpty(mach.Name, mach.ID), MachineID: mach.ID}
	}
	return m.bridgeOffer(r, mach)
}

// bridgeCrossSite is a request sent by a page of another site: a browser
// names the page's origin on every POST, so one that is not the Studio's
// does not open a terminal on someone's machine.
func bridgeCrossSite(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return true
	}
	return r.Header.Get("Origin") != "" && !sessions.SameOrigin(r)
}

// bridgeEntry is how the person's pw code settings hold the studio entry.
type bridgeEntry struct {
	Config struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	} `json:"config"`
}

// target is where the entry sends the Studio's tools, and whether the
// Studio wrote it (the shim, with the shim as it is now).
func (e *bridgeEntry) target() (url string, ours, current bool) {
	c := e.Config
	if c.Command == "sh" && len(c.Args) >= 4 && c.Args[0] == "-c" && c.Args[2] == bridgeShimName {
		at, _, _ := strings.Cut(c.Args[3], bridgeMachineParam)
		return at, true, c.Args[1] == bridgeShim
	}
	if c.URL != "" {
		return c.URL, false, false
	}
	return strings.TrimSpace(c.Command + " " + strings.Join(c.Args, " ")), false, false
}

// bridgeLooked is what bridgeLook found on the machine.
type bridgeLooked struct {
	pw, who string
	curl    bool
	entry   *bridgeEntry
}

func parseBridgeLook(out string) bridgeLooked {
	var l bridgeLooked
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(line, "\r"), "=")
		switch k {
		case "pw":
			l.pw = v
		case "curl":
			l.curl = v == "1"
		case "who":
			l.who = strings.TrimSpace(v)
		case "entry":
			var e bridgeEntry
			if strings.TrimSpace(v) != "" && json.Unmarshal([]byte(v), &e) == nil {
				l.entry = &e
			}
		}
	}
	return l
}

// bridgeSetup is POST /api/mcp/bridge/setup {conversationId | machineId,
// replace}: the Studio adds itself to pw code on the machine, as the
// person, tries the entry, and, for a chat whose machine attaches servers
// to a live session, gives the session in front of them the tools too. An
// entry for another Studio is replaced only when the person said to
// (replace); one for this Studio is left as it is.
func (m *Module) bridgeSetup(w http.ResponseWriter, r *http.Request) error {
	if bridgeCrossSite(r) {
		return pwErr(403, "Set up pw code from this Studio's own pages.")
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	replace, _ := rb.Get("replace").(bool)
	url, why := m.bridgeReach(r)
	if why != "" {
		return pwErr(409, why)
	}
	var link *conversations.PWCodeLink
	var mach bridgeMach
	if id := rb.Str("conversationId"); id != "" {
		link, mach, err = m.bridgeChat(r, id)
	} else {
		mach, err = m.bridgeListed(r, rb.Str("machineId"), "")
	}
	if err != nil {
		return err
	}
	site, key, err := m.bridgeSite(r, mach)
	if err != nil {
		return err
	}
	who := userID(r)
	release, err := m.bridgeClaim(r)
	if err != nil {
		return err
	}
	defer release()

	// A machine the Studio set up, or found set up, for this Studio is not
	// opened again unless the person asks to check it.
	check, _ := rb.Get("check").(bool)
	var out map[string]any
	if p := m.bridgePrefs().get(who, mach.ID); p.URL == url && !check && !replace {
		out = map[string]any{"done": true, "changed": false, "already": true, "machine": firstNonEmpty(mach.Name, mach.ID),
			"tools": len(m.personTools(r, m.personCtx(r))), "worked": true, "why": ""}
	} else {
		out, err = m.bridgeInstall(r, site, key, mach, url, replace)
	}
	if err != nil || out["ask"] == true {
		if err != nil {
			return err
		}
		return reqbody.OK(w, out)
	}
	if out["worked"] == true {
		m.bridgePrefs().set(who, mach.ID, bridgePref{URL: url, Add: true})
	}
	// The session in front of them: pw code reads its servers when a
	// session starts, and a daemon that attaches one to a live session
	// (sessionMCPAttach) takes this Studio's at once.
	live := "next"
	if link != nil && link.Session != "" && mach.Attach && out["worked"] == true {
		if err := m.bridgeAttach()(r, key, mach.ID, link.Session, url); err != nil {
			m.A.Log.Warn("mcp setup: the session in front could not take the Studio's tools", "machine", mach.Name, "err", err.Error())
			out["liveWhy"] = firstLine(apiMessage(err), 300)
		} else {
			live = "now"
		}
	}
	out["live"] = live
	return reqbody.OK(w, out)
}

// bridgeClaim is the one setup or removal a person runs at a time; the
// answer ends it.
func (m *Module) bridgeClaim(r *http.Request) (func(), error) {
	who := userID(r)
	m.bridgeMu.Lock()
	defer m.bridgeMu.Unlock()
	if m.bridgeBusy == nil {
		m.bridgeBusy = map[string]bool{}
	}
	if m.bridgeBusy[who] {
		return nil, pwErr(409, "The Studio is already setting up pw code for you; wait for it to finish.")
	}
	m.bridgeBusy[who] = true
	return func() { m.bridgeMu.Lock(); delete(m.bridgeBusy, who); m.bridgeMu.Unlock() }, nil
}

func bridgeUnreached(machine, site string, err error) error {
	return pwErr(502, "The Studio could not reach "+machine+" with pw ssh as you, at "+site+" ("+firstLine(apiMessage(err), 300)+"). Do it yourself in a terminal there.")
}

// bridgeLookAt is bridgeLook's answer from the machine.
func (m *Module) bridgeLookAt(r *http.Request, site, key, machine string) (bridgeLooked, error) {
	res, err := m.bridgeRun()(r.Context(), site, key, []string{"sh", "-c", bridgeLook, "studio-look", studioMCPName}, 90*time.Second)
	if err != nil {
		return bridgeLooked{}, bridgeUnreached(machine, site, err)
	}
	return parseBridgeLook(res.Stdout), nil
}

// bridgeInstall looks at pw code on the machine, adds the entry where it is
// missing or out of date, and tries it.
func (m *Module) bridgeInstall(r *http.Request, site, key string, mach bridgeMach, url string, replace bool) (map[string]any, error) {
	run := m.bridgeRun()
	ctx := r.Context()
	machine := firstNonEmpty(mach.Name, mach.ID)
	who := userID(r)
	look, err := m.bridgeLookAt(r, site, key, machine)
	if err != nil {
		return nil, err
	}
	me := user(r).Username
	switch {
	case look.pw == "":
		return nil, pwErr(409, "pw is not on "+machine+" for your account, so the Studio cannot add itself to pw code there. Do it yourself where pw code runs.")
	case !look.curl:
		return nil, pwErr(409, "curl is not installed on "+machine+", which the entry sends the Studio's tools through. Do it yourself instead, with a key of yours in the command.")
	case !strings.EqualFold(look.who, me):
		as := look.who
		if as == "" {
			as = "no one"
		}
		return nil, pwErr(409, "pw on "+machine+" is signed in as "+as+", not as you ("+me+"), and the entry uses its sign-in. Run pw auth there as yourself, then set up again.")
	}
	changed, replaced := false, ""
	if e := look.entry; e != nil {
		at, ours, current := e.target()
		switch {
		case at == url && (current || !ours):
			// This Studio's already: the Studio's own form, or one added by hand.
		case at == url:
			changed = true
		case !replace:
			return map[string]any{"done": false, "ask": true, "machine": machine, "current": at,
				"why": "pw code on " + machine + " already has a studio entry, for " + at + ". Replace it with this Studio's?"}, nil
		default:
			changed, replaced = true, at
		}
	} else {
		changed = true
	}
	if changed {
		add := []string{look.pw, "code", "mcp", "add", "--scope", "user", "--force", studioMCPName, "--", "sh", "-c", bridgeShim, bridgeShimName, url, look.pw}
		res, err := run(ctx, site, key, add, 90*time.Second)
		if err != nil {
			return nil, bridgeUnreached(machine, site, err)
		}
		if res.Code != 0 {
			return nil, pwErr(502, "pw code on "+machine+" did not take the entry: "+firstLine(strings.TrimSpace(res.Stderr+" "+res.Stdout), 300))
		}
		m.A.Log.Info("mcp setup: the Studio's tools added to pw code", "user", who, "machine", machine, "replaced", replaced != "")
	}
	// The tools that only read run there without asking (mcpallow.go). A
	// machine whose settings cannot take that still gets the entry.
	allows := m.bridgeAllows(r)
	added, allowWhy := bridgeRulesRun(ctx, run, site, key, "add", machineRules(allows))
	if added > 0 {
		m.A.Log.Info("mcp setup: the Studio's tools that read are allowed in pw code", "user", who, "machine", machine, "rules", added)
	}
	// Try it as a session there would: one tools/list through the shim,
	// which the Studio records as this person's when it arrives.
	start := time.Now()
	tries := []string{"sh", "-c", bridgeTry, "studio-try", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, bridgeShim, url, look.pw}
	tools, why := 0, ""
	if res, err := run(ctx, site, key, tries, 90*time.Second); err != nil {
		why = "The Studio could not try it on " + machine + ": " + firstLine(apiMessage(err), 300)
	} else {
		var answer struct {
			Result struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &answer)
		tools = len(answer.Result.Tools)
		m.bridgeMu.Lock()
		v, ok := m.bridgeVisits[who]
		m.bridgeMu.Unlock()
		seen := ok && !v.At.Before(start.Add(-time.Second))
		switch {
		case answer.Error != nil:
			why = answer.Error.Message
		case tools == 0 || !seen:
			why = "The entry is there, but trying it on " + machine + " did not reach this Studio as you."
		}
	}
	return map[string]any{"done": true, "changed": changed, "replaced": replaced != "", "machine": machine,
		"tools": tools, "worked": why == "", "why": why, "allows": allows, "allowed": added, "allowWhy": allowWhy}, nil
}

// bridgeAttach is the live attach: POST /api/code/machines/{machine}/
// sessions/{id}/mcp through the platform as the viewer, asking the
// machine to send the Studio its own platform credential (platformAuth),
// which the session proxy takes off before the request reaches the Studio.
func (m *Module) bridgeAttach() bridgeAttachFn {
	if m.bridgeAttachFn != nil {
		return m.bridgeAttachFn
	}
	return func(r *http.Request, key, machine, session, url string) error {
		origin := strings.TrimRight(m.pf.Origin(), "/")
		if origin == "" {
			return pwErr(409, "This Studio is not connected to an ACTIVATE platform.")
		}
		body, _ := json.Marshal(map[string]any{"name": studioMCPName, "url": url, "platformAuth": true})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			origin+"/api/code/machines/"+platform.EncodeURIComponent(machine)+"/sessions/"+platform.EncodeURIComponent(session)+"/mcp", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := platform.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
			var e struct {
				Detail, Message string
			}
			_ = json.Unmarshal(b, &e)
			return pwErr(res.StatusCode, firstNonEmpty(e.Detail, e.Message, "ACTIVATE answered "+res.Status))
		}
		return nil
	}
}

// bridgeChoose keeps whether the person wants the Studio's tools on machine.
func (m *Module) bridgeChoose(who, machine string, add bool) {
	p := m.bridgePrefs().get(who, machine)
	if p.Add != add || p.Declined == add {
		p.Add, p.Declined, p.Held = add, !add, ""
		m.bridgePrefs().set(who, machine, p)
	}
}

// bridgeStartWait is the most a session's start waits for the Studio's
// tools to be added on its machine first.
const bridgeStartWait = 45 * time.Second

// bridgeAtStart adds the Studio's tools to pw code on mach before a session
// starts there, since pw code reads its servers as one starts. chose is the
// person's choice with this start, nil for the one they made before: with
// none, or where they said not to, nothing is done. Where the tools could
// not be added, why says so, and the session starts without them. What will
// not change by itself (another Studio's entry, pw signed in as someone
// else) is kept, so the next start does not wait for the same answer.
func (m *Module) bridgeAtStart(r *http.Request, mach bridgeMach, url string, chose *bool) (has bool, why string) {
	who := userID(r)
	if chose != nil {
		m.bridgeChoose(who, mach.ID, *chose)
	}
	p := m.bridgePrefs().get(who, mach.ID)
	switch {
	case p.URL == url:
		return true, ""
	case !p.Add || p.Declined:
		return false, ""
	case p.Held != "":
		return false, p.Held
	}
	if bridgeByHand(mach) != "" {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), bridgeStartWait)
	defer cancel()
	// A request of its own: resolving the viewer writes to the request.
	r = r.WithContext(ctx)
	site, key, err := m.bridgeSite(r, mach)
	if err != nil {
		return false, apiMessage(err)
	}
	release, err := m.bridgeClaim(r)
	if err != nil {
		return false, apiMessage(err)
	}
	defer release()
	out, err := m.bridgeInstall(r, site, key, mach, url, false)
	var refused *apierr.Error
	switch {
	case errors.As(err, &refused) && refused.Status == http.StatusConflict:
		p.Held = apiMessage(err)
	case err != nil:
		why = apiMessage(err)
	case out["ask"] == true:
		at, _ := out["current"].(string)
		p.Held = "pw code there has a studio entry for " + at + ", which the Studio does not replace by itself."
	case out["worked"] != true:
		why, _ = out["why"].(string)
	default:
		p.URL = url
	}
	if p.Held == "" && p.URL != url {
		m.A.Log.Warn("mcp setup: a session starts without the Studio's tools", "user", who, "machine", mach.Name, "why", why)
		return false, firstLine(why, 300)
	}
	m.bridgePrefs().set(who, mach.ID, p)
	return p.URL == url, firstLine(p.Held, 300)
}

// bridgeMachineSeen keeps that a machine's pw code has this Studio, when a
// session that starts there reaches the tools through an entry that names
// the machine (bridgePaste): how the Studio knows it of one it cannot open.
func (m *Module) bridgeMachineSeen(r *http.Request) {
	machine, who := r.URL.Query().Get("machine"), userID(r)
	if who == "" || !pwcode.IsRouteID(machine) {
		return
	}
	url, why := m.bridgeReach(r)
	if why != "" {
		return
	}
	prefs := m.bridgePrefs()
	if p := prefs.get(who, machine); p.URL == url || p.URL == "" && prefs.count(who) >= bridgeMachinesMost {
		return
	}
	prefs.set(who, machine, bridgePref{URL: url, Add: true})
	// A session just started there from here is the one that reached them.
	for _, c := range m.Conv.List() {
		if l := c.PWCode; l != nil && l.Relay && l.Picked && !l.HasTools && l.Machine == machine && deref(c.Owner) == user(r).Username && m.bridgeJustMade(l) {
			now := *l
			now.HasTools, now.ToolsError = true, ""
			m.Conv.SetPWCode(c.ID, &now)
		}
	}
}

// bridgeJustMade is whether l's session was started within the last minutes.
func (m *Module) bridgeJustMade(l *conversations.PWCodeLink) bool {
	at, err := time.Parse(time.RFC3339, l.CreatedAt)
	return err == nil && time.Since(at) < 5*time.Minute
}

// bridgeMachinesMost is the most machines kept for one person.
const bridgeMachinesMost = 500

// bridgeDecline is POST /api/mcp/bridge/prefs {machineId, add | forget}:
// whether the person wants the Studio's tools added on that machine. Not
// wanting them there keeps the Studio from offering them again for it until
// they choose them; forget is neither, for a machine they took them out of.
func (m *Module) bridgeDecline(w http.ResponseWriter, r *http.Request) error {
	if bridgeCrossSite(r) {
		return pwErr(403, "Change this from this Studio's own pages.")
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	id := rb.Str("machineId")
	add, _ := rb.Get("add").(bool)
	if user(r) == nil || id == "" || len(id) > 128 {
		return pwErr(400, "machineId: one of your machines")
	}
	if _, err := m.bridgeListed(r, id, ""); err != nil {
		return err
	}
	if forget, _ := rb.Get("forget").(bool); forget {
		// The person takes them out themselves, where the Studio cannot.
		m.bridgePrefs().set(userID(r), id, bridgePref{})
		return reqbody.OK(w, reqbody.O("ok", true))
	}
	m.bridgeChoose(userID(r), id, add)
	return reqbody.OK(w, reqbody.O("ok", true))
}

// bridgeMachinesRoute is GET /api/mcp/bridge/machines: the Studio's tools
// on each of the viewer's machines (bridgeOffer), for the Code page.
func (m *Module) bridgeMachinesRoute(w http.ResponseWriter, r *http.Request) error {
	if _, why := m.bridgeReach(r); why != "" {
		return reqbody.OK(w, reqbody.O("available", false, "why", why, "machines", []any{}))
	}
	if m.viewerPlatformKey(r) == "" {
		return reqbody.OK(w, reqbody.O("available", false, "why", "Sign in with ACTIVATE under Settings, Accounts, so the Studio can reach your machines as you.", "machines", []any{}))
	}
	machines, err := m.bridgeMachines(r)
	if err != nil {
		return err
	}
	out := make([]bridgeOfferT, 0, len(machines))
	for _, mach := range machines {
		out = append(out, m.bridgeOffer(r, mach))
	}
	return reqbody.OK(w, reqbody.O("available", true, "machines", out))
}

// bridgePref is what the Studio keeps of one person's machine: the Studio
// URL pw code there was last found or set up to reach, whether they said to
// add the tools there or not to, and what kept the Studio from adding them.
type bridgePref struct {
	URL      string `json:"url,omitempty"`
	Add      bool   `json:"add,omitempty"`
	Declined bool   `json:"declined,omitempty"`
	Held     string `json:"held,omitempty"`
	At       string `json:"at,omitempty"`
}

// bridgePrefStore keeps bridgePrefs per person and machine in
// INDEX_BASE/studio-tools-machines.json.
type bridgePrefStore struct {
	file string
	mu   sync.Mutex
	all  map[string]map[string]bridgePref
}

func (m *Module) bridgePrefs() *bridgePrefStore {
	m.bridgeMu.Lock()
	defer m.bridgeMu.Unlock()
	if m.bridgePrefStore == nil {
		m.bridgePrefStore = &bridgePrefStore{file: filepath.Join(m.A.Cfg.IndexBase, "studio-tools-machines.json")}
	}
	return m.bridgePrefStore
}

func (s *bridgePrefStore) load() {
	if s.all != nil {
		return
	}
	s.all = map[string]map[string]bridgePref{}
	if b, err := os.ReadFile(s.file); err == nil {
		_ = json.Unmarshal(b, &s.all)
	}
}

func (s *bridgePrefStore) get(user, machine string) bridgePref {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.all[user][machine]
}

func (s *bridgePrefStore) count(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return len(s.all[user])
}

func (s *bridgePrefStore) set(user, machine string, p bridgePref) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if s.all[user] == nil {
		s.all[user] = map[string]bridgePref{}
	}
	p.At = time.Now().UTC().Format(time.RFC3339)
	s.all[user][machine] = p
	b, _ := json.MarshalIndent(s.all, "", "  ")
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}
