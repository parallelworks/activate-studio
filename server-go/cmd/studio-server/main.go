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
	"syscall"
	"time"

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

	var node *url.URL
	var proc *nodeproc.Proc
	switch {
	case cfg.NodeURL != "":
		if node, err = url.Parse(cfg.NodeURL); err != nil || node.Host == "" {
			log.Error("STUDIO_NODE_URL is not a URL", "value", cfg.NodeURL)
			return 2
		}
	case cfg.NodeSpawn:
		if proc, err = nodeproc.Start(ctx, log, cfg.NodeBin, cfg.NodeMain, 90*time.Second); err != nil {
			log.Error("node", "err", err.Error())
			return 1
		}
		node = proc.URL
	default:
		log.Warn("no Node server (STUDIO_NODE_URL or STUDIO_NODE_SPAWN=1): only Go routes and the web app answer")
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           server.New(cfg, log, node),
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

func nodeString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
