package server

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// registerTracks registers each track's routes on mux (the ported-routes
// mux: a route registered here is served by Go, never by Node) and its
// start-up hooks and background jobs on a. One call per track, each in its
// own slot below, so the tracks' merges touch different lines:
//
//	<pkg>.Register(mux, a)
//
// Replace your track's "slot" comment with the call; keep the blank lines.
func registerTracks(mux *http.ServeMux, a *app.App) {
	_, _ = mux, a

	// slot: core (port-core)

	// slot: index (port-index)

	// slot: files (port-files)

	// slot: libraries (port-libraries)

	// slot: platform (port-platform)

	// slot: updates (port-updates)

	// slot: chat (port-chat)

	// slot: other
}
