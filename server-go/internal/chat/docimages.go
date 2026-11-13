package chat

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

const maxDocImageBytes = 25 << 20

// libraryImages reads the images a PDF or Word file written to dir refers
// to: a path relative to dir, or to the library's top (with or without a
// leading slash), as a Studio link (#open=file:..., /api/kb/raw?path=...),
// or a base64 data: URL. Web images are not fetched.
func libraryImages(root, dir string) docwrite.ImageSource {
	return func(ref string) ([]byte, error) {
		ref = strings.Trim(strings.TrimSpace(ref), "<>")
		low := strings.ToLower(ref)
		switch {
		case strings.HasPrefix(low, "data:"):
			return dataURL(ref)
		case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "//"):
			return nil, errors.New("web images are not fetched; save the image into the library and refer to its path")
		}
		for _, cand := range libraryCandidates(ref, dir) {
			for _, part := range strings.Split(cand, "/") {
				if part != ".." && (kb.ExcludeDirs[part] || kb.HiddenName(part)) {
					return nil, errors.New("not readable: " + part + " is excluded")
				}
			}
			abs, err := kb.Resolve(cand, root)
			if err != nil {
				return nil, err
			}
			st, err := os.Stat(abs)
			if err != nil || st.IsDir() {
				continue
			}
			if st.Size() > maxDocImageBytes {
				return nil, errors.New("over 25 MB")
			}
			return os.ReadFile(abs)
		}
		return nil, errors.New("not found in the library")
	}
}

// libraryCandidates is where a reference may point, most likely first:
// beside the file being written, then from the library's top.
func libraryCandidates(ref, dir string) []string {
	if i := strings.Index(ref, "#open=file:"); i >= 0 {
		ref = "/" + ref[i+len("#open=file:"):]
	} else if u, err := url.Parse(ref); err == nil && strings.HasPrefix(u.Path, "/api/kb/") && u.Query().Get("path") != "" {
		return []string{strings.TrimLeft(u.Query().Get("path"), "/")}
	}
	if p, err := url.PathUnescape(ref); err == nil {
		ref = p
	}
	ref = strings.SplitN(ref, "?", 2)[0]
	if strings.HasPrefix(ref, "/") {
		return []string{path.Clean(strings.TrimLeft(ref, "/"))}
	}
	out := []string{path.Clean(path.Join(dir, ref))}
	if top := path.Clean(ref); top != out[0] && !strings.HasPrefix(top, "../") {
		out = append(out, top)
	}
	return out
}

func dataURL(ref string) ([]byte, error) {
	comma := strings.IndexByte(ref, ',')
	if comma < 0 || !strings.Contains(strings.ToLower(ref[:comma]), ";base64") {
		return nil, errors.New("a data: image must be base64")
	}
	enc := ref[comma+1:]
	if base64.StdEncoding.DecodedLen(len(enc)) > maxDocImageBytes {
		return nil, errors.New("over 25 MB")
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(enc), ""))
}
