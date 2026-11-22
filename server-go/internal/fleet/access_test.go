package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// On a Studio with sign-in, an agent is its owner's and the
// administrators': nobody else lists it, reads it, wakes, pauses, retires
// or writes to it, and no answer carries the owner's sign-in record.
func TestAnAgentIsItsOwners(t *testing.T) {
	f, _, _ := stand(t)
	f.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return "cy"
		}
		return ""
	}
	ada := &auth.User{ID: "ada", Username: "ada", Groups: []string{"team-a"}, Claims: map[string]any{"email": "ada@platform.example"}}
	bob := &auth.User{ID: "bob", Username: "bob"}
	cy := &auth.User{ID: "cy", Username: "cy"}
	a, err := f.Create(Input{Name: "Ada's watcher", Goal: "Watch the queue.", Every: "manual"}, ada)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, f.A, platform.New(platform.Options{Home: t.TempDir(), LookupEnv: func(string) (string, bool) { return "", false }}), f)
	do := func(u *auth.User, method, path string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(`{"text":"read her notes"}`))
		req.Header.Set("Content-Type", "application/json")
		if u != nil {
			req = auth.WithUser(req, u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	count := func(body string) int {
		var out struct{ Agents []map[string]any }
		json.Unmarshal([]byte(body), &out)
		return len(out.Agents)
	}

	for _, u := range []*auth.User{bob, nil} {
		if code, body := do(u, http.MethodGet, "/api/fleet"); code != 200 || count(body) != 0 {
			t.Errorf("%v lists: %d %s", u, code, body)
		}
		if code, _ := do(u, http.MethodGet, "/api/fleet/"+a.ID); code != 404 {
			t.Errorf("%v reads ada's agent: %d", u, code)
		}
		for _, act := range []string{"tick", "pause", "resume", "retire", "message"} {
			if code, _ := do(u, http.MethodPost, "/api/fleet/"+a.ID+"/"+act); code != 404 {
				t.Errorf("%v %s ada's agent: %d", u, act, code)
			}
		}
	}
	if s := f.Get(a.ID); s.State == "paused" || s.State == "retired" || len(s.Inbox) != 0 {
		t.Errorf("bob changed ada's agent: %+v", s)
	}

	// Ada and an administrator see it; neither answer carries Ada's record.
	for _, u := range []*auth.User{ada, cy} {
		code, body := do(u, http.MethodGet, "/api/fleet")
		if code != 200 || count(body) != 1 || strings.Contains(body, "ownerUser") || strings.Contains(body, "ada@platform.example") || strings.Contains(body, "team-a") {
			t.Errorf("%s lists: %d %s", u.ID, code, body)
		}
		code, body = do(u, http.MethodGet, "/api/fleet/"+a.ID)
		if code != 200 || strings.Contains(body, "ownerUser") || strings.Contains(body, "ada@platform.example") {
			t.Errorf("%s reads: %d %s", u.ID, code, body)
		}
	}
	if code, _ := do(cy, http.MethodPost, "/api/fleet/"+a.ID+"/pause"); code != 200 || f.Get(a.ID).State != "paused" {
		t.Errorf("an administrator cannot pause it: %d", code)
	}
	// The record keeps the owner, for the agent's own wake-ups.
	if f.Get(a.ID).OwnerUser == nil {
		t.Error("the record lost its owner")
	}
}
