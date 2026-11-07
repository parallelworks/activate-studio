package server

import (
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
// returns false when there is no built web app.
func serveStatic(w http.ResponseWriter, r *http.Request, dist string) bool {
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
	w.Header().Set("Cache-Control", cacheControl(clean))
	http.ServeContent(w, r, file, st.ModTime(), f)
	return true
}
