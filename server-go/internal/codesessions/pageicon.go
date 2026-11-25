package codesessions

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A machine's icon is one the platform serves: the one uploaded for the
// resource (imageUrl, a blob), or a scheduler's, a cloud's or an operating
// system's, which PW Code on the web picks from the listing's csp,
// schedulerType and os. The page names its path (web/src/code/machineIcon.ts)
// and the Studio passes it on, so it shows wherever the Studio runs. Nothing
// but these paths is fetched.
//
// The platform's own icons are public, so no credential is sent for them. An
// uploaded one is the resource's, and the platform gives it only to someone
// signed in: it is read as the person the page lists the resource for
// (viewerKey), never with another's credential, and kept for that credential
// alone.

// pageIconPath is the paths the page may ask for.
var pageIconPath = regexp.MustCompile(`^(/assets/images/icons/(compute|os)/[a-z0-9-]{1,40}\.(png|svg)|/api/blobs/[A-Za-z0-9_-]{8,128})$`)

// pageIconUploads is where the uploaded ones are.
const pageIconUploads = "/api/blobs/"

const (
	pageIconMost  = 1 << 20
	pageIconKept  = 6 * time.Hour
	pageIconRetry = time.Minute // one the platform did not say yes or no to
	pageIconCount = 500
	pageIconBytes = 32 << 20
)

type pageIconBlob struct {
	until time.Time
	typ   string // "" for one the platform does not have
	body  []byte
}

// pageIcons are the icons kept, by whose they are (a hash of the credential,
// "" for the platform's own) and where.
var pageIcons struct {
	sync.Mutex
	m     map[[2]string]pageIconBlob
	bytes int
}

func keepPageIcon(id [2]string, b pageIconBlob) {
	pageIcons.Lock()
	defer pageIcons.Unlock()
	pageIcons.bytes += len(b.body) - len(pageIcons.m[id].body)
	if pageIcons.m == nil || len(pageIcons.m) >= pageIconCount || pageIcons.bytes > pageIconBytes {
		pageIcons.m, pageIcons.bytes = map[[2]string]pageIconBlob{}, len(b.body)
	}
	pageIcons.m[id] = b
}

// pageIconTypes is what is passed on. An SVG is drawn by an <img>, and opened
// on its own is sandboxed.
var pageIconTypes = map[string]bool{"image/png": true, "image/svg+xml": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

// pageIcon is GET /api/code-page/icon?path=.
func (s *routes) pageIcon(w http.ResponseWriter, r *http.Request) {
	path, origin := r.URL.Query().Get("path"), strings.TrimRight(s.p.Origin(), "/")
	if origin == "" || !pageIconPath.MatchString(path) {
		writeProblem(w, http.StatusNotFound, "no such icon")
		return
	}
	at, key := origin+path, ""
	header := map[string]string{"Accept": "image/*"}
	uploaded := strings.HasPrefix(path, pageIconUploads)
	if uploaded {
		k, err := viewerKey(r, s.p)
		if err != nil || k == "" {
			writeProblem(w, http.StatusNotFound, "no such icon")
			return
		}
		key, header["Authorization"] = k, "Bearer "+k
	}
	id := [2]string{remote.KeyID(key), at}
	pageIcons.Lock()
	b, ok := pageIcons.m[id]
	pageIcons.Unlock()
	if !ok || time.Now().After(b.until) {
		res, done, err := platform.Request(r.Context(), http.MethodGet, at, header, nil, 15*time.Second)
		if err != nil {
			writeProblem(w, http.StatusBadGateway, "The platform did not answer.")
			return
		}
		defer done()
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, pageIconMost+1))
		if err != nil {
			writeProblem(w, http.StatusBadGateway, "The platform's answer was cut off.")
			return
		}
		b = pageIconBlob{until: time.Now().Add(pageIconKept)}
		typ, _, _ := strings.Cut(res.Header.Get("Content-Type"), ";")
		if res.StatusCode == http.StatusOK && len(raw) <= pageIconMost && pageIconTypes[strings.TrimSpace(typ)] {
			b.typ, b.body = strings.TrimSpace(typ), raw
		} else if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotFound {
			b.until = time.Now().Add(pageIconRetry)
		}
		keepPageIcon(id, b)
	}
	if b.typ == "" {
		writeProblem(w, http.StatusNotFound, "no such icon")
		return
	}
	if uploaded {
		// The browser may be someone else's next: it asks here each time.
		tag := `"` + remote.KeyID(string(b.body)) + `"`
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else {
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	w.Header().Set("Content-Type", b.typ)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(b.body)
}
