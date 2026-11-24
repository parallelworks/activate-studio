package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// startReply posts a message whose reply pauses after its first words
// until release is closed; leave cancels the request, as a closed tab or
// a reload does.
func startReply(t *testing.T, base, conv string, release <-chan struct{}) (leave func(), gone <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	b, _ := json.Marshal(map[string]any{"model": "test-model", "conversationId": conv, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "Write the long report."}}})
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/chat/stream", bytes.NewReader(b))
	done := make(chan struct{})
	go func() {
		defer close(done)
		if res, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	return cancel, done
}

// generatingOf is what GET .../generating says: the reply so far, or nil.
func generatingOf(t *testing.T, base, conv string) map[string]any {
	res, err := http.Get(base + "/api/chat/conversations/" + conv + "/generating")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		// The message has not reached the conversation yet.
		return nil
	}
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("generating: %d %s", res.StatusCode, b)
	}
	var v map[string]any
	json.NewDecoder(res.Body).Decode(&v)
	return v
}

// until polls f for up to five seconds.
func until(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A reply runs on when its page goes: a page opened meanwhile (a reload)
// sees what it has written so far and follows it, and the whole reply is
// stored when it ends.
func TestReplyRunsOnWhenThePageGoes(t *testing.T) {
	m, srv, gw, _ := newTestModule(t, nil)
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	r := text("Part one.", " Part two.")
	r.wait = release
	gw.load([]reply{r})
	leave, gone := startReply(t, srv.URL, "c-live", release)
	var first map[string]any
	until(t, "the first words", func() bool {
		first = generatingOf(t, srv.URL, "c-live")
		return first != nil && first["content"] == "Part one."
	})

	// The tab closes mid-reply.
	leave()
	<-gone
	time.Sleep(100 * time.Millisecond)
	// A reload finds it still running, with what it wrote before.
	again := generatingOf(t, srv.URL, "c-live")
	if again == nil || again["content"] != "Part one." || again["messageId"] != first["messageId"] {
		t.Fatalf("after the page went: %v", again)
	}
	free()
	until(t, "the reply to end", func() bool { return generatingOf(t, srv.URL, "c-live") == nil })
	c := m.Conv.Get("c-live")
	if c == nil || len(c.Messages) != 2 || c.Messages[1].Text() != "Part one. Part two." || c.Messages[1].FinishReason == "stopped" {
		t.Fatalf("stored %+v", c)
	}
}

// Stop ends a reply that runs on without its page, and what it wrote is
// stored as stopped.
func TestStopEndsAReplyRunningWithoutItsPage(t *testing.T) {
	m, srv, gw, _ := newTestModule(t, nil)
	var logs lockedBuffer
	was := m.A.Log
	m.A.Log = slog.New(slog.NewTextHandler(&logs, nil))
	defer func() { m.A.Log = was }()
	release := make(chan struct{})
	defer close(release)
	r := text("Part one.", " Part two.")
	r.wait = release
	gw.load([]reply{r})
	leave, gone := startReply(t, srv.URL, "c-stop", release)
	until(t, "the first words", func() bool { g := generatingOf(t, srv.URL, "c-stop"); return g != nil && g["content"] == "Part one." })
	leave()
	<-gone
	res, err := http.Post(srv.URL+"/api/chat/conversations/c-stop/stop", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if out["stopped"] != true {
		t.Fatalf("stop: %v", out)
	}
	if g := generatingOf(t, srv.URL, "c-stop"); g != nil {
		t.Fatalf("still generating: %v", g)
	}
	c := m.Conv.Get("c-stop")
	if c == nil || len(c.Messages) != 2 || c.Messages[1].Text() != "Part one." || c.Messages[1].FinishReason != "stopped" {
		t.Fatalf("stored %+v", c)
	}
	// The person asked for it: logged as stopped, not as a failure.
	if l := logs.String(); !strings.Contains(l, "chat reply stopped") || strings.Contains(l, "chat stream failed") {
		t.Errorf("log:\n%s", l)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
