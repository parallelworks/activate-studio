package platform

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
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/keycookie"
)

// BridgePrefix is Node's bridge; the Go front refuses it from clients.
const BridgePrefix = "/api/__bridge/"

// SingleOwner is credentials.ts's SINGLE_OWNER.
const SingleOwner = "single"

// NodeBridge is how Go routes of this track ask Node for what it still
// holds: the viewer's personal key, conversations, the MCP tool list.
type NodeBridge struct {
	Node   *app.Node
	client *http.Client
}

var bridgeClient = &http.Client{Timeout: 30 * time.Second}

// BridgeSecret is the one bridge secret (internal/bridge).
func BridgeSecret() string { return bridge.Secret() }

// NewNodeBridge wraps a's Node, or is nil without one.
func NewNodeBridge(n *app.Node) *NodeBridge {
	if n == nil {
		return nil
	}
	return &NodeBridge{Node: n, client: bridgeClient}
}

// Do sends a bridge call for the viewer of r (nil: no viewer) and decodes
// the JSON answer into out.
func (b *NodeBridge) Do(r *http.Request, method, path string, body, out any) error {
	if b == nil {
		return errors.New("no Node server")
	}
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	u := strings.TrimSuffix(b.Node.Base.String(), "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	bridge.Sign(req)
	req.Header.Set(app.InternalHeader, "1")
	if r != nil {
		req.Host = r.Host
		if b.Node.Front != nil {
			b.Node.Front.ToNode(req, auth.UserOf(r))
		} else {
			for _, h := range []string{b.Node.Header, "Cookie", "Authorization"} {
				if h != "" && r.Header.Get(h) != "" {
					req.Header.Set(h, r.Header.Get(h))
				}
			}
		}
	}
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return &app.NodeError{Status: res.StatusCode, Body: data}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Background is Do with no viewer.
func (b *NodeBridge) Background(method, path string, body, out any) error {
	return b.Do(nil, method, path, body, out)
}

// User is the verified viewer.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Cred is credentials.ts's UserCred.
type Cred struct {
	Key      string          `json:"key"`
	BaseURL  string          `json:"baseUrl"`
	Endpoint json.RawMessage `json:"endpoint"`
}

// Viewer is who a request is and the personal credentials it carries.
// Cred is the one earlier builds knew: the person's ACTIVATE key, else
// their endpoint. Endpoint is their endpoint, also when Cred is their key.
type Viewer struct {
	AuthEnabled bool            `json:"authEnabled"`
	User        *User           `json:"user"`
	Cred        *Cred           `json:"cred"`
	Endpoint    *Cred           `json:"endpoint,omitempty"`
	KeyStatus   json.RawMessage `json:"keyStatus"`
	SharedLast4 *string         `json:"sharedLast4"`
	// SignIn is the access token of the person's own pw CLI sign-in made
	// through the Studio (Platform.SignInKey), "" for none. It comes before
	// a pasted key for work on the platform.
	SignIn string `json:"-"`
}

// UserID is req.user?.id, or "".
func (v *Viewer) UserID() string {
	if v == nil || v.User == nil {
		return ""
	}
	return v.User.ID
}

// Username is req.user?.username, or "".
func (v *Viewer) Username() string {
	if v == nil || v.User == nil {
		return ""
	}
	return v.User.Username
}

// PlatformKey is the viewer's own platform key: a stored credential with
// no base URL (a provider key is chat-only), or "".
func (v *Viewer) PlatformKey() string {
	if v != nil && v.SignIn != "" {
		return v.SignIn
	}
	if v == nil || v.Cred == nil || v.Cred.BaseURL != "" {
		return ""
	}
	return v.Cred.Key
}

// EndpointCred is the person's own model endpoint, or nil. It covers
// chat only and is never sent to the platform.
func (v *Viewer) EndpointCred() *Cred {
	if v == nil {
		return nil
	}
	if v.Cred != nil && v.Cred.BaseURL != "" {
		return v.Cred
	}
	if v.Endpoint != nil && v.Endpoint.BaseURL != "" {
		return v.Endpoint
	}
	return nil
}

// KeyCred is the person's pasted ACTIVATE key, or nil.
func (v *Viewer) KeyCred() *Cred {
	if v == nil || v.Cred == nil || v.Cred.BaseURL != "" {
		return nil
	}
	return v.Cred
}

type viewerKey struct{}

// WithViewer is r carrying v as the viewer ViewerOf resolves, for a
// caller (a test, a front) that already knows who asks.
func WithViewer(r *http.Request, v *Viewer) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), viewerKey{}, v))
}

// ViewerOf resolves the viewer of r once per request: from Node through
// the bridge while Node holds personal keys, else from the Go front and
// the vault and key cookies here.
func (p *Platform) ViewerOf(r *http.Request) (*Viewer, error) {
	if v, ok := r.Context().Value(viewerKey{}).(*Viewer); ok {
		return v, nil
	}
	var v *Viewer
	if b := p.Node(); b != nil {
		v = &Viewer{}
		err := b.Do(r, http.MethodGet, BridgePrefix+"viewer", nil, v)
		var ne *app.NodeError
		switch {
		case errors.As(err, &ne) && ne.Status == http.StatusNotFound:
			// A Node server without the bridge (started without its secret):
			// what Go can read itself.
			v = p.nativeViewer(r)
		case err != nil:
			return nil, fmt.Errorf("the viewer from Node: %w", err)
		}
		// JSON null keyStatus stays nil for callers.
		if string(v.KeyStatus) == "null" {
			v.KeyStatus = nil
		}
	} else {
		v = p.nativeViewer(r)
	}
	// Where the Go server keeps the keys, its key store says what they
	// are: the cookies hydrate it, and a key not kept in the browser is
	// there too. Node, given the ACTIVATE key, does not know the endpoint
	// beside it.
	if p.OwnKeys != nil && p.Getenv("DISABLE_PERSONAL_KEYS") != "1" {
		owner := v.UserID()
		if owner == "" && !v.AuthEnabled {
			owner = SingleOwner
		}
		if owner != "" {
			key, ep := p.OwnKeys(r, owner)
			if p.Node() == nil {
				v.Cred = key
				if key == nil {
					v.Cred = ep
				}
			}
			if v.Endpoint == nil {
				v.Endpoint = ep
			}
		}
	}
	*r = *r.WithContext(context.WithValue(r.Context(), viewerKey{}, v))
	return v, nil
}

// nativeViewer is credentials.ts read here: the front's user, the single
// owner's vault entry with sign-in off, else the user's key cookie.
func (p *Platform) nativeViewer(r *http.Request) *Viewer {
	v := &Viewer{}
	if p.authEnabled != nil {
		v.AuthEnabled = p.authEnabled()
	}
	if u := auth.UserOf(r); u != nil {
		v.User = &User{ID: u.ID, Username: u.Username, Name: u.Name}
	}
	if p.Getenv("DISABLE_PERSONAL_KEYS") == "1" {
		return v
	}
	sealer := keycookie.New(p.indexBase)
	switch {
	case v.User == nil && !v.AuthEnabled:
		if e, ok := p.vaultEntry(SingleOwner); ok && !expired(e.ExpiresAt) {
			v.Cred = credOf(sealer, e)
			if e.Endpoint != nil && e.Endpoint.BaseURL != nil && !expired(e.Endpoint.ExpiresAt) && v.Cred != nil && v.Cred.BaseURL == "" {
				v.Endpoint = credOf(sealer, *e.Endpoint)
			} else if v.Cred != nil && v.Cred.BaseURL != "" {
				v.Endpoint = v.Cred
			}
		}
	case v.User != nil && v.User.ID != "":
		key, ep := sealer.Both(v.User.ID, r.Header.Get("Cookie"))
		if ep != nil {
			v.Endpoint = &Cred{Key: ep.Key, BaseURL: deref(ep.BaseURL), Endpoint: ep.Options}
			v.Cred = v.Endpoint
		}
		if key != nil {
			v.Cred = &Cred{Key: key.Key, BaseURL: deref(key.BaseURL), Endpoint: key.Options}
		}
		if v.AuthEnabled && p.SignInKey != nil {
			v.SignIn = p.SignInKey(v.User.ID)
		}
	}
	return v
}

// credOf opens a vault entry's key, or nil.
func credOf(sealer *keycookie.Sealer, e vaultEntry) *Cred {
	key, err := sealer.Decrypt(e.Blob)
	if err != nil {
		return nil
	}
	c := &Cred{Key: string(key), BaseURL: deref(e.BaseURL)}
	if e.Opts != "" && c.BaseURL != "" {
		if o, err := sealer.Decrypt(e.Opts); err == nil {
			c.Endpoint = o
		}
	}
	return c
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type vaultEntry struct {
	Blob      string  `json:"blob"`
	Last4     string  `json:"last4"`
	ExpiresAt *string `json:"expiresAt"`
	BaseURL   *string `json:"baseUrl"`
	Opts      string  `json:"opts"`
	// Endpoint is the one owner's endpoint beside the ACTIVATE key.
	Endpoint *vaultEntry `json:"endpoint"`
}

func (p *Platform) vaultEntry(sub string) (vaultEntry, bool) {
	b, err := os.ReadFile(filepath.Join(p.indexBase, "user-credentials.json"))
	if err != nil {
		return vaultEntry{}, false
	}
	var vault map[string]vaultEntry
	if json.Unmarshal(b, &vault) != nil {
		return vaultEntry{}, false
	}
	e, ok := vault[sub]
	return e, ok
}

func expired(iso *string) bool {
	if iso == nil || *iso == "" {
		return false
	}
	t, ok := ParseISO(*iso)
	return ok && t.Before(time.Now())
}

// SharedKey is resolveSharedKey: the key a user promoted for the
// deployment, while valid, or "".
func (p *Platform) SharedKey() string {
	e, ok := p.vaultEntry("__shared__")
	if !ok || expired(e.ExpiresAt) {
		return ""
	}
	key, err := keycookie.New(p.indexBase).Decrypt(e.Blob)
	if err != nil {
		return ""
	}
	return string(key)
}

// SharedKeyLast4 is getSharedKeyStatus().last4 for an active shared key.
func (p *Platform) SharedKeyLast4() string {
	e, _ := p.vaultEntry("__shared__")
	return e.Last4
}

// GatewayKey is the deployment's own credential: PW_API_KEY (or
// OPENAI_API_KEY), else the pw CLI's sign-in for the gateway's host; ""
// after Stop using the pw CLI. On a Studio of one it is the owner's own.
// On a Studio several people use it is the administrator's, and serves
// only the Studio's housekeeping that acts as no person; work for a person
// goes through KeyFor, which never gives it. A key someone shared with
// the deployment (SharedKey) is no longer used.
func (p *Platform) GatewayKey() string {
	if p.Stopped() {
		return ""
	}
	if p.apiKey != "" {
		return p.apiKey
	}
	return p.CLICredential()
}

// HasActivate says whether this computer gives the Studio an ACTIVATE
// credential: PW_API_KEY, OPENAI_API_KEY where the gateway is a platform's
// (elsewhere it is some other endpoint's key), or a pw CLI context that
// holds a sign-in, a token or a key (the one for the gateway's platform,
// else the CLI's current one). False after Stop using the pw CLI. It reads
// no token and renews nothing, so it is cheap to ask on every request.
func (p *Platform) HasActivate() bool {
	if p.Stopped() {
		return false
	}
	if p.EnvKeyIsActivate() {
		return true
	}
	s := p.ReadStore()
	holds := func(name string) bool {
		i, ok := s.identity(name)
		return ok && (i.Token != "" || i.APIKey != "" || i.OAuth != nil)
	}
	if n := p.CredentialContextName(); n != "" && holds(n) {
		return true
	}
	if n := p.ActiveContextName(); n != "" && holds(n) {
		return true
	}
	return false
}

// EnvKeyIsActivate says whether the key in the Studio's environment is an
// ACTIVATE credential: PW_API_KEY, or OPENAI_API_KEY where the gateway is
// an ACTIVATE platform's. OPENAI_API_KEY beside any other endpoint is that
// endpoint's model key. False with no key, and after Stop using the pw CLI.
func (p *Platform) EnvKeyIsActivate() bool {
	return !p.Stopped() && p.apiKey != "" && (p.Getenv("PW_API_KEY") != "" || p.PlatformGateway())
}

// EnvModelKey says whether the key in the Studio's environment is a model
// endpoint's (OPENAI_API_KEY beside an endpoint that is not an ACTIVATE
// platform's), and no ACTIVATE credential.
func (p *Platform) EnvModelKey() bool {
	return !p.Stopped() && p.apiKey != "" && !p.EnvKeyIsActivate()
}

// KeyFor is the platform key work for the viewer runs with: their own
// sign-in or key; on a Studio of one, without either, the owner's
// (GatewayKey), with own false. On a Studio several people use it is never
// the deployment's: "" without a credential of the person's own.
func (p *Platform) KeyFor(v *Viewer) (key string, own bool) {
	if k := v.PlatformKey(); k != "" {
		return k, true
	}
	if (v != nil && v.AuthEnabled) || p.AuthEnabled() {
		return "", false
	}
	return p.GatewayKey(), false
}

// PlatformAvailable is workflowsTab.ts's platformAvailable: a platform
// credential, and a gateway that is a platform's.
func (p *Platform) PlatformAvailable(v *Viewer) bool {
	if !p.PlatformGateway() {
		return false
	}
	k, _ := p.KeyFor(v)
	return k != ""
}
