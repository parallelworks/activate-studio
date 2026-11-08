package libapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The write routes of routes.ts's libraries section: adding an index the
// Studio reads, removing a library, and the depth of one it builds.

func (s *Service) addLibrary(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	def := libraries.Def{ID: strings.ToLower(jsTrim(reqbody.JSString(body.Get("id")))), IndexRoot: reqbody.JSString(body.Get("indexRoot"))}
	if v, ok := body.Get("label").(string); ok {
		def.Label = v
	}
	if v := body.Get("sourceRoot"); reqbody.Truthy(v) {
		src := reqbody.JSString(v)
		def.SourceRoot = &src
	}
	lib, err := s.Reg.Add(r.Context(), def)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, struct {
		Library libraries.SettingsLibrary `json:"library"`
	}{s.Reg.SettingsView(lib)})
	return nil
}

func (s *Service) removeLibrary(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if err := s.Reg.Remove(id); err != nil {
		return err
	}
	apierr.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// builtLibrary is a library the Studio builds, by the route's id: 404 for
// none, 409 for an index it only reads.
func (s *Service) builtLibrary(r *http.Request) (string, error) {
	viewer, err := s.viewer(r)
	if err != nil {
		return "", err
	}
	id := r.PathValue("id")
	for _, l := range s.Reg.List(s.kbLabel(), viewer) {
		if l.ID != id {
			continue
		}
		if l.Depth == "" {
			return "", apierr.New(409, "%s is an index the Studio reads but does not build, so it stays as it was built.", l.Label)
		}
		return id, nil
	}
	return "", apierr.New(404, "There is no such library.")
}

func (s *Service) depthChanged(id string) {
	if id == libraries.PrimaryID {
		s.ix().RequestSweep(2 * time.Second)
		return
	}
	s.builds.requestSync(id, 2*time.Second)
}

type depthAnswer struct {
	ID      string `json:"id"`
	Depth   string `json:"depth"`
	Meaning string `json:"meaning"`
}

type meaningAnswer struct {
	ID      string `json:"id"`
	Meaning string `json:"meaning"`
	Depth   string `json:"depth"`
}

func (s *Service) setDepth(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	depth := body.Get("depth")
	if !index.IsDepth(depth) {
		return apierr.New(400, "depth: names, text, ocr, meaning-documents or meaning-full")
	}
	id, err := s.builtLibrary(r)
	if err != nil {
		return err
	}
	if _, err := s.ix().Settings.Save(id, map[string]any{"depth": depth}); err != nil {
		return err
	}
	s.depthChanged(id)
	st := s.ix().Settings.Get(id)
	apierr.JSON(w, 200, depthAnswer{id, string(st.Depth), string(st.Meaning)})
	return nil
}

func (s *Service) setMeaning(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	level := body.Get("level")
	if !index.IsMeaningLevel(level) {
		return apierr.New(400, "level: off, documents or full")
	}
	id, err := s.builtLibrary(r)
	if err != nil {
		return err
	}
	if _, err := s.ix().Settings.Save(id, map[string]any{"meaning": level}); err != nil {
		return err
	}
	s.depthChanged(id)
	st := s.ix().Settings.Get(id)
	apierr.JSON(w, 200, meaningAnswer{id, string(st.Meaning), string(st.Depth)})
	return nil
}
