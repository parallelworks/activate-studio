package pwcode

import "strings"

// The requests the Studio's Code page may send about one of the person's
// sessions, by method and the route's suffix after /sessions/{id}: what
// PW Code on the web's session list and menu do with a session (rename,
// archive and restore, pin, move to a group, stop, fork, delete, leave its
// worktree) and the reads they need, and an answer to what it waits on.
// Each goes to the platform as the person, which relays it to the
// session's machine, or to this computer's own pw code. Talking to the
// session itself is not here: its view does that (ProxyRoute).
var pageRoutes = map[string]bool{
	"GET ":                       true,
	"GET /worktree":              true,
	"GET /pull-requests":         true,
	"POST /rename":               true,
	"POST /archive":              true,
	"DELETE /archive":            true,
	"POST /pin":                  true,
	"DELETE /pin":                true,
	"PUT /group":                 true,
	"DELETE /group":              true,
	"POST /stop":                 true,
	"POST /fork":                 true,
	"DELETE ":                    true,
	"POST /worktree/leave":       true,
	"POST /approvals/{approval}": true,
}

// PageRoute is the Code page's allowlist key for a request with method and
// suffix, or "" for one the Studio does not send; an id in the route is
// held to routeID as in ProxyRoute.
func PageRoute(method, suffix string) string {
	base := suffix
	if rest, ok := strings.CutPrefix(suffix, "/approvals/"); ok {
		if !routeID.MatchString(rest) {
			return ""
		}
		base = "/approvals/{approval}"
	}
	key := method + " " + base
	if !pageRoutes[key] {
		return ""
	}
	return key
}
