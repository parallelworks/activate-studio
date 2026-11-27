package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"html"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// An icon a deployment configured (APP_ICON, APP_ICON_DARK, APP_FAVICON, or
// the Settings that stand for them) that cannot be shown is replaced by the
// Studio's mark wherever it would show. So that this is not silent, the
// reason is logged once and shown in Settings, General, beside the icon.

// iconSlot is one configured icon: which, and the value configured.
type iconSlot struct{ name, value string }

// iconSlots are the configured icons, each once: the dark one only where it
// is its own, and the tab icon only where APP_FAVICON names one.
func (c *Core) iconSlots() []iconSlot {
	var out []iconSlot
	if v := c.configuredIcon(false); v != "" {
		out = append(out, iconSlot{"light", v})
	}
	if v := c.configuredIcon(true); v != "" && v != c.configuredIcon(false) {
		out = append(out, iconSlot{"dark", v})
	}
	if v := os.Getenv("APP_FAVICON"); v != "" {
		out = append(out, iconSlot{"favicon", v})
	}
	return out
}

// iconProblem is why the image configured as value cannot be shown, or "".
// A remote image not yet loaded is waited for until ctx ends; with wait
// false it is not, and is loaded in the background for the next asker
// (known is then false).
func (c *Core) iconProblem(ctx context.Context, value string, wait bool) (problem string, known bool) {
	if isURL(value) {
		peek := func() (string, bool) {
			c.icons.mu.Lock()
			defer c.icons.mu.Unlock()
			hit := c.icons.m[value]
			switch {
			case hit.body != nil:
				return "", true
			case !hit.failed.IsZero():
				return hit.why, true
			}
			return "", false
		}
		if !wait {
			// Loaded or tried again as the cache says; the next asker sees it.
			go c.fetchRemoteIcon(context.Background(), value)
			return peek()
		}
		c.fetchRemoteIcon(ctx, value)
		return peek()
	}
	st, err := os.Stat(value)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "there is no file at that path on the server", true
	case err != nil:
		return "the server cannot read it", true
	case st.IsDir():
		return "it is a folder, not an image", true
	}
	b, err := os.ReadFile(value)
	if err != nil {
		return "the server cannot read it", true
	}
	// The file's name is trusted for a format the server does not know,
	// not for text.
	if t := imageType(b, MimeFor(value)); t == "" || t != "image/svg+xml" && strings.HasPrefix(http.DetectContentType(b), "text/") {
		return "it is not an image", true
	}
	return "", true
}

var problemsLogged sync.Map

// IconProblems is GET /api/settings' iconProblems: for each configured icon
// that cannot be shown, why, keyed light, dark or favicon. Each is logged
// the first time it is found.
func (c *Core) IconProblems(ctx context.Context, wait bool) *jsonobj.Object {
	o := jsonobj.New()
	for _, s := range c.iconSlots() {
		p, _ := c.iconProblem(ctx, s.value, wait)
		if p == "" {
			continue
		}
		o.SetValue(s.name, p)
		if _, seen := problemsLogged.LoadOrStore(s.name+"\x00"+s.value+"\x00"+p, true); !seen {
			c.log.Warn("configured icon cannot be shown; the Studio's mark shows in its place", "icon", s.name, "value", s.value, "why", p)
		}
	}
	return o
}

// pageDark is whether the page will open dark: the ground the person last
// saw (GroundOf), else the deployment's default theme.
func (c *Core) pageDark(r *http.Request) bool {
	if g := GroundOf(r); g != "" {
		n, _ := strconv.ParseUint(g[1:], 16, 32)
		red, green, blue := float64(n>>16&0xff), float64(n>>8&0xff), float64(n&0xff)
		return 0.2126*red+0.7152*green+0.0722*blue < 128
	}
	return c.a.Settings.Effective().Theme == "dark"
}

// faviconSource is the image the tab icon comes from, as BrandURL picks it.
func (c *Core) faviconSource(dark bool) string {
	if v := os.Getenv("APP_FAVICON"); v != "" && !dark {
		return v
	}
	return c.configuredIcon(dark)
}

var (
	iconLink  = regexp.MustCompile(`<link rel="icon"[^>]*>`)
	touchLink = regexp.MustCompile(`<link rel="apple-touch-icon"[^>]*>`)
)

// PageIcons writes the page's tab icon and Home Screen icon into the built
// index.html as the server sends it: the configured icon where there is one
// that can be shown, the Studio's mark otherwise. A browser that keeps the
// icon the page loaded with (Safari) then has the right one from the first
// request; the app still changes the tab icon as the theme changes.
func (c *Core) PageIcons(r *http.Request, page []byte) []byte {
	dark := c.pageDark(r)
	tab := func(dark bool) string {
		if u := c.BrandURL("favicon", dark); u != nil {
			if p, _ := c.iconProblem(r.Context(), c.faviconSource(dark), false); p == "" {
				return *u
			}
		}
		return "/favicon.svg"
	}
	links := `<link rel="icon" href="` + html.EscapeString(tab(dark)) + `" />`
	// On a first visit the page does not know the theme the person will
	// see; a device set to dark turns the default light theme dark, so a
	// dark icon of its own is named for it. The app removes it once it sets
	// the tab icon itself (web/src/config.ts, setTabIcon).
	if !dark && GroundOf(r) == "" && c.faviconSource(true) != c.faviconSource(false) {
		if d := tab(true); d != tab(false) {
			links += "\n    " + `<link rel="icon" href="` + html.EscapeString(d) + `" media="(prefers-color-scheme: dark)" />`
		}
	}
	// Checked here too, so a problem is logged as the first page is served.
	c.IconProblems(r.Context(), false)
	touch := "/apple-touch-icon.png?v=" + c.touchVersion(r.Context())
	page = iconLink.ReplaceAllLiteral(page, []byte(links))
	return touchLink.ReplaceAllLiteral(page, []byte(`<link rel="apple-touch-icon" href="`+html.EscapeString(touch)+`" />`))
}

// touchVersion names what /apple-touch-icon.png serves now, so the page's
// link to it changes when it does.
func (c *Core) touchVersion(ctx context.Context) string {
	if src := c.configuredIcon(false); src != "" {
		if p, _ := c.iconProblem(ctx, src, false); p == "" {
			if u := c.BrandURL("icon", false); u != nil {
				_, v, _ := strings.Cut(*u, "v=")
				return "b" + v
			}
		}
	}
	_, v, _ := strings.Cut(c.markSrc("apple-touch-icon.png"), "v=")
	return v
}

// touchSize is the Home Screen icon's size, as iOS asks for it.
const touchSize = 180

// touchMade holds the last icon made from a configured image.
var touchMade struct {
	sync.Mutex
	key  string
	body []byte
}

// touchIcon is the Home Screen icon made from a configured image: the
// image itself where it is already a 180-pixel square PNG, else the image
// fitted into one on white, since iOS fills what is transparent with
// black. nil when the image cannot be read (an SVG among them).
func touchIcon(body []byte) []byte {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 {
		return nil
	}
	if format == "png" && cfg.Width == touchSize && cfg.Height == touchSize {
		return body
	}
	sum := sha1.Sum(body)
	key := hex.EncodeToString(sum[:])
	touchMade.Lock()
	defer touchMade.Unlock()
	if touchMade.key == key {
		return touchMade.body
	}
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, touchSize, touchSize))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	w, h := touchSize, touchSize
	if cfg.Width > cfg.Height {
		h = max(1, touchSize*cfg.Height/cfg.Width)
	} else if cfg.Height > cfg.Width {
		w = max(1, touchSize*cfg.Width/cfg.Height)
	}
	at := image.Rect((touchSize-w)/2, (touchSize-h)/2, (touchSize-w)/2+w, (touchSize-h)/2+h)
	draw.CatmullRom.Scale(dst, at, src, src.Bounds(), draw.Over, nil)
	var out bytes.Buffer
	if png.Encode(&out, dst) != nil {
		return nil
	}
	touchMade.key, touchMade.body = key, out.Bytes()
	return touchMade.body
}

// appleTouchIcon is GET /apple-touch-icon.png: the Home Screen icon of an
// iPhone or iPad (and Safari's Add to Dock where the manifest has none),
// made from the configured icon, or the Studio's mark.
func (c *Core) appleTouchIcon(w http.ResponseWriter, r *http.Request) error {
	if src := c.configuredIcon(false); src != "" {
		var body []byte
		if isURL(src) {
			ctx, stop := context.WithTimeout(r.Context(), manifestIconWait)
			if got := c.fetchRemoteIcon(ctx, src); got != nil {
				body = got.body
			}
			stop()
		} else if b, err := os.ReadFile(src); err == nil {
			body = b
		}
		if made := touchIcon(body); made != nil {
			return sendIcon(w, "image/png", "no-cache", made)
		}
	}
	b, err := os.ReadFile(filepath.Join(c.a.Cfg.WebDist, "apple-touch-icon.png"))
	if err != nil {
		http.NotFound(w, r)
		return nil
	}
	return sendIcon(w, "image/png", "no-cache", b)
}

// logIconProblems checks the configured icons once as the Studio starts,
// so a misconfigured one is in the log before anyone opens the page.
func (c *Core) logIconProblems(ctx context.Context) {
	ctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	c.IconProblems(ctx, true)
}
