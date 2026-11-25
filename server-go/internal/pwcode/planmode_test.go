package pwcode

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// keptModeClient is a pw code whose last mode, kept, is given to a session
// that names none.
type keptModeClient struct {
	kept  string
	calls []string
	live  []string
}

func (c *keptModeClient) Site() string { return "local" }
func (c *keptModeClient) Call(_ context.Context, method, route string, body any, _ time.Duration) (any, error) {
	if method == http.MethodDelete {
		c.calls = append(c.calls, "DELETE "+route)
		c.live = slices.DeleteFunc(c.live, func(id string) bool { return route == "/v1/sessions/"+id })
		return nil, nil
	}
	mode := jsonv.String(jsonv.Path(body, "permissionMode"))
	c.calls = append(c.calls, method+" "+route+" "+mode)
	if mode == "" {
		mode = c.kept
	}
	id := "s" + string(rune('0'+len(c.calls)))
	c.live = append(c.live, id)
	return jsonv.NewObject("id", id, "permissionMode", mode, "workspace", jsonv.Path(body, "workspace"), "agent", jsonv.Path(body, "agent")), nil
}

func TestASessionNotAskedToPlanDoesNotStartInPlanMode(t *testing.T) {
	for _, c := range []struct {
		kept, asked string
		want        string
		calls       []string
	}{
		// /plan in another session is what pw code kept.
		{"plan", "", "accept-edits", []string{"POST /v1/sessions ", "DELETE /v1/sessions/s1", "POST /v1/sessions accept-edits"}},
		// Asked to plan, or for another mode, it is as asked.
		{"plan", "plan", "plan", []string{"POST /v1/sessions plan"}},
		{"plan", "read-only", "read-only", []string{"POST /v1/sessions read-only"}},
		// Any other mode the person has pw code keep is theirs.
		{"read-only", "", "read-only", []string{"POST /v1/sessions "}},
		{"accept-edits", "", "accept-edits", []string{"POST /v1/sessions "}},
		{"", "", "", []string{"POST /v1/sessions "}},
	} {
		d := &keptModeClient{kept: c.kept}
		req := jsonv.NewObject("workspace", "/w", "agent", "studio")
		if c.asked != "" {
			req.Set("permissionMode", c.asked)
		}
		s, err := CreateSession(context.Background(), d, req)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsonv.String(jsonv.Path(s, "permissionMode")); got != c.want || !slices.Equal(d.calls, c.calls) {
			t.Errorf("kept %q, asked %q: %q by %q", c.kept, c.asked, got, d.calls)
		}
		if jsonv.Path(s, "workspace") != "/w" || jsonv.Path(s, "agent") != "studio" || len(d.live) != 1 || d.live[0] != jsonv.String(jsonv.Path(s, "id")) {
			t.Errorf("kept %q, asked %q: %v, left %v", c.kept, c.asked, s, d.live)
		}
		if jsonv.String(jsonv.Path(req, "permissionMode")) != c.asked {
			t.Errorf("the request was changed: %v", req)
		}
	}
}
