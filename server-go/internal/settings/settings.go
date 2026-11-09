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
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
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
	// obj is saved with the file's key order.
	obj *jsonobj.Object
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
	var o *jsonobj.Object
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err == nil {
		o, err = jsonobj.Parse(b)
	}
	if err != nil || m == nil {
		// Half written or unreadable: keep the last good read, else nothing.
		if stamp != "none" && s.saved != nil {
			return s.saved
		}
		m, o = map[string]json.RawMessage{}, jsonobj.New()
	}
	s.saved, s.obj, s.stamp = m, o, stamp
	return m
}

// SavedObject is the saved settings in the file's key order, a copy the
// caller may change and hand to WriteObject (PUT /api/settings's next).
func (s *Store) SavedObject() *jsonobj.Object {
	s.Saved()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.obj == nil {
		return jsonobj.New()
	}
	return s.obj.Clone()
}

// WriteObject writes the settings whole, as writeSettingsFile does:
// JSON.stringify(next, null, 1), to a temporary file renamed over the old.
func (s *Store) WriteObject(next *jsonobj.Object) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	b, err := next.Indent(" ")
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
	m := map[string]json.RawMessage{}
	for _, k := range next.Keys() {
		m[k] = next.Get(k)
	}
	s.mu.Lock()
	s.saved, s.obj, s.stamp = m, next.Clone(), stampOf(s.file)
	s.mu.Unlock()
	return nil
}

// Update saves patch over the saved settings, keeping the rest and the
// file's key order (settings.ts updateSettings). A nil value removes the
// key, as undefined does in JSON.
func (s *Store) Update(patch map[string]any) error {
	next := s.SavedObject()
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := next.SetValue(k, patch[k]); err != nil {
			return err
		}
	}
	return s.WriteObject(next)
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
