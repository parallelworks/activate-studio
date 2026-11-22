// Package delegation hands a piece of work to pw code: one session in a
// library folder, with the chosen agent instructions written into that
// folder's AGENTS.md and the goal set, so pw code works until the goal is
// met and does its own splitting. The Studio keeps a record of each
// session it started this way, watches it, and when it finishes indexes
// the folder and lists what the agent wrote. The sessions themselves are
// on the Code page, which marks these.
package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// Record is a session the Studio started for a delegated piece of work.
type Record struct {
	Owner     string `json:"owner"`
	Site      string `json:"site"`
	Session   string `json:"session"`
	Workspace string `json:"workspace"`
	// Folder is the library folder it works in, "" when it works outside
	// the library.
	Folder       string `json:"folder,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Objective    string `json:"objective"`
	Goal         string `json:"goal,omitempty"`
	Started      string `json:"started"`
	// State is working, finished (settled with no error), stopped (on an
	// error, which Error gives), or waiting (for the person's approval).
	State    string   `json:"state"`
	Finished string   `json:"finished,omitempty"`
	Error    string   `json:"error,omitempty"`
	Files    []string `json:"files,omitempty"`
	// More is how many files there are beyond Files.
	More int `json:"more,omitempty"`
}

// Start is what to start.
type Start struct {
	Site, Key, Objective, Goal, Model, PermissionMode string
	MaxTurns                                          float64
	// Instructions is an agent instructions template, applied to the
	// folder's AGENTS.md; "" for none.
	Instructions string
	// Folder is a library folder to work in; "" for a new one under
	// agents/<instructions>/.
	Folder string
}

// Service starts, records and watches delegated sessions.
type Service struct {
	A     *app.App
	Sites func() *agentsites.Sites
	// Poll is how often a watched session is looked at.
	Poll time.Duration
	// Watch is how long a session is watched before the Studio stops
	// looking; the record says it is still working, and the next look at
	// it (agent_status, the Code page) picks it up again.
	Watch time.Duration

	mu       sync.Mutex
	watching map[string]bool
}

// Default is the service Register made.
var Default *Service

// New is a service over a's library and the registered agent sites.
func New(a *app.App) *Service {
	return &Service{A: a, Sites: agentsites.Current, Poll: 20 * time.Second, Watch: 12 * time.Hour, watching: map[string]bool{}}
}

func owner(r *http.Request) string {
	if u := auth.UserOf(r); u != nil && u.ID != "" {
		return u.ID
	}
	return "single"
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// folderFor is a new folder under agents/<who>/ for an objective: the
// date and time and its first words.
func folderFor(who, objective string, now time.Time) string {
	words := strings.Fields(slugRe.ReplaceAllString(strings.ToLower(objective), " "))
	if len(words) > 5 {
		words = words[:5]
	}
	slug := strings.Join(words, "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	name := now.UTC().Format("20060102-1504")
	if slug != "" {
		name += "-" + slug
	}
	return "agents/" + who + "/" + name
}

// Start starts a session for the work: instructions into the folder's
// AGENTS.md first, then the session there with the objective and goal.
// Where the site does not see the library, the session works in a
// folder of its own there and the instructions open its brief instead.
func (s *Service) Start(r *http.Request, o Start) (*Record, error) {
	sites := s.Sites()
	if sites == nil {
		return nil, apierr.New(503, "pw code is not set up on this Studio")
	}
	auth := s.A.Cfg.AuthEnabled()
	objective := strings.TrimSpace(o.Objective)
	if objective == "" {
		return nil, apierr.New(400, "an objective is required")
	}
	name, body := "", ""
	if o.Instructions != "" {
		if agentsmd.Default == nil {
			return nil, apierr.New(503, "agent instructions are not set up on this Studio")
		}
		n, t, err := agentsmd.Default.Instructions(o.Instructions)
		if err != nil {
			return nil, err
		}
		name, body = n, t
	}
	who := name
	if who == "" {
		who = "delegated"
	}
	rel := o.Folder
	named := rel != ""
	if named {
		c, err := agentsmd.CleanFolder(rel)
		if err != nil || c == "" {
			return nil, apierr.New(400, "folder: a folder in the library, relative to its top, with no .. or dot folders")
		}
		rel = c
	} else {
		rel = folderFor(who, objective, time.Now())
	}
	start := agentsites.StartAgent{Site: o.Site, Key: o.Key, Objective: objective, Goal: o.Goal, MaxTurns: o.MaxTurns,
		Model: o.Model, PermissionMode: o.PermissionMode, Library: s.A.Cfg.KBRoot, Folder: rel}
	// The instructions go into the folder before the session is made, so
	// its first turn reads them. The folder is written as the person, like
	// any Library write; where they cannot write it, the session works
	// outside the library.
	//
	// applied is the folder only when these instructions were not in it
	// before, so a start that fails takes out only what it put in, never
	// instructions the person had applied there; made says the folder
	// itself was new.
	var applied agentsmd.Folder
	made := false
	inLibrary := true
	if name != "" {
		f, _, err := agentsmd.Default.FolderToWrite(r, "", rel)
		had := false
		if err == nil {
			had = hasInstructions(f, name)
			_, serr := os.Stat(filepath.Join(s.A.Cfg.KBRoot, filepath.FromSlash(rel)))
			made = os.IsNotExist(serr)
			err = agentsmd.Default.ApplyNamed(f, name, nil)
		}
		if err == nil && had {
			f = nil
		}
		switch {
		case err == nil:
			applied = f
		case named:
			return nil, err
		default:
			inLibrary = false
		}
	} else if agentsmd.Default == nil {
		inLibrary = false
	} else if _, _, err := agentsmd.Default.FolderToWrite(r, "", rel); err != nil {
		if named {
			return nil, err
		}
		inLibrary = false
	}
	var st *jsonv.Object
	var err error
	if inLibrary {
		st, err = sites.Start(r.Context(), start, auth)
		var ae *apierr.Error
		if err != nil && !named && errors.As(err, &ae) && ae.Status == 400 && strings.Contains(ae.Message, "does not see") {
			inLibrary = false
		}
	}
	if !inLibrary {
		if applied != nil {
			// The site does not see the library: take the instructions out
			// of the folder they went into, and the folder when that left
			// it empty.
			agentsmd.RemoveInstructions(applied, name)
			if made {
				dir := filepath.Join(s.A.Cfg.KBRoot, filepath.FromSlash(rel))
				if os.Remove(dir) == nil {
					os.Remove(filepath.Dir(dir))
				}
			}
		}
		st, err = s.startOutside(r, sites, start, body)
	}
	if err != nil {
		if applied != nil && inLibrary {
			agentsmd.RemoveInstructions(applied, name)
			if made {
				os.Remove(filepath.Join(s.A.Cfg.KBRoot, filepath.FromSlash(rel)))
			}
		}
		return nil, err
	}
	folder := ""
	if v, ok := st.Get("library"); ok {
		folder, _ = v.(string)
	}
	rec := &Record{Owner: owner(r), Site: text(jsonv.Path(st, "site")), Session: text(jsonv.Path(st, "session")),
		Workspace: text(jsonv.Path(st, "workspace")), Folder: folder, Instructions: name, Objective: objective,
		Goal: strings.TrimSpace(o.Goal), Started: time.Now().UTC().Format(time.RFC3339), State: "working"}
	s.save(rec)
	s.watch(rec.Session, o.Key)
	return rec, nil
}

// hasInstructions says whether f's AGENTS.md already carries the named
// instructions. When it cannot be read it says yes, so nothing the person
// may have put there is taken out.
func hasInstructions(f agentsmd.Folder, name string) bool {
	b, found, err := f.Read(agentsmd.FileName)
	if err != nil {
		return true
	}
	if !found {
		return false
	}
	names, err := agentsmd.Applied(string(b))
	return err != nil || slices.Contains(names, name)
}

// startOutside starts the session where the site does not see the
// library, with the instructions at the top of its brief.
func (s *Service) startOutside(r *http.Request, sites *agentsites.Sites, o agentsites.StartAgent, text string) (*jsonv.Object, error) {
	o.Library, o.Folder = "", ""
	if text != "" {
		o.Objective = "Work by these instructions:\n\n" + text + "\n\n---\n\n" + o.Objective
	}
	return sites.Start(r.Context(), o, s.A.Cfg.AuthEnabled())
}

/* ---- the record ---- */

func (s *Service) file() string {
	return filepath.Join(s.A.Cfg.IndexBase, "delegation", "sessions.json")
}

// maxRecords is how many records are kept; the oldest go first.
const maxRecords = 500

func (s *Service) load() []Record {
	var out []Record
	if b, err := os.ReadFile(s.file()); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

func (s *Service) store(rs []Record) {
	if len(rs) > maxRecords {
		rs = rs[len(rs)-maxRecords:]
	}
	f := s.file()
	if os.MkdirAll(filepath.Dir(f), 0o700) != nil {
		return
	}
	b, _ := json.MarshalIndent(rs, "", " ")
	tmp := f + ".part"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, f)
	}
}

// save adds the record, or replaces the one for its session.
func (s *Service) save(rec *Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.load()
	for i := range rs {
		if rs[i].Session == rec.Session && rs[i].Site == rec.Site {
			rs[i] = *rec
			s.store(rs)
			return
		}
	}
	s.store(append(rs, *rec))
}

func (s *Service) get(session string) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.load() {
		if r.Session == session {
			return &r
		}
	}
	return nil
}

// Mine is the person's records, newest first.
func (s *Service) Mine(r *http.Request) []Record {
	s.mu.Lock()
	rs := s.load()
	s.mu.Unlock()
	who := owner(r)
	out := []Record{}
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Owner == who {
			out = append(out, rs[i])
		}
	}
	return out
}

/* ---- watching ---- */

// Settled is whether a session's status (agentsites Status) means it is
// not working: idle with no active goal, stopped on an error, or waiting
// for the person.
func Settled(st *jsonv.Object) bool {
	switch text(jsonv.Path(st, "status")) {
	case "running":
		return false
	case "awaiting_approval", "live_elsewhere":
		return true
	}
	if len(jsonv.Arr(jsonv.Path(st, "approvals"))) > 0 {
		return true
	}
	g := jsonv.Obj(jsonv.Path(st, "goal"))
	if g == nil {
		return true
	}
	active, _ := g.Get("active")
	paused, _ := g.Get("paused")
	return active != true || paused == true
}

// watch looks at the session until it settles, then finishes its record.
// A session already watched is left to that watcher.
func (s *Service) watch(session, key string) {
	s.mu.Lock()
	if s.watching[session] {
		s.mu.Unlock()
		return
	}
	s.watching[session] = true
	s.mu.Unlock()
	s.A.Go("delegation watch "+session, func(ctx context.Context) {
		defer func() {
			s.mu.Lock()
			delete(s.watching, session)
			s.mu.Unlock()
		}()
		until := time.Now().Add(s.Watch)
		for time.Now().Before(until) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.Poll):
			}
			if done := s.Look(ctx, session, key); done {
				return
			}
		}
	})
}

// Look reads the session's status once and, when it has settled,
// finishes its record: the folder indexed and its files listed. It
// reports whether the record is finished.
func (s *Service) Look(ctx context.Context, session, key string) bool {
	rec := s.get(session)
	sites := s.Sites()
	if rec == nil || sites == nil {
		return true
	}
	st, err := sites.Status(ctx, rec.Site, key, session, 1, s.A.Cfg.AuthEnabled())
	if err != nil {
		if notFound(err) {
			rec.State, rec.Finished = "gone", time.Now().UTC().Format(time.RFC3339)
			s.save(rec)
			return true
		}
		return false
	}
	if !Settled(st) {
		if rec.State != "working" {
			rec.State = "working"
			s.save(rec)
		}
		return false
	}
	// A session still waiting for an approval was finished when it began
	// to wait: look again on the next poll, without indexing the folder
	// and rewriting the record each time.
	if rec.State == "waiting" && waiting(st) {
		return false
	}
	s.finish(ctx, rec, st)
	return rec.State != "waiting"
}

// waiting says whether a settled session waits for a person to approve a
// call.
func waiting(st *jsonv.Object) bool {
	return text(jsonv.Path(st, "status")) == "awaiting_approval" || len(jsonv.Arr(jsonv.Path(st, "approvals"))) > 0
}

// notFound says whether err is the session not being there: this host's
// daemon answers *pwcode.DaemonError, the platform *apierr.Error.
func notFound(err error) bool {
	var de *pwcode.DaemonError
	if errors.As(err, &de) && de.Status == http.StatusNotFound {
		return true
	}
	var ae *apierr.Error
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func (s *Service) finish(ctx context.Context, rec *Record, st *jsonv.Object) {
	rec.Error = text(jsonv.Path(st, "lastError"))
	switch {
	case waiting(st):
		rec.State = "waiting"
	case rec.Error != "":
		rec.State = "stopped"
	default:
		rec.State = "finished"
	}
	if rec.State != "waiting" {
		rec.Finished = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.Folder != "" {
		kb.ReindexForDir(ctx, kb.ProviderIndexer{A: s.A}, rec.Folder)
		rec.Files, rec.More = s.files(rec.Folder)
	}
	s.save(rec)
}

// maxFiles is how many of a folder's files a record lists.
const maxFiles = 50

// files is what is in a library folder, newest first, leaving out hidden
// files and the AGENTS.md the Studio wrote.
func (s *Service) files(rel string) ([]string, int) {
	root := s.A.Cfg.KBRoot
	dir := filepath.Join(root, filepath.FromSlash(rel))
	type file struct {
		rel string
		mod time.Time
	}
	var all []file
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || p == filepath.Join(dir, agentsmd.FileName) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sub, _ := filepath.Rel(root, p)
		all = append(all, file{filepath.ToSlash(sub), info.ModTime()})
		return nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	out := []string{}
	for _, f := range all[:min(len(all), maxFiles)] {
		out = append(out, f.rel)
	}
	return out, len(all) - len(out)
}

// Resume watches the local sessions whose records say they are still
// working, after a restart. Sessions on clusters need the person's key,
// which is not kept; they are looked at again when the person asks.
func (s *Service) Resume() {
	s.mu.Lock()
	rs := s.load()
	s.mu.Unlock()
	for _, r := range rs {
		if r.State == "working" && r.Site == agentsites.Local {
			s.watch(r.Session, "")
		}
	}
}

// text is a string read from JSON, "" for anything else (absent, null).
func text(v any) string {
	s, _ := v.(string)
	return s
}
