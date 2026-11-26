package userlibs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// TestABodyThatIsNotJSONIsA400: the share routes, and the rest, answer a
// body they cannot read (empty with a JSON content type, malformed, of
// another type) with 400 and words, and one of the wrong shape with what is
// wrong; never a 500.
func TestABodyThatIsNotJSONIsA400(t *testing.T) {
	h := newHarness(t, nil)
	h.auth = true
	routes := [][2]string{
		{http.MethodDelete, "/api/user-libraries/u-space-ada/shares/s-1"},
		{http.MethodPost, "/api/user-libraries/u-space-ada/shares"},
		{http.MethodPost, "/api/user-libraries/shared-with-me/s-1/add"},
		{http.MethodPost, "/api/user-libraries/pw"},
	}
	unreadable := [][2]string{
		{"application/json", ""},
		{"application/json", "{"},
		{"application/json", `{"path": "a"} x`},
		{"application/x-www-form-urlencoded", "path=a"},
		{"multipart/form-data; boundary=x", "--x--"},
	}
	wrong := [][2]string{
		{"application/json", "[1, 2]"},
		{"application/json", `"proj"`},
		{"application/json", "null"},
		{"application/json", `{"path": 5, "kind": [], "name": {}, "access": true, "depth": 3}`},
	}
	ask := func(method, url, ct, body string) (int, string) {
		r := auth.WithUser(httptest.NewRequest(method, url, strings.NewReader(body)), &auth.User{ID: "ada", Username: "ada"})
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		h.mux.ServeHTTP(w, r)
		var out struct{ Error string }
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Error
	}
	for _, rt := range routes {
		for _, b := range unreadable {
			code, why := ask(rt[0], rt[1], b[0], b[1])
			if code != http.StatusBadRequest || why == "" || strings.Contains(why, "internal error") {
				t.Errorf("%s %s, %s %q: %d %q", rt[0], rt[1], b[0], b[1], code, why)
			}
		}
		for _, b := range wrong {
			if code, why := ask(rt[0], rt[1], b[0], b[1]); code >= 500 || why == "" {
				t.Errorf("%s %s, %q: %d %q", rt[0], rt[1], b[1], code, why)
			}
		}
	}
}
