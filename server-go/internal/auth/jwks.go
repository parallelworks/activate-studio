package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWK is one key of a JSON Web Key Set.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

// RemoteJWKS is jose's createRemoteJWKSet: a key set fetched from a URL,
// kept for ten minutes, and fetched again for a key ID it does not hold,
// at most every 30 seconds.
type RemoteJWKS struct {
	URL    string
	Client *http.Client

	mu      sync.Mutex
	keys    []JWK
	fetched time.Time
	fetch   chan struct{} // closed when a fetch in flight ends
}

const (
	jwksMaxAge   = 10 * time.Minute
	jwksCooldown = 30 * time.Second
)

// NewRemoteJWKS is the key set at url.
func NewRemoteJWKS(url string) *RemoteJWKS {
	return &RemoteJWKS{URL: url, Client: &http.Client{Timeout: 5 * time.Second}}
}

func (s *RemoteJWKS) refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.fetch != nil {
		ch := s.fetch
		s.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	s.fetch = ch
	s.mu.Unlock()
	keys, err := fetchJWKS(ctx, s.Client, s.URL)
	s.mu.Lock()
	if err == nil {
		s.keys, s.fetched = keys, time.Now()
	}
	s.fetch = nil
	close(ch)
	s.mu.Unlock()
	return err
}

func fetchJWKS(ctx context.Context, c *http.Client, url string) ([]JWK, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS answered %d", res.StatusCode)
	}
	var set struct {
		Keys []JWK `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return nil, errors.New("JWKS is not a key set")
	}
	return set.Keys, nil
}

// Keys returns the keys that may have signed a token of alg and kid.
func (s *RemoteJWKS) Keys(ctx context.Context, alg, kid string) ([]crypto.PublicKey, error) {
	s.mu.Lock()
	stale := s.keys == nil || time.Since(s.fetched) > jwksMaxAge
	s.mu.Unlock()
	if stale {
		if err := s.refresh(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	found := match(s.keys, alg, kid)
	cool := time.Since(s.fetched) > jwksCooldown
	s.mu.Unlock()
	if len(found) == 0 && cool {
		if err := s.refresh(ctx); err != nil {
			return nil, err
		}
		s.mu.Lock()
		found = match(s.keys, alg, kid)
		s.mu.Unlock()
	}
	if len(found) == 0 {
		return nil, errors.New("no applicable key found in the JSON Web Key Set")
	}
	return found, nil
}

// Static is a key set held in memory.
func Static(keys []JWK) func(alg, kid string) ([]crypto.PublicKey, error) {
	return func(alg, kid string) ([]crypto.PublicKey, error) {
		found := match(keys, alg, kid)
		if len(found) == 0 {
			return nil, errors.New("no applicable key found in the JSON Web Key Set")
		}
		return found, nil
	}
}

// match is jose's key selection: the kid when the token names one, a key
// type that fits the algorithm, use sig or none, and alg when the key
// names one.
func match(keys []JWK, alg, kid string) []crypto.PublicKey {
	var out []crypto.PublicKey
	for _, k := range keys {
		if kid != "" && k.Kid != kid {
			continue
		}
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.Alg != "" && k.Alg != alg {
			continue
		}
		want := ""
		switch {
		case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
			want = "RSA"
		case strings.HasPrefix(alg, "ES"):
			want = "EC"
		case alg == "EdDSA" || alg == "Ed25519":
			want = "OKP"
		}
		if k.Kty != want {
			continue
		}
		if pk, err := k.PublicKey(); err == nil {
			out = append(out, pk)
		}
	}
	return out
}

// PublicKey is the key a JWK describes.
func (k JWK) PublicKey() (crypto.PublicKey, error) {
	num := func(s string) (*big.Int, error) {
		b, err := b64.DecodeString(strings.TrimRight(s, "="))
		if err != nil || len(b) == 0 {
			return nil, errors.New("bad key parameter")
		}
		return new(big.Int).SetBytes(b), nil
	}
	switch k.Kty {
	case "RSA":
		n, err := num(k.N)
		if err != nil {
			return nil, err
		}
		e, err := num(k.E)
		if err != nil || !e.IsInt64() || e.Int64() > 1<<31 {
			return nil, errors.New("bad RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err := num(k.X)
		if err != nil {
			return nil, err
		}
		y, err := num(k.Y)
		if err != nil {
			return nil, err
		}
		pk := &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		if !curve.IsOnCurve(x, y) {
			return nil, errors.New("point not on curve")
		}
		return pk, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, errors.New("unsupported curve")
		}
		b, err := b64.DecodeString(strings.TrimRight(k.X, "="))
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, errors.New("bad Ed25519 key")
		}
		return ed25519.PublicKey(b), nil
	}
	return nil, errors.New("unsupported key type")
}
