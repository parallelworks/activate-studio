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
	// The machine refuses a remote answer's mode above its ceiling,
	// accept-edits by default.
	send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap5", `{"kind":"plan","allowed":true}`)
	send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap6", `{"kind":"plan","allowed":true,"mode":"auto"}`)
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap4", `{"kind":"ask","answers":[{"question":"Which mesh?"}]}`); code != 400 {
		t.Errorf("an unanswered question: %d", code)
	}
	want := []string{
		`ap1 {"allowed":true}`,
		`ap2 {"answers":[{"question":"Which mesh?","picked":["Fine"]},{"question":"Why?","picked":[],"text":"accuracy"}]}`,
		`ap3 {"allowed":false,"feedback":"split step 2"}`,
		`ap5 {"allowed":true,"mode":"accept-edits"}`,
		`ap6 {"allowed":true,"mode":"accept-edits"}`,
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
	m.A.Cfg.FrontAuth = true
	shared := true
	m.Convs.SharedHistory = func() bool { return shared }
	m.Convs.AuthEnabled = func() bool { return true }
	// A pw code chat on this machine, shared as chats are.
	direct := m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()}).ID

	var ae *apierr.Error
	direct2 := func(chat string, want int) {
		t.Helper()
		bob := auth.WithUser(httptest.NewRequest(http.MethodPost, "/", nil), &auth.User{ID: "bob", Username: "bob"})
		bob.SetPathValue("id", chat)
		if _, _, err := m.ownPWCode(bob); !errors.As(err, &ae) || ae.Status != want {
			t.Errorf("bob directs %s: want %d, got %v", chat, want, err)
		}
	}
	// Nor can they send it a message, a new one or one while ada's turn
	// runs (which the session takes as direction): it is not stored in her
	// chat and does not reach her session.
	message := func(chat string, want int) {
		t.Helper()
		body := `{"conversationId":"` + chat + `","userMessageId":"u-bob","engine":"pwcode","messages":[{"role":"user","content":"rm -rf results"}]}`
		r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body)), &auth.User{ID: "bob", Username: "bob"})
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if err := m.stream(rec, r); !errors.As(err, &ae) || ae.Status != want {
			t.Errorf("bob's message to %s: want %d, got %v (%d %s)", chat, want, err, rec.Code, rec.Body.String())
		}
	}
	// A relayed chat holds a session's turns from ada's own machine: it is
	// hers alone, shared history or not (404). A shared chat on this
	// machine is seen but not directed (403).
	for _, on := range []bool{true, false} {
		shared = on
		direct2(id, http.StatusNotFound)
		message(id, http.StatusNotFound)
		if _, err := m.Convs.FindVisible(id, "bob"); err == nil {
			t.Errorf("bob sees the relayed chat (shared %v)", on)
		}
		if _, err := m.Convs.FindVisible(id, "ada"); err != nil {
			t.Errorf("ada does not see her relayed chat: %v", err)
		}
	}
	shared = true
	direct2(direct, http.StatusForbidden)
	message(direct, http.StatusForbidden)
	turn := &pwTurn{m: m, res: &sseWriter{w: httptest.NewRecorder()}, convID: id, done: make(chan struct{})}
	m.follow(turn)
	message(id, http.StatusNotFound)
	m.unfollow(turn)
	if n := len(m.Conv.Get(id).Messages) + len(m.Conv.Get(direct).Messages); n != 0 || len(plat.relayed) != 0 || remote.calls != 0 || len(remote.messages) != 0 || len(local.messages) != 0 {
		t.Errorf("bob reached ada's chats or session: %d messages, %v, %d calls", n, plat.relayed, remote.calls)
	}
	// The question before continuing says so, where people sign in.
	if q := relayConfirm("other-box", "", "idle", "", true, false); !strings.Contains(q, "Only you can see the chat in this Studio, even where chats are shared") {
		t.Errorf("confirm: %s", q)
	}
	if q := relayConfirm("other-box", "", "idle", "", false, false); strings.Contains(q, "Only you") {
		t.Errorf("confirm without sign-in: %s", q)
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
	// The daemon ends a stopped turn with an error of its own, which can
	// reach the chat before the interrupt is answered.
	remote.onInterrupt = func() {
		if !turn.handle(jsonv.NewObject("type", "error", "content", "context canceled")) {
			t.Error("the error did not end the turn")
		}
		turn.finish()
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/interrupt", `{}`); code != 200 || remote.interrupts != 1 {
		t.Fatalf("stop: %d %d", code, remote.interrupts)
	}
	m.unfollow(turn)
	last := m.Conv.Get(id).Messages
	if got := *last[len(last)-1].Content; got != "Stopped." || strings.Contains(rec.Body.String(), "did not finish") {
		t.Errorf("reply %q\n%s", got, rec.Body.String())
	}
}

func TestAFailedStopLeavesTheTurnAsItWas(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{refuseInterrupt: true}
	m, base, _, _ := relayModule(t, local, remote)
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	turn := &pwTurn{m: m, res: &sseWriter{w: httptest.NewRecorder()}, convID: id, done: make(chan struct{})}
	m.follow(turn)
	defer m.unfollow(turn)
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/interrupt", `{}`); code == 200 {
		t.Fatal("a refused stop answered ok")
	}
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if turn.stopped {
		t.Error("a stop that did not happen marked the turn stopped")
	}
}
