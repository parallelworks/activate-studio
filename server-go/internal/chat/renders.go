package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Documents made from a file in the library. A report is written once as
// Markdown and made into a PDF or a Word file from it; an edit to the
// Markdown makes those again, so a correction is a small call and not the
// whole report sent a second time. This is the record of which file each
// document was made from, kept beside the index, with the sum of the
// document as it was made: a document someone has since replaced or
// changed is left alone.

type madeFrom struct {
	Source string `json:"source"`
	Sum    string `json:"sum"`
}

type renderStore struct {
	file string
	mu   sync.Mutex
	recs map[string]madeFrom
	read bool
}

func newRenderStore(indexBase string) *renderStore {
	return &renderStore{file: filepath.Join(indexBase, "made-documents.json")}
}

func sumOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// load reads the record once. The caller holds mu.
func (s *renderStore) load() {
	if s.read {
		return
	}
	s.read = true
	s.recs = map[string]madeFrom{}
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var recs map[string]madeFrom
	if json.Unmarshal(b, &recs) == nil && recs != nil {
		s.recs = recs
	}
}

// persist writes the record whole, through a file beside it. The caller
// holds mu.
func (s *renderStore) persist() {
	b, err := json.MarshalIndent(s.recs, "", " ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

// set records that out was made from source, as data.
func (s *renderStore) set(out, source string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.recs[out] = madeFrom{Source: source, Sum: sumOf(data)}
	s.persist()
}

// drop forgets out: it was written some other way, or is gone.
func (s *renderStore) drop(out string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if _, ok := s.recs[out]; ok {
		delete(s.recs, out)
		s.persist()
	}
}

// get is what out was made from.
func (s *renderStore) get(out string) (madeFrom, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	r, ok := s.recs[out]
	return r, ok
}

// from lists the documents made from source, by name.
func (s *renderStore) from(source string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []string
	for o, r := range s.recs {
		if r.Source == source {
			out = append(out, o)
		}
	}
	sort.Strings(out)
	return out
}
