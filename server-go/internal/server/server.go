// Package server is the Go front of the Studio: it answers the routes
// ported to Go, serves the built web app, and passes every other request to
// the Node server, so routes move one at a time and the app works at every
// step (docs/GO-PORT.md).
package server

import (
	"encoding/json"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// nodeOnly are the Node server's routes outside /api/. They are passed to
// Node even where web/dist holds a file of the same name, since Node's
// routes win over its static files. Every other path outside /api/ is the
// web app's.
var nodeOnly = []string{"/v1/", "/internal/", "/manifest.webmanifest"}

// Server routes each request to a Go handler, the web app, or Node.
type Server struct {
	cfg    config.Config
	log    *slog.Logger
	ported *http.ServeMux
	node   *httputil.ReverseProxy // nil without a Node server
	static *compressCache
	// front verifies identity and hands it to Node (docs/GO-PORT.md,
	// Stage 1); nil passes identity headers through to Node unchanged.
	front *auth.Front
	// App is the process every module shares (internal/app).
	App *app.App
	// unported answers 501 for a Node route no Go package serves, in
	// Node-free mode (cfg.NodeOff).
	unported *http.ServeMux
}

// New makes the handler with a new App. node is the Node server's base
// URL, or nil; front is the identity front, or nil.
func New(cfg config.Config, log *slog.Logger, node *url.URL, front *auth.Front) *Server {
	var n *app.Node
	if node != nil {
		n = app.NewNode(node, front, cfg.Auth.Header)
	}
	return NewWithApp(cfg, log, app.New(cfg, log, nil, n), node, front)
}

// AttachNode points the server at Node once it is up: unported routes go
// to it, and App.Node() and the App's value calls reach it. main.go builds
// the server and the tracks' routes first, so every module's Node
// environment (bridge.RegisterNodeEnv) is in before Node starts. Call it
// before serving.
func (s *Server) AttachNode(node *url.URL) {
	if node == nil {
		return
	}
	s.node = newProxy(node, s.log, s.front)
	s.App.SetNodeHandler(s.node)
	s.App.AttachNode(app.NewNode(node, s.front, s.cfg.Auth.Header))
}

// NewWithApp makes the handler for an App made by the caller (main.go).
func NewWithApp(cfg config.Config, log *slog.Logger, a *app.App, node *url.URL, front *auth.Front) *Server {
	s := &Server{cfg: cfg, log: log, ported: http.NewServeMux(), static: newCompressCache(), front: front, App: a}
	go s.static.warm(cfg.WebDist)
	if node != nil {
		s.node = newProxy(node, log, front)
		a.SetNodeHandler(s.node)
	}
	s.routes()
	s.unported = s.unportedMux()
	return s
}

// routes registers the routes ported to Go. Each is listed in
// docs/GO-PORT.md; a route registered here is no longer reached on Node.
func (s *Server) routes() {
	s.ported.HandleFunc("GET /healthz", s.healthz)
	if s.front != nil {
		// /api/auth/* (with OIDC) and /api/whoami, which reports what the
		// front verified.
		s.front.Routes(s.ported)
	}
	registerTracks(s.ported, s.App)
}

// Unported is every Node route no Go package serves, as "METHOD pattern".
func (s *Server) Unported() []string {
	var out []string
	for _, r := range nodeRoutes {
		if !s.Serves(r.method, r.pattern) {
			out = append(out, r.method+" "+r.pattern)
		}
	}
	return out
}

// Ported is every Node route a Go package serves, as "METHOD pattern".
func (s *Server) Ported() []string {
	var out []string
	for _, r := range nodeRoutes {
		if s.Serves(r.method, r.pattern) {
			out = append(out, r.method+" "+r.pattern)
		}
	}
	return out
}

// Serves reports whether a Go handler answers method on a path the
// pattern stands for (each {name} filled with a sample value).
func (s *Server) Serves(method, pattern string) bool {
	p := samplePath(pattern)
	r, err := http.NewRequest(method, p, nil)
	if err != nil {
		return false
	}
	_, got := s.ported.Handler(r)
	return got != ""
}

// samplePath fills a pattern's wildcards: {rest...} with a/b, {x} with x1.
func samplePath(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			b.WriteByte(pattern[i])
			continue
		}
		j := strings.IndexByte(pattern[i:], '}')
		name := pattern[i+1 : i+j]
		if strings.HasSuffix(name, "...") {
			b.WriteString("a/b")
		} else {
			b.WriteString(name + "1")
		}
		i += j
	}
	return b.String()
}

// unportedMux answers 501 for the Node routes no Go package serves, in
// Node-free mode; nil otherwise.
func (s *Server) unportedMux() *http.ServeMux {
	if !s.cfg.NodeOff {
		return nil
	}
	m := http.NewServeMux()
	seen := map[string]bool{}
	for _, r := range nodeRoutes {
		key := r.method + " " + r.pattern
		if seen[key] || s.Serves(r.method, r.pattern) {
			continue
		}
		seen[key] = true
		file := r.file
		m.HandleFunc(key, func(w http.ResponseWriter, req *http.Request) {
			s.log.Warn("unported route", "method", req.Method, "path", req.URL.Path, "route", key, "node", file)
			apierr.JSON(w, http.StatusNotImplemented, map[string]string{"error": key + " is not ported to the Go server yet (" + file + "), and this server runs without Node (STUDIO_NODE=off)"})
		})
	}
	return m
}

// healthz answers as routes.ts does: {ok, gufi}.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gufi": s.cfg.GufiAvailable()})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	// The proxy ends a response whose client went away (Stop in a chat
	// stream, a closed page) by panicking with http.ErrAbortHandler, which
	// net/http swallows; the request is logged all the same, then the
	// panic goes on.
	via := "node"
	defer func() {
		p := recover()
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"bytes", rec.bytes, "ms", time.Since(start).Milliseconds(), "via", via}
		if p != nil {
			attrs = append(attrs, "aborted", true)
		}
		s.log.Info("request", attrs...)
		if p != nil {
			panic(p)
		}
	}()
	// Bridge-only paths (the transitional Go and Node channel) answer 404
	// to anything without the bridge secret, before sign-in, so a browser
	// cannot reach them through the front.
	if bridge.IsPrivate(r.URL.Path) && !bridge.Authorized(r) {
		via = "bridge-refused"
		http.NotFound(rec, r)
		return
	}
	if s.front != nil {
		gated := s.front.Gate(rec, r)
		if gated == nil {
			via = "auth"
			return
		}
		r = gated
	}
	via = s.dispatch(rec, r)
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) string {
	if _, pattern := s.ported.Handler(r); pattern != "" {
		s.ported.ServeHTTP(w, r)
		return "go"
	}
	if s.unported != nil {
		if _, pattern := s.unported.Handler(r); pattern != "" {
			s.unported.ServeHTTP(w, r)
			return "unported"
		}
	}
	p := r.URL.Path
	toNode := strings.HasPrefix(p, "/api/") || p == "/api"
	for _, pre := range nodeOnly {
		if strings.HasPrefix(p, pre) || p == strings.TrimSuffix(pre, "/") {
			toNode = true
		}
	}
	if toNode {
		if s.node == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return "none"
		}
		s.node.ServeHTTP(w, r)
		return "node"
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if s.node != nil {
			s.node.ServeHTTP(w, r)
			return "node"
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return "none"
	}
	if serveStatic(w, r, s.cfg.WebDist, s.static) {
		return "static"
	}
	// Without a built web app Node answers as it would (Fastify's 404).
	if s.node != nil {
		s.node.ServeHTTP(w, r)
		return "node"
	}
	http.NotFound(w, r)
	return "none"
}

func newProxy(target *url.URL, log *slog.Logger, front *auth.Front) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Node sees the host the browser asked for, as it does today.
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			// Only the Go server's own value calls (app.Node.Do, which go
			// to Node directly) and marked hand-offs carry this; a
			// client's is dropped.
			pr.Out.Header.Del(app.InternalHeader)
			// A Go route's deliberate hand-off (app.HandOff) is marked.
			if _, ok := app.IsHandOff(pr.In); ok {
				pr.Out.Header.Set(app.InternalHeader, "1")
			}
			if front != nil {
				front.ToNode(pr.Out, auth.UserOf(pr.In))
			}
			app.RunToNode(pr.In, pr.Out)
		},
		// 0 flushes only what streams: ReverseProxy writes a response out
		// as Node sends it when it is text/event-stream (/api/chat/stream,
		// /v1/chat/completions) or has no Content-Length (the session relay,
		// chunked downloads). A response of known length, every JSON answer,
		// goes out when it ends, in place of a flush (a write to the socket)
		// for its header and each read from Node.
		FlushInterval: 0,
		BufferPool:    copyBuffers{},
		Transport:     nodeTransport(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("node unreachable", "path", r.URL.Path, "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the Node server is not answering"})
		},
	}
}

// nodeTransport is how the proxy reaches Node. http.DefaultTransport keeps
// two idle connections per host, so a burst of requests (the page's polls,
// searches across libraries, a chat) opened a new connection to Node for
// nearly every request and closed it after; this keeps enough for a burst.
// Idle connections are dropped before Node's keep-alive timeout (72 s,
// main.ts) would close them under a request.
func nodeTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 512
	t.MaxIdleConnsPerHost = 256
	t.IdleConnTimeout = 60 * time.Second
	return t
}

// copyBuffers reuses ReverseProxy's 32 KB copy buffers, which it would
// otherwise allocate for every proxied request.
type copyBuffers struct{}

var copyBufferPool = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}

func (copyBuffers) Get() []byte  { return *copyBufferPool.Get().(*[]byte) }
func (copyBuffers) Put(b []byte) { copyBufferPool.Put(&b) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// recorder notes the status and size of a response for the access log and
// passes Flush through, which the proxy needs for event streams.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
