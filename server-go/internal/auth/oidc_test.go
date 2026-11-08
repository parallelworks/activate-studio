package auth

// The OIDC sign-in tests of the oidc-signin branch (server/test/oidc.test.mjs
// and oidcOff.test.mjs), ported: a provider stood up inside the test (its
// discovery document, authorization, token, userinfo, JWKS, revocation and
// end-session endpoints, with keys made here) and the flow driven the way a
// browser drives it, one redirect at a time. Two tests changed with the
// decisions of stage 1: sign-out ends the Studio's session only by default,
// and OIDC_END_SESSION=1 sends the browser to the provider's end_session.
// The tests after those are new: sessions that outlive a restart, and the
// hand-off of identity to Node.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

const (
	clientID     = "pwapp_test"
	clientSecret = "secret-for-tests"
)

type rsaKey struct {
	priv *rsa.PrivateKey
	kid  string
}

func newKey(t *testing.T, kid string) rsaKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return rsaKey{k, kid}
}

func (k rsaKey) jwk() JWK {
	return JWK{Kty: "RSA", Kid: k.kid, Alg: "RS256", Use: "sig", N: b64.EncodeToString(k.priv.N.Bytes()), E: b64.EncodeToString(big.NewInt(int64(k.priv.E)).Bytes())}
}

func signRS256(t *testing.T, k *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
	pb, _ := json.Marshal(claims)
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	h := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

type mode struct {
	wrongIss, wrongAud, expired, badSig, wrongNonce, offline bool
	expiresIn                                                *int
	respondIss, userinfoSub                                  string
}

type refreshRec struct {
	used  bool
	chain string
	sub   string
}

type provider struct {
	t                    *testing.T
	srv                  *httptest.Server
	base, issuer         string
	idp, stray, platform rsaKey
	mu                   sync.Mutex
	mode                 mode
	codes                map[string]map[string]string
	refresh              map[string]*refreshRec
	revokedChains        map[string]bool
	revoked              []string
	refreshCalls         int
	tokenCalls           []url.Values
	access               map[string]string
	lastAuthorize        url.Values
}

func (p *provider) setMode(m mode) { p.mu.Lock(); p.mode = m; p.mu.Unlock() }

func (p *provider) idToken(sub, nonce string, m mode) string {
	now := time.Now().Unix()
	c := map[string]any{"name": "Ada Lovelace", "preferred_username": "ada", "email": "ada@example.org", "sub": sub, "iss": p.issuer, "aud": clientID, "iat": now, "exp": now + 600}
	if nonce != "" {
		c["nonce"] = nonce
		if m.wrongNonce {
			c["nonce"] = "not-the-nonce"
		}
	}
	if m.wrongIss {
		c["iss"] = "https://other.example.org/api/oidc"
	}
	if m.wrongAud {
		c["aud"] = "pwapp_someone_else"
	}
	if m.expired {
		c["iat"], c["exp"] = now-7200, now-3600
	}
	key := p.idp.priv
	if m.badSig {
		key = p.stray.priv
	}
	return signRS256(p.t, key, "idp-1", c)
}

func hex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (p *provider) issue(sub, nonce, chain string, m mode) map[string]any {
	access := "pwoa_" + hex(12)
	exp := 3600
	if m.expiresIn != nil {
		exp = *m.expiresIn
	}
	out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": exp, "id_token": p.idToken(sub, nonce, m), "scope": "openid profile email groups"}
	if m.offline {
		rt := "pwor_" + hex(12)
		p.refresh[rt] = &refreshRec{chain: chain, sub: sub}
		out["refresh_token"] = rt
	}
	p.access[access] = sub
	return out
}

func clientOK(r *http.Request, form url.Values) bool {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Basic ") || form.Has("client_secret") {
		return false
	}
	raw, _ := base64.StdEncoding.DecodeString(a[6:])
	id, secret, _ := strings.Cut(string(raw), ":")
	id, _ = url.QueryUnescape(id)
	secret, _ = url.QueryUnescape(secret)
	return id == clientID && secret == clientSecret
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func newProvider(t *testing.T) *provider {
	p := &provider{t: t, idp: newKey(t, "idp-1"), stray: newKey(t, "idp-1"), platform: newKey(t, "platform-1"),
		codes: map[string]map[string]string{}, refresh: map[string]*refreshRec{}, revokedChains: map[string]bool{}, access: map[string]string{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	p.base = p.srv.URL
	p.issuer = p.base + "/oidc"
	return p
}

func (p *provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	m := p.mode
	p.mu.Unlock()
	switch r.URL.Path {
	case "/oidc/.well-known/openid-configuration":
		jsonOut(w, 200, map[string]any{
			"issuer": p.issuer, "authorization_endpoint": p.base + "/oidc/authorize", "token_endpoint": p.base + "/oidc/token",
			"userinfo_endpoint": p.base + "/oidc/userinfo", "jwks_uri": p.base + "/oidc/jwks", "end_session_endpoint": p.base + "/oidc/end_session",
			"revocation_endpoint": p.base + "/oidc/revoke", "code_challenge_methods_supported": []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
			"authorization_response_iss_parameter_supported": true,
		})
	case "/oidc/jwks":
		jsonOut(w, 200, map[string]any{"keys": []JWK{p.idp.jwk()}})
	case "/platform/keys":
		jsonOut(w, 200, map[string]any{"keys": []JWK{p.platform.jwk()}})
	case "/oidc/authorize":
		q := r.URL.Query()
		code := hex(8)
		sub := "user-uuid-ada"
		p.mu.Lock()
		p.lastAuthorize = q
		p.codes[code] = map[string]string{"redirectUri": q.Get("redirect_uri"), "challenge": q.Get("code_challenge"), "method": q.Get("code_challenge_method"), "nonce": q.Get("nonce"), "sub": sub}
		p.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		iss := p.issuer
		if m.respondIss != "" {
			iss = m.respondIss
		}
		bq.Set("iss", iss)
		back.RawQuery = bq.Encode()
		w.Header().Set("Location", back.String())
		w.WriteHeader(302)
	case "/oidc/token":
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		p.mu.Lock()
		p.tokenCalls = append(p.tokenCalls, form)
		p.mu.Unlock()
		if !clientOK(r, form) {
			jsonOut(w, 401, map[string]string{"error": "invalid_client"})
			return
		}
		switch form.Get("grant_type") {
		case "authorization_code":
			p.mu.Lock()
			c, ok := p.codes[form.Get("code")]
			delete(p.codes, form.Get("code"))
			p.mu.Unlock()
			if !ok || c["redirectUri"] != form.Get("redirect_uri") {
				jsonOut(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			h := sha256.Sum256([]byte(form.Get("code_verifier")))
			if c["method"] != "S256" || b64.EncodeToString(h[:]) != c["challenge"] {
				jsonOut(w, 400, map[string]string{"error": "invalid_grant", "error_description": "PKCE verification failed"})
				return
			}
			p.mu.Lock()
			out := p.issue(c["sub"], c["nonce"], hex(8), m)
			p.mu.Unlock()
			jsonOut(w, 200, out)
		case "refresh_token":
			p.mu.Lock()
			p.refreshCalls++
			rr, ok := p.refresh[form.Get("refresh_token")]
			if !ok || p.revokedChains[rr.chain] {
				p.mu.Unlock()
				jsonOut(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			if rr.used {
				p.revokedChains[rr.chain] = true
				p.mu.Unlock()
				jsonOut(w, 400, map[string]string{"error": "invalid_grant", "error_description": "reused"})
				return
			}
			rr.used = true
			p.mu.Unlock()
			time.Sleep(50 * time.Millisecond) // long enough for concurrent callers to pile up
			p.mu.Lock()
			out := p.issue(rr.sub, "", rr.chain, m)
			p.mu.Unlock()
			jsonOut(w, 200, out)
		default:
			jsonOut(w, 400, map[string]string{"error": "unsupported_grant_type"})
		}
	case "/oidc/userinfo":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		p.mu.Lock()
		sub, ok := p.access[tok]
		p.mu.Unlock()
		if !ok {
			jsonOut(w, 401, map[string]string{"error": "invalid_token"})
			return
		}
		if m.userinfoSub != "" {
			sub = m.userinfoSub
		}
		jsonOut(w, 200, map[string]any{"sub": sub, "preferred_username": "ada", "groups": []string{"research", "admins"}, "uidNumber": "41001", "gidNumber": 5000, "unixName": "alovelace"})
	case "/oidc/revoke":
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if !clientOK(r, form) {
			jsonOut(w, 401, map[string]string{"error": "invalid_client"})
			return
		}
		p.mu.Lock()
		p.revoked = append(p.revoked, form.Get("token"))
		if rr, ok := p.refresh[form.Get("token")]; ok {
			p.revokedChains[rr.chain] = true
		}
		p.mu.Unlock()
		w.WriteHeader(200)
	default:
		w.WriteHeader(404)
	}
}

// studio is the front as the server runs it: Gate, then the Go routes and
// stand-ins for a page, an asset and an API route that reports req.user.
type studio struct {
	front *Front
	h     http.Handler
	dir   string
}

type opts struct {
	required, endSession bool
	dir                  string
}

func (p *provider) cfg(o opts) config.Config {
	return config.Config{
		IndexBase: o.dir,
		Auth: config.Auth{
			JWKSURL: p.base + "/platform/keys", Header: "x-pw-user-token", Required: o.required,
			AccountClaim: "unixName", UIDClaim: "uidNumber", GIDClaim: "gidNumber",
		},
		OIDC: config.OIDC{
			Issuer: p.issuer, ClientID: clientID, ClientSecret: clientSecret,
			RedirectURI: "https://studio.example.org/api/auth/callback", PostLogoutRedirectURI: "https://studio.example.org/",
			Scopes: "openid profile email groups", UsernameClaim: "preferred_username", EndSession: o.endSession, MaxAgeSec: 43200,
		},
	}
}

func newStudio(t *testing.T, p *provider, o opts) *studio {
	t.Helper()
	if o.dir == "" {
		o.dir = t.TempDir()
	}
	store, err := NewSessionStore(o.dir)
	if err != nil {
		t.Fatal(err)
	}
	f := NewFront(p.cfg(o), nil, store, nil)
	mux := http.NewServeMux()
	f.Routes(mux)
	mux.HandleFunc("GET /api/me-test", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"user": UserOf(r)})
	})
	mux.HandleFunc("POST /api/mcp", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]any{"reachedNode": true}) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!DOCTYPE html><p>app</p>")
	})
	mux.HandleFunc("GET /assets/index-abcdef12.js", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "1") })
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r = f.Gate(w, r); r != nil {
			mux.ServeHTTP(w, r)
		}
	})
	return &studio{front: f, h: h, dir: o.dir}
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (s *studio) do(method, target string, header map[string]string) resp {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return resp{w.Code, w.Header(), w.Body.String()}
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not JSON: %q", r.body)
	}
	return m
}

func (r resp) setCookies() []string { return r.header.Values("Set-Cookie") }

func (r resp) cookie(name string) (string, bool) {
	for _, c := range r.setCookies() {
		if strings.HasPrefix(c, name+"=") {
			v := strings.SplitN(c, ";", 2)[0]
			return v[len(name)+1:], true
		}
	}
	return "", false
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// startSignIn starts a sign-in, lets the provider answer, and returns
// what the browser would carry back to the callback.
func (s *studio) startSignIn(t *testing.T, returnTo string) (auth *url.URL, cb *url.URL, txCookie string) {
	t.Helper()
	login := s.do("GET", LoginPath+"?returnTo="+url.QueryEscape(returnTo), nil)
	if login.status != 302 {
		t.Fatalf("login %d %s", login.status, login.body)
	}
	auth, _ = url.Parse(login.header.Get("Location"))
	for _, c := range login.setCookies() {
		if strings.HasPrefix(c, "studio_tx_") {
			for _, want := range []string{"HttpOnly", "SameSite=Lax", "Secure"} {
				if !strings.Contains(c, want) {
					t.Fatalf("tx cookie lacks %s: %s", want, c)
				}
			}
			txCookie = strings.SplitN(c, ";", 2)[0]
		}
	}
	if txCookie == "" {
		t.Fatal("the sign-in binds itself to this browser")
	}
	res, err := noFollow.Get(auth.String())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	cb, _ = url.Parse(res.Header.Get("Location"))
	return auth, cb, txCookie
}

func (s *studio) signIn(t *testing.T, returnTo string) (resp, string) {
	t.Helper()
	_, cb, tx := s.startSignIn(t, returnTo)
	res := s.do("GET", cb.Path+"?"+cb.RawQuery, map[string]string{"Cookie": tx})
	sid, _ := res.cookie(SessionCookie)
	return res, sid
}

func (s *studio) me(t *testing.T, sid string, header map[string]string) map[string]any {
	t.Helper()
	h := map[string]string{}
	for k, v := range header {
		h[k] = v
	}
	if sid != "" {
		h["Cookie"] = SessionCookie + "=" + sid
	}
	res := s.do("GET", "/api/me-test", h)
	if res.status != 200 {
		t.Fatalf("me-test %d %s", res.status, res.body)
	}
	u, _ := res.json(t)["user"].(map[string]any)
	return u
}

func (p *provider) proxyToken(t *testing.T, username string, key *rsa.PrivateKey) string {
	now := time.Now().Unix()
	return signRS256(t, key, "platform-1", map[string]any{"preferred_username": username, "sub": "user:" + username, "iat": now, "exp": now + 300})
}

func TestSignInIsOnWithOIDC(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	if !s.front.Enabled() || s.front.OIDC == nil {
		t.Fatal("sign-in off")
	}
}

func TestAuthorizationRequest(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	auth, _, _ := s.startSignIn(t, "/library")
	q := auth.Query()
	if auth.Scheme+"://"+auth.Host+auth.Path != p.base+"/oidc/authorize" {
		t.Fatal(auth)
	}
	checks := map[string]string{"response_type": "code", "client_id": clientID, "redirect_uri": "https://studio.example.org/api/auth/callback", "scope": "openid profile email groups", "code_challenge_method": "S256"}
	for k, v := range checks {
		if q.Get(k) != v {
			t.Errorf("%s = %q", k, q.Get(k))
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(q.Get("code_challenge")) {
		t.Error("challenge", q.Get("code_challenge"))
	}
	if len(q.Get("state")) < 32 || len(q.Get("nonce")) < 32 || q.Get("state") == q.Get("nonce") {
		t.Error("state and nonce")
	}
}

func TestFullSignIn(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	res, sid := s.signIn(t, "/library?x=1")
	if res.status != 303 || res.header.Get("Location") != "/library?x=1" {
		t.Fatalf("%d %s", res.status, res.header.Get("Location"))
	}
	var sc string
	for _, c := range res.setCookies() {
		if strings.HasPrefix(c, SessionCookie+"=") {
			sc = c
		}
	}
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Secure", "Path=/"} {
		if !strings.Contains(sc, want) {
			t.Errorf("session cookie lacks %s: %s", want, sc)
		}
	}
	u := s.me(t, sid, nil)
	if u["id"] != "user-uuid-ada" || u["username"] != "ada" || u["name"] != "Ada Lovelace" || u["source"] != "oidc" {
		t.Fatalf("user %v", u)
	}
	if fmt.Sprint(u["groups"]) != "[research admins]" || u["account"] != "alovelace" || u["uid"] != 41001.0 || u["gid"] != 5000.0 {
		t.Fatalf("mapping %v", u)
	}
	call := p.tokenCalls[len(p.tokenCalls)-1]
	if call.Get("grant_type") != "authorization_code" || call.Get("code_verifier") == "" || call.Has("client_secret") {
		t.Fatalf("token call %v", call)
	}
	hb, _ := json.Marshal(res.header)
	if regexp.MustCompile(`pwoa_|eyJ`).Match(hb) {
		t.Fatal("a token reached the browser")
	}
}

func TestUnknownSessionIsNobody(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	if s.me(t, "not-a-session", nil) != nil {
		t.Fatal("somebody")
	}
}

func TestCallbackWithoutBindingOrReplayed(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	_, cb, tx := s.startSignIn(t, "/")
	target := cb.Path + "?" + cb.RawQuery
	no := s.do("GET", target, nil)
	if no.status != 400 {
		t.Fatal(no.status)
	}
	if _, ok := no.cookie(SessionCookie); ok {
		t.Fatal("session set")
	}
	if replay := s.do("GET", target, map[string]string{"Cookie": tx}); replay.status != 400 {
		t.Fatal("replay", replay.status)
	}
}

func TestTamperedState(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	_, cb, tx := s.startSignIn(t, "/")
	q := cb.Query()
	q.Set("state", "forged-state-value-forged-state-value")
	if res := s.do("GET", cb.Path+"?"+q.Encode(), map[string]string{"Cookie": tx}); res.status != 400 {
		t.Fatal(res.status)
	}
}

func TestWrongPKCEVerifier(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	_, a, _ := s.startSignIn(t, "/")
	_, b, txB := s.startSignIn(t, "/")
	q := b.Query()
	q.Set("code", a.Query().Get("code"))
	res := s.do("GET", b.Path+"?"+q.Encode(), map[string]string{"Cookie": txB})
	if res.status != 401 {
		t.Fatal(res.status)
	}
	if _, ok := res.cookie(SessionCookie); ok {
		t.Fatal("session set")
	}
	if p.tokenCalls[len(p.tokenCalls)-1].Get("grant_type") != "authorization_code" {
		t.Fatal("no code exchange")
	}
}

func TestIssParameterRefusedBeforeCodeUse(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	p.setMode(mode{respondIss: "https://evil.example.org/api/oidc"})
	calls := len(p.tokenCalls)
	if res, _ := s.signIn(t, "/"); res.status != 400 {
		t.Fatal(res.status)
	}
	if len(p.tokenCalls) != calls {
		t.Fatal("the code was used")
	}
}

func TestBadIDTokensRefused(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	for label, m := range map[string]mode{
		"a bad signature": {badSig: true}, "the wrong issuer": {wrongIss: true}, "the wrong audience": {wrongAud: true},
		"an expired ID token": {expired: true}, "a nonce from another request": {wrongNonce: true}, "a userinfo subject that differs": {userinfoSub: "someone-else"},
	} {
		t.Run(label, func(t *testing.T) {
			p.setMode(m)
			res, sid := s.signIn(t, "/")
			if res.status != 401 || sid != "" || !strings.Contains(res.body, "could not be verified") {
				t.Fatalf("%d %q %s", res.status, sid, res.body)
			}
		})
	}
}

func lastRefreshToken(p *provider) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var last string
	for k, r := range p.refresh {
		if !r.used {
			last = k
		}
	}
	return last
}

func TestSignOutEndsTheStudioSessionOnlyByDefault(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	p.setMode(mode{offline: true})
	_, sid := s.signIn(t, "/")
	if s.me(t, sid, nil) == nil {
		t.Fatal("not signed in")
	}
	rt := lastRefreshToken(p)
	out := s.do("POST", LogoutPath, map[string]string{"Cookie": SessionCookie + "=" + sid})
	if out.status != 303 || out.header.Get("Location") != "https://studio.example.org/" {
		t.Fatalf("%d %s", out.status, out.header.Get("Location"))
	}
	if v, _ := out.cookie(SessionCookie); v != "" || !strings.Contains(out.setCookies()[0], "Max-Age=0") {
		t.Fatalf("cookie %v", out.setCookies())
	}
	if !strings.Contains(strings.Join(p.revoked, " "), rt) {
		t.Fatal("refresh token not revoked")
	}
	if s.me(t, sid, nil) != nil {
		t.Fatal("still signed in")
	}
}

func TestEndSessionSendsTheBrowserToTheProvider(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{endSession: true})
	_, sid := s.signIn(t, "/")
	out := s.do("GET", LogoutPath, map[string]string{"Cookie": SessionCookie + "=" + sid})
	loc, _ := url.Parse(out.header.Get("Location"))
	if out.status != 303 || loc.Scheme+"://"+loc.Host+loc.Path != p.base+"/oidc/end_session" {
		t.Fatalf("%d %s", out.status, loc)
	}
	q := loc.Query()
	if !strings.HasPrefix(q.Get("id_token_hint"), "eyJ") || q.Get("client_id") != clientID || q.Get("post_logout_redirect_uri") != "https://studio.example.org/" {
		t.Fatalf("query %v", q)
	}
	if s.me(t, sid, nil) != nil {
		t.Fatal("still signed in")
	}
}

func TestRefreshRotatesOnceAndAFailedRefreshEndsTheSession(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	two := 2
	p.setMode(mode{offline: true, expiresIn: &two})
	_, sid := s.signIn(t, "/")
	if s.me(t, sid, nil)["username"] != "ada" {
		t.Fatal("not signed in")
	}
	before := p.refreshCalls
	time.Sleep(1100 * time.Millisecond) // past the refresh point (half of 2 s)
	var wg sync.WaitGroup
	users := make([]map[string]any, 4)
	for i := range users {
		wg.Add(1)
		go func(i int) { defer wg.Done(); users[i] = s.me(t, sid, nil) }(i)
	}
	wg.Wait()
	for _, u := range users {
		if u == nil || u["id"] != "user-uuid-ada" {
			t.Fatalf("users %v", users)
		}
	}
	if p.refreshCalls-before != 1 {
		t.Fatalf("%d refreshes for four requests", p.refreshCalls-before)
	}
	p.mu.Lock()
	for _, r := range p.refresh {
		p.revokedChains[r.chain] = true
	}
	p.mu.Unlock()
	time.Sleep(1100 * time.Millisecond)
	if s.me(t, sid, nil) != nil || s.me(t, sid, nil) != nil {
		t.Fatal("session outlived its refresh")
	}
}

func TestNoRefreshWithoutOfflineAccess(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	one := 1
	p.setMode(mode{expiresIn: &one})
	_, sid := s.signIn(t, "/")
	before := p.refreshCalls
	time.Sleep(600 * time.Millisecond)
	if s.me(t, sid, nil)["username"] != "ada" || p.refreshCalls != before {
		t.Fatal("refreshed")
	}
}

func TestProxyJWTWinsOverSession(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	_, sid := s.signIn(t, "/")
	tok := p.proxyToken(t, "grace", p.platform.priv)
	via := s.me(t, "", map[string]string{"x-pw-user-token": tok})
	if via["username"] != "grace" || via["source"] != "proxy" || via["id"] != "user:grace" {
		t.Fatalf("%v", via)
	}
	if both := s.me(t, sid, map[string]string{"x-pw-user-token": tok}); both["username"] != "grace" {
		t.Fatalf("%v", both)
	}
}

func TestInvalidProxyJWTFallsBackOrIsRefused(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	_, sid := s.signIn(t, "/")
	bad := p.proxyToken(t, "grace", p.stray.priv)
	if u := s.me(t, sid, map[string]string{"x-pw-user-token": bad}); u["username"] != "ada" {
		t.Fatalf("%v", u)
	}
	req := newStudio(t, p, opts{required: true})
	res := req.do("GET", "/api/me-test", map[string]string{"x-pw-user-token": bad})
	if res.status != 401 || res.json(t)["error"] != "invalid token" {
		t.Fatalf("%d %s", res.status, res.body)
	}
}

func TestIDTokenIsNotAProxyToken(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{})
	if s.me(t, "", map[string]string{"x-pw-user-token": p.idToken("user-uuid-ada", "n", mode{})}) != nil {
		t.Fatal("accepted")
	}
}

func TestSignInRequired(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{required: true})
	api := s.do("GET", "/api/me-test", nil)
	if b := api.json(t); api.status != 401 || b["error"] != "sign-in required" || b["signIn"] != LoginPath {
		t.Fatalf("%d %s", api.status, api.body)
	}
	page := s.do("GET", "/?view=chat", nil)
	if page.status != 302 || page.header.Get("Location") != "/api/auth/login?returnTo=%2F%3Fview%3Dchat" {
		t.Fatalf("%d %s", page.status, page.header.Get("Location"))
	}
	if s.do("GET", "/assets/index-abcdef12.js", nil).status != 200 || s.do("GET", "/healthz", nil).status == 401 || s.do("GET", LoginPath, nil).status != 302 {
		t.Fatal("asset, health or login gated")
	}
	if s.do("POST", "/v1/chat/completions", nil).status == 401 {
		t.Fatal("the RAG endpoint keeps its own key check")
	}
	_, sid := s.signIn(t, "/")
	if s.do("GET", "/", map[string]string{"Cookie": SessionCookie + "=" + sid}).status != 200 || s.me(t, sid, nil)["username"] != "ada" {
		t.Fatal("signed in, still gated")
	}
}

func TestMissionBearerGoesOnToNode(t *testing.T) {
	p := newProvider(t)
	s := newStudio(t, p, opts{required: true})
	// Only Node's task board knows mission tokens: the front lets the call
	// through and Node, started with AUTH_REQUIRED, refuses an unknown one.
	if res := s.do("POST", "/api/mcp", map[string]string{"Authorization": "Bearer nope"}); res.status != 200 {
		t.Fatalf("%d %s", res.status, res.body)
	}
	if res := s.do("POST", "/api/mcp", nil); res.status != 401 {
		t.Fatalf("unsigned %d", res.status)
	}
}

func TestReturnToStaysOnThisOrigin(t *testing.T) {
	for in, want := range map[string]string{"//evil.example.org/x": "/", `/\evil.example.org`: "/", "https://evil.example.org/": "/", "/api/auth/logout": "/", "/library?a=1#b": "/library?a=1#b", "/a\r\nb": "/"} {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestSingleUserBaseline(t *testing.T) {
	// Neither OIDC_ISSUER nor AUTH_JWKS_URL: sign-in off, no sign-in
	// routes, nothing refused or redirected, whatever AUTH_REQUIRED says.
	f := NewFront(config.Config{Auth: config.Auth{Required: true, Header: "x-pw-user-token"}, OIDC: config.OIDC{ClientID: "pwapp_without_an_issuer"}}, nil, nil, nil)
	if f.Enabled() {
		t.Fatal("enabled")
	}
	mux := http.NewServeMux()
	f.Routes(mux)
	if _, pat := mux.Handler(httptest.NewRequest("GET", LoginPath, nil)); pat != "" {
		t.Fatal("sign-in route registered")
	}
	r := httptest.NewRequest("GET", "/api/me-test", nil)
	r.Header.Set("Cookie", "studio_session=x")
	r.Header.Set(NodeHeader, "forged")
	w := httptest.NewRecorder()
	out := f.Gate(w, r)
	if out == nil || UserOf(out) != nil || out.Header.Get(NodeHeader) != "" {
		t.Fatal("refused, identified, or a forged hand-off header kept")
	}
	if f.Gate(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) == nil {
		t.Fatal("page refused")
	}
}

func TestSessionsOutliveARestartEncrypted(t *testing.T) {
	p := newProvider(t)
	dir := t.TempDir()
	s := newStudio(t, p, opts{dir: dir})
	_, sid := s.signIn(t, "/")
	if s.me(t, sid, nil) == nil {
		t.Fatal("not signed in")
	}
	st, err := os.Stat(filepath.Join(dir, ".oidc-session-key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file %v %v", err, st)
	}
	st, err = os.Stat(filepath.Join(dir, "oidc-sessions.enc"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("session file %v %v", err, st)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "oidc-sessions.enc"))
	if strings.Contains(string(raw), "pwoa_") || strings.Contains(string(raw), "ada") || strings.Contains(string(raw), "eyJ") {
		t.Fatal("the session file is not encrypted")
	}
	again := newStudio(t, p, opts{dir: dir})
	if u := again.me(t, sid, nil); u == nil || u["username"] != "ada" {
		t.Fatalf("restart lost the sign-in: %v", u)
	}
	// Another key cannot open it: the store starts empty.
	os.WriteFile(filepath.Join(dir, ".oidc-session-key"), make([]byte, 32), 0o600)
	if newStudio(t, p, opts{dir: dir}).me(t, sid, nil) != nil {
		t.Fatal("opened with another key")
	}
}

func TestHandoffTokenIsWhatNodeReads(t *testing.T) {
	h, err := StartHandoff("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	env := h.NodeEnv(true)
	if env["AUTH_HEADER"] != NodeHeader || env["AUTH_ISSUER"] != HandoffIssuer || env["AUTH_AUDIENCE"] != HandoffAudience || env["AUTH_REQUIRED"] != "1" || env["AUTH_JWKS_URL"] != h.JWKSURL {
		t.Fatalf("%v", env)
	}
	tok, err := h.Token(&User{ID: "user-uuid-ada", Username: "ada", Name: "Ada Lovelace", Source: "oidc"})
	if err != nil {
		t.Fatal(err)
	}
	set := NewRemoteJWKS(h.JWKSURL)
	c, err := Verify(tok, func(alg, kid string) ([]cryptoKey, error) { return set.Keys(context.Background(), alg, kid) },
		VerifyOptions{Issuer: HandoffIssuer, Audience: HandoffAudience})
	if err != nil {
		t.Fatal(err)
	}
	u := proxyUser(c, config.Auth{})
	if u.ID != "user-uuid-ada" || u.Username != "ada" || u.Name != "Ada Lovelace" {
		t.Fatalf("%+v", u)
	}
	if _, err := StartHandoff("0.0.0.0:0"); err == nil {
		t.Fatal("a key set on a public address")
	}
	// Signed by another front: refused.
	other, _ := StartHandoff("127.0.0.1:0")
	defer other.Close()
	forged, _ := other.Token(&User{ID: "x", Username: "x"})
	if _, err := Verify(forged, func(alg, kid string) ([]cryptoKey, error) { return set.Keys(context.Background(), alg, kid) }, VerifyOptions{}); err == nil {
		t.Fatal("accepted another key's token")
	}
}
