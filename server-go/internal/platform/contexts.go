package platform

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// Identity is one context in the pw CLI's credential store.
type Identity struct {
	Token        string
	APIKey       string
	Server       string
	Organization string
	OAuth        *OAuth
}

// OAuth is a sign-in's renewal state.
type OAuth struct {
	ExpiresAt    string
	RefreshToken string
}

// Store is the CLI's credential file: identities in the file's key order.
type Store struct {
	Names           []string
	Identities      map[string]Identity
	CurrentIdentity string
}

type storeCache struct {
	file  string
	mtime time.Time
	size  int64
	data  *Store
	ok    bool
}

// CredentialsFile is pwCredentialsFile: PW_CREDENTIALS_DIR, as the CLI
// reads it, else ~/.config/pw, then credentials.
func (p *Platform) CredentialsFile() string {
	dir := p.Getenv("PW_CREDENTIALS_DIR")
	if dir == "" {
		dir = filepath.Join(p.home, ".config", "pw")
	}
	return filepath.Join(dir, "credentials")
}

func str(v any) string { s, _ := v.(string); return s }

func parseStore(b []byte) *Store {
	v, err := jsonv.Decode(b)
	if err != nil {
		return nil
	}
	root := jsonv.Obj(v)
	if root == nil {
		// JSON.parse of a non-object still reads as a store with nothing in it.
		return &Store{Identities: map[string]Identity{}}
	}
	s := &Store{Identities: map[string]Identity{}}
	if c, ok := root.Get("currentIdentity"); ok {
		s.CurrentIdentity = str(c)
	}
	ids := jsonv.Obj(jsonv.Path(root, "identities"))
	for _, name := range ids.Keys() {
		raw, _ := ids.Get(name)
		o := jsonv.Obj(raw)
		if o == nil {
			continue
		}
		id := Identity{
			Token: jsonv.String(jsonv.Path(o, "token")), APIKey: jsonv.String(jsonv.Path(o, "apikey")),
			Organization: jsonv.String(jsonv.Path(o, "organization")),
		}
		// Node's checks are on the raw values: `typeof server === 'string'`.
		id.Server = str(jsonv.Path(o, "server"))
		if !jsonv.Truthy(jsonv.Path(o, "token")) {
			id.Token = ""
		}
		if !jsonv.Truthy(jsonv.Path(o, "apikey")) {
			id.APIKey = ""
		}
		if !jsonv.Truthy(jsonv.Path(o, "organization")) {
			id.Organization = ""
		}
		if oa := jsonv.Path(o, "oauth"); jsonv.Truthy(oa) {
			id.OAuth = &OAuth{ExpiresAt: str(jsonv.Path(oa, "expiresAt")), RefreshToken: str(jsonv.Path(oa, "refreshToken"))}
		}
		s.Names = append(s.Names, name)
		s.Identities[name] = id
	}
	return s
}

// ReadStore is readStore: the CLI's credential store, read again when the
// file's modification time moves, or nil when there is none.
func (p *Platform) ReadStore() *Store {
	file := p.CredentialsFile()
	st, err := os.Stat(file)
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.store
	if err != nil {
		*c = storeCache{file: file, ok: true}
		return nil
	}
	if c.ok && c.file == file && c.mtime.Equal(st.ModTime()) && c.size == st.Size() {
		return c.data
	}
	var data *Store
	if b, err := os.ReadFile(file); err == nil {
		data = parseStore(b)
	}
	*c = storeCache{file: file, mtime: st.ModTime(), size: st.Size(), data: data, ok: true}
	return data
}

func (s *Store) identity(name string) (Identity, bool) {
	if s == nil {
		return Identity{}, false
	}
	i, ok := s.Identities[name]
	return i, ok
}

// JWTExpiry is jwtExpiry: an ISO time from a JWT's exp, or "".
func JWTExpiry(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	seg := strings.NewReplacer("-", "+", "_", "/").Replace(parts[1])
	b, err := base64.StdEncoding.DecodeString(padB64(seg))
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(seg, "=")); err != nil {
			return ""
		}
	}
	var payload map[string]any
	if json.Unmarshal(b, &payload) != nil {
		return ""
	}
	exp, ok := payload["exp"].(float64)
	if !ok {
		return ""
	}
	return ISO(time.UnixMilli(int64(exp * 1000)))
}

func padB64(s string) string {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return s
}

// ISO is Date.prototype.toISOString.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// NowISO is new Date().toISOString().
func NowISO() string { return ISO(time.Now()) }

// ParseISO is Date.parse for the ISO strings the Studio writes; false when
// it is not one.
func ParseISO(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func kindOf(i Identity) string {
	switch {
	case i.OAuth != nil:
		return "sign-in"
	case i.APIKey != "":
		return "api-key"
	case i.Token != "":
		return "token"
	}
	return ""
}

// ListContexts is listCliContexts: every context, without key material,
// in the file's order.
func (p *Platform) ListContexts() []*jsonv.Object {
	s := p.ReadStore()
	out := []*jsonv.Object{}
	if s == nil {
		return out
	}
	for _, name := range s.Names {
		i := s.Identities[name]
		if i.Server == "" {
			continue
		}
		kind := kindOf(i)
		expiresAt := ""
		if kind == "token" {
			expiresAt = JWTExpiry(i.Token)
		}
		expired := false
		if t, ok := ParseISO(expiresAt); ok && expiresAt != "" {
			expired = t.Before(time.Now())
		}
		out = append(out, jsonv.NewObject(
			"name", name, "server", i.Server, "organization", nullable(i.Organization),
			"kind", nullable(kind), "expiresAt", nullable(expiresAt), "expired", expired,
			"current", s.CurrentIdentity == name, "renewError", nullable(p.renewErrorMessage(name)),
		))
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

/* ---- the choice ---- */

func (p *Platform) choiceFile() string { return filepath.Join(p.indexBase, "access.json") }

func (p *Platform) loadChoice() string {
	b, err := os.ReadFile(p.choiceFile())
	if err != nil {
		return ""
	}
	var v struct {
		PWContext any `json:"pwContext"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return str(v.PWContext)
}

// ChosenContext is chosenContext: the context chosen in Model access,
// while the CLI still has it.
func (p *Platform) ChosenContext() string {
	p.rereadChoice()
	p.mu.Lock()
	c := p.chosen
	p.mu.Unlock()
	if c == "" {
		return ""
	}
	if _, ok := p.ReadStore().identity(c); ok {
		return c
	}
	return ""
}

// ActiveContextName is activeContextName: the chosen context, else the one
// the server started with, else the CLI's current one.
func (p *Platform) ActiveContextName() string {
	if c := p.ChosenContext(); c != "" {
		return c
	}
	if p.startupContext != "" {
		return p.startupContext
	}
	if s := p.ReadStore(); s != nil {
		return s.CurrentIdentity
	}
	return ""
}

// StartupContext is PW_CONTEXT as the server started with it.
func (p *Platform) StartupContext() string { return p.startupContext }

// ChooseContext is chooseContext: a context by name, or "" to follow the
// CLI's current one. It writes access.json as Node does.
func (p *Platform) ChooseContext(name string) error {
	if name != "" {
		if _, ok := p.ReadStore().identity(name); !ok {
			return errors.New("the pw CLI has no context named " + name)
		}
	}
	p.mu.Lock()
	p.chosen = name
	p.mu.Unlock()
	if err := os.MkdirAll(p.indexBase, 0o755); err != nil {
		return err
	}
	// JSON.stringify({ pwContext: name ?? undefined }, null, 1)
	body := "{}"
	if name != "" {
		body = "{\n \"pwContext\": " + string(jsonv.MustMarshal(name)) + "\n}"
	}
	if err := os.WriteFile(p.choiceFile(), []byte(body), 0o644); err != nil {
		return err
	}
	p.syncGateway()
	p.reach.reset()
	if err := p.AccessChanged(false); err != nil {
		p.log.Warn("access choice: Node did not answer", "err", err.Error())
	}
	return nil
}

/* ---- the gateway ---- */

// GatewayFor is gatewayFor: the model gateway on a platform host.
func GatewayFor(server string) string { return "https://" + server + "/api/openai/v1" }

const defaultGateway = "https://activate.parallel.works/api/openai/v1"

func (p *Platform) initGateway() {
	v, ok := p.lookupFirst("PW_GATEWAY_URL", "OPENAI_BASE_URL")
	p.gatewayFromEnv = ok && v != ""
	base := v
	if !ok {
		base = p.cliContextGateway()
		if base == "" {
			base = defaultGateway
		}
	}
	p.gatewayBase = strings.TrimSuffix(base, "/")
}

// cliContextGateway is config.ts's: the gateway of the CLI's active context
// at start (PW_CONTEXT, then currentIdentity).
func (p *Platform) cliContextGateway() string {
	b, err := os.ReadFile(p.CredentialsFile())
	if err != nil {
		return ""
	}
	s := parseStore(b)
	if s == nil {
		return ""
	}
	name := p.Getenv("PW_CONTEXT")
	if name == "" {
		name = s.CurrentIdentity
	}
	if i, ok := s.identity(name); ok && i.Server != "" {
		return GatewayFor(i.Server)
	}
	return ""
}

// GatewayFromEnv is GATEWAY_FROM_ENV.
func (p *Platform) GatewayFromEnv() bool { return p.gatewayFromEnv }

// GatewayBase is GATEWAY_BASE, after syncGateway.
func (p *Platform) GatewayBase() string {
	p.syncGateway()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gatewayBase
}

// APIKeyEnv is PW_API_KEY ?? OPENAI_API_KEY ?? ”.
func (p *Platform) APIKeyEnv() string { return p.apiKey }

func (p *Platform) gatewayHost() string {
	p.mu.Lock()
	base := p.gatewayBase
	p.mu.Unlock()
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		return ""
	}
	return u.Host
}

// syncGateway moves the gateway to the active context's platform, unless
// the environment fixed it.
func (p *Platform) syncGateway() {
	if p.gatewayFromEnv {
		return
	}
	name := p.ActiveContextName()
	if name == "" {
		return
	}
	i, ok := p.ReadStore().identity(name)
	if !ok || i.Server == "" || p.gatewayHost() == i.Server {
		return
	}
	p.mu.Lock()
	p.gatewayBase = strings.TrimSuffix(GatewayFor(i.Server), "/")
	p.mu.Unlock()
}

// Origin is new URL(GATEWAY_BASE).origin, or "" when it is not a URL.
func (p *Platform) Origin() string {
	u, err := url.Parse(p.GatewayBase())
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Host is new URL(GATEWAY_BASE).host, or "".
func (p *Platform) Host() string {
	u, err := url.Parse(p.GatewayBase())
	if err != nil || u.Scheme == "" {
		return ""
	}
	return u.Host
}

// PlatformGateway is sessions.ts's platformGateway: the gateway is a
// platform's, so its origin answers the platform API.
func (p *Platform) PlatformGateway() bool {
	if p.Origin() == "" {
		return false
	}
	return !p.gatewayFromEnv || strings.HasSuffix(p.GatewayBase(), "/api/openai/v1")
}

// CredentialContextName is credentialContextName: the context whose
// credential the gateway gets.
func (p *Platform) CredentialContextName() string {
	p.syncGateway()
	s := p.ReadStore()
	if s == nil {
		return ""
	}
	host := p.gatewayHost()
	if active := p.ActiveContextName(); active != "" {
		if i, ok := s.identity(active); ok && i.Server == host {
			return active
		}
	}
	for _, name := range s.Names {
		if s.Identities[name].Server == host {
			return name
		}
	}
	return ""
}
