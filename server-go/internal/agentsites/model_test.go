package agentsites

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// A session made on a model the AI connection does not offer is moved to
// one it does before its first message, and the start says so.
func TestAStartMovesOffAModelNotOffered(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/status":
			io.WriteString(w, `{"protocolVersion":2,"hostname":"here"}`)
		case r.URL.Path == "/v1/sessions" && r.Method == "POST":
			io.WriteString(w, `{"id":"20261122-100002-newnew","model":"acct:anthropic/claude-x"}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	env := map[string]string{"AGENT_LOCAL_SITE": "1", "PW_CODE_SOCKET": sock, "PW_GATEWAY_URL": "https://platform.example/api/openai/v1"}
	p := platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(),
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	s := &Sites{P: p, Daemon: pwcode.New(p)}
	var asked string
	st, err := s.Start(context.Background(), StartAgent{Site: Local, Objective: "List the notes.", Workspace: t.TempDir(),
		ModelFor: func(cur string) (string, string) {
			asked = cur
			return "acct:codex/gpt-b", "pw code's model acct:anthropic/claude-x is not enabled for the AI connection, so the session runs on acct:codex/gpt-b"
		}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "acct:anthropic/claude-x" {
		t.Fatalf("asked about %q", asked)
	}
	if !strings.Contains(jsonv.String(jsonv.Path(st, "modelNote")), "runs on acct:codex/gpt-b") {
		t.Fatalf("no note: %v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	set, msg := -1, -1
	for i, c := range calls {
		if strings.HasPrefix(c, "PATCH /v1/sessions/20261122-100002-newnew/settings") {
			var b map[string]any
			json.Unmarshal([]byte(c[strings.Index(c, "{"):]), &b)
			if b["model"] == "acct:codex/gpt-b" {
				set = i
			}
		}
		if strings.HasPrefix(c, "POST /v1/sessions/20261122-100002-newnew/messages") && msg < 0 {
			msg = i
		}
	}
	if set < 0 || msg < 0 || set > msg {
		t.Fatalf("model moved at %d, first message at %d: %v", set, msg, calls)
	}
}
