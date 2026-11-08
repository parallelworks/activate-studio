package libapi

import (
	"net/http"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// details is GET /api/kb/details (libraryDetails.ts).
func (s *Service) details(w http.ResponseWriter, r *http.Request) error {
	qs := r.URL.Query()
	viewer, err := s.viewer(r)
	if err != nil {
		return err
	}
	label := s.kbLabel()
	var lib libraries.Library
	var sub string
	if strings.ToLower(first(qs, "library")) == "all" {
		rel, err := search.CleanDetailsPath(first(qs, "path"))
		if err != nil {
			return err
		}
		parts := strings.Split(rel, "/")
		libs, _, err := s.Reg.Scope([]string{"all"}, viewer, label)
		if err != nil {
			return err
		}
		found := false
		for _, l := range libs {
			if l.ID == parts[0] {
				lib, found = l, true
				break
			}
		}
		if !found {
			return apierr.New(404, "no library named %s", parts[0])
		}
		sub = strings.Join(parts[1:], "/")
	} else {
		id := first(qs, "library")
		if _, given := qs["library"]; !given {
			id = libraries.PrimaryID
		}
		if lib, err = s.Reg.Get(id, label, viewer); err != nil {
			return err
		}
		if sub, err = search.CleanDetailsPath(first(qs, "path")); err != nil {
			return err
		}
	}
	if !lib.Caps.Index && lib.Site == "" {
		return apierr.New(409, "%s has no index yet", lib.Label)
	}
	d, err := s.Engine.Describe(r.Context(), lib, sub, viewer, first(qs, "count") == "1", s.authEnabled(r))
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, d)
	return nil
}

// topology is GET /api/kb/topology and GET /api/libraries/:id/topology.
func (s *Service) topology(w http.ResponseWriter, r *http.Request) error {
	qs := r.URL.Query()
	var lib *libraries.Library
	if id := r.PathValue("id"); id != "" && id != libraries.PrimaryID {
		viewer, err := s.viewer(r)
		if err != nil {
			return err
		}
		l, err := s.Reg.Get(id, "", viewer)
		if err != nil {
			return err
		}
		lib = &l
	}
	t, err := s.Engine.Topology(r.Context(), lib, first(qs, "dir"), first(qs, "refresh") == "1")
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, t)
	return nil
}
