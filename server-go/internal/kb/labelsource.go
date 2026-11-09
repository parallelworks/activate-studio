package kb

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// What label suggestions read in a library other than the main one: its
// folders, a file's opening text and the labels it carries, through the
// same paths as the tree, the file view and GET /api/kb/tags.

// LabelLibrary is the library and path that library and rel name for the
// request's viewer, as POST /api/kb/tags reads them: "" is the main
// library, "all" takes the library from the path's first part.
func (m *Module) LabelLibrary(r *http.Request, library, rel string) (Library, string, error) {
	return m.labelLibNamed(r, strings.ToLower(JSTrim(library)), rel)
}

// LibraryListing is one folder of lib as the tree lists it, dot entries
// left out: from the index on another system for a library there, else
// from its folder on this host. A library with neither is a 409.
func (m *Module) LibraryListing(r *http.Request, lib Library, rel string) ([]Entry, error) {
	if lib.Site != "" {
		return m.remoteTree(r, lib, rel, false)
	}
	root, err := m.source(lib)
	if err != nil {
		return nil, err
	}
	return ListDir(rel, root, false)
}

// LibraryFileText is the text the file view shows for rel in lib: read on
// the other system or through the other host for a library there, else
// from the folder here with the library's extraction cache. The extracted
// text a library here keeps comes first, as labeling.ts reads it.
func (m *Module) LibraryFileText(r *http.Request, lib Library, rel string) string {
	var o *Obj
	var err error
	if lib.ReadThrough || lib.Site != "" {
		o, err = m.remoteFile(r, lib, rel)
	} else {
		if b, rerr := os.ReadFile(filepath.Join(m.cacheRoot(lib), rel+".txt")); rerr == nil && JSTrim(string(b)) != "" {
			return string(b)
		}
		var root string
		if root, err = m.source(lib); err == nil {
			o, err = ReadFileContent(rel, root, m.cacheRoot(lib))
		}
	}
	if err != nil || o == nil {
		return ""
	}
	c, _ := o.Get("content")
	s, _ := c.(string)
	return s
}

// LibraryLabelled is whether rel in lib carries a label of its own or one
// from a folder above it: on the files for the main library, kept by the
// Studio for any other.
func (m *Module) LibraryLabelled(ctx context.Context, lib Library, rel string) bool {
	var eff *Obj
	if lib.ID == PrimaryID {
		eff = m.Tags.Effective(ctx, rel)
	} else {
		eff = m.Tags.Library.Effective(lib.ID, rel)
	}
	own, _ := eff.Get("own")
	inh, _ := eff.Get("inherited")
	o, _ := own.([]string)
	i, _ := inh.([]string)
	return len(o) > 0 || len(i) > 0
}

// LabelVocabulary is GET /api/tags's labels and counts for the request's
// viewer: the main library's and every other library's, most used first.
func (m *Module) LabelVocabulary(r *http.Request) (labels []string, counts map[string]int, err error) {
	others, err := m.others(r)
	if err != nil {
		return nil, nil, err
	}
	counts = map[string]int{}
	for _, o := range m.Tags.Vocabulary(r.Context(), others) {
		t, _ := o.Get("tag")
		n, _ := o.Get("count")
		tag, _ := t.(string)
		labels = append(labels, tag)
		counts[tag] = toInt(n)
	}
	return labels, counts, nil
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}
