package chat

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
)

// The workflow and cluster tools of tools.ts, over the pw CLI as tools.ts
// runs it (pwToolCLI: the caller's own key when they added one) and the
// platform track's packages.

func init() {
	for name, fn := range map[string]func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error){
		"pw_help":               (*Module).toolPwHelp,
		"list_workflows":        (*Module).toolListWorkflows,
		"workflow_runs":         (*Module).toolWorkflowRuns,
		"list_clusters":         (*Module).toolListClusters,
		"cluster_command":       (*Module).toolClusterCommand,
		"hpc_environments":      (*Module).toolHpcEnvironments,
		"get_workflow":          (*Module).toolGetWorkflow,
		"workflow_configs":      (*Module).toolWorkflowConfigs,
		"list_github_workflows": (*Module).toolListGithubWorkflows,
		"workflow_run_detail":   (*Module).toolWorkflowRunDetail,
		"validate_workflow":     (*Module).toolValidateWorkflow,
		"compose_workflow":      (*Module).toolComposeWorkflow,
		"serve_model":           (*Module).toolServeModel,
		"watch_run":             (*Module).toolWatchRun,
		"hpc_status":            (*Module).toolHpcStatus,
		"run_workflow":          (*Module).toolRunWorkflow,
	} {
		goExecutors[name] = fn
	}
}

const cliDefault = 30 * time.Second

// cli is tools.ts's pwCli(args, timeout).
func (m *Module) cli(r *http.Request, ctx ToolCtx, args []string, timeout time.Duration) (string, error) {
	return m.pwToolCLI(r.Context(), args, timeout, deref(ctx.UserKey))
}

// platformKey is toolContext's userKey || gatewayKey().
func (m *Module) platformKey(ctx ToolCtx) string {
	if k := deref(ctx.UserKey); k != "" {
		return k
	}
	return m.GatewayKey()
}

func capText(s string) string { return jsval.Slice(s, toolOutputCap) }

var (
	notHelpChar = regexp.MustCompile(`(?i)[^a-z0-9-]`)
	notRunID    = regexp.MustCompile(`[^A-Za-z0-9_.:/-]`)
	notCluster  = regexp.MustCompile(`[^\w./:@-]`)
	notWordDot  = regexp.MustCompile(`[^\w.-]`)
	notWfName   = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	ownerPrefix = regexp.MustCompile(`^pw://[^/]+/`)
)

func (m *Module) toolPwHelp(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	parts := []string{}
	for _, p := range strings.FieldsFunc(tmplOr(arg(args, "command"), ""), isJSSpace) {
		if p = notHelpChar.ReplaceAllString(p, ""); p != "" && len(parts) < 4 {
			parts = append(parts, p)
		}
	}
	out, err := m.cli(r, ctx, append(append([]string{}, parts...), "--help"), cliDefault)
	if err != nil {
		return nil, err
	}
	return &Outcome{Result: capText(jsTrim(out)), Summary: "pw " + strings.Join(parts, " ") + " --help"}, nil
}

// tmplOr is String(v ?? d).
func tmplOr(v any, d string) string {
	if nullish(v) {
		return d
	}
	return tmpl(v)
}

func (m *Module) toolListWorkflows(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	out, err := m.cli(r, ctx, []string{"workflows", "ls", "-o", "json"}, cliDefault)
	if err != nil {
		return nil, err
	}
	v, err := parseJSON(out)
	if err != nil {
		return nil, err
	}
	wfs, ok := v.([]any)
	if !ok {
		return nil, &toolError{"wfs.map is not a function"}
	}
	offered := m.eff().WorkflowCollection
	catalog := []any{}
	for _, w := range wfs {
		o := jsonv.NewObject("name", prop(w, "name"), "displayName", prop(w, "displayName"),
			"description", either(prop(w, "description"), ""), "tags", filterTruthy(coalesce(prop(w, "tags"), []any{})), "type", prop(w, "type"))
		if name, ok := prop(w, "name").(string); ok && contains(offered, name) {
			o.Set("offeredHere", true)
		}
		catalog = append(catalog, o)
	}
	for _, name := range offered {
		e := workflows.ParseEntry(name)
		if e == nil || e.Kind == workflows.KindAccount {
			continue
		}
		var g *workflows.GitHubSummary
		if e.Kind == "github" {
			s := workflows.GitHubSummaryOf(r.Context(), e)
			g = &s
		}
		display, desc := workflows.EntryTitle(e), "Marketplace workflow"
		if e.Kind == "github" {
			desc = "Workflow in a GitHub repository"
		}
		if g != nil && g.Title != "" {
			display = g.Title
		}
		if g != nil && g.Description != "" {
			desc = g.Description
		}
		catalog = append(catalog, jsonv.NewObject("name", e.Entry, "displayName", display, "description", desc, "tags", []any{}, "type", e.Kind, "offeredHere", true))
	}
	sort.SliceStable(catalog, func(i, j int) bool {
		return truthy(prop(catalog[i], "offeredHere")) && !truthy(prop(catalog[j], "offeredHere"))
	})
	sum := strconv.Itoa(len(catalog)) + " workflows"
	if len(offered) > 0 {
		sum += ", " + strconv.Itoa(len(offered)) + " offered here"
	}
	return &Outcome{Result: stringify1(catalog), Summary: sum}, nil
}

// filterTruthy is (v).filter(Boolean) for an array.
func filterTruthy(v any) any {
	a, ok := v.([]any)
	if !ok {
		return v
	}
	out := []any{}
	for _, x := range a {
		if truthy(x) {
			out = append(out, x)
		}
	}
	return out
}

func (m *Module) toolWorkflowRuns(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	limit := math.Min(numOr(arg(args, "limit"), 15), 50)
	out, err := m.cli(r, ctx, []string{"workflows", "runs", "list", "-o", "json"}, cliDefault)
	if err != nil {
		if out, err = m.cli(r, ctx, []string{"workflows", "runs", "list"}, cliDefault); err != nil {
			return nil, err
		}
	}
	result := jsTrim(out)
	if v, err := parseJSON(out); err == nil {
		if runs, ok := v.([]any); ok {
			result = stringify1(runs[:sliceTo(len(runs), limit)])
		}
	}
	return &Outcome{Result: capText(result), Summary: "runs listed"}, nil
}

func (m *Module) toolListClusters(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	out, err := m.cli(r, ctx, []string{"cluster", "ls", "-o", "json"}, cliDefault)
	if err != nil {
		return nil, err
	}
	v, err := cliJSON(out)
	if err != nil {
		return &Outcome{Result: capText(out), Summary: "clusters"}, nil
	}
	rows, ok := v.([]any)
	if !ok {
		return nil, &toolError{"rows.map is not a function"}
	}
	slim := make([]any, len(rows))
	for i, c := range rows {
		o := jsonv.NewObject("name", prop(c, "name"))
		if d := prop(c, "displayName"); truthy(d) {
			o.Set("displayName", d)
		}
		o.Set("status", prop(c, "status"))
		if s := prop(c, "schedulerType"); truthy(s) {
			o.Set("scheduler", s)
		}
		o.Set("activeNodes", prop(c, "activeNodes"))
		o.Set("type", prop(c, "type"))
		if s := prop(c, "connectionString"); truthy(s) {
			o.Set("connection", s)
		}
		slim[i] = o
	}
	return &Outcome{Result: stringify1(slim), Summary: strconv.Itoa(len(slim)) + " clusters"}, nil
}

func (m *Module) toolClusterCommand(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	cluster := notCluster.ReplaceAllString(tmplOr(arg(args, "cluster"), ""), "")
	command := tmplOr(arg(args, "command"), "")
	if cluster == "" || command == "" {
		return &Outcome{Result: "cluster and command are required", Summary: "error"}, nil
	}
	if jsval.Len(command) > 8000 {
		return &Outcome{Result: "Command too long (over 8000 characters). For writing files into the knowledge base, use write_kb_file instead of shell redirection.", Summary: "command too long"}, nil
	}
	out, err := m.cli(r, ctx, []string{"ssh", cluster, command}, 90*time.Second)
	if err != nil {
		return nil, err
	}
	return &Outcome{Result: capText(orStr(jsTrim(out), "(no output)")), Summary: cluster + ": " + jsval.Slice(command, 40)}, nil
}

// envRows is jsonFromCli<any[]>(pw environments ls -o json).
func (m *Module) envRows(r *http.Request, ctx ToolCtx, timeout time.Duration) ([]any, error) {
	out, err := m.cli(r, ctx, []string{"environments", "ls", "-o", "json"}, timeout)
	if err != nil {
		return nil, err
	}
	v, err := cliJSON(out)
	if err != nil {
		return nil, err
	}
	envs, ok := v.([]any)
	if !ok {
		return nil, &toolError{"envs.filter is not a function"}
	}
	return envs, nil
}

func (m *Module) toolHpcEnvironments(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	want := lowerJS(notWordDot.ReplaceAllString(tmplOr(arg(args, "cluster"), ""), ""))
	envs, err := m.envRows(r, ctx, 90*time.Second)
	if err != nil {
		return nil, err
	}
	rows := envs
	if want != "" {
		rows = nil
		for _, e := range envs {
			if lowerJS(tmplOr(prop(e, "clusterName"), "")) == want {
				rows = append(rows, e)
			}
		}
	}
	if len(rows) == 0 {
		// [...new Set(names)].sort(): undefined last, the rest by their text.
		var names []any
		seen := map[any]bool{}
		for _, e := range envs {
			n := prop(e, "clusterName")
			key := any(n)
			if _, isObj := n.(*jsonv.Object); isObj {
				key = n
			} else if _, isArr := n.([]any); isArr {
				key = &n
			}
			if !seen[key] {
				seen[key] = true
				names = append(names, n)
			}
		}
		sort.SliceStable(names, func(i, j int) bool {
			if names[i] == undef || names[j] == undef {
				return names[j] == undef && names[i] != undef
			}
			return utf16Less(tmpl(names[i]), tmpl(names[j]))
		})
		return &Outcome{Result: `No partitions found for "` + tmpl(arg(args, "cluster")) + `". Systems with partitions: ` + joinJS(names, ", "), Summary: "no match"}, nil
	}
	if want == "" {
		type group struct {
			name string
			list []any
		}
		var groups []*group
		by := map[string]*group{}
		for _, e := range rows {
			k := tmpl(prop(e, "clusterName"))
			if by[k] == nil {
				by[k] = &group{name: k}
				groups = append(groups, by[k])
			}
			by[k].list = append(by[k].list, e)
		}
		sort.SliceStable(groups, func(i, j int) bool { return utf16Less(groups[i].name+",", groups[j].name+",") })
		lines := make([]string, len(groups))
		for i, g := range groups {
			up, free := 0, 0.0
			for _, e := range g.list {
				if prop(e, "partitionState") == "up" {
					up++
					free += numOr(prop(e, "availNodes"), 0)
				}
			}
			first := g.list[0]
			lines[i] = g.name + " (" + tmpl(either(prop(first, "clusterDisplayName"), g.name)) + "), " + tmpl(prop(first, "schedulerType")) + ": " +
				strconv.Itoa(len(g.list)) + " partitions, " + strconv.Itoa(up) + " up, " + jsonv.FormatNumber(free) + " nodes free"
		}
		return &Outcome{Result: "Systems with schedulable partitions:\n" + strings.Join(lines, "\n") + "\n\nCall again with a cluster name for its partitions and submission fields.", Summary: strconv.Itoa(len(groups)) + " systems"}, nil
	}
	first := rows[0]
	head := tmpl(either(prop(first, "clusterDisplayName"), want)) + " (" + want + "), scheduler " + tmpl(prop(first, "schedulerType")) + ", attached as user " + tmpl(prop(first, "clusterUser"))
	detail := make([]string, len(rows))
	for i, e := range rows {
		var req []any
		for _, f := range jsonv.Arr(coalesce(prop(e, "fields"), []any{})) {
			if truthy(prop(f, "required")) {
				req = append(req, prop(f, "key"))
			}
		}
		detail[i] = "  " + tmpl(prop(e, "partitionName")) + ": " + tmpl(prop(e, "partitionState")) + ", " + tmpl(prop(e, "availNodes")) + "/" + tmpl(prop(e, "totalNodes")) +
			" nodes free, max walltime " + tmpl(prop(e, "maxTime")) +
			"\n    defaults " + stringify(coalesce(prop(e, "defaults"), jsonv.NewObject())) +
			"\n    required " + orStr(joinJS(req, ", "), "(none)")
	}
	return &Outcome{Result: head + "\n" + strings.Join(detail, "\n") + "\n\n" +
		"account and qos are per-user site values; take them from a saved configuration (workflow_configs) when one exists for this system rather than inventing them.",
		Summary: strconv.Itoa(len(rows)) + " partitions on " + want}, nil
}

// utf16Less orders strings as JavaScript's sort does.
func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// wfDoc is tools.ts's wfDoc: a workflow record with its saved input
// configurations; a marketplace or GitHub one read where it lives.
func (m *Module) wfDoc(r *http.Request, ctx ToolCtx, name string) (any, error) {
	if e := workflows.ParseEntry(name); e != nil && e.Kind != workflows.KindAccount {
		routes := workflows.Current()
		if routes == nil {
			return nil, errNodeTool
		}
		d, err := routes.WorkflowDefinition(r.Context(), e.Entry, deref(ctx.UserKey))
		if err != nil {
			return nil, err
		}
		perms := make([]any, len(d.Permissions))
		for i, p := range d.Permissions {
			perms[i] = p
		}
		runAs := any(undef)
		if d.RunAs != "" {
			runAs = d.RunAs
		}
		return jsonv.NewObject("name", e.Entry, "runAs", runAs, "displayName", d.DisplayName, "description", d.Description,
			"yaml", d.YAML, "configurations", []any{}, "permissions", perms), nil
	}
	clean := notWfName.ReplaceAllString(name, "")
	if rec := m.pf.WorkflowRecord(r.Context(), clean, m.platformKey(ctx)); rec != nil {
		return rec, nil
	}
	out, err := m.cli(r, ctx, []string{"workflows", "get", clean, "-o", "json"}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	return cliJSON(out)
}

type declared struct {
	path, typ string
	def       any
}

// declaredInputs is tools.ts's declaredInputs.
func declaredInputs(wf any) []declared {
	on := coalesce(prop(wf, "yaml", "on"), prop(wf, "yaml", "true"), jsonv.NewObject())
	var rows []declared
	var walk func(prefix string, obj any)
	walk = func(prefix string, obj any) {
		keys, vals := entries(obj)
		for i, k := range keys {
			v := vals[i]
			_, isObj := v.(*jsonv.Object)
			_, isArr := v.([]any)
			if !truthy(v) || (!isObj && !isArr) || strings.HasPrefix(k, "$") {
				continue
			}
			if prop(v, "type") == "group" {
				walk(prefix+k+".", coalesce(prop(v, "items"), jsonv.NewObject()))
			} else {
				rows = append(rows, declared{path: prefix + k, typ: tmplOr(prop(v, "type"), ""), def: prop(v, "default")})
			}
		}
	}
	walk("", coalesce(prop(on, "execute", "inputs"), jsonv.NewObject()))
	return rows
}

func slotsOf(rows []declared, typ string) []string {
	var out []string
	for _, d := range rows {
		if d.typ == typ {
			out = append(out, d.path)
		}
	}
	return out
}

func (m *Module) toolGetWorkflow(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	wf, err := m.wfDoc(r, ctx, tmplOr(arg(args, "name"), ""))
	if err != nil {
		return nil, err
	}
	jobs := coalesce(prop(wf, "yaml", "jobs"), jsonv.NewObject())
	keys, defs := entries(jobs)
	edges := []any{}
	for i, job := range keys {
		for _, dep := range iterate(coalesce(prop(defs[i], "needs"), []any{})) {
			edges = append(edges, tmpl(dep)+" -> "+job)
		}
	}
	jobNames := make([]any, len(keys))
	for i, k := range keys {
		jobNames[i] = k
	}
	dag := jsonv.NewObject("name", prop(wf, "name"), "displayName", prop(wf, "displayName"), "jobs", jobNames, "edges", edges)
	full := jsonv.NewObject("dag", dag)
	if perms := prop(wf, "permissions"); truthy(prop(perms, "length")) || len(jsonv.Arr(perms)) > 0 {
		full.Set("permissions", perms)
		full.Set("note", "Running this workflow grants its repository these account variables; ask the user before passing trust to run_workflow.")
	}
	full.Set("yaml", prop(wf, "yaml"))
	result := stringify1(full)
	if jsval.Len(result) > toolOutputCap {
		result = stringify1(jsonv.NewObject("dag", dag))
	}
	return &Outcome{Result: result, Summary: strconv.Itoa(len(keys)) + " jobs, " + strconv.Itoa(len(edges)) + " edges"}, nil
}

// iterate is for...of over an array (or a string's characters).
func iterate(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		var out []any
		for _, c := range x {
			out = append(out, string(c))
		}
		return out
	}
	return nil
}

var newlines = regexp.MustCompile(`\n+`)

func (m *Module) toolWorkflowConfigs(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	wf, err := m.wfDoc(r, ctx, tmplOr(arg(args, "name"), ""))
	if err != nil {
		return nil, err
	}
	configs := jsonv.Arr(coalesce(prop(wf, "configurations"), []any{}))
	if len(configs) == 0 {
		sites := slotsOf(declaredInputs(wf), "compute-clusters")
		res := "Workflow " + tmpl(coalesce(prop(wf, "name"), arg(args, "name"))) + " has no saved configurations. Build inputs from its declared schema (get_workflow) and the site's own values (hpc_environments)."
		if len(sites) > 0 {
			res += "\nIts target-system input is: " + strings.Join(sites, ", ") + "."
		}
		return &Outcome{Result: res, Summary: "no configurations"}, nil
	}
	nameOf := func(c any) any { return coalesce(prop(c, "name"), prop(c, "id")) }
	if truthy(arg(args, "config")) {
		wanted := lowerJS(tmpl(arg(args, "config")))
		for _, c := range configs {
			if lowerJS(tmplOr(coalesce(nameOf(c), ""), "")) == wanted {
				builtin := ""
				if truthy(prop(c, "builtin")) {
					builtin = " (builtin)"
				}
				return &Outcome{Result: `Configuration "` + tmpl(nameOf(c)) + `"` + builtin + " of " + tmpl(prop(wf, "name")) + " sets:\n" + stringify1(coalesce(prop(c, "inputs"), jsonv.NewObject())),
					Summary: "config " + tmpl(nameOf(c))}, nil
			}
		}
		names := make([]any, len(configs))
		for i, c := range configs {
			names[i] = nameOf(c)
		}
		return &Outcome{Result: "No configuration named " + tmpl(arg(args, "config")) + ". Available: " + joinJS(names, ", "), Summary: "no such config"}, nil
	}
	rows := make([]string, len(configs))
	for i, c := range configs {
		ins := coalesce(prop(c, "inputs"), jsonv.NewObject())
		sl := coalesce(prop(ins, "cluster", "slurm"), jsonv.NewObject())
		pb := coalesce(prop(ins, "cluster", "pbs"), jsonv.NewObject())
		var bits []string
		add := func(label string, v any) {
			if truthy(v) {
				bits = append(bits, label+"="+tmpl(v))
			}
		}
		add("resource", prop(ins, "resource"))
		add("partition", prop(sl, "partition"))
		add("qos", prop(sl, "qos"))
		add("account", prop(sl, "account"))
		add("walltime", prop(sl, "time"))
		if d := prop(sl, "scheduler_directives"); truthy(d) {
			bits = append(bits, `sbatch="`+newlines.ReplaceAllString(jsTrim(tmpl(d)), "; ")+`"`)
		}
		if d := prop(pb, "scheduler_directives"); truthy(d) {
			var keep []string
			for _, l := range strings.Split(jsTrim(tmpl(d)), "\n") {
				if l != "" && !strings.HasPrefix(l, "##") {
					keep = append(keep, l)
				}
			}
			bits = append(bits, `pbs="`+strings.Join(keep, "; ")+`"`)
		}
		add("models", prop(ins, "service", "models"))
		kind := " (saved by this account)"
		if truthy(prop(c, "builtin")) {
			kind = " (builtin)"
		}
		rows[i] = tmpl(nameOf(c)) + kind + ": " + orStr(strings.Join(bits, ", "), "(no scheduler settings)")
	}
	return &Outcome{Result: "Saved configurations for " + tmpl(prop(wf, "name")) + ":\n" + strings.Join(rows, "\n") + "\n\n" +
		"Pass one as run_workflow config to launch with these settings; add resource only if the configuration has none.",
		Summary: strconv.Itoa(len(configs)) + " configurations"}, nil
}

func (m *Module) toolListGithubWorkflows(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	e := workflows.ParseEntry(jsTrim(tmplOr(arg(args, "repo"), "")))
	if e == nil || e.Kind != "github" {
		return &Outcome{Result: "repo must be github.com/<owner>/<repo>[/path][@ref].", Summary: "error"}, nil
	}
	list, err := workflows.BrowseGitHub(r.Context(), e)
	if err != nil {
		return nil, err
	}
	rows := make([]any, len(list))
	for i, w := range list {
		rows[i] = jsonv.NewObject("name", w.Entry, "title", w.Title, "summary", w.Description)
	}
	return &Outcome{Result: capText(stringify1(rows)), Summary: strconv.Itoa(len(list)) + " workflows in " + e.Owner + "/" + e.Repo}, nil
}

func (m *Module) toolWorkflowRunDetail(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	id := notRunID.ReplaceAllString(tmplOr(arg(args, "run"), ""), "")
	view, err := m.cli(r, ctx, []string{"workflows", "runs", "view", id, "-o", "json"}, cliDefault)
	if err != nil {
		if view, err = m.cli(r, ctx, []string{"workflows", "runs", "view", id}, cliDefault); err != nil {
			return nil, err
		}
	}
	errs, _ := m.cli(r, ctx, []string{"workflows", "runs", "errors", id}, cliDefault)
	logs, _ := m.cli(r, ctx, []string{"workflows", "runs", "logs", id}, cliDefault)
	text, status := summarizeRunDetail(view, errs, logs, 12_000)
	return &Outcome{Result: text, Summary: id + ": " + status}, nil
}

var (
	stepHead    = regexp.MustCompile(`(?m)^=== job: (.+?) > step: (.+?) \((\w*),[^)]*\) ===\s*$`)
	fetchFailed = regexp.MustCompile(`^\(error fetching logs`)
	failedState = regexp.MustCompile(`(?i)^(error|failed)$`)
)

// summarizeRunDetail is tools.ts's summarizeRunDetail.
func summarizeRunDetail(viewText, errorsText, logsText string, cap int) (string, string) {
	var lines []string
	status := "unknown"
	ok := func() bool {
		v, err := cliJSON(viewText)
		if err != nil || v == nil {
			return false
		}
		status = tmplOr(prop(v, "status"), "unknown")
		head := "Run " + tmplOr(coalesce(prop(v, "slug"), prop(v, "id")), "") + " of " + tmplOr(coalesce(prop(v, "workflowName"), prop(v, "workflow")), "") + ": " + status
		if c := prop(v, "createdAt"); truthy(c) {
			head += ", started " + tmpl(c)
		}
		if c := prop(v, "completedAt"); truthy(c) {
			head += ", ended " + tmpl(c)
		}
		lines = append(lines, head)
		keys, jobs := entries(coalesce(prop(v, "executedJobs"), jsonv.NewObject()))
		for i, name := range keys {
			j := jobs[i]
			host := ""
			if h := prop(j, "ssh", "remoteHost"); truthy(h) {
				host = " on " + tmpl(h)
			}
			var steps []string
			stepList := coalesce(prop(j, "steps"), []any{})
			arr, isArr := stepList.([]any)
			if !isArr {
				return false
			}
			for _, st := range arr {
				if nullish(st) {
					return false
				}
				steps = append(steps, tmpl(prop(st, "name"))+": "+tmpl(prop(st, "status")))
			}
			s := ""
			if len(steps) > 0 {
				s = " [" + strings.Join(steps, "; ") + "]"
			}
			lines = append(lines, "  "+name+": "+tmplOr(prop(j, "status"), "?")+host+s)
		}
		return true
	}()
	if !ok {
		lines = append(lines, "Run detail:\n"+jsval.Slice(jsTrim(viewText), 1500))
	}
	if jsTrim(errorsText) != "" {
		lines = append(lines, "", "Errors:", jsval.Slice(jsTrim(errorsText), 3000))
	}
	type section struct {
		head, body string
		failed     bool
	}
	var sections []section
	heads := stepHead.FindAllStringSubmatchIndex(logsText, -1)
	for i, h := range heads {
		start, end := h[1], len(logsText)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		var keep []string
		for _, l := range strings.Split(logsText[start:end], "\n") {
			if !fetchFailed.MatchString(l) {
				keep = append(keep, l)
			}
		}
		body := jsTrim(strings.Join(keep, "\n"))
		state := logsText[h[6]:h[7]]
		if body == "" || state == "skipped" {
			continue
		}
		label := state
		if label == "" {
			label = "ran"
		}
		sections = append(sections, section{head: logsText[h[2]:h[3]] + " > " + logsText[h[4]:h[5]] + " (" + label + ")", failed: failedState.MatchString(state), body: body})
	}
	tail := func(t string, n int) string {
		ls := strings.Split(t, "\n")
		if len(ls) > n {
			ls = ls[len(ls)-n:]
		}
		return strings.Join(ls, "\n")
	}
	var ordered []section
	for _, s := range sections {
		if s.failed {
			ordered = append(ordered, s)
		}
	}
	for _, s := range sections {
		if !s.failed {
			ordered = append(ordered, s)
		}
	}
	if len(ordered) > 0 {
		lines = append(lines, "", "Step output (tails; failed steps first):")
		for _, s := range ordered {
			n := 30
			if s.failed {
				n = 60
			}
			lines = append(lines, "--- "+s.head+" ---", tail(s.body, n))
		}
	} else if jsTrim(logsText) != "" {
		lines = append(lines, "", "Logs (tail):", tail(jsTrim(logsText), 60))
	}
	text := strings.Join(lines, "\n")
	if jsval.Len(text) > cap {
		text = jsval.Slice(text, cap) + "\n… (truncated)"
	}
	return text, status
}

func (m *Module) schema() *workflows.Schema {
	if routes := workflows.Current(); routes != nil {
		return routes.Schema
	}
	return nil
}

func (m *Module) toolValidateWorkflow(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	sch := m.schema()
	if sch == nil {
		return nil, errNodeTool
	}
	text := tmplOr(arg(args, "yaml"), "")
	if text == "" && truthy(arg(args, "path")) {
		fc, err := kb.ReadFileContent(tmpl(arg(args, "path")), m.A.Cfg.KBRoot, kb.RootsFrom(m.A.Cfg).ExtractCache())
		if err != nil {
			return nil, err
		}
		if c, ok := fc.Get("content"); ok {
			text, _ = c.(string)
		}
	}
	if jsTrim(text) == "" {
		return &Outcome{Result: "Nothing to validate: pass yaml text or a knowledge base path.", Summary: "error"}, nil
	}
	doc, err := jsonv.ParseYAML(text)
	if err != nil {
		return &Outcome{Result: "Not parseable as YAML: " + jsval.Slice(err.Error(), 300), Summary: "parse error"}, nil
	}
	v := sch.Validate(r.Context(), doc)
	switch {
	case v.OK == nil:
		return &Outcome{Result: firstOr(v.Errors), Summary: "schema unavailable"}, nil
	case *v.OK:
		return &Outcome{Result: "Valid against " + v.Source + ".", Summary: "valid"}, nil
	}
	return &Outcome{Result: "Invalid against " + v.Source + ":\n" + bulleted(v.Errors), Summary: strconv.Itoa(len(v.Errors)) + " schema errors"}, nil
}

func firstOr(xs []string) string {
	if len(xs) == 0 {
		return "undefined"
	}
	return xs[0]
}

func bulleted(xs []string) string {
	out := make([]string, len(xs))
	for i, e := range xs {
		out[i] = "- " + e
	}
	return strings.Join(out, "\n")
}

func (m *Module) toolComposeWorkflow(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	sch := m.schema()
	if sch == nil {
		return nil, errNodeTool
	}
	raw := coalesce(arg(args, "workflows"), []any{})
	list, isList := raw.([]any)
	if !isList {
		// Node hands the value on to composeWorkflows, whose steps.map throws.
		return nil, &toolError{"steps.map is not a function"}
	}
	steps := make([]workflows.ComposeStep, len(list))
	for i, s := range list {
		st := workflows.ComposeStep{Uses: tmplOr(prop(s, "uses"), ""), ID: tmplOr(prop(s, "id"), ""), Name: tmplOr(prop(s, "name"), "")}
		if n := prop(s, "needs"); !nullish(n) {
			st.Needs = []string{}
			for _, x := range jsonv.Arr(n) {
				st.Needs = append(st.Needs, tmpl(x))
			}
		}
		steps[i] = st
	}
	composed, err := workflows.Compose(r.Context(), m.pf, steps, !truthy(arg(args, "parallel")))
	if err != nil {
		return nil, err
	}
	v := sch.Validate(r.Context(), composed.YAML)
	var notes []string
	if len(composed.Missing) > 0 {
		notes = append(notes, "could not read: "+strings.Join(composed.Missing, ", ")+" (inputs for those steps were not merged)")
	}
	if len(composed.Conflicts) > 0 {
		notes = append(notes, "input conflicts: "+strings.Join(composed.Conflicts, "; "))
	}
	if v.OK != nil && *v.OK {
		notes = append(notes, "schema check: valid against "+v.Source)
	}
	failure := ""
	switch {
	case v.OK == nil:
		failure = "\n\n(" + firstOr(v.Errors) + ")"
	case !*v.OK:
		failure = "\n\nSCHEMA CHECK FAILED against " + v.Source + "; fix these before writing or running this workflow:\n" + bulleted(v.Errors)
	}
	result := composed.YAMLText
	if len(notes) > 0 {
		result += "\n# " + strings.Join(notes, "\n# ")
	}
	sum := "composed " + strconv.Itoa(len(list)) + " workflows, " + strconv.Itoa(len(composed.Inputs)) + " inputs"
	if v.OK != nil && !*v.OK {
		sum += ", schema errors"
	}
	return &Outcome{Result: result + failure, Summary: sum}, nil
}

// mergeInputs is tools.ts's mergeInputs (and serve_model's merge): the
// preset under the caller's values, groups merged by key.
func mergeInputs(base, over any) any {
	o, isObj := over.(*jsonv.Object)
	if !truthy(over) || !isObj {
		if nullish(over) {
			return base
		}
		return over
	}
	out := jsonv.NewObject()
	if b, ok := base.(*jsonv.Object); ok {
		for _, k := range b.Keys() {
			v, _ := b.Get(k)
			out.Set(k, v)
		}
	}
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		cur, ok := out.Get(k)
		if !ok {
			cur = undef
		}
		out.Set(k, mergeInputs(cur, v))
	}
	return out
}

// parseArgJSON is JSON.parse(String(v)).
func parseArgJSON(v any) (any, error) { return parseJSON(tmpl(v)) }

func (m *Module) toolServeModel(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	engines := jsonv.NewObject()
	if v, err := jsonv.DecodeString(m.eff().ServeWorkflows); err == nil {
		if o, ok := v.(*jsonv.Object); ok {
			engines = o
		}
	}
	engine := lowerJS(tmplOr(arg(args, "engine"), ""))
	cfg, ok := engines.Get(engine)
	if engine == "" || !ok || !truthy(cfg) {
		var listing []string
		for _, k := range engines.Keys() {
			v, _ := engines.Get(k)
			listing = append(listing, k+": workflow "+tmpl(prop(v, "workflow")))
		}
		return &Outcome{Result: "Configured serving engines:\n" + orStr(strings.Join(listing, "\n"), "(none configured on this deployment)") + "\nCall again with engine and launch=false to see that workflow's inputs.", Summary: "engines listed"}, nil
	}
	merged := any(jsonv.NewObject())
	if ins, ok := coalesce(prop(cfg, "inputs"), jsonv.NewObject()).(*jsonv.Object); ok {
		merged = ins.Clone()
	}
	if truthy(arg(args, "inputs")) {
		over, err := parseArgJSON(arg(args, "inputs"))
		if err != nil {
			return nil, err
		}
		merged = mergeInputs(merged, over)
	}
	if engine == "vllm" && truthy(arg(args, "model")) {
		merged = mergeInputs(merged, jsonv.NewObject("model", jsonv.NewObject("hf_model_id", tmpl(arg(args, "model")))))
	}
	wf := tmpl(prop(cfg, "workflow"))
	if arg(args, "launch") != true {
		declaredText := ""
		doc, err := func() (any, error) {
			if rec := m.pf.WorkflowRecord(r.Context(), wf, m.platformKey(ctx)); rec != nil {
				return rec, nil
			}
			out, err := m.cli(r, ctx, []string{"workflows", "get", wf, "-o", "json"}, 60*time.Second)
			if err != nil {
				return nil, err
			}
			return parseJSON(strings.SplitN(out, "\n\n", 2)[0])
		}()
		if err != nil {
			declaredText = "(could not read the workflow: " + jsval.Slice(err.Error(), 120) + ")"
		} else {
			ins := coalesce(prop(doc, "yaml", "on", "execute", "inputs"), prop(doc, "yaml", "true", "execute", "inputs"), jsonv.NewObject())
			var rows []string
			var walk func(prefix string, obj any)
			walk = func(prefix string, obj any) {
				keys, vals := entries(obj)
				for i, k := range keys {
					if strings.HasPrefix(k, "$") {
						continue
					}
					v := vals[i]
					_, isObj := v.(*jsonv.Object)
					_, isArr := v.([]any)
					if !isObj && !isArr {
						continue
					}
					if prop(v, "type") == "group" {
						walk(prefix+k+".", coalesce(prop(v, "items"), jsonv.NewObject()))
					} else {
						rows = append(rows, prefix+k+" (default: "+stringify(coalesce(prop(v, "default"), nil))+")")
					}
				}
			}
			walk("", ins)
			declaredText = strings.Join(rows, "\n")
		}
		return &Outcome{Result: "Engine " + engine + " runs workflow " + wf + ".\nDeclared inputs:\n" + declaredText + "\n\nInputs that would be submitted now:\n" + stringify1(merged) +
			"\n\nCall again with launch=true to submit. Only do so if the user asked to launch a model.", Summary: engine + " serve described"}, nil
	}
	out, err := m.cli(r, ctx, []string{"workflows", "run", "-i", stringify(merged), wf, "-o", "json"}, 180*time.Second)
	if err != nil {
		return nil, err
	}
	return &Outcome{Result: "Run submitted for " + wf + ".\n" + jsval.Slice(jsTrim(out), 2000) + "\nWatch it with workflow_runs / workflow_run_detail. A vLLM endpoint appears in the model catalog once the model finishes loading.",
		Summary: engine + " serve launched"}, nil
}

var (
	terminalRun = map[string]bool{"completed": true, "error": true, "failed": true, "canceled": true, "cancelled": true}
	liveJob     = regexp.MustCompile(`(?i)^(running|in_progress|active|started)$`)
)

// runsOf is Array.isArray(listed) ? listed : listed?.runs ?? [].
func runsOf(listed any) []any {
	if a, ok := listed.([]any); ok {
		return a
	}
	return jsonv.Arr(coalesce(prop(listed, "runs"), []any{}))
}

func (m *Module) toolWatchRun(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	wait := math.Max(0, math.Min(numOr(coalesce(arg(args, "wait"), 45.0), 45), 120))
	id := notRunID.ReplaceAllString(tmplOr(arg(args, "run"), ""), "")
	list := func() ([]any, error) {
		out, err := m.cli(r, ctx, []string{"workflows", "runs", "list", "-o", "json"}, cliDefault)
		if err != nil {
			return nil, err
		}
		v, err := cliJSON(out)
		if err != nil {
			return nil, err
		}
		return runsOf(v), nil
	}
	if id == "" {
		runs, err := list()
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			return &Outcome{Result: "No workflow runs on this account yet.", Summary: "none"}, nil
		}
		id = tmpl(coalesce(prop(runs[0], "slug"), prop(runs[0], "id")))
	}
	view := func(key string) (any, error) {
		out, err := m.cli(r, ctx, []string{"workflows", "runs", "view", key, "-o", "json"}, 60*time.Second)
		if err != nil {
			return nil, err
		}
		return cliJSON(out)
	}
	doc, err := view(id)
	if err != nil {
		runs, err := list()
		if err != nil {
			return nil, err
		}
		var hit any
		for _, x := range runs {
			if prop(x, "id") == id || prop(x, "slug") == id {
				hit = x
				break
			}
		}
		if hit == nil {
			return &Outcome{Result: "No run named " + id + ". List them with workflow_runs.", Summary: "not found"}, nil
		}
		id = tmpl(coalesce(prop(hit, "slug"), prop(hit, "id")))
		if doc, err = view(id); err != nil {
			return nil, err
		}
	}
	stamp := func(d any) string {
		keys, vals := entries(coalesce(prop(d, "executedJobs"), jsonv.NewObject()))
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ":" + tmpl(prop(vals[i], "status"))
		}
		return tmpl(prop(d, "status")) + "|" + strings.Join(parts, ",")
	}
	started := time.Now()
	before := stamp(doc)
	for wait > 0 && !terminalRun[tmpl(prop(doc, "status"))] && time.Since(started) < time.Duration(wait*float64(time.Second)) {
		if sleepCtx(r.Context(), 5*time.Second) != nil {
			break
		}
		next, err := view(id)
		if err != nil {
			break
		}
		doc = next
		if s := stamp(next); s != before {
			break
		}
	}
	keys, vals := entries(coalesce(prop(doc, "executedJobs"), jsonv.NewObject()))
	jobs := make([]string, len(keys))
	for i, k := range keys {
		v := vals[i]
		var step any = undef
		if liveJob.MatchString(tmpl(prop(v, "status"))) {
			for _, s := range jsonv.Arr(coalesce(prop(v, "steps"), []any{})) {
				if st := prop(s, "status"); truthy(st) && st != "completed" {
					step = s
					break
				}
			}
		}
		state := "not started"
		if st := prop(v, "status"); truthy(st) {
			state = tmpl(st)
		}
		line := "  " + k + ": " + state
		if h := prop(v, "ssh", "remoteHost"); truthy(h) {
			line += " on " + tmpl(h)
		}
		if n := prop(step, "name"); truthy(n) {
			line += ` (at "` + tmpl(n) + `")`
		}
		jobs[i] = line
	}
	slug := tmpl(coalesce(prop(doc, "slug"), id))
	status := tmpl(prop(doc, "status"))
	parts := []string{"Run " + slug + " of " + tmpl(prop(doc, "workflowName")) + ": " + status, strings.Join(jobs, "\n")}
	svc := coalesce(prop(doc, "inputs", "service"), jsonv.NewObject())
	if ep := prop(svc, "endpoint_name"); truthy(ep) {
		s := `Endpoint "` + tmpl(ep) + `"`
		if mo := prop(svc, "models"); truthy(mo) {
			s += " serving " + tmpl(mo)
		}
		if !terminalRun[status] {
			s += ": appears in the platform model catalog and session list once wait_for_endpoint completes."
		} else {
			s += ": not serving, the run is " + status + "."
		}
		parts = append(parts, s)
	}
	if terminalRun[status] && status != "completed" {
		parts = append(parts, "Use workflow_run_detail for the errors and log tail.")
	} else {
		parts = append(parts, m.runSessionsNote(r, ctx, slug))
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return &Outcome{Result: capText(strings.Join(keep, "\n")), Summary: slug + ": " + status}, nil
}

/* ---- the Status Monitor (hpc_status) ---- */

type monitorCache struct {
	at  time.Time
	url string
}

const monitorTTL = 300 * time.Second

func (m *Module) gatewayAuth() map[string]string {
	if k := m.GatewayKey(); k != "" {
		return map[string]string{"Authorization": "Bearer " + k}
	}
	return map[string]string{}
}

var monitorName = regexp.MustCompile(`(?i)status|hpc.?mon`)
var httpURL = regexp.MustCompile(`(?i)^https?:`)
var trailingSlashes = regexp.MustCompile(`/+$`)

func (m *Module) probeMonitor(ctx context.Context, base string) bool {
	c := &http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/fleet/summary", nil)
	if err != nil {
		return false
	}
	for k, v := range m.gatewayAuth() {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300 && strings.Contains(res.Header.Get("Content-Type"), "json")
}

// discoverStatusMonitor is tools.ts's: a running session of this account
// that answers as a Status Monitor, remembered five minutes.
func (m *Module) discoverStatusMonitor(r *http.Request, ctx ToolCtx) string {
	m.monitorMu.Lock()
	c := m.monitor
	m.monitorMu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < monitorTTL {
		return c.url
	}
	found := ""
	if out, err := m.cli(r, ctx, []string{"sessions", "ls", "-o", "json"}, 20*time.Second); err == nil {
		if v, err := parseJSON(out); err == nil {
			var cands []any
			for _, x := range jsonv.Arr(v) {
				href, ok := prop(x, "externalHref").(string)
				if tmpl(prop(x, "status")) == "running" && ok && httpURL.MatchString(href) {
					cands = append(cands, x)
				}
			}
			looks := func(x any) bool {
				return monitorName.MatchString(tmplOr(prop(x, "name"), "") + " " + tmplOr(prop(x, "targetName"), ""))
			}
			sort.SliceStable(cands, func(i, j int) bool { return looks(cands[i]) && !looks(cands[j]) })
			if len(cands) > 8 {
				cands = cands[:8]
			}
			for _, x := range cands {
				base := trailingSlashes.ReplaceAllString(tmpl(prop(x, "externalHref")), "")
				if m.probeMonitor(r.Context(), base) {
					found = base
					break
				}
			}
		}
	}
	m.monitorMu.Lock()
	m.monitor = monitorCache{at: time.Now(), url: found}
	m.monitorMu.Unlock()
	return found
}

var monitorViews = []struct{ name, path string }{
	{"summary", "/api/fleet/summary"}, {"fleet", "/api/fleet"}, {"queues", ""}, {"placement", "/api/placement"},
	{"insights", "/api/insights"}, {"storage", "/api/storage"}, {"events", "/api/events"},
}

func (m *Module) toolHpcStatus(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	eff := m.eff()
	if arg(args, "launch") == true {
		cliArgs := []string{"workflows", "run"}
		if truthy(arg(args, "inputs")) {
			v, err := parseArgJSON(arg(args, "inputs"))
			if err != nil {
				return nil, err
			}
			cliArgs = append(cliArgs, "-i", stringify(v))
		}
		cliArgs = append(cliArgs, eff.HpcStatusWorkflow)
		out, err := m.cli(r, ctx, cliArgs, 180*time.Second)
		if err != nil {
			return nil, err
		}
		return &Outcome{Result: "Status Monitor launch submitted via workflow " + eff.HpcStatusWorkflow + ".\n" + jsval.Slice(jsTrim(out), 1500) +
			"\nWatch it with workflow_runs; once it serves, set its URL as the Status Monitor URL in Settings so this tool can read it.", Summary: "monitor launch submitted"}, nil
	}
	base := eff.HpcStatusURL
	if base == "" {
		base = m.discoverStatusMonitor(r, ctx)
	}
	if base == "" {
		return &Outcome{Result: "No Status Monitor is configured on this deployment, and no running session on this account answers as one. Set the Status Monitor URL in Settings (or HPC_STATUS_URL), or deploy one with launch=true, which runs the " +
			eff.HpcStatusWorkflow + " workflow; only do that if the user asked for it.", Summary: "not configured"}, nil
	}
	view := tmplOr(arg(args, "view"), "summary")
	cluster := notWordDot.ReplaceAllString(tmplOr(arg(args, "cluster"), ""), "")
	path, names := "", make([]string, len(monitorViews))
	for i, v := range monitorViews {
		names[i] = v.name
		if v.name == view {
			path = v.path
			if v.name == "queues" {
				path = "/api/cluster-usage"
				if cluster != "" {
					path += "/" + cluster
				}
			}
		}
	}
	if path == "" {
		return &Outcome{Result: "Unknown view: " + view + ". Views: " + strings.Join(names, ", "), Summary: "bad view"}, nil
	}
	res, err := platform.Get(r.Context(), base+path, m.gatewayAuth(), 30*time.Second)
	if err != nil {
		msg := "fetch failed"
		if r.Context().Err() == nil && strings.Contains(err.Error(), "deadline") {
			msg = "The operation was aborted due to timeout"
		}
		return &Outcome{Result: "Status Monitor unreachable at " + base + ": " + msg, Summary: "unreachable"}, nil
	}
	if res.Status < 200 || res.Status >= 300 {
		return &Outcome{Result: "Status Monitor returned " + strconv.Itoa(res.Status) + " for " + path + ".", Summary: "http " + strconv.Itoa(res.Status)}, nil
	}
	sum := view
	if cluster != "" {
		sum += ":" + cluster
	}
	return &Outcome{Result: capText(string(res.Raw)), Summary: sum}, nil
}
