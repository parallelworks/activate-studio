package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

/* ---- the model listing, kept per credential ---- */

const (
	listingFresh = 60 * time.Second
	listingKeep  = 30 * time.Minute
)

type listing struct {
	models      []*jsonobj.Object
	impaired    []Impaired
	unreachable json.RawMessage
}

type listingHit struct {
	at time.Time
	v  *listing
}

type listingRun struct {
	done chan struct{}
	v    *listing
	err  error
}

type listingCache struct {
	mu         sync.Mutex
	hits       map[string]listingHit
	refreshing map[string]*listingRun
}

func newListingCache() *listingCache {
	return &listingCache{hits: map[string]listingHit{}, refreshing: map[string]*listingRun{}}
}

func (c *listingCache) clear() {
	c.mu.Lock()
	c.hits = map[string]listingHit{}
	c.mu.Unlock()
}

// listingReply is a listing failure answered as an ordinary payload.
type listingReply struct{ payload *jsonobj.Object }

func (l *listingReply) Error() string { return "listing reply" }

func lastN(s string, n int) string {
	u := []rune(s)
	if len(u) <= n {
		return s
	}
	return string(u[len(u)-n:])
}

func (m *Module) listingKey(cred *modelkeys.Cred, viewer string) string {
	if cred == nil {
		return viewer + "|" + lastN(m.GatewayKey(), 8) + "|" + m.gatewayBase()
	}
	return viewer + "|" + lastN(cred.Key, 8) + "|" + cred.Base()
}

var (
	rejected401Re  = regexp.MustCompile(`(?i)\b(401|403)\b|unauthorized|forbidden`)
	lockedRe       = regexp.MustCompile(`(?i)401|403|locked|unauthorized`)
	localBaseRe    = regexp.MustCompile(`(^|//)(localhost|127\.0\.0\.1)`)
	studioRagIDRe  = regexp.MustCompile(`/studio-(agent|rag)$`)
	sessionModelRe = regexp.MustCompile(`^session:[^:]+:([^/]+)/(.+)$`)
	engineName     = map[string]string{"vllm": "vLLM", "ollama": "Ollama", "llamacpp": "llama.cpp"}
)

func objStr(o *jsonobj.Object, k string) string { return jsString(decode(o.Get(k))) }

func decode(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// labelled is the provider mapping of a model without provider fields.
func labelled(mo *jsonobj.Object, provider, engine string) *jsonobj.Object {
	c := mo.Clone()
	_ = c.SetValue("provider", provider)
	_ = c.SetValue("provider_type", "local")
	_ = c.SetValue("provider_name", engine)
	_ = c.SetValue("name", orStr(objStr(mo, "name"), objStr(mo, "id")))
	cw := mo.Get("context_window")
	if jsonobj.IsNullish(cw) {
		cw = mo.Get("max_model_len")
	}
	if jsonobj.IsNullish(cw) {
		// context_window: undefined leaves it out of the JSON
		c.Delete("context_window")
	} else {
		c.Set("context_window", cw)
	}
	return c
}

func (m *Module) computeListing(ctx context.Context, cred *modelkeys.Cred, r *http.Request) (*listing, error) {
	sidecar := m.LLM.SidecarModels(ctx)
	var wire *llm.Listing
	if m.GatewayKey() != "" || cred != nil {
		var err error
		key, base := "", ""
		var ep *modelkeys.Endpoint
		if cred != nil {
			key, base, ep = cred.Key, cred.Base(), cred.Endpoint
		}
		wire, err = m.LLM.ListModels(ctx, key, base, ep)
		if err != nil {
			msg := err.Error()
			unlock := llm.UnlockURL(msg)
			if cred != nil && cred.Base() != "" && (unlock != nil || lockedRe.MatchString(msg)) {
				host := cred.Base()
				if u, e := url.Parse(host); e == nil {
					host = u.Host
				}
				text := "Your provider key is locked or rejected by " + host + "."
				if unlock != nil {
					text += " Unlock it here and reload: " + *unlock
				}
				text += " The Settings, Model access page shows the same link and a Re-check."
				return nil, &listingReply{reqbody.O("models", objs(sidecar), "unreachableSessions", []any{}, "error", text)}
			}
			if rejected401Re.MatchString(msg) {
				var kind, exp *string
				if cred != nil && user(r) != nil {
					st := m.Keys.Status(user(r).ID)
					if st.Kind != "" {
						kind = &st.Kind
					}
					exp = st.CredExpiresAt
				}
				owner := "deployment"
				if cred != nil {
					owner = "personal"
				} else if !m.A.Cfg.AuthEnabled() && pwAPIKey() == "" && m.contextName() != nil {
					owner = "cli"
				}
				return nil, &listingReply{reqbody.O("models", objs(sidecar), "unreachableSessions", []any{}, "credential", "rejected",
					"error", CredentialRejectionMessage(owner, kind, exp, m.contextName(), time.Now()))}
			}
			var un *llm.Unreachable
			if errors.As(err, &un) && len(sidecar) == 0 {
				return nil, &listingReply{reqbody.O("models", []any{}, "unreachableSessions", []any{}, "error", un.Msg, "offline", true)}
			}
			if len(sidecar) == 0 {
				return nil, err
			}
			m.A.Log.Warn("gateway model listing failed; serving the local model only", "err", msg)
			wire = nil
		}
	}
	var models []*jsonobj.Object
	if wire != nil {
		models = wire.Items
	}
	if os.Getenv("PW_ALLOCATION") == "" {
		models = filter(models, func(o *jsonobj.Object) bool { return !strings.HasPrefix(objStr(o, "id"), "org:") })
	}
	models = filter(models, func(o *jsonobj.Object) bool {
		id := objStr(o, "id")
		return !(strings.HasPrefix(id, "session:") && studioRagIDRe.MatchString(id))
	})
	base := m.gatewayBase()
	if cred != nil && cred.Base() != "" {
		base = cred.Base()
	}
	localBase := localBaseRe.MatchString(base)
	endpointHost := "local"
	if u, err := url.Parse(base); err == nil && u.Scheme != "" {
		endpointHost = u.Host
	}
	for i, mo := range models {
		if jsTruthy(decode(mo.Get("provider"))) || jsTruthy(decode(mo.Get("provider_name"))) {
			continue
		}
		owned := objStr(mo, "owned_by")
		engine := engineName[strings.ToLower(owned)]
		if engine == "" {
			engine = orStr(owned, objStr(mo, "publisher"))
			if engine == "" {
				if localBase {
					engine = "local"
				} else {
					engine = endpointHost
				}
			}
		}
		provider := engine
		if localBase {
			provider = "Served here (" + engine + ")"
		}
		models[i] = labelled(mo, provider, engine)
	}
	if len(sidecar) > 0 {
		served := map[string]bool{}
		for _, s := range sidecar {
			served[objStr(s, "id")] = true
		}
		models = filter(models, func(o *jsonobj.Object) bool {
			p := sessionModelRe.FindStringSubmatch(objStr(o, "id"))
			if p == nil {
				return true
			}
			if m.LLM.SidecarEndpoint != "" {
				return p[1] != m.LLM.SidecarEndpoint
			}
			return !served[p[2]]
		})
	}
	for _, s := range sidecar {
		owned := objStr(s, "owned_by")
		engine := engineName[strings.ToLower(owned)]
		if engine == "" {
			engine = orStr(owned, "local")
		}
		models = append(models, labelled(s, "Served here ("+engine+")", engine))
	}
	prefixes := map[string]string{}
	var order []string
	if cred == nil || cred.Base() == "" {
		for _, mo := range models {
			id := objStr(mo, "id")
			if s := strings.Index(id, "/"); s > 0 {
				p := id[:s]
				if _, seen := prefixes[p]; !seen && m.LLM.Probeable(p) {
					prefixes[p] = id
					order = append(order, p)
				}
			}
		}
	}
	verdicts := map[string]llm.Verdict{}
	if len(order) > 0 {
		var vmu sync.Mutex
		var wg sync.WaitGroup
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6500*time.Millisecond)
		key := ""
		if cred != nil {
			key = cred.Key
		}
		for _, p := range order {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				v := m.LLM.ProbeProvider(pctx, p, prefixes[p], key)
				vmu.Lock()
				verdicts[p] = v
				vmu.Unlock()
			}(p)
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-pctx.Done():
		}
		cancel()
		vmu.Lock()
		vs := map[string]llm.Verdict{}
		for k, v := range verdicts {
			vs[k] = v
		}
		vmu.Unlock()
		verdicts = vs
	}
	marked, impaired := markImpaired(models, verdicts)
	l := &listing{models: marked, impaired: impaired}
	if wire != nil {
		l.unreachable = wire.UnreachableSession
	}
	return l, nil
}

func filter(xs []*jsonobj.Object, keep func(*jsonobj.Object) bool) []*jsonobj.Object {
	out := []*jsonobj.Object{}
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

func objs(xs []*jsonobj.Object) []*jsonobj.Object {
	if xs == nil {
		return []*jsonobj.Object{}
	}
	return xs
}

func (m *Module) personalRequired(cred *modelkeys.Cred) bool {
	return m.eff().RequirePersonalKey && m.A.Cfg.AuthEnabled() && !m.Keys.Disabled() && cred == nil && !m.Keys.Shared("").Active
}

func (m *Module) models(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	if !m.eff().AiEnabled {
		return reqbody.OK(w, reqbody.O("models", []any{}, "unreachableSessions", []any{}, "error", AIOff))
	}
	m.refreshAccess(r, 8000)
	cred := m.cred(r)
	if m.GatewayKey() == "" && cred == nil && !m.LLM.SidecarConfigured() {
		return reqbody.OK(w, reqbody.O("models", []any{}, "unreachableSessions", []any{}, "error", "no gateway credential: set PW_API_KEY or authenticate the pw CLI"))
	}
	forConfig := r.URL.Query().Get("config") == "1"
	if !forConfig && m.personalRequired(cred) {
		return reqbody.OK(w, reqbody.O("models", []any{}, "unreachableSessions", []any{}, "error", "This deployment requires your own model credential: add your API key or platform token in Settings, Model access."))
	}
	viewer := ""
	if u := user(r); u != nil {
		viewer = strings.ToLower(u.Username)
	}
	key := m.listingKey(cred, viewer)
	c := m.listings
	if r.URL.Query().Get("refresh") == "1" {
		m.LLM.InvalidateProbes()
		c.mu.Lock()
		delete(c.hits, key)
		c.mu.Unlock()
	}
	compute := func() *listingRun {
		run := &listingRun{done: make(chan struct{})}
		c.refreshing[key] = run
		go func() {
			v, err := m.computeListing(context.WithoutCancel(r.Context()), cred, r)
			c.mu.Lock()
			if err == nil {
				c.hits[key] = listingHit{time.Now(), v}
			}
			delete(c.refreshing, key)
			c.mu.Unlock()
			run.v, run.err = v, err
			close(run.done)
		}()
		return run
	}
	c.mu.Lock()
	hit, ok := c.hits[key]
	var l *listing
	var run *listingRun
	if ok && time.Since(hit.at) < listingKeep {
		l = hit.v
		if time.Since(hit.at) > listingFresh && c.refreshing[key] == nil {
			compute()
		}
	} else if run = c.refreshing[key]; run == nil {
		run = compute()
	}
	c.mu.Unlock()
	if l == nil {
		<-run.done
		if run.err != nil {
			var lr *listingReply
			if errors.As(run.err, &lr) {
				return reqbody.OK(w, lr.payload)
			}
			return run.err
		}
		l = run.v
	}
	models := make([]*jsonobj.Object, len(l.models))
	for i, mo := range l.models {
		if f, has := m.LLM.ModelFailure(objStr(mo, "id")); has {
			c := mo.Clone()
			_ = c.SetValue("callable", false)
			_ = c.SetValue("last_error", reqbody.O("at", f.At, "auth", f.Auth, "status", f.Status))
			mo = c
		}
		models[i] = mo
	}
	sort.SliceStable(models, func(i, j int) bool {
		return !strings.HasPrefix(objStr(models[i], "id"), "org:") && strings.HasPrefix(objStr(models[j], "id"), "org:")
	})
	imp := []*jsonobj.Object{}
	for _, i := range l.impaired {
		imp = append(imp, i.object())
	}
	un := l.unreachable
	if jsonobj.IsNullish(un) {
		un = json.RawMessage("[]")
	}
	return reqbody.OK(w, reqbody.O("models", models, "impaired", imp, "unreachableSessions", un))
}

// retryChoices is the routes' retryChoices(req, failed).
func (m *Module) retryChoices(r *http.Request, failed string) []choice {
	viewer := ""
	if u := user(r); u != nil {
		viewer = strings.ToLower(u.Username)
	}
	key := m.listingKey(m.Keys.Resolve(userID(r)), viewer)
	m.listings.mu.Lock()
	hit, ok := m.listings.hits[key]
	m.listings.mu.Unlock()
	var out []choice
	if !ok {
		return out
	}
	for _, mo := range hit.v.models {
		id := objStr(mo, "id")
		if id == "" || id == failed {
			continue
		}
		if f, has := m.LLM.ModelFailure(id); has {
			if t, ok := parseDate(f.At); ok && time.Since(t) < 10*time.Minute {
				continue
			}
		}
		out = append(out, choice{id, orStr(objStr(mo, "name"), id)})
		if len(out) == 4 {
			break
		}
	}
	return out
}

/* ---- the footer's health line ---- */

func (m *Module) health(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if !m.eff().AiEnabled {
		return reqbody.OK(w, reqbody.O("ok", false, "status", "off", "models", 0, "gatewayHost", "", "message", AIOff, "lastCallFailure", nil, "checkedAt", now))
	}
	cred := m.cred(r)
	if m.personalRequired(cred) {
		return reqbody.OK(w, reqbody.O("ok", false, "status", "key-required", "models", 0, "gatewayHost", "",
			"message", "This deployment requires your own model credential; add it in Settings, Model access.", "lastCallFailure", nil, "checkedAt", now))
	}
	if r.URL.Query().Get("fresh") == "1" {
		m.LLM.InvalidateProbes()
	}
	m.refreshAccess(r, 5000)
	key, base := "", ""
	var ep *modelkeys.Endpoint
	if cred != nil {
		key, base, ep = cred.Key, cred.Base(), cred.Endpoint
	}
	h := m.LLM.Health(r.Context(), key, base, false, ep)
	o := h.Object()
	var access any
	if cred != nil && cred.Base() != "" {
		access = false
	} else if cred != nil && h.Status != "offline" {
		access = nil
		host := m.gatewayBase()
		if u, err := url.Parse(host); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			req, _ := http.NewRequestWithContext(ctx, "GET", u.Scheme+"://"+u.Host+"/api/workflows", nil)
			req.Header.Set("Authorization", "Bearer "+cred.Key)
			if res, err := m.LLM.HTTP.Do(req); err == nil {
				access = res.StatusCode >= 200 && res.StatusCode < 300
				res.Body.Close()
			}
			cancel()
		}
	}
	reqbody.Put(o, "platformAccess", access)
	return reqbody.OK(w, o)
}

/* ---- the personal model key ---- */

func (m *Module) gatewayHostName() string {
	b := m.gatewayBase()
	if u, err := url.Parse(b); err == nil && u.Host != "" {
		return u.Host
	}
	return b
}

func putStatus(o *jsonobj.Object, st modelkeys.Status) {
	for _, kv := range st.Fields() {
		reqbody.Put(o, kv[0].(string), kv[1])
	}
}

func (m *Module) keyStatusPayload(sub string) *jsonobj.Object {
	o := reqbody.O("authEnabled", true, "verified", true, "gatewayHost", m.gatewayHostName())
	putStatus(o, m.Keys.Status(sub))
	reqbody.Put(o, "shared", m.Keys.Shared(sub))
	reqbody.Put(o, "sharingAllowed", m.eff().AllowKeySharing)
	return o
}

func (m *Module) keyOwner(r *http.Request) string {
	if id := userID(r); id != "" {
		return id
	}
	if m.A.Cfg.AuthEnabled() || m.Keys.Disabled() {
		return ""
	}
	return modelkeys.SingleOwner
}

func (m *Module) statusFor(r *http.Request, owner string) *jsonobj.Object {
	if user(r) != nil {
		return m.keyStatusPayload(owner)
	}
	o := reqbody.O("authEnabled", false, "verified", true, "single", true, "gatewayHost", m.gatewayHostName())
	putStatus(o, m.Keys.Status(owner))
	reqbody.Put(o, "shared", reqbody.O("active", false))
	reqbody.Put(o, "sharingAllowed", false)
	return o
}

func (m *Module) setKeyCookies(w http.ResponseWriter, sub string) {
	for _, c := range KeyCookieJar(m.Keys.Seal(sub)) {
		w.Header().Add("Set-Cookie", c)
	}
}

func clearKeyCookies(w http.ResponseWriter) {
	for _, c := range KeyCookieJar(nil) {
		w.Header().Add("Set-Cookie", c)
	}
}

// optionsFor is the routes' optionsFor(body, baseUrl, stored).
func optionsFor(b reqbody.Body, baseURL *string, stored *modelkeys.Cred) (any, error) {
	if baseURL == nil {
		return nil, nil
	}
	same := stored != nil && stored.BaseURL != nil && *stored.BaseURL == *baseURL
	if !b.Has("endpoint") {
		if same && stored.Endpoint != nil {
			return stored.Endpoint, nil
		}
		return nil, nil
	}
	var st *modelkeys.Endpoint
	if same {
		st = stored.Endpoint
	}
	e, err := modelkeys.NormalizeEndpoint(b.Get("endpoint"), st)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, nil
	}
	return e, nil
}

func (m *Module) keyRoutes(h func(string, func(http.ResponseWriter, *http.Request) error)) {
	notVerified := "Personal keys need a platform-verified identity; open the app through the platform."
	guard := func(fn func(http.ResponseWriter, *http.Request) error) func(http.ResponseWriter, *http.Request) error {
		return func(w http.ResponseWriter, r *http.Request) error {
			if !m.known() {
				m.toNode(w, r, "the viewer is identified by Node")
				return nil
			}
			return fn(w, r)
		}
	}
	h("GET /api/me/model-key", guard(func(w http.ResponseWriter, r *http.Request) error {
		shared := m.eff().ChatSharedHistory
		host := m.gatewayHostName()
		if m.Keys.Disabled() {
			return reqbody.OK(w, reqbody.O("authEnabled", false, "verified", false, "mode", "none", "gatewayHost", host, "sharedHistory", shared))
		}
		if !m.A.Cfg.AuthEnabled() {
			o := m.statusFor(r, modelkeys.SingleOwner)
			reqbody.Put(o, "sharedHistory", shared)
			return reqbody.OK(w, o)
		}
		u := user(r)
		if u == nil {
			return reqbody.OK(w, reqbody.O("authEnabled", true, "verified", false, "mode", "none", "gatewayHost", host, "sharedHistory", shared))
		}
		m.Keys.Hydrate(u.ID, r.Header.Get("Cookie"))
		o := m.keyStatusPayload(u.ID)
		reqbody.Put(o, "sharedHistory", shared)
		return reqbody.OK(w, o)
	}))
	set := func(put bool) func(http.ResponseWriter, *http.Request) error {
		return guard(func(w http.ResponseWriter, r *http.Request) error {
			owner := m.keyOwner(r)
			if owner == "" {
				return apierr.New(403, "%s", notVerified)
			}
			b, err := reqbody.Read(r)
			if err != nil {
				return err
			}
			m.Keys.Hydrate(owner, r.Header.Get("Cookie"))
			if put {
				key := jsTrim(b.Str("key"))
				if key == "" {
					return apierr.New(400, "key required")
				}
				if err := m.Keys.Set(owner, key, nil, nil); err != nil {
					return apierr.New(400, "%s", err.Error())
				}
			} else {
				baseURL := modelkeys.NormalizeBaseURL(b.Get("baseUrl"))
				stored := m.Keys.Resolve(owner)
				key := jsTrim(b.Str("key"))
				if key == "" && baseURL != nil && stored != nil && stored.BaseURL != nil && *stored.BaseURL == *baseURL {
					key = stored.Key
				}
				if key == "" {
					return apierr.New(400, "key required")
				}
				opts, err := optionsFor(b, baseURL, stored)
				if err == nil {
					err = m.Keys.Set(owner, key, baseURL, opts)
				}
				if err != nil {
					return apierr.New(400, "%s", err.Error())
				}
				m.LLM.InvalidateProbes()
				m.listings.clear()
			}
			if user(r) != nil {
				if p, has := b.M()["persist"]; has && p == false {
					clearKeyCookies(w)
				} else {
					m.setKeyCookies(w, owner)
				}
			}
			return reqbody.OK(w, m.statusFor(r, owner))
		})
	}
	h("POST /api/me/model-key", set(false))
	h("PUT /api/me/model-key", set(true))
	clear := guard(func(w http.ResponseWriter, r *http.Request) error {
		owner := m.keyOwner(r)
		if owner == "" {
			return apierr.New(403, "not verified")
		}
		m.Keys.Clear(owner)
		if user(r) != nil {
			clearKeyCookies(w)
		}
		return reqbody.OK(w, m.statusFor(r, owner))
	})
	h("POST /api/me/model-key/clear", clear)
	h("DELETE /api/me/model-key", clear)
	h("POST /api/me/model-key/share", guard(func(w http.ResponseWriter, r *http.Request) error {
		u := user(r)
		if u == nil {
			return apierr.New(403, "not verified")
		}
		if !m.eff().AllowKeySharing {
			return apierr.New(403, "this deployment does not allow sharing a personal key")
		}
		m.Keys.Hydrate(u.ID, r.Header.Get("Cookie"))
		if err := m.Keys.Share(u.ID, orStr(u.Name, u.Username)); err != nil {
			return apierr.New(400, "%s", err.Error())
		}
		return reqbody.OK(w, m.keyStatusPayload(u.ID))
	}))
	h("POST /api/me/model-key/unshare", guard(func(w http.ResponseWriter, r *http.Request) error {
		u := user(r)
		if u == nil {
			return apierr.New(403, "not verified")
		}
		m.Keys.ClearShared()
		return reqbody.OK(w, m.keyStatusPayload(u.ID))
	}))
	h("POST /api/me/model-key/test", guard(func(w http.ResponseWriter, r *http.Request) error {
		m.LLM.InvalidateProbes()
		m.listings.clear()
		owner := m.keyOwner(r)
		if owner == "" {
			return apierr.New(403, "not verified")
		}
		b, err := reqbody.Read(r)
		if err != nil {
			return err
		}
		m.Keys.Hydrate(owner, r.Header.Get("Cookie"))
		stored := m.Keys.Resolve(owner)
		typed := jsTrim(b.Str("key"))
		baseURL := modelkeys.NormalizeBaseURL(b.Get("baseUrl"))
		if baseURL == nil && stored != nil {
			baseURL = stored.BaseURL
		}
		candidate := typed
		if candidate == "" && stored != nil && ptrEq(stored.BaseURL, baseURL) {
			candidate = stored.Key
		}
		var model *string
		if s, ok := b.Get("model").(string); ok {
			model = &s
		}
		tools := b.Get("tools") != false
		if candidate == "" {
			if stored != nil {
				where := "the platform"
				if stored.BaseURL != nil {
					where = *stored.BaseURL
				}
				bu := "null"
				if baseURL != nil {
					bu = *baseURL
				}
				return apierr.New(400, "enter the key for %s; the stored key is for %s", bu, where)
			}
			if !reqbody.Truthy(b.Get("key")) && !m.A.Cfg.AuthEnabled() {
				m.refreshAccess(r, 15000)
				h := m.LLM.Health(r.Context(), "", "", true, nil)
				return reqbody.OK(w, m.withToolCheck(r, h, "", "", nil, model, tools))
			}
			return apierr.New(400, "no key to test")
		}
		bu := ""
		if baseURL != nil {
			bu = *baseURL
			if !baseURLRe.MatchString(bu) {
				return apierr.New(400, "the base URL must start with http:// or https://")
			}
		}
		opts, err := optionsFor(b, baseURL, stored)
		if err != nil {
			return apierr.New(400, "%s", err.Error())
		}
		ep, _ := opts.(*modelkeys.Endpoint)
		h := m.LLM.Health(r.Context(), candidate, bu, true, ep)
		return reqbody.OK(w, m.withToolCheck(r, h, candidate, bu, ep, model, tools))
	}))
}

var baseURLRe = regexp.MustCompile(`(?i)^https?://[^/\s]+`)

func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

/* ---- suggestions ---- */

func (m *Module) suggestions(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	id := r.URL.Query().Get("conversation")
	var c *conversations.Conversation
	if id != "" {
		c = m.Conv.Get(id)
	}
	if c == nil {
		return reqbody.OK(w, reqbody.O("after", nil, "suggestions", []any{}))
	}
	// A conversation is owned by username, as the stream records it; the
	// viewer's id is another form of it on a platform sign-in.
	if u := user(r); c.OwnerName() != "" && u != nil && u.Username != "" && c.OwnerName() != u.Username && !m.eff().ChatSharedHistory {
		return apierr.New(403, "not your conversation")
	}
	return reqbody.OK(w, SuggestNext(c))
}
