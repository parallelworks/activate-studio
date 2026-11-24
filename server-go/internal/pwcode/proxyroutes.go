package pwcode

import (
	"regexp"
	"strings"
)

// The requests a Studio page may send a pw code session it shows, by
// method and the route's suffix after /v1/sessions/{id} (the platform's
// /api/code/machines/{machine}/sessions/{id}): what @parallelworks/code's
// session view calls, as PW Code on the web's session page calls it, and
// what the platform relays. That is reading the session and following it,
// talking to it and its subagents, answering it, its settings, its goal,
// slash and shell commands, images and pastes, moving a command to the
// background, asking for a recap, and the Claude Code accounts the session
// may use. Managing the session itself (renaming, archiving, pinning,
// grouping, forking or deleting it, or leaving its worktree) is refused:
// that is the Code page's.
var proxyRoutes = map[string]bool{
	"GET ":                       true,
	"GET /transcript":            true,
	"GET /palette":               true,
	"GET /subagents":             true,
	"GET /models":                true,
	"GET /worktree":              true,
	"GET /pull-requests":         true,
	"GET /claude-code/accounts":  true,
	"GET /images/{ref}":          true,
	"GET /github-images/{asset}": true,
	"POST /messages":             true,
	"POST /direction":            true,
	"POST /direction/withdraw":   true,
	"POST /interrupt":            true,
	"POST /stop":                 true,
	"POST /texts":                true,
	"POST /images":               true,
	"POST /approvals/{approval}": true,
	"POST /background":           true,
	"POST /recap":                true,
	"POST /goal":                 true,
	"POST /clear":                true,
	"POST /compact":              true,
	"POST /plan":                 true,
	"POST /shell":                true,
	"POST /commands":             true,
	"DELETE /queued/{turn}":      true,
	"PATCH /settings":            true,

	"GET /subagents/{sub}":                     true,
	"POST /subagents/{sub}/messages":           true,
	"POST /subagents/{sub}/direction":          true,
	"POST /subagents/{sub}/direction/withdraw": true,
	"POST /subagents/{sub}/stop":               true,
	"POST /subagents/{sub}/dismiss":            true,
	"POST /subagents/stop-all":                 true,
}

// The routes with an id in them, by the prefix before the id; the id is
// one segment, and what follows it, if anything, is the rest of the route.
var idPrefixes = []struct{ prefix, name string }{
	{"/approvals/", "{approval}"},
	{"/queued/", "{turn}"},
	{"/images/", "{ref}"},
	{"/github-images/", "{asset}"},
	{"/subagents/", "{sub}"},
}

// routeID is what an id in a route (an approval's, a queued turn's) may be,
// as the Studio's own approvals route takes it: no dot segment, encoded or
// not, no ';', space or '%', so the route sent on is the one checked here
// whatever a hop in front of the session makes of it.
var routeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// IsRouteID reports whether id may be put in a route as one segment.
func IsRouteID(id string) bool { return routeID.MatchString(id) }

// ProxyRoute is the allowlist key for a request with method and suffix (the
// route after /v1/sessions/{id}, without its query), or "" for one the
// Studio does not send. One id in a route must be a single segment that
// routeID takes.
func ProxyRoute(method, suffix string) string {
	if key := method + " " + suffix; proxyRoutes[key] && !strings.Contains(suffix, "{") {
		return key
	}
	for _, p := range idPrefixes {
		rest, ok := strings.CutPrefix(suffix, p.prefix)
		if !ok {
			continue
		}
		id, tail, _ := strings.Cut(rest, "/")
		if tail != "" {
			tail = "/" + tail
		}
		if !routeID.MatchString(id) {
			return ""
		}
		key := method + " " + p.prefix + p.name + tail
		if !proxyRoutes[key] {
			return ""
		}
		return key
	}
	return ""
}

// The requests a Studio page may send a machine's pw code about the Claude
// Code accounts its sessions use, by method and the route after
// /v1/claude-code (the platform's /api/code/machines/{machine}/claude-code):
// what the account panel calls to list the accounts and their models, sign
// one in, remove one, or move its sessions to another.
var machineRoutes = map[string]bool{
	"GET /accounts":              true,
	"GET /models":                true,
	"DELETE /accounts/{name}":    true,
	"POST /accounts/{name}/move": true,
	"POST /logins":               true,
	"GET /logins/{login}":        true,
	"POST /logins/{login}/input": true,
	"DELETE /logins/{login}":     true,
}

// MachineRoute is the allowlist key for a machine request with method and
// suffix (the route after /v1/claude-code, without its query), or "".
func MachineRoute(method, suffix string) string {
	if key := method + " " + suffix; machineRoutes[key] && !strings.Contains(suffix, "{") {
		return key
	}
	for _, p := range []struct{ prefix, name string }{{"/accounts/", "{name}"}, {"/logins/", "{login}"}} {
		rest, ok := strings.CutPrefix(suffix, p.prefix)
		if !ok {
			continue
		}
		id, tail, _ := strings.Cut(rest, "/")
		if tail != "" {
			tail = "/" + tail
		}
		if !routeID.MatchString(id) {
			return ""
		}
		key := method + " " + p.prefix + p.name + tail
		if !machineRoutes[key] {
			return ""
		}
		return key
	}
	return ""
}
