package pwcode

import "strings"

// The requests a Studio page may send a pw code session it shows, by
// method and the route's suffix after /v1/sessions/{id} (the platform's
// /api/code/machines/{machine}/sessions/{id}): what @parallelworks/code's
// session view calls to read the session, follow it, talk to it, answer it
// and set its mode, model and effort. Everything else the platform relays,
// such as archiving, deleting, forking or clearing the session, running a
// shell command or a slash command in it, or changing its worktree, is
// refused: the Studio shows a session, it does not manage it.
var proxyRoutes = map[string]bool{
	"GET ":                       true,
	"GET /transcript":            true,
	"GET /palette":               true,
	"GET /subagents":             true,
	"POST /messages":             true,
	"POST /direction":            true,
	"POST /direction/withdraw":   true,
	"POST /interrupt":            true,
	"POST /stop":                 true,
	"POST /texts":                true,
	"POST /approvals/{approval}": true,
	"DELETE /queued/{turn}":      true,
	"PATCH /settings":            true,
}

// ProxyRoute is the allowlist key for a request with method and suffix (the
// route after /v1/sessions/{id}, without its query), or "" for one the
// Studio does not send. One id in a route (an approval's, a queued turn's)
// must be a single path segment.
func ProxyRoute(method, suffix string) string {
	base := suffix
	for prefix, name := range map[string]string{"/approvals/": "{approval}", "/queued/": "{turn}"} {
		if rest, ok := strings.CutPrefix(suffix, prefix); ok {
			if rest == "" || strings.ContainsAny(rest, "/?#") {
				return ""
			}
			base = prefix + name
		}
	}
	key := method + " " + base
	if !proxyRoutes[key] {
		return ""
	}
	return key
}
