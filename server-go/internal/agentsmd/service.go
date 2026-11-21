package agentsmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// Service resolves the folders instructions go into and the templates
// they are made from.
type Service struct {
	A     *app.App
	Exts  *extensions.Set
	scan  scanCache
	recMu sync.Mutex
}

// New is the service for a Studio.
func New(a *app.App) *Service {
	return &Service{A: a, Exts: extensions.New(a.Cfg.IndexBase, a.Cfg.ProjectRoot, a.Cfg.KBRoot)}
}

// Target is a folder as the routes report it.
type Target struct {
	Library string `json:"library"`
	Label   string `json:"label"`
	Folder  string `json:"folder"`
	// Path is AGENTS.md's path in the library.
	Path string `json:"path"`
	// Space: the person's own library, written as them.
	Space bool `json:"space"`
}

// Folder is the folder rel of the library id ("" for the main library)
// for the person r is from, with the rights the Library has there: the
// main library when it is writable on this host, written as the Library
// writes it, or the person's own library, written as them over their
// own session. Any other library is refused.
func (s *Service) Folder(r *http.Request, id, rel string) (Folder, Target, error) {
	rel, err := CleanFolder(rel)
	if err != nil {
		return nil, Target{}, err
	}
	t := Target{Folder: rel, Path: strings.TrimPrefix(rel+"/"+FileName, "/")}
	if kb.Default == nil {
		return nil, t, apierr.New(409, "there is no library here")
	}
	main, ok, err := kb.Default.Libs.Get("")
	if err != nil {
		return nil, t, err
	}
	id = strings.TrimSpace(id)
	if id == "" || (ok && id == main.ID) {
		if !ok {
			return nil, t, apierr.New(409, "the main library is not set up here")
		}
		if err := kb.RequireWritable(main); err != nil {
			return nil, t, err
		}
		t.Library, t.Label = main.ID, main.Label
		ctx := context.WithoutCancel(r.Context())
		return Local{Root: s.A.Cfg.KBRoot, Rel: rel, Label: main.Label, Index: func(rels []string) { s.index(ctx, rels) }}, t, nil
	}
	if kb.LibraryResolver == nil {
		return nil, t, apierr.New(404, "no library named %s", id)
	}
	lib, err := kb.LibraryResolver.Library(r, id)
	if err != nil {
		return nil, t, err
	}
	t.Library, t.Label = lib.ID, lib.Label
	switch {
	case lib.Space && lib.SharedFrom == "":
		t.Space = true
		return Space{R: r, Lib: lib, Rel: rel}, t, nil
	case lib.Space:
		return nil, t, apierr.New(403, "%s is shared with you by %s; agent instructions go into your own library or the main library", lib.Label, lib.SharedFrom)
	}
	return nil, t, apierr.New(409, "agent instructions go into the main library or My library, and %s is neither", lib.Label)
}

// index indexes the library files that changed; hidden ones (a skill
// under .agents) are not indexed.
func (s *Service) index(ctx context.Context, rels []string) {
	for _, rel := range rels {
		hidden := false
		for _, p := range strings.Split(rel, "/") {
			hidden = hidden || strings.HasPrefix(p, ".")
		}
		if hidden {
			continue
		}
		if _, err := kb.ReindexForFile(ctx, kb.ProviderIndexer{A: s.A}, rel); err != nil {
			s.A.Log.Warn("agent instructions: indexing " + rel + ": " + err.Error())
		}
	}
}

// Instructions is a template's normalized name and its text.
func (s *Service) Instructions(name string) (string, string, error) {
	want := extensions.NormalizeName(name)
	for _, a := range s.Exts.AgentFiles() {
		if a.Name != want {
			continue
		}
		b, err := os.ReadFile(a.File)
		if err != nil {
			return "", "", err
		}
		return want, extensions.StripFrontmatter(string(b)), nil
	}
	return "", "", apierr.New(404, "no agent instructions named %s", name)
}

// SkillText is a skill's normalized name, description and text.
func (s *Service) SkillText(name string) (string, string, string, error) {
	want := extensions.NormalizeName(name)
	for _, k := range s.Exts.Skills() {
		if k.Name != want {
			continue
		}
		body, ok := s.Exts.SkillBody(want)
		if !ok {
			break
		}
		return want, k.Description, extensions.StripFrontmatter(body), nil
	}
	return "", "", "", apierr.New(404, "no skill named %s", name)
}

// ApplyNamed writes the named instructions, and each named skill, into f.
func (s *Service) ApplyNamed(f Folder, instructions string, skills []string) error {
	if strings.TrimSpace(instructions) != "" {
		name, body, err := s.Instructions(instructions)
		if err != nil {
			return err
		}
		if err := ApplyInstructions(f, name, body); err != nil {
			return wrap(err)
		}
	}
	for _, k := range skills {
		name, desc, body, err := s.SkillText(k)
		if err != nil {
			return err
		}
		if err := ApplySkill(f, name, desc, body); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// wrap gives a plain error the 409 the routes answer it with.
func wrap(err error) error {
	if _, ok := err.(*apierr.Error); ok {
		return err
	}
	return apierr.New(409, "%s", err.Error())
}

// Register adds the routes:
//
//	GET  /api/agents-md?library=&folder=   what the Studio has put there
//	POST /api/agents-md/apply              {library, folder, instructions, skills}
//	POST /api/agents-md/remove             {library, folder, instructions, skills}
func Register(mux *http.ServeMux, a *app.App) {
	s := New(a)
	Default = s
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(pattern, apierr.Handler(a.Log, fn))
	}
	h("GET /api/agents-md", func(w http.ResponseWriter, r *http.Request) error {
		q := r.URL.Query()
		f, t, err := s.Folder(r, q.Get("library"), q.Get("folder"))
		if err != nil {
			return err
		}
		return s.answer(w, r, f, t)
	})
	h("GET /api/agents-md/folders", func(w http.ResponseWriter, r *http.Request) error {
		list, err := s.Carriers(r)
		if err != nil {
			return err
		}
		return reqbody.OK(w, map[string]any{"folders": list})
	})
	h("POST /api/agents-md/apply", func(w http.ResponseWriter, r *http.Request) error {
		b, err := readChange(r)
		if err != nil {
			return err
		}
		f, t, err := s.Folder(r, b.Library, b.Folder)
		if err != nil {
			return err
		}
		if err := s.ApplyNamed(f, b.Instructions, b.Skills); err != nil {
			return err
		}
		return s.answer(w, r, f, t)
	})
	h("POST /api/agents-md/remove", func(w http.ResponseWriter, r *http.Request) error {
		b, err := readChange(r)
		if err != nil {
			return err
		}
		f, t, err := s.Folder(r, b.Library, b.Folder)
		if err != nil {
			return err
		}
		if n := strings.TrimSpace(b.Instructions); n != "" {
			if _, err := RemoveInstructions(f, extensions.NormalizeName(n)); err != nil {
				return wrap(err)
			}
		}
		for _, k := range b.Skills {
			if _, err := RemoveSkill(f, extensions.NormalizeName(k)); err != nil {
				return wrap(err)
			}
		}
		return s.answer(w, r, f, t)
	})
}

// Default is the Studio's service once Register has run.
var Default *Service

type change struct {
	Library      string   `json:"library"`
	Folder       string   `json:"folder"`
	Instructions string   `json:"instructions"`
	Skills       []string `json:"skills"`
}

func readChange(r *http.Request) (change, error) {
	var b change
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16+1))
	if err != nil {
		return b, err
	}
	if len(raw) > 1<<16 {
		return b, apierr.New(413, "Request body is too large")
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, apierr.New(400, "a JSON body with library, folder, and instructions or skills")
	}
	if strings.TrimSpace(b.Instructions) == "" && len(b.Skills) == 0 {
		return b, apierr.New(400, "instructions or skills: what to apply or take off")
	}
	if len(b.Skills) > 20 {
		return b, apierr.New(400, "at most 20 skills at a time")
	}
	return b, nil
}

func (s *Service) answer(w http.ResponseWriter, r *http.Request, f Folder, t Target) error {
	st, err := Read(f)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPost {
		s.note(r, t, st)
	}
	return reqbody.OK(w, struct {
		Target
		State
	}{t, st})
}
