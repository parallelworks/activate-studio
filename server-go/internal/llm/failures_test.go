package llm

import "testing"

// A model's failed call is remembered for the credential that made it,
// "" being the gateway's own key.
func TestModelFailuresArePerKey(t *testing.T) {
	c := New(func() string { return "https://gw.example/v1" }, func() string { return "gateway-key" })
	c.RecordOutcome("alex-key", "m", &ModelFailure{Message: "rate limited", Status: 429})
	if _, has := c.ModelFailure("sam-key", "m"); has {
		t.Fatal("alex's failure is sam's")
	}
	if _, has := c.ModelFailure("", "m"); has {
		t.Fatal("alex's failure is the gateway's")
	}
	if f, has := c.ModelFailure("alex-key", "m"); !has || f.Status != 429 || f.At == "" {
		t.Fatalf("alex's failure: %+v %v", f, has)
	}
	c.RecordOutcome("", "m", &ModelFailure{Status: 500})
	if _, has := c.ModelFailure("gateway-key", "m"); !has {
		t.Fatal(`"" is the gateway's own key`)
	}
	c.RecordOutcome("alex-key", "m", nil)
	if _, has := c.ModelFailure("alex-key", "m"); has {
		t.Fatal("a call that worked clears the failure")
	}
}
