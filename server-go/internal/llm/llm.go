// Package llm is server/src/chat/gateway.ts: the client of the
// OpenAI-compatible model gateway (the platform's, a model served beside
// the Studio, or an endpoint of the user's own), its listing, health and
// tool-calling checks, provider probes, and the one streamed chat turn the
// assistant's loop is built from. Requests carry secrets: never log a
// header or a body.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
)

// DefaultGateway is config.ts's last fallback for GATEWAY_BASE.
const DefaultGateway = "https://activate.parallel.works/api/openai/v1"

// Client is the module's state: one per server.
type Client struct {
	// Base is GATEWAY_BASE as it is now (a pw CLI context choice moves it).
	Base func() string
	// Key is gatewayKey(): the shared key, PW_API_KEY, or the pw CLI's.
	Key func() string
	// HTTP is the client every request goes through.
	HTTP *http.Client

	SidecarBase     string
	SidecarKey      string
	SidecarEndpoint string
	// IdleTimeout is STUDIO_STREAM_IDLE_MS (300 s).
	IdleTimeout time.Duration
	probePat    *regexp.Regexp

	mu          sync.Mutex
	sidecarIDs  map[string]bool
	lastFailure *CallFailure
	failures    map[string]ModelFailure
	probes      map[string]probeHit
	pings       map[string]pingHit
	now         func() time.Time
}

// CallFailure is lastCallFailure.
type CallFailure struct {
	At      string `json:"at"`
	Message string `json:"message"`
	Auth    bool   `json:"auth"`
}

// ModelFailure is a model's last failed call.
type ModelFailure struct {
	At      string `json:"at"`
	Message string `json:"message"`
	Auth    bool   `json:"auth"`
	Status  int    `json:"status"`
}

type probeHit struct {
	at time.Time
	v  Verdict
}

type pingHit struct {
	at     time.Time
	status int
	text   string
}

// New is the client. base and key are read at each call.
func New(base, key func() string) *Client {
	idle := 300 * time.Second
	if n, err := strconv.Atoi(os.Getenv("STUDIO_STREAM_IDLE_MS")); err == nil && n > 0 {
		idle = time.Duration(n) * time.Millisecond
	}
	pat := os.Getenv("STUDIO_PROBE_PROVIDERS")
	if pat == "" {
		pat = "genai"
	}
	re, err := regexp.Compile("(?i)" + pat)
	if err != nil {
		re = regexp.MustCompile("(?i)genai")
	}
	sk := os.Getenv("SIDECAR_MODEL_KEY")
	if sk == "" {
		sk = "local"
	}
	return &Client{
		Base: base, Key: key, HTTP: &http.Client{},
		SidecarBase:     strings.TrimRight(os.Getenv("SIDECAR_MODEL_URL"), "/"),
		SidecarKey:      sk,
		SidecarEndpoint: os.Getenv("SIDECAR_ENDPOINT"),
		IdleTimeout:     idle, probePat: re,
		sidecarIDs: map[string]bool{}, failures: map[string]ModelFailure{}, probes: map[string]probeHit{}, pings: map[string]pingHit{},
		now: time.Now,
	}
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Configured is gatewayConfigured().
func (c *Client) Configured() bool { return c.Key() != "" }

// GatewayHost is the host of GATEWAY_BASE, or the base as written.
func (c *Client) GatewayHost() string { return HostOf(c.Base()) }

// HostOf is new URL(u).host, or u when it does not parse.
func HostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return u
	}
	return p.Host
}

/* ---- errors ---- */

// Unreachable is GatewayUnreachable: the host could not be reached.
type Unreachable struct{ Msg string }

func (e *Unreachable) Error() string { return e.Msg }

// AuthError is GatewayAuthError: the credential was refused.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// StreamedError is StreamedTurnError: an error inside a stream after its
// 200, or the error field of an unstreamed answer.
type StreamedError struct{ Msg string }

func (e *StreamedError) Error() string { return e.Msg }

// Headers is modelHeaders(key, endpoint): the endpoint's extra headers and
// the key in the header it takes it in.
func Headers(key string, ep *modelkeys.Endpoint) http.Header {
	h := http.Header{}
	if ep != nil {
		for _, x := range ep.Headers {
			h.Set(x.Name, x.Value)
		}
	}
	switch ep.AuthOr() {
	case "bearer":
		h.Set("Authorization", "Bearer "+key)
	case "api-key":
		h.Set("api-key", key)
	case "x-api-key":
		h.Set("x-api-key", key)
	}
	return h
}

// WithQuery is withQuery(url, endpoint).
func WithQuery(u string, ep *modelkeys.Endpoint) string {
	if ep == nil || ep.Query == "" {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&" + ep.Query
	}
	return u + "?" + ep.Query
}

// reach is a request whose network failure reads as a sentence about the
// host.
func (c *Client) reach(req *http.Request) (*http.Response, error) {
	res, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
			return nil, err
		}
		host := HostOf(req.URL.String())
		if m := netx.ConnectionProblem(err, host, os.Getenv); m != "" {
			return nil, &Unreachable{m}
		}
		if f := netx.Failure(err); f != nil {
			return nil, &Unreachable{netx.UnreachableMessage(host, f)}
		}
		return nil, err
	}
	return res, nil
}

var authish = regexp.MustCompile(`(?i)unauthoriz|invalid[ _-]?(api[ _-]?)?key|credential|locked|expired token`)

func (c *Client) gatewayError(status int, body, ctx string) error {
	auth := status == 401 || status == 403 || authish.MatchString(body)
	var err error
	if auth {
		err = &AuthError{fmt.Sprintf("The model gateway rejected the credential (%d). The API key may be locked or expired; some providers require a periodic unlock. Unlock or update the key, then retry. Gateway said: %s", status, Slice(body, 200))}
	} else {
		err = fmt.Errorf("gateway %s %d: %s", ctx, status, Slice(body, 300))
	}
	c.mu.Lock()
	c.lastFailure = &CallFailure{At: iso(c.now()), Message: Slice(err.Error(), 300), Auth: auth}
	c.mu.Unlock()
	return err
}

// Slice is String.prototype.slice(0, n) on UTF-16 units, near enough: n
// runes.
func Slice(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// LastFailure is lastCallFailure.
func (c *Client) LastFailure() *CallFailure {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastFailure
}

// ModelFailure is modelFailure(id).
func (c *Client) ModelFailure(id string) (ModelFailure, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.failures[id]
	return f, ok
}

// RecordOutcome is recordModelOutcome.
func (c *Client) RecordOutcome(id string, failure *ModelFailure) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if failure != nil {
		f := *failure
		f.At = iso(c.now())
		c.failures[id] = f
	} else {
		delete(c.failures, id)
	}
}

/* ---- the sidecar ---- */

// SidecarConfigured is sidecarConfigured().
func (c *Client) SidecarConfigured() bool { return c.SidecarBase != "" }

// IsSidecarModel is isSidecarModel(id).
func (c *Client) IsSidecarModel(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sidecarIDs[id]
}

// SidecarModels is listSidecarModels(): its catalog, empty on any failure.
func (c *Client) SidecarModels(ctx context.Context) []*jsonobj.Object {
	if !c.SidecarConfigured() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.SidecarBase+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+c.SidecarKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil
	}
	b, _ := io.ReadAll(res.Body)
	models := ListingItems(b)
	c.mu.Lock()
	for _, m := range models {
		if id := ObjString(m, "id"); id != "" {
			c.sidecarIDs[id] = true
		}
	}
	c.mu.Unlock()
	return models
}

/* ---- listing ---- */

// ModelsURL is modelsUrl(base).
func ModelsURL(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host == "models.github.ai" {
		return u.Scheme + "://" + u.Host + "/catalog/models"
	}
	return base + "/models"
}

// ListingItems is asListing(body)'s data ?? models: the entries as ordered
// objects, falsy entries dropped.
func ListingItems(b []byte) []*jsonobj.Object {
	t := bytes.TrimSpace(b)
	var arr []json.RawMessage
	if len(t) > 0 && t[0] == '[' {
		_ = json.Unmarshal(t, &arr)
	} else if o, err := jsonobj.Parse(t); err == nil {
		v := o.Get("data")
		if jsonobj.IsNullish(v) {
			v = o.Get("models")
		}
		_ = json.Unmarshal(v, &arr)
	}
	var out []*jsonobj.Object
	for _, r := range arr {
		if o, err := jsonobj.Parse(r); err == nil {
			out = append(out, o)
		}
	}
	return out
}

// Listing is a model listing's body as the gateway answered, parsed.
type Listing struct {
	Items              []*jsonobj.Object
	UnreachableSession json.RawMessage
}

// ObjString is a string field of an object, "" when not a string.
func ObjString(o *jsonobj.Object, k string) string {
	s, _ := jsonobj.String(o.Get(k))
	return s
}

// ListModels is listModels(key, baseUrl, endpoint).
func (c *Client) ListModels(ctx context.Context, key string, baseURL string, ep *modelkeys.Endpoint) (*Listing, error) {
	if baseURL != "" && ep != nil && len(ep.Models) > 0 {
		l := &Listing{}
		for _, id := range ep.Models {
			o := jsonobj.New()
			_ = o.SetValue("id", id)
			_ = o.SetValue("object", "model")
			l.Items = append(l.Items, o)
		}
		return l, nil
	}
	base := baseURL
	var target *modelkeys.Endpoint
	if base == "" {
		base = c.Base()
	} else {
		target = ep
	}
	if key == "" {
		key = c.Key()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", WithQuery(ModelsURL(base), target), nil)
	req.Header = Headers(key, target)
	res, err := c.reach(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, c.gatewayError(res.StatusCode, string(b), "/models")
	}
	t := bytes.TrimSpace(b)
	if !json.Valid(t) {
		return nil, fmt.Errorf("invalid JSON in the model listing")
	}
	l := &Listing{Items: ListingItems(t)}
	if len(t) > 0 && t[0] == '{' {
		if o, err := jsonobj.Parse(t); err == nil {
			l.UnreachableSession = o.Get("unreachable_sessions")
		}
	}
	return l, nil
}

// NotFoundMessage is notFoundMessage(tried, base).
func NotFoundMessage(tried, base string) string {
	path, origin := "", base
	if u, err := url.Parse(base); err == nil && u.Scheme != "" {
		path = strings.TrimRight(u.Path, "/")
		origin = u.Scheme + "://" + u.Host
	}
	if regexp.MustCompile(`/api/openai/v1$`).MatchString(path) {
		return fmt.Sprintf("%s does not exist (404), so %s is not an ACTIVATE platform. For a site AI portal or another OpenAI-compatible service, use OpenAI-compatible endpoint with the service's base URL.", tried, HostOf(base))
	}
	trimmed := regexp.MustCompile(`/(chat/completions|completions|models|embeddings)$`).ReplaceAllString(path, "")
	if trimmed != path {
		return fmt.Sprintf("%s does not exist (404). The base URL ends before /chat/completions: try %s%s.", tried, origin, trimmed)
	}
	if regexp.MustCompile(`(?i)/v\d+[a-z0-9]*$`).MatchString(path) {
		return fmt.Sprintf("%s does not exist (404). If %s is the right address, the endpoint does not list its models: type their names under Advanced (Settings, Model access, OpenAI-compatible endpoint), and Test then tries a chat completion with the first.", tried, base)
	}
	return fmt.Sprintf("%s does not exist (404). An OpenAI-compatible base URL usually ends in a version, such as %s%s/v1.", tried, origin, path)
}

/* ---- provider probes ---- */

// Verdict is ProviderVerdict.
type Verdict struct {
	OK        bool
	Kind      string // "locked", "unavailable" or ""
	UnlockURL *string
	Message   string
}

const probeTTL = 5 * time.Minute

// InvalidateProbes is invalidateProviderProbes().
func (c *Client) InvalidateProbes() {
	c.mu.Lock()
	c.probes = map[string]probeHit{}
	c.pings = map[string]pingHit{}
	c.mu.Unlock()
}

// Probeable is probeableProvider(prefix).
func (c *Client) Probeable(prefix string) bool { return c.probePat.MatchString(prefix) }

var (
	msgFull  = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	msgCut   = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)$`)
	streamRe = regexp.MustCompile(`(?i)^received error while streaming:\s*`)
	spaceRe  = regexp.MustCompile(`\s+`)
	unlockRe = regexp.MustCompile(`unlock_url[\\":\s]*(https?://[^"\\\s]+)`)
)

// ProviderReason is providerReason(text).
func ProviderReason(text string) string {
	t := text
	for i := 0; i < 3; i++ {
		m := msgFull.FindStringSubmatch(t)
		if m == nil {
			m = msgCut.FindStringSubmatch(t)
		}
		if m == nil {
			break
		}
		t = strings.ReplaceAll(strings.ReplaceAll(m[1], `\"`, `"`), `\\`, `\`)
	}
	t = strings.TrimSpace(spaceRe.ReplaceAllString(streamRe.ReplaceAllString(t, ""), " "))
	if len([]rune(t)) > 180 {
		return string([]rune(t)[:177]) + "..."
	}
	return t
}

// UnlockURL is extractUnlockUrl(text).
func UnlockURL(text string) *string {
	m := unlockRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	return &m[1]
}

var (
	paramWord = regexp.MustCompile(`(?i)unsupported|unknown|unrecognized|invalid|not supported`)
	paramName = regexp.MustCompile(`(?i)parameter|max_tokens|max_output_tokens|max_completion_tokens|temperature|top_p`)
	dataLine  = regexp.MustCompile(`(?m)^data: `)
	errorBody = regexp.MustCompile(`^\s*\{"error"`)
	lockedRe  = regexp.MustCompile(`(?i)key locked|locked|unauthorized|api key`)
)

func rejectedParameter(text string) bool {
	return paramWord.MatchString(text) && paramName.MatchString(text)
}

// firstFrame reads a response up to its first SSE frame, or whole when it
// is not a success.
func firstFrame(res *http.Response, cancel context.CancelFunc) string {
	defer cancel()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	var buf strings.Builder
	p := make([]byte, 4096)
	for {
		n, err := res.Body.Read(p)
		buf.Write(p[:n])
		s := buf.String()
		if dataLine.MatchString(s) || errorBody.MatchString(s) || buf.Len() > 16384 {
			break
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

// ProbeProvider is probeProvider(prefix, sample, key).
func (c *Client) ProbeProvider(ctx context.Context, prefix, sample, key string) Verdict {
	ck := prefix + ":" + lastN(key, 6)
	c.mu.Lock()
	if hit, ok := c.probes[ck]; ok && c.now().Sub(hit.at) < probeTTL {
		c.mu.Unlock()
		return hit.v
	}
	c.mu.Unlock()
	ping := func() (ok bool, status int, text string, err error) {
		pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		body, _ := json.Marshal(map[string]any{"model": sample, "messages": []map[string]string{{"role": "user", "content": "ping"}}, "stream": true})
		body = orderedPing(sample)
		req, _ := http.NewRequestWithContext(pctx, "POST", c.Base()+"/chat/completions", bytes.NewReader(body))
		k := key
		if k == "" {
			k = c.Key()
		}
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.HTTP.Do(req)
		if err != nil {
			cancel()
			return false, 0, "", err
		}
		defer res.Body.Close()
		text = firstFrame(res, cancel)
		ok = res.StatusCode >= 200 && res.StatusCode < 300 && !errorBody.MatchString(text)
		return ok, res.StatusCode, Slice(text, 4000), nil
	}
	v := Verdict{OK: true}
	func() {
		ok, status, text, err := ping()
		if err != nil {
			return
		}
		if !ok && rejectedParameter(text) {
			ok = true
		}
		if ok {
			return
		}
		unlock := UnlockURL(text)
		if unlock != nil || status == 401 || status == 403 || lockedRe.MatchString(text) {
			v = Verdict{OK: false, Kind: "locked", UnlockURL: unlock, Message: Slice(text, 2000)}
			return
		}
		ok, _, text, err = ping()
		if err != nil {
			return
		}
		if !ok {
			v = Verdict{OK: false, Kind: "unavailable", UnlockURL: UnlockURL(text), Message: Slice(text, 2000)}
		}
	}()
	c.mu.Lock()
	c.probes[ck] = probeHit{c.now(), v}
	c.mu.Unlock()
	return v
}

func orderedPing(model string) []byte {
	o := jsonobj.New()
	_ = o.SetValue("model", model)
	_ = o.SetValue("messages", []map[string]string{{"role": "user", "content": "ping"}})
	_ = o.SetValue("stream", true)
	b, _ := jsonobj.Marshal(o)
	// map key order inside messages: role, content
	return bytes.Replace(b, []byte(`{"content":"ping","role":"user"}`), []byte(`{"role":"user","content":"ping"}`), 1)
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

/* ---- health ---- */

// Health is AiHealth, written in Node's key order.
type Health struct {
	OK          bool
	Status      string
	Models      int
	GatewayHost string
	Message     *string
	LastFailure *CallFailure
	CheckedAt   string
	UnlockURL   *string
	HasUnlock   bool
	IDs         []string
	HasIDs      bool
	Checked     string
	NotFound    string
}

// Object is the health as /api/ai/health and the key test send it.
func (h Health) Object() *jsonobj.Object {
	o := jsonobj.New()
	// { ...base (models, gatewayHost, lastCallFailure, checkedAt), ok, status, ... }
	_ = o.SetValue("models", h.Models)
	_ = o.SetValue("gatewayHost", h.GatewayHost)
	_ = o.SetValue("lastCallFailure", h.LastFailure)
	_ = o.SetValue("checkedAt", h.CheckedAt)
	_ = o.SetValue("ok", h.OK)
	_ = o.SetValue("status", h.Status)
	if h.OK {
		// models is base's key, overwritten in place by the spread
		_ = o.SetValue("models", h.Models)
		o.Set("message", json.RawMessage("null"))
		_ = o.SetValue("checked", h.Checked)
		if h.HasIDs {
			_ = o.SetValue("ids", orEmpty(h.IDs))
		}
		return o
	}
	if h.Checked != "" {
		_ = o.SetValue("checked", h.Checked)
	}
	_ = o.SetValue("message", h.Message)
	if h.HasUnlock {
		_ = o.SetValue("unlockUrl", h.UnlockURL)
	}
	if h.NotFound != "" {
		_ = o.SetValue("notFound", h.NotFound)
	}
	return o
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func strp(s string) *string { return &s }

func (c *Client) pingCompletion(ctx context.Context, u string, h http.Header, model, ck string) (int, string, error) {
	c.mu.Lock()
	if hit, ok := c.pings[ck]; ok && c.now().Sub(hit.at) < probeTTL {
		c.mu.Unlock()
		return hit.status, hit.text, nil
	}
	c.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	req, _ := http.NewRequestWithContext(pctx, "POST", u, bytes.NewReader(orderedPing(model)))
	req.Header = h.Clone()
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		cancel()
		return 0, "", err
	}
	defer res.Body.Close()
	text := firstFrame(res, cancel)
	status := res.StatusCode
	if status >= 200 && status < 300 && errorBody.MatchString(text) {
		status = 500
	}
	text = Slice(text, 4000)
	c.mu.Lock()
	c.pings[ck] = pingHit{c.now(), status, text}
	c.mu.Unlock()
	return status, text, nil
}

// Health is aiHealth(key, baseUrl, {ids, endpoint}).
func (c *Client) Health(ctx context.Context, key, baseURL string, ids bool, endpoint *modelkeys.Endpoint) Health {
	apiBase := baseURL
	if apiBase == "" {
		apiBase = c.Base()
	}
	h := Health{GatewayHost: HostOf(apiBase), LastFailure: c.LastFailure(), CheckedAt: iso(c.now())}
	if key == "" && !c.Configured() {
		h.Status = "unconfigured"
		h.Message = strp("No gateway credential configured (PW_API_KEY or pw CLI login).")
		return h
	}
	var ep *modelkeys.Endpoint
	if baseURL != "" {
		ep = endpoint
	}
	var typed []string
	if ep != nil {
		typed = ep.Models
	}
	k := key
	if k == "" {
		k = c.Key()
	}
	headers := Headers(k, ep)
	tried := ModelsURL(apiBase)
	if len(typed) > 0 {
		tried = apiBase + "/chat/completions"
	}
	tried = WithQuery(tried, ep)
	var status int
	var text string
	var err error
	if len(typed) > 0 {
		epj, _ := json.Marshal(ep)
		sum := sha256.Sum256([]byte(fmt.Sprintf("[%q,%q,%s]", apiBase, key, epj)))
		status, text, err = c.pingCompletion(ctx, tried, headers, typed[0], hex.EncodeToString(sum[:]))
	} else {
		hctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(hctx, "GET", tried, nil)
		req.Header = headers
		var res *http.Response
		res, err = c.HTTP.Do(req)
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			status, text = res.StatusCode, string(b)
		}
	}
	if err != nil {
		if m := netx.ConnectionProblem(err, h.GatewayHost, os.Getenv); m != "" {
			h.Status, h.Message = "unreachable", &m
			return h
		}
		if f := netx.Failure(err); f != nil {
			h.Status = "offline"
			if netx.IsLoopbackHost(h.GatewayHost) {
				h.Status = "unreachable"
			}
			h.Message = strp(netx.UnreachableMessage(h.GatewayHost, f))
			return h
		}
		h.Status, h.Message = "unreachable", strp(Slice(err.Error(), 200))
		return h
	}
	h.Checked = "models"
	if len(typed) > 0 {
		h.Checked = "completion"
	}
	if status < 200 || status >= 300 {
		auth := status == 401 || status == 403
		unlock := UnlockURL(text)
		h.Status = "unreachable"
		if auth {
			h.Status = "auth"
		}
		var msg string
		switch {
		case unlock != nil:
			msg = "The provider locked this API key (it does so on a schedule); unlock it and re-check."
		case auth && baseURL != "":
			msg = fmt.Sprintf("%s rejected this credential (%d). Check the key, and under Advanced the header the endpoint expects it in.", h.GatewayHost, status)
		case auth:
			msg = fmt.Sprintf("%s rejected this credential (%d). A platform token lasts 24 hours from login; an API key from Account, API keys does not expire.", h.GatewayHost, status)
		case status == 404 && len(typed) == 0:
			msg = NotFoundMessage(tried, apiBase)
		case len(typed) > 0:
			r := ProviderReason(text)
			if r == "" {
				r = "no reason given"
			}
			msg = fmt.Sprintf("A chat completion with %s at %s failed (%d): %s", typed[0], tried, status, r)
		default:
			msg = fmt.Sprintf("%d: %s", status, Slice(text, 200))
		}
		h.Message, h.UnlockURL, h.HasUnlock = &msg, unlock, true
		if status == 404 {
			h.NotFound = tried
		}
		return h
	}
	if len(typed) > 0 {
		h.OK, h.Status, h.Models = true, "ok", len(typed)
		if ids {
			h.HasIDs = true
			h.IDs = append([]string(nil), typed[:min(len(typed), 500)]...)
		}
		return h
	}
	if !json.Valid([]byte(strings.TrimSpace(text))) {
		h.Status, h.Message = "unreachable", strp(Slice(fmt.Sprintf("Unexpected token in JSON: %s", Slice(text, 40)), 200))
		h.Checked = ""
		return h
	}
	list := ListingItems([]byte(text))
	h.OK, h.Status, h.Models = true, "ok", len(list)
	if ids {
		h.HasIDs = true
		for _, m := range list {
			if id := jsStringOf(m.Get("id")); id != "" && len(h.IDs) < 500 {
				h.IDs = append(h.IDs, id)
			}
		}
	}
	return h
}

func jsStringOf(raw json.RawMessage) string {
	if jsonobj.IsNullish(raw) {
		return ""
	}
	if s, ok := jsonobj.String(raw); ok {
		return s
	}
	return string(raw)
}

/* ---- tool-calling check ---- */

// ToolProbe is what one request offering a tool showed about a model.
type ToolProbe struct {
	Kind   string // calls, no-call, refused, failed
	Detail string
}

const toolCheckTimeout = 15 * time.Second

var timeTool = json.RawMessage(`{"type":"function","function":{"name":"get_utc_time","description":"Returns the current date and time in UTC.","parameters":{"type":"object","properties":{},"required":[]}}}`)

// ProbeToolCalling is probeToolCalling(model, o).
func (c *Client) ProbeToolCalling(ctx context.Context, model, key, baseURL string, endpoint *modelkeys.Endpoint, stream bool, extra *jsonobj.Object) ToolProbe {
	var target *modelkeys.Endpoint
	if baseURL != "" {
		target = endpoint
	}
	base := baseURL
	if base == "" {
		base = c.Base()
	}
	u := WithQuery(base+"/chat/completions", target)
	k := key
	if k == "" {
		k = c.Key()
	}
	headers := Headers(k, target)
	headers.Set("Content-Type", "application/json")
	type result struct {
		status  int
		text    string
		called  bool
		content string
		finish  string
	}
	send := func(capped bool) (result, error) {
		sctx, cancel := context.WithTimeout(ctx, toolCheckTimeout)
		defer cancel()
		o := jsonobj.New()
		_ = o.SetValue("model", model)
		_ = o.SetValue("stream", stream)
		if capped {
			_ = o.SetValue("max_tokens", 256)
		}
		if extra != nil {
			for _, kk := range extra.Keys() {
				o.Set(kk, extra.Get(kk))
			}
		}
		o.Set("messages", json.RawMessage(`[{"role":"user","content":"What time is it in UTC right now? Call the get_utc_time tool to find out."}]`))
		o.Set("tools", json.RawMessage("["+string(timeTool)+"]"))
		_ = o.SetValue("tool_choice", "auto")
		b, _ := jsonobj.Marshal(o)
		req, _ := http.NewRequestWithContext(sctx, "POST", u, bytes.NewReader(b))
		req.Header = headers.Clone()
		res, err := c.HTTP.Do(req)
		if err != nil {
			return result{}, err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode > 299 {
			t, _ := io.ReadAll(res.Body)
			return result{status: res.StatusCode, text: Slice(string(t), 4000)}, nil
		}
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "text/event-stream"
		}
		if !stream || !strings.Contains(ct, "event-stream") {
			var full struct {
				Error   json.RawMessage `json:"error"`
				Choices []struct {
					FinishReason *string `json:"finish_reason"`
					Message      struct {
						Content   *string           `json:"content"`
						ToolCalls []json.RawMessage `json:"tool_calls"`
					} `json:"message"`
				} `json:"choices"`
			}
			b, _ := io.ReadAll(res.Body)
			if err := json.Unmarshal(b, &full); err != nil {
				return result{}, err
			}
			if !jsonobj.IsNullish(full.Error) {
				return result{status: 500, text: Slice(string(full.Error), 4000)}, nil
			}
			r := result{status: res.StatusCode}
			if len(full.Choices) > 0 {
				m := full.Choices[0].Message
				r.called = len(m.ToolCalls) > 0
				if m.Content != nil {
					r.content = *m.Content
				}
				if full.Choices[0].FinishReason != nil {
					r.finish = *full.Choices[0].FinishReason
				}
			}
			return r, nil
		}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		content := ""
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(line[5:])
			if data == "[DONE]" {
				return result{status: res.StatusCode, content: content}, nil
			}
			var chunk struct {
				Error   json.RawMessage `json:"error"`
				Choices []struct {
					FinishReason *string `json:"finish_reason"`
					Delta        struct {
						Content   any `json:"content"`
						ToolCalls []struct {
							Function struct {
								Name string `json:"name"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			if !jsonobj.IsNullish(chunk.Error) {
				return result{status: 500, text: Slice(string(chunk.Error), 4000), content: content}, nil
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			ch := chunk.Choices[0]
			for _, t := range ch.Delta.ToolCalls {
				if t.Function.Name != "" {
					return result{status: res.StatusCode, called: true, content: content}, nil
				}
			}
			if s, ok := ch.Delta.Content.(string); ok {
				content += s
			}
			if (ch.FinishReason != nil && *ch.FinishReason != "") || len(content) > 4000 {
				f := ""
				if ch.FinishReason != nil {
					f = *ch.FinishReason
				}
				return result{status: res.StatusCode, content: content, finish: f}, nil
			}
		}
		return result{status: res.StatusCode, content: content}, nil
	}
	r, err := send(true)
	if err == nil && r.status == 400 && rejectedParameter(r.text) && regexp.MustCompile(`max_tokens|max_output_tokens|max_completion_tokens`).MatchString(r.text) {
		r, err = send(false)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return ToolProbe{"failed", fmt.Sprintf("no answer within %d seconds", int(toolCheckTimeout/time.Second))}
		}
		return ToolProbe{"failed", Slice(err.Error(), 200)}
	}
	if r.status >= 200 && r.status < 300 {
		if r.called {
			return ToolProbe{"calls", ""}
		}
		if r.finish == "length" && strings.TrimSpace(r.content) == "" {
			return ToolProbe{"failed", "the model used its whole allowance for the check before calling the tool or answering"}
		}
		d := ""
		if strings.Contains(r.content, "get_utc_time") {
			d = "it wrote the call as text instead of making it"
		}
		return ToolProbe{"no-call", d}
	}
	reason := ProviderReason(r.text)
	if reason == "" {
		reason = fmt.Sprintf("status %d", r.status)
	}
	if (r.status == 400 || r.status == 422 || r.status == 500) && regexp.MustCompile(`(?i)tool|function`).MatchString(r.text) {
		return ToolProbe{"refused", reason}
	}
	return ToolProbe{"failed", fmt.Sprintf("%d: %s", r.status, reason)}
}
