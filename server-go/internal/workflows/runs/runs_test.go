package runs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
)

// The cases of server/test/runs.test.mjs, with the conversation note
// observed at the bridge call Node answers.

type note struct{ id, content string }

func registry(t *testing.T, ix string) (*Registry, func() []note) {
	t.Helper()
	env := map[string]string{"STUDIO_RUN_POLL_MS": "15"}
	p := platform.New(platform.Options{IndexBase: ix, KBRoot: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	r := New(p)
	var mu sync.Mutex
	var notes []note
	r.Note = func(id, c string) { mu.Lock(); notes = append(notes, note{id, c}); mu.Unlock() }
	r.Log = func(string) {}
	r.SessionKey = func(*string) string { return "k" }
	r.Sessions = func(context.Context, string) ([]*sessions.SessionRow, error) {
		return nil, errors.New("no network in tests")
	}
	t.Cleanup(r.Stop)
	return r, func() []note { mu.Lock(); defer mu.Unlock(); return append([]note{}, notes...) }
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

func lines(t *testing.T, file string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(file)
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunFollowedToItsEndAndReported(t *testing.T) {
	r, notes := registry(t, t.TempDir())
	var views atomic.Int32
	r.CLI = func(_ context.Context, args []string) (string, error) {
		if strings.Join(args[:3], " ") != "workflows runs view" {
			t.Error(args)
		}
		st := "running"
		if views.Add(1) >= 3 {
			st = "completed"
		}
		return `{"slug":"` + args[3] + `","status":"` + st + `"}` + "\n\nA new version of the PW CLI is available.\n", nil
	}
	var ended []RunEnded
	var mu sync.Mutex
	OnRunEnded(func(e RunEnded) { mu.Lock(); ended = append(ended, e); mu.Unlock() })
	t.Cleanup(func() { hooksMu.Lock(); hooks = nil; hooksMu.Unlock() })

	rec := r.RecordRun(RecordInput{Slug: "hellobatch-00007", Workflow: "hellobatch", Resource: Str("vega"), ConversationID: Str("c1"), Owner: Str("me")})
	if rec.State != "running" {
		t.Fatal(rec.State)
	}
	waitFor(t, "completed", func() bool { return r.List(10)[0].State == "completed" })
	if r.List(10)[0].EndedAt == nil {
		t.Fatal("endedAt")
	}
	var ts []any
	for _, l := range lines(t, r.File()) {
		ts = append(ts, l["t"])
	}
	if len(ts) != 2 || ts[0] != "launch" || ts[1] != "end" {
		t.Fatal(ts)
	}
	n := notes()
	if len(n) != 1 || n[0].id != "c1" || n[0].content != "Run hellobatch-00007 of hellobatch on vega finished: completed. Ask for its detail (workflow_run_detail hellobatch-00007) to see the output." {
		t.Fatal(n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ended) != 1 || ended[0].Slug != "hellobatch-00007" || ended[0].State != "completed" || *ended[0].Resource != "vega" {
		t.Fatal(ended)
	}
}

func TestUnfinishedRunsAreWatchedAgainFromTheFile(t *testing.T) {
	ix := t.TempDir()
	os.WriteFile(filepath.Join(ix, "runs.jsonl"), []byte(`{"t":"launch","slug":"x-1","workflow":"w","launchedAt":"2026-09-05T00:00:00Z"}
{"t":"launch","slug":"x-2","workflow":"w","launchedAt":"2026-09-05T00:00:01Z"}
{"t":"end","slug":"x-2","state":"completed","endedAt":"2026-09-05T00:01:00Z"}
`), 0o644)
	r, _ := registry(t, ix)
	var mu sync.Mutex
	asked := map[string]bool{}
	r.CLI = func(_ context.Context, args []string) (string, error) {
		mu.Lock()
		asked[args[3]] = true
		mu.Unlock()
		return `{"status":"error"}`, nil
	}
	if n := r.WatchRegistered(); n != 1 {
		t.Fatal("only the run without an end event:", n)
	}
	waitFor(t, "x-1 error", func() bool { return r.Get("x-1").State == "error" })
	mu.Lock()
	if len(asked) != 1 || !asked["x-1"] {
		t.Fatal(asked)
	}
	mu.Unlock()
	if r.Get("x-2").State != "completed" {
		t.Fatal(r.Get("x-2").State)
	}
	// Newest first.
	if l := r.List(10); l[0].Slug != "x-2" || l[1].Slug != "x-1" {
		t.Fatal(l[0].Slug, l[1].Slug)
	}
}

func TestARunThePlatformKeepsFailingIsDropped(t *testing.T) {
	r, _ := registry(t, t.TempDir())
	var n atomic.Int32
	r.CLI = func(context.Context, []string) (string, error) {
		n.Add(1)
		return "", errors.New("Workflow run not found")
	}
	r.RecordRun(RecordInput{Slug: "gone-1", Workflow: "w"})
	waitFor(t, "30 failures", func() bool { return n.Load() >= 30 })
	waitFor(t, "watch dropped", func() bool { return !r.Watching("gone-1") })
	time.Sleep(15 * 6 * time.Millisecond)
	if n.Load() != 30 {
		t.Fatal("no polls after giving up:", n.Load())
	}
}

func TestAnotherProcessesLaunchIsRead(t *testing.T) {
	ix := t.TempDir()
	r, _ := registry(t, ix)
	if len(r.List(10)) != 0 {
		t.Fatal("empty")
	}
	// Node (or an older process) appends a line; the registry rereads.
	f, _ := os.OpenFile(filepath.Join(ix, "runs.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"t":"launch","slug":"n-1","workflow":"w","launchedAt":"2026-09-05T00:00:00Z","conversationId":"c9"}` + "\n")
	f.Close()
	got := r.Get("n-1")
	if got == nil || got.State != "running" || *got.ConversationID != "c9" {
		t.Fatal(got)
	}
}

func TestSessionShownOnceInItsConversation(t *testing.T) {
	r, notes := registry(t, t.TempDir())
	r.CLI = func(context.Context, []string) (string, error) { return `{"status":"running"}`, nil }
	r.Sessions = func(context.Context, string) ([]*sessions.SessionRow, error) {
		return []*sessions.SessionRow{{Name: "other", Status: "running"}, {Name: "jupyter-s-1", User: "me", Status: "running"}}, nil
	}
	r.RecordRun(RecordInput{Slug: "s-1", Workflow: "jupyter", ConversationID: Str("c1")})
	waitFor(t, "session note", func() bool { return len(notes()) > 0 })
	time.Sleep(60 * time.Millisecond)
	n := notes()
	if len(n) != 1 || n[0].content != "Run s-1 of jupyter opened the session jupyter-s-1.\n\n![jupyter-s-1](/?embed=session&user=me&name=jupyter-s-1)" {
		t.Fatal(n)
	}
	last := lines(t, r.File())
	if l := last[len(last)-1]; l["t"] != "session" || l["name"] != "jupyter-s-1" {
		t.Fatal(l)
	}
}
