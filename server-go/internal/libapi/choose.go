package libapi

import (
	"net/http"
	"path/filepath"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/folders"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// What Add a library chooses from on this computer, on a Studio that does
// not sign people in: a folder's folders, what a folder holds, and whether
// a folder is an index.

// localFolders lists the folders in one directory of this computer
// (GET ?path=&filter=).
func (s *Service) localFolders(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBuild)
	}
	q := r.URL.Query()
	dir := q.Get("path")
	if dir != "" {
		dir = expand(dir)
	}
	l := folders.Local(dir)
	if l == nil {
		return apierr.New(400, "%s is not a folder the Studio can read.", q.Get("path"))
	}
	apierr.JSON(w, 200, l.Filter(q.Get("filter")))
	return nil
}

// localCount is what a folder of this computer holds, counted for a few
// seconds at most; null where it is not counted.
func (s *Service) localCount(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBuild)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	var count *folders.Count
	if typed := jsTrim(body.Str("sourceRoot")); typed != "" && !hasControl(typed) {
		count = folders.CountLocal(r.Context(), expand(typed))
	}
	apierr.JSON(w, 200, struct {
		Count *folders.Count `json:"count"`
	}{count})
	return nil
}

// indexCheck is whether a folder of this computer is an index that can be
// connected as it is, and what search covers in it.
func (s *Service) indexCheck(w http.ResponseWriter, r *http.Request) error {
	if s.authEnabled(r) {
		return apierr.New(403, "%s", deploymentLibraries)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	typed, source := jsTrim(body.Str("indexRoot")), jsTrim(body.Str("sourceRoot"))
	if typed == "" || hasControl(typed) || hasControl(source) {
		return apierr.New(400, "index directory: the top of a GUFI index")
	}
	steps := []buildStep{}
	step := func(name string, ok bool, detail string) {
		steps = append(steps, buildStep{Name: name, OK: ok, Detail: detail})
	}
	dir := expand(typed)
	var fullText, vectors bool
	switch {
	case !exists(dir):
		step("index", false, dir+" does not exist.")
	case !isDir(dir):
		step("index", false, dir+" is a file, not a folder.")
	default:
		caps, err := s.Reg.Probe(r.Context(), dir)
		if err != nil {
			return err
		}
		fullText, vectors = caps.FullText, caps.Vectors
		if caps.Index {
			step("index", true, dir+" is a GUFI index.")
		} else {
			step("index", false, dir+" is not the top of a GUFI index: it has no db.db. Choose the folder gufi_dir2index made.")
		}
	}
	if source != "" {
		if src := expand(source); !isDir(src) {
			step("source", false, src+" is not a folder.")
		} else {
			step("source", true, filepath.Clean(src)+" is there; files open from it.")
		}
	}
	ok := true
	for _, st := range steps {
		ok = ok && st.OK
	}
	apierr.JSON(w, 200, struct {
		OK       bool        `json:"ok"`
		Steps    []buildStep `json:"steps"`
		FullText bool        `json:"fullText"`
		Vectors  bool        `json:"vectors"`
	}{ok, steps, fullText, vectors})
	return nil
}
