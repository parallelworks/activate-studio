package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// session is one OIDC sign-in, kept on the server; the browser holds only
// a random session ID, and the store is keyed by its SHA-256.
type session struct {
	User            *User  `json:"user"`
	IDToken         string `json:"idToken"`
	AccessToken     string `json:"accessToken"`
	AccessExpiresAt int64  `json:"accessExpiresAt"` // ms
	RefreshToken    string `json:"refreshToken,omitempty"`
	ExpiresAt       int64  `json:"expiresAt"` // ms
}

// SessionStore holds sign-ins in memory and, with a directory, in an
// AES-256-GCM encrypted file there (oidc-sessions.enc), so a restart keeps
// them. The key is a random 32 bytes in .oidc-session-key beside it, mode
// 0600, made on first use and never logged. Without a directory sessions
// last as long as the process.
type SessionStore struct {
	file, keyFile string

	mu       sync.Mutex
	sessions map[string]*session
	key      []byte
	now      func() time.Time
}

// NewSessionStore opens the store in dir ("" for memory only) and reads
// the sessions saved there; a file that cannot be read or opened (another
// key, damage) starts the store empty.
func NewSessionStore(dir string) (*SessionStore, error) {
	s := &SessionStore{sessions: map[string]*session{}, now: time.Now}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s.file = filepath.Join(dir, "oidc-sessions.enc")
	s.keyFile = filepath.Join(dir, ".oidc-session-key")
	key, err := loadKey(s.keyFile)
	if err != nil {
		return nil, err
	}
	s.key = key
	if b, err := os.ReadFile(s.file); err == nil {
		if plain, err := open(key, b); err == nil {
			var m map[string]*session
			if json.Unmarshal(plain, &m) == nil && m != nil {
				s.sessions = m
			}
		}
	}
	s.prune()
	return s, nil
}

func loadKey(file string) ([]byte, error) {
	if b, err := os.ReadFile(file); err == nil && len(b) == 32 {
		// A key left readable by others is narrowed back to the owner.
		if st, err := os.Stat(file); err == nil && st.Mode().Perm()&0o077 != 0 {
			os.Chmod(file, 0o600)
		}
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, file); err != nil {
		return nil, err
	}
	return key, nil
}

func seal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append([]byte("v1"), gcm.Seal(nonce, nonce, plain, []byte("oidc-sessions"))...), nil
}

func open(key, b []byte) ([]byte, error) {
	if len(b) < 2 || string(b[:2]) != "v1" {
		return nil, errors.New("not a session file")
	}
	b = b[2:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(b) < gcm.NonceSize() {
		return nil, errors.New("short session file")
	}
	return gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], []byte("oidc-sessions"))
}

// saveLocked writes the sessions; s.mu is held.
func (s *SessionStore) saveLocked() {
	if s.file == "" {
		return
	}
	plain, err := json.Marshal(s.sessions)
	if err != nil {
		return
	}
	b, err := seal(s.key, plain)
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())
	if os.WriteFile(tmp, b, 0o600) == nil {
		if os.Rename(tmp, s.file) != nil {
			os.Remove(tmp)
		}
	}
}

func (s *SessionStore) get(key string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[key]
	if !ok {
		return nil, false
	}
	c := *v
	return &c, true
}

func (s *SessionStore) put(key string, v *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *v
	s.sessions[key] = &c
	s.saveLocked()
}

func (s *SessionStore) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[key]; ok {
		delete(s.sessions, key)
		s.saveLocked()
	}
}

// prune drops sessions past their end.
func (s *SessionStore) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixMilli()
	n := len(s.sessions)
	for k, v := range s.sessions {
		if v == nil || v.ExpiresAt < now {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) != n {
		s.saveLocked()
	}
}

// Count is how many sessions are held (for tests and diagnostics).
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
