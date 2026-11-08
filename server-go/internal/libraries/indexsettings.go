package libraries

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// IndexSettings is what this package needs of a library's index settings
// (indexSettings.ts): its depth and level of search by meaning, saving a
// new one, and forgetting a removed library's.
//
// TODO(index track, internal/index): the owner of indexing.json implements
// this; FileIndexSettings is the stand-in, reading and writing the same
// file in the same format.
type IndexSettings interface {
	Depth(library string) string
	Meaning(library string) string
	MeaningSpeed(library string) string
	// Save checks and saves body's fields (saveIndexSettings).
	Save(library string, body map[string]any) error
	Forget(library string)
}

// Depths, in order.
var IndexDepths = []string{"names", "text", "ocr", "meaning-documents", "meaning-full"}

// NewLibraryDepth is NEW_LIBRARY_DEPTH and NEW_REMOTE_LIBRARY_DEPTH.
const NewLibraryDepth = "meaning-documents"

func depthRank(d string) int { return slices.Index(IndexDepths, d) }

// MeaningOfDepth is depthParts(depth).meaning.
func MeaningOfDepth(d string) string {
	switch d {
	case "meaning-full":
		return "full"
	case "meaning-documents":
		return "documents"
	}
	return "off"
}

// DocumentsOfDepth and OCROfDepth are depthParts' documents and ocr.
func DocumentsOfDepth(d string) bool { return depthRank(d) >= depthRank("text") }
func OCROfDepth(d string) bool       { return depthRank(d) >= depthRank("ocr") }

// MeaningArgs is meaningArgs; nil when nothing is embedded.
func MeaningArgs(level, speed string) []string {
	if level == "off" {
		return nil
	}
	out := []string{}
	if level == "documents" {
		out = append(out, "--max-chunks", "4", "--documents-only")
	}
	if speed == "faster" {
		out = append(out, "--max-tokens", "128")
	}
	return out
}

// TextArgs is textArgs; nil when the depth reads no text.
func TextArgs(depth string) []string {
	if depth == "names" {
		return nil
	}
	if depth == "text" {
		return []string{"--no-ocr"}
	}
	return []string{}
}

// FileIndexSettings reads and writes INDEX_BASE/indexing.json as
// indexSettings.ts does, and reads it again when it changes on disk, so
// Node's writes are seen here.
type FileIndexSettings struct {
	File string

	mu      sync.Mutex
	stamp   string
	data    map[string]map[string]json.RawMessage
	order   []string
	libKeys map[string][]string
}

// NewFileIndexSettings is the stand-in over indexBase/indexing.json.
func NewFileIndexSettings(indexBase string) *FileIndexSettings {
	return &FileIndexSettings{File: filepath.Join(indexBase, "indexing.json")}
}

func (s *FileIndexSettings) loadLocked() {
	st := fileStamp(s.File)
	if s.data != nil && st == s.stamp {
		return
	}
	s.stamp = st
	s.data, s.order, s.libKeys = map[string]map[string]json.RawMessage{}, nil, map[string][]string{}
	b, err := os.ReadFile(s.File)
	if err != nil {
		return
	}
	var raw struct {
		Libraries orderedObject `json:"libraries"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	for _, k := range raw.Libraries.keys {
		var m orderedObject
		if json.Unmarshal(raw.Libraries.vals[k], &m) == nil && m.vals != nil {
			s.data[k] = m.vals
			s.libKeys[k] = m.keys
			s.order = append(s.order, k)
		}
	}
}

func str(m map[string]json.RawMessage, k string) (string, bool) {
	var v string
	if raw, ok := m[k]; ok && json.Unmarshal(raw, &v) == nil {
		return v, true
	}
	return "", false
}

func boolean(m map[string]json.RawMessage, k string) (bool, bool) {
	var v bool
	if raw, ok := m[k]; ok && json.Unmarshal(raw, &v) == nil {
		return v, true
	}
	return false, false
}

func (s *FileIndexSettings) depthLocked(library string) string {
	saved := s.data[library]
	if d, ok := str(saved, "depth"); ok && depthRank(d) >= 0 {
		return d
	}
	meaning := "off"
	if library == "kb" {
		meaning = "documents"
	}
	if m, ok := str(saved, "meaning"); ok && (m == "off" || m == "documents" || m == "full") {
		meaning = m
	} else if e, ok := boolean(saved, "embeddings"); ok && !e {
		meaning = "off"
	}
	if meaning != "off" {
		if meaning == "full" {
			return "meaning-full"
		}
		return "meaning-documents"
	}
	main := s.data["kb"]
	if d, ok := boolean(main, "documents"); ok && !d {
		return "names"
	}
	if o, ok := boolean(main, "ocr"); ok && !o {
		return "text"
	}
	return "ocr"
}

// Depth is indexSettings(library).depth.
func (s *FileIndexSettings) Depth(library string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.depthLocked(library)
}

// Meaning is indexSettings(library).meaning.
func (s *FileIndexSettings) Meaning(library string) string { return MeaningOfDepth(s.Depth(library)) }

// MeaningSpeed is indexSettings(library).meaningSpeed.
func (s *FileIndexSettings) MeaningSpeed(library string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if v, ok := str(s.data[library], "meaningSpeed"); ok && v != "" {
		return v
	}
	return "standard"
}

func (s *FileIndexSettings) writeLocked() error {
	var b strings.Builder
	b.WriteString("{\n \"libraries\": {")
	for i, k := range s.order {
		if i > 0 {
			b.WriteString(",")
		}
		kj, _ := json.Marshal(k)
		b.WriteString("\n  " + string(kj) + ": ")
		b.WriteString(indentObject(s.data[k], s.libKeys[k], "  "))
	}
	if len(s.order) > 0 {
		b.WriteString("\n ")
	}
	b.WriteString("}\n}")
	os.MkdirAll(filepath.Dir(s.File), 0o755)
	if err := writeAtomic(s.File, []byte(b.String())); err != nil {
		return err
	}
	s.stamp = fileStamp(s.File)
	return nil
}

// Forget is forgetIndexSettings.
func (s *FileIndexSettings) Forget(library string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if library == "kb" || s.data[library] == nil {
		return
	}
	delete(s.data, library)
	s.order = slices.DeleteFunc(s.order, func(k string) bool { return k == library })
	s.writeLocked()
}

// Save is saveIndexSettings for the fields this package's routes send:
// depth, meaning, embeddings, documents, ocr, threads, imageDescriptions,
// legacyOffice, pdfReader, meaningSpeed.
func (s *FileIndexSettings) Save(library string, body map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	next := map[string]json.RawMessage{}
	for k, v := range s.data[library] {
		next[k] = v
	}
	set := func(k string, v any) { b, _ := json.Marshal(v); next[k] = b }
	if v, ok := body["threads"]; ok {
		if v == nil || v == "" || v == float64(0) {
			set("threads", nil)
		} else {
			n, ok := v.(float64)
			if !ok || n != math.Trunc(n) || n < 1 || n > 256 {
				return apierr.New(400, "threads: a whole number from 1 to 256, or empty for one per core")
			}
			set("threads", n)
		}
	}
	depth := s.depthLocked(library)
	if v, ok := body["depth"]; ok {
		d, _ := v.(string)
		if depthRank(d) < 0 {
			return apierr.New(400, "depth: %s", strings.Join(IndexDepths, ", "))
		}
		depth = d
	}
	for _, k := range []string{"documents", "ocr", "embeddings"} {
		if v, ok := body[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return apierr.New(400, "%s: true or false", k)
			}
		}
	}
	if v, ok := body["meaning"]; ok {
		if m, _ := v.(string); m != "off" && m != "documents" && m != "full" {
			return apierr.New(400, "meaning: off, documents or full")
		}
	}
	atLeast := func(d, floor string) string {
		if depthRank(d) < depthRank(floor) {
			return floor
		}
		return d
	}
	atMost := func(d, ceil string) string {
		if depthRank(d) > depthRank(ceil) {
			return ceil
		}
		return d
	}
	if _, ok := body["depth"]; !ok {
		if v, ok := body["documents"]; ok {
			if v.(bool) {
				depth = atLeast(depth, "text")
			} else {
				depth = "names"
			}
		}
		if v, ok := body["ocr"]; ok {
			if v.(bool) {
				depth = atLeast(depth, "ocr")
			} else {
				depth = atMost(depth, "text")
			}
		}
		meaning := ""
		if v, ok := body["meaning"]; ok {
			meaning = v.(string)
		} else if v, ok := body["embeddings"]; ok {
			meaning = "off"
			if v.(bool) {
				meaning = "documents"
			}
		}
		if meaning == "off" {
			depth = atMost(depth, "ocr")
		} else if meaning != "" {
			depth = "meaning-" + meaning
		}
	}
	_, depthGiven := body["depth"]
	if depth != s.depthLocked(library) || depthGiven {
		set("depth", depth)
		delete(next, "meaning")
		delete(next, "embeddings")
	}
	for _, k := range []string{"imageDescriptions", "legacyOffice"} {
		if v, ok := body[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return apierr.New(400, "%s: true or false", k)
			}
			set(k, v)
		}
	}
	if v, ok := body["pdfReader"]; ok {
		if p, _ := v.(string); p != "auto" && p != "pdftotext" && p != "pypdf" {
			return apierr.New(400, "pdfReader: auto, pdftotext or pypdf")
		}
		set("pdfReader", v)
	}
	if v, ok := body["meaningSpeed"]; ok {
		if v != "standard" && v != "faster" {
			return apierr.New(400, "meaningSpeed: standard or faster")
		}
		set("meaningSpeed", v)
	}
	if s.data[library] == nil {
		s.order = append(s.order, library)
	}
	// Keys keep the order they had, new ones after, as a JS object's do.
	prev := s.libKeys[library]
	keys := []string{}
	for _, k := range prev {
		if _, ok := next[k]; ok {
			keys = append(keys, k)
		}
	}
	for _, k := range []string{"threads", "depth", "imageDescriptions", "legacyOffice", "pdfReader", "meaningSpeed"} {
		if _, ok := next[k]; ok && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for k := range next {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if s.libKeys == nil {
		s.libKeys = map[string][]string{}
	}
	s.libKeys[library] = keys
	s.data[library] = next
	return s.writeLocked()
}
