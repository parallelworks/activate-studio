package server

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// A file under /assets with its content hash in its name never changes and
// is cached for a year; everything else, the HTML shell above all, is never
// cached, so a deploy is picked up on the next load (main.ts).
var hashedName = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

func cacheControl(urlPath string) string {
	if strings.Contains(urlPath, "/assets/") && hashedName.MatchString(urlPath) {
		return "public, max-age=31536000, immutable"
	}
	return "no-store, must-revalidate"
}

// serveStatic answers from the built web app: the file asked for where it
// exists, else index.html (the app routes in the browser). It reads from
// disk on each request, so a rebuild of the web app needs no restart. It
// returns false when there is no built web app. A text file goes out
// compressed when the browser accepts it (compress.go), with Vary and an
// ETag of its own.
func serveStatic(w http.ResponseWriter, r *http.Request, dist string, cache *compressCache) bool {
	index := filepath.Join(dist, "index.html")
	if _, err := os.Stat(index); err != nil {
		return false
	}
	clean := path.Clean("/" + r.URL.Path)
	file := filepath.Join(dist, filepath.FromSlash(clean))
	if st, err := os.Stat(file); err == nil && st.IsDir() {
		file = filepath.Join(file, "index.html")
	}
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() {
		file, clean = index, "/index.html"
	}
	f, err := os.Open(file)
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return true
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return true
	}
	h := w.Header()
	h.Set("Cache-Control", cacheControl(clean))
	if cache != nil && compressible(file, st.Size()) {
		h.Add("Vary", "Accept-Encoding")
		if enc := negotiate(r.Header.Get("Accept-Encoding")); enc != "" {
			if data, ctype, err := cache.get(file, st, enc); err == nil {
				h.Set("Content-Encoding", enc)
				h.Set("Content-Type", ctype)
				h.Set("ETag", encodedETag(st.Size(), st.ModTime().UnixMilli(), enc))
				// A range of a compressed body is of no use to a browser.
				r.Header.Del("Range")
				http.ServeContent(w, r, file, st.ModTime(), bytes.NewReader(data))
				return true
			}
		}
	}
	// @fastify/static's weak ETag (size and modification time in
	// milliseconds, in hex), so a revalidation gets its 304 as from Node.
	h.Set("ETag", etag(st.Size(), st.ModTime().UnixMilli()))
	http.ServeContent(w, r, file, st.ModTime(), f)
	return true
}

// etag is the weak validator @fastify/static (the send package) sends.
func etag(size, mtimeMs int64) string {
	return fmt.Sprintf(`W/"%x-%x"`, size, mtimeMs)
}
