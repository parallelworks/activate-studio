package core

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// markDist is a built web app with only the mark's files, each its own bytes.
func markDist(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, f := range []string{"icon-192.png", "icon-512.png", "icon-maskable-512.png"} {
		if err := os.WriteFile(filepath.Join(d, f), []byte("the mark "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// listed is the manifest's icon addresses as a browser resolves them under
// mount.
func (e *env) listed(mount string) []string {
	e.t.Helper()
	m := e.do("GET", "/manifest.webmanifest", nil)
	base, _ := url.Parse("https://host.example" + mount + "manifest.webmanifest")
	var out []string
	for _, i := range m.body["icons"].([]any) {
		u, err := base.Parse(i.(map[string]any)["src"].(string))
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, u.RequestURI())
	}
	return out
}

func (e *env) showsMark() {
	e.t.Helper()
	got := e.listed("/")
	if len(got) != 3 || !strings.HasPrefix(got[0], "/icon-192.png?v=") || !strings.HasPrefix(got[1], "/icon-512.png?v=") || !strings.HasPrefix(got[2], "/icon-maskable-512.png?v=") {
		e.t.Fatalf("not the mark: %v", got)
	}
}

// A Studio served under a path names its own icons, not the host's, whether
// the mark's or the deployment's.
func TestManifestIconsUnderAPath(t *testing.T) {
	brand := filepath.Join(t.TempDir(), "brand.png")
	os.WriteFile(brand, pngOf(t, 256, 256), 0o644)
	for _, icon := range []string{"", brand} {
		e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": markDist(t), "APP_ICON": icon})
		for _, mount := range []string{"/", "/me/session/ada/studio/"} {
			got := e.listed(mount)
			if len(got) == 0 {
				t.Fatal("no icons")
			}
			for _, u := range got {
				if !strings.HasPrefix(u, mount) || !regexp.MustCompile(`^(icon-192\.png|icon-512\.png|icon-maskable-512\.png|api/brand-icon)\?v=[0-9a-f]{10}$`).MatchString(strings.TrimPrefix(u, mount)) {
					t.Errorf("at %s: %s", mount, u)
				}
			}
		}
	}
}

// A browser that installed the Studio looks at its icon again only when the
// icon's address changes, so a new mark has a new address, and the same one
// keeps its own.
func TestManifestIconAddressFollowsTheMark(t *testing.T) {
	dist := markDist(t)
	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": dist})
	e.showsMark()
	first := e.listed("/")
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(dist, "icon-512.png"), later, later)
	if again := e.listed("/"); strings.Join(again, " ") != strings.Join(first, " ") {
		t.Fatalf("the same mark moved: %v then %v", first, again)
	}
	os.WriteFile(filepath.Join(dist, "icon-512.png"), []byte("a new mark"), 0o644)
	if next := e.listed("/"); next[1] == first[1] || next[0] != first[0] {
		t.Fatalf("%v then %v", first, next)
	}
}

// The deployment's icon is the only one listed, since browsers take the
// largest, and some the maskable one; one an installed app cannot have
// leaves the mark.
func TestManifestBrandIcon(t *testing.T) {
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 200, 200)), nil)
	dir := t.TempDir()
	for _, c := range []struct {
		name  string
		body  []byte
		typ   string
		sizes string // empty for the mark
	}{
		{"square.png", pngOf(t, 256, 256), "image/png", "256x256"},
		{"photo.jpg", jpg.Bytes(), "image/jpeg", "200x200"},
		{"smallest.png", pngOf(t, 144, 144), "image/png", "144x144"},
		{"small.png", pngOf(t, 64, 64), "", ""},
		{"wide.png", pngOf(t, 600, 200), "", ""},
		{"mark.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "", ""},
		{"page.png", []byte("<!doctype html><title>Sign in</title>"), "", ""},
		{"gone.png", nil, "", ""},
	} {
		file := filepath.Join(dir, c.name)
		if c.body != nil {
			os.WriteFile(file, c.body, 0o644)
		}
		e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": markDist(t), "APP_ICON": file})
		if c.sizes == "" {
			e.showsMark()
			continue
		}
		icons := e.do("GET", "/manifest.webmanifest", nil).body["icons"].([]any)
		i := icons[0].(map[string]any)
		if len(icons) != 1 || i["type"] != c.typ || i["sizes"] != c.sizes || !strings.HasPrefix(i["src"].(string), "api/brand-icon?v=") {
			t.Errorf("%s: %v", c.name, icons)
		}
	}
}

// A configured image that cannot be loaded, or is not one (a sign-in page),
// is replaced by the mark everywhere it would show.
func TestBrandIconThatCannotBeLoaded(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.URL.Path == "/login" {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<!doctype html><title>Sign in</title>"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	for _, at := range []string{"/login", "/missing.png"} {
		e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": markDist(t), "APP_ICON": srv.URL + at})
		asked.Store(0)
		e.showsMark()
		for _, u := range []string{"/api/brand-icon", "/api/favicon", "/api/brand-icon?variant=dark"} {
			g := e.do("GET", u, nil)
			if g.status != 200 || string(g.raw) != "the mark icon-512.png" || g.header.Get("Content-Type") != "image/png" || g.header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s at %s: %d %v %s", u, at, g.status, g.header, g.raw)
			}
		}
		if n := asked.Load(); n != 1 {
			t.Errorf("%s: asked %d times within a minute", at, n)
		}
	}
	// With no built web app there is no mark to show instead.
	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": t.TempDir(), "APP_ICON": srv.URL + "/missing.png"})
	if g := e.do("GET", "/api/brand-icon", nil); g.status != 502 {
		t.Errorf("%d", g.status)
	}
}

// Once loaded, an image stays when its server goes down, so the icon does
// not change and come back.
func TestBrandIconKeptWhileItsServerIsDown(t *testing.T) {
	c := &Core{icons: newIconCache()}
	c.icons.m["https://images.example/i.png"] = remoteIcon{body: []byte("kept"), typ: "image/png", at: time.Now().Add(-time.Hour), failed: time.Now()}
	if got := c.fetchRemoteIcon(t.Context(), "https://images.example/i.png"); got == nil || string(got.body) != "kept" {
		t.Fatalf("%+v", got)
	}
}

// A server that never answers holds the manifest up for seconds, not the
// image's whole time limit.
func TestManifestDoesNotWaitForAHungIcon(t *testing.T) {
	stop := make(chan struct{})
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stop)
	old := manifestIconWait
	manifestIconWait = 100 * time.Millisecond
	defer func() { manifestIconWait = old }()
	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": markDist(t), "APP_ICON": srv.URL + "/i.png"})
	start := time.Now()
	e.showsMark()
	e.showsMark()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("%d loads at once", n)
	}
}

func TestImageType(t *testing.T) {
	for _, c := range []struct{ body, typ, want string }{
		{string(pngOf(t, 1, 1)), "application/octet-stream", "image/png"},
		{"<?xml version=\"1.0\"?>\n<svg/>", "application/octet-stream", "image/svg+xml"},
		{"<!-- " + strings.Repeat("a", 5000) + " --><svg/>", "image/svg+xml; charset=utf-8", "image/svg+xml"},
		{"<!doctype html><svg/>", "image/svg+xml", ""},
		{"<HTML><body>", "image/png", ""},
		{"\x00\x00\x00 ftypavif", "image/avif", "image/avif"},
		{"hello", "text/plain", ""},
		{"", "image/png", ""},
	} {
		if got := imageType([]byte(c.body), c.typ); got != c.want {
			t.Errorf("%.30q %s: %q, want %q", c.body, c.typ, got, c.want)
		}
	}
}
