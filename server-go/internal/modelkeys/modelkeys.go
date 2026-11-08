// Package modelkeys is server/src/credentials.ts: the personal model
// credentials of each verified user, the key one user shares for the
// deployment, and the one user's key on a Studio with sign-in off.
//
// Storage is Node's: INDEX_BASE/user-credentials.json (the vault, written
// 0600 with JSON.stringify(v, null, 1)) holds the shared key and, with
// sign-in off, the one user's; its blobs are sealed with
// INDEX_BASE/.credentials-secret (internal/keycookie). A signed-in user's
// key lives in memory for 12 hours, and its durable copy is the sealed
// browser cookie (ade_mk, ade_mkp), which hydrates the memory copy on a
// request. Keys and header values are never logged and never sent to the
// browser.
package modelkeys

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/keycookie"
)

// SingleOwner is the one user of a Studio with sign-in off.
const SingleOwner = "single"

// sharedSub is the vault subject of the key a user shares for everyone.
const sharedSub = "__shared__"

const sessionTTL = 12 * time.Hour

// Header is one extra header an endpoint takes.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Endpoint is EndpointOptions: what an OpenAI-compatible endpoint needs
// beyond its base URL and key. Field order is Node's.
type Endpoint struct {
	Auth    string   `json:"auth,omitempty"`
	Headers []Header `json:"headers,omitempty"`
	Models  []string `json:"models,omitempty"`
	Query   string   `json:"query,omitempty"`
}

// AuthOr is the header the key goes in, bearer when unset.
func (e *Endpoint) AuthOr() string {
	if e == nil || e.Auth == "" {
		return "bearer"
	}
	return e.Auth
}

// Summary is endpointSummary: header names, never values.
type Summary struct {
	Auth    string   `json:"auth"`
	Headers []string `json:"headers"`
	Models  []string `json:"models"`
	Query   string   `json:"query"`
}

// SummaryOf is endpointSummary(o), nil for no options.
func SummaryOf(o *Endpoint) *Summary {
	if o == nil {
		return nil
	}
	s := &Summary{Auth: o.AuthOr(), Headers: []string{}, Models: o.Models, Query: o.Query}
	for _, h := range o.Headers {
		s.Headers = append(s.Headers, h.Name)
	}
	if s.Models == nil {
		s.Models = []string{}
	}
	return s
}

// Cred is UserCred: the caller's own key, where it goes, and its options.
type Cred struct {
	Key      string
	BaseURL  *string
	Endpoint *Endpoint
}

// Base is the base URL, "" for the platform gateway.
func (c *Cred) Base() string {
	if c == nil || c.BaseURL == nil {
		return ""
	}
	return *c.BaseURL
}

// Store is the module: one per server.
type Store struct {
	indexBase   string
	vaultFile   string
	sealer      *keycookie.Sealer
	authEnabled func() bool
	disabled    func() bool
	now         func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	vaultMu  sync.Mutex
}

type session struct {
	key        string
	expiresAt  time.Time
	addedAt    string
	last4      string
	kind       string
	credExpiry *string
	baseURL    *string
	endpoint   *Endpoint
}

// New is the store beside indexBase. authEnabled is authEnabled(); a
// Studio with sign-in off keeps its one user's key in the vault.
func New(indexBase string, authEnabled func() bool) *Store {
	return &Store{
		indexBase:   indexBase,
		vaultFile:   filepath.Join(indexBase, "user-credentials.json"),
		sealer:      keycookie.New(indexBase),
		authEnabled: authEnabled,
		disabled:    func() bool { return os.Getenv("DISABLE_PERSONAL_KEYS") == "1" },
		now:         time.Now,
		sessions:    map[string]*session{},
	}
}

// Sealer is the cookie sealer the store uses.
func (s *Store) Sealer() *keycookie.Sealer { return s.sealer }

// Disabled is personalKeysDisabled().
func (s *Store) Disabled() bool { return s.disabled() }

func (s *Store) onDisk(sub string) bool { return sub == SingleOwner && !s.authEnabled() }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func expired(isoTime *string, now time.Time) bool {
	if isoTime == nil || *isoTime == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, *isoTime)
	if err != nil {
		return false // Date.parse NaN < now is false
	}
	return t.Before(now)
}

// inspect: a JWT-shaped key is a token with its expiry.
func inspect(key string) (kind string, expiresAt *string) {
	parts := strings.Split(key, ".")
	if len(parts) == 3 {
		p := strings.NewReplacer("-", "+", "_", "/").Replace(parts[1])
		raw, err := base64.StdEncoding.DecodeString(padB64(p))
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(p, "="))
		}
		if err == nil {
			var payload map[string]any
			if json.Unmarshal(raw, &payload) == nil {
				if exp, ok := payload["exp"].(float64); ok {
					v := iso(time.UnixMilli(int64(exp * 1000)))
					return "token", &v
				}
				return "token", nil
			}
		}
	}
	return "api-key", nil
}

func padB64(s string) string {
	if m := len(s) % 4; m != 0 {
		return s + strings.Repeat("=", 4-m)
	}
	return s
}

func last4Of(key string) string {
	t := strings.TrimRight(key, "=")
	r := []rune(t)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return string(r)
}

/* ---- endpoint options ---- */

var (
	auths       = []string{"bearer", "api-key", "x-api-key", "none"}
	authHeader  = map[string]string{"bearer": "authorization", "api-key": "api-key", "x-api-key": "x-api-key"}
	reserved    = map[string]bool{"host": true, "content-type": true, "content-length": true, "connection": true, "transfer-encoding": true, "accept-encoding": true, "x-allocation": true}
	tokenRe     = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	badValueRe  = regexp.MustCompile("[\r\n\x00]")
	splitModels = regexp.MustCompile(`[\n,]`)
)

// NormalizeBaseURL is normalizeBaseUrl: trimmed, no trailing slash, nil
// when empty.
func NormalizeBaseURL(v any) *string {
	s := strings.TrimSpace(jsString(v))
	s = strings.TrimRight(s, "/")
	if s == "" {
		return nil
	}
	return &s
}

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// Problem is a validation message, for a 400.
type Problem struct{ Msg string }

func (p *Problem) Error() string { return p.Msg }

func problem(f string, a ...any) error { return &Problem{fmt.Sprintf(f, a...)} }

// NormalizeEndpoint is normalizeEndpointOptions(raw, stored): raw is the
// decoded JSON value (nil for null or absent).
func NormalizeEndpoint(raw any, stored *Endpoint) (*Endpoint, error) {
	if raw == nil {
		return nil, nil
	}
	r, ok := raw.(map[string]any)
	if !ok {
		return nil, problem("endpoint options must be an object")
	}
	auth := "bearer"
	if a, has := r["auth"]; has && a != nil && a != "" {
		auth = jsString(a)
	}
	if !contains(auths, auth) {
		return nil, problem("unknown key header %q", jsString(r["auth"]))
	}
	var headers []Header
	var rows []any
	if h, has := r["headers"]; has && h != nil {
		list, ok := h.([]any)
		if !ok {
			return nil, problem("headers must be a list")
		}
		rows = list
	}
	if len(rows) > 20 {
		return nil, problem("at most 20 extra headers")
	}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		name := strings.TrimSpace(jsString(m["name"]))
		value := ""
		if g, has := m["value"]; has && g != nil {
			value = strings.TrimSpace(jsString(g))
		}
		if name == "" && value == "" {
			continue
		}
		if name == "" || len([]rune(name)) > 100 || !tokenRe.MatchString(name) {
			return nil, problem("%q is not a valid header name", firstRunes(name, 100))
		}
		lower := strings.ToLower(name)
		if reserved[lower] {
			return nil, problem("the Studio sets %s itself", name)
		}
		if authHeader[auth] == lower {
			return nil, problem("the key is already sent as %s; choose None as the key header to set %s yourself", name, name)
		}
		for _, h := range headers {
			if strings.ToLower(h.Name) == lower {
				return nil, problem("%s is listed twice", name)
			}
		}
		if value == "" && stored != nil {
			for _, h := range stored.Headers {
				if strings.ToLower(h.Name) == lower {
					value = h.Value
					break
				}
			}
		}
		if value == "" {
			return nil, problem("header %s needs a value", name)
		}
		if len([]rune(value)) > 4096 || badValueRe.MatchString(value) {
			return nil, problem("the value of %s is not a valid header value", name)
		}
		headers = append(headers, Header{name, value})
	}
	var list []string
	switch m := r["models"].(type) {
	case nil:
	case []any:
		for _, x := range m {
			list = append(list, jsString(x))
		}
	default:
		list = splitModels.Split(jsString(m), -1)
	}
	var models []string
	seen := map[string]bool{}
	for _, x := range list {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		models = append(models, x)
	}
	if len(models) > 100 {
		return nil, problem("at most 100 model names")
	}
	for _, m := range models {
		if len([]rune(m)) > 200 || strings.ContainsAny(m, " \t\n\r\f\v") {
			return nil, problem("a model name is one word, at most 200 characters")
		}
	}
	query := strings.TrimLeft(strings.TrimSpace(jsString(r["query"])), "?")
	if len([]rune(query)) > 2000 || strings.ContainsAny(query, " \t\n\r\f\v#") {
		return nil, problem("the query string has spaces or a #, or is too long")
	}
	if auth == "bearer" && len(headers) == 0 && len(models) == 0 && query == "" {
		return nil, nil
	}
	o := &Endpoint{Headers: headers, Models: models, Query: query}
	if auth != "bearer" {
		o.Auth = auth
	}
	return o, nil
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

/* ---- the vault ---- */

type vaultEntry struct {
	Blob        string  `json:"blob"`
	Last4       string  `json:"last4"`
	AddedAt     string  `json:"addedAt"`
	Kind        string  `json:"kind,omitempty"`
	ExpiresAt   *string `json:"expiresAt"`
	BaseURL     *string `json:"baseUrl"`
	SharedBy    string  `json:"sharedBy,omitempty"`
	SharedBySub string  `json:"sharedBySub,omitempty"`
	Opts        string  `json:"opts,omitempty"`
}

func (s *Store) loadVault() *jsonobj.Object {
	b, err := os.ReadFile(s.vaultFile)
	if err != nil {
		return jsonobj.New()
	}
	o, err := jsonobj.Parse(b)
	if err != nil {
		return jsonobj.New()
	}
	return o
}

func entryOf(v *jsonobj.Object, sub string) *vaultEntry {
	raw := v.Get(sub)
	if raw == nil {
		return nil
	}
	var e vaultEntry
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	return &e
}

func (s *Store) saveVault(v *jsonobj.Object) error {
	compact, err := jsonobj.Marshal(v)
	if err != nil {
		return err
	}
	out, err := jsonobj.IndentJS(compact, " ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.vaultFile, os.Getpid())
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.vaultFile)
}

func setEntry(v *jsonobj.Object, sub string, e vaultEntry) {
	b, _ := jsonobj.Marshal(e)
	v.Set(sub, b)
}

func (s *Store) open(blob string) (string, bool) {
	b, err := s.sealer.Decrypt(blob)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (s *Store) openOptions(blob string) *Endpoint {
	if blob == "" {
		return nil
	}
	plain, ok := s.open(blob)
	if !ok {
		return nil
	}
	var e Endpoint
	if json.Unmarshal([]byte(plain), &e) != nil {
		return nil
	}
	return &e
}

/* ---- keys ---- */

func (s *Store) live(sub string) *session {
	x := s.sessions[sub]
	if x == nil {
		return nil
	}
	if s.now().After(x.expiresAt) {
		delete(s.sessions, sub)
		return nil
	}
	return x
}

// Set is setUserKey(sub, key, persist, baseUrl, endpoint).
func (s *Store) Set(sub, key string, baseURL *string, endpoint any) error {
	if s.Disabled() {
		return problem("personal keys are disabled on this deployment")
	}
	if sub == sharedSub {
		return problem("reserved subject")
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || len([]rune(trimmed)) > 8192 {
		return problem("invalid key")
	}
	u := NormalizeBaseURL(ptrString(baseURL))
	if u != nil {
		pu, err := url.Parse(*u)
		if err != nil || pu.Scheme == "" {
			return problem("invalid provider base URL")
		}
		if !strings.HasPrefix(*u, "http://") && !strings.HasPrefix(*u, "https://") {
			return problem("provider base URL must be http(s)")
		}
	}
	var opts *Endpoint
	if u != nil {
		var err error
		switch e := endpoint.(type) {
		case *Endpoint:
			opts, err = NormalizeEndpoint(endpointValue(e), nil)
		default:
			opts, err = NormalizeEndpoint(endpoint, nil)
		}
		if err != nil {
			return err
		}
	}
	last4 := last4Of(trimmed)
	addedAt := iso(s.now())
	kind, exp := inspect(trimmed)
	if s.onDisk(sub) {
		s.vaultMu.Lock()
		defer s.vaultMu.Unlock()
		v := s.loadVault()
		blob, err := s.sealer.Encrypt([]byte(trimmed))
		if err != nil {
			return err
		}
		e := vaultEntry{Blob: blob, Last4: last4, AddedAt: addedAt, Kind: kind, ExpiresAt: exp, BaseURL: u}
		if opts != nil {
			b, _ := json.Marshal(opts)
			e.Opts, _ = s.sealer.Encrypt(b)
		}
		setEntry(v, sub, e)
		return s.saveVault(v)
	}
	s.mu.Lock()
	s.sessions[sub] = &session{key: trimmed, expiresAt: s.now().Add(sessionTTL), addedAt: addedAt, last4: last4, kind: kind, credExpiry: exp, baseURL: u, endpoint: opts}
	s.mu.Unlock()
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	v := s.loadVault()
	if v.Has(sub) {
		v.Delete(sub)
		return s.saveVault(v)
	}
	return nil
}

// endpointValue turns stored options back into the decoded-JSON shape
// NormalizeEndpoint reads.
func endpointValue(e *Endpoint) any {
	if e == nil {
		return nil
	}
	b, _ := json.Marshal(e)
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func ptrString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// ScrubPersonal is scrubPersonalVaultEntries: personal entries leave the
// vault so nothing personal stays at rest.
func (s *Store) ScrubPersonal() int {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	v := s.loadVault()
	n := 0
	for _, k := range v.Keys() {
		if k != sharedSub && !s.onDisk(k) {
			v.Delete(k)
			n++
		}
	}
	if n > 0 {
		_ = s.saveVault(v)
	}
	return n
}

// Clear is clearUserKey(sub).
func (s *Store) Clear(sub string) {
	s.mu.Lock()
	delete(s.sessions, sub)
	s.mu.Unlock()
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	v := s.loadVault()
	if v.Has(sub) {
		v.Delete(sub)
		_ = s.saveVault(v)
	}
}

// Share is shareUserKey(sub, sharedBy).
func (s *Store) Share(sub, sharedBy string) error {
	cred := s.Resolve(sub)
	if cred == nil {
		return problem("add your own key first, then share it")
	}
	if cred.BaseURL != nil {
		return problem("a key bound to a custom provider cannot be shared; the deployment credential always calls this deployment's gateway")
	}
	st := s.Status(sub)
	blob, err := s.sealer.Encrypt([]byte(cred.Key))
	if err != nil {
		return err
	}
	last4 := st.Last4
	if last4 == "" {
		last4 = last4Of(cred.Key)
	}
	kind := st.Kind
	if kind == "" {
		kind = "api-key"
	}
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	v := s.loadVault()
	setEntry(v, sharedSub, vaultEntry{Blob: blob, Last4: last4, AddedAt: iso(s.now()), Kind: kind, ExpiresAt: st.CredExpiresAt, BaseURL: nil, SharedBy: sharedBy, SharedBySub: sub})
	return s.saveVault(v)
}

// ClearShared is clearSharedKey().
func (s *Store) ClearShared() {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	v := s.loadVault()
	if v.Has(sharedSub) {
		v.Delete(sharedSub)
		_ = s.saveVault(v)
	}
}

// SharedStatus is SharedKeyStatus, in Node's key order.
type SharedStatus struct {
	Active        bool    `json:"active"`
	Last4         string  `json:"last4,omitempty"`
	SharedBy      *string `json:"sharedBy,omitempty"`
	SharedAt      string  `json:"sharedAt,omitempty"`
	Kind          string  `json:"kind,omitempty"`
	CredExpiresAt *string `json:"credExpiresAt,omitempty"`
	CredExpired   *bool   `json:"credExpired,omitempty"`
	Mine          *bool   `json:"mine,omitempty"`
}

// MarshalJSON writes credExpiresAt as null when active and unset.
func (st SharedStatus) MarshalJSON() ([]byte, error) {
	if !st.Active {
		return []byte(`{"active":false}`), nil
	}
	o := jsonobj.New()
	_ = o.SetValue("active", true)
	_ = o.SetValue("last4", st.Last4)
	if st.SharedBy != nil {
		_ = o.SetValue("sharedBy", *st.SharedBy)
	}
	_ = o.SetValue("sharedAt", st.SharedAt)
	_ = o.SetValue("kind", st.Kind)
	_ = o.SetValue("credExpiresAt", st.CredExpiresAt)
	_ = o.SetValue("credExpired", st.CredExpired != nil && *st.CredExpired)
	_ = o.SetValue("mine", st.Mine != nil && *st.Mine)
	return jsonobj.Marshal(o)
}

// Shared is getSharedKeyStatus(sub).
func (s *Store) Shared(sub string) SharedStatus {
	e := entryOf(s.loadVault(), sharedSub)
	if e == nil {
		return SharedStatus{}
	}
	kind := e.Kind
	if kind == "" {
		kind = "api-key"
	}
	ex := expired(e.ExpiresAt, s.now())
	mine := sub != "" && e.SharedBySub == sub
	var by *string
	if e.SharedBy != "" {
		by = &e.SharedBy
	}
	return SharedStatus{Active: true, Last4: e.Last4, SharedBy: by, SharedAt: e.AddedAt, Kind: kind, CredExpiresAt: e.ExpiresAt, CredExpired: &ex, Mine: &mine}
}

// SharedKey is resolveSharedKey(): the promoted key, if active and valid.
func (s *Store) SharedKey() string {
	e := entryOf(s.loadVault(), sharedSub)
	if e == nil || expired(e.ExpiresAt, s.now()) {
		return ""
	}
	k, _ := s.open(e.Blob)
	return k
}

// Status is UserKeyStatus, written in Node's key order by MarshalJSON.
type Status struct {
	Mode             string
	Last4            string
	AddedAt          string
	SessionExpiresAt string
	Kind             string
	CredExpiresAt    *string
	CredExpired      bool
	BaseURL          *string
	Endpoint         *Summary
}

// Fields are the status's keys and values in order, for merging into a
// payload.
func (st Status) Fields() [][2]any {
	if st.Mode == "none" || st.Mode == "" {
		return [][2]any{{"mode", "none"}}
	}
	out := [][2]any{{"mode", st.Mode}, {"last4", st.Last4}, {"addedAt", st.AddedAt}}
	if st.Mode == "session" {
		out = append(out, [2]any{"sessionExpiresAt", st.SessionExpiresAt})
	}
	out = append(out, [2]any{"kind", st.Kind}, [2]any{"credExpiresAt", st.CredExpiresAt}, [2]any{"credExpired", st.CredExpired}, [2]any{"baseUrl", st.BaseURL}, [2]any{"endpoint", st.Endpoint})
	return out
}

// Status is getUserKeyStatus(sub).
func (s *Store) Status(sub string) Status {
	if s.onDisk(sub) {
		e := entryOf(s.loadVault(), sub)
		if e == nil {
			return Status{Mode: "none"}
		}
		kind := e.Kind
		if kind == "" {
			kind = "api-key"
		}
		return Status{Mode: "stored", Last4: e.Last4, AddedAt: e.AddedAt, Kind: kind, CredExpiresAt: e.ExpiresAt, CredExpired: expired(e.ExpiresAt, s.now()), BaseURL: e.BaseURL, Endpoint: SummaryOf(s.openOptions(e.Opts))}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.live(sub)
	if x == nil {
		return Status{Mode: "none"}
	}
	return Status{Mode: "session", Last4: x.last4, AddedAt: x.addedAt, SessionExpiresAt: iso(x.expiresAt), Kind: x.kind, CredExpiresAt: x.credExpiry, CredExpired: expired(x.credExpiry, s.now()), BaseURL: x.baseURL, Endpoint: SummaryOf(x.endpoint)}
}

// Resolve is resolveUserCred(sub): the caller's own credential, or nil.
// With sign-in off, a request without a user is the one user's.
func (s *Store) Resolve(sub string) *Cred {
	if sub == "" && !s.authEnabled() {
		sub = SingleOwner
	}
	if sub == "" || sub == sharedSub || s.Disabled() {
		return nil
	}
	if s.onDisk(sub) {
		e := entryOf(s.loadVault(), sub)
		if e == nil || expired(e.ExpiresAt, s.now()) {
			return nil
		}
		key, ok := s.open(e.Blob)
		if !ok || key == "" {
			return nil
		}
		c := &Cred{Key: key, BaseURL: e.BaseURL}
		if e.BaseURL != nil {
			c.Endpoint = s.openOptions(e.Opts)
		}
		return c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.live(sub)
	if x == nil || expired(x.credExpiry, s.now()) {
		return nil
	}
	return &Cred{Key: x.key, BaseURL: x.baseURL, Endpoint: x.endpoint}
}

// Seal is sealKeyCookie(sub): the session key sealed with its subject,
// in cookie-sized chunks; nil without a session.
func (s *Store) Seal(sub string) []string {
	s.mu.Lock()
	x := s.live(sub)
	var k keycookie.Key
	if x != nil {
		k = keycookie.Key{Sub: sub, Key: x.key, BaseURL: x.baseURL}
		if x.endpoint != nil {
			k.Options, _ = json.Marshal(x.endpoint)
		}
	}
	s.mu.Unlock()
	if x == nil {
		return nil
	}
	chunks, err := s.sealer.Seal(k)
	if err != nil {
		return nil
	}
	return chunks
}

// Hydrate is hydrateFromCookie(sub, cookieHeader): a signed-in user with
// no live key gets the one their cookie carries, if it was sealed for them.
func (s *Store) Hydrate(sub, cookieHeader string) {
	if sub == "" || cookieHeader == "" || s.Disabled() {
		return
	}
	s.mu.Lock()
	has := s.live(sub) != nil
	s.mu.Unlock()
	if has {
		return
	}
	k, ok := s.sealer.FromCookies(sub, cookieHeader)
	if !ok || strings.TrimSpace(k.Key) == "" {
		return
	}
	var opts any
	if len(bytes.TrimSpace(k.Options)) > 0 {
		_ = json.Unmarshal(k.Options, &opts)
	}
	_ = s.Set(sub, k.Key, k.BaseURL, opts)
}

// IsProblem reports whether err is a validation message (a 400).
func IsProblem(err error) bool {
	var p *Problem
	return errors.As(err, &p)
}

// SealedSession is the session key sealed for sub as one value (the
// header Node mirrors it from); ” without a session.
func (s *Store) SealedSession(sub string) string {
	return strings.Join(s.Seal(sub), "")
}
