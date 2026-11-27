package core

import (
	"bytes"
	"context"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// page is the head of web/index.html as the build writes it.
const page = `<head>
    <link rel="icon" href="/favicon.svg" />
    <link rel="manifest" href="/manifest.webmanifest" crossorigin="use-credentials" />
    <link rel="apple-touch-icon" href="/apple-touch-icon.png" />
</head>`

// pageDist is markDist with the tab and Home Screen icons.
func pageDist(t *testing.T) string {
	t.Helper()
	d := markDist(t)
	os.WriteFile(filepath.Join(d, "favicon.svg"), []byte("<svg>the mark</svg>"), 0o644)
	os.WriteFile(filepath.Join(d, "apple-touch-icon.png"), []byte("the mark apple-touch-icon.png"), 0o644)
	return d
}

var (
	iconHref  = regexp.MustCompile(`<link rel="icon" href="([^"]*)" />`)
	touchHref = regexp.MustCompile(`<link rel="apple-touch-icon" href="([^"]*)" />`)
)

// served is the tab icon and Home Screen icon the page names.
func (e *env) served(headers ...string) (icon, touch string) {
	e.t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	out := string(e.a.PageFilter(r, []byte(page)))
	a, b := iconHref.FindStringSubmatch(out), touchHref.FindStringSubmatch(out)
	if a == nil || b == nil || strings.Count(out, "<link") < 3 {
		e.t.Fatalf("page: %s", out)
	}
	return html.UnescapeString(a[1]), html.UnescapeString(b[1])
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The page names the configured icon from its first request, so a browser
// that keeps the icon a page loaded with shows it; without one, or with one
// that cannot be shown, the Studio's mark.
func TestPageNamesTheConfiguredIcon(t *testing.T) {
	dir := t.TempDir()
	light, dark := filepath.Join(dir, "light.png"), filepath.Join(dir, "dark.png")
	os.WriteFile(light, pngOf(t, 256, 256), 0o644)
	os.WriteFile(dark, pngOf(t, 300, 300), 0o644)
	down := httptest.NewServer(http.NotFoundHandler())
	defer down.Close()

	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t)})
	icon, touch := e.served()
	if icon != "/favicon.svg" || !regexp.MustCompile(`^/apple-touch-icon\.png\?v=[0-9a-f]{10}$`).MatchString(touch) {
		t.Errorf("no icon: %s %s", icon, touch)
	}
	mark := touch

	e = newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": light, "APP_ICON_DARK": dark})
	icon, touch = e.served()
	if !regexp.MustCompile(`^/api/favicon\?v=[0-9a-f]{10}$`).MatchString(icon) || touch == mark || !strings.HasPrefix(touch, "/apple-touch-icon.png?v=b") {
		t.Errorf("light: %s %s", icon, touch)
	}
	if icon != *e.c().BrandURL("favicon", false) {
		t.Errorf("not the address the app sets: %s", icon)
	}
	// Dark, as the person last saw the page, or as the deployment defaults.
	icon, _ = e.served("Cookie", "ade_ground=0c1320")
	if !strings.HasPrefix(icon, "/api/favicon?variant=dark&v=") {
		t.Errorf("dark ground: %s", icon)
	}
	if icon, _ = e.served("Cookie", "ade_ground=f3f4f6"); strings.Contains(icon, "dark") {
		t.Errorf("light ground: %s", icon)
	}
	// A first visit names the dark icon for a device set to dark; a visit
	// that knows the theme, and a Studio with one icon, do not.
	first := func(e *env, headers ...string) string {
		r := httptest.NewRequest("GET", "/", nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		return string(e.a.PageFilter(r, []byte(page)))
	}
	if out := first(e); !regexp.MustCompile(`<link rel="icon" href="/api/favicon\?v=[0-9a-f]{10}" />\n    <link rel="icon" href="/api/favicon\?variant=dark&amp;v=[0-9a-f]{10}" media="\(prefers-color-scheme: dark\)" />`).MatchString(out) {
		t.Errorf("first visit: %s", out)
	}
	if out := first(e, "Cookie", "ade_ground=f3f4f6"); strings.Contains(out, "media=") {
		t.Errorf("known light: %s", out)
	}
	one := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": light})
	if out := first(one); strings.Contains(out, "media=") {
		t.Errorf("one icon: %s", out)
	}

	for _, bad := range []string{filepath.Join(dir, "missing.png"), dir, down.URL + "/missing.png"} {
		e = newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": bad})
		e.IconProblemsNow()
		if icon, touch = e.served(); icon != "/favicon.svg" || touch != mark {
			t.Errorf("%s: %s %s", bad, icon, touch)
		}
	}
	// An address whose image is still loading is named as the app names it:
	// /api/favicon answers with the mark if it fails.
	e = newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": "https://images.example/i.png"})
	e.c().icons.busy["https://images.example/i.png"] = make(chan struct{})
	if icon, _ = e.served(); !strings.HasPrefix(icon, "/api/favicon?v=") {
		t.Errorf("loading: %s", icon)
	}
}

// c is the env's Core, as the page filter holds it.
func (e *env) c() *Core {
	return e.core
}

// IconProblemsNow waits for the configured icons to be checked.
func (e *env) IconProblemsNow() *map[string]any {
	e.t.Helper()
	r := e.do("GET", "/api/settings", nil)
	p, _ := r.body["iconProblems"].(map[string]any)
	return &p
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	return img
}

// An iPhone or iPad's Home Screen icon is made from the configured icon:
// as it is where it is already a 180-pixel PNG, else fitted into one. The
// mark where there is none or it cannot be read.
func TestAppleTouchIconFollowsTheBrand(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	exact := pngOf(t, 180, 180)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big.png" {
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngOf(t, 1024, 1024))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	for _, c := range []struct {
		icon string
		want string // "mark", "same", or "made"
	}{
		{"", "mark"},
		{write("exact.png", exact), "same"},
		{write("big.png", pngOf(t, 512, 512)), "made"},
		{write("wide.jpg", jpegOf(t, 400, 100)), "made"},
		{write("small.png", pngOf(t, 32, 32)), "made"},
		{srv.URL + "/big.png", "made"},
		{write("logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), "mark"},
		{write("page.png", []byte("<!doctype html><title>Sign in</title>")), "mark"},
		{filepath.Join(dir, "missing.png"), "mark"},
		{srv.URL + "/missing.png", "mark"},
	} {
		e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": c.icon})
		g := e.do("GET", "/apple-touch-icon.png", nil)
		if g.status != 200 || g.header.Get("Content-Type") != "image/png" || g.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %v", c.icon, g.status, g.header)
			continue
		}
		switch c.want {
		case "mark":
			if string(g.raw) != "the mark apple-touch-icon.png" {
				t.Errorf("%s: not the mark", c.icon)
			}
		case "same":
			if !bytes.Equal(g.raw, exact) {
				t.Errorf("%s: not the image itself", c.icon)
			}
		case "made":
			if b := decodePNG(t, g.raw).Bounds(); b.Dx() != 180 || b.Dy() != 180 {
				t.Errorf("%s: %v", c.icon, b)
			}
		}
	}
	// Fitted on white, since iOS fills what is transparent with black.
	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": write("wide2.jpg", jpegOf(t, 400, 100))})
	img := decodePNG(t, e.do("GET", "/apple-touch-icon.png", nil).raw)
	if r, g, b, a := img.At(90, 5).RGBA(); r != 0xffff || g != 0xffff || b != 0xffff || a != 0xffff {
		t.Errorf("margin: %v %v %v %v", r, g, b, a)
	}
	if r, _, _, _ := img.At(90, 90).RGBA(); r > 0x1000 {
		t.Errorf("image: %v", r)
	}
}

type records struct{ msgs []string }

func (h *records) Enabled(context.Context, slog.Level) bool { return true }
func (h *records) Handle(_ context.Context, r slog.Record) error {
	s := r.Message
	r.Attrs(func(a slog.Attr) bool { s += " " + a.Key + "=" + a.Value.String(); return true })
	h.msgs = append(h.msgs, s)
	return nil
}
func (h *records) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *records) WithGroup(string) slog.Handler      { return h }

// A configured icon that cannot be shown is said so in Settings, and logged
// once.
func TestIconProblems(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.png")
	os.WriteFile(good, pngOf(t, 64, 64), 0o644)
	text := filepath.Join(dir, "notes.png")
	os.WriteFile(text, []byte("not an image"), 0o644)
	locked := filepath.Join(dir, "locked.png")
	os.WriteFile(locked, pngOf(t, 64, 64), 0o000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.png":
			w.Write(pngOf(t, 64, 64))
		case "/login":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<!doctype html><title>Sign in</title>"))
		default:
			http.Error(w, "gone", http.StatusGone)
		}
	}))
	defer srv.Close()

	cases := map[string]string{
		good:                     "",
		srv.URL + "/good.png":    "",
		filepath.Join(dir, "no"): "there is no file at that path on the server",
		dir:                      "it is a folder, not an image",
		text:                     "it is not an image",
		srv.URL + "/gone.png":    "its server answered 410",
		srv.URL + "/login":       "its server sent something other than an image",
		"http://127.0.0.1:1/i":   "its server could not be reached",
	}
	if os.Geteuid() != 0 {
		cases[locked] = "the server cannot read it"
	}
	for icon, want := range cases {
		e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": icon})
		got := *e.IconProblemsNow()
		if want == "" {
			if len(got) != 0 {
				t.Errorf("%s: %v", icon, got)
			}
			continue
		}
		if len(got) != 1 || got["light"] != want {
			t.Errorf("%s: %v", icon, got)
		}
	}

	// Light and dark are each said; a dark that is the light one is not
	// said twice. Each is logged once.
	h := &records{}
	e := newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": filepath.Join(dir, "a.png"), "APP_ICON_DARK": filepath.Join(dir, "b.png")})
	e.core.log = slog.New(h)
	for range 3 {
		got := *e.IconProblemsNow()
		if len(got) != 2 || got["light"] == nil || got["dark"] == nil {
			t.Fatalf("%v", got)
		}
		e.served()
	}
	if len(h.msgs) != 2 || !strings.Contains(h.msgs[0], "a.png") || !strings.Contains(h.msgs[1], "b.png") || !strings.Contains(h.msgs[0], "no file") {
		t.Errorf("logged: %q", h.msgs)
	}
	e = newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t), "APP_ICON": filepath.Join(dir, "c.png"), "APP_ICON_DARK": filepath.Join(dir, "c.png")})
	if got := *e.IconProblemsNow(); len(got) != 1 {
		t.Errorf("%v", got)
	}
	// A setting saved in Settings counts as APP_ICON does.
	e = newEnv(t, map[string]string{"STUDIO_WEB_DIST": pageDist(t)})
	if r := e.do("PUT", "/api/settings", map[string]any{"iconUrl": filepath.Join(dir, "d.png")}); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if got := *e.IconProblemsNow(); got["light"] != "there is no file at that path on the server" {
		t.Errorf("saved: %v", got)
	}
}
