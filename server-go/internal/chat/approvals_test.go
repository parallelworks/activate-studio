package chat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

type stubPlatform struct{ origin string }

func (s stubPlatform) GatewayBase() string           { return s.origin + "/api/openai/v1" }
func (s stubPlatform) Origin() string                { return s.origin }
func (s stubPlatform) PlatformGateway() bool         { return s.origin != "" }
func (s stubPlatform) CredentialContextName() string { return "" }
func (s stubPlatform) CLI() string                   { return "/bin/false" }

// TestHeldCallsAskThenRunOnce holds a stop_cluster call, refuses the
// answers that may not run it, and runs the alternative the user chose
// once, against a fake platform.
func TestHeldCallsAskThenRunOnce(t *testing.T) {
	var calls []string
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/api/auth/session":
			io.WriteString(w, `{"username":"alice","organization":"acme"}`)
		case strings.HasSuffix(r.URL.Path, "/destroy"):
			w.WriteHeader(409)
			io.WriteString(w, `{"message":"cluster c1 is busy"}`)
		default:
			w.WriteHeader(204)
		}
	}))
	defer plat.Close()
	accounts.mu.Lock()
	accounts.m = map[string]struct {
		at time.Time
		v  *Account
	}{}
	accounts.mu.Unlock()

	dir := t.TempDir()
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	m := New(a)
	m.Plat = stubPlatform{plat.URL}
	tools := goTools{m: m}
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	key := "own-key"

	if c := m.changesAvailable(m.viewerOf(r), &key, nil, nil); !c.OK {
		t.Fatalf("changes %+v", c)
	}
	if c := m.changesAvailable(m.viewerOf(r), nil, nil, nil); c.OK || c.Why != "no platform credential is configured" {
		t.Errorf("no key: %+v", c)
	}
	prep, _ := tools.Prepare(r, "stop_cluster", `{"clusterName":"c1"}`, key)
	if prep.Problem != "" || prep.Canonical != `{"clusterName":"c1","user":"alice"}` {
		t.Fatalf("prepare %+v", prep)
	}
	content := "stopping"
	turn := []llm.Message{{Role: "assistant", Content: &content}}
	approval, err := tools.Hold(r, map[string]any{
		"conversationId": "conv-1", "messageId": "srv-1", "toolCallId": "call_1", "tool": "stop_cluster",
		"canonical": prep.Canonical, "args": prep.Args, "turn": turn, "groups": []string{"changes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var part map[string]any
	json.Unmarshal(approval, &part)
	if part["header"] != "Stop cluster c1?" || part["command"] != `stop_cluster(user: "alice", clusterName: "c1")` {
		t.Errorf("approval %s", approval)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "held-tool-calls.json"))
	if !strings.HasPrefix(string(b), `[{"conversationId":"conv-1","messageId":"srv-1","toolCallId":"call_1","tool":"stop_cluster","args":"{\"clusterName\":\"c1\",\"user\":\"alice\"}","viewer":"local"`) {
		t.Errorf("held file %s", b)
	}
	if hs, _ := tools.Held(r, "conv-1"); len(hs) != 1 || hs[0].Viewer != "local" {
		t.Errorf("held %+v", hs)
	}
	answer := func(o map[string]any) *Answered {
		base := map[string]any{"conversationId": "conv-1", "messageId": "srv-1", "toolCallId": "call_1", "allowed": true}
		for k, v := range o {
			base[k] = v
		}
		got, _ := tools.Answer(r, base, &key)
		return got
	}
	if got := answer(map[string]any{"messageId": "srv-2"}); got.OK || got.Why != "The answer does not belong to this request; nothing ran." {
		t.Errorf("wrong message: %+v", got)
	}
	if got := answer(map[string]any{"args": `{"clusterName":"c2"}`}); got.OK || !strings.HasPrefix(got.Why, "The approval was for other arguments") {
		t.Errorf("other args: %+v", got)
	}
	if got := answer(map[string]any{"tool": "start_cluster"}); got.OK || got.Why != "start_cluster cannot be run in place of stop_cluster; nothing ran." {
		t.Errorf("not an alternative: %+v", got)
	}
	got := answer(map[string]any{"tool": "destroy_cluster", "args": `{"clusterName":"c1"}`})
	if !got.OK || !got.Failed || got.Summary != "failed: cluster c1 is busy" || !strings.HasPrefix(got.Result, "The user chose destroy_cluster in place of stop_cluster, so destroy_cluster ran. ") {
		t.Errorf("swapped: %+v", got)
	}
	if got.Rec == nil || len(got.Rec.Turn) != 1 || got.Rec.Turn[0].Text() != "stopping" {
		t.Errorf("record %+v", got.Rec)
	}
	if again := answer(nil); again.OK || !strings.HasPrefix(again.Why, "This request is no longer waiting") {
		t.Errorf("second answer: %+v", again)
	}
	want := "POST /api/organizations/acme/users/alice/clusters/c1/destroy Bearer own-key"
	if calls[len(calls)-1] != want {
		t.Errorf("platform calls %q", calls)
	}

	// A denial runs nothing and says what the user said.
	tools.Hold(r, map[string]any{"conversationId": "conv-1", "messageId": "srv-3", "toolCallId": "call_2", "tool": "cancel_workflow_run", "canonical": `{"slug":"r1"}`, "args": json.RawMessage(`{"slug":"r1"}`)})
	n := len(calls)
	d := answer(map[string]any{"messageId": "srv-3", "toolCallId": "call_2", "allowed": false, "feedback": " not now "})
	if !d.OK || d.Summary != "declined" || !strings.HasSuffix(d.Result, "They said: not now") || len(calls) != n {
		t.Errorf("denial %+v", d)
	}
	// An expired hold is refused and dropped.
	t.Setenv("STUDIO_APPROVAL_TTL_MS", "1")
	tools.Hold(r, map[string]any{"conversationId": "conv-1", "messageId": "srv-4", "toolCallId": "call_3", "tool": "cancel_workflow_run", "canonical": `{"slug":"r1"}`, "args": json.RawMessage(`{"slug":"r1"}`)})
	time.Sleep(5 * time.Millisecond)
	if e := answer(map[string]any{"messageId": "srv-4", "toolCallId": "call_3"}); e.OK || !strings.HasPrefix(e.Why, "This request expired") {
		t.Errorf("expired %+v", e)
	}
}
