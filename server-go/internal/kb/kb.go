// Package kb is the knowledge base's files: the roots a request may reach,
// path resolution and its safety checks, listings and file contents, the
// file operations (move, copy, rename, delete, upload), previews, and
// labels. It ports server/src/kb.ts, move.ts, preview.ts, officePreview.ts,
// uploads.ts, tags.ts, tagsOverlay.ts and libraryLabels.ts, and the KB file
// routes of routes.ts (docs/GO-PORT.md, "KB files").
//
// The package's Go API (Roots, Resolve, ListDir, ReadFileContent, the
// operations, Tags, Indexer) is what other tracks call; the routes are
// registered by Register.
package kb

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// Roots are the directories the KB lives in, as config.ts names them.
type Roots struct {
	KBRoot    string // KB_ROOT: the main library's files
	IndexBase string // INDEX_BASE: the Studio's own state
	IndexDir  string // STUDIO_INDEX_DIR, else IndexBase: the index and its caches
	GufiIndex string // IndexDir/gufi/<basename of KBRoot>
	GufiBin   string
}

// RootsFrom are the roots of the server's configuration.
func RootsFrom(c config.Config) Roots {
	return Roots{KBRoot: c.KBRoot, IndexBase: c.IndexBase, IndexDir: c.IndexDir, GufiIndex: c.GufiIndex, GufiBin: c.GufiBin}
}

// ExtractCache is EXTRACT_CACHE: extracted text by KB path.
func (r Roots) ExtractCache() string { return filepath.Join(r.IndexDir, "extract") }

// ExtractPath is the extracted-text cache file of a KB-relative path.
func (r Roots) ExtractPath(rel string) string { return filepath.Join(r.ExtractCache(), rel+".txt") }

// PDFCache is where office documents rendered as PDF are kept. Node keeps
// it under INDEX_BASE, not the index folder (preview.ts).
func (r Roots) PDFCache() string { return filepath.Join(r.IndexBase, "pdf-preview") }

// PDFPages is where PDF pages rendered as PNG are kept (INDEX_BASE too).
func (r Roots) PDFPages() string { return filepath.Join(r.IndexBase, "pdf-pages") }

// ModelCachePath is model.ts's cache file of a 3D model.
func (r Roots) ModelCachePath(rel string) string {
	return filepath.Join(r.IndexBase, "model-cache", rel+".mdl")
}

// Error is a KbError: a status and a message the client sees.
func Error(status int, format string, a ...any) error { return apierr.New(status, format, a...) }

// Suffix sets and limits of config.ts.
var (
	ExcludeDirs = set(".git", "node_modules", ".venv", "venv", "__pycache__", "dist", "build",
		".cache", ".pytest_cache", "screenshots", ".ipynb_checkpoints")
	ExcludeFileSuffixes = []string{".sif", ".pyc", ".so", ".o", ".zip", ".tar", ".gz", ".tgz"}
	TextSuffixes        = set(".md", ".txt", ".py", ".sh", ".yaml", ".yml", ".json", ".csv", ".tsv", ".xml",
		".html", ".css", ".js", ".ts", ".tsx", ".svg", ".toml", ".cfg", ".ini", ".def", ".mjs", ".sql",
		".log", ".conf", ".env", ".service")
	ExtractedSuffixes = set(".pdf", ".docx", ".pptx", ".xlsx", ".doc", ".ppt", ".xls", ".odt", ".odp", ".ods")
	ImageSuffixes     = set(".png", ".jpg", ".jpeg", ".gif", ".webp")
	ModelSuffixes     = set(".stl", ".step", ".stp")
	// VisibleDotDirs are dot directories the Library shows anyway.
	VisibleDotDirs = set(".agents")
)

// MaxPreviewBytes is MAX_PREVIEW_BYTES.
const MaxPreviewBytes = 512 * 1024

// ProvenancePrefix opens extracted text that downmark converted
// (preextract.ts).
const ProvenancePrefix = "<!-- extracted by downmark "

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Kinds of entries, as KbEntry['kind'].
const (
	KindText      = "text"
	KindExtracted = "extracted"
	KindImage     = "image"
	KindModel     = "model"
	KindBinary    = "binary"
	KindDir       = "dir"
)

// ExtName is path.extname: the suffix from the last dot of the base name,
// "" for a name with no dot or only a leading one.
func ExtName(p string) string {
	base := p
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	// path.extname ignores leading dots: ".bashrc" has none, "..x" is ".x".
	start := 0
	for start < len(base) && base[start] == '.' {
		start++
	}
	i := strings.LastIndexByte(base[start:], '.')
	if i < 0 {
		return ""
	}
	return base[start+i:]
}

// Classify is kb.ts's classify.
func Classify(name string, isDir bool) string {
	if isDir {
		return KindDir
	}
	ext := strings.ToLower(ExtName(name))
	switch {
	case TextSuffixes[ext]:
		return KindText
	case ExtractedSuffixes[ext]:
		return KindExtracted
	case ImageSuffixes[ext]:
		return KindImage
	case ModelSuffixes[ext]:
		return KindModel
	}
	return KindBinary
}

// Excluded is kb.ts's excluded: machinery directories and opaque binaries.
func Excluded(name string, isDir bool) bool {
	if isDir {
		return ExcludeDirs[name]
	}
	low := strings.ToLower(name)
	for _, s := range ExcludeFileSuffixes {
		if strings.HasSuffix(low, s) {
			return true
		}
	}
	return false
}

// HiddenName is kb.ts's hiddenName: a dot entry other than the persona
// library.
func HiddenName(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && !VisibleDotDirs[name]
}

// Resolve is kb.ts's resolveKb: a client's relative path inside base (the
// main library's folder unless a library's own root is given), refused
// with 400 when it leaves base. Like Node it checks the path as written:
// a symbolic link inside base is followed wherever it points.
func Resolve(rel, base string) (string, error) {
	cleaned := strings.TrimLeft(rel, "/")
	abs := jsResolve(base, cleaned)
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return "", Error(400, "path escapes the knowledge base root")
	}
	return abs, nil
}

// jsResolve is path.resolve(base, rel) for an absolute base: rel joined
// and normalized, no trailing slash. A relative base is taken from the
// working directory, as Node does.
func jsResolve(base, rel string) string {
	if strings.HasPrefix(rel, "/") {
		return filepath.Clean(rel)
	}
	if !filepath.IsAbs(base) {
		if wd, err := os.Getwd(); err == nil {
			base = filepath.Join(wd, base)
		}
	}
	return filepath.Clean(base + "/" + rel)
}

// trimSlashes is rel.replace(/^\/+|\/+$/g, ”).
func trimSlashes(s string) string { return strings.Trim(s, "/") }

// protected is the check every mutation runs on each part of a path: no
// machinery directory and nothing hidden.
func protected(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
