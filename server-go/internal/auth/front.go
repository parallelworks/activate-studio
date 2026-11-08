package auth

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// Front is auth.ts's onRequest hook as the Go front runs it: it decides
// who each request is, turns away what sign-in requires, and leaves the
// result on the request for the Go routes and for the hand-off to Node.
type Front struct {
	cfg     config.Config
	log     *slog.Logger
	proxy   *RemoteJWKS // the session proxy's keys, nil without AUTH_JWKS_URL
	OIDC    *OIDC       // nil without OIDC_ISSUER and OIDC_CLIENT_ID
	Handoff *Handoff
}

type ctxKey struct{}

// UserOf is the request's verified user, or nil.
func UserOf(r *http.Request) *User {
	u, _ := r.Context().Value(ctxKey{}).(*User)
	return u
}

// WithUser is r carrying u.
func WithUser(r *http.Request, u *User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
}

// NewFront is the hook for cfg. store holds OIDC sessions; h signs the
// hand-off tokens (nil in tests that only check the hook).
func NewFront(cfg config.Config, log *slog.Logger, store *SessionStore, h *Handoff) *Front {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	f := &Front{cfg: cfg, log: log, Handoff: h}
	if cfg.Auth.JWKSURL != "" {
		f.proxy = NewRemoteJWKS(cfg.Auth.JWKSURL)
	}
	if cfg.OIDC.Enabled() {
		f.OIDC = NewOIDC(cfg.OIDC, cfg.Auth, store, log)
	}
	return f
}

// Enabled is authEnabled(): some source of identity is configured.
func (f *Front) Enabled() bool { return f.proxy != nil || f.OIDC != nil }

func (f *Front) required() bool { return f.cfg.Auth.Required }

// Routes registers /api/auth/* and /api/whoami on mux.
func (f *Front) Routes(mux *http.ServeMux) {
	if f.OIDC != nil {
		mux.HandleFunc("GET "+LoginPath, f.OIDC.Login)
		mux.HandleFunc("GET "+CallbackPath, f.OIDC.Callback)
		mux.HandleFunc("GET "+LogoutPath, f.OIDC.Logout)
		mux.HandleFunc("POST "+LogoutPath, f.OIDC.Logout)
	}
	mux.HandleFunc("GET /api/whoami", f.whoami)
}

// verifyProxy is auth.ts's verify(): the token in AUTH_HEADER (a Bearer
// prefix dropped) against the platform's keys, issuer and audience.
func (f *Front) verifyProxy(r *http.Request) (*User, error) {
	if f.proxy == nil {
		return nil, nil
	}
	tok := r.Header.Get(f.cfg.Auth.Header)
	if len(tok) >= 7 && strings.EqualFold(tok[:7], "bearer ") {
		tok = tok[7:]
	}
	if tok == "" {
		return nil, nil
	}
	ctx := r.Context()
	c, err := Verify(tok, func(alg, kid string) ([]cryptoKey, error) { return f.proxy.Keys(ctx, alg, kid) },
		VerifyOptions{Issuer: f.cfg.Auth.Issuer, Audience: f.cfg.Auth.Audience})
	if err != nil {
		return nil, err
	}
	return proxyUser(c, f.cfg.Auth), nil
}

// Identify is the proxy token first, then the OIDC session. invalid
// reports a proxy token that was present and failed, when no session
// stood in for it.
func (f *Front) Identify(r *http.Request) (u *User, invalid bool) {
	u, err := f.verifyProxy(r)
	if err != nil {
		f.log.Warn("token verification failed", "path", r.URL.Path, "err", err.Error())
		invalid = true
	} else if u != nil {
		return u, false
	}
	if f.OIDC != nil {
		if s := f.OIDC.SessionUser(r); s != nil {
			return s, false
		}
	}
	return nil, invalid
}

var assetRe = regexp.MustCompile(`(?i)\.(m?js|css|map|png|jpe?g|gif|svg|ico|webp|woff2?|ttf|webmanifest|txt)$`)

// isAsset is a file the browser loads for the app itself; the code is
// public, and the sign-in redirect is for the page that loads them.
func isAsset(p string) bool { return strings.HasPrefix(p, "/assets/") || assetRe.MatchString(p) }

// LoginURL is the sign-in route with a return path.
func LoginURL(returnTo string) string { return LoginPath + "?returnTo=" + encodeURIComponent(returnTo) }

// Gate runs the hook. It answers the request itself (a 401 or a redirect
// to sign-in) and returns nil, or returns the request to serve, carrying
// its user when there is one. Every request loses any NodeHeader a client
// sent.
func (f *Front) Gate(w http.ResponseWriter, r *http.Request) *http.Request {
	r.Header.Del(NodeHeader)
	if !f.Enabled() {
		return r
	}
	p := r.URL.Path
	if p == "/healthz" {
		return r
	}
	if !strings.HasPrefix(p, "/api/") {
		// Pages are gated only where people sign in through the Studio
		// itself; behind the session proxy the platform already did. Node's
		// own paths outside /api (/v1, /internal) keep their own checks.
		if f.OIDC == nil || !f.required() || isAsset(p) || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/internal/") {
			return r
		}
		if u, _ := f.Identify(r); u != nil {
			return WithUser(r, u)
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, LoginURL(r.URL.RequestURI()), http.StatusFound)
			return nil
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign-in required", "signIn": LoginPath})
		return nil
	}
	// Sign-in, callback and sign-out answer everyone.
	if strings.HasPrefix(p, "/api/auth/") {
		if p == LogoutPath {
			return r
		}
		if u, _ := f.Identify(r); u != nil {
			return WithUser(r, u)
		}
		return r
	}
	u, invalid := f.Identify(r)
	if u != nil {
		return WithUser(r, u)
	}
	// A mission worker presents its mission token, which the task board
	// checks: the request goes on to /api/mcp (internal/chat), which admits
	// it for a task token and otherwise refuses it as any unsigned call is
	// refused where sign-in is required.
	if strings.HasPrefix(p, "/api/mcp") {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") && strings.TrimSpace(a[7:]) != "" {
			return r
		}
	}
	if !f.required() {
		return r
	}
	if invalid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return nil
	}
	if f.OIDC != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign-in required", "signIn": LoginPath})
	} else {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
	return nil
}

// ToNode sets the hand-off token on a request going to Node.
func (f *Front) ToNode(out *http.Request, u *User) {
	out.Header.Del(NodeHeader)
	if u == nil || f.Handoff == nil || !strings.HasPrefix(out.URL.Path, "/api/") {
		return
	}
	tok, err := f.Handoff.Token(u)
	if err != nil {
		f.log.Error("identity hand-off", "err", err.Error())
		return
	}
	out.Header.Set(NodeHeader, tok)
}

// Run prunes OIDC state every ten minutes until ctx ends.
func (f *Front) Run(ctx context.Context) {
	if f.OIDC == nil {
		return
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.OIDC.Prune()
		}
	}
}

func isoOf(v any) any {
	if f, ok := v.(float64); ok {
		return time.Unix(0, int64(f*1000)*int64(time.Millisecond)).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return nil
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// whoami is GET /api/whoami as the oidc-signin branch answers it: whether
// this request carried a valid platform token or a signed-in session, and
// what it said.
func (f *Front) whoami(w http.ResponseWriter, r *http.Request) {
	u := UserOf(r)
	var source, logout any
	if u != nil {
		source = u.Source
		if f.OIDC != nil && u.Source == "oidc" {
			logout = LogoutPath
		}
	}
	var login any
	if f.OIDC != nil {
		login = LoginPath
	}
	out := map[string]any{
		"authEnabled": f.Enabled(),
		"header":      f.cfg.Auth.Header,
		"verified":    u != nil,
		"signIn": map[string]any{
			"proxy": f.proxy != nil, "oidc": f.OIDC != nil, "required": f.Enabled() && f.required(),
			"source": source, "loginUrl": login, "logoutUrl": logout,
		},
		"user": nil, "token": nil, "claims": nil,
	}
	if u != nil {
		user := map[string]any{"id": u.ID, "username": u.Username, "account": orNil(u.Account), "uid": nil, "gid": nil, "groups": nil}
		if u.Name != "" {
			user["name"] = u.Name
		}
		if u.UID != nil {
			user["uid"] = *u.UID
		}
		if u.GID != nil {
			user["gid"] = *u.GID
		}
		if u.Groups != nil {
			user["groups"] = u.Groups
		}
		out["user"] = user
		iss, _ := u.Claims["iss"].(string)
		out["token"] = map[string]any{"issuer": orNil(iss), "audience": u.Claims["aud"], "issuedAt": isoOf(u.Claims["iat"]), "expiresAt": isoOf(u.Claims["exp"])}
		out["claims"] = u.Claims
	}
	writeJSON(w, http.StatusOK, out)
}
