package core

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/version"
)

// mimes is preview.ts's MIME.
var mimes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	".pdf": "application/pdf",
	".md":  "text/plain; charset=utf-8", ".txt": "text/plain; charset=utf-8",
	".html": "text/html; charset=utf-8", ".json": "application/json",
	".csv": "text/csv; charset=utf-8",
}

// MimeFor is preview.ts's mimeFor.
func MimeFor(p string) string {
	if m, ok := mimes[strings.ToLower(filepath.Ext(p))]; ok {
		return m
	}
	return "application/octet-stream"
}

var urlRe = regexp.MustCompile(`(?i)^https?://`)

func isURL(v string) bool { return v != "" && urlRe.MatchString(v) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// configuredIcon is routes.ts's configuredIcon: the dark variant when one
// is set and dark is asked for, otherwise the ordinary one.
func (c *Core) configuredIcon(dark bool) string {
	s := c.a.Settings.Effective()
	or := func(vs ...string) string {
		for _, v := range vs {
			if v != "" {
				return v
			}
		}
		return ""
	}
	if dark {
		return or(s.IconURLDark, os.Getenv("APP_ICON_DARK"), s.IconURL, os.Getenv("APP_ICON"))
	}
	return or(s.IconURL, os.Getenv("APP_ICON"))
}

// mtimeMs is fs.Stats's mtimeMs as JavaScript prints it.
func mtimeMs(st os.FileInfo) string {
	t := st.ModTime()
	ms := float64(t.Unix())*1e3 + float64(t.Nanosecond())/1e6
	return jsval.NumberString(ms)
}

// BrandURL is routes.ts's brandUrl: the URL the browser loads a brand
// image from, our own endpoint whenever one is configured, carrying a
// version so a different image is a different URL; nil without one.
func (c *Core) BrandURL(kind string, dark bool) *string {
	configured := c.configuredIcon(dark)
	if kind == "favicon" && os.Getenv("APP_FAVICON") != "" && !dark {
		configured = os.Getenv("APP_FAVICON")
	}
	if configured == "" || (!isURL(configured) && !exists(configured)) {
		return nil
	}
	stamp := configured
	if !isURL(configured) {
		if st, err := os.Stat(configured); err == nil {
			stamp += ":" + mtimeMs(st)
		}
	}
	sum := sha1.Sum([]byte(stamp))
	v := hex.EncodeToString(sum[:])[:10]
	path := "/api/favicon"
	if kind == "icon" {
		path = "/api/brand-icon"
	}
	q := ""
	if dark {
		q = "variant=dark&"
	}
	u := path + "?" + q + "v=" + v
	return &u
}

func (c *Core) faviconFile() string {
	for _, p := range []string{os.Getenv("APP_FAVICON"), c.a.Settings.Effective().IconURL, os.Getenv("APP_ICON")} {
		if p != "" && !isURL(p) && exists(p) {
			return p
		}
	}
	return ""
}

// iconCache is the remote brand images, kept 10 minutes.
type iconCache struct {
	mu sync.Mutex
	m  map[string]remoteIcon
}

type remoteIcon struct {
	body []byte
	typ  string
	at   time.Time
}

func newIconCache() *iconCache { return &iconCache{m: map[string]remoteIcon{}} }

// fetchRemoteIcon is routes.ts's: public images need no credential; a
// platform blob answers 401 without one, so the deployment's is tried.
func (c *Core) fetchRemoteIcon(r *http.Request, url string) *remoteIcon {
	c.icons.mu.Lock()
	hit, ok := c.icons.m[url]
	c.icons.mu.Unlock()
	if ok && time.Since(hit.at) < 10*time.Minute {
		return &hit
	}
	attempt := func(withKey bool) *remoteIcon {
		key := ""
		if withKey {
			if key = c.a.Providers.GatewayKey(); key == "" {
				return nil
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
		if err != nil {
			return nil
		}
		if withKey {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		cl := &http.Client{Timeout: 15 * time.Second}
		res, err := cl.Do(req)
		if err != nil {
			return nil
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode > 299 {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		if err != nil {
			return nil
		}
		t := res.Header.Get("Content-Type")
		if t == "" {
			t = "image/png"
		}
		return &remoteIcon{body: b, typ: t, at: time.Now()}
	}
	got := attempt(false)
	if got == nil {
		got = attempt(true)
	}
	if got != nil {
		c.icons.mu.Lock()
		c.icons.m[url] = *got
		c.icons.mu.Unlock()
	}
	return got
}

// imageOnly: a brand image is an image and nothing else.
func imageOnly(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func sendIcon(w http.ResponseWriter, typ, cache string, body []byte) error {
	imageOnly(w)
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", cache)
	_, err := w.Write(body)
	return err
}

func sendIconFile(w http.ResponseWriter, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return sendIcon(w, MimeFor(file), "public, max-age=3600", b)
}

func (c *Core) brandIcon(w http.ResponseWriter, r *http.Request) error {
	icon := c.configuredIcon(r.URL.Query().Get("variant") == "dark")
	if isURL(icon) {
		got := c.fetchRemoteIcon(r, icon)
		if got == nil {
			return apierr.New(502, "could not load the configured brand image")
		}
		return sendIcon(w, got.typ, "public, max-age=600", got.body)
	}
	if icon == "" || !exists(icon) {
		return apierr.New(404, "no brand icon configured")
	}
	return sendIconFile(w, icon)
}

func (c *Core) favicon(w http.ResponseWriter, r *http.Request) error {
	dark := r.URL.Query().Get("variant") == "dark"
	configured := ""
	if !dark {
		configured = os.Getenv("APP_FAVICON")
	}
	if configured == "" {
		configured = c.configuredIcon(dark)
	}
	if isURL(configured) {
		got := c.fetchRemoteIcon(r, configured)
		if got == nil {
			return apierr.New(502, "could not load the configured favicon")
		}
		return sendIcon(w, got.typ, "public, max-age=600", got.body)
	}
	icon := configured
	if icon == "" || !exists(icon) {
		icon = c.faviconFile()
	}
	if icon == "" {
		return apierr.New(404, "no favicon configured")
	}
	return sendIconFile(w, icon)
}

// ImageSizes is routes.ts's imageSizes: a PNG's pixel size, else "any".
func ImageSizes(body []byte, typ string) string {
	if strings.Contains(typ, "svg") {
		return "any"
	}
	if len(body) > 24 && binary.BigEndian.Uint32(body) == 0x89504e47 {
		return strconv.Itoa(int(binary.BigEndian.Uint32(body[16:]))) + "x" + strconv.Itoa(int(binary.BigEndian.Uint32(body[20:])))
	}
	return "any"
}

var hexColor = regexp.MustCompile(`(?i)^#[0-9a-f]{6}$`)
var spaces = regexp.MustCompile(`\s+`)

// WebManifest is routes.ts's webManifest, keys in its order.
func WebManifest(name string, dark bool, brand *jsonobj.Object, banner string) *jsonobj.Object {
	bg := "#f3f4f6"
	if dark {
		bg = "#0c1320"
	}
	bar := bg
	if banner != "" && hexColor.MatchString(banner) {
		bar = banner
	}
	if name == "" {
		name = "Studio"
	}
	short := name
	if jsval.Len(name) > 12 {
		parts := spaces.Split(name, -1)
		short = jsval.Slice(parts[len(parts)-1], 12)
	}
	o := jsonobj.New()
	o.SetValue("name", name)
	o.SetValue("short_name", short)
	o.SetValue("start_url", "./")
	o.SetValue("scope", "./")
	o.SetValue("display", "standalone")
	if bar != bg {
		o.SetValue("display_override", []string{"window-controls-overlay"})
	}
	o.SetValue("background_color", bg)
	o.SetValue("theme_color", bar)
	icons := []any{}
	if brand != nil {
		icons = append(icons, brand)
	}
	icon := func(src, sizes, purpose string) *jsonobj.Object {
		i := jsonobj.New()
		i.SetValue("src", src)
		i.SetValue("sizes", sizes)
		i.SetValue("type", "image/png")
		i.SetValue("purpose", purpose)
		return i
	}
	icons = append(icons, icon("/icon-192.png", "192x192", "any"), icon("/icon-512.png", "512x512", "any"), icon("/icon-maskable-512.png", "512x512", "maskable"))
	o.SetValue("icons", icons)
	return o
}

func (c *Core) manifest(w http.ResponseWriter, r *http.Request) error {
	eff := c.a.Settings.Effective()
	var brand *jsonobj.Object
	if configured := c.configuredIcon(false); configured != "" {
		var body []byte
		typ := ""
		if isURL(configured) {
			if got := c.fetchRemoteIcon(r, configured); got != nil {
				body, typ = got.body, got.typ
			}
		} else if b, err := os.ReadFile(configured); err == nil {
			body, typ = b, MimeFor(configured)
		}
		if typ != "" {
			src := "/api/brand-icon"
			if u := c.BrandURL("icon", false); u != nil {
				src = *u
			}
			brand = jsonobj.New()
			brand.SetValue("src", src)
			brand.SetValue("type", typ)
			brand.SetValue("sizes", ImageSizes(body, typ))
		}
	}
	banner := ""
	if jsval.Trim(eff.BannerText) != "" {
		banner = eff.BannerColor
		if banner == "" {
			banner = "#24612e"
		}
	}
	b, err := WebManifest(eff.AppName, eff.Theme == "dark", brand, banner).MarshalJSON()
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Content-Type", "application/manifest+json")
	_, err = w.Write(b)
	return err
}

func (c *Core) version(w http.ResponseWriter, r *http.Request) error {
	return ok(w, version.Of(c.a.Cfg.ProjectRoot))
}

func (c *Core) help(w http.ResponseWriter, r *http.Request) error {
	file := lookupEnv("HELP_FILE", filepath.Join(c.a.Cfg.ProjectRoot, "docs", "HELP.md"))
	md := "No help content configured."
	if b, err := os.ReadFile(file); err == nil {
		md = string(b)
	}
	eff := c.a.Settings.Effective()
	md = strings.ReplaceAll(md, "{appName}", eff.AppName)
	md = strings.ReplaceAll(md, "{kbLabel}", eff.KbLabel)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, err := io.WriteString(w, md)
	return err
}

// config is GET /api/config, the merge point of the client's start-up
// values: presentation from settings, the libraries and platform access
// from their owners (app.Providers).
func (c *Core) config(w http.ResponseWriter, r *http.Request) error {
	eff := c.a.Settings.Effective()
	o := jsonobj.New()
	set := func(k string, v any) {
		if err := o.SetValue(k, v); err != nil {
			c.log.Error("config", "key", k, "err", err.Error())
		}
	}
	nullable := func(p *string) any {
		if p == nil {
			return json.RawMessage("null")
		}
		return *p
	}
	set("appName", eff.AppName)
	set("iconUrl", nullable(c.BrandURL("icon", false)))
	set("iconUrlDark", nullable(c.BrandURL("icon", true)))
	set("faviconUrl", nullable(c.BrandURL("favicon", false)))
	set("faviconUrlDark", nullable(c.BrandURL("favicon", true)))
	set("kbLabel", eff.KbLabel)
	set("theme", eff.Theme)
	set("accent", eff.Accent)
	set("surface", eff.Surface)
	set("suggestedPrompts", eff.SuggestedPrompts)
	set("bannerText", eff.BannerText)
	set("bannerColor", eff.BannerColor)
	set("bannerWhenEmbedded", eff.BannerWhenEmbedded)
	voice := jsonobj.New()
	voice.SetValue("enabled", eff.VoiceEnabled && eff.VoiceURL != "")
	voice.SetValue("url", eff.VoiceURL)
	features := jsonobj.New()
	features.SetValue("voice", voice)
	set("features", features)
	libs, err := c.a.Providers.Libraries(r, eff.KbLabel, viewer(r))
	if err != nil {
		return err
	}
	if libs == nil {
		libs = []json.RawMessage{}
	}
	set("libraries", libs)
	if len(eff.Sections) > 0 {
		set("sections", eff.Sections)
	}
	set("aiEnabled", eff.AiEnabled)
	set("platform", c.a.Providers.PlatformAvailable(r, viewer(r)))
	n := 0
	for _, w := range eff.WorkflowCollection {
		if w != "" {
			n++
		}
	}
	set("workflowCount", n)
	user := jsonobj.New()
	if u := auth.UserOf(r); u != nil {
		user.SetValue("id", u.ID)
		user.SetValue("username", u.Username)
		if u.Name != "" {
			user.SetValue("name", u.Name)
		}
	} else {
		user.SetValue("id", lookupEnv("APP_USER_ID", "user"))
		user.SetValue("username", lookupEnv("APP_USERNAME", "user"))
		if v := os.Getenv("APP_USER_NAME"); v != "" {
			user.SetValue("name", v)
		}
	}
	set("user", user)
	return ok(w, o)
}
