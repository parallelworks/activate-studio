package agentsmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// Carrying is a folder with something the Studio put there.
type Carrying struct {
	Target
	State
}

// scanCap is the most folders of the main library one scan looks in.
const scanCap = 20_000

// scanDepth is how deep a scan goes below the library's top.
const scanDepth = 10

// scanTTL is how long a scan of the main library is reused.
const scanTTL = 20 * time.Second

type scanCache struct {
	mu   sync.Mutex
	at   time.Time
	root string
	hits []string
}

// scanMain is the folders of the main library that hold an AGENTS.md or a
// .agents/skills folder, found by walking it: hidden and machinery folders
// are skipped and links are not followed.
func (s *Service) scanMain(root string) []string {
	c := &s.scan
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == root && time.Since(c.at) < scanTTL {
		return c.hits
	}
	var hits []string
	seen := 0
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		if depth > scanDepth || seen >= scanCap {
			return
		}
		seen++
		abs := filepath.Join(root, filepath.FromSlash(rel))
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		hit := false
		var subs []string
		for _, e := range entries {
			n := e.Name()
			switch {
			case n == FileName && e.Type().IsRegular():
				hit = true
			case n == ".agents" && e.IsDir():
				if st, err := os.Stat(filepath.Join(abs, ".agents", "skills")); err == nil && st.IsDir() {
					hit = true
				}
			case e.IsDir() && !strings.HasPrefix(n, ".") && !kb.ExcludeDirs[n]:
				subs = append(subs, n)
			}
		}
		if hit {
			hits = append(hits, rel)
		}
		sort.Strings(subs)
		for _, n := range subs {
			walk(path.Join(rel, n), depth+1)
		}
	}
	walk("", 0)
	c.root, c.at, c.hits = root, time.Now(), hits
	return hits
}

// forget drops the cached scan, after a write changed what it found.
func (s *Service) forget() {
	s.scan.mu.Lock()
	s.scan.at = time.Time{}
	s.scan.mu.Unlock()
}

/* ---- the record of folders in people's own libraries ---- */

// A person's own library is on a cluster and read as them, so it is not
// walked: the Studio keeps a record of the folders it wrote in there, per
// person, and reads each one again when it lists them.

type spaceRecord struct {
	Owner   string `json:"owner"`
	Library string `json:"library"`
	Folder  string `json:"folder"`
}

func (s *Service) recordFile() string {
	return filepath.Join(s.A.Cfg.IndexBase, "agents-md", "space-folders.json")
}

func (s *Service) records() []spaceRecord {
	var out []spaceRecord
	if b, err := os.ReadFile(s.recordFile()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func (s *Service) saveRecords(rs []spaceRecord) {
	f := s.recordFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return
	}
	b, _ := json.MarshalIndent(rs, "", " ")
	tmp := f + ".part"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, f)
	}
}

func owner(r *http.Request) string {
	if u := auth.UserOf(r); u != nil && u.ID != "" {
		return u.ID
	}
	return "single"
}

// note keeps or drops a folder of the person's own library in the record:
// kept while the Studio has something there.
func (s *Service) note(r *http.Request, t Target, st State) {
	if !t.Space {
		s.forget()
		return
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	who := owner(r)
	keep := st.Exists && len(st.Instructions) > 0 || len(st.Skills) > 0
	var out []spaceRecord
	found := false
	for _, rec := range s.records() {
		if rec.Owner == who && rec.Library == t.Library && rec.Folder == t.Folder {
			found = true
			if !keep {
				continue
			}
		}
		out = append(out, rec)
	}
	if keep && !found {
		out = append(out, spaceRecord{who, t.Library, t.Folder})
	}
	if keep == found {
		return
	}
	s.saveRecords(out)
}

// Carriers is every folder the person can see with instructions or skills
// the Studio put there: those the scan of the main library finds, then
// those of their own library the record names, each read again.
func (s *Service) Carriers(r *http.Request) ([]Carrying, error) {
	out := []Carrying{}
	if kb.Default != nil {
		if main, ok, err := kb.Default.Libs.Get(""); err == nil && ok && !main.Elsewhere() {
			for _, rel := range s.scanMain(s.A.Cfg.KBRoot) {
				f := Local{Root: s.A.Cfg.KBRoot, Rel: rel, Label: main.Label}
				st, err := Read(f)
				if err != nil || len(st.Instructions)+len(st.Skills) == 0 {
					continue
				}
				out = append(out, Carrying{Target{Library: main.ID, Label: main.Label, Folder: rel, Path: strings.TrimPrefix(rel+"/"+FileName, "/")}, st})
			}
		}
	}
	who := owner(r)
	for _, rec := range s.records() {
		if rec.Owner != who {
			continue
		}
		f, t, err := s.Folder(r, rec.Library, rec.Folder)
		if err != nil || !t.Space {
			continue
		}
		st, err := Read(f)
		if err != nil || len(st.Instructions)+len(st.Skills) == 0 {
			continue
		}
		out = append(out, Carrying{t, st})
	}
	return out, nil
}
