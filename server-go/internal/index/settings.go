package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// How a library's index is built (indexSettings.ts): threads, readers and
// depth, kept per library in INDEX_BASE/indexing.json in Node's format.

// Depth is how far an index goes, each stop adding to the one before.
type Depth string

const (
	DepthNames            Depth = "names"
	DepthText             Depth = "text"
	DepthOCR              Depth = "ocr"
	DepthMeaningDocuments Depth = "meaning-documents"
	DepthMeaningFull      Depth = "meaning-full"
)

// Depths are the stops in order.
var Depths = []Depth{DepthNames, DepthText, DepthOCR, DepthMeaningDocuments, DepthMeaningFull}

// IsDepth reports whether v is a depth.
func IsDepth(v any) bool { s, ok := v.(string); return ok && slices.Contains(Depths, Depth(s)) }

func rank(d Depth) int { return slices.Index(Depths, d) }

// MeaningLevel is how much search by meaning covers: off, documents, full.
type MeaningLevel string

// MeaningLevels are the levels.
var MeaningLevels = []string{"off", "documents", "full"}

// IsMeaningLevel reports whether v is a level.
func IsMeaningLevel(v any) bool { s, ok := v.(string); return ok && slices.Contains(MeaningLevels, s) }

// NewLibraryDepth is the depth a library newly built on this computer gets.
const NewLibraryDepth = DepthMeaningDocuments

// NewRemoteLibraryDepth is the same for a library built on another system.
const NewRemoteLibraryDepth = DepthMeaningDocuments

// MaxThreads caps the thread setting.
const MaxThreads = 256

// Settings are one library's index settings, in the key order Node's
// JSON has them (INDEX_SETTINGS_DEFAULTS spread first, then the saved
// keys, then the depth's parts).
type Settings struct {
	Threads           *int         `json:"threads"`
	Depth             Depth        `json:"depth"`
	Documents         bool         `json:"documents"`
	OCR               bool         `json:"ocr"`
	Meaning           MeaningLevel `json:"meaning"`
	PDFReader         string       `json:"pdfReader"`
	ImageDescriptions bool         `json:"imageDescriptions"`
	LegacyOffice      bool         `json:"legacyOffice"`
	MeaningSpeed      string       `json:"meaningSpeed"`
}

// DepthParts are what a depth reads and makes.
func DepthParts(d Depth) (documents, ocr bool, meaning MeaningLevel) {
	documents = rank(d) >= rank(DepthText)
	ocr = rank(d) >= rank(DepthOCR)
	switch d {
	case DepthMeaningFull:
		meaning = "full"
	case DepthMeaningDocuments:
		meaning = "documents"
	default:
		meaning = "off"
	}
	return
}

func withDepth(s Settings, d Depth) Settings {
	s.Depth = d
	s.Documents, s.OCR, s.Meaning = DepthParts(d)
	return s
}

// Defaults are INDEX_SETTINGS_DEFAULTS.
func Defaults() Settings {
	return withDepth(Settings{PDFReader: "auto", ImageDescriptions: true, LegacyOffice: true, MeaningSpeed: "standard"}, DepthMeaningDocuments)
}

// SettingsStore is indexing.json.
type SettingsStore struct {
	file string
	mu   sync.Mutex
	// cache, as Node keeps it: replaced on write, and read again when the
	// file changes on disk (Node writes it too while it runs).
	cache map[string]map[string]any
	stamp string
}

func settingsStamp(f string) string { return FileStamp(f, "") }

// SettledFor is how long after a file last changed its size and time are
// taken to tell it apart from any later version. A second write within
// the same tick of the file system's clock can keep both; one in place by
// Node, the same length, would then go unseen.
const SettledFor = 2 * time.Second

var unsettled atomic.Int64

// FileStamp is a file's size and time, with extra appended, to tell when
// the file has changed since it was read. A file changed in the last
// SettledFor stamps as one never seen before, so a copy kept from it is
// read again on the next call.
func FileStamp(f, extra string) string {
	st, err := os.Stat(f)
	if err != nil {
		return "none"
	}
	if time.Since(st.ModTime()) < SettledFor {
		return fmt.Sprintf("unsettled:%d", unsettled.Add(1))
	}
	return fmt.Sprintf("%d:%d%s", st.Size(), st.ModTime().UnixNano(), extra)
}

// NewSettingsStore is the store under indexBase.
func NewSettingsStore(indexBase string) *SettingsStore {
	return &SettingsStore{file: filepath.Join(indexBase, "indexing.json")}
}

func (s *SettingsStore) load() map[string]map[string]any {
	stamp := settingsStamp(s.file)
	if s.cache != nil && stamp == s.stamp {
		return s.cache
	}
	s.stamp = stamp
	s.cache = map[string]map[string]any{}
	b, err := os.ReadFile(s.file)
	if err != nil {
		return s.cache
	}
	var raw struct {
		Libraries map[string]map[string]any `json:"libraries"`
	}
	if json.Unmarshal(b, &raw) == nil && raw.Libraries != nil {
		for k, v := range raw.Libraries {
			if v == nil {
				v = map[string]any{}
			}
			s.cache[k] = v
		}
	}
	return s.cache
}

// Forget drops the cache, so the file is read again.
func (s *SettingsStore) Forget() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// Get is indexSettings(library).
func (s *SettingsStore) Get(library string) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(library)
}

func (s *SettingsStore) get(library string) Settings {
	out := Defaults()
	saved := s.load()[library]
	if v, ok := saved["threads"]; ok {
		if v == nil {
			out.Threads = nil
		} else if f, ok := v.(float64); ok {
			n := int(f)
			out.Threads = &n
		}
	}
	if v, ok := saved["pdfReader"].(string); ok {
		out.PDFReader = v
	}
	if v, ok := saved["imageDescriptions"].(bool); ok {
		out.ImageDescriptions = v
	}
	if v, ok := saved["legacyOffice"].(bool); ok {
		out.LegacyOffice = v
	}
	if v, ok := saved["meaningSpeed"].(string); ok {
		out.MeaningSpeed = v
	}
	return withDepth(out, s.depthOf(library))
}

// Built is builtLibrarySettings: the main folder's settings with the
// library's own depth.
func (s *SettingsStore) Built(library string) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return withDepth(s.get("kb"), s.depthOf(library))
}

func (s *SettingsStore) depthOf(library string) Depth {
	all := s.load()
	saved := all[library]
	if IsDepth(saved["depth"]) {
		return Depth(saved["depth"].(string))
	}
	var meaning string
	switch {
	case IsMeaningLevel(saved["meaning"]):
		meaning = saved["meaning"].(string)
	case saved["embeddings"] == false:
		meaning = "off"
	case library == "kb":
		meaning = "documents"
	default:
		meaning = "off"
	}
	if meaning != "off" {
		if meaning == "full" {
			return DepthMeaningFull
		}
		return DepthMeaningDocuments
	}
	main := all["kb"]
	switch {
	case main["documents"] == false:
		return DepthNames
	case main["ocr"] == false:
		return DepthText
	}
	return DepthOCR
}

// CPUCount is the cores this process may use.
func CPUCount() int { return max(1, runtime.NumCPU()) }

// Threads is threadsFor(library).
func (s *SettingsStore) Threads(library string) int {
	t := s.Get(library).Threads
	if t != nil && *t > 0 {
		return min(*t, MaxThreads)
	}
	return CPUCount()
}

// ForgetLibrary is forgetIndexSettings: a removed library's settings.
func (s *SettingsStore) ForgetLibrary(library string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.load()
	if library == "kb" {
		return nil
	}
	if _, ok := all[library]; !ok {
		return nil
	}
	next := map[string]map[string]any{}
	for k, v := range all {
		if k != library {
			next[k] = v
		}
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.cache = next
	s.stamp = settingsStamp(s.file)
	return nil
}

// write writes the file as Node's JSON.stringify(all, null, 1): one
// space of indent, keys in their insertion order. Go maps have none, so
// the libraries and their keys are written in a stable order: the order
// they had in the file, then new ones.
func (s *SettingsStore) write(all map[string]map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.file, orderedJSON(all, s.keyOrder()), 0o644)
}

// keyOrder is the libraries and their keys in the order the file holds
// them, for writing the file back in the same order.
func (s *SettingsStore) keyOrder() [][]string {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return nil
	}
	return fileKeyOrder(b)
}

// Save is saveIndexSettings: checks body and saves; returns the settings
// in force.
func (s *SettingsStore) Save(library string, body map[string]any) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.load()
	next := map[string]any{}
	for k, v := range all[library] {
		next[k] = v
	}
	order := keysInOrder(s.keyOrder(), library, all[library])
	set := func(k string, v any) {
		if _, ok := next[k]; !ok {
			order = append(order, k)
		}
		next[k] = v
	}
	del := func(k string) {
		delete(next, k)
		order = slices.DeleteFunc(order, func(x string) bool { return x == k })
	}
	if v, ok := body["threads"]; ok {
		switch {
		case v == nil || v == "" || v == float64(0):
			set("threads", nil)
		default:
			n, isNum := jsNumber(v)
			if !isNum || n != float64(int(n)) || n < 1 || n > MaxThreads {
				return Settings{}, apierr.New(400, "threads: a whole number from 1 to %d, or empty for one per core", MaxThreads)
			}
			set("threads", float64(int(n)))
		}
	}
	depth := s.depthOf(library)
	_, hasDepth := body["depth"]
	if hasDepth {
		if !IsDepth(body["depth"]) {
			return Settings{}, apierr.New(400, "depth: names, text, ocr, meaning-documents, meaning-full")
		}
		depth = Depth(body["depth"].(string))
	}
	for _, k := range []string{"documents", "ocr", "embeddings"} {
		if v, ok := body[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return Settings{}, apierr.New(400, "%s: true or false", k)
			}
		}
	}
	if v, ok := body["meaning"]; ok && !IsMeaningLevel(v) {
		return Settings{}, apierr.New(400, "meaning: off, documents or full")
	}
	if !hasDepth {
		if v, ok := body["documents"].(bool); ok {
			if v {
				depth = atLeast(depth, DepthText)
			} else {
				depth = DepthNames
			}
		}
		if v, ok := body["ocr"].(bool); ok {
			if v {
				depth = atLeast(depth, DepthOCR)
			} else {
				depth = atMost(depth, DepthText)
			}
		}
		meaning, has := body["meaning"].(string)
		if !has {
			if e, ok := body["embeddings"].(bool); ok {
				has = true
				meaning = "off"
				if e {
					meaning = "documents"
				}
			}
		}
		if has {
			if meaning == "off" {
				depth = atMost(depth, DepthOCR)
			} else {
				depth = Depth("meaning-" + meaning)
			}
		}
	}
	if depth != s.depthOf(library) || hasDepth {
		set("depth", string(depth))
		del("meaning")
		del("embeddings")
	}
	for _, k := range []string{"imageDescriptions", "legacyOffice"} {
		if v, ok := body[k]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return Settings{}, apierr.New(400, "%s: true or false", k)
			}
			set(k, b)
		}
	}
	if v, ok := body["pdfReader"]; ok {
		str := fmt.Sprint(v)
		if !slices.Contains([]string{"auto", "pdftotext", "pypdf"}, str) {
			return Settings{}, apierr.New(400, "pdfReader: auto, pdftotext or pypdf")
		}
		set("pdfReader", str)
	}
	if v, ok := body["meaningSpeed"]; ok {
		if v != "standard" && v != "faster" {
			return Settings{}, apierr.New(400, "meaningSpeed: standard or faster")
		}
		set("meaningSpeed", v)
	}
	nextAll := map[string]map[string]any{}
	for k, v := range all {
		nextAll[k] = v
	}
	nextAll[library] = next
	ko := s.keyOrder()
	ko = setLibraryOrder(ko, library, order)
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return Settings{}, err
	}
	if err := os.WriteFile(s.file, orderedJSON(nextAll, ko), 0o644); err != nil {
		return Settings{}, err
	}
	s.cache = nextAll
	s.stamp = settingsStamp(s.file)
	return s.get(library), nil
}

func atLeast(d, floor Depth) Depth {
	if rank(d) < rank(floor) {
		return floor
	}
	return d
}

func atMost(d, ceiling Depth) Depth {
	if rank(d) > rank(ceiling) {
		return ceiling
	}
	return d
}

// jsNumber is Number(v) for the values a JSON body holds.
func jsNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		var f float64
		if _, err := fmt.Sscan(x, &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// MeaningArgs are what embed.py is told for a level and speed, as flags
// of the embed pass; nil when nothing is embedded.
func MeaningArgs(level MeaningLevel, speed string) []string {
	if level == "off" || level == "" {
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
