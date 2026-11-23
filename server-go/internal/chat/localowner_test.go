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
		mu.Lock()
		touched = append(touched, r.Method+" "+r.URL.Path)
		mu.Unlock()
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

// With sign-in on, the chat's agent tools offer no session on this host,
// even with AGENT_LOCAL_SITE=1: its daemon runs as the Studio's account.
// Nothing is listed, read, steered, given a goal, answered, stopped or
// started there, by anyone, and nothing reaches the daemon.
func TestWithSignInOnTheChatsAgentToolsHaveNoLocalSite(t *testing.T) {
	m, bob, touched := localOwners(t)
	ada := auth.WithUser(httptest.NewRequest("POST", "/api/chat", nil), &auth.User{ID: "ada"})
	if m.localAgents() {
		t.Fatal("the local site is offered")
	}
	for _, r := range []*http.Request{ada, bob} {
		for _, name := range []string{"agent_sessions", "agent_status", "agent_steer", "agent_goal", "agent_approve", "agent_stop", "start_agent"} {
			args := map[string]any{"site": "local", "session": adaSession, "text": "read her notes", "action": "clear", "approval": "ap1", "allow": true, "objective": "x"}
			out, err := goExecutors[name](m, r, args, ToolCtx{})
			if err == nil && strings.Contains(out.Result, "adaada") {
				t.Errorf("%s: %+v", name, out)
			}
		}
	}
	if got := touched(); len(got) != 0 {
		t.Fatalf("calls reached the daemon: %v", got)
	}
}
