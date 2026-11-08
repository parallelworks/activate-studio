package userlibs

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// GET /api/whoami where the front's identity is off: the platform token
// verified here, and routes.ts's answer.
func TestWhoamiVerifiesThePlatformToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []auth.JWK{{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub), Kid: "k1", Use: "sig", Alg: "EdDSA"}}})
	}))
	defer keys.Close()
	ask := func(a config.Auth, token string) (int, string) {
		cfg := config.Config{Auth: a}
		front := auth.NewFront(cfg, nil, nil, nil)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, r *http.Request) {
			v, err := whoami(a, front, r)
			if err != nil {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			b, _ := json.Marshal(v)
			w.Write(b)
		})
		r := httptest.NewRequest("GET", "/api/whoami", nil)
		if token != "" {
			r.Header.Set(a.Header, token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	off := config.Auth{Header: "x-pw-user-token"}
	if c, b := ask(off, ""); c != 200 || b != `{"authEnabled":false,"header":"x-pw-user-token","verified":false,"user":null,"token":null,"claims":null}` {
		t.Errorf("off: %d %s", c, b)
	}
	on := config.Auth{JWKSURL: keys.URL, Header: "x-pw-user-token", Issuer: "platform"}
	now := time.Now().Unix()
	// The claims in the order written, as jose decodes them.
	tok, _ := auth.SignEdDSA(priv, "k1", json.RawMessage(`{"sub":"u-1","username":"ada","name":"Ada L","iss":"platform","iat":`+itoa(now)+`,"exp":`+itoa(now+600)+`}`))
	c, b := ask(on, "Bearer "+tok)
	want := `{"authEnabled":true,"header":"x-pw-user-token","verified":true,"user":{"id":"u-1","username":"ada","name":"Ada L"},"token":{"issuer":"platform","audience":null,"issuedAt":"` + iso(now) + `","expiresAt":"` + iso(now+600) + `"},"claims":{"sub":"u-1","username":"ada","name":"Ada L","iss":"platform","iat":` + itoa(now) + `,"exp":` + itoa(now+600) + `}}`
	if c != 200 || b != want {
		t.Errorf("verified:\n got %d %s\nwant %s", c, b, want)
	}
	if c, b := ask(on, "not-a-token"); c != 200 || b != `{"authEnabled":true,"header":"x-pw-user-token","verified":false,"user":null,"token":null,"claims":null}` {
		t.Errorf("unverified, not required: %d %s", c, b)
	}
	on.Required = true
	if c, b := ask(on, "not-a-token"); c != 401 || b != "{\"error\":\"invalid token\"}\n" {
		t.Errorf("invalid: %d %s", c, b)
	}
	if c, b := ask(on, ""); c != 401 || b != "{\"error\":\"authentication required\"}\n" {
		t.Errorf("missing: %d %s", c, b)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
func iso(s int64) string  { return time.Unix(s, 0).UTC().Format("2006-01-02T15:04:05.000Z") }
