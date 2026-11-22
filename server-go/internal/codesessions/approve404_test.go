package codesessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Review 1, finding 21: a platform 404 to an answer is not always an
// approval that is gone. The session is read again to say which: still
// waiting on it (the platform did not take the answer), gone (no longer
// waiting), or the session itself not there.
func TestApproveNotFoundSaysWhy(t *testing.T) {
	session := "waiting"
	plat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"detail": "Not Found"})
		case r.URL.Path == "/api/code/machines/m1/sessions/"+sid && session != "missing":
			ap := []any{}
			if session == "waiting" {
				ap = append(ap, map[string]any{"id": "ap1", "kind": "confirm", "command": "ls"})
			}
			json.NewEncoder(w).Encode(map[string]any{"id": sid, "status": "awaiting_approval", "approvals": ap})
		default:
			w.WriteHeader(404)
		}
	}))
	defer plat.Close()
	base := codeServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1", "PW_API_KEY": "k"}, false).URL
	answer := `{"machine":"m1","id":"` + sid + `","approval":"ap1","allowed":true}`
	for _, c := range []struct {
		session string
		code    int
		says    string
	}{
		{"waiting", 502, "did not take the answer although the session still waits on it (Not Found). Answer it where the session runs."},
		{"answered", 409, "no longer waiting"},
		{"missing", 404, "could not reach that session"},
	} {
		session = c.session
		code, out := postAnswer(t, base, answer)
		if msg, _ := out["error"].(string); code != c.code || !strings.Contains(msg, c.says) {
			t.Errorf("%s: %d %q", c.session, code, msg)
		}
	}
}
