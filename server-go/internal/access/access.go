// Package access ports server/src/accessRoutes.ts: Model access as one
// picture (where model calls and platform calls get their credential, the
// pw CLI's contexts and which one is used), signing the CLI in to a
// platform, and subscriptions connected through the platform (a ChatGPT
// plan, GitHub Copilot).
package access

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Routes serves accessRoutes.ts's routes.
type Routes struct {
	p *platform.Platform
	a *app.App

	mu       sync.Mutex
	accounts map[string]account
}

type account struct {
	at        time.Time
	org, user string
}

// Register registers the ten Model access routes.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) *Routes {
	s := &Routes{p: p, a: a, accounts: map[string]account{}}
	h := func(f func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, f)
	}
	mux.HandleFunc("GET /api/access", h(s.get))
	mux.HandleFunc("PUT /api/access/cli/path", h(s.cliPath))
	mux.HandleFunc("PUT /api/access/source", h(s.source))
	mux.HandleFunc("POST /api/access/cli/login", h(s.loginStart))
	mux.HandleFunc("GET /api/access/cli/login", h(s.loginGet))
	mux.HandleFunc("POST /api/access/cli/login/cancel", h(s.loginCancel))
	mux.HandleFunc("GET /api/access/subscriptions", h(s.subscriptions))
	mux.HandleFunc("POST /api/access/codex", h(s.codex))
	mux.HandleFunc("POST /api/access/codex/{id}", h(s.codexPoll))
	mux.HandleFunc("POST /api/access/copilot", h(s.copilot))
	return s
}

// ResetAccounts forgets the platform accounts (resetAccessRoutesForTests).
func (s *Routes) ResetAccounts() {
	s.mu.Lock()
	s.accounts = map[string]account{}
	s.mu.Unlock()
}

// hostOf is new URL(String(u)).host, or nil.
func hostOf(u string) any {
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return nil
	}
	h := strings.ToLower(p.Host)
	if (p.Scheme == "https" && strings.HasSuffix(h, ":443")) || (p.Scheme == "http" && strings.HasSuffix(h, ":80")) {
		h = h[:strings.LastIndex(h, ":")]
	}
	return h
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// deploymentSource is the deployment's own chain: a shared key,
// PW_API_KEY, the pw CLI.
func (s *Routes) deploymentSource(v *platform.Viewer, single bool) *jsonv.Object {
	host := hostOf(s.p.GatewayBase())
	if s.p.SharedKey() != "" {
		var last4 any
		if v.SharedLast4 != nil {
			last4 = *v.SharedLast4
		} else {
			last4 = nullable(s.p.SharedKeyLast4())
		}
		return jsonv.NewObject("source", "shared", "host", host, "context", nil, "last4", last4)
	}
	if s.p.APIKeyEnv() != "" {
		return jsonv.NewObject("source", "env", "host", host, "context", nil)
	}
	if s.p.CLICredential() != "" {
		var ctx any
		if single {
			ctx = nullable(s.p.CredentialContextName())
		}
		return jsonv.NewObject("source", "cli", "host", host, "context", ctx)
	}
	return jsonv.NewObject("source", "none", "host", host, "context", nil)
}

// sources is accessSources: where model calls and platform calls get their
// credential for the viewer.
func (s *Routes) sources(v *platform.Viewer) (models, plat *jsonv.Object) {
	single := !v.AuthEnabled
	var status any
	if len(v.KeyStatus) > 0 {
		status, _ = jsonv.Decode(v.KeyStatus)
	}
	dep := s.deploymentSource(v, single)
	cred := v.Cred
	mine := func(source string, host any) *jsonv.Object {
		return jsonv.NewObject("source", source, "host", host, "context", nil, "baseUrl", nullable(cred.BaseURL),
			"last4", jsonv.Path(status, "last4"), "kind", jsonv.Path(status, "kind"))
	}
	gw := hostOf(s.p.GatewayBase())
	switch {
	case cred != nil && cred.BaseURL != "":
		models = mine("endpoint", hostOf(cred.BaseURL))
	case cred != nil:
		models = mine("key", gw)
	case jsonv.Str(jsonv.Path(dep, "source")) != "none":
		models = dep
	case strings.TrimSuffix(s.p.Getenv("SIDECAR_MODEL_URL"), "/") != "":
		models = jsonv.NewObject("source", "sidecar", "host", nil, "context", nil)
	default:
		models = dep
	}
	if cred != nil && cred.BaseURL == "" {
		plat = mine("key", gw)
	} else {
		plat = dep
	}
	return models, plat
}

func (s *Routes) requireSingle(v *platform.Viewer) error {
	if v.AuthEnabled {
		return platform.KbError(403, "The pw CLI on this server belongs to the deployment; choose a credential of your own under Model access instead.")
	}
	return nil
}

func (s *Routes) get(w http.ResponseWriter, r *http.Request) (any, error) {
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	single := !v.AuthEnabled
	s.p.EnsureFreshAccess(5 * time.Second)
	models, plat := s.sources(v)
	fresh := platform.Q(r, "fresh") == "1"
	out := jsonv.NewObject("single", single, "authEnabled", v.AuthEnabled, "models", models, "platform", plat,
		"deployment", s.deploymentSource(v, single), "gatewayHost", hostOf(s.p.GatewayBase()), "platformOrigin", nullable(s.p.Origin()))
	if single {
		info := s.p.Info(fresh)
		contexts := s.p.ListContexts()
		if contexts == nil {
			contexts = []*jsonv.Object{}
		}
		out.Set("cli", jsonv.NewObject(
			"available", info.Available, "version", nullable(info.Version), "path", nullable(info.Path), "source", info.Source,
			"chosenPath", nullable(s.p.ChosenCLI()), "commonDirs", s.p.CommonCLIDirs(),
			"contexts", contexts,
			"chosen", nullable(s.p.ChosenContext()),
			"active", nullable(s.p.ActiveContextName()),
			"used", nullable(s.p.CredentialContextName()),
			"gatewayFromEnv", s.p.GatewayFromEnv(),
			"envKey", s.p.APIKeyEnv() != "",
		))
		out.Set("login", s.p.Login().JSON())
	} else {
		out.Set("cli", nil)
		out.Set("login", nil)
	}
	return out, nil
}

// cliPath is where the pw CLI is, when the Studio does not find it by
// itself: a full path, or null to forget the choice.
func (s *Routes) cliPath(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := platform.BodyObject(r)
	if err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.requireSingle(v); err != nil {
		return nil, err
	}
	raw, _ := body.Get("path")
	p := ""
	if raw != nil && raw != jsonv.Undefined {
		p = strings.TrimSpace(jsonv.String(raw))
	}
	if _, _, err := s.p.ChooseCLI(p); err != nil {
		return nil, platform.KbError(400, err.Error())
	}
	info := s.p.Info(true)
	if p != "" && (!info.Available || info.Version == "") {
		s.p.ChooseCLI("")
		return nil, platform.KbError(400, p+" did not answer --version as the pw CLI does, so it was not kept.")
	}
	return jsonv.NewObject("cli", info.JSON()), nil
}

// source is which context the Studio uses: a name, or null to follow the
// CLI's own; clearKey also removes a stored key or endpoint, which would
// otherwise keep taking precedence over the context.
func (s *Routes) source(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := platform.BodyObject(r)
	if err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.requireSingle(v); err != nil {
		return nil, err
	}
	name := ""
	if c, _ := body.Get("context"); c != nil && c != jsonv.Undefined {
		name = jsonv.String(c)
	}
	if err := s.p.ChooseContext(name); err != nil {
		return nil, platform.KbError(400, err.Error())
	}
	if c, _ := body.Get("clearKey"); jsonv.Truthy(c) {
		if err := s.p.AccessChanged(true); err != nil {
			return nil, err
		}
		v = s.viewerAfterClear(r, v)
	}
	s.p.InvalidateProviderProbes()
	s.ResetAccounts()
	models, plat := s.sources(v)
	return jsonv.NewObject("ok", true, "chosen", nullable(s.p.ChosenContext()), "active", nullable(s.p.ActiveContextName()),
		"models", models, "platform", plat), nil
}

// viewerAfterClear is the viewer once Node removed the single owner's
// stored key: asked again where Node holds it.
func (s *Routes) viewerAfterClear(r *http.Request, v *platform.Viewer) *platform.Viewer {
	if b := s.p.Node(); b != nil {
		fresh := &platform.Viewer{}
		if err := b.Do(r, http.MethodGet, platform.BridgePrefix+"viewer", nil, fresh); err == nil {
			if string(fresh.KeyStatus) == "null" {
				fresh.KeyStatus = nil
			}
			return fresh
		}
	}
	if v.UserID() != "" {
		return v
	}
	c := *v
	c.Cred = nil
	if c.KeyStatus != nil {
		c.KeyStatus = []byte(`{"mode":"none"}`)
	}
	return &c
}

func (s *Routes) loginStart(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := platform.BodyObject(r)
	if err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.requireSingle(v); err != nil {
		return nil, err
	}
	host, _ := body.Get("host")
	st, err := s.p.StartLogin(jsonv.String(undefToNil(host)), 0, 0)
	if err != nil {
		return nil, platform.KbError(400, err.Error())
	}
	return jsonv.NewObject("login", st.JSON()), nil
}

func undefToNil(v any) any {
	if v == jsonv.Undefined {
		return nil
	}
	return v
}

func (s *Routes) loginGet(w http.ResponseWriter, r *http.Request) (any, error) {
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.requireSingle(v); err != nil {
		return nil, err
	}
	return jsonv.NewObject("login", s.p.Login().JSON()), nil
}

func (s *Routes) loginCancel(w http.ResponseWriter, r *http.Request) (any, error) {
	if _, err := platform.Body(r); err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	if err := s.requireSingle(v); err != nil {
		return nil, err
	}
	return jsonv.NewObject("login", s.p.CancelLogin().JSON()), nil
}

/* ---- the platform API, for subscriptions ---- */

// ownPlatformKey is the platform credential that is the person's own:
// their platform key, or, with sign-in off, whatever the one user's Studio
// runs with. Never the deployment's on a shared deployment.
func (s *Routes) ownPlatformKey(v *platform.Viewer) string {
	if v.Cred != nil && v.Cred.BaseURL == "" {
		return v.Cred.Key
	}
	if !v.AuthEnabled {
		return s.p.GatewayKey()
	}
	return ""
}

func sliceUnits(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

func (s *Routes) call(ctx context.Context, key, method, path string, body any) (any, error) {
	origin := s.p.Origin()
	if origin == "" {
		return nil, platform.KbError(409, "No platform is configured.")
	}
	var rd io.Reader
	hdr := map[string]string{"Authorization": "Bearer " + key, "Accept": "application/json"}
	if body != nil {
		b, err := jsonv.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	res, cancel, err := platform.Request(ctx, method, origin+path, hdr, rd, 15*time.Second)
	if err != nil {
		host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
		if conn := netx.ConnectionProblem(err, host, s.p.Getenv); conn != "" {
			return nil, platform.KbError(503, conn)
		}
		if f := netx.Failure(err); f != nil {
			return nil, platform.KbError(503, netx.UnreachableMessage(host, f))
		}
		msg := "fetch failed"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "The operation was aborted due to timeout"
		}
		return nil, platform.KbError(503, sliceUnits(msg, 200))
	}
	defer cancel()
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	var data any = text
	if d, err := jsonv.Decode(raw); err == nil {
		data = d
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		said := text
		if o := jsonv.Obj(data); o != nil {
			m := jsonv.Path(o, "message")
			if m == nil {
				m = jsonv.Path(o, "error")
			}
			if m == nil {
				m = jsonv.Path(o, "detail")
			}
			said = jsonv.String(m)
		} else if _, ok := data.([]any); ok {
			said = "" // an array is an object to typeof, with no message
		}
		said = sliceUnits(said, 300)
		tail := ""
		if said != "" {
			tail = ": " + said
		}
		st := res.StatusCode
		if st == 401 || st == 403 {
			return nil, platform.KbError(st, "The platform refused this credential ("+itoa(st)+")"+tail+". A platform token or platform API key is needed; an AI gateway key is not enough.")
		}
		code := 502
		if st == 404 {
			code = 404
		}
		return nil, platform.KbError(code, "The platform answered "+itoa(st)+tail)
	}
	return data, nil
}

func itoa(n int) string { return jsonv.FormatNumber(float64(n)) }

var userPrefix = regexp.MustCompile(`^user:`)

func (s *Routes) accountOf(ctx context.Context, key string) (org, user string, err error) {
	k := key
	if len(k) > 12 {
		k = k[len(k)-12:]
	}
	s.mu.Lock()
	hit, ok := s.accounts[k]
	s.mu.Unlock()
	if ok && time.Since(hit.at) < 10*time.Minute {
		return hit.org, hit.user, nil
	}
	w, err := s.call(ctx, key, http.MethodGet, "/api/auth/whoami", nil)
	if err != nil {
		return "", "", err
	}
	o, err := s.call(ctx, key, http.MethodGet, "/api/auth/whoami/organization", nil)
	if err != nil {
		return "", "", err
	}
	who := jsTrim(jsonv.String(w))
	org = jsTrim(jsonv.String(o))
	user = userPrefix.ReplaceAllString(who, "")
	if user == "" || org == "" || (user == who && strings.Contains(who, ":")) {
		return "", "", platform.KbError(409, "This credential does not belong to a platform user.")
	}
	s.mu.Lock()
	s.accounts[k] = account{at: time.Now(), org: org, user: user}
	s.mu.Unlock()
	return org, user, nil
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

var enc = platform.EncodeURIComponent

// get is o[k] with JavaScript's undefined for a missing key.
func get(o any, k string) any {
	ob := jsonv.Obj(o)
	if ob == nil {
		return jsonv.Undefined
	}
	v, ok := ob.Get(k)
	if !ok {
		return jsonv.Undefined
	}
	return v
}

// orNull is v ?? null.
func orNull(v any) any {
	if v == jsonv.Undefined {
		return nil
	}
	return v
}

// coalesce is a ?? b ?? ... for values from get.
func coalesce(vs ...any) any {
	for _, v := range vs {
		if v != nil && v != jsonv.Undefined {
			return v
		}
	}
	return jsonv.Undefined
}

// same is ===, for the JSON values a platform sends.
func same(a, b any) bool {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case nil:
		return b == nil
	}
	return a == b
}

func offered(integrations []any, kind string) any {
	for _, i := range integrations {
		lc := coalesce(get(i, "lifecycle"), "active")
		if same(get(i, "kind"), kind) && same(lc, "active") {
			return i
		}
	}
	return nil
}

func ofKind(providers []any, id any) []any {
	out := []any{}
	if !jsonv.Truthy(undefToNil(id)) {
		return out
	}
	for _, p := range providers {
		if !same(get(p, "catalogEntryId"), id) {
			continue
		}
		out = append(out, jsonv.NewObject("name", get(p, "name"), "displayName", orNull(coalesce(get(p, "displayName"))),
			"status", orNull(coalesce(get(p, "status"))), "account", orNull(coalesce(get(p, "accountEmail"), get(p, "accountLogin")))))
	}
	return out
}

func (s *Routes) subscriptions(w http.ResponseWriter, r *http.Request) (any, error) {
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	key := s.ownPlatformKey(v)
	if key == "" {
		return jsonv.NewObject("available", false, "reason", "Subscriptions connect to your own ACTIVATE account, which needs your platform token or platform API key here first."), nil
	}
	s.p.EnsureFreshAccess(5 * time.Second)
	org, user, err := s.accountOf(r.Context(), key)
	if err != nil {
		return nil, err
	}
	var (
		wg                      sync.WaitGroup
		integrations, providers []any
		github                  any
		ierr                    error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		d, err := s.call(r.Context(), key, http.MethodGet, "/api/organizations/"+enc(org)+"/ai-integrations", nil)
		if err != nil {
			ierr = err
			return
		}
		if a := jsonv.Arr(jsonv.Path(d, "integrations")); a != nil {
			integrations = a
		} else if a, ok := d.([]any); ok && jsonv.Path(d, "integrations") == nil {
			integrations = a
		}
	}()
	go func() {
		defer wg.Done()
		d, err := s.call(r.Context(), key, http.MethodGet, "/api/organizations/"+enc(org)+"/users/"+enc(user)+"/ai-providers", nil)
		if err == nil {
			providers, _ = d.([]any)
		}
	}()
	go func() {
		defer wg.Done()
		d, err := s.call(r.Context(), key, http.MethodGet, "/api/integrations/github/oauth/status", nil)
		if err == nil {
			github = d
		}
	}()
	wg.Wait()
	if ierr != nil {
		return nil, ierr
	}
	codex := offered(integrations, "codex-subscription")
	copilot := offered(integrations, "github-copilot")
	var gh any
	if jsonv.Truthy(github) {
		gh = jsonv.NewObject("linked", jsonv.Truthy(undefToNil(get(github, "username"))), "username", orNull(get(github, "username")),
			"appConfigured", jsonv.Truthy(undefToNil(get(github, "appConfigured"))), "tokensAllowed", jsonv.Truthy(undefToNil(get(github, "tokensAllowed"))))
	}
	return jsonv.NewObject(
		"available", true, "user", user, "org", org,
		"codex", jsonv.NewObject("offered", codex != nil, "providers", ofKind(providers, get(codex, "id"))),
		"copilot", jsonv.NewObject("offered", copilot != nil, "providers", ofKind(providers, get(copilot, "id")), "github", gh),
	), nil
}

var (
	codexName   = regexp.MustCompile(`[^a-z0-9]`)
	copilotName = regexp.MustCompile(`[^a-z0-9-]`)
)

// providerName is String(name ?? fallback).toLowerCase() kept to re's
// complement, at most 40 characters, or fallback.
func providerName(v any, re *regexp.Regexp, fallback string) string {
	v = undefToNil(v)
	raw := fallback
	if v != nil {
		raw = jsonv.String(v)
	}
	n := sliceUnits(re.ReplaceAllString(strings.ToLower(raw), ""), 40)
	if n == "" {
		return fallback
	}
	return n
}

func (s *Routes) codex(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := platform.BodyObject(r)
	if err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	key := s.ownPlatformKey(v)
	if key == "" {
		return nil, platform.KbError(409, "Add your platform token or platform API key first; the subscription connects to your own ACTIVATE account.")
	}
	n, _ := body.Get("name")
	name := providerName(n, codexName, "chatgpt")
	org, user, err := s.accountOf(r.Context(), key)
	if err != nil {
		return nil, err
	}
	req := jsonv.NewObject("name", name)
	if rc, _ := body.Get("reconnect"); jsonv.Truthy(undefToNil(rc)) {
		req.Set("reconnect", true)
	}
	d, err := s.call(r.Context(), key, http.MethodPost, "/api/organizations/"+enc(org)+"/users/"+enc(user)+"/codex-device-authorizations", req)
	if err != nil {
		return nil, err
	}
	id, code, u := get(d, "id"), get(d, "userCode"), get(d, "verificationUrl")
	if !jsonv.Truthy(undefToNil(id)) || !jsonv.Truthy(undefToNil(code)) || !jsonv.Truthy(undefToNil(u)) {
		return nil, platform.KbError(502, "The platform did not return a sign-in code.")
	}
	interval := jsNumber(get(d, "pollIntervalSeconds"))
	if interval != interval || interval == 0 { // NaN or 0: || 5
		interval = 5
	}
	if interval < 2 {
		interval = 2
	}
	return jsonv.NewObject("id", id, "url", u, "code", code, "expiresAt", orNull(coalesce(get(d, "expiresAt"))),
		"intervalSec", interval, "provider", coalesceOr(get(d, "providerName"), name)), nil
}

func coalesceOr(v any, fallback any) any {
	if c := coalesce(v); c != jsonv.Undefined {
		return c
	}
	return fallback
}

// jsNumber is Number(v) for JSON values; NaN where JavaScript gives NaN.
func jsNumber(v any) float64 {
	nan := func() float64 { var z float64; return z / z }()
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		t := jsTrim(x)
		if t == "" {
			return 0
		}
		if f, ok := jsonv.Num(parseNum(t)); ok {
			return f
		}
		return nan
	}
	return nan
}

func parseNum(s string) any {
	v, err := jsonv.DecodeString(s)
	if err != nil {
		return nil
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return nil
}

func (s *Routes) codexPoll(w http.ResponseWriter, r *http.Request) (any, error) {
	if _, err := platform.Body(r); err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	key := s.ownPlatformKey(v)
	if key == "" {
		return nil, platform.KbError(409, "No platform credential of your own.")
	}
	org, user, err := s.accountOf(r.Context(), key)
	if err != nil {
		return nil, err
	}
	d, err := s.call(r.Context(), key, http.MethodPost, "/api/organizations/"+enc(org)+"/users/"+enc(user)+"/codex-device-authorizations/"+enc(r.PathValue("id")), nil)
	if err != nil {
		return nil, err
	}
	connected := same(get(d, "status"), "connected")
	if connected {
		s.p.InvalidateProviderProbes()
	}
	status := "pending"
	if connected {
		status = "connected"
	}
	var provider any
	if pv := get(d, "provider"); jsonv.Truthy(undefToNil(pv)) {
		provider = jsonv.NewObject("name", get(pv, "name"), "displayName", orNull(coalesce(get(pv, "displayName"))), "account", orNull(coalesce(get(pv, "accountEmail"))))
	}
	return jsonv.NewObject("status", status, "provider", provider), nil
}

func (s *Routes) copilot(w http.ResponseWriter, r *http.Request) (any, error) {
	body, err := platform.BodyObject(r)
	if err != nil {
		return nil, err
	}
	v, err := s.p.ViewerOf(r)
	if err != nil {
		return nil, err
	}
	key := s.ownPlatformKey(v)
	if key == "" {
		return nil, platform.KbError(409, "Add your platform token or platform API key first; Copilot connects to your own ACTIVATE account.")
	}
	n, _ := body.Get("name")
	name := providerName(n, copilotName, "copilot")
	org, user, err := s.accountOf(r.Context(), key)
	if err != nil {
		return nil, err
	}
	d, err := s.call(r.Context(), key, http.MethodGet, "/api/organizations/"+enc(org)+"/ai-integrations", nil)
	if err != nil {
		return nil, err
	}
	entry := offered(jsonv.Arr(jsonv.Path(d, "integrations")), "github-copilot")
	if entry == nil {
		return nil, platform.KbError(409, "This platform does not offer GitHub Copilot.")
	}
	p, err := s.call(r.Context(), key, http.MethodPost, "/api/organizations/"+enc(org)+"/users/"+enc(user)+"/ai-providers",
		jsonv.NewObject("name", name, "catalogEntryId", get(entry, "id"), "displayName", "GitHub Copilot"))
	if err != nil {
		return nil, err
	}
	s.p.InvalidateProviderProbes()
	return jsonv.NewObject("provider", jsonv.NewObject("name", coalesceOr(get(p, "name"), name),
		"displayName", orNull(coalesce(get(p, "displayName"))), "account", orNull(coalesce(get(p, "accountLogin"))))), nil
}
