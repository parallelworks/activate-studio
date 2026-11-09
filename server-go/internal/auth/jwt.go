package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"slices"
	"strings"
	"time"
)

// cryptoKey is a public key a token may be verified with.
type cryptoKey = crypto.PublicKey

// Claims is a JWT's payload.
type Claims map[string]any

// header is a JWS protected header.
type header struct {
	Alg  string `json:"alg"`
	Kid  string `json:"kid"`
	Typ  string `json:"typ"`
	Crit []any  `json:"crit"`
}

// VerifyOptions are the checks jose's jwtVerify makes that the Studio uses.
type VerifyOptions struct {
	Issuer   string   // required iss when set
	Audience string   // required among aud when set
	Algs     []string // allowed algorithms; empty: every asymmetric one
	// ClockTolerance is the leeway for exp, nbf and iat.
	ClockTolerance time.Duration
	// Required claims must be present.
	Required []string
	Now      func() time.Time
}

var asymmetric = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA", "Ed25519"}

var b64 = base64.RawURLEncoding

// Verify checks a compact JWS against the keys keys returns for its header
// and the claims against opts, as jose's jwtVerify does with a key set:
// the signature over one of the candidate keys, then exp and nbf (and iat
// when required), issuer and audience. "none" and HMAC algorithms are
// never accepted.
func Verify(token string, keys func(alg, kid string) ([]crypto.PublicKey, error), opts VerifyOptions) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a compact JWS")
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("bad JWS header")
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, errors.New("bad JWS header")
	}
	if len(h.Crit) > 0 {
		return nil, errors.New("unsupported crit header")
	}
	allowed := opts.Algs
	if len(allowed) == 0 {
		allowed = asymmetric
	}
	if !slices.Contains(asymmetric, h.Alg) || !slices.Contains(allowed, h.Alg) {
		return nil, fmt.Errorf("algorithm %q not allowed", h.Alg)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("bad JWS signature")
	}
	candidates, err := keys(h.Alg, h.Kid)
	if err != nil {
		return nil, err
	}
	signed := []byte(parts[0] + "." + parts[1])
	ok := false
	for _, k := range candidates {
		if verifySig(h.Alg, k, signed, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errors.New("signature verification failed")
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("bad JWT payload")
	}
	var c Claims
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil || c == nil {
		return nil, errors.New("JWT payload is not an object")
	}
	c = normalizeNumbers(c).(map[string]any)
	if err := checkClaims(c, opts); err != nil {
		return nil, err
	}
	return c, nil
}

// normalizeNumbers turns json.Number into float64, as JSON.parse does.
func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeNumbers(e)
		}
		return x
	case Claims:
		return normalizeNumbers(map[string]any(x))
	case []any:
		for i, e := range x {
			x[i] = normalizeNumbers(e)
		}
		return x
	}
	return v
}

func numClaim(c Claims, k string) (float64, bool, error) {
	v, ok := c[k]
	if !ok {
		return 0, false, nil
	}
	f, isNum := v.(float64)
	if !isNum {
		return 0, true, fmt.Errorf("%q claim must be a number", k)
	}
	return f, true, nil
}

func checkClaims(c Claims, opts VerifyOptions) error {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	t := float64(now().Unix())
	tol := opts.ClockTolerance.Seconds()
	for _, k := range opts.Required {
		if _, ok := c[k]; !ok {
			return fmt.Errorf("missing required %q claim", k)
		}
	}
	if exp, ok, err := numClaim(c, "exp"); err != nil {
		return err
	} else if ok && exp <= t-tol {
		return errors.New(`"exp" claim timestamp check failed`)
	}
	if nbf, ok, err := numClaim(c, "nbf"); err != nil {
		return err
	} else if ok && nbf > t+tol {
		return errors.New(`"nbf" claim timestamp check failed`)
	}
	if _, _, err := numClaim(c, "iat"); err != nil {
		return err
	}
	if opts.Issuer != "" {
		if iss, _ := c["iss"].(string); iss != opts.Issuer {
			return errors.New(`unexpected "iss" claim value`)
		}
	}
	if opts.Audience != "" && !HasAudience(c, opts.Audience) {
		return errors.New(`unexpected "aud" claim value`)
	}
	return nil
}

// HasAudience reports whether aud is the claims' audience or one of them.
func HasAudience(c Claims, aud string) bool {
	switch a := c["aud"].(type) {
	case string:
		return a == aud
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == aud {
				return true
			}
		}
	}
	return false
}

func hashFor(alg string) (crypto.Hash, func() hash.Hash) {
	switch alg[len(alg)-3:] {
	case "384":
		return crypto.SHA384, sha512.New384
	case "512":
		return crypto.SHA512, sha512.New
	}
	return crypto.SHA256, sha256.New
}

func verifySig(alg string, key crypto.PublicKey, signed, sig []byte) bool {
	switch k := key.(type) {
	case *rsa.PublicKey:
		if !strings.HasPrefix(alg, "RS") && !strings.HasPrefix(alg, "PS") {
			return false
		}
		if k.N.BitLen() < 2048 {
			return false
		}
		ch, hf := hashFor(alg)
		h := hf()
		h.Write(signed)
		if alg[0] == 'R' {
			return rsa.VerifyPKCS1v15(k, ch, h.Sum(nil), sig) == nil
		}
		return rsa.VerifyPSS(k, ch, h.Sum(nil), sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
	case *ecdsa.PublicKey:
		size := map[string]int{"ES256": 32, "ES384": 48, "ES512": 66}[alg]
		if size == 0 || (k.Curve.Params().BitSize+7)/8 != size || len(sig) != 2*size {
			return false
		}
		_, hf := hashFor(alg)
		h := hf()
		h.Write(signed)
		r := new(big.Int).SetBytes(sig[:size])
		s := new(big.Int).SetBytes(sig[size:])
		return ecdsa.Verify(k, h.Sum(nil), r, s)
	case ed25519.PublicKey:
		if alg != "EdDSA" && alg != "Ed25519" {
			return false
		}
		return ed25519.Verify(k, signed, sig)
	}
	return false
}

// SignEdDSA makes a compact JWS of claims with an Ed25519 key.
func SignEdDSA(key ed25519.PrivateKey, kid string, claims any) (string, error) {
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"})
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString(hb) + "." + b64.EncodeToString(pb)
	return in + "." + b64.EncodeToString(ed25519.Sign(key, []byte(in))), nil
}
