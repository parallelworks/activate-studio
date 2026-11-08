package pwcode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

func newPlatform(t *testing.T, env map[string]string) *platform.Platform {
	t.Helper()
	return platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}})
}

func send(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func status(w http.ResponseWriter, proto int) {
	send(w, 200, map[string]any{"protocolVersion": proto, "version": "v-test", "pid": 1, "hostname": "node-7", "sessions": 0, "remoteStart": true, "remoteMaxPermissionMode": "read-only"})
}

func TestStatusAndProtocol(t *testing.T) {
	var proto atomic.Int32
	proto.Store(2)
	var seen atomic.Value
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Host + " " + r.Header.Get("Accept"))
		if r.URL.Path == "/v1/status" {
			status(w, int(proto.Load()))
			return
		}
		send(w, 404, map[string]string{"detail": "no such session"})
	}))
	s := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": sock}))
	ctx := context.Background()
	st := s.Status(ctx, nil)
	if st == nil || UnusableReason(st) != "" {
		t.Fatal(st)
	}
	if seen.Load() != "daemon application/json" {
		t.Fatal(seen.Load())
	}
	// The status keeps the daemon's key order.
	// The status keeps the daemon's key order (Go's encoder sorted them).
	if got := string(jsonv.MustMarshal(st)); got != `{"hostname":"node-7","pid":1,"protocolVersion":2,"remoteMaxPermissionMode":"read-only","remoteStart":true,"sessions":0,"version":"v-test"}` {
		t.Fatal(got)
	}
	proto.Store(3)
	if why := UnusableReason(s.Status(ctx, nil)); why != "the pw code daemon speaks protocol 3; this Studio speaks 2" {
		t.Fatal(why)
	}
	if why := UnusableReason(nil); why != "no pw code daemon answers on this host" {
		t.Fatal(why)
	}
	if why := UnusableReason(jsonv.NewObject()); why != "the pw code daemon speaks protocol undefined; this Studio speaks 2" {
		t.Fatal(why)
	}
	_, err := GetSession(ctx, s.Local(), "nope")
	var de *DaemonError
	if !errors.As(err, &de) || de.Status != 404 || de.Message != "pw code daemon: no such session" {
		t.Fatal(err)
	}
	// Nothing on the socket.
	gone := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": filepath.Join(t.TempDir(), "none.sock")}))
	if gone.Status(ctx, nil) != nil {
		t.Fatal("no daemon")
	}
	_, err = GetSession(ctx, gone.Local(), "x")
	if err == nil || err.Error() != "pw code daemon unreachable: ENOENT" {
		t.Fatal(err)
	}
}

func TestSocketPathDefault(t *testing.T) {
	s := New(newPlatform(t, map[string]string{"XDG_STATE_HOME": "/st"}))
	host, _ := os.Hostname()
	if s.SocketPath() != "/st/pw/code-"+host+".sock" {
		t.Fatal(s.SocketPath())
	}
}

func TestPlainErrorAndTimeout(t *testing.T) {
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(time.Second)
		}
		w.WriteHeader(500)
		io.WriteString(w, "plain failure")
	}))
	s := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": sock}))
	_, err := s.Local().Call(context.Background(), "GET", "/x", nil, 0)
	if err == nil || err.Error() != "pw code daemon: plain failure" {
		t.Fatal(err)
	}
	_, err = s.Local().Call(context.Background(), "GET", "/slow", nil, 100*time.Millisecond)
	if err == nil || err.Error() != "pw code daemon did not answer in time" {
		t.Fatal(err)
	}
}

func TestGoalAttachesWhenNotLive(t *testing.T) {
	var attached atomic.Bool
	var bodies []string
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		switch r.URL.Path {
		case "/v1/sessions/s%2F1/goal", "/v1/sessions/s/1/goal":
			if !attached.Load() {
				send(w, 409, map[string]string{"title": "session is not live"})
				return
			}
			send(w, 200, map[string]any{"output": "Goal set."})
		case "/v1/sessions/s%2F1/attach", "/v1/sessions/s/1/attach":
			attached.Store(true)
			send(w, 200, map[string]any{"id": "s/1"})
		}
	}))
	s := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": sock}))
	v, err := SetGoal(context.Background(), s.Local(), "s/1", "done")
	if err != nil || jsonv.String(jsonv.Path(v, "output")) != "Goal set." {
		t.Fatal(v, err)
	}
	want := []string{
		`POST /v1/sessions/s/1/goal {"args":"done","client":"studio"}`,
		`POST /v1/sessions/s/1/attach {}`,
		`POST /v1/sessions/s/1/goal {"args":"done","client":"studio"}`,
	}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Fatal(bodies)
	}
}

func TestWaitForMCP(t *testing.T) {
	var polls atomic.Int32
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := polls.Add(1)
		// Right after creation: neither connecting nor connected; then connecting; then connected.
		send(w, 200, map[string]any{"servers": []any{map[string]any{"name": "task", "connected": n > 2, "connecting": n == 2}}})
	}))
	s := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": sock}))
	servers := WaitForMCP(context.Background(), s.Local(), "x", 5*time.Second)
	if len(servers) != 1 || polls.Load() != 3 {
		t.Fatal(len(servers), polls.Load())
	}
	// A daemon without the route: go ahead with none.
	none := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": filepath.Join(t.TempDir(), "x.sock")}))
	if WaitForMCP(context.Background(), none.Local(), "x", time.Second) != nil {
		t.Fatal("none")
	}
}

func TestEnsureDaemonStartsOne(t *testing.T) {
	dir := t.TempDir()
	sockDir, _ := os.MkdirTemp("", "pwe")
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "code.sock")
	calls := filepath.Join(dir, "calls")
	cli := filepath.Join(dir, "pw")
	os.WriteFile(cli, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755)
	p := newPlatform(t, map[string]string{"PW_CODE_SOCKET": sock, "PW_CLI": cli, "PW_CONTEXT": "ctx-a"})
	s := New(p)
	go func() {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(calls); err == nil {
				pwcodetest.Listen(t, sock, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { status(w, 2) }))
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	// Two callers share one start.
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.EnsureDaemon(context.Background()); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(calls)
	if strings.TrimSpace(string(b)) != "code daemon start --context ctx-a" {
		t.Fatalf("%q", b)
	}
	// A CLI that fails says why.
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("#!/bin/sh\necho '  no login here  ' >&2\nexit 1\n"), 0o755)
	q := New(newPlatform(t, map[string]string{"PW_CODE_SOCKET": filepath.Join(dir, "none.sock"), "PW_CLI": bad}))
	if _, err := q.EnsureDaemon(context.Background()); err == nil || err.Error() != "could not start the pw code daemon: no login here" {
		t.Fatal(err)
	}
}

/* ---- the bridge ---- */

// TestHelperStdioDaemon is the fake remote daemon: the bridge's command
// runs this test binary, which serves the daemon API on its stdio.
func TestHelperStdioDaemon(t *testing.T) {
	if os.Getenv("PWC_HELPER") != "1" {
		t.Skip("helper process")
	}
	log := os.Getenv("PWC_LOG")
	pwcodetest.ServeStdio(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _ := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(r.Method + " " + r.URL.Path + "\n")
		f.Close()
		switch r.URL.Path {
		case "/v1/status":
			status(w, 2)
		case "/v1/die":
			os.Exit(3)
		default:
			send(w, 404, map[string]string{"detail": "no route"})
		}
	}))
	os.Exit(0)
}

func bridgeCLI(t *testing.T, dir string) (cli, calls, log string) {
	calls = filepath.Join(dir, "calls")
	log = filepath.Join(dir, "daemon.log")
	cli = filepath.Join(dir, "pw")
	exe, _ := os.Executable()
	script := "#!/bin/sh\necho \"$# $1 $2 $3 $4|$5|key=$PW_API_KEY\" >> " + calls + "\necho 'Warning: Permanently added host' >&2\n" +
		"PWC_HELPER=1 PWC_LOG=" + log + " exec " + exe + " -test.run=TestHelperStdioDaemon\n"
	os.WriteFile(cli, []byte(script), 0o755)
	return
}

func TestBridgeOneConnectionPerKeyAndMachine(t *testing.T) {
	dir := t.TempDir()
	cli, calls, log := bridgeCLI(t, dir)
	s := New(newPlatform(t, map[string]string{"PW_CLI": cli, "PW_GATEWAY_URL": "https://platform.test/api/openai/v1"}))
	defer s.CloseBridges()
	ctx := context.Background()
	c := s.Remote("cluster-a", "0123456789abcdefuser-key-tail")
	if c.Site() != "cluster-a" {
		t.Fatal(c.Site())
	}
	for i := 0; i < 3; i++ {
		st := s.Status(ctx, c)
		if st == nil || jsonv.String(jsonv.Path(st, "hostname")) != "node-7" {
			t.Fatal(i, st)
		}
	}
	// A 404 is an answer: the connection stays.
	if _, err := GetSession(ctx, c, "x"); err == nil || err.Error() != "pw code daemon: no route" {
		t.Fatal(err)
	}
	s.Status(ctx, s.Remote("cluster-a", "0123456789abcdefuser-key-tail"))
	b, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 || lines[0] != "5 ssh --platform-host platform.test cluster-a|pw code daemon proxy --ensure|key=0123456789abcdefuser-key-tail" {
		t.Fatalf("%q", b)
	}
	d, _ := os.ReadFile(log)
	if strings.Count(string(d), "GET /v1/status") != 4 {
		t.Fatalf("%s", d)
	}
	// The pipe dying mid-request: the error names the machine and what ssh
	// said last, and the next request opens a fresh bridge.
	_, err := c.Call(ctx, "GET", "/v1/die", nil, 0)
	if err == nil || err.Error() != "cluster-a: Warning: Permanently added host" {
		t.Fatal(err)
	}
	if s.Status(ctx, c) == nil {
		t.Fatal("reopened")
	}
	b, _ = os.ReadFile(calls)
	if n := len(strings.Split(strings.TrimSpace(string(b)), "\n")); n != 2 {
		t.Fatal(n)
	}
	// Idle bridges are swept.
	if s.Sweep(time.Now()) != 0 || s.Sweep(time.Now().Add(IdleTimeout+time.Second)) != 1 {
		t.Fatal("sweep")
	}
}

func TestBridgeThatNeverConnects(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "pw")
	os.WriteFile(cli, []byte("#!/bin/sh\necho 'ssh: connect to host: Connection refused' >&2\nexit 255\n"), 0o755)
	s := New(newPlatform(t, map[string]string{"PW_CLI": cli}))
	defer s.CloseBridges()
	_, err := s.Remote("cl", "k").Call(context.Background(), "GET", "/v1/status", nil, 0)
	if err == nil || err.Error() != "cl: ssh: connect to host: Connection refused" {
		t.Fatal(err)
	}
}
