package codesessions

import (
	"encoding/json"
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// ClaudeCodeShown is a machine record as the person may use it: where
// Claude Code accounts are off for them (pwcode.ClaudeCode), the machine
// says it does not allow Claude Code, so @parallelworks/code offers no
// Claude models, account chip or account panel for it.
func ClaudeCodeShown(r *http.Request, mach *jsonv.Object) *jsonv.Object {
	if mach != nil && pwcode.ClaudeCodeOff(r.Context()) != "" {
		mach.Set("claudeCodeAllowed", false)
	}
	return mach
}

// claudeCodeListing is the platform's /api/code/sessions answer with each
// machine as ClaudeCodeShown has it.
func claudeCodeListing(r *http.Request, raw []byte) []byte {
	if pwcode.ClaudeCodeOff(r.Context()) == "" {
		return raw
	}
	var body map[string]json.RawMessage
	var machines []map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || json.Unmarshal(body["machines"], &machines) != nil {
		return raw
	}
	for _, m := range machines {
		m["claudeCodeAllowed"] = json.RawMessage("false")
	}
	b, err := json.Marshal(machines)
	if err != nil {
		return raw
	}
	body["machines"] = b
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}
