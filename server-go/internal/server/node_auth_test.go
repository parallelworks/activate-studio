package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/nodeproc"
)

// TestNodeTrustsTheFrontOnly runs the built Node server behind the Go
// front with the identity hand-off (STUDIO_TEST_NODE names node; the
// repository's server/dist must be built). The front verifies the session
// proxy's JWT; Node sees the user through the hand-off token, refuses the
// platform's own token sent to it directly, and still judges a mission
// bearer on /api/mcp itself.
func TestNodeTrustsTheFrontOnly(t *testing.T) {
	nodeBin := os.Getenv("STUDIO_TEST_NODE")
	if nodeBin == "" {
		t.Skip("STUDIO_TEST_NODE unset")
	}
	main, _ := filepath.Abs("../../../server/dist/main.js")
	if _, err := os.Stat(main); err != nil {
		t.Skip("server/dist not built")
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	b64 := base64.RawURLEncoding
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "p1", "alg": "RS256", "n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer platform.Close()
	sign := func(user string) string {
		hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "p1"})
		now := time.Now().Unix()
		pb, _ := json.Marshal(map[string]any{"sub": "user:" + user, "preferred_username": user, "name": "Grace", "iss": "pw-session-proxy", "iat": now, "exp": now + 300})
		in := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
		h := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
		return in + "." + b64.EncodeToString(sig)
	}
	dir := t.TempDir()
	cfg := config.Config{WebDist: webDist(t), GufiBin: t.TempDir(), GufiIndex: filepath.Join(dir, "none"), IndexBase: filepath.Join(dir, "ix"),
		Auth: config.Auth{JWKSURL: platform.URL, Header: "x-pw-user-token", Issuer: "pw-session-proxy", Required: true}}
	h, err := auth.StartHandoff("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	env := h.NodeEnv(true)
	env["KB_ROOT"] = filepath.Join(dir, "kb")
	env["INDEX_BASE"] = cfg.IndexBase
	env["OIDC_ISSUER"] = ""
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proc, err := nodeproc.Start(t.Context(), log, nodeBin, main, 60*time.Second, env)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(10 * time.Second)
	srv := httptest.NewServer(New(cfg, log, proc.URL, auth.NewFront(cfg, log, nil, h)))
	defer srv.Close()

	call := func(base, method, path string, hdr map[string]string) (int, map[string]any) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m map[string]any
		json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	tok := sign("grace")
	code, cfgBody := call(srv.URL, "GET", "/api/config", map[string]string{"x-pw-user-token": tok})
	if code != 200 {
		t.Fatalf("config %d %v", code, cfgBody)
	}
	if u, _ := cfgBody["user"].(map[string]any); u["username"] != "grace" || u["id"] != "user:grace" || u["name"] != "Grace" {
		t.Fatalf("Node's user %v", cfgBody["user"])
	}
	if code, _ := call(srv.URL, "GET", "/api/config", nil); code != 401 {
		t.Fatalf("unsigned %d", code)
	}
	// A hand-off header from the client is dropped, not trusted.
	forged, _ := h.Token(&auth.User{ID: "x", Username: "x"})
	if code, _ := call(srv.URL, "GET", "/api/config", map[string]string{auth.NodeHeader: forged}); code != 401 {
		t.Fatalf("forged hand-off %d", code)
	}
	// Node itself trusts the front's key only.
	if code, _ := call(proc.URL.String(), "GET", "/api/config", map[string]string{"x-pw-user-token": tok, auth.NodeHeader: tok}); code != 401 {
		t.Fatalf("platform token straight to Node %d", code)
	}
	// whoami is the front's.
	code, who := call(srv.URL, "GET", "/api/whoami", map[string]string{"x-pw-user-token": tok})
	if tokInfo, _ := who["token"].(map[string]any); code != 200 || who["verified"] != true || tokInfo["issuer"] != "pw-session-proxy" || who["header"] != "x-pw-user-token" {
		t.Fatalf("whoami %d %v", code, who)
	}
	// An unknown mission bearer reaches Node, which refuses it.
	if code, body := call(srv.URL, "POST", "/api/mcp", map[string]string{"Authorization": "Bearer nope"}); code != 401 {
		t.Fatalf("mission bearer %d %v", code, body)
	}
}
