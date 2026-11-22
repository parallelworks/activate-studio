package chat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// relayModule is a test Studio whose own pw code is local (daemon d1, on
// machine m1) and whose viewer also has machine m2, other-box, which the
// Studio reaches only through the stand-in platform relay to remote.
func relayModule(t *testing.T, local, remote *fakePW) (*Module, string, *fakeCodePlatform, string) {
	m, base, plat, origin := continueModuleAt(t, local, "d1")
	plat.mu.Lock()
	plat.others = []map[string]any{{"id": "m2", "name": "other-box", "kind": "existing", "status": "connected", "daemonId": "d2", "hostname": "other-box.example"}}
	plat.sessions = append(plat.sessions, map[string]any{"machineId": "m2", "id": "s1", "autoName": "Tune the solver", "workspace": "/home/me/solver", "status": "idle", "messages": 2})
	plat.relay = map[string]http.Handler{"m2": remote}
	plat.mu.Unlock()
	return m, base, plat, origin
}

func TestContinueASessionOnAnotherMachineThroughTheRelay(t *testing.T) {
	local := &fakePW{script: helloTurn}
	remote := &fakePW{script: helloTurn, detail: map[string]any{"history": earlierTurns, "model": "m9", "workspace": "/home/me/solver"}}
	m, base, plat, origin := relayModule(t, local, remote)

	if out := get(t, base+"/api/chat/pwcode/here"); out["available"] != true || out["relay"] != true || out["relayReason"] != nil {
		t.Errorf("here: %v", out)
	}
	// The question says where it runs and that it stays there.
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1"}`)
	ask, _ := out["confirm"].(string)
	if code != 200 || out["relay"] != true || !strings.Contains(ask, "runs on other-box, in …/me/solver, and stays there") || !strings.Contains(ask, "its commands run on other-box") || !strings.Contains(ask, "not the Studio's library tools") {
		t.Fatalf("ask: %d %v", code, out)
	}
	code, out, _ = send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if code != 200 || id == "" {
		t.Fatalf("continue: %d %v", code, out)
	}
	// Nothing is attached or moved, on either machine.
	if remote.attached != 0 || local.attached != 0 || len(local.created) != 0 || len(remote.created) != 0 {
		t.Errorf("attached %d/%d, made %d/%d", local.attached, remote.attached, len(local.created), len(remote.created))
	}
	c := m.Conv.Get(id)
	if c == nil || c.PWCode == nil || !c.PWCode.Relay || c.PWCode.Site != pwcode.RelaySite("m2") || c.PWCode.Machine != "m2" || c.PWCode.MachineName != "other-box" || c.PWCode.Workspace != "/home/me/solver" {
		t.Fatalf("link: %+v", c.PWCode)
	}
	if note := *c.Messages[0].Content; !strings.Contains(note, "keeps running on other-box, where its commands run") || !strings.Contains(note, "its own tools") || len(c.Messages) != 3 {
		t.Errorf("note: %q (%d messages)", note, len(c.Messages))
	}

	// A message goes to the session there, and its reply comes back over
	// the relay's events.
	sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u9", "parentMessageId": c.Messages[2].ID,
		"messages": []any{map[string]any{"role": "user", "content": "try a finer mesh"}}})
	if !strings.Contains(sse, `"content":"Hello"`) || len(remote.messages) != 1 || !strings.HasSuffix(remote.messages[0], "try a finer mesh") || len(local.messages) != 0 {
		t.Errorf("message: remote %q, local %q\n%s", remote.messages, local.messages, sse)
	}
	if strings.Contains(remote.messages[0], "Scope from the Studio's chat bar") {
		t.Errorf("a session without the Studio's tools is told of their scope: %q", remote.messages[0])
	}

	// Its state, Stop, and answers go there too.
	if st := get(t, base+"/api/chat/pwcode/"+id); st["relay"] != true || st["machine"] != "other-box" || st["continued"] != true {
		t.Errorf("status: %v", st)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/interrupt", `{}`); code != 200 || remote.interrupts != 1 || local.interrupts != 0 {
		t.Errorf("stop: %d, remote %d local %d", code, remote.interrupts, local.interrupts)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap1", `{"allowed":true}`); code != 200 {
		t.Errorf("approve: %d", code)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap2", `{"kind":"ask","answers":[{"question":"Which mesh?","picked":["Fine"]},{"question":"Why?","text":"accuracy"}]}`); code != 200 {
		t.Errorf("ask: %d", code)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap3", `{"kind":"plan","allowed":false,"message":"split step 2"}`); code != 200 {
		t.Errorf("plan: %d", code)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap4", `{"kind":"ask","answers":[{"question":"Which mesh?"}]}`); code != 400 {
		t.Errorf("an unanswered question: %d", code)
	}
	want := []string{
		`ap1 {"allowed":true}`,
		`ap2 {"answers":[{"question":"Which mesh?","picked":["Fine"]},{"question":"Why?","picked":[],"text":"accuracy"}]}`,
		`ap3 {"allowed":false,"feedback":"split step 2"}`,
	}
	if strings.Join(remote.answers, "\n") != strings.Join(want, "\n") || len(local.answers) != 0 {
		t.Errorf("answers:\n%s\nwant\n%s", strings.Join(remote.answers, "\n"), strings.Join(want, "\n"))
	}

	// Every relayed request carried the viewer's own credential, and the
	// events handshake the platform's own origin.
	plat.mu.Lock()
	defer plat.mu.Unlock()
	if len(plat.relayed) == 0 || len(plat.origins) == 0 {
		t.Fatalf("relayed %v", plat.relayed)
	}
	for _, r := range plat.relayed {
		if !strings.HasSuffix(r, " Bearer k") || !strings.Contains(r, "/machines/m2/sessions/s1") {
			t.Errorf("relayed: %s", r)
		}
	}
	for _, o := range plat.origins {
		if o != strings.ToLower(origin) {
			t.Errorf("origin %q, want %q", o, origin)
		}
	}
}

func TestARelayedChatReadsTheSessionsStateThere(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{script: helloTurn}
	_, base, _, _ := relayModule(t, local, remote)
	// Archived: what still works here, and where to restore it.
	remote.detail = map[string]any{"archivedAt": "2026-11-20T00:00:00Z"}
	if code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1"}`); code != 409 || !strings.Contains(out["error"].(string), "still read it on the Code page") {
		t.Errorf("archived: %d %v", code, out)
	}
	remote.detail = nil
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if id == "" {
		t.Fatalf("continue: %v", out)
	}
	// Archived since: the next message says so and sends nothing.
	remote.detail = map[string]any{"archivedAt": "2026-11-21T00:00:00Z"}
	if sse := post(t, base, map[string]any{"conversationId": id, "userMessageId": "u1", "messages": []any{map[string]any{"role": "user", "content": "go on"}}}); !strings.Contains(sse, "was archived") || len(remote.messages) != 0 {
		t.Errorf("archived since: %s", sse)
	}
}

func TestTheRelayIsNeverTheStudiosAccount(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, plat, _ := relayModule(t, local, remote)
	// With sign-in on, a person without an ACTIVATE sign-in of their own
	// gets no relay, though the Studio has an account (PW_API_KEY k).
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	bob := auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), &auth.User{ID: "bob", Username: "bob"})
	if c, err := m.relayClient(bob, "m2"); err == nil || c != nil {
		t.Fatalf("relay as the Studio's account: %v %v", c, err)
	}
	if why := m.relayUnavailable(bob); !strings.Contains(why, "own ACTIVATE sign-in") {
		t.Errorf("why: %q", why)
	}
	if plat.reads != 0 || len(plat.relayed) != 0 {
		t.Errorf("the platform was asked: %d reads, %v", plat.reads, plat.relayed)
	}
}

func TestARelayedChatIsOnlyItsOwners(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, plat, _ := relayModule(t, local, remote)
	title := "Tune the solver"
	id := m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: pwcode.RelaySite("m2"), Relay: true, Session: "s1",
		Continued: true, Machine: "m2", MachineName: "other-box", CreatedAt: m.Conv.Now()}).ID
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	plat.mu.Lock()
	plat.relayed = nil
	plat.mu.Unlock()
	bob := auth.WithUser(httptest.NewRequest(http.MethodPost, "/", nil), &auth.User{ID: "bob", Username: "bob"})
	bob.SetPathValue("id", id)
	_, _, err := m.ownPWCode(bob)
	var ae *apierr.Error
	// 404 where they cannot see the chat, 403 where it is shared with them.
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound && ae.Status != http.StatusForbidden {
		t.Errorf("another person: %v", err)
	}
	if len(plat.relayed) != 0 || remote.calls != 0 {
		t.Errorf("the session was reached for them: %v, %d calls", plat.relayed, remote.calls)
	}
}

func TestAStoppedTurnSaysStoppedNotFailed(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, _, _ := relayModule(t, local, remote)
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	c := m.Conv.Get(id)
	if c == nil {
		t.Fatalf("continue: %v", out)
	}
	rec := httptest.NewRecorder()
	turn := &pwTurn{m: m, res: &sseWriter{w: rec, f: rec}, convID: id, link: c.PWCode, done: make(chan struct{})}
	m.follow(turn)
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/interrupt", `{}`); code != 200 || remote.interrupts != 1 {
		t.Fatalf("stop: %d %d", code, remote.interrupts)
	}
	// The daemon ends a stopped turn with an error of its own.
	if !turn.handle(jsonv.NewObject("type", "error", "content", "context canceled")) {
		t.Fatal("the error did not end the turn")
	}
	turn.finish()
	m.unfollow(turn)
	last := m.Conv.Get(id).Messages
	if got := *last[len(last)-1].Content; got != "Stopped." || strings.Contains(rec.Body.String(), "did not finish") {
		t.Errorf("reply %q\n%s", got, rec.Body.String())
	}
}
