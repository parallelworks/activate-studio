package fleet

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// On a Studio several people use, a standing agent would run on this
// host's pw code as the Studio's own account, so none is made or woken:
// nothing reaches the daemon.
func TestStandingAgentsDoNotRunOnASharedStudio(t *testing.T) {
	f, d, _ := stand(t)
	ada := &auth.User{ID: "ada", Username: "ada"}
	a, err := f.Create(Input{Name: "Watcher", Goal: "Watch.", Every: "manual"}, ada)
	if err != nil {
		t.Fatal(err)
	}
	f.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	f.Tick(a.ID, "now", true)
	waitFor(t, "the wake-up", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.busy[a.ID] })
	if got := f.Get(a.ID); got.Session != "" || !strings.Contains(got.Note, "do not run on a Studio several people use") {
		t.Fatalf("%+v", got)
	}
	d.mu.Lock()
	n := len(d.created)
	d.mu.Unlock()
	if n != 0 {
		t.Fatal("a session was made as the Studio's account")
	}
	mux := http.NewServeMux()
	Register(mux, f.A, platform.New(platform.Options{Home: t.TempDir(), LookupEnv: func(string) (string, bool) { return "", false }}), f)
	req := auth.WithUser(httptest.NewRequest("POST", "/api/fleet", strings.NewReader(`{"name":"x","goal":"y","every":"manual"}`)), ada)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Studio's own account") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
