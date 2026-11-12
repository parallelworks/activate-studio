package chat

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// Reasoning effort, chosen in the composer for each turn. It goes to the
// provider as the OpenAI-style reasoning_effort, which the ACTIVATE gateway
// takes and checks (none, minimal, low, medium, high, xhigh, max). A model
// whose chatTemplateKwargs entry already sets its reasoning gets the choice
// there instead: gpt-oss's reasoning_effort takes it (capped at high), and
// Qwen's enable_thinking is off for low and minimal and on above them.

var efforts = map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// effortOf is the request's effort when it is one the gateway takes, else "".
func effortOf(v *string) string {
	if v == nil {
		return ""
	}
	e := strings.ToLower(strings.TrimSpace(*v))
	if efforts[e] {
		return e
	}
	return ""
}

// withEffort is the chat_template_kwargs to send and the top-level
// reasoning_effort, given the model's kwargs entry and the turn's effort.
func withEffort(kwargs json.RawMessage, effort string) (json.RawMessage, string) {
	if effort == "" {
		return kwargs, ""
	}
	if kwargs != nil {
		var o map[string]json.RawMessage
		if json.Unmarshal(kwargs, &o) == nil {
			if _, ok := o["reasoning_effort"]; ok {
				v := effort
				switch effort {
				case "minimal":
					v = "low"
				case "xhigh", "max":
					v = "high"
				}
				o["reasoning_effort"], _ = json.Marshal(v)
				b, _ := json.Marshal(o)
				return b, ""
			}
			if _, ok := o["enable_thinking"]; ok {
				o["enable_thinking"], _ = json.Marshal(effort != "low" && effort != "minimal")
				b, _ := json.Marshal(o)
				return b, ""
			}
		}
	}
	return kwargs, effort
}

// rejectedEffortRe matches a provider's refusal of the parameter itself
// ("Unrecognized request argument supplied: reasoning_effort") or of the
// value ("Invalid reasoning effort").
var rejectedEffortRe = regexp.MustCompile(`(?i)reasoning[_ ]effort|reasoning`)

func rejectedEffort(errText string) bool {
	return rejectedEffortRe.MatchString(errText) && unsupportedRe.MatchString(errText)
}

// noEffortModels are models whose provider refused an effort since start.
var noEffortModels sync.Map

func (m *Module) noEffort(model string) bool {
	_, ok := noEffortModels.Load(model)
	return ok
}

func (m *Module) rememberNoEffort(model string) { noEffortModels.Store(model, true) }
