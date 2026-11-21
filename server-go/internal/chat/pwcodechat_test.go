package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// fakePW is a pw code daemon with one session at a time: a message
// appends a scripted turn to the session's events, which the events
// WebSocket sends after ?after=.
type fakePW struct {
	mu       sync.Mutex
	created  []map[string]any
	messages []string
	answers  []string
	events   []string
	seq      int64
	turn     int
	// script is the events of a turn (without seq and turnId).
	script []map[string]any
	// mcp is how the session's studio MCP server stands: "" (a daemon
	// without the route), "ok", "fail" or "stale" (connected, one tool);
	// fix makes a reconnect leave it "ok".
	mcp string
	fix bool
	// calls counts the requests the daemon got.
	calls      int
	reconnects int
	archived   int
	// status and detail add to what /v1/status and GET /v1/sessions/s1
	// answer (a daemon's machine id, a session's history); attached
	// counts POST /v1/sessions/s1/attach.
	status   map[string]any
	detail   map[string]any
	attached int
}

func (f *fakePW) add(ev map[string]any) {
	f.seq++
	ev["seq"] = f.seq
	b, _ := json.Marshal(ev)
	f.events = append(f.events, string(b))
}

func (f *fakePW) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	send := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	switch {
	case p == "/v1/status":
		st := map[string]any{"protocolVersion": pwcode.ProtocolVersion}
		f.mu.Lock()
		for k, v := range f.status {
			st[k] = v
		}
		f.mu.Unlock()
		send(st)
	case p == "/v1/sessions/s1/attach":
		f.mu.Lock()
		f.attached++
		f.mu.Unlock()
		send(map[string]any{"id": "s1", "status": "idle"})
	case p == "/v1/sessions" && r.Method == http.MethodPost:
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.created = append(f.created, req)
		f.mu.Unlock()
		send(map[string]any{"id": "s1", "status": "idle"})
	case p == "/v1/sessions/s1/state":
		send(map[string]any{"remoteUrl": "https://platform.example/code?session=s1", "model": "m1"})
	case p == "/v1/sessions/s1/events":
		websocket.Server{Handler: f.stream}.ServeHTTP(w, r)
	case p == "/v1/sessions/s1/messages":
		var req struct{ Text string }
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.messages = append(f.messages, req.Text)
		f.turn++
		tid := "tn" + strconv.Itoa(f.turn)
		go func(script []map[string]any) {
			time.Sleep(30 * time.Millisecond)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.add(map[string]any{"type": "user_message", "content": req.Text, "turnId": tid})
			for _, ev := range script {
				e := map[string]any{"turnId": tid}
				for k, v := range ev {
					e[k] = v
				}
				f.add(e)
			}
		}(f.script)
		f.mu.Unlock()
		send(map[string]any{"queued": false, "turnId": tid})
	case strings.HasPrefix(p, "/v1/sessions/s1/approvals/"):
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.answers = append(f.answers, strings.TrimPrefix(p, "/v1/sessions/s1/approvals/")+" "+string(b))
		f.mu.Unlock()
		w.WriteHeader(204)
	case p == "/v1/sessions/s1/mcp":
		f.mu.Lock()
		defer f.mu.Unlock()
		switch f.mcp {
		case "":
			w.WriteHeader(404)
		case "ok":
			send([]any{map[string]any{"name": "studio", "connected": true, "transport": "http", "tools": 999}})
		case "stale":
			send([]any{map[string]any{"name": "studio", "connected": true, "transport": "http", "tools": []any{map[string]any{"name": "search_kb"}}}})
		default:
			send([]any{map[string]any{"name": "studio", "transport": "http", "err": "connect: dial tcp 127.0.0.1:1: connect: connection refused"}})
		}
	case p == "/v1/sessions/s1/mcp/studio/reconnect":
		f.mu.Lock()
		f.reconnects++
		if f.fix {
			f.mcp = "ok"
		}
		f.mu.Unlock()
		w.WriteHeader(204)
	case p == "/v1/sessions/s1/archive":
		f.mu.Lock()
		f.archived++
		f.mu.Unlock()
		w.WriteHeader(204)
	case p == "/v1/sessions/s1":
		f.mu.Lock()
		d := map[string]any{"id": "s1", "lastSeq": f.seq, "status": "idle"}
		for k, v := range f.detail {
			d[k] = v
		}
		send(d)
		f.mu.Unlock()
	default:
		w.WriteHeader(404)
	}
}

func (f *fakePW) stream(ws *websocket.Conn) {
	after, _ := strconv.ParseInt(ws.Request().URL.Query().Get("after"), 10, 64)
	sent := 0
	for {
		f.mu.Lock()
		var out []string
		for i := sent; i < len(f.events); i++ {
			var e struct{ Seq int64 }
			json.Unmarshal([]byte(f.events[i]), &e)
			if e.Seq > after {
				out = append(out, f.events[i])
			}
		}
		sent = len(f.events)
		f.mu.Unlock()
		for _, e := range out {
			if websocket.Message.Send(ws, e) != nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pwModule is a test Studio whose pw code is f.
func pwModule(t *testing.T, f *fakePW, env map[string]string) (*Module, string) {
	sock := pwcodetest.Socket(t, f)
	e := map[string]string{"PW_CODE_SOCKET": sock}
	for k, v := range env {
		e[k] = v
	}
	m, srv, _, _ := newTestModule(t, e)
	// The Studio runs under ACTIVATE: the platform is connected.
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return true }
	daemon := pwcode.New(m.pf)
	was := pwSites
	pwSites = func() *agentsites.Sites { return &agentsites.Sites{P: m.pf, Daemon: daemon} }
	t.Cleanup(func() { pwSites = was })
	return m, srv.URL
}

var helloTurn = []map[string]any{
	{"type": "reasoning"},
	{"type": "tool_start", "name": "mcp__studio__search_kb", "args": `{"query":"budget"}`, "callId": "c1"},
	{"type": "tool_end", "name": "mcp__studio__search_kb", "result": "notes/budget.md\nmore", "callId": "c1"},
	{"type": "chunk", "content": "Hel"},
	{"type": "chunk", "content": "lo"},
	{"type": "done", "content": "Hello", "turn": map[string]any{"model": "m1"}},
}

func TestPWCodeAnswersByDefaultWhereItRuns(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	for _, want := range []string{
		`event: pwcode` + "\n" + `data: {"session":"s1","url":"https://platform.example/code?session=s1"`,
		`"phase":"call","id":"c1","name":"search_kb"`,
		`"phase":"result","id":"c1","name":"search_kb","summary":"notes/budget.md","error":false`,
		"event: content\ndata: {\"text\":\"Hel\"}", "event: content\ndata: {\"text\":\"lo\"}",
		`event: done` + "\n" + `data: {"content":"Hello","finishReason":"stop","model":"pw code · m1"`,
	} {
		if !strings.Contains(sse, want) {
			t.Errorf("missing %q in\n%s", want, sse)
		}
	}
	if len(f.created) != 1 {
		t.Fatalf("sessions made: %d", len(f.created))
	}
	req := f.created[0]
	if req["workspace"] != m.A.Cfg.KBRoot || req["remoteControl"] != true || req["agent"] != "studio" {
		t.Errorf("session request: %v", req)
	}
	agents, _ := req["agentsJson"].(string)
	if !strings.Contains(agents, `"url":"http://127.0.0.1:`+strconv.Itoa(m.A.Cfg.Port)+`/api/mcp"`) || !strings.Contains(agents, `"Authorization":"Bearer `+chatTokenPrefix) {
		t.Errorf("agents: %s", agents)
	}
	if tools, _ := req["allowedTools"].([]any); len(tools) == 0 || tools[0] != "mcp__studio__search_kb" {
		t.Errorf("allowed tools: %v", req["allowedTools"])
	}
	if f.messages[0] != "What is the budget?" {
		t.Errorf("sent: %q", f.messages[0])
	}
	got := m.Conv.Get(c.ID)
	last := got.Messages[len(got.Messages)-1]
	if last.Role != "assistant" || *last.Content != "Hello" || len(last.Parts) != 1 || last.Parts[0].Status != "ok" || *last.Model != "pw code · m1" {
		t.Errorf("recorded: %+v", last)
	}
	if got.PWCode == nil || got.PWCode.Session != "s1" || got.PWCode.LastMessage != last.ID || got.PWCode.URL == "" {
		t.Errorf("link: %+v", got.PWCode)
	}

	// The next message goes to the same session, which has seen it all.
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2",
		"messages": []any{map[string]any{"role": "user", "content": "And last year?"}}})
	if len(f.created) != 1 || len(f.messages) != 2 || f.messages[1] != "And last year?" {
		t.Errorf("second turn: %d sessions, sent %q", len(f.created), f.messages)
	}
}

func TestPWCodeSessionHearsWhatItMissed(t *testing.T) {
	c := &conversations.Conversation{Messages: []conversations.Message{
		{ID: "u1", Role: "user", Content: sp("first question")},
		{ID: "a1", Role: "assistant", Content: sp("pw code's answer")},
		{ID: "u2", Role: "user", Content: sp("second question")},
		{ID: "a2", Role: "assistant", Content: sp("the Studio assistant's answer")},
		{ID: "u3", Role: "user", Content: sp("third")},
	}, PWCode: &conversations.PWCodeLink{LastMessage: "a1"}}
	got := pwcodeContext(c, false, "u3")
	if !strings.Contains(got, "The person: second question") || !strings.Contains(got, "The Studio assistant: the Studio assistant's answer") || strings.Contains(got, "first question") || strings.Contains(got, "third") {
		t.Errorf("%q", got)
	}
	if all := pwcodeContext(c, true, "u3"); !strings.Contains(all, "first question") {
		t.Errorf("a new session hears everything: %q", all)
	}
	c.PWCode.LastMessage = "a2"
	if none := pwcodeContext(c, false, "u3"); none != "" {
		t.Errorf("nothing missed: %q", none)
	}
}

func TestPWCodeApprovalReachesTheChatAndIsAnswered(t *testing.T) {
	f := &fakePW{script: []map[string]any{
		{"type": "tool_start", "name": "Bash", "args": `{"command":"date"}`, "callId": "c1"},
		{"type": "approval_request", "approval": map[string]any{"id": "ap-1", "kind": "confirm", "tool": "Bash", "command": "date"}},
		{"type": "approval_resolved", "approvalId": "ap-1"},
		{"type": "tool_end", "name": "Bash", "result": "denied", "callId": "c1"},
		{"type": "approval_request", "approval": map[string]any{"id": "ap-2", "kind": "confirm", "tool": "mcp__studio__write_kb_file", "command": `mcp__studio__write_kb_file {"path":"a.pdf"}`}},
		{"type": "done", "content": "Not run."},
	}}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "run date"}}})
	if !strings.Contains(sse, "event: pwcode_approval\ndata: {\"conversationId\":\""+c.ID+"\",\"session\":\"s1\"") || !strings.Contains(sse, `"command":"date"`) || !strings.Contains(sse, "event: pwcode_approval_resolved") ||
		!strings.Contains(sse, `"tool":"write_kb_file"`) || !strings.Contains(sse, `"command":"write_kb_file {\"path\":\"a.pdf\"}"`) {
		t.Errorf("%s", sse)
	}
	if st := get(t, base+"/api/chat/pwcode/"+c.ID); st["session"] != "s1" || st["status"] != "idle" || st["answering"] != false || st["url"] == "" {
		t.Errorf("status: %v", st)
	}
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+c.ID+"/approvals/ap-1", `{"allowed":false}`)
	if code != 200 || out["ok"] != true || len(f.answers) != 1 || !strings.Contains(f.answers[0], `"allowed":false`) || !strings.Contains(f.answers[0], "Denied from the Studio.") {
		t.Errorf("%d %v %v", code, out, f.answers)
	}
	plain := m.Conv.Create(nil, "")
	if st := get(t, base+"/api/chat/pwcode/"+plain.ID); st["session"] != nil || st["status"] != "idle" {
		t.Errorf("a chat without pw code: %v", st)
	}
	if code, _, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+c.ID+"/approvals/..%2f", `{}`); code == 200 {
		t.Error("a bad approval id was sent on")
	}
}

func TestStudioAnswersWhenChosenOrWherePWCodeIsNot(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": c.ID, "engine": "studio", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if len(f.created) != 0 {
		t.Error("pw code answered a message sent to the Studio assistant")
	}
	if out := get(t, base+"/api/chat/engine"); out["default"] != "pwcode" || out["pwcode"] != true {
		t.Errorf("engine: %v", out)
	}

	// No ACTIVATE credential: auto falls back, a choice of pw code says why.
	m2, base2 := pwModule(t, &fakePW{}, map[string]string{"PW_API_KEY": ""})
	m2.setAccess(&Access{GatewayBase: "http://127.0.0.1:9/v1"})
	if out := get(t, base2+"/api/chat/engine"); out["default"] != "studio" || out["pwcode"] != false || !strings.Contains(out["reason"].(string), "pw code needs ACTIVATE") {
		t.Errorf("engine: %v", out)
	}
	sse := post(t, base2, map[string]any{"engine": "pwcode", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if !strings.Contains(sse, `"message":"pw code: pw code needs ACTIVATE`) {
		t.Errorf("%s", sse)
	}
}

func TestChatTokenActsForItsPersonOnly(t *testing.T) {
	m, base := pwModule(t, &fakePW{}, map[string]string{"MCP_ENABLED": "0"})
	tok, err := m.issueChatToken(nil, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if ct, ok := m.openChatToken(tok); !ok || ct.Conv != "c1" {
		t.Fatal("round trip")
	}
	if _, ok := m.openChatToken(tok[:len(tok)-4] + "AAAA"); ok {
		t.Error("a changed token opened")
	}
	if _, ok := m.openChatToken("studio-chat.v1:x:y:z"); ok {
		t.Error("a forged token opened")
	}
	call := func(bearer string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, base+"/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	// A chat token reaches the library tools though MCP access for other
	// clients is off.
	if code, out := call(tok); code != 200 || out["result"] == nil {
		t.Errorf("chat token: %d %v", code, out)
	}
	if code, _ := call("studio-chat.v1:AAAA:BBBB:CCCC"); code != 401 {
		t.Errorf("a bad chat token: %d", code)
	}
}
