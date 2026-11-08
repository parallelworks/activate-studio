package userlibs

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// The pw CLI, for the index workflow and the list of systems.

// pw runs the platform CLI with key, against the gateway's platform.
func (m *Module) pw(ctx context.Context, args []string, key string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if u, err := url.Parse(m.P.GatewayBase()); err == nil && u.Hostname() != "" {
		args = append(append([]string{}, args...), "--platform-host", u.Hostname())
	}
	out, err := m.P.Exec(ctx, args, platform.ExecOptions{Timeout: timeout, MaxBuffer: 16 * 1024 * 1024, Env: map[string]string{"PW_API_KEY": key}})
	if err != nil {
		msg := lastOf(platform.StripANSI(platform.ErrText(err)))
		return "", apierr.New(502, "%s", slice(msg, 300))
	}
	return out, nil
}

// firstJSON is the first JSON value in the CLI's output.
func firstJSON(text string) (any, error) {
	if !strings.ContainsAny(text, "[{") {
		return nil, apierr.New(502, "no JSON from the platform CLI")
	}
	return platform.JSONHead(text)
}

// workflowName is the workflow's name in the user's account, versioned by
// its content so a changed workflow registers afresh.
func (m *Module) workflowName() (string, error) {
	b, err := os.ReadFile(m.workflowFile())
	if err != nil {
		return "", err
	}
	return "studio-gufi-index-" + sha8(b), nil
}

func (m *Module) ensureWorkflow(ctx context.Context, key string) (string, error) {
	name, err := m.workflowName()
	if err != nil {
		return "", err
	}
	if _, err := m.pw(ctx, []string{"workflows", "get", name, "-o", "json"}, key, 60*time.Second); err == nil {
		return name, nil
	}
	if _, err := m.pw(ctx, []string{"workflows", "create", "--yaml", m.workflowFile(), "--display-name", "Studio library index", name}, key, 60*time.Second); err != nil {
		return "", err
	}
	return name, nil
}

func (m *Module) launch(ctx context.Context, d *libraries.UserDef, key, mode string) (string, error) {
	name, err := m.ensureWorkflow(ctx, key)
	if err != nil {
		return "", err
	}
	s := d.Scheduler
	cluster := jsonv.NewObject("scheduler", s != nil && s.Partition != "")
	if s != nil && s.Partition != "" {
		slurm := jsonv.NewObject("partition", s.Partition)
		if s.Account != "" {
			slurm.Set("account", s.Account)
		}
		if s.QOS != "" {
			slurm.Set("qos", s.QOS)
		}
		t := s.Time
		if t == "" {
			t = "01:00:00"
		}
		slurm.Set("time", t)
		cluster.Set("slurm", slurm)
	}
	inputs := jsonv.NewObject("resource", d.Resource, "cluster", cluster,
		"index", jsonv.NewObject("source_dir", d.SourceDir, "index_dir", deref(d.IndexRoot), "mode", mode, "threads", 4.0))
	in, _ := jsonv.Marshal(inputs)
	out, err := m.pw(ctx, []string{"workflows", "run", "-i", string(in), name, "-o", "json"}, key, 180*time.Second)
	if err != nil {
		return "", err
	}
	doc, err := firstJSON(out)
	if err != nil {
		return "", err
	}
	o := jsonv.Obj(doc)
	run := jsonv.Obj(get(o, "run"))
	slug := ""
	for _, v := range []any{get(run, "slug"), get(o, "slug"), get(run, "id"), get(o, "id")} {
		if v != nil {
			slug = jsonv.String(v)
			break
		}
	}
	if slug == "" {
		return "", apierr.New(502, "the platform returned no run for the index workflow")
	}
	return slug, nil
}

var manifestRe = regexp.MustCompile(`STUDIO_INDEX_BEGIN\s*\n([\s\S]*?)\n\s*STUDIO_INDEX_END`)

// manifestFrom is the index summary the workflow prints between its
// markers; nil when there is none, or it is not an object.
func manifestFrom(log string) *jsonv.Object {
	g := manifestRe.FindStringSubmatch(log)
	if g == nil {
		return nil
	}
	v, err := jsonv.DecodeString(g[1])
	if err != nil {
		return nil
	}
	return jsonv.Obj(v)
}

var (
	terminal = map[string]bool{"completed": true, "error": true, "failed": true, "canceled": true, "cancelled": true}
	absRe    = regexp.MustCompile(`^/(?:[A-Za-z0-9_.@+-]+/?)*$`)
	hexOnly  = regexp.MustCompile(`[^0-9a-f]`)
	tildeAt  = regexp.MustCompile(`^~(/|$)`)
	homeAt   = regexp.MustCompile(`^\$HOME(/|$)`)
)

// checkRun follows a library's index run one step: on completion it reads
// the summary and makes the library ready; on failure it says so.
func (m *Module) checkRun(ctx context.Context, owner string, d libraries.UserDef, key string) {
	if d.Run == nil || (d.State != "building" && d.State != "updating") {
		return
	}
	if d.Run.CheckedAt != "" {
		if t, ok := platform.ParseISO(d.Run.CheckedAt); ok && time.Since(t) < 15*time.Second {
			return
		}
	}
	run := *d.Run
	run.CheckedAt = isoNow()
	m.update(owner, d.ID, func(x *libraries.UserDef) { x.Run = &run })
	slug := d.Run.Slug
	view, err := m.pw(ctx, []string{"workflows", "runs", "view", slug, "-o", "json"}, key, 60*time.Second)
	if err != nil {
		return
	}
	doc, err := firstJSON(view)
	if err != nil {
		return
	}
	status := jsonv.String(get(jsonv.Obj(doc), "status"))
	lower := strings.ToLower(status)
	if !terminal[lower] {
		return
	}
	readyOr := func(x *libraries.UserDef) string {
		if deref(d.IndexRoot) != "" {
			return "ready"
		}
		return "failed"
	}
	if lower != "completed" {
		msg := fmt.Sprintf("the index run %s ended %s", slug, status)
		m.update(owner, d.ID, func(x *libraries.UserDef) { x.State, x.Error = readyOr(x), &msg })
		return
	}
	logs, _ := m.pw(ctx, []string{"workflows", "runs", "logs", slug}, key, 120*time.Second)
	mf := manifestFrom(logs)
	index, ok := isStr(get(mf, "index"))
	if mf == nil || !ok {
		msg := fmt.Sprintf("run %s finished without an index summary", slug)
		m.update(owner, d.ID, func(x *libraries.UserDef) { x.State, x.Error = readyOr(x), &msg })
		return
	}
	sourceRoot := d.SourceRoot
	if s, ok := isStr(get(mf, "source")); ok {
		sourceRoot = &s
	}
	commit := hexOnly.ReplaceAllString(jsonv.String(get(mf, "gufiCommit")), "")
	// The summary may use ~; the remote shell resolves it, and the GUFI
	// install it ran from.
	r, err := m.Pool.Run(ctx, d.Site, key, []string{"bash", "-c", workflowHome, "_", commit}, 0)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	home, bin := lines[0], ""
	if len(lines) > 1 {
		bin = lines[1]
	}
	expand := func(p string) string {
		if p == "" || home == "" {
			return p
		}
		p = tildeAt.ReplaceAllString(p, home+"$1")
		return homeAt.ReplaceAllString(p, home+"$1")
	}
	index = expand(index)
	if sourceRoot != nil {
		s := expand(*sourceRoot)
		sourceRoot = &s
	}
	if !absRe.MatchString(index) || !strings.HasPrefix(bin, "/") {
		msg := "the index summary named a location the Studio cannot use"
		m.update(owner, d.ID, func(x *libraries.UserDef) { x.State, x.Error = "failed", &msg })
		return
	}
	builtAt := isoNow()
	if v := get(mf, "builtAt"); v != nil {
		builtAt = jsonv.String(v)
	}
	orNil := func(v any) *int64 {
		f := orZero(v)
		if f == 0 {
			return nil
		}
		n := int64(f)
		return &n
	}
	m.update(owner, d.ID, func(x *libraries.UserDef) {
		x.State, x.Error, x.IndexRoot, x.GufiBin = "ready", nil, &index, &bin
		x.SourceRoot = nil
		if sourceRoot != nil && absRe.MatchString(*sourceRoot) {
			x.SourceRoot = sourceRoot
		}
		x.BuiltAt, x.Directories, x.Files = &builtAt, orNil(get(mf, "directories")), orNil(get(mf, "files"))
	})
}

// resourceOf is the cluster as a workflow input: pw://<owner>/<site>.
func (m *Module) resourceOf(ctx context.Context, site, key string) (string, error) {
	out, err := m.pw(ctx, []string{"clusters", "ls", "--status", "active", "-o", "json"}, key, 90*time.Second)
	if err != nil {
		return "", err
	}
	doc, err := firstJSON(out)
	if err != nil {
		return "", err
	}
	for _, row := range jsonv.Arr(doc) {
		r := jsonv.Obj(row)
		if get(r, "name") != site {
			continue
		}
		user := ""
		switch u := get(r, "user").(type) {
		case string:
			user = u
		case *jsonv.Object:
			user = jsonv.String(get(u, "username"))
		}
		if user != "" {
			return "pw://" + user + "/" + site, nil
		}
		return site, nil
	}
	return "", apierr.New(404, "%s is not one of your active clusters", site)
}

// listPwSystems is the viewer's systems on the platform, with their own
// key, running ones first. Names pw ssh cannot take are left out.
func (m *Module) listPwSystems(ctx context.Context, w who) ([]any, error) {
	_, key, err := m.pwKey(w)
	if err != nil {
		return nil, err
	}
	out, err := m.pw(ctx, []string{"clusters", "ls", "-o", "json"}, key, 60*time.Second)
	if err != nil {
		return nil, err
	}
	doc, err := firstJSON(out)
	if err != nil {
		return nil, err
	}
	rows := jsonv.Arr(doc)
	if rows == nil {
		rows = jsonv.Arr(get(jsonv.Obj(doc), "clusters"))
	}
	type system struct {
		name, label, status, typ string
		running                  bool
	}
	list := []system{}
	for _, row := range rows {
		r := jsonv.Obj(row)
		name := jsonv.String(get(r, "name"))
		if !clusterRe.MatchString(name) {
			continue
		}
		status := strings.ToLower(jsonv.String(get(r, "status")))
		if status == "" {
			status = "unknown"
		}
		lbl := name
		if v := get(r, "displayName"); jsonv.Truthy(v) {
			lbl = jsonv.String(v)
		}
		typ := ""
		if v := get(r, "type"); jsonv.Truthy(v) {
			typ = jsonv.String(v)
		}
		list = append(list, system{name, lbl, status, typ, status == "active"})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].running != list[j].running {
			return list[i].running
		}
		return kb.LocaleCompare(list[i].label, list[j].label) < 0
	})
	outList := make([]any, len(list))
	for i, s := range list {
		outList[i] = jsonv.NewObject("name", s.name, "label", s.label, "status", s.status, "running", s.running, "type", nullable(s.typ))
	}
	return outList, nil
}
