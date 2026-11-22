package agentsites

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// With sign-in on (and AGENT_LOCAL_SITE=1), every session on this host's
// daemon runs as the Studio's account, so a person reaches only the ones
// the Studio started for them: their chats, delegations and standing
// agents (Owned, the Code page's rule). Another person's is answered as
// not there, and a session is started here only by delegating, which
// records it as theirs.

// ownHere is the viewer's sessions on this host, and whether site is
// this host with sign-in on, where only those are theirs.
func (s *Sites) ownHere(r *http.Request, site string, authEnabled bool) (map[string]bool, bool) {
	if !authEnabled {
		return nil, false
	}
	if v, err := s.CheckSite(site, authEnabled); err != nil || v != Local {
		return nil, false
	}
	if s.Owned == nil {
		return map[string]bool{}, true
	}
	return s.Owned(r), true
}

// Mine refuses session id on site when it is on this host and not the
// viewer's.
func (s *Sites) Mine(r *http.Request, site, id string, authEnabled bool) error {
	if own, limited := s.ownHere(r, site, authEnabled); limited && !own[id] {
		return kb(404, "That session is not one of yours on this computer.")
	}
	return nil
}

// MineOnly is rows (Sessions) less the sessions on this host that are not
// the viewer's.
func (s *Sites) MineOnly(r *http.Request, site string, rows []any, authEnabled bool) []any {
	own, limited := s.ownHere(r, site, authEnabled)
	if !limited {
		return rows
	}
	out := []any{}
	for _, x := range rows {
		if own[jsonv.String(jsonv.Path(x, "id"))] {
			out = append(out, x)
		}
	}
	return out
}

// MayStart refuses a session started directly on this host with sign-in
// on: it would run as the Studio's account and be nobody's.
func (s *Sites) MayStart(site string, authEnabled bool) error {
	if !authEnabled {
		return nil
	}
	if v, err := s.CheckSite(site, authEnabled); err == nil && v == Local {
		return kb(403, "On a Studio with sign-in, a session on this computer is started by delegating the work, which keeps it as yours.")
	}
	return nil
}
