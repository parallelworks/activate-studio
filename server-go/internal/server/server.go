// Package server is the Go front of the Studio: it answers the routes
// ported to Go, serves the built web app, and passes every other request to
// the Node server, so routes move one at a time and the app works at every
// step (docs/GO-PORT.md).
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

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
}

// New makes the handler. node is the Node server's base URL, or nil.
func New(cfg config.Config, log *slog.Logger, node *url.URL) *Server {
	s := &Server{cfg: cfg, log: log, ported: http.NewServeMux()}
	if node != nil {
		s.node = newProxy(node, log)
	}
	s.routes()
	return s
}

// routes registers the routes ported to Go. Each is listed in
// docs/GO-PORT.md; a route registered here is no longer reached on Node.
func (s *Server) routes() {
	s.ported.HandleFunc("GET /healthz", s.healthz)
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
	via = s.dispatch(rec, r)
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) string {
	if _, pattern := s.ported.Handler(r); pattern != "" {
		s.ported.ServeHTTP(w, r)
		return "go"
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
	if serveStatic(w, r, s.cfg.WebDist) {
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

func newProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Node sees the host the browser asked for, as it does today.
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
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
