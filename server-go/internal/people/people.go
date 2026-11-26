// Package people keeps who has signed in to this Studio: each person the
// sign-in gate verified, recorded once, the first time they made a request.
// It answers whether an ACTIVATE account is a person of this Studio, for the
// routes outside sign-in that learn a caller's account from the platform
// (the grounded model at /v1, internal/chat).
//
// The record is a file of the Studio's account alone (INDEX_BASE/people.json,
// 0600): each person's id, user name and when they were first seen.
package people

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// Person is one who has signed in here.
type Person struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	First    string `json:"first"`
}

// Store is the record.
type Store struct {
	path   string
	mu     sync.Mutex
	loaded bool
	known  map[string]Person // by the person's id
	names  map[string]bool   // their account names, lower case
}

var (
	instMu sync.Mutex
	inst   = map[*app.App]*Store{}
)

// Of is a's record, made on first use.
func Of(a *app.App) *Store {
	instMu.Lock()
	defer instMu.Unlock()
	if s := inst[a]; s != nil {
		return s
	}
	path := ""
	if a.Cfg.IndexDir != "" {
		path = filepath.Join(a.Cfg.IndexDir, "people.json")
	}
	s := New(path)
	inst[a] = s
	return s
}

// New is the record kept at path; with none, it is kept in memory only.
func New(path string) *Store { return &Store{path: path} }

// account is a platform account name as the platform and the sign-in token
// give it: without the "user:" a subject or the platform's whoami may carry,
// in lower case.
func account(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "user:"))
}

func (s *Store) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.known, s.names = map[string]Person{}, map[string]bool{}
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []Person
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, p := range list {
		s.add(p)
	}
}

func (s *Store) add(p Person) {
	s.known[p.ID] = p
	for _, n := range []string{p.Username, p.ID} {
		if a := account(n); a != "" {
			s.names[a] = true
		}
	}
}

// Seen records u, a person the sign-in gate verified, the first time.
func (s *Store) Seen(u *auth.User) {
	if u == nil || u.ID == "" || u.Username == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if p, ok := s.known[u.ID]; ok && p.Username == u.Username {
		return
	}
	s.add(Person{ID: u.ID, Username: u.Username, First: time.Now().UTC().Format(time.RFC3339)})
	if s.path == "" {
		return
	}
	list := make([]Person, 0, len(s.known))
	for _, p := range s.known {
		list = append(list, p)
	}
	b, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.path), 0o700) != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, s.path)
	}
}

// Known says whether the platform account name (as the platform's whoami
// gives it) is one of a person who has signed in here.
func (s *Store) Known(name string) bool {
	a := account(name)
	if a == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.names[a]
}
