package codesessions

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A machine's icon is one the platform serves: the one uploaded for the
// resource (imageUrl, a blob), or a scheduler's, a cloud's or an operating
// system's, which PW Code on the web picks from the listing's csp,
// schedulerType and os. The page names its path (web/src/code/machineIcon.ts)
// and the Studio passes it on, so it shows wherever the Studio runs. Both are
// public on the platform, so no credential is sent, and nothing but these
// paths is fetched.

// pageIconPath is the paths the page may ask for.
var pageIconPath = regexp.MustCompile(`^(/assets/images/icons/(compute|os)/[a-z0-9-]{1,40}\.(png|svg)|/api/blobs/[A-Za-z0-9_-]{8,128})$`)

const (
	pageIconMost  = 1 << 20
	pageIconKept  = 6 * time.Hour
	pageIconCount = 100
)

type pageIconBlob struct {
	at   time.Time
	typ  string // "" for one the platform does not have
	body []byte
}

var pageIcons struct {
	sync.Mutex
	m map[string]pageIconBlob
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
	at := origin + path
	pageIcons.Lock()
	b, ok := pageIcons.m[at]
	pageIcons.Unlock()
	if !ok || time.Since(b.at) > pageIconKept {
		res, done, err := platform.Request(r.Context(), http.MethodGet, at, map[string]string{"Accept": "image/*"}, nil, 15*time.Second)
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
		b = pageIconBlob{at: time.Now()}
		typ, _, _ := strings.Cut(res.Header.Get("Content-Type"), ";")
		if res.StatusCode == http.StatusOK && len(raw) <= pageIconMost && pageIconTypes[strings.TrimSpace(typ)] {
			b.typ, b.body = strings.TrimSpace(typ), raw
		}
		pageIcons.Lock()
		if pageIcons.m == nil || len(pageIcons.m) >= pageIconCount {
			pageIcons.m = map[string]pageIconBlob{}
		}
		pageIcons.m[at] = b
		pageIcons.Unlock()
	}
	if b.typ == "" {
		writeProblem(w, http.StatusNotFound, "no such icon")
		return
	}
	w.Header().Set("Content-Type", b.typ)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(b.body)
}
