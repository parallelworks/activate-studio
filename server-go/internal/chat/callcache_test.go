package chat

import (
	"sync"
	"testing"
)

// TestRepeatedCalls keeps a read's result for the rest of a reply, looks
// again at anything that changes by itself, and reads again after a call
// that changed something.
func TestRepeatedCalls(t *testing.T) {
	cache, mu := map[string]string{}, &sync.Mutex{}
	read := `{"path":"reports/survey.md"}`
	rememberCall(cache, mu, "read_kb_file", read, "v1")
	if hit, ok := repeatedCall(cache, mu, "read_kb_file", read); !ok || hit != "v1" {
		t.Errorf("a repeated read: %q %v", hit, ok)
	}
	if _, ok := repeatedCall(cache, mu, "read_kb_file", `{"path":"reports/other.md"}`); ok {
		t.Errorf("another file was answered from the first")
	}
	// A status is a second look, never the first one repeated.
	rememberCall(cache, mu, "agent_status", `{"session":"s"}`, "running")
	if _, ok := repeatedCall(cache, mu, "agent_status", `{"session":"s"}`); ok {
		t.Errorf("an agent's status was repeated from before")
	}
	// After an edit the read is made again; the edit itself is not.
	rememberCall(cache, mu, "edit_kb_file", `{"path":"reports/survey.md"}`, "edited")
	if _, ok := repeatedCall(cache, mu, "read_kb_file", read); ok {
		t.Errorf("a read was repeated from before the edit")
	}
	if hit, ok := repeatedCall(cache, mu, "edit_kb_file", `{"path":"reports/survey.md"}`); !ok || hit != "edited" {
		t.Errorf("the edit made twice: %q %v", hit, ok)
	}
	for name := range liveTools {
		if changingTools[name] {
			t.Errorf("%s both changes and only reads", name)
		}
	}
	for _, set := range []map[string]bool{liveTools, changingTools} {
		for name := range set {
			if !isBuiltin(name) && !isPlatformTool(name) {
				t.Errorf("%s is not a tool", name)
			}
		}
	}
}
