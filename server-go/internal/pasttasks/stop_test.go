package pasttasks

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// daemon answers interrupts: ok for the sessions it has, 404 for others,
// or fails as unreachable.
type daemon struct {
	has         map[string]bool
	down        bool
	refuse      int
	interrupted []string
}

func (d *daemon) Site() string { return "local" }

func (d *daemon) Call(_ context.Context, method, route string, _ any, _ time.Duration) (any, error) {
	if d.down {
		return nil, errors.New("dial unix: connection refused")
	}
	if d.refuse != 0 {
		return nil, &pwcode.DaemonError{Message: "refused", Status: d.refuse}
	}
	id, ok := strings.CutSuffix(strings.TrimPrefix(route, "/v1/sessions/"), "/interrupt")
	if method != http.MethodPost || !ok {
		return nil, &pwcode.DaemonError{Message: "no route", Status: 404}
	}
	if !d.has[id] {
		return nil, &pwcode.DaemonError{Message: "no such session", Status: 404}
	}
	d.interrupted = append(d.interrupted, id)
	return map[string]any{}, nil
}

// The sessions of agents still working when the engine was retired are
// interrupted once, so the page's "stopped" is true; finished agents'
// sessions are left alone, and a daemon that cannot be reached is tried
// again next time.
func TestTheSessionsLeftRunningAreStopped(t *testing.T) {
	a := seed(t)
	os.WriteFile(filepath.Join(a.Dir, "2026-11-02-live-cd34", "state.json"), []byte(`{"id":"2026-11-02-live-cd34","objective":"Was running","state":"working","createdAt":"2026-11-02T00:00:00.000Z","agents":[
		{"name":"w1","state":"working","sessionId":"20261102-120000-abcdef12"},
		{"name":"w2","state":"input-required","sessionId":"20261102-120001-abcdef13"},
		{"name":"w3","state":"completed","sessionId":"20261102-120002-abcdef14"},
		{"name":"w4","state":"working","sessionId":"20261102-120003-gone0000"}]}`), 0o644)
	if got := strings.Join(a.LeftRunning(), ","); got != "20261102-120000-abcdef12,20261102-120001-abcdef13,20261102-120003-gone0000" {
		t.Fatalf("left running: %s", got)
	}
	d := &daemon{down: true, has: map[string]bool{"20261102-120000-abcdef12": true, "20261102-120001-abcdef13": true, "20261102-120002-abcdef14": true}}
	if n := a.StopLeftRunning(context.Background(), d); n != 0 || len(d.interrupted) != 0 {
		t.Fatalf("with the daemon down: %d", n)
	}
	d.down = false
	// A daemon that refuses the Studio is tried again, not taken as
	// nothing to stop.
	for _, code := range []int{401, 403} {
		d.refuse = code
		if n := a.StopLeftRunning(context.Background(), d); n != 0 {
			t.Fatalf("refused %d: %d", code, n)
		}
		if _, err := os.Stat(filepath.Join(a.Dir, stoppedMark)); err == nil {
			t.Fatalf("a %d refusal marked the sessions stopped", code)
		}
	}
	d.refuse = 0
	if n := a.StopLeftRunning(context.Background(), d); n != 2 || strings.Join(d.interrupted, ",") != "20261102-120000-abcdef12,20261102-120001-abcdef13" {
		t.Fatalf("stopped %d: %v", n, d.interrupted)
	}
	if n := a.StopLeftRunning(context.Background(), d); n != 0 || len(d.interrupted) != 2 {
		t.Fatalf("stopped again: %d %v", n, d.interrupted)
	}
}
