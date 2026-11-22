package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A call an agent's session waits to have approved shows in its detail
// and is answered from the Fleet page, by its owner only, once; on a
// Studio with sign-in the Code page does not list this computer's
// sessions, so this is where it can be answered.
func TestApproveFromTheFleetPage(t *testing.T) {
	f, d, _ := stand(t)
	f.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	ada, bob := &auth.User{ID: "ada", Username: "ada"}, &auth.User{ID: "bob", Username: "bob"}
	a, err := f.Create(Input{Name: "Watcher", Goal: "Watch.", Every: "manual"}, ada)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tick(a.ID, "now", true) {
		t.Fatal("no wake-up")
	}
	waitFor(t, "the session", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.agents[a.ID].Session != "" && !f.busy[a.ID] })
	sess := f.Get(a.ID).Session
	d.mu.Lock()
	d.approval[sess], d.status[sess] = "rm -rf build", "awaiting_approval"
	d.mu.Unlock()

	mux := http.NewServeMux()
	Register(mux, f.A, platform.New(platform.Options{Home: t.TempDir(), LookupEnv: func(string) (string, bool) { return "", false }}), f)
	do := func(u *auth.User, method, path, body string) (int, string) {
		req := auth.WithUser(httptest.NewRequest(method, path, strings.NewReader(body)), u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := do(ada, http.MethodGet, "/api/fleet/"+a.ID, "")
	var got struct {
		Agent    Agent
		Approval *Approval
	}
	json.Unmarshal([]byte(body), &got)
	if code != 200 || got.Approval == nil || got.Approval.ID != "ap1" || got.Approval.Kind != "confirm" || got.Approval.Tool != "Bash" ||
		got.Approval.Command != "rm -rf build" || !got.Approval.Approvable || got.Agent.State != "input-required" {
		t.Fatalf("detail: %d %s", code, body)
	}
	// The note names the call, from the daemon's fields.
	if !strings.Contains(got.Agent.Note, "Bash: rm -rf build") {
		t.Fatalf("note %q", got.Agent.Note)
	}
	if code, _ := do(bob, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":true}`); code != 404 || len(d.answers) != 0 {
		t.Fatalf("another person answered: %d", code)
	}
	if code, _ := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"../x","allowed":true}`); code != 400 {
		t.Fatalf("a bad id: %d", code)
	}
	if code, body := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":false}`); code != 200 {
		t.Fatalf("approve: %d %s", code, body)
	}
	if len(d.answers) != 1 || d.answers[0]["allowed"] != false || d.answers[0]["denyMessage"] != DenyNote {
		t.Fatalf("answers %v", d.answers)
	}
	if s := f.state(a.ID); s != "working" {
		t.Fatalf("after the answer: %s", s)
	}
	// A second answer finds nothing waiting.
	if code, body := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":true}`); code != 409 || !strings.Contains(body, "no longer waiting") {
		t.Fatalf("second answer: %d %s", code, body)
	}

	// A plan or a question is not approved here, and nothing is sent.
	d.mu.Lock()
	d.approval[sess], d.approvalKind[sess], d.status[sess] = "Plan: rewrite the build", "plan", "awaiting_approval"
	d.mu.Unlock()
	_, body = do(ada, http.MethodGet, "/api/fleet/"+a.ID, "")
	got.Approval = nil
	json.Unmarshal([]byte(body), &got)
	if got.Approval == nil || got.Approval.Kind != "plan" || got.Approval.Approvable {
		t.Fatalf("a plan: %s", body)
	}
	for _, allowed := range []string{"true", "false"} {
		if code, _ := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":`+allowed+`}`); code != 409 {
			t.Fatalf("a plan answered %s: %d", allowed, code)
		}
	}
	// A command too long to show whole can be denied, not approved.
	d.mu.Lock()
	d.approval[sess], d.approvalKind[sess] = strings.Repeat("y", maxApprovalCommand+10), "confirm"
	d.mu.Unlock()
	_, body = do(ada, http.MethodGet, "/api/fleet/"+a.ID, "")
	json.Unmarshal([]byte(body), &got)
	if !got.Approval.CommandCut || got.Approval.CommandLength != maxApprovalCommand+10 || got.Approval.Approvable {
		t.Fatalf("a long command: cut %v length %d approvable %v", got.Approval.CommandCut, got.Approval.CommandLength, got.Approval.Approvable)
	}
	if code, _ := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":true}`); code != 409 {
		t.Fatalf("a long command approved: %d", code)
	}
	n := len(d.answers)
	if code, _ := do(ada, http.MethodPost, "/api/fleet/"+a.ID+"/approve", `{"approval":"ap1","allowed":false}`); code != 200 || len(d.answers) != n+1 {
		t.Fatalf("a long command denied: %d", code)
	}
}
