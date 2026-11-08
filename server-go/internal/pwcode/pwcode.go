// Package pwcode is a client for the pw code daemon, the process that hosts
// pw code sessions for one Unix user: this host's daemon over its
// owner-only Unix socket, and another machine's over `pw ssh` (bridge.go).
// It ports server/src/pwcode.ts and server/src/pwcodeBridge.ts.
//
// The daemon's API is versioned by an integer it reports; this client
// checks it by strict equality, as the CLI does, because a session request
// the daemon misreads would run with the wrong settings.
//
// Answers are JSON values as JSON.parse gives them (internal/jsonv), so a
// route that sends a daemon's answer on keeps the daemon's key order.
package pwcode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// ProtocolVersion is the daemon API this client speaks.
const ProtocolVersion = 2

// DaemonError is a failed daemon request: Status is the daemon's HTTP
// status when it answered, 0 when it did not.
type DaemonError struct {
	Message string
	Status  int
}

func (e *DaemonError) Error() string { return e.Message }

// Client is where daemon requests go: this host's socket, or another
// machine's daemon through the bridge.
type Client interface {
	// Site is "local", or the platform resource the daemon runs on.
	Site() string
	// Call sends one request; timeout 0 is the client's default. The
	// answer is the parsed JSON body (nil for an empty one).
	Call(ctx context.Context, method, route string, body any, timeout time.Duration) (any, error)
}

// Service is one process's daemon access: the local daemon, the bridges
// to remote ones, and the start of the local daemon.
type Service struct {
	p *platform.Platform

	local *localClient

	startMu  sync.Mutex
	starting *startFlight

	bridges *bridges
}

type startFlight struct {
	done chan struct{}
	st   *jsonv.Object
	err  error
}

// New is the daemon access for p.
func New(p *platform.Platform) *Service {
	s := &Service{p: p}
	s.local = &localClient{s: s}
	s.bridges = newBridges(s)
	return s
}

// SocketPath is the daemon's socket: PW_CODE_SOCKET, else
// $XDG_STATE_HOME/pw/code-<hostname>.sock (the CLI's path).
func (s *Service) SocketPath() string {
	if v := s.p.Getenv("PW_CODE_SOCKET"); v != "" {
		return v
	}
	state := s.p.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	host, _ := os.Hostname()
	return filepath.Join(state, "pw", "code-"+host+".sock")
}

// Local is this host's daemon.
func (s *Service) Local() Client { return s.local }

// IsLocal reports whether c is this host's daemon.
func (s *Service) IsLocal(c Client) bool { _, ok := c.(*localClient); return ok }

type localClient struct{ s *Service }

func (l *localClient) Site() string { return "local" }

func (l *localClient) Call(ctx context.Context, method, route string, body any, timeout time.Duration) (any, error) {
	sock := l.s.SocketPath()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
	defer client.CloseIdleConnections()
	return Request(ctx, client, method, route, body, timeout)
}

// Doer sends one HTTP request (an *http.Client, or a bridge's pipe).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Request is daemonRequest: one request through client, which reaches the
// daemon. The timeout (default 30 s) is a plain timer.
func Request(ctx context.Context, client Doer, method, route string, body any, timeout time.Duration) (any, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var payload []byte
	if body != nil {
		b, err := jsonv.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if len(payload) > 0 {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(cctx, method, "http://daemon"+route, rd)
	if err != nil {
		return nil, &DaemonError{Message: "pw code daemon unreachable: " + err.Error()}
	}
	req.Host = "daemon"
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(payload))
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, transportError(cctx, err)
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, transportError(cctx, err)
	}
	var parsed any
	if len(text) > 0 {
		if v, err := jsonv.Decode(text); err == nil {
			parsed = v
		}
	}
	if res.StatusCode >= 400 {
		detail := ""
		if o := jsonv.Obj(parsed); o != nil {
			d, _ := o.Get("detail")
			t, _ := o.Get("title")
			switch {
			case jsonv.Truthy(d):
				detail = jsonv.String(d)
			case jsonv.Truthy(t):
				detail = jsonv.String(t)
			}
		}
		if detail == "" {
			detail = sliceRunes(string(text), 200)
		}
		if detail == "" {
			detail = strconv.Itoa(res.StatusCode)
		}
		return nil, &DaemonError{Message: "pw code daemon: " + detail, Status: res.StatusCode}
	}
	return parsed, nil
}

// transportError is how Node's request reports a connection that failed:
// its errno code, else its message.
func transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &DaemonError{Message: "pw code daemon did not answer in time"}
	}
	var de *DaemonError
	if errors.As(err, &de) {
		return de
	}
	return &DaemonError{Message: "pw code daemon unreachable: " + errCode(err)}
}

func errCode(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if n := errnoNames[errno]; n != "" {
			return n
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "ECONNRESET" // Node: "socket hang up", code ECONNRESET
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

var errnoNames = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.ECONNREFUSED: "ECONNREFUSED", syscall.EACCES: "EACCES",
	syscall.ECONNRESET: "ECONNRESET", syscall.EPIPE: "EPIPE", syscall.ENOTSOCK: "ENOTSOCK",
	syscall.ETIMEDOUT: "ETIMEDOUT", syscall.EPERM: "EPERM",
}

func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

/* ---- status ---- */

// Status is daemonStatus: the daemon's status object, or nil when nothing
// answers (5 s locally, 60 s over a bridge).
func (s *Service) Status(ctx context.Context, c Client) *jsonv.Object {
	if c == nil {
		c = s.local
	}
	t := 60 * time.Second
	if s.IsLocal(c) {
		t = 5 * time.Second
	}
	v, err := c.Call(ctx, http.MethodGet, "/v1/status", nil, t)
	if err != nil {
		return nil
	}
	o := jsonv.Obj(v)
	if o == nil && v != nil {
		// A daemon answering something other than an object: what the
		// fields read as in Node (undefined) is no object at all here.
		return jsonv.NewObject()
	}
	return o
}

// UnusableReason is unusableReason: why the daemon cannot be used, or "".
func UnusableReason(st *jsonv.Object) string {
	if st == nil {
		return "no pw code daemon answers on this host"
	}
	pv, _ := st.Get("protocolVersion")
	if f, ok := pv.(float64); !ok || f != ProtocolVersion {
		shown := "undefined"
		switch {
		case !st.Has("protocolVersion"):
		case pv == nil:
			shown = "null"
		default:
			shown = jsonv.String(pv)
		}
		return "the pw code daemon speaks protocol " + shown + "; this Studio speaks " + strconv.Itoa(ProtocolVersion)
	}
	return ""
}

// EnsureDaemon is ensureDaemon: the local daemon's status, starting it
// (`pw code daemon start [--context ctx]`, then up to 40 polls 250 ms
// apart) when none answers. Concurrent callers share one start.
func (s *Service) EnsureDaemon(ctx context.Context) (*jsonv.Object, error) {
	s.startMu.Lock()
	f := s.starting
	if f == nil {
		f = &startFlight{done: make(chan struct{})}
		s.starting = f
		go func() {
			f.st, f.err = s.ensure(context.WithoutCancel(ctx))
			s.startMu.Lock()
			s.starting = nil
			s.startMu.Unlock()
			close(f.done)
		}()
	}
	s.startMu.Unlock()
	select {
	case <-f.done:
		return f.st, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) ensure(ctx context.Context) (*jsonv.Object, error) {
	st := s.Status(ctx, nil)
	if st == nil {
		args := []string{"code", "daemon", "start"}
		if c := s.p.PWContext(); c != "" {
			args = append(args, "--context", c)
		}
		if _, err := s.p.Exec(ctx, args, platform.ExecOptions{Timeout: 30 * time.Second}); err != nil {
			return nil, &DaemonError{Message: "could not start the pw code daemon: " + sliceRunes(trimJS(platform.ErrText(err)), 300)}
		}
		for i := 0; i < 40 && st == nil; i++ {
			time.Sleep(250 * time.Millisecond)
			st = s.Status(ctx, nil)
		}
	}
	if why := UnusableReason(st); why != "" {
		return nil, &DaemonError{Message: why}
	}
	return st, nil
}

var wsTrim = regexp.MustCompile(`^[\s\x{feff}\x{a0}]+|[\s\x{feff}\x{a0}]+$`)

func trimJS(s string) string { return wsTrim.ReplaceAllString(s, "") }

/* ---- sessions ---- */

func sid(id string) string { return "/v1/sessions/" + platform.EncodeURIComponent(id) }

// CreateSession is POST /v1/sessions with the request as given (an
// object whose absent fields are left out, as Node's).
func CreateSession(ctx context.Context, c Client, req *jsonv.Object) (*jsonv.Object, error) {
	return obj(c.Call(ctx, http.MethodPost, "/v1/sessions", req, 0))
}

// ListSessions is GET /v1/sessions.
func ListSessions(ctx context.Context, c Client) (any, error) {
	return c.Call(ctx, http.MethodGet, "/v1/sessions", nil, 0)
}

// GetSession is GET /v1/sessions/:id.
func GetSession(ctx context.Context, c Client, id string) (*jsonv.Object, error) {
	return obj(c.Call(ctx, http.MethodGet, sid(id), nil, 0))
}

// AttachSession loads a saved session into the daemon, which a goal needs.
func AttachSession(ctx context.Context, c Client, id string) (any, error) {
	return c.Call(ctx, http.MethodPost, sid(id)+"/attach", jsonv.NewObject(), 0)
}

// SessionMCP is GET /v1/sessions/:id/mcp.
func SessionMCP(ctx context.Context, c Client, id string) (any, error) {
	return c.Call(ctx, http.MethodGet, sid(id)+"/mcp", nil, 0)
}

// SendMessage queues a message as the session's next turn.
func SendMessage(ctx context.Context, c Client, id, text string) (*jsonv.Object, error) {
	return obj(c.Call(ctx, http.MethodPost, sid(id)+"/messages", jsonv.NewObject("text", text), 0))
}

// AnswerApproval answers an approval request: {allowed, denyMessage?}.
func AnswerApproval(ctx context.Context, c Client, id, approvalID string, answer *jsonv.Object) (any, error) {
	return c.Call(ctx, http.MethodPost, sid(id)+"/approvals/"+platform.EncodeURIComponent(approvalID), answer, 0)
}

// SteerSession gives a running turn direction without ending it.
func SteerSession(ctx context.Context, c Client, id, text string) (*jsonv.Object, error) {
	return obj(c.Call(ctx, http.MethodPost, sid(id)+"/direction", jsonv.NewObject("text", text), 0))
}

// SessionState is GET /v1/sessions/:id/state.
func SessionState(ctx context.Context, c Client, id string) (*jsonv.Object, error) {
	return obj(c.Call(ctx, http.MethodGet, sid(id)+"/state", nil, 0))
}

// InterruptSession stops the session's turn.
func InterruptSession(ctx context.Context, c Client, id string) (any, error) {
	return c.Call(ctx, http.MethodPost, sid(id)+"/interrupt", jsonv.NewObject(), 0)
}

var notLive = regexp.MustCompile(`(?i)not live`)

// SetGoal is setGoal: a condition sets the goal, "" reads it, and pause,
// resume and clear do what they say; a session not live in the daemon is
// attached and the request sent again.
func SetGoal(ctx context.Context, c Client, id, args string) (any, error) {
	goal := func() (any, error) {
		return c.Call(ctx, http.MethodPost, sid(id)+"/goal", jsonv.NewObject("args", args, "client", "studio"), 0)
	}
	v, err := goal()
	if err == nil {
		return v, nil
	}
	var de *DaemonError
	if !errors.As(err, &de) || !notLive.MatchString(de.Message) {
		return nil, err
	}
	if _, err := AttachSession(ctx, c, id); err != nil {
		return nil, err
	}
	return goal()
}

// WaitForMCP is waitForMcp: polls the session's MCP servers until none is
// pending or timeout (default 20 s) passes; a daemon without the route
// gives none.
func WaitForMCP(ctx context.Context, c Client, id string, timeout time.Duration) []*jsonv.Object {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	until := time.Now().Add(timeout)
	for {
		r, err := SessionMCP(ctx, c, id)
		if err != nil {
			return nil
		}
		var list []any
		if a, ok := r.([]any); ok {
			list = a
		} else {
			list = jsonv.Arr(jsonv.Path(r, "servers"))
		}
		servers := make([]*jsonv.Object, 0, len(list))
		pending := false
		for _, x := range list {
			o := jsonv.Obj(x)
			if o == nil {
				o = jsonv.NewObject()
			}
			servers = append(servers, o)
			g := func(k string) bool { v, _ := o.Get(k); return jsonv.Truthy(v) }
			if g("connecting") || (!g("connected") && !g("err") && !g("disabled")) {
				pending = true
			}
		}
		if !pending || time.Now().After(until) {
			return servers
		}
		select {
		case <-ctx.Done():
			return servers
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func obj(v any, err error) (*jsonv.Object, error) {
	if err != nil {
		return nil, err
	}
	return jsonv.Obj(v), nil
}
