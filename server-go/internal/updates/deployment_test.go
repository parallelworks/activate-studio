package updates

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// deployed is a Linux payload as the ACTIVATE workflow lays it out: no
// window, no installer, no install-kind, on a hosted Studio that signs
// people in.
func deployed(t *testing.T, version string) *fixture {
	fx := newFixture(t, "linux", version, goLayout)
	for _, f := range []string{"bin", "install-kind"} {
		if err := os.RemoveAll(filepath.Join(fx.app, f)); err != nil {
			t.Fatal(err)
		}
	}
	fx.env["PW_PLATFORM_HOST"] = "platform.example.com"
	fx.u.o.AuthEnabled = true
	return fx
}

func callAs(fx *fixture, user *auth.User, method, path string) (int, string) {
	mux := http.NewServeMux()
	Routes(fx.u, mux, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if user != nil {
		req = auth.WithUser(req, user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestDeploymentUpdatesOnlyWhenItSaysSo(t *testing.T) {
	fx := deployed(t, "v1.188")
	alice := &auth.User{ID: "alice", Username: "alice"}
	fx.env["STUDIO_UPDATE_OWNERS"] = "alice"
	if fx.u.Mode() != "hosted" {
		t.Fatalf("without STUDIO_HOSTED_UPDATES: %s", fx.u.Mode())
	}
	if code, body := callAs(fx, alice, "GET", "/api/updates"); code != 403 || !strings.Contains(body, "updated by its deployment") {
		t.Fatalf("hosted, to its owner too: %d %s", code, body)
	}
	fx.env["STUDIO_HOSTED_UPDATES"] = "1"
	if fx.u.Mode() != "app" {
		t.Fatalf("with STUDIO_HOSTED_UPDATES: %s", fx.u.Mode())
	}
	// A payload the deployment cannot replace stays hosted.
	if err := os.Chmod(filepath.Dir(fx.app), 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(fx.app), 0o755)
	if os.Geteuid() != 0 && fx.u.Mode() != "hosted" {
		t.Fatalf("read-only: %s", fx.u.Mode())
	}
}

func TestDeploymentOwners(t *testing.T) {
	fx := deployed(t, "v1.188")
	fx.env["STUDIO_HOSTED_UPDATES"] = "1"
	fx.env["STUDIO_UPDATE_OWNERS"] = "user:Alice.Smith, carol"
	for _, c := range []struct {
		user *auth.User
		code int
	}{
		{&auth.User{ID: "alice.smith", Username: "alice.smith"}, 200},
		{&auth.User{ID: "x", Username: "carol"}, 200},
		{&auth.User{ID: "bob", Username: "bob"}, 403},
		{nil, 403},
	} {
		if code, body := callAs(fx, c.user, "GET", "/api/updates"); code != c.code {
			t.Fatalf("%+v: %d %s", c.user, code, body)
		}
	}
	for _, p := range []string{"/api/updates/check", "/api/updates/install", "/api/updates/rollback"} {
		if code, _ := callAs(fx, &auth.User{ID: "bob", Username: "bob"}, "POST", p); code != 403 {
			t.Fatalf("%s for someone else: %d", p, code)
		}
	}
	// No owner named: nobody updates it.
	fx.env["STUDIO_UPDATE_OWNERS"] = ""
	if code, _ := callAs(fx, &auth.User{ID: "carol", Username: "carol"}, "GET", "/api/updates"); code != 403 {
		t.Fatal("no owners")
	}
	// Without sign-in the deployment is one person's.
	fx.u.o.AuthEnabled = false
	if code, _ := callAs(fx, nil, "GET", "/api/updates"); code != 200 {
		t.Fatal("a deployment without sign-in")
	}
}

func TestDeploymentInstallsWithoutWindow(t *testing.T) {
	fx := deployed(t, "v1.188")
	fx.env["STUDIO_HOSTED_UPDATES"] = "1"
	fx.env["STUDIO_UPDATE_OWNERS"] = "alice"
	alice := &auth.User{ID: "alice", Username: "alice"}
	// One that replaces the whole desktop app restarts a deployment's
	// server all the same.
	fx.feed.set(fx.rel("v1.190", goLayout, relOpts{manifest: map[string]any{"requiresRelaunch": true}}))
	fx.u.Check()
	s := fx.u.Status()
	if !s.Available || !s.CanInstall || !s.Deployment || s.Reopen {
		t.Fatalf("status %+v %v", s, deref(s.Reason))
	}
	if code, body := callAs(fx, alice, "POST", "/api/updates/install"); code != 200 {
		t.Fatalf("install: %d %s", code, body)
	}
	s = fx.settle()
	if s.State != "restarting" || len(fx.restarts) != 1 || fx.restarts[0] != 76 {
		t.Fatalf("%s %v %v", s.State, deref(s.Error), fx.restarts)
	}
	if v := fx.versionIn(fx.app); v != "v1.190" {
		t.Fatalf("in place: %s", v)
	}
	for _, f := range windowFiles {
		if exists(filepath.Join(fx.app, f)) {
			t.Fatalf("%s left in the payload", f)
		}
	}
	if !exists(filepath.Join(fx.app, "studio-run.sh")) || !exists(filepath.Join(fx.app, "app", "bin", "studio-server")) {
		t.Fatal("the server or the run loop went")
	}
	if exists(filepath.Join(fx.data, "updates", "reopen")) || exists(filepath.Join(fx.data, "updates", "relaunch.json")) {
		t.Fatal("a deployment has no window to reopen")
	}
	if !exists(filepath.Join(fx.data, "updates", "pending.json")) || fx.versionIn(fx.u.beside("previous")) != "v1.188" {
		t.Fatal("the first starts are not marked, or the previous copy is not kept")
	}
}
