package pwcode

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// A pw code daemon on another machine is reached the way `pw code attach`
// reaches it: `pw ssh <resource> pw code daemon proxy --ensure` bridges the
// ssh session's stdio to that machine's daemon socket (starting the daemon
// when none runs), and the daemon's HTTP API is spoken over the pipe. One
// keep-alive connection per viewer and machine carries every request, one
// at a time, so only the first pays for the ssh setup. The viewer's
// platform key authenticates `pw ssh`.

// IdleTimeout is how long an unused bridge stays open.
const IdleTimeout = 10 * time.Minute

// SweepEvery is how often idle bridges are looked for.
const SweepEvery = 60 * time.Second

type bridges struct {
	s  *Service
	mu sync.Mutex
	m  map[string]*bridge
	// spawn makes the command whose stdio is the pipe (a test seam).
	spawn func(resource, key string) *exec.Cmd
}

type bridge struct {
	id       string
	resource string
	spawn    func() *exec.Cmd

	// reqMu makes requests take turns on the one connection (maxSockets 1).
	reqMu sync.Mutex

	mu     sync.Mutex
	last   time.Time
	stderr string
	conn   *pipeConn
	rd     *bufio.Reader
	dead   bool
}

func newBridges(s *Service) *bridges {
	b := &bridges{s: s, m: map[string]*bridge{}}
	b.spawn = func(resource, key string) *exec.Cmd {
		args := []string{"ssh"}
		if host := s.platformHost(); host != "" {
			args = append(args, "--platform-host", host)
		}
		args = append(args, resource, "pw code daemon proxy --ensure")
		cmd := exec.Command(s.p.CLI(), args...)
		cmd.Env = s.p.Environ(map[string]string{"PW_API_KEY": key})
		return cmd
	}
	return b
}

// platformHost is new URL(GATEWAY_BASE).hostname, or "".
func (s *Service) platformHost() string {
	u, err := url.Parse(s.p.GatewayBase())
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// SetBridgeSpawn replaces what runs in place of `pw ssh … daemon proxy`
// (tests).
func (s *Service) SetBridgeSpawn(fn func(resource, key string) *exec.Cmd) {
	s.bridges.mu.Lock()
	s.bridges.spawn = fn
	s.bridges.mu.Unlock()
}

// Remote is remoteDaemon: a client for the user's daemon on resource,
// authenticated with key.
func (s *Service) Remote(resource, key string) Client {
	k := key
	if len(k) > 16 {
		k = k[len(k)-16:]
	}
	return &remoteClient{s: s, id: k + "|" + resource, resource: resource, key: key}
}

type remoteClient struct {
	s                 *Service
	id, resource, key string
}

func (r *remoteClient) Site() string { return r.resource }

func (r *remoteClient) bridge() *bridge {
	bs := r.s.bridges
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if b := bs.m[r.id]; b != nil {
		return b
	}
	spawn := bs.spawn
	resource, key := r.resource, r.key
	b := &bridge{id: r.id, resource: r.resource, last: time.Now(), spawn: func() *exec.Cmd { return spawn(resource, key) }}
	bs.m[r.id] = b
	return b
}

func (r *remoteClient) Call(ctx context.Context, method, route string, body any, timeout time.Duration) (any, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	b := r.bridge()
	b.mu.Lock()
	b.last = time.Now()
	b.mu.Unlock()
	v, err := Request(ctx, b, method, route, body, timeout)
	if err == nil {
		return v, nil
	}
	// An answer from the daemon (a 4xx) leaves the connection good.
	// Anything else may have left the pipe mid-response, so the next
	// request starts a fresh one, and the error says what ssh said.
	var de *DaemonError
	if errors.As(err, &de) && de.Status != 0 {
		return nil, err
	}
	r.s.bridges.drop(b)
	b.destroy()
	b.mu.Lock()
	said := ""
	for _, l := range strings.Split(trimJS(b.stderr), "\n") {
		if l != "" {
			said = l
		}
	}
	b.mu.Unlock()
	if said == "" {
		said = err.Error()
	}
	return nil, &DaemonError{Message: r.resource + ": " + said}
}

func (bs *bridges) drop(b *bridge) {
	bs.mu.Lock()
	if bs.m[b.id] == b {
		delete(bs.m, b.id)
	}
	bs.mu.Unlock()
}

// destroy is agent.destroy(): the pipe closed, its process killed.
func (b *bridge) destroy() {
	b.mu.Lock()
	c := b.conn
	b.conn, b.rd, b.dead = nil, nil, true
	b.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Do sends one request over the bridge's connection, opening it first
// when there is none. A request is never sent twice: what fails on the
// pipe fails the request (Node's agent does not retry either).
func (b *bridge) Do(req *http.Request) (*http.Response, error) {
	b.reqMu.Lock()
	b.mu.Lock()
	c, rd := b.conn, b.rd
	b.mu.Unlock()
	if c == nil {
		conn, err := b.open(b.spawn())
		if err != nil {
			b.reqMu.Unlock()
			return nil, err
		}
		c, rd = conn, bufio.NewReader(conn)
		b.mu.Lock()
		b.conn, b.rd, b.dead = conn, rd, false
		b.mu.Unlock()
	}
	type result struct {
		res *http.Response
		err error
	}
	done := make(chan result, 1)
	go func() {
		if err := req.Write(c); err != nil {
			done <- result{nil, err}
			return
		}
		res, err := http.ReadResponse(rd, req)
		if err != nil {
			done <- result{nil, err}
			return
		}
		// The body is read whole here, so the connection is free for the
		// next request as soon as this one returns.
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			done <- result{nil, err}
			return
		}
		res.Body = io.NopCloser(bytes.NewReader(data))
		if res.Close {
			b.mu.Lock()
			if b.conn == c {
				b.conn, b.rd = nil, nil
			}
			b.mu.Unlock()
			c.Close()
		}
		done <- result{res, nil}
	}()
	select {
	case r := <-done:
		b.reqMu.Unlock()
		return r.res, r.err
	case <-req.Context().Done():
		// req.destroy(): the pipe goes with the request.
		b.mu.Lock()
		if b.conn == c {
			b.conn, b.rd = nil, nil
		}
		b.mu.Unlock()
		c.Close()
		b.reqMu.Unlock()
		return nil, req.Context().Err()
	}
}

// open starts the bridge process and gives its stdio as a connection.
func (b *bridge) open(cmd *exec.Cmd) (*pipeConn, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Its stdout through a pipe of our own: cmd.Wait closes the pipes it
	// made, which could drop what the daemon wrote just before exiting.
	stdout, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = w
	b.mu.Lock()
	b.stderr = ""
	b.mu.Unlock()
	cmd.Stderr = stderrTail{b}
	err = cmd.Start()
	w.Close()
	if err != nil {
		stdout.Close()
		return nil, err
	}
	c := &pipeConn{cmd: cmd, in: stdin, out: stdout, b: b, exited: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(c.exited)
	}()
	return c, nil
}

// stderrTail keeps the last 2,000 bytes ssh wrote to stderr.
type stderrTail struct{ b *bridge }

func (t stderrTail) Write(p []byte) (int, error) {
	t.b.mu.Lock()
	s := t.b.stderr + string(p)
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	t.b.stderr = s
	t.b.mu.Unlock()
	return len(p), nil
}

// pipeConn is the bridge process's stdio as a net.Conn: writes go to its
// stdin, reads come from its stdout. A pipe has no deadlines.
type pipeConn struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	b      *bridge
	once   sync.Once
	exited chan struct{}
}

func (c *pipeConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *pipeConn) Close() error {
	c.once.Do(func() {
		c.in.Close()
		c.out.Close()
		if c.cmd.Process != nil {
			c.cmd.Process.Signal(syscall.SIGTERM)
		}
	})
	return nil
}
func (c *pipeConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *pipeConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pw-ssh" }

// Sweep closes bridges unused for IdleTimeout; it returns how many.
func (s *Service) Sweep(now time.Time) int {
	bs := s.bridges
	bs.mu.Lock()
	var idle []*bridge
	for id, b := range bs.m {
		b.mu.Lock()
		old := now.Sub(b.last) > IdleTimeout
		b.mu.Unlock()
		if old {
			idle = append(idle, b)
			delete(bs.m, id)
		}
	}
	bs.mu.Unlock()
	for _, b := range idle {
		b.destroy()
	}
	return len(idle)
}

// CloseBridges is closeBridges: every bridge closed; it returns how many
// were open.
func (s *Service) CloseBridges() int {
	bs := s.bridges
	bs.mu.Lock()
	all := make([]*bridge, 0, len(bs.m))
	for id, b := range bs.m {
		all = append(all, b)
		delete(bs.m, id)
	}
	bs.mu.Unlock()
	for _, b := range all {
		b.destroy()
	}
	return len(all)
}

// Start runs the sweeper under a and closes every bridge at stop.
func (s *Service) Start(a *app.App) {
	a.Go("pw code bridge sweeper", func(ctx context.Context) {
		t := time.NewTicker(SweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.Sweep(now)
			}
		}
	})
	a.OnStop("pw code bridges", s.CloseBridges)
}
