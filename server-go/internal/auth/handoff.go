package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

// The identity hand-off to Node. The Go front verifies who a request is
// (the session proxy's JWT, or an OIDC session) and passes the result to
// the Node server behind it as a short-lived JWT of its own, signed with
// an Ed25519 key made at start and never written anywhere. Node verifies
// it with its unchanged auth.ts: the Go server starts Node with
// AUTH_JWKS_URL at the key set served here on a loopback port,
// AUTH_HEADER=x-studio-identity, and the issuer and audience below. A
// client cannot forge the header (it is removed from every request
// before the front sets it), and the platform's own token never reaches
// Node, which no longer needs to fetch the platform's keys.
const (
	// NodeHeader carries the hand-off token to Node.
	NodeHeader = "x-studio-identity"
	// HandoffIssuer and HandoffAudience are what Node checks.
	HandoffIssuer   = "studio-go-front"
	HandoffAudience = "studio-node"
	handoffLife     = 5 * time.Minute
)

// Handoff signs identity tokens for Node and serves their key set.
type Handoff struct {
	key ed25519.PrivateKey
	kid string
	// JWKSURL is the loopback URL of the key set.
	JWKSURL string
	srv     *http.Server
	now     func() time.Time
}

// StartHandoff makes a key and serves its key set on addr (a loopback
// address; port 0 picks a free one).
func StartHandoff(addr string) (*Handoff, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("STUDIO_IDENTITY_ADDR must be a loopback address")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	kidBytes := make([]byte, 9)
	rand.Read(kidBytes)
	h := &Handoff{key: priv, kid: "front-" + b64.EncodeToString(kidBytes), now: time.Now}
	set, _ := json.Marshal(map[string]any{"keys": []JWK{{Kty: "OKP", Crv: "Ed25519", X: b64.EncodeToString(pub), Kid: h.kid, Use: "sig", Alg: "EdDSA"}}})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(set)
	})
	h.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go h.srv.Serve(ln)
	h.JWKSURL = "http://" + ln.Addr().String() + "/jwks.json"
	return h, nil
}

// Close stops serving the key set.
func (h *Handoff) Close() error { return h.srv.Close() }

// Token is u as Node's auth.ts reads a token: sub is the user's ID,
// preferred_username the username, name the display name.
func (h *Handoff) Token(u *User) (string, error) {
	now := h.now().Unix()
	c := map[string]any{
		"iss": HandoffIssuer, "aud": HandoffAudience, "iat": now, "exp": now + int64(handoffLife/time.Second),
		"sub": u.ID, "preferred_username": u.Username, "studio_source": u.Source,
	}
	if u.Name != "" {
		c["name"] = u.Name
	}
	return SignEdDSA(h.key, h.kid, c)
}

// NodeEnv is the environment Node is started with so that it verifies
// these tokens and nothing else. required passes the front's sign-in
// requirement on, so Node still turns away an unsigned /api/mcp call
// whose bearer is not a mission token.
func (h *Handoff) NodeEnv(required bool) map[string]string {
	req := ""
	if required {
		req = "1"
	}
	return map[string]string{
		"AUTH_JWKS_URL": h.JWKSURL,
		"AUTH_HEADER":   NodeHeader,
		"AUTH_ISSUER":   HandoffIssuer,
		"AUTH_AUDIENCE": HandoffAudience,
		"AUTH_REQUIRED": req,
	}
}
