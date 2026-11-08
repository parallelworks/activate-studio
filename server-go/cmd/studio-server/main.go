// Command studio-server is the Go server of ACTIVATE Studio. It listens on
// HOST:PORT as the Node server does, answers the routes ported to Go, serves
// web/dist, and passes every other request to the Node server: one at
// STUDIO_NODE_URL, or one it starts itself with STUDIO_NODE_SPAWN=1.
// See docs/GO-PORT.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/nodeproc"
	"github.com/parallelworks/activate-studio/server-go/internal/server"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("config", "err", err.Error())
		return 2
	}
	var handler slog.Handler = slog.NewJSONHandler(os.Stderr, nil)
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stderr, nil)
	}
	log := slog.New(handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	front, nodeEnv, err := identityFront(cfg, log)
	if err != nil {
		log.Error("auth", "err", err.Error())
		return 2
	}
	if front != nil {
		if front.Handoff != nil {
			defer front.Handoff.Close()
		}
		go front.Run(ctx)
	}

	var node *url.URL
	var proc *nodeproc.Proc
	switch {
	case cfg.NodeURL != "":
		if node, err = url.Parse(cfg.NodeURL); err != nil || node.Host == "" {
			log.Error("STUDIO_NODE_URL is not a URL", "value", cfg.NodeURL)
			return 2
		}
	case cfg.NodeSpawn:
		if proc, err = nodeproc.Start(ctx, log, cfg.NodeBin, cfg.NodeMain, 90*time.Second, nodeEnv); err != nil {
			log.Error("node", "err", err.Error())
			return 1
		}
		node = proc.URL
	default:
		log.Warn("no Node server (STUDIO_NODE_URL or STUDIO_NODE_SPAWN=1): only Go routes and the web app answer")
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           server.New(cfg, log, node, front),
		ReadHeaderTimeout: 30 * time.Second,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Error("listen", "addr", srv.Addr, "err", err.Error())
		if proc != nil {
			proc.Stop(25 * time.Second)
		}
		return 1
	}
	log.Info("listening", "addr", ln.Addr().String(), "web", cfg.WebDist, "node", nodeString(node),
		"kbRoot", cfg.KBRoot, "indexBase", cfg.IndexBase, "gufi", cfg.GufiAvailable())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	var nodeDone <-chan struct{}
	if proc != nil {
		nodeDone = proc.Done()
	}
	code := 0
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err.Error())
			code = 1
		}
	case <-nodeDone:
		// Node stopped on its own (a crash, or exit 76 for a relaunch after
		// an update): the Go server stops with its code, so whatever
		// supervises the Studio restarts both.
		code = proc.ExitCode()
		log.Warn("node exited", "code", code)
	}
	// Node first: it lets open answers finish (up to 20 s), and they reach
	// the browser through this server, which is still serving.
	if proc != nil {
		proc.Stop(25 * time.Second)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Warn("shutdown", "err", err.Error())
	}
	return code
}

// identityFront is the Go front's identity handling (docs/GO-PORT.md,
// Stage 1) and the environment Node needs to accept its hand-off: nil and
// no change to Node's environment with STUDIO_FRONT_AUTH off, where
// identity headers pass through to Node as before.
func identityFront(cfg config.Config, log *slog.Logger) (*auth.Front, map[string]string, error) {
	if !cfg.FrontAuth {
		if cfg.OIDC.Enabled() {
			log.Warn("OIDC_ISSUER is set but the Go server does not handle sign-in for a Node server it did not start; set STUDIO_FRONT_AUTH=1 and start Node with the AUTH_* the Go server logs")
		}
		return nil, nil, nil
	}
	// Node gets none of the OIDC settings: the Go front signs people in.
	env := map[string]string{"AUTH_JWKS_URL": "", "AUTH_HEADER": "", "AUTH_ISSUER": "", "AUTH_AUDIENCE": "", "AUTH_REQUIRED": "", "OIDC_REQUIRED": ""}
	for _, k := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URI", "OIDC_POST_LOGOUT_REDIRECT_URI", "OIDC_SCOPES", "OIDC_USERNAME_CLAIM", "OIDC_END_SESSION", "OIDC_SESSION_MAX_AGE_SEC"} {
		env[k] = ""
	}
	var store *auth.SessionStore
	if cfg.OIDC.Enabled() {
		var err error
		if store, err = auth.NewSessionStore(cfg.IndexBase); err != nil {
			return nil, nil, err
		}
	}
	var h *auth.Handoff
	if cfg.AuthEnabled() {
		var err error
		if h, err = auth.StartHandoff(cfg.IdentityAddr); err != nil {
			return nil, nil, err
		}
		for k, v := range h.NodeEnv(cfg.Auth.Required) {
			env[k] = v
		}
	}
	front := auth.NewFront(cfg, log, store, h)
	what := []string{}
	if cfg.Auth.JWKSURL != "" {
		what = append(what, "session proxy JWT verification")
	}
	if cfg.OIDC.Enabled() {
		what = append(what, "OIDC sign-in ("+cfg.OIDC.Issuer+")")
	}
	if len(what) == 0 {
		what = append(what, "open (no AUTH_JWKS_URL or OIDC_ISSUER)")
	}
	attrs := []any{"mode", strings.Join(what, " and "), "required", cfg.Auth.Required}
	if h != nil {
		attrs = append(attrs, "nodeJwks", h.JWKSURL)
	}
	log.Info("auth", attrs...)
	return front, env, nil
}

func nodeString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
