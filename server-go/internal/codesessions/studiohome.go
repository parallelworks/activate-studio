package codesessions

import (
	"encoding/json"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// StudioHome marks, in a pw code machine listing, the machine where this
// Studio runs pw code for the person: the cluster it runs beside under
// sign-in, where their sessions run as them, or this computer without
// sign-in. The pages name it as this Studio's, list it first and start on it
// by default.
const StudioHome = "studioHome"

// NoHomeCluster is why a Studio with sign-in has no machine of its own for
// the person's pw code.
const NoHomeCluster = "This Studio does not know the cluster it runs on, so it cannot start pw code here. Its operator sets STUDIO_HOME_CLUSTER."

// IsHomeCluster is whether the platform's machine (its kind, name and
// owner) is the cluster home names (pw://owner/name, or a bare name).
func IsHomeCluster(home, kind, name, owner string) bool {
	if home == "" || name == "" || kind == "local" || kind == "workspace" || kind == "pod" {
		return false
	}
	who, at := "", home
	if rest, ok := strings.CutPrefix(home, "pw://"); ok {
		who, at, _ = strings.Cut(rest, "/")
	}
	return strings.EqualFold(at, name) && (who == "" || owner == "" || strings.EqualFold(who, owner))
}

// studioHomeListing is the platform's machine listing raw with this
// Studio's machine marked (StudioHome) and put first. With sign-in on and
// no home cluster known, an entry in its place says why nothing starts
// there.
func studioHomeListing(raw []byte, signIn bool) []byte {
	var body map[string]json.RawMessage
	var machines []map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || json.Unmarshal(body["machines"], &machines) != nil {
		return raw
	}
	home := libraries.HomeCluster()
	str := func(m map[string]json.RawMessage, k string) string {
		var s string
		_ = json.Unmarshal(m[k], &s)
		return s
	}
	out := make([]map[string]json.RawMessage, 0, len(machines)+1)
	found := false
	for _, m := range machines {
		if !found && IsHomeCluster(home, str(m, "kind"), str(m, "name"), str(m, "owner")) {
			m[StudioHome] = json.RawMessage("true")
			out = append([]map[string]json.RawMessage{m}, out...)
			found = true
			continue
		}
		out = append(out, m)
	}
	if signIn && home == "" {
		why, _ := json.Marshal(NoHomeCluster)
		out = append([]map[string]json.RawMessage{{
			"id": json.RawMessage(`"studio"`), "name": json.RawMessage(`"This Studio"`), "kind": json.RawMessage(`"existing"`),
			"owner": json.RawMessage(`""`), "status": json.RawMessage(`"unreachable"`), "error": why, StudioHome: json.RawMessage("true"),
		}}, out...)
	} else if !found {
		return raw
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	body["machines"] = b
	if res, err := json.Marshal(body); err == nil {
		return res
	}
	return raw
}
