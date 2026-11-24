package chat

import (
	"strings"
	"testing"
)

// A provider's failure is told with the model's own name and the
// provider's own words, not the gateway's status line and JSON body.
func TestAProvidersFailureReadsInPlainWords(t *testing.T) {
	got := GatewayChatMessage(`gateway chat 502: {"error":{"message":"The AI provider could not be reached","type":"error","code":"server_error"}}`, "someone:codex/gpt-a")
	want := "gpt-a cannot be called right now: its provider returned an error. Pick another model from the list while it recovers. Other models are unaffected. Provider said: The AI provider could not be reached"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	for _, c := range []struct{ msg, said string }{
		{`gateway chat 503: {"message":"Overloaded"}`, "Overloaded"},
		{`gateway chat 500: {"error":"upstream timed out"}`, "upstream timed out"},
		{`gateway chat 502: Bad Gateway`, "Bad Gateway"},
		{`connection reset by peer`, "connection reset by peer"},
	} {
		if got := GatewayChatMessage(c.msg, "gpt-a"); !strings.HasSuffix(got, "Provider said: "+c.said) {
			t.Errorf("%s: %q", c.msg, got)
		}
	}
	if got := GatewayChatMessage("gateway chat 502: x", "the model"); !strings.HasPrefix(got, "the model cannot") {
		t.Errorf("no model: %q", got)
	}
}
