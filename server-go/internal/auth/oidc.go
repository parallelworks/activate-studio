package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// OpenID Connect sign-in: the Studio as a relying party of any provider
// that publishes a discovery document, ACTIVATE's among them. The flow is
// the authorization code flow with PKCE (S256), state bound to the browser
// by a short-lived cookie, and a nonce; the ID token's signature, issuer,
// audience and expiry are checked, and the RFC 9207 iss parameter before
// the code is redeemed. Tokens stay on the server (SessionStore); the
// browser holds an HttpOnly, SameSite=Lax session cookie. With
// offline_access the refresh token is rotated once for concurrent
// requests, and a failed refresh ends the session. The tokens establish
// who the person is and nothing more: they are never sent to the platform
// API or a model gateway.
//
// Differences from the oidc-signin branch, by decision: sessions persist
// across restarts (encrypted, sessions.go), and sign-out ends the Studio's
// session only unless OIDC_END_SESSION=1 asks for the provider's
// end_session as well.

const (
	SessionCookie  = "studio_session"
	txCookiePrefix = "studio_tx_"
	txTTL          = 10 * time.Minute
	fetchTimeout   = 10 * time.Second
	discoveryTTL   = time.Hour
	maxTxs         = 10000
	LoginPath      = "/api/auth/login"
	LogoutPath     = "/api/auth/logout"
	CallbackPath   = "/api/auth/callback"
)

type discoveryDoc struct {
	Issuer                          string   `json:"issuer"`
	AuthorizationEndpoint           string   `json:"authorization_endpoint"`
	TokenEndpoint                   string   `json:"token_endpoint"`
	JWKSURI                         string   `json:"jwks_uri"`
	UserinfoEndpoint                string   `json:"userinfo_endpoint"`
	EndSessionEndpoint              string   `json:"end_session_endpoint"`
	RevocationEndpoint              string   `json:"revocation_endpoint"`
	TokenEndpointAuthMethods        []string `json:"token_endpoint_auth_methods_supported"`
	IDTokenSigningAlgs              []string `json:"id_token_signing_alg_values_supported"`
	CodeChallengeMethods            []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParam   bool     `json:"authorization_response_iss_parameter_supported"`
	codeChallengeMethodsPresent     bool
	idTokenSigningAlgsPresent       bool
	tokenEndpointAuthMethodsPresent bool
}

type tx struct {
	nonce, verifier, returnTo, redirectURI string
	expiresAt                              time.Time
}

// OIDC is the relying party.
type OIDC struct {
	cfg    config.OIDC
	auth   config.Auth
	log    *slog.Logger
	client *http.Client
	store  *SessionStore
	now    func() time.Time

	mu         sync.Mutex
	disc       *discoveryDoc
	discAt     time.Time
	jwks       *RemoteJWKS
	txs        map[string]tx
	refreshing map[string]chan struct{}
}

// NewOIDC is the relying party for cfg, its sessions in store.
func NewOIDC(cfg config.OIDC, a config.Auth, store *SessionStore, log *slog.Logger) *OIDC {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &OIDC{cfg: cfg, auth: a, log: log, store: store, now: time.Now,
		client: &http.Client{Timeout: fetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		txs:    map[string]tx{}, refreshing: map[string]chan struct{}{}}
}

// Store is the relying party's session store.
func (o *OIDC) Store() *SessionStore { return o.store }

var loopback = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// safeURL is https, or http on loopback only (a provider on the same machine).
func safeURL(u, what string) (*url.URL, error) {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return nil, fmt.Errorf("%s is not a URL: %s", what, u)
	}
	if p.Scheme == "https" || (p.Scheme == "http" && loopback[p.Hostname()]) {
		return p, nil
	}
	return nil, fmt.Errorf("%s must use https: %s", what, u)
}

func (o *OIDC) discovery(ctx context.Context) (*discoveryDoc, *RemoteJWKS, error) {
	o.mu.Lock()
	if o.disc != nil && o.now().Sub(o.discAt) < discoveryTTL {
		d, j := o.disc, o.jwks
		o.mu.Unlock()
		return d, j, nil
	}
	o.mu.Unlock()
	base, err := safeURL(o.cfg.Issuer, "OIDC_ISSUER")
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimRight(base.String(), "/") + "/.well-known/openid-configuration"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	res, err := o.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("OIDC discovery answered %d at %s", res.StatusCode, u)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var d discoveryDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, nil, errors.New("OIDC discovery is not JSON")
	}
	var present map[string]json.RawMessage
	json.Unmarshal(raw, &present)
	_, d.codeChallengeMethodsPresent = present["code_challenge_methods_supported"]
	_, d.idTokenSigningAlgsPresent = present["id_token_signing_alg_values_supported"]
	_, d.tokenEndpointAuthMethodsPresent = present["token_endpoint_auth_methods_supported"]
	// OIDC Discovery 4.3: the document's issuer must be the one configured.
	if d.Issuer != o.cfg.Issuer {
		return nil, nil, fmt.Errorf("OIDC discovery issuer %s does not match OIDC_ISSUER %s", d.Issuer, o.cfg.Issuer)
	}
	for k, v := range map[string]string{"authorization_endpoint": d.AuthorizationEndpoint, "token_endpoint": d.TokenEndpoint, "jwks_uri": d.JWKSURI} {
		if v == "" {
			return nil, nil, fmt.Errorf("OIDC discovery has no %s", k)
		}
		if _, err := safeURL(v, k); err != nil {
			return nil, nil, err
		}
	}
	for k, v := range map[string]string{"userinfo_endpoint": d.UserinfoEndpoint, "end_session_endpoint": d.EndSessionEndpoint, "revocation_endpoint": d.RevocationEndpoint} {
		if v != "" {
			if _, err := safeURL(v, k); err != nil {
				return nil, nil, err
			}
		}
	}
	if d.codeChallengeMethodsPresent && !slices.Contains(d.CodeChallengeMethods, "S256") {
		return nil, nil, errors.New("the OIDC provider does not support PKCE with S256")
	}
	j := NewRemoteJWKS(d.JWKSURI)
	j.Client = o.client
	o.mu.Lock()
	o.disc, o.discAt, o.jwks = &d, o.now(), j
	o.mu.Unlock()
	return &d, j, nil
}

// ResetDiscovery forgets the cached discovery document and keys (tests).
func (o *OIDC) ResetDiscovery() {
	o.mu.Lock()
	o.disc = nil
	o.mu.Unlock()
}

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return b64.EncodeToString(b)
}

func hashID(s string) string {
	h := sha256.Sum256([]byte(s))
	return b64.EncodeToString(h[:])
}

// ParseCookies is oidc.ts's parseCookies: name=value pairs split on ';',
// later names winning.
func ParseCookies(header string) map[string]string {
	jar := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		if eq := strings.Index(part, "="); eq > 0 {
			jar[strings.TrimSpace(part[:eq])] = strings.TrimSpace(part[eq+1:])
		}
	}
	return jar
}

func firstHeader(v string) string {
	return strings.TrimSpace(strings.Split(v, ",")[0])
}

// redirectURIFor is the configured redirect URI, or this request's origin.
// The provider matches it exactly against the registration, so a forged
// Host header gets a refusal from the provider and nothing else.
func (o *OIDC) redirectURIFor(r *http.Request) string {
	if o.cfg.RedirectURI != "" {
		return o.cfg.RedirectURI
	}
	proto := firstHeader(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := firstHeader(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = firstHeader(r.Host)
	}
	return proto + "://" + host + CallbackPath
}

func cookieAttrs(secure bool, path string) string {
	s := "Path=" + path + "; HttpOnly; SameSite=Lax"
	if secure {
		s += "; Secure"
	}
	return s
}

var crlf = regexp.MustCompile(`[\r\n]`)

// SafeReturnTo is a path on this origin only: never //host or /\host,
// which browsers read as another site, and never the auth routes.
func SafeReturnTo(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, `/\`) || crlf.MatchString(s) {
		return "/"
	}
	if strings.HasPrefix(s, "/api/auth/") {
		return "/"
	}
	return s
}

func failurePage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Sign-in did not complete</title></head>`+
		`<body style="font-family:system-ui,sans-serif;max-width:40em;margin:4em auto;line-height:1.5">`+
		`<h1 style="font-size:1.3em">Sign-in did not complete</h1><p>%s</p>`+
		`<p><a href="%s">Sign in again</a></p></body></html>`, html.EscapeString(message), LoginPath)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type tokenResponse struct {
	AccessToken      string   `json:"access_token"`
	TokenType        string   `json:"token_type"`
	IDToken          string   `json:"id_token"`
	RefreshToken     string   `json:"refresh_token"`
	ExpiresIn        *float64 `json:"expires_in"`
	Error            string   `json:"error"`
	ErrorDescription string   `json:"error_description"`
}

// clientAuth is one client authentication method per request (RFC 6749
// 2.3): HTTP Basic where the provider offers it (the default when it says
// nothing), the secret in the body otherwise, the client ID alone for a
// public client.
func (o *OIDC) clientAuth(d *discoveryDoc, form url.Values, h http.Header) {
	if o.cfg.ClientSecret == "" {
		form.Set("client_id", o.cfg.ClientID)
		return
	}
	methods := d.TokenEndpointAuthMethods
	if !d.tokenEndpointAuthMethodsPresent {
		methods = []string{"client_secret_basic"}
	}
	if slices.Contains(methods, "client_secret_basic") {
		enc := func(s string) string { return encodeURIComponent(s) }
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(enc(o.cfg.ClientID)+":"+enc(o.cfg.ClientSecret))))
	} else {
		form.Set("client_id", o.cfg.ClientID)
		form.Set("client_secret", o.cfg.ClientSecret)
	}
}

// encodeURIComponent is JavaScript's.
func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (o *OIDC) tokenRequest(ctx context.Context, d *discoveryDoc, params url.Values) (*tokenResponse, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Accept", "application/json")
	o.clientAuth(d, params, h)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(params.Encode()))
	req.Header = h
	res, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var t tokenResponse
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t)
	if res.StatusCode != http.StatusOK || t.Error != "" {
		msg := fmt.Sprintf("token endpoint answered %d", res.StatusCode)
		if t.Error != "" {
			msg += " " + t.Error
		}
		if t.ErrorDescription != "" {
			msg += ": " + t.ErrorDescription
		}
		return nil, errors.New(msg)
	}
	return &t, nil
}

func (o *OIDC) verifyIDToken(ctx context.Context, d *discoveryDoc, j *RemoteJWKS, idToken string, nonce *string) (Claims, error) {
	var algs []string
	src := d.IDTokenSigningAlgs
	if !d.idTokenSigningAlgsPresent {
		src = []string{"RS256"}
	}
	for _, a := range src {
		if a != "" && a != "none" {
			algs = append(algs, a)
		}
	}
	if len(algs) == 0 {
		algs = []string{"RS256"}
	}
	c, err := Verify(idToken, func(alg, kid string) ([]cryptoKey, error) { return j.Keys(ctx, alg, kid) }, VerifyOptions{
		Issuer: o.cfg.Issuer, Audience: o.cfg.ClientID, Algs: algs,
		ClockTolerance: 30 * time.Second, Required: []string{"sub", "exp", "iat"}, Now: o.now,
	})
	if err != nil {
		return nil, err
	}
	if aud, ok := c["aud"].([]any); ok && len(aud) > 1 {
		if azp, _ := c["azp"].(string); azp != o.cfg.ClientID {
			return nil, errors.New("ID token azp does not name this client")
		}
	}
	if nonce != nil {
		if n, _ := c["nonce"].(string); n != *nonce {
			return nil, errors.New("ID token nonce does not match the sign-in request")
		}
	}
	return c, nil
}

var errSubject = errors.New("userinfo subject does not match the ID token")

func (o *OIDC) userinfo(ctx context.Context, d *discoveryDoc, access, sub string) (map[string]any, error) {
	if d.UserinfoEndpoint == "" {
		return nil, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.UserinfoEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	res, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "json") {
		return nil, nil // a signed userinfo response is not read
	}
	var info map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info); err != nil {
		return nil, nil
	}
	// OIDC Core 5.3.2: the userinfo subject must be the ID token's.
	if s, _ := info["sub"].(string); s != sub {
		return nil, errSubject
	}
	return info, nil
}

func (o *OIDC) userFrom(c Claims) *User {
	sub := jsString(c["sub"])
	username, ok := StringClaim(c, o.cfg.UsernameClaim)
	if !ok {
		if username, ok = StringClaim(c, "preferred_username"); !ok {
			if username, ok = StringClaim(c, "email"); !ok {
				username = sub
			}
		}
	}
	u := &User{ID: sub, Username: strings.TrimPrefix(username, "user:"), Source: "oidc", Claims: c}
	if n, ok := c["name"].(string); ok {
		u.Name = n
	}
	mapIdentity(u, c, o.auth)
	return u
}

// mergeClaims is the ID token's claims over what userinfo adds.
func mergeClaims(id Claims, info map[string]any) Claims {
	out := Claims{}
	for k, v := range info {
		out[k] = v
	}
	for k, v := range id {
		out[k] = v
	}
	return out
}

// refreshSkew is when to refresh before the access token expires: 30 s,
// or half its life when that is shorter.
func refreshSkew(lifeSec float64) time.Duration {
	return min(30*time.Second, time.Duration(max(0, lifeSec)*float64(time.Second))/2)
}

func lifeOf(t *tokenResponse) float64 {
	if t.ExpiresIn != nil {
		return *t.ExpiresIn
	}
	return 3600
}

func (o *OIDC) refresh(ctx context.Context, s *session) bool {
	if s.RefreshToken == "" {
		return false
	}
	d, j, err := o.discovery(ctx)
	if err != nil {
		return false
	}
	t, err := o.tokenRequest(ctx, d, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}})
	if err != nil || t.AccessToken == "" {
		return false
	}
	if t.IDToken != "" {
		p, err := o.verifyIDToken(ctx, d, j, t.IDToken, nil)
		// OIDC Core 12.2: the same person, or the session ends.
		if err != nil || jsString(p["sub"]) != s.User.ID {
			return false
		}
		s.User = o.userFrom(mergeClaims(p, nil))
		s.IDToken = t.IDToken
	}
	s.AccessToken = t.AccessToken
	life := lifeOf(t)
	s.AccessExpiresAt = o.now().Add(time.Duration(life*float64(time.Second)) - refreshSkew(life)).UnixMilli()
	// Rotation: each refresh token is single use; keep the old one only
	// when the provider did not issue a replacement.
	if t.RefreshToken != "" {
		s.RefreshToken = t.RefreshToken
	}
	return true
}

func (o *OIDC) sessionKey(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		if sid := ParseCookies(r.Header.Get("Cookie"))[SessionCookie]; sid != "" {
			return hashID(sid)
		}
		return ""
	}
	if c.Value == "" {
		return ""
	}
	return hashID(c.Value)
}

// SessionUser is the signed-in person for the request's session cookie,
// refreshing the provider's tokens first when they are due. Concurrent
// requests share one refresh: a refresh token is single use, and
// presenting a used one is treated by providers as theft and ends the
// whole chain.
func (o *OIDC) SessionUser(r *http.Request) *User {
	key := o.sessionKey(r)
	if key == "" {
		return nil
	}
	for {
		s, ok := o.store.get(key)
		if !ok {
			return nil
		}
		now := o.now().UnixMilli()
		if now > s.ExpiresAt {
			o.store.remove(key)
			return nil
		}
		if s.RefreshToken == "" || now < s.AccessExpiresAt {
			return s.User
		}
		o.mu.Lock()
		if ch, busy := o.refreshing[key]; busy {
			o.mu.Unlock()
			select {
			case <-ch:
				continue // read the refreshed session, or find it gone
			case <-r.Context().Done():
				return nil
			}
		}
		ch := make(chan struct{})
		o.refreshing[key] = ch
		o.mu.Unlock()
		// Not the request's context: a client that goes away must not leave
		// a rotated refresh token unrecorded.
		ctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
		ok = o.refresh(ctx, s)
		cancel()
		if ok {
			o.store.put(key, s)
		} else {
			o.store.remove(key)
		}
		o.mu.Lock()
		delete(o.refreshing, key)
		close(ch)
		o.mu.Unlock()
		if !ok {
			return nil
		}
		return s.User
	}
}

func (o *OIDC) pruneTxs() {
	now := o.now()
	for k, t := range o.txs {
		if t.expiresAt.Before(now) {
			delete(o.txs, k)
		}
	}
}

// Prune drops expired sign-ins in progress and sessions; the server runs
// it every ten minutes.
func (o *OIDC) Prune() {
	o.mu.Lock()
	o.pruneTxs()
	o.mu.Unlock()
	o.store.prune()
}

// Login starts a sign-in: GET /api/auth/login?returnTo=<path>.
func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	d, _, err := o.discovery(r.Context())
	if err != nil {
		o.log.Error("OIDC discovery failed", "err", err.Error())
		failurePage(w, http.StatusBadGateway, "The identity provider could not be reached or its configuration was not usable. The server log has the detail.")
		return
	}
	o.mu.Lock()
	if len(o.txs) >= maxTxs {
		o.pruneTxs()
	}
	if len(o.txs) >= maxTxs {
		o.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many sign-ins in progress"})
		return
	}
	state, nonce, verifier := random(32), random(32), random(48)
	redirectURI := o.redirectURIFor(r)
	o.txs[state] = tx{nonce: nonce, verifier: verifier, redirectURI: redirectURI, returnTo: SafeReturnTo(r.URL.Query().Get("returnTo")), expiresAt: o.now().Add(txTTL)}
	o.mu.Unlock()
	u, _ := url.Parse(d.AuthorizationEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", o.cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", o.cfg.Scopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	ch := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", b64.EncodeToString(ch[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	// The browser that started the sign-in is the one that may finish it.
	w.Header().Add("Set-Cookie", fmt.Sprintf("%s%s=1; Max-Age=%d; %s", txCookiePrefix, state, int(txTTL/time.Second), cookieAttrs(strings.HasPrefix(redirectURI, "https:"), CallbackPath)))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// Callback finishes a sign-in: GET /api/auth/callback.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	o.mu.Lock()
	t, found := o.txs[state]
	if state != "" {
		delete(o.txs, state) // single use, whatever happens next
	}
	o.mu.Unlock()
	jar := ParseCookies(r.Header.Get("Cookie"))
	clearTx := fmt.Sprintf("%s%s=; Max-Age=0; %s", txCookiePrefix, state, cookieAttrs(found && strings.HasPrefix(t.redirectURI, "https:"), CallbackPath))
	if state != "" {
		w.Header().Add("Set-Cookie", clearTx)
	}
	if state == "" || !found || t.expiresAt.Before(o.now()) || jar[txCookiePrefix+state] != "1" {
		failurePage(w, http.StatusBadRequest, "This sign-in was not started from this browser, or it took longer than ten minutes.")
		return
	}
	ctx := r.Context()
	d, j, err := o.discovery(ctx)
	if err != nil {
		o.log.Error("OIDC discovery failed", "err", err.Error())
		failurePage(w, http.StatusBadGateway, "The identity provider could not be reached.")
		return
	}
	// RFC 9207: the response names its issuer; check it before using the code.
	if iss, has := q["iss"]; has {
		if iss[0] != o.cfg.Issuer {
			failurePage(w, http.StatusBadRequest, "The response did not come from the configured identity provider.")
			return
		}
	} else if d.AuthorizationResponseIssParam {
		failurePage(w, http.StatusBadRequest, "The response did not come from the configured identity provider.")
		return
	}
	if e := q.Get("error"); e != "" {
		desc := ""
		if v, has := q["error_description"]; has {
			desc = ": " + cut(v[0], 200)
		}
		failurePage(w, http.StatusBadRequest, fmt.Sprintf("The identity provider refused the sign-in (%s%s).", cut(e, 80), desc))
		return
	}
	code := q.Get("code")
	if code == "" {
		failurePage(w, http.StatusBadRequest, "The identity provider returned no authorization code.")
		return
	}
	tok, user, err := o.redeem(ctx, d, j, code, t)
	if err != nil {
		o.log.Warn("OIDC sign-in failed", "err", err.Error())
		failurePage(w, http.StatusUnauthorized, "The identity provider's answer could not be verified. The server log has the detail.")
		return
	}
	// A fresh session ID on every sign-in; any session this browser had ends.
	if old := o.sessionKey(r); old != "" {
		o.store.remove(old)
	}
	sid := random(32)
	life := lifeOf(tok)
	now := o.now()
	o.store.put(hashID(sid), &session{
		User: user, IDToken: tok.IDToken, AccessToken: tok.AccessToken,
		AccessExpiresAt: now.Add(time.Duration(life*float64(time.Second)) - refreshSkew(life)).UnixMilli(),
		RefreshToken:    tok.RefreshToken,
		ExpiresAt:       now.Add(time.Duration(o.cfg.MaxAgeSec) * time.Second).UnixMilli(),
	})
	secure := strings.HasPrefix(t.redirectURI, "https:")
	w.Header().Del("Set-Cookie")
	w.Header().Add("Set-Cookie", clearTx)
	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%s; Max-Age=%d; %s", SessionCookie, sid, o.cfg.MaxAgeSec, cookieAttrs(secure, "/")))
	w.Header().Set("Cache-Control", "no-store")
	o.log.Info("OIDC sign-in", "user", user.Username)
	w.Header().Set("Location", t.returnTo)
	w.WriteHeader(http.StatusSeeOther)
}

func (o *OIDC) redeem(ctx context.Context, d *discoveryDoc, j *RemoteJWKS, code string, t tx) (*tokenResponse, *User, error) {
	tok, err := o.tokenRequest(ctx, d, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {t.redirectURI}, "code_verifier": {t.verifier}})
	if err != nil {
		return nil, nil, err
	}
	if tok.IDToken == "" || tok.AccessToken == "" {
		return nil, nil, errors.New("token response has no ID token or access token")
	}
	if tok.TokenType != "" && !strings.EqualFold(tok.TokenType, "bearer") {
		return nil, nil, fmt.Errorf("unexpected token type %s", tok.TokenType)
	}
	id, err := o.verifyIDToken(ctx, d, j, tok.IDToken, &t.nonce)
	if err != nil {
		return nil, nil, err
	}
	info, err := o.userinfo(ctx, d, tok.AccessToken, jsString(id["sub"]))
	if errors.Is(err, errSubject) {
		return nil, nil, err
	}
	if err != nil {
		o.log.Warn("OIDC userinfo failed; using the ID token claims", "err", err.Error())
		info = nil
	}
	return tok, o.userFrom(mergeClaims(id, info)), nil
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Logout ends the Studio's session (GET or POST /api/auth/logout),
// revokes the tokens it held, and, with OIDC_END_SESSION=1, sends the
// browser to the provider's end_session endpoint too.
func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	key := o.sessionKey(r)
	var s *session
	if key != "" {
		s, _ = o.store.get(key)
		o.store.remove(key)
	}
	secure := strings.HasPrefix(o.redirectURIFor(r), "https:")
	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=; Max-Age=0; %s", SessionCookie, cookieAttrs(secure, "/")))
	w.Header().Set("Cache-Control", "no-store")
	d, _, err := o.discovery(r.Context())
	if err != nil {
		d = nil // sign out locally regardless
	}
	// Revoke what the session held; revoking a refresh token ends its chain.
	if s != nil && d != nil && d.RevocationEndpoint != "" {
		token, hint := s.AccessToken, "access_token"
		if s.RefreshToken != "" {
			token, hint = s.RefreshToken, "refresh_token"
		}
		form := url.Values{"token": {token}, "token_type_hint": {hint}}
		h := http.Header{}
		h.Set("Content-Type", "application/x-www-form-urlencoded")
		o.clientAuth(d, form, h)
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.RevocationEndpoint, strings.NewReader(form.Encode()))
		req.Header = h
		if res, err := o.client.Do(req); err != nil {
			o.log.Warn("OIDC token revocation failed", "err", err.Error())
		} else {
			res.Body.Close()
		}
		cancel()
	}
	after := o.cfg.PostLogoutRedirectURI
	if after == "" {
		after = "/"
	}
	if s != nil && o.cfg.EndSession && d != nil && d.EndSessionEndpoint != "" {
		u, _ := url.Parse(d.EndSessionEndpoint)
		q := u.Query()
		q.Set("id_token_hint", s.IDToken)
		q.Set("client_id", o.cfg.ClientID)
		if o.cfg.PostLogoutRedirectURI != "" {
			q.Set("post_logout_redirect_uri", o.cfg.PostLogoutRedirectURI)
		}
		u.RawQuery = q.Encode()
		after = u.String()
	}
	w.Header().Set("Location", after)
	w.WriteHeader(http.StatusSeeOther)
}
