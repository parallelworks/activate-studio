package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The entry the Studio writes asks the pw CLI on the machine for its token
// at run time and sends each message to the Studio with it, in a header
// curl reads from a file, never on a command line; it renews the token on
// a 401, writes nothing for a notification, and answers a request the
// Studio refused with a JSON-RPC error, so pw code is not left waiting.
func TestTheEntryUsesTheMachinesOwnSignIn(t *testing.T) {
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	// A pw CLI whose token changes at each renewal, as a sign-in does.
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte("#!/bin/sh\nn=$(cat "+dir+"/n 2>/dev/null || echo 0); n=$((n+1)); echo $n >"+dir+"/n\n[ \"$1 $2 $3\" = 'auth token --print' ] && echo tok$n\n"), 0o755)
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case r.Header.Get("Authorization") == "Bearer tok1":
			w.WriteHeader(401) // the first token has expired
		case strings.Contains(string(b), `"notifications/`):
			w.WriteHeader(202)
		case strings.Contains(string(b), `"boom"`):
			w.WriteHeader(502)
			io.WriteString(w, "<html>bad gateway</html>")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "{\"jsonrpc\":\"2.0\",\"id\":1,\n\"result\":{\"tools\":[{\"name\":\"search_kb\"}]}}\n")
		}
	}))
	defer srv.Close()
	cmd := exec.Command("sh", "-c", bridgeShim, bridgeShimName, srv.URL+"/api/mcp", pw)
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":"x7","method":"boom","params":{"id":3}}`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("answers %q", out)
	}
	var first, second map[string]any
	if json.Unmarshal([]byte(lines[0]), &first) != nil || first["result"] == nil {
		t.Errorf("the answer is not one line of JSON: %q", lines[0])
	}
	if json.Unmarshal([]byte(lines[1]), &second) != nil || second["id"] != "x7" || second["error"] == nil {
		t.Errorf("a refused request: %q", lines[1])
	}
	if want := []string{"Bearer tok1", "Bearer tok2", "Bearer tok2", "Bearer tok2"}; !slices.Equal(seen, want) {
		t.Errorf("tokens sent %v, want %v", seen, want)
	}
	if strings.Contains(bridgeShim, "${") || strings.Contains(bridgeShim, "Bearer $t") && !strings.Contains(bridgeShim, `>"$d/h"`) {
		t.Error("the shim puts the token where pw code or ps can read it")
	}
}

// fakeMachine is a machine reached through pw ssh: it answers the Studio's
// steps as pw code there would, keeping its studio entry, and passes a
// tried tools/list on to the Studio as the person's session would.
type fakeMachine struct {
	mu    sync.Mutex
	who   string
	curl  bool
	entry *bridgeEntry
	adds  int
	// attached is each live attach: key, machine, session and URL.
	attached []string
	calls    []string // site and key of each run
	// rules are the allow rules in its settings; rulesWhy is why they cannot change.
	rules    []string
	rulesWhy string
	m        *Module
	as       *auth.User
}

func (f *fakeMachine) run(_ context.Context, site, key string, argv []string, _ time.Duration) (remote.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, site+" "+key)
	switch {
	case len(argv) > 3 && argv[2] == bridgeLook:
		out := "pw=/home/x/pw/pw\nwho=" + f.who + "\n"
		if f.curl {
			out += "curl=1\n"
		}
		if f.entry != nil {
			b, _ := json.Marshal(f.entry)
			out += "entry=" + string(b) + "\n"
		}
		return remote.Result{Stdout: out}, nil
	case len(argv) > 3 && argv[1] == "code" && argv[3] == "add":
		f.adds++
		e := &bridgeEntry{}
		dash := slices.Index(argv, "--")
		e.Config.Command, e.Config.Args = argv[dash+1], argv[dash+2:]
		f.entry = e
		return remote.Result{Stdout: "Added"}, nil
	case len(argv) > 5 && argv[2] == bridgeRules:
		if f.rulesWhy != "" {
			return remote.Result{Stdout: "why=" + f.rulesWhy + "\n"}, nil
		}
		n := 0
		for _, x := range strings.Fields(argv[5]) {
			i := slices.Index(f.rules, x)
			if argv[4] == "add" && i < 0 {
				f.rules, n = append(f.rules, x), n+1
			} else if argv[4] == "remove" && i >= 0 {
				f.rules, n = slices.Delete(f.rules, i, i+1), n+1
			}
		}
		return remote.Result{Stdout: "changed=" + strconv.Itoa(n) + "\n"}, nil
	case len(argv) > 3 && argv[1] == "code" && argv[3] == "remove":
		f.entry = nil
		return remote.Result{Stdout: "Removed"}, nil
	case len(argv) > 3 && argv[2] == bridgeTry:
		f.mu.Unlock()
		defer f.mu.Lock()
		r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(argv[4])), f.as)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		if err := f.m.mcpPost(w, r); err != nil {
			return remote.Result{}, err
		}
		return remote.Result{Stdout: w.Body.String()}, nil
	}
	return remote.Result{Code: 1, Stderr: "unknown step"}, nil
}

// setupStudio is a Studio behind the session proxy where Bob has a relayed
// chat with a session on his cluster, and his own ACTIVATE sign-in.
func setupStudio(t *testing.T) (*Module, *fakeMachine, string) {
	t.Helper()
	m := bridgeStudio(t)
	// The Studio has an account of its own (PW_API_KEY k), never used here.
	f := &fakeMachine{who: "bob", curl: true, m: m, as: bobProxy}
	m.bridgeRunFn = f.run
	machines := map[string]bridgeMach{
		"m-bob":    {ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"},
		"m-laptop": {ID: "m-laptop", Kind: "local", Name: "bobs-laptop", Owner: "bob", Status: "connected"},
		"m-ada":    {ID: "m-ada", Kind: "cloud", Name: "adas-cluster", Owner: "ada", Status: "connected"},
	}
	m.bridgeMachineFn = func(r *http.Request, id string) (bridgeMach, error) {
		if mach, ok := machines[id]; ok {
			return mach, nil
		}
		return bridgeMach{}, platform.KbError(404, "not yours")
	}
	m.bridgeMachinesFn = func(r *http.Request) ([]bridgeMach, error) {
		return []bridgeMach{machines["m-bob"], machines["m-laptop"]}, nil
	}
	m.bridgeAttachFn = func(r *http.Request, key, machine, session, url string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attached = append(f.attached, key+" "+machine+" "+session+" "+url)
		return nil
	}
	title := "Relayed"
	c := m.Conv.CreateWith(&title, "bob", nil, &conversations.PWCodeLink{Relay: true, Machine: "m-bob", MachineName: "bobs-cluster", Session: "s1", Site: "relay:m-bob"})
	m.Keys.Set("bob", "pwt_bobs.key", nil, nil)
	m.Convs.AuthEnabled = func() bool { return true }
	viewerKeys = map[string]string{"bob": "pwt_bobs.key", "ada": "pwt_adas.key"}
	return m, f, c.ID
}

// viewerKeys is each person's own ACTIVATE credential in the Studio.
var viewerKeys map[string]string

// asViewer is r as u, with their own credential when they have one.
func asViewer(r *http.Request, u *auth.User) *http.Request {
	r = auth.WithUser(r, u)
	v := &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: u.ID, Username: u.Username}}
	if k := viewerKeys[u.ID]; k != "" {
		v.Cred = &platform.Cred{Key: k}
	}
	return platform.WithViewer(r, v)
}

// setupReq is a request from u's browser through the session proxy.
func setupReq(u *auth.User) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/mcp/bridge/setup", nil)
	r.Header.Set("Content-Type", "application/json")
	r.Host = "127.0.0.1:8080"
	r.Header.Set("X-Forwarded-Host", "platform.example")
	r.Header.Set("X-Forwarded-Prefix", "/me/session/ada/studio")
	return asViewer(r, u)
}

func setupAs(t *testing.T, m *Module, u *auth.User, body string) (int, map[string]any) {
	t.Helper()
	r := setupReq(u)
	r.Body = io.NopCloser(strings.NewReader(body))
	w := httptest.NewRecorder()
	err := m.bridgeSetup(w, r)
	var out map[string]any
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Status, map[string]any{"error": ae.Message}
	} else if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// "Set up for me" adds the Studio to pw code on the chat's machine through
// pw ssh as the person, with their own credential, tries it, and says it
// worked; a second time it changes nothing.
func TestSetUpForMeAddsTheStudioOnTheMachine(t *testing.T) {
	m, f, id := setupStudio(t)
	// From the Studio's own page through the session proxy.
	r := setupReq(bobProxy)
	r.Header.Set("Origin", "https://platform.example")
	r.Body = io.NopCloser(strings.NewReader(`{"conversationId":"` + id + `"}`))
	w := httptest.NewRecorder()
	if err := m.bridgeSetup(w, r); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["done"] != true || out["changed"] != true || out["worked"] != true {
		t.Fatalf("%d %v", w.Code, out)
	}
	if n, _ := out["tools"].(float64); n < 10 {
		t.Errorf("tools %v", out["tools"])
	}
	at, ours, current := f.entry.target()
	if !ours || !current || at != "https://platform.example/me/session/ada/studio/api/mcp" {
		t.Errorf("entry %+v", f.entry.Config)
	}
	if f.entry.Config.Args[4] != "/home/x/pw/pw" {
		t.Errorf("the entry does not name the pw it found: %v", f.entry.Config.Args)
	}
	for _, c := range f.calls {
		if c != "pw://bob/bobs-cluster pwt_bobs.key" {
			t.Errorf("ran as %q, not with Bob's own key on his cluster", c)
		}
	}
	if out["live"] != "next" || len(f.attached) != 0 {
		t.Errorf("a daemon that does not attach to a live session: %v %v", out["live"], f.attached)
	}
	// Again: the machine is known to have this Studio, so it is not opened.
	n := len(f.calls)
	code, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	if code != 200 || out["changed"] != false || out["already"] != true || out["worked"] != true || f.adds != 1 || len(f.calls) != n {
		t.Fatalf("again: %d %v, %d adds, %d runs", code, out, f.adds, len(f.calls)-n)
	}
	// Asked to check, it looks and tries, and still adds nothing.
	code, out = setupAs(t, m, bobProxy, `{"conversationId":"`+id+`","check":true}`)
	if code != 200 || out["changed"] != false || out["worked"] != true || f.adds != 1 || len(f.calls) == n {
		t.Fatalf("checked: %d %v, %d adds", code, out, f.adds)
	}
	// By machine, from the Code page, the same.
	code, out = setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`)
	if code != 200 || out["worked"] != true || f.adds != 1 {
		t.Fatalf("by machine: %d %v", code, out)
	}
}

// Found already set up for this Studio by hand, the machine is recorded
// as set and left as it is.
func TestAMachineAlreadySetUpIsSkipped(t *testing.T) {
	m, f, id := setupStudio(t)
	f.entry = &bridgeEntry{}
	f.entry.Config.Type, f.entry.Config.URL = "http", "https://platform.example/me/session/ada/studio/api/mcp"
	if _, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); out["changed"] != false || out["worked"] != true || f.adds != 0 {
		t.Fatalf("%v, %d adds", out, f.adds)
	}
	r := setupReq(bobProxy)
	if o := m.bridgeOffer(r, bridgeMach{ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"}); o.State != "set" {
		t.Errorf("offer %+v", o)
	}
}

// Where the machine's pw code attaches a server to a live session, the
// session in front of the person gets the tools at once, through the
// platform as them, and the machine sends its own credential.
func TestTheSessionInFrontGetsThemWhereTheDaemonAttaches(t *testing.T) {
	m, f, id := setupStudio(t)
	inner := m.bridgeMachineFn
	m.bridgeMachineFn = func(r *http.Request, mid string) (bridgeMach, error) {
		mach, err := inner(r, mid)
		mach.Attach = true
		return mach, err
	}
	_, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	if out["live"] != "now" || !slices.Equal(f.attached, []string{"pwt_bobs.key m-bob s1 https://platform.example/me/session/ada/studio/api/mcp"}) {
		t.Fatalf("%v %v", out, f.attached)
	}
	// By machine there is no session in front to attach to.
	f.attached = nil
	if _, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`); out["live"] != "next" || len(f.attached) != 0 {
		t.Errorf("by machine: %v %v", out, f.attached)
	}
}

// A machine the person said not to add the tools on is not offered again
// until they choose it; the Code page lists each machine's state.
func TestADeclinedMachineIsNotOfferedAgain(t *testing.T) {
	m, _, _ := setupStudio(t)
	list := func() map[string]string {
		w := httptest.NewRecorder()
		if err := m.bridgeMachinesRoute(w, setupReq(bobProxy)); err != nil {
			t.Fatal(err)
		}
		var out struct {
			Machines []bridgeOfferT `json:"machines"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		states := map[string]string{}
		for _, o := range out.Machines {
			states[o.MachineID] = o.State
		}
		return states
	}
	if got := list(); got["m-bob"] != "offer" || got["m-laptop"] != "command" {
		t.Fatalf("%v", got)
	}
	prefs := func(u *auth.User, body string) int {
		r := setupReq(u)
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		var ae *apierr.Error
		if err := m.bridgeDecline(w, r); errors.As(err, &ae) {
			return ae.Status
		} else if err != nil {
			t.Fatal(err)
		}
		return w.Code
	}
	if prefs(bobProxy, `{"machineId":"m-bob","add":false}`) != 200 || list()["m-bob"] != "declined" {
		t.Fatalf("declined: %v", list())
	}
	// Kept across a restart of the Studio.
	m.bridgePrefStore = nil
	if list()["m-bob"] != "declined" {
		t.Fatal("forgotten")
	}
	if prefs(bobProxy, `{"machineId":"m-bob","add":true}`) != 200 || list()["m-bob"] != "offer" {
		t.Fatalf("chosen again: %v", list())
	}
	if prefs(bobProxy, `{"machineId":"m-gone","add":false}`) != 409 {
		t.Error("a machine not listed for him")
	}
}

// An entry for another Studio is replaced only once the person says so;
// one they added by hand for this Studio is left as it is.
func TestAnotherStudiosEntryIsReplacedOnlyWhenAsked(t *testing.T) {
	m, f, id := setupStudio(t)
	f.entry = &bridgeEntry{}
	f.entry.Config.Type, f.entry.Config.URL = "http", "https://platform.example/me/session/cy/old/api/mcp"
	_, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	if out["ask"] != true || out["current"] != "https://platform.example/me/session/cy/old/api/mcp" || f.adds != 0 {
		t.Fatalf("not asked first: %v, %d adds", out, f.adds)
	}
	_, out = setupAs(t, m, bobProxy, `{"conversationId":"`+id+`","replace":true}`)
	if out["replaced"] != true || out["worked"] != true || f.adds != 1 {
		t.Fatalf("replace: %v", out)
	}
	f.entry = &bridgeEntry{}
	f.entry.Config.Type, f.entry.Config.URL = "http", "https://platform.example/me/session/ada/studio/api/mcp"
	f.entry.Config.Headers = map[string]string{"Authorization": "Bearer <hidden>"}
	if _, out = setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); out["changed"] != false || f.adds != 1 {
		t.Fatalf("an entry by hand for this Studio: %v", out)
	}
}

// Nothing runs on a machine but as its owner, with their own credential:
// not for a second person, not with the Studio's account, not on a
// computer pw ssh cannot open, and not where pw there is someone else's.
func TestSetupRunsOnlyAsTheMachinesOwner(t *testing.T) {
	m, f, id := setupStudio(t)
	ada := &auth.User{ID: "ada", Username: "ada", Source: "proxy"}
	for what, c := range map[string]struct {
		u    *auth.User
		body string
		want int
	}{
		"someone else's chat": {ada, `{"conversationId":"` + id + `"}`, 404},
		"no chat":             {bobProxy, `{"conversationId":"nope"}`, 404},
		"bob's machine":       {ada, `{"machineId":"m-bob"}`, 403},
		"bob's laptop":        {ada, `{"machineId":"m-laptop"}`, 409},
	} {
		if code, out := setupAs(t, m, c.u, c.body); code != c.want {
			t.Errorf("%s: %d %v", what, code, out)
		}
	}
	// Where chats are shared, a relayed chat is still only its owner's.
	m.Convs.SharedHistory = func() bool { return true }
	if code, out := setupAs(t, m, ada, `{"conversationId":"`+id+`"}`); code != 404 {
		t.Errorf("someone else's shared chat: %d %v", code, out)
	}
	m.Convs.SharedHistory = func() bool { return false }
	// A page of another site cannot set it up as him.
	for _, h := range []map[string]string{{"Origin": "https://elsewhere.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		r := setupReq(bobProxy)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		r.Body = io.NopCloser(strings.NewReader(`{"conversationId":"` + id + `"}`))
		var ae *apierr.Error
		if err := m.bridgeSetup(httptest.NewRecorder(), r); !errors.As(err, &ae) || ae.Status != 403 {
			t.Errorf("from another site (%v): %v", h, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran %v", f.calls)
	}
	// Without a credential of his own, the Studio's account is not used.
	delete(viewerKeys, "bob")
	if code, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); code != 409 || !strings.Contains(out["error"].(string), "your own ACTIVATE sign-in") {
		t.Errorf("without his key: %d %v", code, out)
	}
	viewerKeys["bob"] = "pwt_bobs.key"
	at := func(machine string) {
		m.Conv.SetPWCode(id, &conversations.PWCodeLink{Relay: true, Machine: machine, MachineName: "bobs-cluster", Session: "s1", Site: "relay:" + machine})
	}
	for machine, want := range map[string]string{"m-laptop": "cannot open a terminal", "m-ada": "not yours", "m-gone": "did not list"} {
		at(machine)
		if code, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); code < 400 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s: %d %v", machine, code, out)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran %v", f.calls)
	}
	at("m-bob")
	f.who = "cluster-session:bob/bobs-cluster/1"
	if code, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); code != 409 || !strings.Contains(out["error"].(string), "signed in as cluster-session") || f.adds != 0 {
		t.Errorf("pw there as someone else: %d %v", code, out)
	}
	f.who, f.curl = "bob", false
	if code, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); code != 409 || !strings.Contains(out["error"].(string), "curl") || f.adds != 0 {
		t.Errorf("no curl: %d %v", code, out)
	}
	// MCP access off: nothing runs.
	f.calls = nil
	t.Setenv("MCP_ENABLED", "0")
	if code, _ := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); code != 409 || len(f.calls) != 0 {
		t.Errorf("MCP access off: %d, ran %v", code, f.calls)
	}
}

// The card offers "Set up for me" for the person's own relayed chat on a
// machine pw ssh reaches, and says why not elsewhere.
func TestTheCardOffersSetupWhereItCanRun(t *testing.T) {
	m, _, id := setupStudio(t)
	get := func(u *auth.User, conv string) map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/api/mcp/bridge?conversation="+conv, nil)
		r.Header.Set("X-Forwarded-Host", "platform.example")
		r = asViewer(r, u)
		w := httptest.NewRecorder()
		if err := m.bridgeRoute(w, r); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		s, _ := out["setup"].(map[string]any)
		return s
	}
	if s := get(bobProxy, id); s["available"] != true || s["machine"] != "bobs-cluster" {
		t.Errorf("bob's card: %v", s)
	}
	if s := get(&auth.User{ID: "ada", Username: "ada", Source: "proxy"}, id); s["available"] != false {
		t.Errorf("ada's card for bob's chat: %v", s)
	}
}

// The machines the Studio lists to Bob, by id.
func offersOf(t *testing.T, m *Module, u *auth.User) map[string]bridgeOfferT {
	t.Helper()
	w := httptest.NewRecorder()
	if err := m.bridgeMachinesRoute(w, setupReq(u)); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []bridgeOfferT `json:"machines"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	by := map[string]bridgeOfferT{}
	for _, o := range out.Machines {
		by[o.MachineID] = o
	}
	return by
}

// A computer that connected itself has no pw ssh: the person adds the tools
// with one command, and the Studio knows once a session there reaches them.
func TestAComputerThatConnectedItselfIsGivenACommand(t *testing.T) {
	m, f, _ := setupStudio(t)
	o := offersOf(t, m, bobProxy)["m-laptop"]
	if o.State != "command" || o.Available || !strings.HasPrefix(o.Command, "pw code mcp add --scope user") || !strings.Contains(o.Command, "/me/session/ada/studio/api/mcp?machine=m-laptop'") || o.Uncommand != "pw code mcp remove --scope user studio" {
		t.Fatalf("%+v", o)
	}
	if strings.Contains(o.Command, "pwt_") {
		t.Error("the command carries a key")
	}
	// A session of his starting there.
	reach := func(u *auth.User, machine, method string) {
		r := asViewer(httptest.NewRequest(http.MethodPost, "/api/mcp?machine="+machine, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`)), u)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-Host", "platform.example")
		r.Header.Set("X-Forwarded-Prefix", "/me/session/ada/studio")
		w := httptest.NewRecorder()
		if err := m.mcpPost(w, r); err != nil || w.Code != 200 {
			t.Fatalf("%d %v", w.Code, err)
		}
	}
	title := "Started there"
	made := func(machine string, ago time.Duration) string {
		return m.Conv.CreateWith(&title, "bob", nil, &conversations.PWCodeLink{Relay: true, Picked: true, Machine: machine, Session: "s9", ToolsError: "x", CreatedAt: time.Now().Add(-ago).UTC().Format("2006-01-02T15:04:05.000Z")}).ID
	}
	now, before, elsewhere := made("m-laptop", time.Minute), made("m-laptop", time.Hour), made("m-bob", time.Minute)
	// One already open calls on; only a session that starts says so.
	reach(bobProxy, "m-laptop", "tools/list")
	if o := offersOf(t, m, bobProxy)["m-laptop"]; o.State != "command" {
		t.Fatalf("after a call: %+v", o)
	}
	reach(bobProxy, "m-laptop", "initialize")
	reach(bobProxy, "../m", "initialize")
	o = offersOf(t, m, bobProxy)["m-laptop"]
	if o.State != "set" || o.Available || o.Command == "" || o.Uncommand == "" || len(f.calls) != 0 {
		t.Fatalf("%+v, ran %v", o, f.calls)
	}
	if l := m.Conv.Get(now).PWCode; !l.HasTools || l.ToolsError != "" {
		t.Errorf("the session that just started there: %+v", l)
	}
	if m.Conv.Get(before).PWCode.HasTools || m.Conv.Get(elsewhere).PWCode.HasTools {
		t.Error("a session that did not reach the tools was said to have them")
	}
	// Each person's own.
	if p := m.bridgePrefs().get("ada", "m-laptop"); p.URL != "" {
		t.Errorf("ada's %+v", p)
	}
	// Taken out by hand, it is as it was.
	r := setupReq(bobProxy)
	r.Body = io.NopCloser(strings.NewReader(`{"machineId":"m-laptop","forget":true}`))
	if err := m.bridgeDecline(httptest.NewRecorder(), r); err != nil || offersOf(t, m, bobProxy)["m-laptop"].State != "command" {
		t.Fatalf("%v %+v", err, offersOf(t, m, bobProxy)["m-laptop"])
	}
}

// The entry a pasted command wrote is this Studio's own, though its address
// names the machine: Set up leaves it, and Remove takes it.
func TestAnEntryThatNamesItsMachineIsThisStudios(t *testing.T) {
	m, f, id := setupStudio(t)
	f.entry = &bridgeEntry{}
	f.entry.Config.Command = "sh"
	f.entry.Config.Args = []string{"-c", bridgeShim, bridgeShimName, "https://platform.example/me/session/ada/studio/api/mcp?machine=m-bob", "/home/x/pw/pw"}
	if _, out := setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`); out["changed"] != false || out["worked"] != true || f.adds != 0 {
		t.Fatalf("%v, %d adds", out, f.adds)
	}
	r := setupReq(bobProxy)
	r.Body = io.NopCloser(strings.NewReader(`{"machineId":"m-bob"}`))
	if err := m.bridgeRemove(httptest.NewRecorder(), r); err != nil || f.entry != nil {
		t.Fatalf("%v %v", err, f.entry)
	}
}

// Where a machine's pw code takes the tools with a session, nothing is set
// up on it, a computer that connected itself included.
func TestAMachineThatTakesThemWithASessionNeedsNoCommand(t *testing.T) {
	m, _, _ := setupStudio(t)
	for _, mach := range []bridgeMach{{ID: "m-laptop", Kind: "local", Name: "bobs-laptop", Owner: "bob", Status: "connected", Attach: true}, {ID: "m-laptop", Kind: "local", Name: "bobs-laptop", Owner: "bob", Status: "connected", Create: true}} {
		if o := m.bridgeOffer(setupReq(bobProxy), mach); o.State != "offer" || !o.Attach || o.Available || o.Command != "" {
			t.Errorf("%+v: %+v", mach, o)
		}
	}
}

// The tools are added as a session starts: once the person has chosen it
// for that machine, and quietly from then on.
func TestTheToolsAreAddedAsASessionStarts(t *testing.T) {
	const url = "https://platform.example/me/session/ada/studio/api/mcp"
	yes, no := true, false
	bobs := bridgeMach{ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"}
	m, f, _ := setupStudio(t)
	// Never asked: nothing runs.
	if has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, url, nil); has || why != "" || len(f.calls) != 0 {
		t.Fatalf("not asked: %v %q, ran %v", has, why, f.calls)
	}
	// Unticked: kept, and nothing runs then or later.
	for _, chose := range []*bool{&no, nil} {
		if has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, url, chose); has || why != "" || len(f.calls) != 0 {
			t.Fatalf("unticked: %v %q, ran %v", has, why, f.calls)
		}
	}
	if o := offersOf(t, m, bobProxy)["m-bob"]; o.State != "declined" || o.Chosen {
		t.Fatalf("unticked: %+v", o)
	}
	// Ticked: added as him, with the rules for the tools that only read.
	if has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, url, &yes); !has || why != "" || f.adds != 1 || len(f.rules) == 0 {
		t.Fatalf("ticked: %v %q, %d adds, rules %v", has, why, f.adds, f.rules)
	}
	for _, rule := range f.rules {
		if !chatReadTools[strings.TrimPrefix(rule, studioToolPrefix)] {
			t.Errorf("allows %s", rule)
		}
	}
	for _, c := range f.calls {
		if c != "pw://bob/bobs-cluster pwt_bobs.key" {
			t.Errorf("ran as %q", c)
		}
	}
	if o := offersOf(t, m, bobProxy)["m-bob"]; o.State != "set" || !o.Chosen {
		t.Fatalf("added: %+v", o)
	}
	// There already: not opened again.
	n := len(f.calls)
	if has, _ := m.bridgeAtStart(setupReq(bobProxy), bobs, url, nil); !has || len(f.calls) != n {
		t.Fatalf("again: %v, %d more runs", has, len(f.calls)-n)
	}
	// For another Studio it is another's entry, which is left as it is.
	if has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, "https://platform.example/me/session/ada/studio2/api/mcp", nil); has || !strings.Contains(why, "does not replace by itself") || f.adds != 1 {
		t.Fatalf("another Studio's entry: %v %q, %d adds", has, why, f.adds)
	}
}

// What keeps the tools from being added does not keep a session from
// starting: it is said, and what will not change by itself is not tried
// again at every start.
func TestAStartWaitsOnceForWhatWillNotChange(t *testing.T) {
	const url = "https://platform.example/me/session/ada/studio/api/mcp"
	yes := true
	bobs := bridgeMach{ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"}
	for what, c := range map[string]struct {
		set  func(*fakeMachine)
		says string
		held bool
	}{
		"another Studio's entry": {func(f *fakeMachine) {
			f.entry = &bridgeEntry{}
			f.entry.Config.Type, f.entry.Config.URL = "http", "https://platform.example/me/session/cy/old/api/mcp"
		}, "pw code there has a studio entry for https://platform.example/me/session/cy/old/api/mcp", true},
		"pw signed in as someone else": {func(f *fakeMachine) { f.who = "ada" }, "signed in as ada", true},
		"out of reach": {func(f *fakeMachine) {
			f.m.bridgeRunFn = func(_ context.Context, site, key string, _ []string, _ time.Duration) (remote.Result, error) {
				f.calls = append(f.calls, site+" "+key)
				return remote.Result{}, errors.New("timed out")
			}
		}, "could not reach bobs-cluster with pw ssh", false},
	} {
		m, f, _ := setupStudio(t)
		c.set(f)
		has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, url, &yes)
		if has || !strings.Contains(why, c.says) || f.adds != 0 {
			t.Fatalf("%s: %v %q, %d adds", what, has, why, f.adds)
		}
		n := len(f.calls)
		if has, why = m.bridgeAtStart(setupReq(bobProxy), bobs, url, nil); has || !strings.Contains(why, c.says) || (len(f.calls) == n) != c.held {
			t.Errorf("%s, again: %v %q, %d more runs", what, has, why, len(f.calls)-n)
		}
		o := offersOf(t, m, bobProxy)["m-bob"]
		if o.State != "offer" || !o.Chosen || (o.Held != "") != c.held {
			t.Errorf("%s: %+v", what, o)
		}
	}
	// Choosing it again tries again.
	m, f, _ := setupStudio(t)
	f.who = "ada"
	m.bridgeAtStart(setupReq(bobProxy), bobs, url, &yes)
	f.who = "bob"
	if code, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`); code != 200 || out["worked"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if has, why := m.bridgeAtStart(setupReq(bobProxy), bobs, url, nil); !has || why != "" {
		t.Errorf("after Add now: %v %q", has, why)
	}
	// Only as the machine's owner, and not where pw ssh does not reach.
	n := len(f.calls)
	for _, mach := range []bridgeMach{{ID: "m-ada", Kind: "cloud", Name: "adas-cluster", Owner: "ada", Status: "connected"}, {ID: "m-laptop", Kind: "local", Name: "bobs-laptop", Owner: "bob", Status: "connected"}, {ID: "m-off", Kind: "cloud", Name: "bobs-old", Owner: "bob", Status: "no_agent"}} {
		if has, _ := m.bridgeAtStart(setupReq(bobProxy), mach, url, &yes); has || len(f.calls) != n {
			t.Errorf("%s: %v, %d runs", mach.ID, has, len(f.calls)-n)
		}
	}
}
