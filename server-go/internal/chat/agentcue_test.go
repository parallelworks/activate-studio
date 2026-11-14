package chat

import "testing"

// Asking for a code session, by any of its names, brings the agent tools
// into the reply.
func TestAgentCue(t *testing.T) {
	for _, q := range []string{"start a pw code session on the cluster", "open a code session there", "start a coding session on gpu1", "use pw-code for this", "run pwcode on it", "hand this to an agent"} {
		if !initialToolGroups(q, nil)["agents"] {
			t.Errorf("%q does not bring the agent tools", q)
		}
	}
	if initialToolGroups("what is the zip code for the session venue", nil)["agents"] {
		t.Error("an unrelated question brings the agent tools")
	}
}
