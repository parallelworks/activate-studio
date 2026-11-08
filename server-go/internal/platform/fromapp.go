package platform

import (
	"net/http"
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
)

// FromApp is the process's Platform for a: its paths, its logger, the Go
// front's authEnabled where the front verifies identity, and the Node
// bridge where there is a Node server.
func FromApp(a *app.App) *Platform {
	cfg := a.Cfg
	var authEnabled func() bool
	if cfg.FrontAuth {
		authEnabled = cfg.AuthEnabled
	} else {
		authEnabled = func() bool { return false }
	}
	p := New(Options{IndexBase: cfg.IndexBase, KBRoot: cfg.KBRoot, Log: a.Log, AuthEnabled: authEnabled})
	p.ownsJobs = cfg.NodeURL == "" || os.Getenv("STUDIO_PLATFORM_JOBS") == "1"
	if a.NodeAPI != nil && !a.NodeFree() {
		p.SetNode(NewNodeBridge(a.NodeAPI))
	}
	return p
}

// OwnsJobs reports whether this process runs the track's background work
// at start (the run watcher's reattach, the fleet, task reattach, the RAG
// endpoint's auto-start): a Go server that started Node, or runs without
// one, does; one in front of a Node server it did not start
// (STUDIO_NODE_URL, the test harness) leaves that Node's own work alone,
// unless STUDIO_PLATFORM_JOBS=1.
func (p *Platform) OwnsJobs() bool { return p.ownsJobs }

// RequireSecret guards a Go route Node calls (/internal/platform/*).
func RequireSecret(r *http.Request) error {
	if !bridge.Authorized(r) {
		return KbError(http.StatusNotFound, "not found")
	}
	return nil
}
