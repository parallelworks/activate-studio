package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extract"
)

// ---- dirScan.ts ----

// ExcludeDirs are config.ts's EXCLUDE_DIRS.
var ExcludeDirs = []string{".git", "node_modules", ".venv", "venv", "__pycache__", "dist", "build", ".cache", ".pytest_cache", "screenshots", ".ipynb_checkpoints"}

// Scan is scanLatestDetail's answer.
type Scan struct {
	// Latest is each folder's latest change in whole milliseconds (its own
	// time and the newest of the files directly in it), by its path below
	// the root ("" for the root), in walk order.
	Latest map[string]int64
	Order  []string
	// Denied are the folders that could not be listed for want of
	// permission.
	Denied []string
	Locked []Locked
}

// ScanLatest is dirScan.ts's scanLatestDetail: names starting with a dot
// are passed over, and so are the folders exclude names.
func ScanLatest(root string, exclude, skip []string) Scan {
	ex := map[string]bool{}
	for _, e := range exclude {
		ex[e] = true
	}
	passOver := map[string]bool{}
	for _, e := range append(append([]string{}, exclude...), skip...) {
		passOver[e] = true
	}
	s := Scan{Latest: map[string]int64{}, Denied: []string{}}
	var visit func(abs, rel string)
	visit = func(abs, rel string) {
		entries, err := os.ReadDir(abs)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				s.Denied = append(s.Denied, rel)
			}
			return
		}
		var latest float64
		if st, err := os.Stat(abs); err == nil {
			latest = msOf(st)
			if l := lockedEntry(abs, rel, st.Mode(), entries, passOver); l != nil {
				s.Locked = append(s.Locked, *l)
			}
		}
		var dirs []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				if !ex[e.Name()] {
					dirs = append(dirs, e.Name())
				}
				continue
			}
			if st, err := os.Stat(filepath.Join(abs, e.Name())); err == nil {
				if m := msOf(st); m > latest {
					latest = m
				}
			}
		}
		s.Latest[rel] = int64(latest)
		s.Order = append(s.Order, rel)
		for _, d := range dirs {
			r := d
			if rel != "" {
				r = rel + "/" + d
			}
			visit(filepath.Join(abs, d), r)
		}
	}
	visit(root, "")
	return s
}

// msOf is a file's mtimeMs.
func msOf(st os.FileInfo) float64 { return float64(st.ModTime().UnixNano()) / 1e6 }

// ---- depthState.ts ----

// DepthRecord is what an index was last built or synced at.
type DepthRecord struct {
	Depth        Depth  `json:"depth"`
	OCR          bool   `json:"ocr"`
	MeaningSpeed string `json:"meaningSpeed"`
}

// DepthChange is what applying a new depth runs over the whole library.
type DepthChange struct {
	Text, Meaning bool
	Label         string
}

var (
	tessOnce  sync.Once
	tessFound bool
)

// TesseractAvailable is preextract.ts's tesseractAvailable.
func TesseractAvailable() bool {
	tessOnce.Do(func() { tessFound = extract.FindTools().Tesseract != "" })
	return tessFound
}

// DepthRecordOf is the record for settings an index was built with now.
func DepthRecordOf(s Settings) DepthRecord {
	return DepthRecord{Depth: s.Depth, OCR: s.OCR && TesseractAvailable(), MeaningSpeed: s.MeaningSpeed}
}

// ReadDepthRecord reads a record; nil when there is none.
func ReadDepthRecord(file string) *DepthRecord {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var r map[string]any
	if json.Unmarshal(b, &r) != nil || !IsDepth(r["depth"]) {
		return nil
	}
	out := &DepthRecord{Depth: Depth(r["depth"].(string)), OCR: r["ocr"] == true, MeaningSpeed: "standard"}
	if r["meaningSpeed"] == "faster" {
		out.MeaningSpeed = "faster"
	}
	return out
}

// WriteDepthRecord writes a record, through a temporary file.
func WriteDepthRecord(file string, r DepthRecord) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	b, _ := json.Marshal(r)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// DepthChangeOf is depthChange: nil when the records are the same.
func DepthChangeOf(was, now DepthRecord) *DepthChange {
	if was == now {
		return nil
	}
	newText := rank(was.Depth) < rank(DepthText) && rank(now.Depth) >= rank(DepthText)
	newOCR := now.OCR && !was.OCR
	text := newText || newOCR
	_, _, m0 := DepthParts(was.Depth)
	_, _, m1 := DepthParts(now.Depth)
	meaning := m1 != "off" && (m0 == "off" || (m0 == "documents" && m1 == "full") || was.MeaningSpeed != now.MeaningSpeed || text)
	var parts []string
	if text {
		switch {
		case newText && newOCR:
			parts = append(parts, "text and OCR")
		case newText:
			parts = append(parts, "text")
		default:
			parts = append(parts, "OCR")
		}
	}
	if meaning {
		parts = append(parts, "search by meaning")
	}
	return &DepthChange{Text: text, Meaning: meaning, Label: strings.Join(parts, " and ")}
}

// ---- omissions.ts, the index's part ----

// DeniedRecord is what a build or sync could not read.
type DeniedRecord struct {
	Folders []string `json:"folders"`
	Files   int      `json:"files"`
	At      string   `json:"at"`
}

func (ix *Indexer) deniedFile() string { return filepath.Join(ix.P.IndexBase, "denied.json") }

// ReadDenied is readDenied("kb").
func (ix *Indexer) ReadDenied() *DeniedRecord { return ReadDeniedFile(ix.deniedFile()) }

// ReadDeniedFile reads a denied.json.
func ReadDeniedFile(file string) *DeniedRecord {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var r DeniedRecord
	if json.Unmarshal(b, &r) != nil || r.Folders == nil || (len(r.Folders) == 0 && r.Files == 0) {
		return nil
	}
	return &r
}

// RecordDeniedFile is recordDenied: nothing denied clears it.
func RecordDeniedFile(file string, folders []string, files int) {
	if len(folders) == 0 && files == 0 {
		os.Remove(file)
		return
	}
	var uniq []string
	seen := map[string]bool{}
	for _, f := range folders {
		if !seen[f] {
			seen[f] = true
			uniq = append(uniq, f)
		}
	}
	if len(uniq) > 200 {
		uniq = uniq[:200]
	}
	os.MkdirAll(filepath.Dir(file), 0o755)
	b, _ := json.Marshal(DeniedRecord{Folders: uniq, Files: files, At: isoNow()})
	os.WriteFile(file, b, 0o644)
}

func (ix *Indexer) recordDenied(folders []string) { RecordDeniedFile(ix.deniedFile(), folders, 0) }

var (
	deniedLineRe = regexp.MustCompile(`(?i)Operation not permitted|Permission denied|EPERM|EACCES`)
	mkdirFailRe  = regexp.MustCompile(`^mkdir /.*/db\.db failure:`)
	deniedPathRe = regexp.MustCompile("[\"'`]?(/[^\"'`\\n]*?)[\"'`]?(?::\\s|\\s\\(|$)")
	errorLineRe  = regexp.MustCompile(`(?i)^Error\b|Could not|cannot|failed`)
)

// DeniedPaths are the absolute paths named in permission errors a tool
// printed, one per line.
func DeniedPaths(stderr string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stderr, "\n") {
		if !deniedLineRe.MatchString(line) || mkdirFailRe.MatchString(line) {
			continue
		}
		if m := deniedPathRe.FindStringSubmatch(line); m != nil {
			p := strings.TrimRight(m[1], "/")
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// OnlyDenied reports whether a tool's failure was only folders or files it
// may not read.
func OnlyDenied(stderr string) bool {
	var errs, denied int
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || !errorLineRe.MatchString(l) {
			continue
		}
		errs++
		if deniedLineRe.MatchString(l) {
			denied++
		}
	}
	return denied > 0 && denied == errs
}

// DeniedReason is the reason for a folder the Studio may not read.
func DeniedReason(folder string) string {
	if runtime.GOOS == "darwin" {
		return "macOS has not allowed ACTIVATE Studio to read " + folder + "; allow it in System Settings, Privacy & Security, Files and Folders (or Full Disk Access)"
	}
	return "permission denied reading " + folder
}

// ---- textCleanup.ts ----

const textCleanMark = ".studio-text-clean"

// ProvenancePrefix is preextract.ts's PROVENANCE_PREFIX.
const ProvenancePrefix = "<!-- extracted by downmark "

// TextCleanNeeded reports whether an index has words that may still start
// with the converter's provenance line.
func TextCleanNeeded(indexRoot string) bool {
	return exists(filepath.Join(indexRoot, "db.db")) && !exists(filepath.Join(indexRoot, textCleanMark))
}

// MarkTextClean records that an index's words are without the line.
func MarkTextClean(indexRoot string) error {
	if !exists(filepath.Join(indexRoot, "db.db")) {
		return nil
	}
	b, _ := json.Marshal(struct {
		ProvenanceStripped bool   `json:"provenanceStripped"`
		At                 string `json:"at"`
	}{true, isoNow()})
	return os.WriteFile(filepath.Join(indexRoot, textCleanMark), append(b, '\n'), 0o644)
}

var cleanSQL = "UPDATE words SET wordf = ltrim(substr(wordf, instr(wordf, char(10)) + 1), char(10)) " +
	"WHERE wordf LIKE '" + strings.ReplaceAll(ProvenancePrefix, "'", "''") + "%' AND instr(wordf, char(10)) > 0"

// CleanIndexedText takes the provenance line off the start of every words
// row under an index root, in place.
func CleanIndexedText(indexRoot string, stop func() bool) (dbs, rows int, err error) {
	var files []string
	stack := []string{indexRoot}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				stack = append(stack, filepath.Join(dir, e.Name()))
			} else if e.Type().IsRegular() && e.Name() == "db.db" {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, f := range files {
		if stop() {
			return dbs, rows, ErrCancelled
		}
		func() {
			db, err := sql.Open("sqlite", "file:"+f+"?_pragma=busy_timeout(30000)")
			if err != nil {
				return
			}
			defer db.Close()
			var one int
			if db.QueryRow("SELECT 1 FROM sqlite_master WHERE name = 'words'").Scan(&one) != nil {
				return
			}
			if r, err := db.Exec(cleanSQL); err == nil {
				n, _ := r.RowsAffected()
				rows += int(n)
			}
		}()
		dbs++
	}
	return dbs, rows, nil
}

// ---- small things ----

func isoAt(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func containsStr(xs []string, s string) bool { return slices.Contains(xs, s) }
