// Package ragendpoint is server/src/ragEndpoint.ts: the optional
// registration of the Studio's /v1 surface as a platform AI provider, by
// running `pw endpoints run --openai ... -- <forwarder>` around a small
// forwarder to this server. Node ran the forwarder as `node -e FORWARDER`;
// the Go release has no Node, so the forwarder is this binary's own
// subcommand (`studio-server rag-forward`, Forward below), which reads the
// same STUDIO_TARGET_PORT, STUDIO_INJECT_KEY and PORT.
package ragendpoint

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// Subcommand is the forwarder's subcommand name.
const Subcommand = "rag-forward"

// Status is RagEndpointStatus.
type Status struct {
	State, Name, URL, StartedAt, Detail string
}

func (s Status) json() *jsonv.Object {
	n := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	return jsonv.NewObject("state", s.State, "name", n(s.Name), "url", n(s.URL), "startedAt", n(s.StartedAt), "detail", n(s.Detail))
}

// Endpoint is the module state: the child and its status.
type Endpoint struct {
	p        *platform.Platform
	settings *settings.Store
	port     int
	// Self is the forwarder command: this binary and the subcommand.
	Self []string

	mu     sync.Mutex
	child  *exec.Cmd
	status Status
}

// New is the endpoint for a.
func New(a *app.App, p *platform.Platform) *Endpoint {
	self, _ := os.Executable()
	return &Endpoint{p: p, settings: a.Settings, port: a.Cfg.Port, Self: []string{self, Subcommand}, status: Status{State: "stopped"}}
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Name is endpointName.
func (e *Endpoint) Name() string {
	eff := e.settings.Effective()
	if eff.RagEndpointName != "" {
		return eff.RagEndpointName
	}
	app := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(eff.AppName), "-"), "-")
	if app == "" {
		app = "studio"
	}
	n := app + "-rag"
	if len(n) > 40 {
		n = n[:40]
	}
	return n
}

// Status is ragEndpointStatus.
func (e *Endpoint) Status() Status { e.mu.Lock(); defer e.mu.Unlock(); return e.status }

// target is explicitPlatformTarget: PW_CONTEXT as Node's process.env holds
// it (the chosen context or the start one), else PW_PLATFORM_HOST.
func (e *Endpoint) target() (args []string, detail string, ok bool) {
	if ctx := strings.TrimSpace(e.p.PWContext()); ctx != "" {
		return []string{"--context", ctx}, "context " + ctx, true
	}
	if host := strings.TrimSpace(e.p.Getenv("PW_PLATFORM_HOST")); host != "" {
		return nil, "platform " + host, true
	}
	return nil, "", false
}

var urlRe = regexp.MustCompile(`https?://\S+`)
var urlTail = regexp.MustCompile(`[).,]$`)

type tail struct {
	e   *Endpoint
	cmd *exec.Cmd
	mu  sync.Mutex
	buf string
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	t.buf += string(b)
	if len(t.buf) > 2000 {
		t.buf = t.buf[len(t.buf)-2000:]
	}
	text := t.buf
	t.mu.Unlock()
	e := t.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if m := urlRe.FindString(text); m != "" && e.status.State == "starting" {
		e.status.State = "running"
		e.status.URL = urlTail.ReplaceAllString(m, "")
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	e.status.Detail = cut(strings.Join(lines, " | "), 400)
	return len(b), nil
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Start is startRagEndpoint.
func (e *Endpoint) Start() Status {
	e.mu.Lock()
	if e.child != nil {
		st := e.status
		e.mu.Unlock()
		return st
	}
	e.mu.Unlock()
	targs, _, ok := e.target()
	if !ok {
		return e.fail("No explicit platform target: set PW_CONTEXT or PW_PLATFORM_HOST for this deployment, otherwise the proxy would register on whatever platform the host’s pw CLI happens to point at.")
	}
	if !e.settings.Effective().RagProxyEnabled {
		return e.fail("The /v1 surface is disabled in Settings; enable \"Serve the grounded model at /v1\" before registering it on the platform.")
	}
	key := e.p.GatewayKey()
	if key == "" {
		return e.fail("No deployment gateway credential; the registered provider would have nothing to authenticate with.")
	}
	name := e.Name()
	cli := e.p.CLI()
	args := append([]string{"endpoints", "run"}, targs...)
	args = append(args, "--openai", "--name", name, "--subdomain", name, "-o", "text", "--")
	args = append(args, e.Self...)
	cmd := exec.Command(cli, args...)
	cmd.Env = e.p.Environ(map[string]string{"STUDIO_TARGET_PORT": strconv.Itoa(e.port), "STUDIO_INJECT_KEY": key})
	t := &tail{e: e, cmd: cmd}
	cmd.Stdout, cmd.Stderr = t, t
	e.mu.Lock()
	e.status = Status{State: "starting", Name: name, StartedAt: platform.NowISO()}
	e.mu.Unlock()
	if err := cmd.Start(); err != nil {
		// The 'error' event: no child, and the reason in words.
		e.mu.Lock()
		e.status.State, e.status.URL = "error", ""
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) || errors.Is(err, os.ErrNotExist) {
			e.status.Detail = "The pw CLI is not available to this deployment (looked for \"" + cli + "\"), so the model cannot be registered on the platform from here. " +
				"Bind the CLI into the container, or set PW_CLI to its path."
		} else {
			e.status.Detail = "Could not start the endpoint agent: " + err.Error()
		}
		st := e.status
		e.mu.Unlock()
		return st
	}
	e.mu.Lock()
	e.child = cmd
	st := e.status
	e.mu.Unlock()
	go func() {
		err := cmd.Wait()
		code := "0"
		if err != nil {
			var ee *exec.ExitError
			code = "null"
			if errors.As(err, &ee) && ee.ExitCode() >= 0 {
				code = strconv.Itoa(ee.ExitCode())
			}
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.child == cmd {
			e.child = nil
		}
		// As Node: the exit decides the state even after a stop, so a
		// stopped endpoint killed by its signal reads as an error.
		if code == "0" {
			e.status.State = "stopped"
		} else {
			e.status.State = "error"
			e.status.Detail = cut("exited with code "+code+": "+e.status.Detail, 400)
		}
		e.status.URL = ""
	}()
	return st
}

func (e *Endpoint) fail(detail string) Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.State, e.status.Detail = "error", detail
	return e.status
}

// Stop is stopRagEndpoint: SIGTERM, then SIGKILL after five seconds.
func (e *Endpoint) Stop() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.child; c != nil {
		e.child = nil
		c.Process.Signal(syscall.SIGTERM)
		go func() {
			time.Sleep(5 * time.Second)
			c.Process.Kill() // as Node: unconditionally; gone already, it fails quietly
		}()
	}
	e.status.State, e.status.URL = "stopped", ""
	return e.status
}

// running reports whether a child is up (for the stop hook).
func (e *Endpoint) running() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.child != nil }

// MaybeAutoStart is maybeAutoStart.
func (e *Endpoint) MaybeAutoStart(log func(string)) {
	eff := e.settings.Effective()
	if !eff.RagEndpointAutoStart || !eff.RagProxyEnabled {
		return
	}
	if _, detail, ok := e.target(); !ok {
		log("rag endpoint auto-start skipped: no explicit platform target (set PW_CONTEXT or PW_PLATFORM_HOST)")
	} else {
		log("rag endpoint auto-start enabled; registering on " + detail)
	}
	e.Start()
}

// Register serves the three routes, fills the StopRagEndpoint provider,
// auto-starts at start where this process owns the track's jobs, and
// stops the child with the server.
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) *Endpoint {
	e := New(a, p)
	h := func(f func() Status) http.HandlerFunc {
		return platform.Handler(a.Log, func(http.ResponseWriter, *http.Request) (any, error) { return f().json(), nil })
	}
	mux.Handle("GET /api/rag/endpoint", h(e.Status))
	mux.Handle("POST /api/rag/endpoint/start", h(e.Start))
	mux.Handle("POST /api/rag/endpoint/stop", h(e.Stop))
	a.Providers.StopRagEndpoint = func(*http.Request) { e.Stop() }
	if p.OwnsJobs() {
		a.OnStart("rag endpoint: auto-start", func(context.Context) { e.MaybeAutoStart(func(m string) { a.Log.Info(m) }) })
	}
	a.OnStop("rag endpoint", func() int {
		if e.running() {
			e.Stop()
			return 1
		}
		return 0
	})
	return e
}

/* ---- the forwarder ---- */

// hop are the headers net/http manages itself on each side.
var hop = map[string]bool{"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Upgrade": true, "Proxy-Connection": true, "Te": true, "Trailer": true}

// Forwarder is the FORWARDER script: every request on PORT goes to the
// Studio at 127.0.0.1:STUDIO_TARGET_PORT with its headers as they came
// (Host included), plus the deployment key as a bearer and
// x-studio-injected-key: 1 when it carries no authorization; the answer
// streams back with its status and headers, and ends 400 ms after the
// Studio's does, as the script's; a failed request is an empty 502.
func Forwarder(targetPort, injectKey string) http.Handler {
	target := "http://127.0.0.1:" + targetPort
	tr := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConnsPerHost: 64}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		out, err := http.NewRequestWithContext(q.Context(), q.Method, target+q.URL.RequestURI(), q.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for k, vs := range q.Header {
			if !hop[k] {
				out.Header[k] = vs
			}
		}
		out.Host = q.Host
		out.ContentLength = q.ContentLength
		if q.Header.Get("Authorization") == "" && injectKey != "" {
			out.Header.Set("Authorization", "Bearer "+injectKey)
			out.Header.Set("X-Studio-Injected-Key", "1")
		}
		res, err := client.Do(out)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, vs := range res.Header {
			if !hop[k] {
				w.Header()[k] = vs
			}
		}
		w.WriteHeader(res.StatusCode)
		f, _ := w.(http.Flusher)
		buf := make([]byte, 32<<10)
		for {
			n, rerr := res.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if f != nil {
					f.Flush()
				}
			}
			if rerr != nil {
				break
			}
		}
		if rerr := q.Context().Err(); rerr == nil {
			time.Sleep(400 * time.Millisecond)
		}
	})
}

// Forward runs the forwarder until it fails: PORT from the endpoint agent,
// the Studio's port and the key from STUDIO_TARGET_PORT and
// STUDIO_INJECT_KEY, as the script read them.
func Forward(stderr io.Writer) int {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", os.Getenv("PORT")))
	if err != nil {
		io.WriteString(stderr, "rag-forward: "+err.Error()+"\n")
		return 1
	}
	srv := &http.Server{Handler: Forwarder(os.Getenv("STUDIO_TARGET_PORT"), os.Getenv("STUDIO_INJECT_KEY")), ReadHeaderTimeout: 60 * time.Second}
	if err := srv.Serve(ln); err != nil {
		io.WriteString(stderr, "rag-forward: "+err.Error()+"\n")
		return 1
	}
	return 0
}
