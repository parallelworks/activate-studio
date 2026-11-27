package codesessions

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// On a Studio with no platform that offers its own host as an agent site
// (sign-in off, or AGENT_LOCAL_SITE=1), the Code page lists the sessions
// of this host's pw code daemon instead, read from its socket in the same
// shapes the platform passes on from a machine's daemon.

// localDaemon is this host's daemon when the page reads it: the
// platform not available to the viewer, and the local site offered.
func (s *routes) localDaemon(r *http.Request) pwcode.Client {
	if !localCode(r, s.a, s.p) {
		return nil
	}
	return agentsites.Current().Daemon.Local()
}

func localCode(r *http.Request, a *app.App, p *platform.Platform) bool {
	sites := agentsites.Current()
	if sites == nil || sites.Daemon == nil || !sites.LocalSiteAllowed(a.Cfg.AuthEnabled()) {
		return false
	}
	v, err := p.ViewerOf(r)
	if err != nil {
		return false
	}
	if a.Cfg.AuthEnabled() || signedIn(v) {
		// With sign-in on (AGENT_LOCAL_SITE=1) only a signed-in person
		// reads this host, and only when the platform is not theirs to
		// read with their own credential (viewerKey).
		return v.UserID() != "" && (!p.PlatformGateway() || v.PlatformKey() == "")
	}
	return !p.PlatformAvailable(v)
}

// mine is, with sign-in on, the sessions on this host's daemon the
// Studio started for the viewer (their chats, delegations and standing
// agents), and whether the page is limited to them. Every session on the
// daemon runs as the Studio's account, so without this a person would
// read everyone's and could answer their waiting calls.
func (s *routes) mine(r *http.Request) (map[string]bool, bool) {
	if !s.a.Cfg.AuthEnabled() {
		if v, err := s.p.ViewerOf(r); err != nil || !signedIn(v) {
			return nil, false
		}
	}
	return s.a.Providers.SessionsOf(r), true
}

// notYours is the answer for a session on this host that is not the
// viewer's: the same as for one that is not there.
func notYours() error {
	return platform.KbError(404, "That session is not one of yours on this computer.")
}

// LocalName is the machine's name on the Code page for this host.
const LocalName = "This computer"

func call(ctx context.Context, c pwcode.Client, route string, out any) error {
	v, err := c.Call(ctx, http.MethodGet, route, nil, 15*time.Second)
	if err != nil {
		return platform.KbError(502, "pw code on this computer did not answer: "+err.Error())
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

func (s *routes) localMachine(ctx context.Context, c pwcode.Client) (Machine, error) {
	var st struct {
		MachineID       string   `json:"machineId"`
		Hostname        string   `json:"hostname"`
		PermissionModes []string `json:"permissionModes"`
	}
	if err := call(ctx, c, "/v1/status", &st); err != nil {
		return Machine{}, err
	}
	id := st.MachineID
	if id == "" {
		id = agentsites.Local
	}
	return Machine{ID: id, Name: LocalName, Kind: "local", Hostname: st.Hostname, Status: "connected", DaemonID: st.MachineID, PermissionModes: st.PermissionModes}, nil
}

func (s *routes) localList(r *http.Request, c pwcode.Client) (any, error) {
	m, err := s.localMachine(r.Context(), c)
	if err != nil {
		return nil, err
	}
	var rows []session
	if err := call(r.Context(), c, "/v1/sessions", &rows); err != nil {
		return nil, err
	}
	names := map[string]string{m.ID: m.Name}
	own, only := s.mine(r)
	out := []View{}
	for _, x := range rows {
		if only && !own[x.ID] {
			continue
		}
		x.MachineID = m.ID
		v := view(x, names, "")
		v.URL = ""
		out = append(out, v)
	}
	return map[string]any{"machines": []Machine{m}, "sessions": out, "codeUrl": "", "local": true}, nil
}

func (s *routes) localItem(r *http.Request, c pwcode.Client, mid, id string) (*detail, error) {
	m, err := s.localMachine(r.Context(), c)
	if err != nil {
		return nil, err
	}
	if mid != m.ID {
		return nil, platform.KbError(404, "That session is not on this computer.")
	}
	if own, only := s.mine(r); only && !own[id] {
		return nil, notYours()
	}
	var d detail
	if err := call(r.Context(), c, "/v1/sessions/"+platform.EncodeURIComponent(id), &d); err != nil {
		return nil, err
	}
	d.MachineID = m.ID
	return &d, nil
}

// registered is the routes Register made, for Find.
var registered atomic.Pointer[routes]

// localFind is Find on this host's daemon.
func (s *routes) localFind(r *http.Request, c pwcode.Client, mid, id string) (*Machine, *View, error) {
	d, err := s.localItem(r, c, mid, id)
	if err != nil {
		return nil, nil, err
	}
	m, err := s.localMachine(r.Context(), c)
	if err != nil {
		return nil, nil, err
	}
	v := view(d.session, map[string]string{m.ID: m.Name}, "")
	v.URL = ""
	return &m, &v, nil
}

// localHere is, without sign-in, the machine of this computer's pw code
// daemon, which the Studio drives for the person, so the platform's listing
// can mark it as this Studio's; nil with sign-in on or no daemon answering.
func (s *routes) localHere(r *http.Request, signIn bool) *Machine {
	sites := agentsites.Current()
	if signIn || sites == nil || sites.Daemon == nil {
		return nil
	}
	c := sites.Daemon.Local()
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), localHereTimeout)
	defer cancel()
	m, err := s.localMachine(ctx, c)
	if err != nil {
		return nil
	}
	return &m
}

const localHereTimeout = 2 * time.Second
