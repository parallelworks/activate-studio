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
	return err == nil && !p.PlatformAvailable(v)
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
		MachineID string `json:"machineId"`
		Hostname  string `json:"hostname"`
	}
	if err := call(ctx, c, "/v1/status", &st); err != nil {
		return Machine{}, err
	}
	id := st.MachineID
	if id == "" {
		id = agentsites.Local
	}
	return Machine{ID: id, Name: LocalName, Kind: "local", Hostname: st.Hostname, Status: "connected", DaemonID: st.MachineID}, nil
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
	out := []View{}
	for _, x := range rows {
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
