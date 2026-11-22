package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

const adaSession = "20261122-100000-adaada"

// localOwners is a Studio with sign-in on and AGENT_LOCAL_SITE=1 whose
// host has Ada's session and Cy's, both about a report; Bob owns none.
// It answers the module, Bob's request, and the daemon calls beyond the
// session list.
func localOwners(t *testing.T) (*Module, *http.Request, func() []string) {
	var mu sync.Mutex
	var touched []string
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if !(r.URL.Path == "/v1/sessions" && r.Method == "GET") && r.URL.Path != "/v1/status" {
			mu.Lock()
			touched = append(touched, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		switch {
		case r.URL.Path == "/v1/status":
			io.WriteString(w, `{"protocolVersion":2,"hostname":"here"}`)
		case r.URL.Path == "/v1/sessions" && r.Method == "GET":
			io.WriteString(w, `[{"id":"`+adaSession+`","name":"weekly report","status":"idle"},{"id":"20261122-100001-cycycy","name":"daily report","status":"idle"}]`)
		default:
			io.WriteString(w, `{"id":"x","status":"idle","history":[]}`)
		}
	}))
	m, _, _, _ := newTestModule(t, map[string]string{"PW_CODE_SOCKET": sock, "AGENT_LOCAL_SITE": "1"})
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	s := &agentsites.Sites{P: m.pf, Daemon: pwcode.New(m.pf), Owned: func(r *http.Request) map[string]bool {
		if u := auth.UserOf(r); u != nil && u.ID == "ada" {
			return map[string]bool{adaSession: true}
		}
		return map[string]bool{}
	}}
	was := agentsites.Current()
	agentsites.SetCurrent(s)
	t.Cleanup(func() { agentsites.SetCurrent(was) })
	r := auth.WithUser(httptest.NewRequest("POST", "/api/chat", nil), &auth.User{ID: "bob"})
	return m, r, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), touched...) }
}

// Review 2, R4: each of the chat's agent tools keeps to the person's own
// sessions on this host: another's is not listed, read, steered, given a
// goal, answered or stopped, and nothing is started here directly.
func TestTheChatsAgentToolsKeepToTheirOwnersSessions(t *testing.T) {
	m, bob, touched := localOwners(t)
	out, err := goExecutors["agent_sessions"](m, bob, map[string]any{"site": "local"}, ToolCtx{})
	if err != nil || strings.Contains(out.Result, "adaada") {
		t.Fatalf("bob lists ada's: %v %+v", err, out)
	}
	for _, name := range []string{"agent_status", "agent_steer", "agent_goal", "agent_approve", "agent_stop"} {
		args := map[string]any{"site": "local", "session": adaSession, "text": "read her notes", "action": "clear", "approval": "ap1", "allow": true}
		if out, err := goExecutors[name](m, bob, args, ToolCtx{}); err == nil {
			t.Errorf("%s on ada's session: %+v", name, out)
		} else if !strings.Contains(err.Error(), "not one of yours") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if out, err := goExecutors["start_agent"](m, bob, map[string]any{"site": "local", "objective": "x"}, ToolCtx{}); err == nil || !strings.Contains(err.Error(), "delegating") {
		t.Errorf("a direct start here: %v %+v", err, out)
	}
	if got := touched(); len(got) != 0 {
		t.Fatalf("bob's calls reached the daemon: %v", got)
	}
}

// Review 2, R1: a session given by name is looked up among the person's
// own on this host: another's is neither listed back nor matched, so its
// name and state stay theirs.
func TestASessionByNameIsLookedUpAmongTheirOwn(t *testing.T) {
	m, bob, touched := localOwners(t)
	out, err := goExecutors["agent_status"](m, bob, map[string]any{"site": "local", "session": "report"}, ToolCtx{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Result, "adaada") || strings.Contains(out.Result, "weekly report") || strings.Contains(out.Result, "cycycy") || !strings.Contains(out.Result, "No session on that site matches") {
		t.Fatalf("bob by name: %s", out.Result)
	}
	if got := touched(); len(got) != 0 {
		t.Fatalf("bob's call reached the daemon: %v", got)
	}
	ada := auth.WithUser(httptest.NewRequest("POST", "/api/chat", nil), &auth.User{ID: "ada"})
	out, err = goExecutors["agent_status"](m, ada, map[string]any{"site": "local", "session": "report"}, ToolCtx{})
	if err != nil || strings.Contains(out.Result, "More than one") {
		t.Fatalf("ada by name: %v %+v", err, out)
	}
}
