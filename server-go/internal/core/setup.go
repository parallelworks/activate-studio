package core

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/state"
)

// First-run setup for a single-user install (setup.ts): the icon, the
// primary folder, a build of its whole index, the port, and a record that
// setup is done. Everything but the status refuses where users sign in.

// IconMaxBytes is setup.ts's ICON_MAX_BYTES.
const IconMaxBytes = 1024 * 1024

func (c *Core) requireSingleUser() error {
	if c.a.Cfg.AuthEnabled() {
		return apierr.New(403, "setup is for a single-user install; this Studio signs users in")
	}
	return nil
}

func (c *Core) embedModel() string {
	return filepath.Join(c.a.Cfg.IndexBase, "models", "minilm384.gguf")
}

// ExpandPath is setup.ts's expandPath: ~ is the home folder, and a
// relative path is taken from it.
func ExpandPath(p string) string {
	home, _ := os.UserHomeDir()
	if h := os.Getenv("HOME"); h != "" {
		home = h
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(home, p)
}

func isDir(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

func realOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if a, err := filepath.Abs(r); err == nil {
			return a
		}
	}
	a, _ := filepath.Abs(p)
	return a
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func (c *Core) gufiFound() bool {
	return exists(filepath.Join(c.a.Cfg.GufiBin, "gufi_dir2index")) && exists(filepath.Join(c.a.Cfg.GufiBin, "gufi_query"))
}

// EarlierIndexOf is indexing.ts's earlierIndexOf: an index of root kept
// from before another folder was chosen (INDEX_DIR/folders/<sha1 of the
// path, 16>/root), with when its tree was built; JSON null for none.
func EarlierIndexOf(indexDir, root string) json.RawMessage {
	abs, _ := filepath.Abs(root)
	sum := sha1.Sum([]byte(abs))
	d := filepath.Join(indexDir, "folders", hex.EncodeToString(sum[:])[:16])
	if !exists(filepath.Join(d, "root")) {
		return json.RawMessage("null")
	}
	var builtAt any
	if st, err := os.Stat(filepath.Join(d, "gufi", "db.db")); err == nil {
		builtAt = state.ISO(st.ModTime())
	}
	b, _ := json.Marshal(map[string]any{"builtAt": builtAt})
	return b
}

// runningJob is currentBuild(): the whole-folder build while queued or
// running, or nil.
func (c *Core) runningJob(r *http.Request) (json.RawMessage, error) {
	st, err := c.a.Providers.SetupIndexState(r, true, false)
	if err != nil {
		return nil, err
	}
	if jsonobj.IsNullish(st.Job) {
		return nil, nil
	}
	return st.Job, nil
}

// PortFree is setup.ts's portFree: nothing answers on the port where the
// Studio listens, and it can be bound.
func PortFree(port int, host string) bool {
	dial := host
	if host == "0.0.0.0" || host == "::" || host == "" {
		dial = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(dial, strconv.Itoa(port)), time.Second)
	if err == nil {
		conn.Close()
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// nextPort is the port saved for the next start, when it is not the one in use.
func (c *Core) nextPort() *int {
	saved, err := strconv.Atoi(app.ReadLauncherSettings()["PORT"])
	if err != nil || saved <= 0 || saved == c.a.Cfg.Port {
		return nil
	}
	return &saved
}

func (c *Core) branding() *jsonobj.Object {
	eff := c.a.Settings.Effective()
	o := jsonobj.New()
	var light, dark any = json.RawMessage("null"), json.RawMessage("null")
	if eff.IconURL != "" {
		if u := c.BrandURL("icon", false); u != nil {
			light = *u
		}
	}
	if eff.IconURLDark != "" {
		if u := c.BrandURL("icon", true); u != nil {
			dark = *u
		}
	}
	o.SetValue("light", light)
	o.SetValue("dark", dark)
	o.SetValue("iconUrl", eff.IconURL)
	o.SetValue("iconUrlDark", eff.IconURLDark)
	return o
}

func (c *Core) setupStatus(w http.ResponseWriter, r *http.Request) error {
	if c.a.Cfg.AuthEnabled() {
		o := jsonobj.New()
		o.SetValue("firstRun", false)
		o.SetValue("standalone", false)
		o.SetValue("canRestart", false)
		return ok(w, o)
	}
	cfg := c.a.Cfg
	eff := c.a.Settings.Effective()
	indexed := exists(filepath.Join(cfg.GufiIndex, "db.db"))
	wantFiles := indexed && r.URL.Query().Get("files") == "1"
	ix, err := c.a.Providers.SetupIndexState(r, indexed, wantFiles)
	if err != nil {
		return err
	}
	found := c.gufiFound()
	model := exists(c.embedModel())
	o := jsonobj.New()
	o.SetValue("firstRun", !eff.SetupDone && os.Getenv("PW_PLATFORM_HOST") == "")
	o.SetValue("standalone", true)
	o.SetValue("canRestart", app.CanRestart() && app.LauncherConfigFile() != "")
	o.SetValue("launcher", app.LauncherConfigFile() != "")
	if b := os.Getenv("STUDIO_APP_BUNDLE"); b != "" {
		o.SetValue("macApp", b)
	} else {
		o.SetValue("macApp", json.RawMessage("null"))
	}
	o.SetValue("port", cfg.Port)
	if n := c.nextPort(); n != nil {
		o.SetValue("portNext", *n)
	} else {
		o.SetValue("portNext", json.RawMessage("null"))
	}
	primary := jsonobj.New()
	primary.SetValue("path", cfg.KBRoot)
	primary.SetValue("exists", isDir(cfg.KBRoot))
	primary.SetValue("writable", writable(cfg.KBRoot))
	primary.SetValue("indexed", indexed)
	if wantFiles && ix.Files != nil {
		primary.SetValue("files", *ix.Files)
	}
	if indexed {
		primary.SetValue("earlierIndex", json.RawMessage("null"))
	} else {
		primary.SetValue("earlierIndex", EarlierIndexOf(cfg.IndexDir, cfg.KBRoot))
	}
	o.SetValue("primary", primary)
	g := jsonobj.New()
	g.SetValue("found", found)
	g.SetValue("bin", cfg.GufiBin)
	g.SetValue("semantic", found && model && exists(filepath.Join(cfg.GufiBin, "gufi_sqlite3")))
	o.SetValue("gufi", g)
	em := jsonobj.New()
	em.SetValue("path", c.embedModel())
	em.SetValue("found", model)
	o.SetValue("embedModel", em)
	o.SetValue("branding", c.branding())
	libs, err := c.a.Providers.Libraries(r, eff.KbLabel, viewer(r))
	if err != nil {
		return err
	}
	if libs == nil {
		libs = []json.RawMessage{}
	}
	o.SetValue("libraries", libs)
	o.SetValue("job", orNullRaw(ix.Job))
	return ok(w, o)
}

func orNullRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

var digitRun = regexp.MustCompile(`\d+|\D+`)

// localeLess is a.localeCompare(b, undefined, {sensitivity: 'base',
// numeric: true}) < 0, for folder names: case and accents aside, digit runs
// by value.
func localeLess(a, b string) bool {
	fold := func(s string) string {
		return strings.Map(func(r rune) rune { return unicode.ToLower(r) }, s)
	}
	pa, pb := digitRun.FindAllString(fold(a), -1), digitRun.FindAllString(fold(b), -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, y := pa[i], pb[i]
		if x == y {
			continue
		}
		nx, ex := strconv.ParseUint(x, 10, 64)
		ny, ey := strconv.ParseUint(y, 10, 64)
		if ex == nil && ey == nil && nx != ny {
			return nx < ny
		}
		return x < y
	}
	return len(pa) < len(pb)
}

func (c *Core) setupBrowse(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	raw := jsval.Trim(r.URL.Query().Get("path"))
	if raw == "" {
		raw = "~"
	}
	abs := ExpandPath(raw)
	st, err := os.Stat(abs)
	if err != nil {
		return apierr.New(404, "no such folder: %s", abs)
	}
	if !st.IsDir() {
		return apierr.New(400, "not a folder: %s", abs)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return apierr.New(403, "cannot read %s (%s)", abs, errnoCode(err))
	}
	type dir struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := []dir{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isD := e.IsDir()
		if !isD && e.Type()&os.ModeSymlink != 0 {
			isD = isDir(filepath.Join(abs, e.Name()))
		}
		if isD {
			dirs = append(dirs, dir{e.Name(), filepath.Join(abs, e.Name())})
		}
	}
	sortStable(dirs, func(a, b dir) bool { return localeLess(a.Name, b.Name) })
	if len(dirs) > 2000 {
		dirs = dirs[:2000]
	}
	o := jsonobj.New()
	o.SetValue("path", abs)
	if parent := filepath.Dir(abs); parent == abs {
		o.SetValue("parent", json.RawMessage("null"))
	} else {
		o.SetValue("parent", parent)
	}
	o.SetValue("writable", writable(abs))
	o.SetValue("dirs", dirs)
	return ok(w, o)
}

func errnoCode(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "EACCES"
	case errors.Is(err, os.ErrNotExist):
		return "ENOENT"
	}
	return "error"
}

func (c *Core) setupPrimaryFolder(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	b, err := body(r)
	if err != nil {
		return err
	}
	raw := ""
	if !jsonobj.IsNullish(b["path"]) {
		raw = jsval.Trim(jsval.String(b["path"]))
	}
	if raw == "" {
		return apierr.New(400, "path required")
	}
	abs := ExpandPath(raw)
	if !exists(abs) {
		return apierr.New(400, "no such folder: %s", abs)
	}
	if !isDir(abs) {
		return apierr.New(400, "not a folder: %s", abs)
	}
	if !writable(abs) {
		return apierr.New(400, "%s is not writable", abs)
	}
	real := realOr(abs)
	cfg := c.a.Cfg
	for _, index := range uniq(realOr(cfg.IndexBase), realOr(cfg.IndexDir)) {
		if within(real, index) || within(index, real) {
			return apierr.New(400, "%s holds the Studio's own index (%s); choose a folder that does not contain it", abs, index)
		}
	}
	if real == realOr(cfg.KBRoot) {
		return ok(w, ordered("saved", true, "restarting", false, "note", "This folder is already in use."))
	}
	if job, err := c.runningJob(r); err != nil {
		return err
	} else if job != nil {
		return apierr.New(409, "an index build is running; wait for it to finish")
	}
	if app.LauncherConfigFile() == "" {
		return ok(w, ordered("saved", false, "restarting", false, "note", fmt.Sprintf("Set KB_ROOT=%s in the Studio's environment and restart it.", abs)))
	}
	if err := app.SaveLauncherSetting("KB_ROOT", abs); err != nil {
		return err
	}
	restarting := c.a.RequestRestart(app.RestartExitCode)
	earlier := EarlierIndexOf(cfg.IndexDir, abs)
	var o *jsonobj.Object
	if restarting {
		o = ordered("saved", true, "restarting", true)
	} else {
		o = ordered("saved", true, "restarting", false, "note", "Saved. Restart the Studio to open this folder.")
	}
	if !jsonobj.IsNullish(earlier) {
		o.Set("earlierIndex", earlier)
	}
	return ok(w, o)
}

func uniq(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// ordered is an object of key, value pairs in order.
func ordered(kv ...any) *jsonobj.Object {
	o := jsonobj.New()
	for i := 0; i+1 < len(kv); i += 2 {
		o.SetValue(kv[i].(string), kv[i+1])
	}
	return o
}

func (c *Core) setupPort(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	b, err := body(r)
	if err != nil {
		return err
	}
	pv := b["port"]
	if jsonobj.IsNullish(pv) {
		pv = nil
	}
	n := jsval.Number(pv)
	if !jsval.IsInteger(n) || n < 1024 || n > 65535 {
		return apierr.New(400, "the port must be a whole number from 1024 to 65535")
	}
	port := int(n)
	cfg := c.a.Cfg
	if port == cfg.Port {
		if app.LauncherConfigFile() != "" && c.nextPort() != nil {
			if err := app.SaveLauncherSetting("PORT", strconv.Itoa(cfg.Port)); err != nil {
				return err
			}
		}
		return ok(w, ordered("saved", true, "restarting", false, "port", port, "note", "The Studio already uses this port."))
	}
	if app.LauncherConfigFile() == "" {
		return ok(w, ordered("saved", false, "restarting", false, "port", port, "note", fmt.Sprintf("Set PORT=%d in the Studio's environment and restart it.", port)))
	}
	if job, err := c.runningJob(r); err != nil {
		return err
	} else if job != nil {
		return apierr.New(409, "an index build is running; wait for it to finish")
	}
	if !PortFree(port, cfg.Host) {
		return apierr.New(409, "port %d is in use by another program", port)
	}
	if err := app.SaveLauncherSetting("PORT", strconv.Itoa(port)); err != nil {
		return err
	}
	if c.a.RequestRestart(app.RestartExitCode) {
		return ok(w, ordered("saved", true, "restarting", true, "port", port))
	}
	return ok(w, ordered("saved", true, "restarting", false, "port", port, "note", "Saved. Restart the Studio to use this port."))
}

func (c *Core) setupIndex(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	if job, err := c.runningJob(r); err != nil {
		return err
	} else if job != nil {
		return ok(w, job)
	}
	if !c.gufiFound() {
		return apierr.New(409, "GUFI is not installed: no gufi_dir2index in %s", c.a.Cfg.GufiBin)
	}
	if !isDir(c.a.Cfg.KBRoot) {
		return apierr.New(409, "the folder %s does not exist", c.a.Cfg.KBRoot)
	}
	job, err := c.a.Providers.StartBuild(r)
	if err != nil {
		return providerError(err)
	}
	return ok(w, job)
}

// providerError passes Node's own error answer through as it was, and
// makes a provider with no owner in Node-free mode a 501.
func providerError(err error) error {
	var ne *app.NodeError
	if errors.As(err, &ne) {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(ne.Body, &e) == nil && e.Error != "" {
			return apierr.New(ne.Status, "%s", e.Error)
		}
	}
	var un *app.Unported
	if errors.As(err, &un) {
		return apierr.New(501, "%s", un.Error())
	}
	return err
}

// IconType is setup.ts's iconType: the image type by its first bytes.
func IconType(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "jpg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	case bytes.IndexByte(b, 0) >= 0:
		return ""
	}
	text := strings.TrimLeftFunc(strings.TrimPrefix(string(bytes.ToValidUTF8(b, []byte("�"))), "\uFEFF"), isJSSpace)
	if strings.HasPrefix(text, "<") && svgTag.MatchString(text) {
		return "svg"
	}
	return ""
}

func isJSSpace(r rune) bool { return jsval.Trim(string(r)) == "" }

var svgTag = regexp.MustCompile(`(?i)<svg[\s>]`)

var svgChecks = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?i)<script\b`), "a script"},
	{regexp.MustCompile(`(?i)<foreignObject\b`), "a foreignObject"},
	{regexp.MustCompile(`(?i)<(?:iframe|embed|object)\b`), "embedded content"},
	{regexp.MustCompile(`(?i)<!(?:ENTITY|DOCTYPE)\b`), "a DOCTYPE or entity"},
	{regexp.MustCompile(`(?i)<\?xml-stylesheet\b`), "a stylesheet reference"},
	{regexp.MustCompile(`(?i)\son[a-z]+\s*=`), "an event handler"},
	{regexp.MustCompile(`(?i)javascript:`), "a javascript: link"},
	{regexp.MustCompile(`(?i)@import\b`), "a CSS import"},
	{nil, "a link to another file"},
	{nil, "a reference to another file"},
	{regexp.MustCompile(`(?i)<(?:set|animate)\b[^>]*attributeName\s*=\s*["']?(?:xlink:)?href`), "an animated link"},
}

// Go's regexp has no lookahead: setup.ts's two checks
// /\bhref\s*=(?!\s*["']?\s*(?:#|data:image\/(?:png|jpe?g|gif|webp);))/ and
// the same for url( are a match of the head followed by a test of what
// comes after it.
var (
	hrefHead = regexp.MustCompile(`(?i)\bhref\s*=`)
	urlHead  = regexp.MustCompile(`(?i)url\(`)
	safeRef  = regexp.MustCompile(`(?i)^\s*["']?\s*(?:#|data:image/(?:png|jpe?g|gif|webp);)`)
)

func unsafeRef(head *regexp.Regexp, s string) bool {
	for _, loc := range head.FindAllStringIndex(s, -1) {
		if !safeRef.MatchString(s[loc[1]:]) {
			return true
		}
	}
	return false
}

// SVGProblem is setup.ts's svgProblem: why an SVG cannot be a brand icon,
// or "" when it can.
func SVGProblem(svg string) string {
	for i, ch := range svgChecks {
		var hit bool
		switch i {
		case 8:
			hit = unsafeRef(hrefHead, svg)
		case 9:
			hit = unsafeRef(urlHead, svg)
		default:
			hit = ch.re.MatchString(svg)
		}
		if hit {
			return "This SVG contains " + ch.what + ". Export it as a plain SVG, or as a PNG."
		}
	}
	return ""
}

func (c *Core) brandingDir() string { return filepath.Join(c.a.Cfg.IndexBase, "branding") }

var iconFileRe = regexp.MustCompile(`^icon-[0-9a-f]{16}\.(png|jpg|webp|svg)$`)

// dropUnusedIcons removes icon files no setting names any more.
func (c *Core) dropUnusedIcons() {
	saved := c.a.Settings.SavedObject()
	keep := map[string]bool{}
	for _, k := range []string{"iconUrl", "iconUrlDark"} {
		if s, ok := jsonobj.String(saved.Get(k)); ok && s != "" {
			keep[s] = true
		}
	}
	names, err := os.ReadDir(c.brandingDir())
	if err != nil {
		return
	}
	for _, n := range names {
		f := filepath.Join(c.brandingDir(), n.Name())
		if iconFileRe.MatchString(n.Name()) && !keep[f] {
			os.RemoveAll(f)
		}
	}
}

func variantField(r *http.Request) string {
	if r.URL.Query().Get("variant") == "dark" {
		return "iconUrlDark"
	}
	return "iconUrl"
}

// firstFile is @fastify/multipart's req.file({limits: {fileSize: limit,
// files: 1}}): the first file part, read up to limit, and whether it was
// cut there.
func firstFile(r *http.Request, limit int64) (*multipart.Part, []byte, bool, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, nil, false, err
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil, false, nil
			}
			return nil, nil, false, err
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			return nil, nil, false, err
		}
		if int64(len(b)) > limit {
			return part, b[:limit], true, nil
		}
		return part, b, false, nil
	}
}

func (c *Core) setupIcon(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	field := variantField(r)
	part, b, cut, err := firstFile(r, IconMaxBytes)
	if err != nil {
		return apierr.New(400, "send the icon as multipart/form-data")
	}
	if part == nil {
		return apierr.New(400, "no image in the request")
	}
	if cut {
		return apierr.New(413, "The icon is over 1 MB.")
	}
	typ := IconType(b)
	if typ == "" {
		return apierr.New(415, "The icon must be a PNG, JPEG, SVG or WebP image.")
	}
	if typ == "svg" {
		if p := SVGProblem(string(b)); p != "" {
			return apierr.New(415, "%s", p)
		}
	}
	if err := os.MkdirAll(c.brandingDir(), 0o755); err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	file := filepath.Join(c.brandingDir(), "icon-"+hex.EncodeToString(sum[:])[:16]+"."+typ)
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return err
	}
	if err := c.a.Settings.Update(map[string]any{field: file}); err != nil {
		return err
	}
	c.dropUnusedIcons()
	return ok(w, c.branding())
}

func (c *Core) setupIconDelete(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	if err := c.a.Settings.Update(map[string]any{variantField(r): ""}); err != nil {
		return err
	}
	c.dropUnusedIcons()
	return ok(w, c.branding())
}

func (c *Core) setupComplete(w http.ResponseWriter, r *http.Request) error {
	if err := c.requireSingleUser(); err != nil {
		return err
	}
	saved := c.a.Settings.SavedObject()
	eff := c.a.Settings.Effective()
	patch := map[string]any{"setupDone": true}
	if !saved.Has("aiEnabled") || jsonobj.IsNullish(saved.Get("aiEnabled")) {
		patch["aiEnabled"] = eff.AiEnabled
	}
	if (!saved.Has("sections") || jsonobj.IsNullish(saved.Get("sections"))) && len(eff.Sections) > 0 {
		patch["sections"] = eff.Sections
	}
	if err := c.a.Settings.Update(patch); err != nil {
		return err
	}
	return ok(w, map[string]bool{"setupDone": true})
}
