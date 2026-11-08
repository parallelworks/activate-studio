// Package keycookie reads and writes the browser-held personal model keys
// of server/src/credentials.ts: the key, its provider URL and endpoint
// options, sealed with the server secret (INDEX_BASE/.credentials-secret,
// AES-256-GCM, "v1:<iv>:<tag>:<data>" in base64) together with the
// subject it belongs to, and split across numbered cookies (ade_mk0,
// ade_mk1, ...; ade_mkp0... is the CHIPS-partitioned twin). The format is
// Node's byte for byte, so a cookie either server sealed opens on the
// other; keys are never logged.
package keycookie

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// Cookie and CookieP are the cookie names; either one is enough.
	Cookie  = "ade_mk"
	CookieP = "ade_mkp"
	chunk   = 3800
)

// Key is what a cookie carries.
type Key struct {
	Sub     string          `json:"s"`
	Key     string          `json:"k"`
	BaseURL *string         `json:"u"`
	Options json.RawMessage `json:"o,omitempty"`
}

// Sealer holds the server secret.
type Sealer struct {
	file string
	mu   sync.Mutex
	key  []byte
}

// New is the sealer for indexBase's secret, made on first use as Node
// makes it.
func New(indexBase string) *Sealer {
	return &Sealer{file: filepath.Join(indexBase, ".credentials-secret")}
}

func (s *Sealer) secret() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	if b, err := os.ReadFile(s.file); err == nil && len(b) == 32 {
		s.key = b
		return b, nil
	}
	fresh := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, fresh); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.file, fresh, 0o600); err != nil {
		return nil, err
	}
	s.key = fresh
	return fresh, nil
}

// Encrypt is credentials.ts's encrypt.
func (s *Sealer) Encrypt(plain []byte) (string, error) {
	key, err := s.secret()
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	data, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	e := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("v1:%s:%s:%s", e(iv), e(tag), e(data)), nil
}

// Decrypt is credentials.ts's decrypt.
func (s *Sealer) Decrypt(blob string) ([]byte, error) {
	parts := strings.Split(blob, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return nil, errors.New("not a sealed value")
	}
	d := base64.StdEncoding.DecodeString
	iv, err1 := d(parts[1])
	tag, err2 := d(parts[2])
	data, err3 := d(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || len(iv) != 12 || len(tag) != 16 {
		return nil, errors.New("not a sealed value")
	}
	key, err := s.secret()
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	return gcm.Open(nil, iv, append(data, tag...), nil)
}

// Seal is sealKeyCookie: the cookie values, in order, for k.
func (s *Sealer) Seal(k Key) ([]string, error) {
	plain, err := json.Marshal(k)
	if err != nil {
		return nil, err
	}
	blob, err := s.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := 0; i < len(blob); i += chunk {
		out = append(out, blob[i:min(i+chunk, len(blob))])
	}
	return out, nil
}

func parse(header string) map[string]string {
	jar := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		if eq := strings.Index(part, "="); eq > 0 {
			jar[strings.TrimSpace(part[:eq])] = strings.TrimSpace(part[eq+1:])
		}
	}
	return jar
}

// FromCookies is hydrateFromCookie's reading: the key sealed for sub in
// either cookie, or false. A cookie sealed for another subject, or from
// an older build without one, is refused.
func (s *Sealer) FromCookies(sub, header string) (Key, bool) {
	if sub == "" || header == "" {
		return Key{}, false
	}
	jar := parse(header)
	assemble := func(name string) string {
		var b strings.Builder
		for i := 0; ; i++ {
			c, ok := jar[fmt.Sprintf("%s%d", name, i)]
			if !ok || c == "" {
				break
			}
			b.WriteString(c)
		}
		return b.String()
	}
	for _, blob := range []string{assemble(Cookie), assemble(CookieP)} {
		if blob == "" {
			continue
		}
		plain, err := s.Decrypt(blob)
		if err != nil {
			continue // sealed under a rotated secret: try the other
		}
		var k Key
		if json.Unmarshal(plain, &k) != nil || k.Sub != sub || strings.TrimSpace(k.Key) == "" {
			continue
		}
		return k, true
	}
	return Key{}, false
}
