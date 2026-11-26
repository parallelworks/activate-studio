package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/gif" // ImageSizes reads these formats' sizes
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/helpdoc"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/links"
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

// iconCache is the remote brand images, kept 10 minutes. busy holds the ones
// being loaded, each closed when its load is done.
type iconCache struct {
	mu   sync.Mutex
	m    map[string]remoteIcon
	busy map[string]chan struct{}
}

type remoteIcon struct {
	body []byte // the last image loaded, nil before the first
	typ  string
	at   time.Time
	// failed is when the last try failed, the zero time when it did not.
	failed time.Time
}

// An image that could not be loaded is not asked for again for a minute, so
// a server that hangs does not hold up every page.
const iconRetry = time.Minute

// imageType is the image type of a body its server calls typ, or "" when it
// is not an image (a sign-in page in an image's place, say).
func imageType(body []byte, typ string) string {
	if t := http.DetectContentType(body); strings.HasPrefix(t, "image/") {
		return t
	}
	head := bytes.ToLower(bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	head = head[:min(len(head), 4096)]
	html := bytes.HasPrefix(head, []byte("<!doctype html")) || bytes.Contains(head, []byte("<html"))
	switch {
	case html || len(body) == 0:
		return ""
	case bytes.HasPrefix(head, []byte("<")) && (bytes.Contains(head, []byte("<svg")) || strings.HasPrefix(strings.ToLower(typ), "image/svg")):
		return "image/svg+xml"
	case strings.HasPrefix(strings.ToLower(typ), "image/"):
		return typ
	}
	return ""
}

func newIconCache() *iconCache {
	return &iconCache{m: map[string]remoteIcon{}, busy: map[string]chan struct{}{}}
}

// fetchRemoteIcon is the image at url, or nil when it cannot be loaded (or
// not before ctx ends). One load runs at a time for an address, whoever
// waits for it. When it fails, the copy loaded before is used, so a server
// that is down for a while does not change the icon.
func (c *Core) fetchRemoteIcon(ctx context.Context, url string) *remoteIcon {
	ic := c.icons
	ic.mu.Lock()
	hit := ic.m[url]
	fresh := hit.body != nil && time.Since(hit.at) < 10*time.Minute
	if !fresh && time.Since(hit.failed) >= iconRetry {
		done := ic.busy[url]
		if done == nil {
			done = make(chan struct{})
			ic.busy[url] = done
			go func() {
				got := c.loadRemoteIcon(url)
				ic.mu.Lock()
				defer ic.mu.Unlock()
				if got != nil {
					ic.m[url] = *got
				} else {
					was := ic.m[url]
					was.failed = time.Now()
					ic.m[url] = was
				}
				delete(ic.busy, url)
				close(done)
			}()
		}
		ic.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		ic.mu.Lock()
		hit = ic.m[url]
	}
	ic.mu.Unlock()
	if hit.body == nil {
		return nil
	}
	return &hit
}

// loadRemoteIcon is routes.ts's fetchRemoteIcon: public images need no
// credential; a platform blob answers 401 without one, so the deployment's
// is tried.
func (c *Core) loadRemoteIcon(url string) *remoteIcon {
	attempt := func(withKey bool) *remoteIcon {
		key := ""
		if withKey {
			if key = c.a.Providers.GatewayKey(); key == "" {
				return nil
			}
		}
		req, err := http.NewRequest("GET", url, nil)
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
		t := imageType(b, res.Header.Get("Content-Type"))
		if t == "" {
			return nil
		}
		return &remoteIcon{body: b, typ: t, at: time.Now()}
	}
	if got := attempt(false); got != nil {
		return got
	}
	return attempt(true)
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

// sendMark answers with the Studio's own mark in place of a configured image
// that cannot be loaded; failed is the answer where there is no built web app
// to take it from. Not kept by the browser, so the image shows once it can be.
func (c *Core) sendMark(w http.ResponseWriter, failed error) error {
	b, err := os.ReadFile(filepath.Join(c.a.Cfg.WebDist, "icon-512.png"))
	if err != nil {
		return failed
	}
	return sendIcon(w, "image/png", "no-store", b)
}

func (c *Core) brandIcon(w http.ResponseWriter, r *http.Request) error {
	icon := c.configuredIcon(r.URL.Query().Get("variant") == "dark")
	if isURL(icon) {
		got := c.fetchRemoteIcon(r.Context(), icon)
		if got == nil {
			return c.sendMark(w, apierr.New(502, "could not load the configured brand image"))
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
		got := c.fetchRemoteIcon(r.Context(), configured)
		if got == nil {
			return c.sendMark(w, apierr.New(502, "could not load the configured favicon"))
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

// ImageSizes is routes.ts's imageSizes: an image's pixel size where its
// header is read, else "any" (an SVG has none).
func ImageSizes(body []byte, typ string) string {
	if strings.Contains(typ, "svg") {
		return "any"
	}
	if i, _, err := image.DecodeConfig(bytes.NewReader(body)); err == nil {
		return strconv.Itoa(i.Width) + "x" + strconv.Itoa(i.Height)
	}
	return "any"
}

// minInstallIcon is the smallest icon Chrome takes for an app it installs.
const minInstallIcon = 144

// InstallIcon is routes.ts's installIcon: a deployment's own image as the
// manifest lists it, or nil when an installed app cannot have it and has the
// Studio's mark: not an image, not square, too small, or an SVG, from which
// Chrome made a gray square with a letter. src is relative to the manifest,
// so it holds under a path.
func InstallIcon(src string, body []byte, typ string) *jsonobj.Object {
	if typ = imageType(body, typ); typ == "" {
		return nil
	}
	sizes := ImageSizes(body, typ)
	w, h, _ := strings.Cut(sizes, "x")
	if n, _ := strconv.Atoi(w); w != h || n < minInstallIcon {
		return nil
	}
	o := jsonobj.New()
	o.SetValue("src", strings.TrimPrefix(src, "/"))
	o.SetValue("type", typ)
	o.SetValue("sizes", sizes)
	return o
}

// markSrc is one of the Studio's own icon files as the manifest names it:
// relative to the manifest, and with the file's hash, since a browser that
// installed the Studio looks at an icon again only when its address changes.
func (c *Core) markSrc(file string) string {
	b, err := os.ReadFile(filepath.Join(c.a.Cfg.WebDist, file))
	if err != nil {
		return file
	}
	sum := sha1.Sum(b)
	return file + "?v=" + hex.EncodeToString(sum[:])[:10]
}

var hexColor = regexp.MustCompile(`(?i)^#[0-9a-f]{6}$`)
var spaces = regexp.MustCompile(`\s+`)

// GroundCookie holds the page ground the person last saw, as six hex
// digits; the page sets it (web/src/theme.ts) and the manifest reads it.
const GroundCookie = "ade_ground"

var groundForm = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// GroundOf is the request's ground cookie as "#rrggbb", or "" when it has
// none or one of another form.
func GroundOf(r *http.Request) string {
	c, err := r.Cookie(GroundCookie)
	if err != nil || !groundForm.MatchString(c.Value) {
		return ""
	}
	return "#" + strings.ToLower(c.Value)
}

// WebManifest is routes.ts's webManifest, keys in its order. ground is the
// page ground the person last saw ("#rrggbb", or "" for none): the splash an
// installed app shows before the page paints takes it, so a person on the
// light theme of a dark deployment does not open on a dark screen. Without
// it the splash is the deployment's default theme's ground. brand is the
// deployment's own icon (InstallIcon); with one, the Studio's mark is not
// listed, since browsers take the largest icon, and on a Mac and on Android
// the maskable one, whatever comes first. src names the mark's files.
func WebManifest(name string, dark bool, brand *jsonobj.Object, banner, ground string, src func(file string) string) *jsonobj.Object {
	bg := "#f3f4f6"
	if dark {
		bg = "#0c1320"
	}
	if hexColor.MatchString(ground) {
		bg = strings.ToLower(ground)
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
	icon := func(file, sizes, purpose string) *jsonobj.Object {
		i := jsonobj.New()
		i.SetValue("src", src(file))
		i.SetValue("sizes", sizes)
		i.SetValue("type", "image/png")
		i.SetValue("purpose", purpose)
		return i
	}
	icons := []any{icon("icon-192.png", "192x192", "any"), icon("icon-512.png", "512x512", "any"), icon("icon-maskable-512.png", "512x512", "maskable")}
	if brand != nil {
		icons = []any{brand}
	}
	o.SetValue("icons", icons)
	return o
}

// manifestIconWait is how long the manifest waits for a configured image.
var manifestIconWait = 5 * time.Second

func (c *Core) manifest(w http.ResponseWriter, r *http.Request) error {
	eff := c.a.Settings.Effective()
	var brand *jsonobj.Object
	if configured := c.configuredIcon(false); configured != "" {
		var body []byte
		typ := ""
		if isURL(configured) {
			// The manifest does not wait long for it: an install is waiting.
			ctx, stop := context.WithTimeout(r.Context(), manifestIconWait)
			if got := c.fetchRemoteIcon(ctx, configured); got != nil {
				body, typ = got.body, got.typ
			}
			stop()
		} else if b, err := os.ReadFile(configured); err == nil {
			body, typ = b, MimeFor(configured)
		}
		if u := c.BrandURL("icon", false); u != nil {
			brand = InstallIcon(*u, body, typ)
		}
	}
	banner := ""
	if jsval.Trim(eff.BannerText) != "" {
		banner = eff.BannerColor
		if banner == "" {
			banner = "#24612e"
		}
	}
	b, err := WebManifest(eff.AppName, eff.Theme == "dark", brand, banner, GroundOf(r), c.markSrc).MarshalJSON()
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
	md = helpdoc.Fill(md, eff.AppName, eff.KbLabel)
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
	// Whether people sign in: it decides what the main library is called
	// (theirs, or the one they share) and what My library means.
	set("signIn", c.a.Cfg.AuthEnabled())
	// Whether the one who asks has not signed in, on a Studio where that is
	// not required, and the Studio's own sign-in page where it has one.
	if c.a.Cfg.AuthEnabled() && c.a.Cfg.FrontAuth && auth.UserOf(r) == nil {
		set("signedOut", true)
	}
	if c.a.Cfg.OIDC.Enabled() {
		set("signInUrl", auth.LoginPath)
	}
	// Whether each person uses an ACTIVATE sign-in of their own here before
	// models answer them (REQUIRE_PERSONAL_KEY): the app then says so,
	// rather than that the Studio works without one.
	if eff.RequirePersonalKey && c.a.Cfg.AuthEnabled() && !c.a.Cfg.DisablePersonalKeys {
		set("signInFirst", true)
	}
	// The address links to items start with, where it is fixed.
	set("linkBase", links.Base(os.Getenv("STUDIO_SELF_URL"), c.a.Cfg.OIDC.RedirectURI, auth.CallbackPath))
	// Whether the Studio's settings are its administrators' and not this
	// person's: the app then keeps a preference such as the sidebar's
	// width in the browser instead.
	set("settingsLocked", c.SettingsLocked(r) != "")
	set("theme", eff.Theme)
	set("accent", eff.Accent)
	set("surface", eff.Surface)
	set("navWidth", eff.NavWidth)
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
	// Asked after platform, which asks for the person's token and so finds
	// a kept sign-in that stopped working.
	if f := c.a.Providers.SignInStopped; f != nil {
		if u := auth.UserOf(r); u != nil && f(u.ID) != "" {
			set("signInStopped", true)
		}
	}
	if f := c.a.Providers.LocalCode; f != nil && f(r) {
		set("localCode", true)
	}
	if f := c.a.Providers.Accounts; f != nil && f(r) {
		set("accounts", true)
	}
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
