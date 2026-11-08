package workflows

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/wfparser"
	"github.com/parallelworks/activate-studio/server-go/internal/workspace"
)

// The Workflows tab (workflowsTab.ts): a deployment's curated set of
// workflows, shown as tiles and run from their own forms. Everything acts
// as the viewer, with their own platform key, else the deployment's.

// CLI runs the pw CLI with a key (PW_API_KEY) and a timeout; a failure's
// error is the CLI's trimmed stderr or message, or ErrMissingCLI.
type CLI func(ctx context.Context, args []string, key string, timeout time.Duration) (string, error)

// ErrMissingCLI is the pw CLI not being found, even after looking again.
var ErrMissingCLI = errors.New(platform.NoCLIMessage)

// Routes holds the routes' state: the CLI, the caches and the registry.
type Routes struct {
	a      *app.App
	p      *platform.Platform
	CLI    CLI
	Runs   *runs.Registry
	Schema *Schema
	// Curated is the workflow collection (Settings, or STUDIO_WORKFLOWS).
	Curated func() []string

	mu          sync.Mutex
	listCache   map[string]rowsHit
	marketCache map[string]rowsHit
	iconCache   map[string]blob
	runChecked  map[string]time.Time
	readmes     map[string]readmeValue
	assets      map[string]blob
}

type rowsHit struct {
	at   time.Time
	rows []any
}

type blob struct {
	at   time.Time
	typ  string
	body []byte
}

// NewRoutes is the routes' state for p.
func NewRoutes(a *app.App, p *platform.Platform) *Routes {
	r := &Routes{a: a, p: p, Runs: runs.New(p), Schema: NewSchema(p)}
	r.CLI = r.execCLI
	r.Curated = func() []string {
		if a == nil || a.Settings == nil {
			return nil
		}
		return a.Settings.Effective().WorkflowCollection
	}
	r.Reset()
	return r
}

// Reset empties the caches (resetWorkflowsTabForTests, resetWorkflowReadmeForTests).
func (r *Routes) Reset() {
	r.mu.Lock()
	r.listCache, r.marketCache = map[string]rowsHit{}, map[string]rowsHit{}
	r.iconCache, r.assets = map[string]blob{}, map[string]blob{}
	r.runChecked = map[string]time.Time{}
	r.readmes = map[string]readmeValue{}
	r.mu.Unlock()
}

func (r *Routes) execCLI(ctx context.Context, args []string, key string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	env := map[string]string{}
	if key != "" {
		env["PW_API_KEY"] = key
	}
	o := platform.ExecOptions{Timeout: timeout, MaxBuffer: 32 << 20, Env: env}
	out, err := r.p.Exec(ctx, args, o)
	if platform.IsMissing(err) {
		// Not found where it was: look again, it may have been installed since.
		if path, _ := r.p.FindCLI(); path == "" {
			return "", ErrMissingCLI
		}
		out, err = r.p.Exec(ctx, args, o)
		if platform.IsMissing(err) {
			return "", ErrMissingCLI
		}
	}
	if err != nil {
		return "", plainError(strings.TrimSpace(platform.ErrText(err)))
	}
	return out, nil
}

var (
	ansiColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	logPrefix = regexp.MustCompile(`(?m)^\S+Z \[(ERROR|WARN)\] `)
	authFail  = regexp.MustCompile(`(?i)authentication has expired|please authenticate|401|unauthorized`)
)

func cleanCLI(msg string) string { return logPrefix.ReplaceAllString(ansiColor.ReplaceAllString(msg, ""), "") }

// PlatformError is platformError: a failed CLI call as an error the page
// can show.
func (r *Routes) PlatformError(err error) error {
	if errors.Is(err, ErrMissingCLI) {
		return platform.KbError(503, platform.NoCLIMessage)
	}
	text := jsTrim(cleanCLI(err.Error()))
	if authFail.MatchString(text) {
		return platform.KbError(401, "The platform credential has expired. Add your own platform API key under Settings, Model access, or renew the deployment credential.")
	}
	if netx.CLINetworkFailure(text) {
		return platform.KbError(503, netx.UnreachableMessage(r.p.Host(), nil))
	}
	return platform.KbError(502, "The platform did not answer: "+sliceJS(strings.Split(text, "\n")[0], 300))
}

func (r *Routes) requireReachable(ctx context.Context) error {
	if re := r.p.Reachability(ctx); !re.OK {
		msg := re.Message
		if msg == "" {
			msg = "The platform cannot be reached."
		}
		return platform.KbError(503, msg)
	}
	return nil
}

// Call is workflowsCli: the platform reachable, then the CLI with key,
// its failure as PlatformError.
func (r *Routes) Call(ctx context.Context, args []string, key string, timeout time.Duration) (string, error) {
	if err := r.requireReachable(ctx); err != nil {
		return "", err
	}
	out, err := r.CLI(ctx, args, key, timeout)
	if err != nil {
		return "", r.PlatformError(err)
	}
	return out, nil
}

/* ---- JavaScript value helpers ---- */

func undef(v any) bool   { return v == jsonv.Undefined }
func nullish(v any) bool { return v == nil || undef(v) }

// at is a?.[k] on a decoded value.
func at(v any, keys ...string) any {
	for _, k := range keys {
		o := jsonv.Obj(v)
		if o == nil {
			return jsonv.Undefined
		}
		x, ok := o.Get(k)
		if !ok {
			return jsonv.Undefined
		}
		v = x
	}
	return v
}

// coalesce is a ?? b ?? ...: the first value that is not null or undefined.
func coalesce(vs ...any) any {
	for _, v := range vs {
		if !nullish(v) {
			return v
		}
	}
	return vs[len(vs)-1]
}

// or is a || b.
func or(a, b any) any {
	if !undef(a) && jsonv.Truthy(a) {
		return a
	}
	return b
}

// orNull is v ?? null.
func orNull(v any) any {
	if nullish(v) {
		return nil
	}
	return v
}

// jsStr is String(v).
func jsStr(v any) string {
	switch {
	case v == nil:
		return "null"
	case undef(v):
		return "undefined"
	}
	return jsonv.String(v)
}

func truthy(v any) bool { return !undef(v) && jsonv.Truthy(v) }

func keyTail(k string) string {
	if len(k) > 12 {
		return k[len(k)-12:]
	}
	return k
}

var collator = collate.New(language.Und)

// sortByDisplay is .sort((a, b) => a.displayName.localeCompare(b.displayName)).
func sortByDisplay(rows []*jsonv.Object) {
	sort.SliceStable(rows, func(i, j int) bool {
		return collator.CompareString(jsStr(at(rows[i], "displayName")), jsStr(at(rows[j], "displayName"))) < 0
	})
}

/* ---- lists ---- */

func (r *Routes) cachedRows(cache map[string]rowsHit, key string, ttl time.Duration, load func() ([]any, error)) ([]any, error) {
	k := keyTail(key)
	r.mu.Lock()
	hit, ok := cache[k]
	r.mu.Unlock()
	if ok && time.Since(hit.at) < ttl {
		return hit.rows, nil
	}
	rows, err := load()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	cache[k] = rowsHit{time.Now(), rows}
	r.mu.Unlock()
	return rows, nil
}

func listOf(raw any, field string) []any {
	if a, ok := raw.([]any); ok {
		return a
	}
	return jsonv.Arr(coalesce(at(raw, field), []any{}))
}

// listFor is the key's account workflows, kept a minute.
func (r *Routes) listFor(ctx context.Context, key string) ([]any, error) {
	return r.cachedRows(r.listCache, key, time.Minute, func() ([]any, error) {
		out, err := r.Call(ctx, []string{"workflows", "ls", "-o", "json"}, key, 90*time.Second)
		if err != nil {
			return nil, err
		}
		raw, err := platform.FirstJSON(out)
		if err != nil {
			return nil, err
		}
		rows := listOf(raw, "workflows")
		if rows == nil {
			rows = []any{}
		}
		return rows, nil
	})
}

// marketplaceFor is the marketplace's workflows, kept five minutes.
func (r *Routes) marketplaceFor(ctx context.Context, key string) ([]any, error) {
	return r.cachedRows(r.marketCache, key, 5*time.Minute, func() ([]any, error) {
		out, err := r.Call(ctx, []string{"marketplace", "ls", "-o", "json"}, key, 90*time.Second)
		if err != nil {
			return nil, err
		}
		raw, err := platform.FirstJSON(out)
		if err != nil {
			return nil, err
		}
		rows := []any{}
		for _, m := range listOf(raw, "items") {
			if t := at(m, "type"); !truthy(t) || t == "workflow" {
				rows = append(rows, m)
			}
		}
		return rows, nil
	})
}

func findBy(rows []any, field, want string) any {
	for _, w := range rows {
		if at(w, field) == want {
			return w
		}
	}
	return jsonv.Undefined
}

// CuratedNames is curatedNames.
func (r *Routes) CuratedNames() []string {
	var out []string
	for _, n := range r.Curated() {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (r *Routes) curated(entry string) bool {
	for _, n := range r.CuratedNames() {
		if n == entry {
			return true
		}
	}
	return false
}

/* ---- references and icons ---- */

// ResolveStudioRefs is resolveStudioRefs: the cluster and bucket pickers'
// objects as the pw:// string they carry.
func ResolveStudioRefs(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ResolveStudioRefs(e)
		}
		return out
	case *jsonv.Object:
		if ref, ok := at(x, "_studioRef").(string); ok {
			return ref
		}
		o := jsonv.NewObject()
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			o.Set(k, ResolveStudioRefs(e))
		}
		return o
	}
	return v
}

// IconCandidates is iconCandidates: the addresses to try for an icon, in order.
func IconCandidates(imageURL, fallbackURL, platformOrigin string) []string {
	abs := func(u string) string {
		if u == "" {
			return ""
		}
		if strings.HasPrefix(u, "/") {
			return platformOrigin + u
		}
		return u
	}
	primary, fallback := abs(imageURL), abs(fallbackURL)
	anonymous := true
	if u, err := url.Parse(primary); err == nil && u.Scheme != "" && u.Host != "" {
		h := u.Hostname()
		anonymous = strings.HasPrefix(primary, platformOrigin) || h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com")
	}
	ordered := []string{primary, fallback}
	if !anonymous {
		ordered = []string{fallback, primary}
	}
	var out []string
	seen := map[string]bool{}
	for _, u := range ordered {
		if strings.HasPrefix(u, "https://") && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// formEncode is URLSearchParams' encoding of a value.
func formEncode(v string) string {
	return strings.NewReplacer("%2A", "*", "~", "%7E").Replace(url.QueryEscape(v))
}

// searchParams is new URLSearchParams(pairs).toString().
func searchParams(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(formEncode(pairs[i]) + "=" + formEncode(pairs[i+1]))
	}
	return b.String()
}

// ThumbnailRoute is thumbnailRoute: the platform's thumbnail route for a
// repository file, rebuilt from its raw address, or "".
func ThumbnailRoute(imageURL string) string {
	u, err := url.Parse(imageURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	var parts []string
	for _, s := range strings.Split(u.EscapedPath(), "/") {
		if s == "" {
			continue
		}
		d, err := url.PathUnescape(s)
		if err != nil {
			return ""
		}
		parts = append(parts, d)
	}
	var repo, ref string
	var file []string
	if u.Hostname() == "raw.githubusercontent.com" {
		if len(parts) < 4 {
			return ""
		}
		repo, ref, file = parts[0]+"/"+parts[1], parts[2], parts[3:]
	} else {
		i := -1
		for j, p := range parts {
			if p == "-" {
				i = j
				break
			}
		}
		if i < 1 || i+1 >= len(parts) || parts[i+1] != "raw" || len(parts) < i+4 {
			return ""
		}
		repo, ref, file = u.Host+"/"+strings.Join(parts[:i], "/"), parts[i+2], parts[i+3:]
	}
	return "/api/repositories/thumbnail?" + searchParams("path", strings.Join(file, "/"), "ref", ref, "repo", repo)
}

/* ---- definitions ---- */

// Definition is a workflow's YAML and details, wherever it is defined.
type Definition struct {
	YAML                     any
	DisplayName, Description any
	Configurations           []any
	// RunAs is what the CLI runs: a GitHub directory resolves to its file.
	RunAs string
	// Permissions are the account variables a GitHub workflow declares.
	Permissions []string
	Source      *jsonv.Object
}

// RecordSource is recordSource: the origin a workflow record declares.
func RecordSource(doc any) *jsonv.Object {
	rem := at(doc, "remote")
	if repo := at(rem, "repo"); truthy(repo) {
		s := strings.TrimSuffix(httpScheme.ReplaceAllString(jsStr(repo), ""), ".git")
		return jsonv.NewObject("kind", "remote", "repo", s, "branch", or(at(rem, "branch"), nil), "path", or(at(rem, "yaml"), nil))
	}
	return jsonv.NewObject("kind", "local")
}

func entryError(raw string) error {
	return platform.KbError(400, "invalid workflow: "+raw+"; use a name, marketplace/<slug>, or github.com/<owner>/<repo>[/path][@ref]")
}

// WorkflowDefinition is workflowDefinition: a collection entry's
// definition, read with key, for the assistant's tools.
func (r *Routes) WorkflowDefinition(ctx context.Context, entry, key string) (*Definition, error) {
	e := ParseEntry(entry)
	if e == nil {
		return nil, entryError(entry)
	}
	return r.definitionOf(ctx, e, key)
}

func (r *Routes) definitionOf(ctx context.Context, e *Entry, key string) (*Definition, error) {
	switch e.Kind {
	case KindAccount:
		var doc any
		if rec := r.p.WorkflowRecord(ctx, e.Name, key); rec != nil {
			doc = rec
		} else {
			out, err := r.Call(ctx, []string{"workflows", "get", e.Name, "-o", "json"}, key, 60*time.Second)
			if err != nil {
				return nil, err
			}
			if doc, err = platform.FirstJSON(out); err != nil {
				return nil, err
			}
		}
		y := at(doc, "yaml")
		if s, ok := y.(string); ok {
			var err error
			if y, err = jsonv.ParseYAML(s); err != nil {
				return nil, err
			}
		}
		confs := []any{}
		for _, c := range jsonv.Arr(coalesce(at(doc, "configurations"), []any{})) {
			confs = append(confs, jsonv.NewObject("name", coalesce(at(c, "name"), at(c, "id"), ""), "inputs", coalesce(at(c, "inputs"), jsonv.NewObject())))
		}
		return &Definition{YAML: y, DisplayName: or(at(doc, "displayName"), e.Name), Description: coalesce(at(doc, "description"), ""),
			Configurations: confs, RunAs: e.Entry, Permissions: []string{}, Source: RecordSource(doc)}, nil
	case KindMarketplace:
		text, err := r.Call(ctx, []string{"marketplace", "get", e.Slug, "--yaml"}, key, 60*time.Second)
		if err != nil {
			return nil, err
		}
		y, err := jsonv.ParseYAML(text)
		if err != nil {
			return nil, err
		}
		rows, _ := r.marketplaceFor(ctx, key)
		m := findBy(rows, "slug", e.Slug)
		return &Definition{YAML: y, DisplayName: or(at(m, "name"), e.Slug), Description: coalesce(at(m, "description"), ""), Configurations: []any{},
			RunAs: e.Entry, Permissions: []string{}, Source: jsonv.NewObject("kind", "marketplace", "slug", e.Slug)}, nil
	}
	g, err := GitHubDefinitionOf(ctx, e)
	if err != nil {
		return nil, err
	}
	var ref, p any
	if e.Ref != nil {
		ref = *e.Ref
	}
	if e.Path != "" {
		p = e.Path
	}
	desc := g.Description
	if desc == "" {
		desc = g.Entry
	}
	return &Definition{YAML: g.YAML, DisplayName: g.Title, Description: desc, Configurations: []any{}, RunAs: g.Entry, Permissions: g.Permissions,
		Source: jsonv.NewObject("kind", "remote", "repo", "github.com/"+e.Owner+"/"+e.Repo, "branch", ref, "path", p)}, nil
}

/* ---- the routes ---- */

const noCredential = "No platform credential: add your ACTIVATE API key under Settings, Model access."

type viewerKey struct {
	key      string
	own      bool
	username any // null without one
	userID   string
}

func (r *Routes) keyOf(req *http.Request) (viewerKey, error) {
	v, err := r.p.ViewerOf(req)
	if err != nil {
		return viewerKey{}, err
	}
	key, own := r.p.KeyFor(v)
	var name any
	if v.User != nil && v.User.Username != "" {
		name = v.User.Username
	}
	return viewerKey{key: key, own: own, username: name, userID: v.UserID()}, nil
}

func (r *Routes) viewer(req *http.Request) (viewerKey, error) {
	v, err := r.keyOf(req)
	if err == nil && v.key == "" {
		err = platform.KbError(409, noCredential)
	}
	return v, err
}

func entryOf(req *http.Request) (*Entry, error) {
	e := ParseEntry(platform.Q(req, "w"))
	if e == nil {
		return nil, platform.KbError(400, "invalid workflow: use a name, marketplace/<slug>, or github.com/<owner>/<repo>[/path][@ref]")
	}
	return e, nil
}

// fresh is the tab's preHandler: a pw CLI sign-in about to expire is
// renewed before the credential is read.
func (r *Routes) fresh(h func(w http.ResponseWriter, req *http.Request) (any, error)) http.HandlerFunc {
	return platform.Handler(r.p.Log(), func(w http.ResponseWriter, req *http.Request) (any, error) {
		r.p.EnsureFreshAccess(8 * time.Second)
		return h(w, req)
	})
}

func (r *Routes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflows/catalog", r.fresh(r.catalog))
	mux.HandleFunc("GET /api/workflows/collection", r.fresh(r.collection))
	mux.HandleFunc("GET /api/workflows/item/icon", r.fresh(r.icon))
	mux.HandleFunc("GET /api/workflows/item/form", r.fresh(r.form))
	mux.HandleFunc("GET /api/platform/clusters", r.fresh(r.clusters))
	mux.HandleFunc("GET /api/platform/partitions", r.fresh(r.partitions))
	mux.HandleFunc("GET /api/platform/buckets", r.fresh(r.buckets))
	mux.HandleFunc("GET /api/workflows/github/browse", r.fresh(r.githubBrowse))
	mux.HandleFunc("GET /api/workflows/github/resolve", r.fresh(r.githubResolve))
	mux.HandleFunc("POST /api/workflows/item/run", r.fresh(r.run))
	mux.HandleFunc("POST /api/workflows/item/install", r.fresh(r.install))
	mux.HandleFunc("GET /api/workflows/runs", r.fresh(r.runsList))
	mux.HandleFunc("GET /api/workflows/item/readme", platform.Handler(r.p.Log(), r.readme))
	mux.HandleFunc("GET /api/workflows/item/asset", platform.Handler(r.p.Log(), r.asset))
	mux.HandleFunc("GET /api/workflows", platform.Handler(r.p.Log(), r.workflowList))
	mux.HandleFunc("GET /api/workflows/{name}", platform.Handler(r.p.Log(), r.workflowItem))
}

func (r *Routes) catalog(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	dep := r.p.GatewayKey()
	mine, err := r.keyOf(req)
	if err != nil {
		return nil, err
	}
	var rows []any
	source := "deployment"
	var depErr error
	if dep != "" {
		if rows, depErr = r.listFor(ctx, dep); depErr != nil {
			rows = nil
		}
	}
	if rows == nil && mine.own && mine.key != "" {
		if rows, err = r.listFor(ctx, mine.key); err != nil {
			return nil, err
		}
		source = "viewer"
	}
	if rows == nil {
		if depErr != nil {
			return nil, depErr
		}
		return nil, platform.KbError(409, noCredential)
	}
	curated := map[string]bool{}
	for _, n := range r.CuratedNames() {
		curated[n] = true
	}
	mkey := dep
	if source == "viewer" {
		mkey = mine.key
	}
	market, _ := r.marketplaceFor(ctx, mkey)
	mrows := []*jsonv.Object{}
	for _, m := range market {
		slug := jsStr(at(m, "slug"))
		mrows = append(mrows, jsonv.NewObject("name", "marketplace/"+slug, "displayName", or(at(m, "name"), at(m, "slug")),
			"description", coalesce(at(m, "description"), ""), "curated", curated["marketplace/"+slug]))
	}
	sortByDisplay(mrows)
	wrows := []*jsonv.Object{}
	names := map[any]bool{}
	for _, wf := range rows {
		names[at(wf, "name")] = true
		wrows = append(wrows, jsonv.NewObject("name", at(wf, "name"), "displayName", or(at(wf, "displayName"), at(wf, "name")),
			"description", coalesce(at(wf, "description"), ""), "tags", truthyOnly(at(wf, "tags")), "curated", curated[jsStr(at(wf, "name"))] && !undef(at(wf, "name"))))
	}
	sortByDisplay(wrows)
	missing := []any{}
	for _, n := range r.uniqueCurated() {
		if e := ParseEntry(n); e != nil && e.Kind == KindAccount && !names[n] {
			missing = append(missing, n)
		}
	}
	return jsonv.NewObject("source", source, "marketplace", objs(mrows), "workflows", objs(wrows), "missing", missing), nil
}

func (r *Routes) uniqueCurated() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range r.CuratedNames() {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func objs(rows []*jsonv.Object) []any {
	out := make([]any, len(rows))
	for i, o := range rows {
		out[i] = o
	}
	return out
}

// truthyOnly is (v ?? []).filter(Boolean).
func truthyOnly(v any) []any {
	out := []any{}
	for _, x := range jsonv.Arr(coalesce(v, []any{})) {
		if truthy(x) {
			out = append(out, x)
		}
	}
	return out
}

func iconOf(entry string) string { return "/api/workflows/item/icon?w=" + platform.EncodeURIComponent(entry) }

func (r *Routes) collection(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	names := r.CuratedNames()
	if len(names) == 0 {
		return jsonv.NewObject("configured", false, "workflows", []any{}), nil
	}
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	var entries []*Entry
	var accountNames []string
	hasMarket := false
	for _, n := range names {
		if e := ParseEntry(n); e != nil {
			entries = append(entries, e)
			switch e.Kind {
			case KindAccount:
				accountNames = append(accountNames, e.Entry)
			case KindMarketplace:
				hasMarket = true
			}
		}
	}
	var mine []any
	if len(accountNames) > 0 {
		if mine, err = r.listFor(ctx, v.key); err != nil {
			return nil, err
		}
	}
	byName := map[string]any{}
	for _, wf := range mine {
		if n, ok := at(wf, "name").(string); ok {
			if _, dup := byName[n]; !dup {
				byName[n] = wf
			}
		}
	}
	// new Map(rows.map(...)) keeps the last of a repeated name.
	for _, wf := range mine {
		if n, ok := at(wf, "name").(string); ok {
			byName[n] = wf
		}
	}
	var owner map[string]any
	missingSome := false
	for _, n := range accountNames {
		if _, ok := byName[n]; !ok {
			missingSome = true
		}
	}
	if v.own && missingSome && r.p.GatewayKey() != "" {
		owner = map[string]any{}
		rows, _ := r.listFor(ctx, r.p.GatewayKey())
		for _, wf := range rows {
			if n, ok := at(wf, "name").(string); ok {
				owner[n] = wf
			}
		}
	}
	var market []any
	marketOK := false
	if hasMarket {
		if m, err := r.marketplaceFor(ctx, v.key); err == nil {
			market, marketOK = m, true
		}
	}
	summaries := map[string]GitHubSummary{}
	var smu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range entries {
		if e.Kind != KindGitHub {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := GitHubSummaryOf(ctx, e)
			smu.Lock()
			summaries[e.Entry] = s
			smu.Unlock()
		}()
	}
	wg.Wait()
	out := []any{}
	for _, e := range entries {
		switch e.Kind {
		case KindAccount:
			wf, installed := byName[e.Name]
			if !installed {
				if o, ok := owner[e.Name]; ok {
					wf = o
				} else {
					wf = jsonv.Undefined
				}
			}
			var icon any
			if truthy(at(wf, "imageUrl")) {
				icon = iconOf(e.Entry)
			}
			confs := []any{}
			if installed {
				for _, c := range jsonv.Arr(coalesce(at(wf, "configurations"), []any{})) {
					if n := coalesce(at(c, "name"), at(c, "id"), ""); truthy(n) {
						confs = append(confs, n)
					}
				}
			}
			out = append(out, jsonv.NewObject("name", e.Entry, "kind", e.Kind, "displayName", or(at(wf, "displayName"), e.Name),
				"description", coalesce(at(wf, "description"), ""), "tags", truthyOnly(at(wf, "tags")), "icon", icon,
				"configurations", confs, "installed", installed, "available", !undef(wf)))
		case KindMarketplace:
			m := findBy(market, "slug", e.Slug)
			var icon any
			if truthy(at(m, "imageUrl")) {
				icon = iconOf(e.Entry)
			}
			out = append(out, jsonv.NewObject("name", e.Entry, "kind", e.Kind, "displayName", or(at(m, "name"), e.Slug),
				"description", coalesce(at(m, "description"), ""), "tags", []any{}, "icon", icon, "configurations", []any{},
				"installed", true, "available", !marketOK || !undef(m)))
		default:
			g := summaries[e.Entry]
			var icon any
			if g.Thumbnail != "" {
				icon = iconOf(e.Entry)
			}
			title := g.Title
			if title == "" {
				title = EntryTitle(e)
			}
			desc := g.Description
			if desc == "" {
				desc = e.Entry
			}
			out = append(out, jsonv.NewObject("name", e.Entry, "kind", e.Kind, "displayName", title, "description", desc, "tags", []any{},
				"icon", icon, "configurations", []any{}, "installed", true, "available", true))
		}
	}
	return jsonv.NewObject("configured", true, "workflows", out), nil
}

// iconCSP keeps an SVG opened on its own from running script on the Studio's origin.
const iconCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

var imageType = regexp.MustCompile(`^image/`)

func sendImage(w http.ResponseWriter, typ string, body []byte) {
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", iconCSP)
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (r *Routes) icon(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	e, err := entryOf(req)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	hit, ok := r.iconCache[e.Entry]
	r.mu.Unlock()
	if ok && time.Since(hit.at) < time.Hour {
		sendImage(w, hit.typ, hit.body)
		return nil, nil
	}
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	var imageURL, fallback string
	switch e.Kind {
	case KindAccount:
		rows, err := r.listFor(ctx, v.key)
		if err != nil {
			return nil, err
		}
		rows = append([]any{}, rows...)
		if gk := r.p.GatewayKey(); v.own && gk != "" {
			more, _ := r.listFor(ctx, gk)
			rows = append(rows, more...)
		}
		row := findBy(rows, "name", e.Name)
		imageURL, fallback = strOr(at(row, "imageUrl")), strOr(at(row, "imageFallbackUrl"))
	case KindMarketplace:
		rows, _ := r.marketplaceFor(ctx, v.key)
		row := findBy(rows, "slug", e.Slug)
		imageURL, fallback = strOr(at(row, "imageUrl")), strOr(at(row, "imageFallbackUrl"))
	default:
		imageURL = GitHubSummaryOf(ctx, e).Thumbnail
		if imageURL == "" {
			imageURL = GitHubThumbnail(e)
		}
	}
	host := r.p.Origin()
	if fallback == "" {
		fallback = ThumbnailRoute(imageURL)
	}
	cands := IconCandidates(imageURL, fallback, host)
	if len(cands) == 0 {
		platform.JSON(w, 404, jsonv.NewObject("error", "no icon"))
		return nil, nil
	}
	for _, abs := range cands {
		var h map[string]string
		if strings.HasPrefix(abs, host) {
			h = map[string]string{"Authorization": "Bearer " + v.key}
		}
		res, err := platform.Get(ctx, abs, h, 15*time.Second)
		if err != nil {
			continue
		}
		typ := res.Header.Get("Content-Type")
		if typ == "" {
			typ = "image/png"
		}
		if !res.OK || !imageType.MatchString(typ) {
			continue
		}
		r.mu.Lock()
		r.iconCache[e.Entry] = blob{time.Now(), typ, res.Raw}
		r.mu.Unlock()
		sendImage(w, typ, res.Raw)
		return nil, nil
	}
	platform.JSON(w, 404, jsonv.NewObject("error", "icon unavailable"))
	return nil, nil
}

// strOr is a string field, "" for anything else (the routes test it for truthiness).
func strOr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (r *Routes) form(w http.ResponseWriter, req *http.Request) (any, error) {
	e, err := entryOf(req)
	if err != nil {
		return nil, err
	}
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	d, err := r.definitionOf(req.Context(), e, v.key)
	if err != nil {
		return nil, err
	}
	inputs := at(d.YAML, "on", "execute", "inputs")
	var form any = jsonv.NewObject()
	if truthy(inputs) && wfparser.HasUserInputs(d.YAML) {
		form = wfparser.ConvertToDynamicForm(inputs)
	}
	var src any
	if d.Source != nil {
		src = d.Source
	}
	return jsonv.NewObject("name", e.Entry, "kind", e.Kind, "displayName", d.DisplayName, "description", d.Description, "form", form,
		"configurations", d.Configurations, "runAs", d.RunAs, "permissions", strs(d.Permissions), "source", src), nil
}

func (r *Routes) cliJSON(ctx context.Context, args []string, key string, timeout time.Duration) (any, error) {
	out, err := r.Call(ctx, args, key, timeout)
	if err != nil {
		return nil, err
	}
	return platform.FirstJSON(out)
}

// errNotArray is rows.map on what is not an array (a 500, as in Node).
var errNotArray = plainError("rows.map is not a function")

func (r *Routes) clusters(w http.ResponseWriter, req *http.Request) (any, error) {
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	raw, err := r.cliJSON(req.Context(), []string{"cluster", "ls", "-o", "json"}, v.key, 90*time.Second)
	if err != nil {
		return nil, err
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, errNotArray
	}
	out := []any{}
	for _, c := range rows {
		value := jsStr(at(c, "name"))
		if truthy(at(c, "user")) {
			value = "pw://" + jsStr(at(c, "user")) + "/" + jsStr(at(c, "name"))
		}
		out = append(out, jsonv.NewObject("value", value, "name", jsStr(at(c, "name")), "label", or(at(c, "displayName"), at(c, "name")),
			"status", jsStr(coalesce(at(c, "status"), "")), "scheduler", orNull(at(c, "schedulerType")), "user", orNull(at(c, "user")), "type", orNull(at(c, "type"))))
	}
	return jsonv.NewObject("clusters", out), nil
}

var pwPrefix = regexp.MustCompile(`^pw://[^/]+/`)

func (r *Routes) partitions(w http.ResponseWriter, req *http.Request) (any, error) {
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	cluster := pwPrefix.ReplaceAllString(platform.Q(req, "cluster"), "")
	raw, err := r.cliJSON(req.Context(), []string{"environments", "ls", "-o", "json"}, v.key, 90*time.Second)
	if err != nil {
		return nil, err
	}
	envs, ok := raw.([]any)
	if !ok {
		return nil, errNotArray
	}
	out := []any{}
	for _, e := range envs {
		if cluster != "" && jsStr(at(e, "clusterName")) != cluster {
			continue
		}
		out = append(out, jsonv.NewObject("value", jsStr(coalesce(at(e, "partitionName"), at(e, "name"))),
			"label", jsStr(coalesce(at(e, "displayName"), at(e, "partitionName"), at(e, "name"))),
			"state", jsStr(coalesce(at(e, "partitionState"), at(e, "status"), "")),
			"availNodes", orNull(at(e, "availNodes")), "totalNodes", orNull(at(e, "totalNodes"))))
	}
	return jsonv.NewObject("partitions", out), nil
}

func (r *Routes) buckets(w http.ResponseWriter, req *http.Request) (any, error) {
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	raw, err := r.cliJSON(req.Context(), []string{"buckets", "ls", "-o", "json"}, v.key, 60*time.Second)
	if err != nil {
		return nil, err
	}
	list := listOf(raw, "buckets")
	out := []any{}
	for _, b := range list {
		alt := jsStr(at(b, "name"))
		if truthy(at(b, "user")) {
			alt = "pw://" + jsStr(at(b, "user")) + "/" + jsStr(at(b, "name"))
		}
		out = append(out, jsonv.NewObject("value", coalesce(at(b, "uri"), alt), "label", or(at(b, "displayName"), at(b, "name"))))
	}
	return jsonv.NewObject("buckets", out), nil
}

func (r *Routes) githubBrowse(w http.ResponseWriter, req *http.Request) (any, error) {
	e := ParseEntry(platform.Q(req, "repo"))
	if e == nil || e.Kind != KindGitHub {
		return nil, platform.KbError(400, "repo must be github.com/<owner>/<repo>[/path][@ref]")
	}
	list, err := BrowseGitHub(req.Context(), e)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(list))
	for i, g := range list {
		out[i] = g.JSON()
	}
	return jsonv.NewObject("workflows", out), nil
}

func (r *Routes) githubResolve(w http.ResponseWriter, req *http.Request) (any, error) {
	e, err := entryOf(req)
	if err != nil {
		return nil, err
	}
	if e.Kind != KindGitHub {
		return jsonv.NewObject("entries", []any{e.Entry}), nil
	}
	list, err := ResolveGitHubEntry(req.Context(), e)
	if err != nil {
		return nil, err
	}
	return jsonv.NewObject("entries", strs(list)), nil
}

var (
	trustAsk  = regexp.MustCompile(`(?i)wants access to|re-run with --trust`)
	oldCLIGit = regexp.MustCompile(`(?i)unable to find workflow github\.com/`)
)

func (r *Routes) run(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	e, err := entryOf(req)
	if err != nil {
		return nil, err
	}
	body, err := platform.BodyObject(req)
	if err != nil {
		return nil, err
	}
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	if !r.curated(e.Entry) {
		return nil, platform.KbError(403, e.Entry+" is not in this Studio's workflow collection")
	}
	if err := r.requireReachable(ctx); err != nil {
		return nil, err
	}
	d, err := r.definitionOf(ctx, e, v.key)
	if err != nil {
		return nil, err
	}
	values := coalesce(at(body, "inputs"), jsonv.NewObject())
	user := ""
	if s, ok := v.username.(string); ok {
		user = s
	}
	values = ResolveStudioRefs(wfparser.PrepareSubmittableValues(d.YAML, values, user))
	dry := truthy(at(body, "dryRun"))
	args := []string{"workflows", "run", d.RunAs, "-i", string(jsonv.MustMarshal(values)), "-o", "json"}
	if dry {
		args = append(args, "--dry-run")
	}
	if len(d.Permissions) > 0 && at(body, "trust") == true {
		args = append(args, "--trust")
	}
	// Raw CLI errors here: the workspace runner recognizes a stopped
	// workspace by its text.
	runner := workspace.Runner{CLI: func(ctx context.Context, a []string, t time.Duration) (string, error) { return r.CLI(ctx, a, v.key, t) }, Log: func(s string) { r.p.Log().Info(s) }}
	out, _, err := workspace.With(ctx, runner, func(ctx context.Context) (string, error) { return r.CLI(ctx, args, v.key, 180*time.Second) })
	if err != nil {
		msg := cleanCLI(err.Error())
		if trustAsk.MatchString(msg) {
			perms := strings.Join(d.Permissions, ", ")
			if perms == "" {
				perms = "unspecified"
			}
			return jsonv.NewObject("ok", false, "dryRun", dry, "needsTrust", true, "permissions", strs(d.Permissions),
				"message", "This workflow's repository asks for access to your account variables ("+perms+"). Approve the access and run again; you can revoke it later with pw workflows permissions revoke."), nil
		}
		hint := ""
		if e.Kind == KindGitHub && oldCLIGit.MatchString(msg) {
			hint = " The platform CLI on this host is too old to run a GitHub workflow file; update pw (v7.105 runs them)."
		}
		return jsonv.NewObject("ok", false, "dryRun", dry, "message", sliceJS(msg+hint, 2000)), nil
	}
	if dry {
		m := sliceJS(jsTrim(out), 2000)
		if m == "" {
			m = "Validation passed."
		}
		return jsonv.NewObject("ok", true, "dryRun", true, "message", m), nil
	}
	var slug any
	if doc, err := platform.FirstJSON(out); err == nil {
		x := coalesce(at(doc, "run"), doc)
		if s := jsStr(coalesce(at(x, "slug"), at(x, "id"), "")); s != "" {
			slug = s
		}
	}
	if s, ok := slug.(string); ok {
		var owner *string
		if u, ok := v.username.(string); ok {
			owner = &u
		}
		r.Runs.RecordRun(runs.RecordInput{Slug: s, Workflow: e.Entry, Owner: owner})
		return jsonv.NewObject("ok", true, "dryRun", false, "slug", s, "message", "Run "+s+" submitted."), nil
	}
	return jsonv.NewObject("ok", true, "dryRun", false, "slug", nil, "message", sliceJS(jsTrim(out), 2000)), nil
}

func (r *Routes) install(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	e, err := entryOf(req)
	if err != nil {
		return nil, err
	}
	v, err := r.viewer(req)
	if err != nil {
		return nil, err
	}
	if !r.curated(e.Entry) {
		return nil, platform.KbError(403, e.Entry+" is not in this Studio's workflow collection")
	}
	if e.Kind != KindAccount {
		return nil, platform.KbError(409, "Marketplace and GitHub workflows run without a copy in your account.")
	}
	ownerKey := r.p.GatewayKey()
	if !v.own || ownerKey == "" {
		return nil, platform.KbError(409, "Nothing to add: this Studio runs workflows with its own credential.")
	}
	var doc any
	if rec := r.p.WorkflowRecord(ctx, e.Name, ownerKey); rec != nil {
		doc = rec
	} else if doc, err = r.cliJSON(ctx, []string{"workflows", "get", e.Name, "-o", "json"}, ownerKey, 60*time.Second); err != nil {
		return nil, err
	}
	text, ok := at(doc, "yaml").(string)
	if !ok {
		// Node writes this with the yaml package's stringify; js-yaml's dump
		// reads back as the same document.
		text = wfparser.DumpYAML(coalesce(at(doc, "yaml"), jsonv.NewObject()))
	}
	dir, err := os.MkdirTemp("", "wf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, e.Name+".yaml")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return nil, err
	}
	args := []string{"workflows", "create", "--yaml", tmp, e.Name}
	if dn := at(doc, "displayName"); truthy(dn) {
		args = []string{"workflows", "create", "--display-name", jsStr(dn), "--yaml", tmp, e.Name}
	}
	if _, err := r.Call(ctx, args, v.key, 60*time.Second); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.listCache = map[string]rowsHit{}
	r.mu.Unlock()
	return jsonv.NewObject("ok", true), nil
}

func (r *Routes) runsList(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	names := map[string]bool{}
	for _, n := range r.CuratedNames() {
		names[n] = true
	}
	kv, err := r.keyOf(req)
	if err != nil {
		return nil, err
	}
	me, _ := kv.username.(string)
	mine := func() []*runs.Record {
		var out []*runs.Record
		for _, rec := range r.Runs.List(100) {
			if names[rec.Workflow] && (me == "" || rec.Owner == nil || *rec.Owner == "" || *rec.Owner == me) {
				out = append(out, rec)
			}
		}
		if len(out) > 30 {
			out = out[:30]
		}
		return out
	}
	var open []*runs.Record
	r.mu.Lock()
	for _, rec := range mine() {
		if rec.EndedAt == nil && !runs.IsTerminal(rec.State) && time.Since(r.runChecked[rec.Slug]) > 20*time.Second {
			open = append(open, rec)
		}
	}
	r.mu.Unlock()
	if kv.key != "" && len(open) > 0 {
		if len(open) > 5 {
			open = open[:5]
		}
		var wg sync.WaitGroup
		for _, rec := range open {
			r.mu.Lock()
			r.runChecked[rec.Slug] = time.Now()
			r.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				doc, err := r.cliJSON(ctx, []string{"workflows", "runs", "view", rec.Slug, "-o", "json"}, kv.key, 30*time.Second)
				if err != nil {
					return // left to the watcher
				}
				if st := at(doc, "status"); truthy(st) && runs.IsTerminal(jsStr(st)) {
					r.Runs.MarkEnded(rec.Slug, strings.ToLower(jsStr(st)))
				}
			}()
		}
		wg.Wait()
	}
	out := []any{}
	for _, rec := range mine() {
		out = append(out, rec.Object())
	}
	return jsonv.NewObject("runs", out), nil
}
