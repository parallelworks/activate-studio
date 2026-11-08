package libapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// kbResolver answers internal/kb for the libraries a request can read:
// the viewer's own on their systems, and the files of a library on another
// system or read through another host.
type kbResolver struct{ s *Service }

func toKB(l libraries.Library) kb.Library {
	return kb.Library{
		ID: l.ID, Label: l.Label, IndexRoot: l.IndexRoot, SourceRoot: l.SourceRoot, CacheRoot: l.CacheRoot,
		Writable: l.Writable, Primary: l.Primary, Pinned: l.Pinned, Indexed: l.Caps.Index,
		ReadThrough: l.ReadThrough != nil, Site: l.Site,
	}
}

// full is the registry's library a kb library stands for.
func (k kbResolver) full(r *http.Request, lib kb.Library) (libraries.Library, libraries.Viewer, error) {
	viewer, err := k.s.viewer(r)
	if err != nil {
		return libraries.Library{}, nil, err
	}
	l, err := k.s.Reg.Get(lib.ID, k.s.kbLabel(), viewer)
	return l, viewer, err
}

func (k kbResolver) Library(r *http.Request, id string) (kb.Library, error) {
	viewer, err := k.s.viewer(r)
	if err != nil {
		return kb.Library{}, err
	}
	l, err := k.s.Reg.Get(id, k.s.kbLabel(), viewer)
	if err != nil {
		return kb.Library{}, err
	}
	return toKB(l), nil
}

func (k kbResolver) All(r *http.Request) ([]kb.Library, error) {
	viewer, err := k.s.viewer(r)
	if err != nil {
		return nil, err
	}
	libs, _, err := k.s.Reg.Scope([]string{"all"}, viewer, k.s.kbLabel())
	if err != nil {
		return nil, err
	}
	out := make([]kb.Library, len(libs))
	for i, l := range libs {
		out[i] = toKB(l)
	}
	return out, nil
}

func (k kbResolver) Tree(r *http.Request, lib kb.Library, rel string) ([]kb.Entry, error) {
	return k.s.Engine.RemoteTree(r.Context(), lib.IndexRoot, rel)
}

func (k kbResolver) File(r *http.Request, lib kb.Library, rel string) (*kb.Obj, error) {
	l, viewer, err := k.full(r, lib)
	if err != nil {
		return nil, err
	}
	if l.ReadThrough != nil {
		o, err := k.s.OpenOriginal(r.Context(), l, rel, viewer)
		if err != nil {
			return nil, err
		}
		return o.File, nil
	}
	if l.Site == "" {
		return nil, apierr.New(409, "%s has no files on this host; it can be searched and described but not opened", l.Label)
	}
	return k.s.Engine.RemoteFile(r.Context(), l.IndexRoot, rel)
}

func (k kbResolver) Download(r *http.Request, lib kb.Library, rel string) (string, []byte, error) {
	l, viewer, err := k.full(r, lib)
	if err != nil {
		return "", nil, err
	}
	if l.ReadThrough != nil {
		return k.s.DownloadOriginal(r.Context(), l, rel, viewer)
	}
	return k.s.Engine.RemoteDownload(r.Context(), l.IndexRoot, rel)
}

func (k kbResolver) Aggregate(ctx context.Context, indexRoot string, stages []string) (string, error) {
	args := append([]string{"-n", strconv.Itoa(search.LocalThreads()), "-d", search.Delim, "-a", "1"}, stages...)
	return k.s.Engine.RemoteQuery(ctx, indexRoot, args, "")
}

func (k kbResolver) BuiltAt(indexRoot string) string {
	t, err := k.s.Engine.Target(indexRoot)
	if err != nil || t == nil {
		return ""
	}
	return t.BuiltAt
}
