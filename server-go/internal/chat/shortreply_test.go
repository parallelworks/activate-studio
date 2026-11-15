package chat

import (
	"strings"
	"testing"
)

// A short reply after reading tools is taken for a stub and a final answer
// is asked for. After a call that changed something it is a confirmation,
// and stands.
func TestShortReplyAfterAChangeStands(t *testing.T) {
	for _, c := range []struct {
		tool   string
		forced bool
	}{
		{"list_kb_dir", true},
		{"copy_kb_files", false},
		{"transfer_files", false},
	} {
		t.Run(c.tool, func(t *testing.T) {
			_, srv, gw, _ := newTestModule(t, nil)
			gw.load([]reply{call(c.tool, `{}`), text("Done."), text("The final answer.")})
			sse := post(t, srv.URL, map[string]any{"model": "test-model", "conversationId": "c-" + c.tool, "userMessageId": "u1",
				"messages": []any{map[string]any{"role": "user", "content": "do it"}}})
			n := 2
			if c.forced {
				n = 3
			}
			if len(gw.requests) != n || strings.Contains(sse, "The final answer.") != c.forced || !strings.Contains(sse, `"content":"Done.`) {
				t.Errorf("%d model requests, stream %q", len(gw.requests), sse)
			}
		})
	}
}
