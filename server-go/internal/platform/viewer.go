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

// BridgeSecret is the one bridge secret (internal/bridge).
func BridgeSecret() string { return bridge.Secret() }

// NewNodeBridge wraps a's Node, or is nil without one.
func NewNodeBridge(n *app.Node) *NodeBridge {
	if n == nil {
		return nil
	}
	return &NodeBridge{Node: n, client: &http.Client{Timeout: 30 * time.Second}}
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

// Viewer is who a request is and the personal credential it carries.
type Viewer struct {
	AuthEnabled bool            `json:"authEnabled"`
	User        *User           `json:"user"`
	Cred        *Cred           `json:"cred"`
	KeyStatus   json.RawMessage `json:"keyStatus"`
	SharedLast4 *string         `json:"sharedLast4"`
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
	if v == nil || v.Cred == nil || v.Cred.BaseURL != "" {
		return ""
	}
	return v.Cred.Key
}

type viewerKey struct{}

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
			if key, err := sealer.Decrypt(e.Blob); err == nil {
				v.Cred = &Cred{Key: string(key), BaseURL: deref(e.BaseURL)}
				if e.Opts != "" && v.Cred.BaseURL != "" {
					if o, err := sealer.Decrypt(e.Opts); err == nil {
						v.Cred.Endpoint = o
					}
				}
			}
		}
	case v.User != nil && v.User.ID != "":
		if k, ok := sealer.FromCookies(v.User.ID, r.Header.Get("Cookie")); ok {
			v.Cred = &Cred{Key: k.Key, BaseURL: deref(k.BaseURL), Endpoint: k.Options}
		}
	}
	return v
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

// GatewayKey is gatewayKey(): the shared key, else PW_API_KEY, else the
// pw CLI's credential.
func (p *Platform) GatewayKey() string {
	if k := p.SharedKey(); k != "" {
		return k
	}
	if p.apiKey != "" {
		return p.apiKey
	}
	return p.CLICredential()
}

// KeyFor is platformKeyFor / sessionKeyFor: the viewer's own platform key,
// else the deployment's; own says which.
func (p *Platform) KeyFor(v *Viewer) (key string, own bool) {
	if k := v.PlatformKey(); k != "" {
		return k, true
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
