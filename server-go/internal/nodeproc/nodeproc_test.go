package nodeproc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// The test binary stands in for Node: run with NODEPROC_FAKE set, it serves
// /healthz on PORT the way that mode says.
func TestMain(m *testing.M) {
	if mode := os.Getenv("NODEPROC_FAKE"); mode != "" {
		fakeNode(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeNode(mode string) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("PORT"))
	if err != nil {
		os.Exit(1)
	}
	healthy := make(chan struct{})
	switch mode {
	case "signal":
		// /healthz never answers 200, so only the signal can say ready.
		if fd, err := strconv.Atoi(os.Getenv("STUDIO_READY_FD")); err == nil {
			f := os.NewFile(uintptr(fd), "ready")
			f.WriteString("ready\n")
			f.Close()
		}
	case "poll":
		// An older Node: no signal, /healthz answers after a while.
		time.AfterFunc(150*time.Millisecond, func() { close(healthy) })
	}
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-healthy:
			io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
}

func startFake(t *testing.T, mode string) (*Proc, time.Duration, error) {
	t.Helper()
	t.Setenv("NODEPROC_FAKE", mode)
	t0 := time.Now()
	p, err := Start(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), os.Args[0], "-test.run=^$", 10*time.Second, nil)
	if p != nil {
		t.Cleanup(func() { p.Stop(2 * time.Second) })
	}
	return p, time.Since(t0), err
}

// Node's line on the readiness pipe is enough: Start returns although
// /healthz never answers 200.
func TestStartReadyBySignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no readiness pipe on Windows")
	}
	if _, took, err := startFake(t, "signal"); err != nil {
		t.Fatalf("Start: %v (after %s)", err, took)
	}
}

// Without the signal Start polls /healthz, often enough early on that it
// returns soon after Node answers (it answers 150 ms in).
func TestStartReadyByPolling(t *testing.T) {
	_, took, err := startFake(t, "poll")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if took < 150*time.Millisecond {
		t.Fatalf("ready after %s, before /healthz answered", took)
	}
	if took > 2*time.Second {
		t.Fatalf("ready only after %s", took)
	}
}

func TestEnvironOverridesAndRemoves(t *testing.T) {
	got := Environ([]string{"A=1", "AUTH_JWKS_URL=https://platform/keys", "OIDC_ISSUER=x", "B=2"},
		map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks.json", "OIDC_ISSUER": "", "C": "3"})
	want := map[string]bool{"A=1": true, "B=2": true, "AUTH_JWKS_URL=http://127.0.0.1:9/jwks.json": true, "C=3": true}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Fatalf("unexpected %q in %v", kv, got)
		}
	}
}
