package auth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

func token(t *testing.T, hdr map[string]any, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	return in + "." + b64.EncodeToString(sign([]byte(in)))
}

func TestVerifyAlgorithmsAndClaims(t *testing.T) {
	now := time.Now().Unix()
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecJWK := JWK{Kty: "EC", Crv: "P-256", Kid: "e", X: b64.EncodeToString(ec.X.FillBytes(make([]byte, 32))), Y: b64.EncodeToString(ec.Y.FillBytes(make([]byte, 32)))}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	edJWK := JWK{Kty: "OKP", Crv: "Ed25519", Kid: "d", X: b64.EncodeToString(pub)}
	keys := Static([]JWK{ecJWK, edJWK})
	es := func(b []byte) []byte {
		h := sha256.Sum256(b)
		r, s, _ := ecdsa.Sign(rand.Reader, ec, h[:])
		return append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	}
	ed := func(b []byte) []byte { return ed25519.Sign(priv, b) }
	ok := map[string]any{"sub": "a", "iss": "i", "aud": []any{"x", "y"}, "exp": now + 60, "iat": now}
	if _, err := Verify(token(t, map[string]any{"alg": "ES256", "kid": "e"}, ok, es), keys, VerifyOptions{Issuer: "i", Audience: "y"}); err != nil {
		t.Fatal("ES256", err)
	}
	if _, err := Verify(token(t, map[string]any{"alg": "EdDSA", "kid": "d"}, ok, ed), keys, VerifyOptions{}); err != nil {
		t.Fatal("EdDSA", err)
	}
	for name, tc := range map[string]struct {
		hdr    map[string]any
		claims map[string]any
		opts   VerifyOptions
		sign   func([]byte) []byte
	}{
		"alg none":         {map[string]any{"alg": "none"}, ok, VerifyOptions{}, func([]byte) []byte { return nil }},
		"HMAC":             {map[string]any{"alg": "HS256", "kid": "d"}, ok, VerifyOptions{}, ed},
		"alg not allowed":  {map[string]any{"alg": "EdDSA", "kid": "d"}, ok, VerifyOptions{Algs: []string{"RS256"}}, ed},
		"wrong key type":   {map[string]any{"alg": "ES256", "kid": "d"}, ok, VerifyOptions{}, es},
		"expired":          {map[string]any{"alg": "EdDSA", "kid": "d"}, map[string]any{"exp": now - 1}, VerifyOptions{}, ed},
		"not yet":          {map[string]any{"alg": "EdDSA", "kid": "d"}, map[string]any{"nbf": now + 100}, VerifyOptions{}, ed},
		"issuer":           {map[string]any{"alg": "EdDSA", "kid": "d"}, ok, VerifyOptions{Issuer: "other"}, ed},
		"audience":         {map[string]any{"alg": "EdDSA", "kid": "d"}, ok, VerifyOptions{Audience: "z"}, ed},
		"required claim":   {map[string]any{"alg": "EdDSA", "kid": "d"}, map[string]any{"sub": "a"}, VerifyOptions{Required: []string{"exp"}}, ed},
		"crit":             {map[string]any{"alg": "EdDSA", "kid": "d", "crit": []string{"b64"}}, ok, VerifyOptions{}, ed},
		"exp not a number": {map[string]any{"alg": "EdDSA", "kid": "d"}, map[string]any{"exp": "soon"}, VerifyOptions{}, ed},
	} {
		if _, err := Verify(token(t, tc.hdr, tc.claims, tc.sign), keys, tc.opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Clock tolerance covers a token just expired.
	late := token(t, map[string]any{"alg": "EdDSA", "kid": "d"}, map[string]any{"exp": now - 10}, ed)
	if _, err := Verify(late, keys, VerifyOptions{ClockTolerance: 30 * time.Second}); err != nil {
		t.Fatal("tolerance", err)
	}
	if _, err := Verify("a.b", keys, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "compact") {
		t.Fatal("malformed")
	}
}

func TestClaimMapping(t *testing.T) {
	c := map[string]any{"ext": map[string]any{"uidNumber": "1000"}, "gid": 4294967296.0, "acct": "  bob ", "groups": []any{"a", 3.0, "b"}}
	u := &User{}
	mapIdentity(u, c, config.Auth{AccountClaim: "acct", UIDClaim: "ext.uidNumber", GIDClaim: "gid"})
	if u.Account != "bob" || u.UID == nil || *u.UID != 1000 || u.GID != nil || len(u.Groups) != 2 {
		t.Fatalf("%+v", u)
	}
}
