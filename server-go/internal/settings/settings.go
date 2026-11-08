// Package settings reads INDEX_BASE/settings.json as server/src/settings.ts
// does, so the Go server and the Node server behind it see the same
// settings: the file is read again whenever its size, modification time or
// inode changes (Node does the same since stage 1), a file that does not
// parse leaves the last good read in place, and writes go through a
// temporary file renamed over the old one.
//
// Effective is settings.ts's effectiveSettings: the saved values over the
// environment's defaults, field for field and in the same order, so the
// two serialize alike (golden_test.go compares them).
package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Store is one settings file.
type Store struct {
	file   string
	env    func(string) string
	lookup func(string) (string, bool)
	// kbRoot is KB_ROOT, for the default library label.
	kbRoot string
	// authEnabled is whether people sign in (config.AuthEnabled).
	authEnabled bool

	mu    sync.Mutex
	stamp string
	saved map[string]json.RawMessage
}

// Open is the settings of indexBase. lookup is os.LookupEnv in the server
// (nil), a map in tests.
func Open(indexBase, kbRoot string, authEnabled bool, lookup func(string) (string, bool)) *Store {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := func(k string) string { v, _ := lookup(k); return v }
	return &Store{file: filepath.Join(indexBase, "settings.json"), env: env, lookup: lookup, kbRoot: kbRoot, authEnabled: authEnabled}
}

// File is the settings file's path.
func (s *Store) File() string { return s.file }

func stampOf(file string) string {
	st, err := os.Stat(file)
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("%d:%d:%d", st.Size(), st.ModTime().UnixNano(), inode(st))
}

// Saved is the file's contents as saved, each value as written. The map
// is the store's own: callers must not change it.
func (s *Store) Saved() map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamp := stampOf(s.file)
	if s.saved != nil && stamp == s.stamp {
		return s.saved
	}
	b, err := os.ReadFile(s.file)
	var m map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil || m == nil {
		// Half written or unreadable: keep the last good read, else nothing.
		if stamp != "none" && s.saved != nil {
			return s.saved
		}
		m = map[string]json.RawMessage{}
	}
	s.saved, s.stamp = m, stamp
	return m
}

// Update saves patch over the saved settings, keeping the rest (settings.ts
// updateSettings). A nil value removes the key, as undefined does in JSON.
func (s *Store) Update(patch map[string]any) error {
	cur := s.Saved()
	next := make(map[string]json.RawMessage, len(cur)+len(patch))
	for k, v := range cur {
		next[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(next, k)
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		next[k] = b
	}
	return s.write(next)
}

func (s *Store) write(next map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	// JSON.stringify(next, null, 1) keeps insertion order; Go sorts keys.
	// Either reads back the same.
	b, err := json.MarshalIndent(next, "", " ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.file); err != nil {
		os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.saved, s.stamp = next, stampOf(s.file)
	s.mu.Unlock()
	return nil
}

// field decodes one saved value into v. A value of another type than the
// field's counts as unset (Node would pass it through as it is).
func field[T any](m map[string]json.RawMessage, k string) *T {
	raw, ok := m[k]
	if !ok || string(raw) == "null" {
		return nil
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

// Number is a JavaScript number: NaN and the infinities serialize as null,
// as JSON.stringify writes them.
type Number float64

func (n Number) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}

// JSNumber is JavaScript's Number(s) for a string.
func JSNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X' || t[1] == 'o' || t[1] == 'O' || t[1] == 'b' || t[1] == 'B') {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]
		if v, err := strconv.ParseUint(t[2:], base, 64); err == nil {
			return float64(v)
		}
		return math.NaN()
	}
	low := strings.ToLower(t)
	if strings.Contains(low, "inf") || strings.Contains(low, "nan") || strings.HasPrefix(low, "0x") || strings.Contains(t, "_") {
		return math.NaN()
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

// truthy is JavaScript's truthiness of a number (`x || d`).
func truthy(f float64) bool { return f != 0 && !math.IsNaN(f) }
