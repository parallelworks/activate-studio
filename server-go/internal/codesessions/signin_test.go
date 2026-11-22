package codesessions

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// signedInServer is the Code page's routes on a Studio with sign-in on,
// each request asked by viewer().
func signedInServer(t *testing.T, env map[string]string, sites bool, viewer func() *platform.Viewer, own func(r *http.Request) []string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	env["INDEX_BASE"], env["KB_ROOT"], env["STUDIO_WEB_DIST"] = filepath.Join(dir, "ix"), filepath.Join(dir, "kb"), filepath.Join(dir, "web")
	env["AUTH_JWKS_URL"] = "https://id.platform.example/jwks"
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	if own != nil {
		a.Providers.OwnSessions = append(a.Providers.OwnSessions, own)
	}
	p := platform.New(platform.Options{IndexBase: env["INDEX_BASE"], Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	was := agentsites.Current()
	if sites {
		agentsites.SetCurrent(&agentsites.Sites{P: p, Daemon: pwcode.New(p)})
	} else {
		agentsites.SetCurrent(nil)
	}
	t.Cleanup(func() { agentsites.SetCurrent(was) })
	mux := http.NewServeMux()
	Register(mux, a, p)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, platform.WithViewer(r, viewer()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getCode(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// With sign-in on, a person with no ACTIVATE credential of their own is
// not given the deployment's: the Studio's account runs other people's
// delegated and standing agents, so listing, reading or answering with it
// would show and answer theirs. With their own sign-in, it is theirs.
func TestSignedInUsesOnlyTheirOwnKey(t *testing.T) {
	var mu sync.Mutex
	used := []string{}
	var got answers
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		used = append(used, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/code/sessions":
			json.NewEncoder(w).Encode(map[string]any{"machines": []any{map[string]any{"id": "m1", "name": "box"}},
				"sessions": []any{map[string]any{"id": sid, "machineId": "m1", "name": "someone's delegated work", "status": "awaiting_approval"}}})
		case r.URL.Path == "/api/code/machines/m1/sessions/"+sid:
			json.NewEncoder(w).Encode(map[string]any{"id": sid, "status": "awaiting_approval", "history": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/approvals/ap1"):
			got.take(r)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer plat.Close()
	var v *platform.Viewer
	base := signedInServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1", "PW_API_KEY": "deployment-key"}, false,
		func() *platform.Viewer { return v }, nil).URL
	answer := `{"machine":"m1","id":"` + sid + `","approval":"ap1","allowed":true}`

	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam-id", Username: "sam"}}
	if code, out := getCode(t, base+"/api/code-sessions"); code != 409 || !strings.Contains(out["error"].(string), "your own ACTIVATE sign-in") {
		t.Fatalf("list with no key of their own: %d %v", code, out)
	}
	if code, _ := getCode(t, base+"/api/code-sessions/item?machine=m1&id="+sid); code != 409 {
		t.Fatalf("item with no key of their own: %d", code)
	}
	if code, _ := postAnswer(t, base, answer); code != 409 {
		t.Fatalf("approve with no key of their own: %d", code)
	}
	if len(used) != 0 || len(got.got) != 0 {
		t.Fatalf("the platform was asked with %v, answers %v", used, got.got)
	}
	// A key for a model provider is not a platform credential.
	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam-id"}, Cred: &platform.Cred{Key: "k", BaseURL: "https://models.example.com"}}
	if code, _ := postAnswer(t, base, answer); code != 409 || len(used) != 0 {
		t.Fatalf("a provider key: %d %v", code, used)
	}
	v = &platform.Viewer{AuthEnabled: true}
	if code, _ := getCode(t, base+"/api/code-sessions"); code != 401 {
		t.Fatalf("signed out: %d", code)
	}

	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}, Cred: &platform.Cred{Key: "alex-own-key"}}
	if code, out := getCode(t, base+"/api/code-sessions"); code != 200 || len(out["sessions"].([]any)) != 1 {
		t.Fatalf("list as alex: %d %v", code, out)
	}
	if code, out := postAnswer(t, base, answer); code != 200 || out["ok"] != true || len(got.got) != 1 {
		t.Fatalf("approve as alex: %d %v %v", code, out, got.got)
	}
	for _, k := range used {
		if k != "Bearer alex-own-key" {
			t.Fatalf("the platform was asked with %q", k)
		}
	}
	// With sign-in off, the deployment's key is the one person's, as before.
	used = nil
	off := codeServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1", "PW_API_KEY": "deployment-key"}, false).URL
	if code, _ := getCode(t, off+"/api/code-sessions"); code != 200 || len(used) != 1 || used[0] != "Bearer deployment-key" {
		t.Fatalf("sign-in off: %d %v", code, used)
	}
}

// With sign-in on and this host offered (AGENT_LOCAL_SITE=1), every
// session on its daemon runs as the Studio's account: a person sees and
// answers only those the Studio started for them.
func TestSignedInOnThisComputerSeesOnlyTheirs(t *testing.T) {
	const mine, theirs = "20261121-120000-aaaaaa", "20261121-120000-bbbbbb"
	var got answers
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"protocolVersion": pwcode.ProtocolVersion, "machineId": "mach1", "hostname": "box.example"})
		case r.URL.Path == "/v1/sessions":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": mine, "name": "my summary", "status": "idle"}, map[string]any{"id": theirs, "name": "sam's agent", "status": "awaiting_approval"}})
		case r.URL.Path == "/v1/sessions/"+mine || r.URL.Path == "/v1/sessions/"+theirs:
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "status": "awaiting_approval", "history": []any{}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/approvals/"):
			got.take(r)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	var v *platform.Viewer
	own := func(r *http.Request) []string {
		if pv, _ := platform.New(platform.Options{}).ViewerOf(r); pv != nil && pv.UserID() == "alex-id" {
			return []string{mine}
		}
		return nil
	}
	base := signedInServer(t, map[string]string{"PW_CODE_SOCKET": sock, "AGENT_LOCAL_SITE": "1"}, true, func() *platform.Viewer { return v }, own).URL

	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}}
	code, out := getCode(t, base+"/api/code-sessions")
	if code != 200 || out["local"] != true {
		t.Fatalf("list: %d %v", code, out)
	}
	if ss := out["sessions"].([]any); len(ss) != 1 || ss[0].(map[string]any)["id"] != mine {
		t.Fatalf("alex sees %v", ss)
	}
	if code, _ := getCode(t, base+"/api/code-sessions/item?machine=mach1&id="+theirs); code != 404 {
		t.Fatalf("someone else's session read: %d", code)
	}
	if code, _ := getCode(t, base+"/api/code-sessions/item?machine=mach1&id="+mine); code != 200 {
		t.Fatalf("their own session: %d", code)
	}
	if code, _ := postAnswer(t, base, `{"machine":"mach1","id":"`+theirs+`","approval":"ap1","allowed":true}`); code != 404 || len(got.got) != 0 {
		t.Fatalf("someone else's call approved: %d %v", code, got.got)
	}
	if code, _ := postAnswer(t, base, `{"machine":"mach1","id":"`+mine+`","approval":"ap1","allowed":true}`); code != 200 || len(got.got) != 1 {
		t.Fatalf("their own call: %d %v", code, got.got)
	}
	// Another person, and nobody signed in, see none of them.
	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam2-id"}}
	if _, out := getCode(t, base+"/api/code-sessions"); len(out["sessions"].([]any)) != 0 {
		t.Fatalf("another person sees %v", out["sessions"])
	}
	v = &platform.Viewer{AuthEnabled: true}
	if code, out := getCode(t, base+"/api/code-sessions"); code == 200 && out["local"] == true {
		t.Fatalf("signed out reads this computer: %v", out)
	}
	if code, _ := postAnswer(t, base, `{"machine":"mach1","id":"`+mine+`","approval":"ap1","allowed":true}`); code == 200 || len(got.got) != 1 {
		t.Fatalf("signed out answered: %d", code)
	}
}
