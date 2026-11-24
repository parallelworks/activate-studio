package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/keycookie"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A person keeps two keys of their own: an ACTIVATE key, for the platform
// and for chat when they have no endpoint, and an OpenAI-compatible
// endpoint, for chat only. Saving or removing one leaves the other.

const (
	bobKey      = "pwt_bob.activate-1234"
	bobEndpoint = "sk-bob-endpoint-5678"
	bobURL      = "https://models.example.org/v1"
)

type keysStudio struct {
	t   *testing.T
	m   *Module
	url string
}

// newKeysStudio is a Studio people sign in to, whose identity the Go
// server checks, so the keys are Go's; X-Test-User names who asks.
func newKeysStudio(t *testing.T) *keysStudio {
	t.Helper()
	m, _, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "https://platform.example/keys", "STUDIO_FRONT_AUTH": "1"})
	mux := http.NewServeMux()
	Register(mux, m.A)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get("X-Test-User"); u != "" {
			r = auth.WithUser(r, &auth.User{ID: u, Username: u, Source: "proxy"})
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &keysStudio{t, Default, srv.URL}
}

// do sends a request as user with the cookie header given, and answers
// the status, the body and the Set-Cookie values.
func (s *keysStudio) do(user, method, path, body, cookie string) (int, map[string]any, []string) {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", user)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(b, &out)
	return res.StatusCode, out, res.Header.Values("Set-Cookie")
}

// jar is the cookie header a browser sends after these Set-Cookie values.
func jar(prev string, set []string) string {
	have := map[string]string{}
	var order []string
	for _, p := range strings.Split(prev, "; ") {
		if k, v, ok := strings.Cut(p, "="); ok {
			if _, seen := have[k]; !seen {
				order = append(order, k)
			}
			have[k] = v
		}
	}
	for _, c := range set {
		nv, attrs, _ := strings.Cut(c, "; ")
		k, v, _ := strings.Cut(nv, "=")
		if strings.Contains(attrs, "Max-Age=0") || v == "" {
			delete(have, k)
			continue
		}
		if _, seen := have[k]; !seen {
			order = append(order, k)
		}
		have[k] = v
	}
	var parts []string
	for _, k := range order {
		if v, ok := have[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

// saveBoth gives bob both keys, kept in his browser, and answers his
// cookie header.
func (s *keysStudio) saveBoth() string {
	s.t.Helper()
	code, _, set := s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`"}`, "")
	if code != 200 {
		s.t.Fatalf("key: %d", code)
	}
	c := jar("", set)
	code, _, set = s.do("bob", "POST", "/api/me/model-key", `{"key":"`+bobEndpoint+`","baseUrl":"`+bobURL+`/"}`, c)
	if code != 200 {
		s.t.Fatalf("endpoint: %d", code)
	}
	return jar(c, set)
}

func (s *keysStudio) req(user, cookie string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	return auth.WithUser(r, &auth.User{ID: user, Username: user, Source: "proxy"})
}

// What chat uses, and what the platform's work uses, for user.
func (s *keysStudio) uses(user, cookie string) (chatKey, chatBase, platformKey string) {
	s.t.Helper()
	if c := s.m.cred(s.req(user, cookie)); c != nil {
		chatKey, chatBase = c.Key, c.Base()
	}
	tc, _ := s.m.chatToolCtx(s.req(user, cookie), "")
	key, base, ok := s.m.A.Providers.PersonalCred(user, cookie)
	if ok && base == "" && key != deref(tc.UserKey) {
		s.t.Errorf("the platform's tools use %q, its other work %q", deref(tc.UserKey), key)
	}
	return chatKey, chatBase, deref(tc.UserKey)
}

func TestEndpointAndActivateKeyAreKeptApart(t *testing.T) {
	s := newKeysStudio(t)
	s.saveBoth()
	code, st, _ := s.do("bob", "GET", "/api/me/model-key", "", "")
	act, _ := st["activate"].(map[string]any)
	if code != 200 || st["baseUrl"] != bobURL || st["last4"] != "5678" || act == nil || act["last4"] != "1234" || act["baseUrl"] != nil {
		t.Fatalf("status %d %v", code, st)
	}
	if ck, cb, pk := s.uses("bob", ""); ck != bobEndpoint || cb != bobURL || pk != bobKey {
		t.Fatalf("both: chat %q at %q, platform %q", ck, cb, pk)
	}

	// Removing the endpoint leaves the key, for chat and the platform.
	if code, st, _ := s.do("bob", "POST", "/api/me/model-key/clear", `{"which":"endpoint"}`, ""); code != 200 || st["baseUrl"] != nil || st["last4"] != "1234" {
		t.Fatalf("remove endpoint: %d %v", code, st)
	}
	if ck, cb, pk := s.uses("bob", ""); ck != bobKey || cb != "" || pk != bobKey {
		t.Fatalf("key only: chat %q at %q, platform %q", ck, cb, pk)
	}

	// Saving the endpoint again, then removing the key, leaves the
	// endpoint, which the platform never uses.
	s.do("bob", "POST", "/api/me/model-key", `{"key":"`+bobEndpoint+`","baseUrl":"`+bobURL+`"}`, "")
	code, st, _ = s.do("bob", "DELETE", "/api/me/model-key?which=activate", "", "")
	act, _ = st["activate"].(map[string]any)
	if code != 200 || st["baseUrl"] != bobURL || act["mode"] != "none" {
		t.Fatalf("remove key: %d %v", code, st)
	}
	if ck, cb, pk := s.uses("bob", ""); ck != bobEndpoint || cb != bobURL || pk != "" {
		t.Fatalf("endpoint only: chat %q at %q, platform %q", ck, cb, pk)
	}

	// Without which, both go, as before.
	s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`"}`, "")
	if code, st, _ := s.do("bob", "DELETE", "/api/me/model-key", "", ""); code != 200 || st["mode"] != "none" || st["activate"].(map[string]any)["mode"] != "none" {
		t.Fatalf("remove both: %d %v", code, st)
	}
	if code, _, _ := s.do("bob", "POST", "/api/me/model-key/clear", `{"which":"other"}`, ""); code != 400 {
		t.Errorf("unknown which: %d", code)
	}
}

// The platform is never given the endpoint's key: the viewer's platform
// key, KeyFor, the person's tools and the copy Node is sent are the
// ACTIVATE key.
func TestPlatformWorkNeverCarriesTheEndpointKey(t *testing.T) {
	s := newKeysStudio(t)
	cookie := s.saveBoth()
	p := platform.Of(s.m.A)
	v, err := p.ViewerOf(s.req("bob", cookie))
	if err != nil {
		t.Fatal(err)
	}
	if v.PlatformKey() != bobKey || v.EndpointCred() == nil || v.EndpointCred().Key != bobEndpoint {
		t.Fatalf("viewer: platform %q, endpoint %+v", v.PlatformKey(), v.EndpointCred())
	}
	if k, own := p.KeyFor(v); k != bobKey || !own {
		t.Fatalf("KeyFor %q %v", k, own)
	}
	if tc := s.m.personCtx(s.req("bob", cookie)); deref(tc.UserKey) != bobKey {
		t.Fatalf("person's tools: %q", deref(tc.UserKey))
	}
	sealed := s.m.Keys.SealedSession("bob")
	if k, ok := s.m.Keys.Sealer().FromCookies("bob", keycookie.Cookie+"0="+sealed); !ok || k.Key != bobKey || k.IsEndpoint() {
		t.Fatalf("Node is sent %+v", k)
	}
}

func cookieNamed(set []string, name string) (value, attrs string) {
	for _, c := range set {
		nv, a, _ := strings.Cut(c, "; ")
		if k, v, _ := strings.Cut(nv, "="); k == name {
			return v, a
		}
	}
	return "", ""
}

// ade_me and ade_mep are set exactly as ade_mk and ade_mkp are, a server
// that has forgotten the keys reads both back from them, and they are
// bound to their person.
func TestEndpointCookieIsSetAndBoundLikeTheKeyCookie(t *testing.T) {
	s := newKeysStudio(t)
	code, _, set := s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`"}`, "")
	c := jar("", set)
	code, _, set = s.do("bob", "POST", "/api/me/model-key", `{"key":"`+bobEndpoint+`","baseUrl":"`+bobURL+`"}`, c)
	if code != 200 {
		t.Fatal(code)
	}
	for _, pair := range [][2]string{{"ade_mk0", "ade_me0"}, {"ade_mkp0", "ade_mep0"}} {
		kv, ka := cookieNamed(set, pair[0])
		ev, ea := cookieNamed(set, pair[1])
		if kv == "" || ev == "" || ka != ea {
			t.Fatalf("%s %q vs %s %q", pair[0], ka, pair[1], ea)
		}
		for _, want := range []string{"Max-Age=31536000", "Path=/", "HttpOnly", "Secure", "SameSite=None"} {
			if !strings.Contains(ea, want) {
				t.Errorf("%s lacks %s: %q", pair[1], want, ea)
			}
		}
		if strings.Contains(ea, "Partitioned") != (pair[1] == "ade_mep0") {
			t.Errorf("%s partitioned: %q", pair[1], ea)
		}
	}
	cookie := jar(c, set)
	// The sealed values never hold a key in the clear.
	if strings.Contains(cookie, "5678") || strings.Contains(cookie, "1234") || strings.Contains(strings.Join(set, "\n"), bobEndpoint) {
		t.Fatal("a key in the clear")
	}

	// A server that forgot the keys (a restart, or the 12 hours passed)
	// reads both from the cookies.
	s.m.Keys.Clear("bob")
	if ck, cb, pk := s.uses("bob", cookie); ck != bobEndpoint || cb != bobURL || pk != bobKey {
		t.Fatalf("hydrated: chat %q at %q, platform %q", ck, cb, pk)
	}

	// Bound to bob: ada sending his cookies, or her own ade_mk beside his
	// ade_me, gets neither of his keys.
	if ck, _, pk := s.uses("ada", cookie); ck != "" || pk != "" {
		t.Fatalf("ada with bob's cookies: chat %q, platform %q", ck, pk)
	}
	_, _, adaSet := s.do("ada", "PUT", "/api/me/model-key", `{"key":"pwt_ada.key-0001"}`, "")
	adaMk, _ := cookieNamed(adaSet, "ade_mk0")
	bobMe, _ := cookieNamed(set, "ade_me0")
	s.m.Keys.Clear("ada")
	if ck, cb, pk := s.uses("ada", "ade_mk0="+adaMk+"; ade_me0="+bobMe); ck != "pwt_ada.key-0001" || cb != "" || pk != "pwt_ada.key-0001" {
		t.Fatalf("ada with bob's ade_me: chat %q at %q, platform %q", ck, cb, pk)
	}
	// His ade_me with no ade_mk beside it, or his ACTIVATE key moved into
	// ade_me, is not read as an endpoint.
	s.m.Keys.Clear("bob")
	if ck, _, pk := s.uses("bob", "ade_me0="+bobMe); ck != "" || pk != "" {
		t.Fatalf("ade_me alone: chat %q, platform %q", ck, pk)
	}
	bobMk, _ := cookieNamed(jarSet(cookie), "ade_mk0")
	s.m.Keys.Clear("bob")
	if ck, cb, pk := s.uses("bob", "ade_mk0="+bobMk+"; ade_me0="+bobMk); ck != bobKey || cb != "" || pk != bobKey {
		t.Fatalf("key as ade_me: chat %q at %q, platform %q", ck, cb, pk)
	}
}

// jarSet turns a cookie header back into name=value items.
func jarSet(header string) []string { return strings.Split(header, "; ") }

// A cookie an earlier build wrote for an endpoint (ade_mk holding it) is
// read as the endpoint; saving an ACTIVATE key keeps it, moved to ade_me;
// and an earlier build, which reads ade_mk only, still finds the person's
// ACTIVATE key, or their endpoint when they have no key.
func TestOldCookiesAndRollingBack(t *testing.T) {
	s := newKeysStudio(t)
	sealer := s.m.Keys.Sealer()
	u := bobURL
	old, _ := sealer.Seal(keycookie.Key{Sub: "bob", Key: bobEndpoint, BaseURL: &u})
	cookie := "ade_mk0=" + old[0] + "; ade_mkp0=" + old[0]
	if ck, cb, pk := s.uses("bob", cookie); ck != bobEndpoint || cb != bobURL || pk != "" {
		t.Fatalf("old endpoint cookie: chat %q at %q, platform %q", ck, cb, pk)
	}
	code, st, set := s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`"}`, cookie)
	if code != 200 || st["baseUrl"] != bobURL || st["activate"].(map[string]any)["last4"] != "1234" {
		t.Fatalf("key beside an old endpoint: %d %v", code, st)
	}
	cookie = jar(cookie, set)
	key, ep := sealer.Both("bob", cookie)
	if key == nil || key.Key != bobKey || ep == nil || ep.Key != bobEndpoint {
		t.Fatalf("cookies now: %+v %+v", key, ep)
	}
	// What v1.203.32 reads (keycookie.FromCookies on ade_mk, ade_mkp).
	if k, ok := sealer.FromCookies("bob", cookie); !ok || k.Key != bobKey || k.IsEndpoint() {
		t.Fatalf("rolled back, both: %+v", k)
	}
	_, _, set = s.do("bob", "POST", "/api/me/model-key/clear", `{"which":"activate"}`, cookie)
	cookie = jar(cookie, set)
	if k, ok := sealer.FromCookies("bob", cookie); !ok || k.Key != bobEndpoint || !k.IsEndpoint() {
		t.Fatalf("rolled back, endpoint only: %+v", k)
	}
	if strings.Contains(cookie, "ade_me0=") {
		t.Errorf("ade_me kept beside an endpoint in ade_mk: %s", cookie)
	}
	// Not kept in the browser: the cookie keeps only what was.
	_, _, set = s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`","persist":false}`, cookie)
	cookie = jar(cookie, set)
	if k, ok := sealer.FromCookies("bob", cookie); !ok || k.Key != bobEndpoint {
		t.Fatalf("unkept key written: %+v", k)
	}
	if ck, _, pk := s.uses("bob", cookie); ck != bobEndpoint || pk != bobKey {
		t.Fatalf("unkept key not used: chat %q, platform %q", ck, pk)
	}
}

// Two sealed values more on each request: with a typical token and
// endpoint, the key cookies stay well inside the 8 KB a proxy commonly
// allows for the whole Cookie header.
func TestKeyCookiesSize(t *testing.T) {
	s := newKeysStudio(t)
	token := "pwt_" + strings.Repeat("t", 1200)
	_, _, set := s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+token+`"}`, "")
	c := jar("", set)
	_, _, set = s.do("bob", "POST", "/api/me/model-key", `{"key":"sk-`+strings.Repeat("e", 60)+`","baseUrl":"`+bobURL+`","endpoint":{"headers":[{"name":"X-Org","value":"`+strings.Repeat("o", 40)+`"}],"models":["model-a","model-b"]}}`, c)
	c = jar(c, set)
	t.Logf("key cookies: %d bytes in the Cookie header (%d for ade_me and ade_mep)", len(c), len(c)-len(strings.Split(c, "; ade_me0=")[0]))
	if len(c) > 6000 {
		t.Errorf("key cookies take %d bytes", len(c))
	}
}

// On a Studio of one, with sign-in off, both are kept on this computer.
func TestOneOwnerKeepsBothThroughTheRoutes(t *testing.T) {
	m, srv, _, _ := newTestModule(t, nil)
	if code, _, _ := send(t, "POST", srv.URL+"/api/me/model-key", `{"key":"`+bobEndpoint+`","baseUrl":"`+bobURL+`"}`); code != 200 {
		t.Fatal(code)
	}
	code, out, h := send(t, "PUT", srv.URL+"/api/me/model-key", `{"key":"`+bobKey+`"}`)
	act, _ := out["activate"].(map[string]any)
	if code != 200 || out["baseUrl"] != bobURL || out["mode"] != "stored" || act == nil || act["last4"] != "1234" || len(h.Values("Set-Cookie")) != 0 {
		t.Fatalf("%d %v", code, out)
	}
	if key, base, ok := m.A.Providers.PersonalCred("single", ""); !ok || key != bobKey || base != "" {
		t.Fatalf("platform: %q %q %v", key, base, ok)
	}
	if code, out, _ := send(t, "POST", srv.URL+"/api/me/model-key/clear", `{"which":"activate"}`); code != 200 || out["baseUrl"] != bobURL {
		t.Fatalf("remove key: %d %v", code, out)
	}
}

// Where the Go server keeps the keys, the viewer (Accounts, workflows,
// the platform's other work) reads the same key store as the routes: a
// key not kept in the browser counts, and so does a key whose cookie the
// request does not carry.
func TestTheViewerReadsTheKeyStore(t *testing.T) {
	s := newKeysStudio(t)
	if code, _, _ := s.do("bob", "PUT", "/api/me/model-key", `{"key":"`+bobKey+`","persist":false}`, ""); code != 200 {
		t.Fatal(code)
	}
	v, err := platform.Of(s.m.A).ViewerOf(s.req("bob", ""))
	if err != nil || v.PlatformKey() != bobKey {
		t.Fatalf("viewer platform key %q, %v", v.PlatformKey(), err)
	}
	if key, base, ok := s.m.A.Providers.PersonalCred("bob", ""); !ok || key != bobKey || base != "" {
		t.Fatalf("PersonalCred %q %q %v", key, base, ok)
	}
	// Nobody else is given it.
	if v, _ := platform.Of(s.m.A).ViewerOf(s.req("ada", "")); v.PlatformKey() != "" {
		t.Fatalf("ada's viewer has %q", v.PlatformKey())
	}
}
