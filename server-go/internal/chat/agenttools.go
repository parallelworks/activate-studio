package chat

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
	"github.com/parallelworks/activate-studio/server-go/internal/tasks"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
	"github.com/parallelworks/activate-studio/server-go/internal/workspace"
)

// run_workflow, the session, agent-site and task tools, list_storage and
// the five calls that run only from an approval (tools.ts).

func init() {
	for name, fn := range map[string]func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error){
		"list_sessions":  (*Module).toolListSessions,
		"agent_sites":    (*Module).toolAgentSites,
		"agent_sessions": (*Module).toolAgentSessions,
		"start_agent":    (*Module).toolStartAgent,
		"agent_status":   (*Module).toolAgentStatus,
		"agent_steer":    (*Module).toolAgentSteer,
		"agent_goal":     (*Module).toolAgentGoal,
		"agent_approve":  (*Module).toolAgentApprove,
		"agent_stop":     (*Module).toolAgentStop,
		"delegate":       (*Module).toolDelegate,
		"task_status":    (*Module).toolTaskStatus,
		"list_storage":   (*Module).toolListStorage,
	} {
		goExecutors[name] = fn
	}
	for _, name := range []string{"start_cluster", "resume_cluster", "stop_cluster", "destroy_cluster", "cancel_workflow_run"} {
		n := name
		goExecutors[n] = func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error) {
			return &Outcome{Result: n + " changes something on the platform and runs only after the user approves the call in the Studio chat. Nothing ran.", Summary: "needs the user's approval"}, nil
		}
	}
}

const noPlatformKey = "No platform credential: the user can add their ACTIVATE API key under Settings, Model access."

/* ---- run_workflow ---- */

// setPath is tools.ts's setPath.
func setPath(obj *jsonv.Object, dotted string, value any) {
	parts := strings.Split(dotted, ".")
	cur := obj
	for _, p := range parts[:len(parts)-1] {
		v, _ := cur.Get(p)
		next, ok := v.(*jsonv.Object)
		if !truthy(v) || !ok {
			next = jsonv.NewObject()
			cur.Set(p, next)
		}
		cur = next
	}
	cur.Set(parts[len(parts)-1], value)
}

// pathGet is tools.ts's pathGet.
func pathGet(obj any, dotted string) any {
	v := obj
	for _, k := range strings.Split(dotted, ".") {
		switch x := v.(type) {
		case *jsonv.Object:
			e, ok := x.Get(k)
			if !ok {
				return undef
			}
			v = e
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != k {
				return undef
			}
			v = x[i]
		default:
			return undef
		}
	}
	return v
}

// findKey is tools.ts's findKey: the first scalar under key anywhere.
func findKey(obj any, key string) any {
	keys, vals := entries(obj)
	for i, k := range keys {
		v := vals[i]
		_, isObj := v.(*jsonv.Object)
		_, isArr := v.([]any)
		if k == key && !nullish(v) && v != "" && !isObj && !isArr {
			return v
		}
		if d := findKey(v, key); d != undef {
			return d
		}
	}
	return undef
}

// placeFlatInputs is tools.ts's placeFlatInputs.
func placeFlatInputs(inputs *jsonv.Object, decl []declared) []string {
	top := map[string]bool{}
	for _, d := range decl {
		top[strings.SplitN(d.path, ".", 2)[0]] = true
	}
	var moved []string
	for _, k := range inputs.Keys() {
		if top[k] {
			continue
		}
		var hits []declared
		for _, d := range decl {
			ps := strings.Split(d.path, ".")
			if ps[len(ps)-1] == k {
				hits = append(hits, d)
			}
		}
		if len(hits) != 1 {
			continue
		}
		v, _ := inputs.Get(k)
		setPath(inputs, hits[0].path, v)
		inputs.Delete(k)
		moved = append(moved, k+" → "+hits[0].path)
	}
	return moved
}

// resolveResource is tools.ts's resolveResource: a system name as the
// workflow's compute-clusters input wants it.
func (m *Module) resolveResource(r *http.Request, ctx ToolCtx, name string) string {
	raw := jsTrim(name)
	if strings.HasPrefix(raw, "pw://") {
		return raw
	}
	bare := notWordDot.ReplaceAllString(raw, "")
	if envs, err := m.envRows(r, ctx, 60*time.Second); err == nil {
		for _, e := range envs {
			if lowerJS(tmplOr(prop(e, "clusterName"), "")) == lowerJS(bare) {
				if u := prop(e, "clusterUser"); truthy(u) {
					return "pw://" + tmpl(u) + "/" + tmpl(prop(e, "clusterName"))
				}
				if n := prop(e, "clusterName"); truthy(n) {
					return tmpl(n)
				}
				break
			}
		}
	}
	return bare
}

// defaultPartition is tools.ts's defaultPartition: the system's up
// partition with the most free nodes.
func (m *Module) defaultPartition(r *http.Request, ctx ToolCtx, resource string) (name, why string, ok bool) {
	bare := lowerJS(notWordDot.ReplaceAllString(ownerPrefix.ReplaceAllString(resource, ""), ""))
	envs, err := m.envRows(r, ctx, 60*time.Second)
	if err != nil {
		return "", "", false
	}
	var up []any
	for _, e := range envs {
		if lowerJS(tmplOr(prop(e, "clusterName"), "")) == bare && prop(e, "partitionState") == "up" && truthy(prop(e, "partitionName")) {
			up = append(up, e)
		}
	}
	if len(up) == 0 {
		return "", "", false
	}
	sort.SliceStable(up, func(i, j int) bool { return numOr(prop(up[i], "availNodes"), 0) > numOr(prop(up[j], "availNodes"), 0) })
	e := up[0]
	return tmpl(prop(e, "partitionName")), tmpl(prop(e, "availNodes")) + "/" + tmpl(prop(e, "totalNodes")) + " nodes free, max walltime " + tmpl(prop(e, "maxTime")), true
}

type siteValues struct {
	account, qos, partition, from string
	has                           map[string]bool
}

type siteCacheEntry struct {
	at time.Time
	v  siteValues
}

// siteDefaults is tools.ts's siteDefaults: the account, QoS and partition
// of the user's most recent completed run on the system.
func (m *Module) siteDefaults(r *http.Request, ctx ToolCtx, cluster, workflow string) siteValues {
	name := lowerJS(ownerPrefix.ReplaceAllString(cluster, ""))
	m.siteMu.Lock()
	if hit, ok := m.siteCache[name]; ok && time.Since(hit.at) < 10*time.Minute {
		m.siteMu.Unlock()
		return hit.v
	}
	m.siteMu.Unlock()
	out := siteValues{has: map[string]bool{}}
	func() {
		text, err := m.cli(r, ctx, []string{"workflows", "runs", "list", "-o", "json"}, 60*time.Second)
		if err != nil {
			return
		}
		listed, err := cliJSON(text)
		if err != nil {
			return
		}
		wf := lowerJS(regexp.MustCompile(`^.*/`).ReplaceAllString(workflow, ""))
		var done []any
		for _, x := range runsOf(listed) {
			if regexp.MustCompile(`(?i)complete`).MatchString(tmplOr(prop(x, "status"), "")) {
				done = append(done, x)
			}
		}
		same := func(x any) int {
			if lowerJS(tmplOr(prop(x, "workflowName"), "")) == wf {
				return 1
			}
			return 0
		}
		sort.SliceStable(done, func(i, j int) bool {
			if d := same(done[j]) - same(done[i]); d != 0 {
				return d < 0
			}
			return strings.Compare(tmplOr(prop(done[j], "createdAt"), ""), tmplOr(prop(done[i], "createdAt"), "")) < 0
		})
		if len(done) > 40 {
			done = done[:40]
		}
		key := m.platformKey(ctx)
		for i := 0; i < len(done) && out.from == ""; i += 6 {
			batch := done[i:min(i+6, len(done))]
			ins := make([]any, len(batch))
			var wg sync.WaitGroup
			for j, x := range batch {
				wg.Add(1)
				go func(j int, x any) {
					defer wg.Done()
					ins[j] = prop(m.pf.APIJSON(r.Context(), "/api/workflow-runs/"+platform.EncodeURIComponent(tmpl(prop(x, "slug"))), key, 20*time.Second), "inputs")
				}(j, x)
			}
			wg.Wait()
			for j, x := range batch {
				in := ins[j]
				if !truthy(in) || !strings.Contains(lowerJS(stringify(in)), name) {
					continue
				}
				account, qos, partition := findKey(in, "account"), findKey(in, "qos"), findKey(in, "partition")
				if account == undef && qos == undef {
					continue
				}
				if account != undef {
					out.account, out.has["account"] = tmpl(account), true
				}
				if qos != undef {
					out.qos, out.has["qos"] = tmpl(qos), true
				}
				if partition != undef {
					out.partition, out.has["partition"] = tmpl(partition), true
				}
				out.from = tmpl(prop(x, "slug"))
				break
			}
		}
	}()
	m.siteMu.Lock()
	if m.siteCache == nil {
		m.siteCache = map[string]siteCacheEntry{}
	}
	m.siteCache[name] = siteCacheEntry{at: time.Now(), v: out}
	m.siteMu.Unlock()
	return out
}

func (s siteValues) get(k string) string {
	switch k {
	case "account":
		return s.account
	case "qos":
		return s.qos
	}
	return s.partition
}

func configNamed(doc any, name string) any {
	for _, c := range jsonv.Arr(coalesce(prop(doc, "configurations"), []any{})) {
		if lowerJS(tmplOr(coalesce(prop(c, "name"), prop(c, "id")), "")) == lowerJS(name) {
			return c
		}
	}
	return undef
}

var (
	notRunName    = regexp.MustCompile(`[^A-Za-z0-9_./-]`)
	missingField  = regexp.MustCompile(`(?i)missing required field`)
	missingFields = regexp.MustCompile(`(?i)missing required fields?:\s*([^;]+)`)
)

func (m *Module) toolRunWorkflow(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	rawName := tmplOr(arg(args, "name"), "")
	parsed := workflows.ParseEntry(rawName)
	wfName := notRunName.ReplaceAllString(rawName, "")
	if parsed != nil {
		wfName = parsed.Entry
	}
	dryRun := arg(args, "dry_run") != false
	cliArgs := []string{"workflows", "run"}
	if dryRun {
		cliArgs = append(cliArgs, "--dry-run")
	}
	if parsed != nil && parsed.Kind == "github" {
		gdoc, err := m.wfDoc(r, ctx, wfName)
		if err != nil {
			return nil, err
		}
		if ra := prop(gdoc, "runAs"); !nullish(ra) {
			wfName = tmpl(ra)
		}
		if perms := jsonv.Arr(prop(gdoc, "permissions")); len(perms) > 0 {
			if arg(args, "trust") != true {
				return &Outcome{Result: "Not run. " + parsed.Entry + " asks for access to these account variables: " + joinJS(perms, ", ") +
					" ('*' means all of them). Running it grants that access to the repository, revocable with pw workflows permissions revoke. Ask the user to approve; if they do, call run_workflow again with trust: true.",
					Summary: "needs the user's approval"}, nil
			}
			cliArgs = append(cliArgs, "--trust")
		}
	}
	inputs := jsonv.NewObject()
	if truthy(arg(args, "inputs")) {
		v, err := parseArgJSON(arg(args, "inputs"))
		if err != nil {
			return nil, err
		}
		if o, ok := v.(*jsonv.Object); ok {
			inputs = o
		}
	}
	resource := arg(args, "resource")
	config := arg(args, "config")
	if truthy(resource) {
		target := m.resolveResource(r, ctx, tmpl(resource))
		var slots []string
		if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
			slots = slotsOf(declaredInputs(doc), "compute-clusters")
		}
		if len(slots) == 0 {
			slots = []string{"resource"}
		}
		for _, p := range slots {
			setPath(inputs, p, target)
		}
	}
	if truthy(config) {
		cliArgs = append(cliArgs, "--saved-inputs", tmpl(config))
	}
	setInputs := func() {
		for i, a := range cliArgs {
			if a == "-i" && i+1 < len(cliArgs) {
				cliArgs[i+1] = stringify(inputs)
				return
			}
		}
		cliArgs = append(cliArgs, "-i", stringify(inputs))
	}
	if inputs.Len() > 0 {
		if truthy(config) {
			if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
				if cin := prop(configNamed(doc, tmpl(config)), "inputs"); truthy(cin) {
					if o, ok := mergeInputs(cin, inputs).(*jsonv.Object); ok {
						inputs = o
					}
				}
			}
		}
		cliArgs = append(cliArgs, "-i", stringify(inputs))
	}
	fillNote := ""
	if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
		decl := declaredInputs(doc)
		merged := inputs
		if truthy(config) && inputs.Len() == 0 {
			if cin := prop(configNamed(doc, tmpl(config)), "inputs"); truthy(cin) {
				if o, ok := mergeInputs(cin, jsonv.NewObject()).(*jsonv.Object); ok {
					merged = o
				}
			}
		}
		moved := placeFlatInputs(merged, decl)
		clusterSlot := ""
		if s := slotsOf(decl, "compute-clusters"); len(s) > 0 {
			clusterSlot = s[0]
		}
		var cluster string
		if !nullish(resource) {
			cluster = tmpl(resource)
		} else if clusterSlot != "" {
			cluster = tmplOr(pathGet(merged, clusterSlot), "")
		}
		var filled []string
		sched := findKey(merged, "scheduler")
		if cluster != "" && sched != false && sched != "false" {
			want := func(t, leaf string) []declared {
				var out []declared
				for _, d := range decl {
					if (d.typ == t || strings.HasSuffix(d.path, "slurm."+leaf)) && !truthy(pathGet(merged, d.path)) {
						out = append(out, d)
					}
				}
				return out
			}
			empty := map[string][]declared{"account": want("slurm-accounts", "account"), "qos": want("slurm-qos", "qos"), "partition": want("slurm-partitions", "partition")}
			if len(empty["account"])+len(empty["qos"])+len(empty["partition"]) > 0 {
				site := m.siteDefaults(r, ctx, cluster, wfName)
				for _, k := range []string{"account", "qos", "partition"} {
					if site.get(k) == "" {
						continue
					}
					for _, d := range empty[k] {
						setPath(merged, d.path, site.get(k))
						filled = append(filled, k+" "+site.get(k))
					}
				}
				if len(filled) > 0 {
					var uniq []string
					seen := map[string]bool{}
					for _, f := range filled {
						if !seen[f] {
							seen[f] = true
							uniq = append(uniq, f)
						}
					}
					fillNote = "\nFilled from your run " + orUndefined(site.from) + " on " + ownerPrefix.ReplaceAllString(cluster, "") + ": " + strings.Join(uniq, ", ") + "."
				}
			}
		}
		if len(moved) > 0 {
			fillNote += "\nPlaced inputs: " + strings.Join(moved, "; ") + "."
		}
		if len(moved) > 0 || len(filled) > 0 {
			inputs = merged
			setInputs()
		}
	}
	if !truthy(resource) && !truthy(config) {
		if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
			var configs []string
			for _, c := range jsonv.Arr(coalesce(prop(doc, "configurations"), []any{})) {
				if n := tmplOr(coalesce(prop(c, "name"), prop(c, "id")), ""); n != "" {
					configs = append(configs, n)
				}
			}
			var empty []string
			for _, p := range slotsOf(declaredInputs(doc), "compute-clusters") {
				v := pathGet(inputs, p)
				o, isObj := v.(*jsonv.Object)
				a, isArr := v.([]any)
				if nullish(v) || v == "" || (isObj && o.Len() == 0) || (isArr && len(a) == 0) {
					empty = append(empty, p)
				}
			}
			if len(configs) > 0 && len(empty) > 0 {
				if len(configs) > 12 {
					configs = configs[:12]
				}
				return &Outcome{Result: "This launch names no saved configuration and no system, so " + strings.Join(empty, ", ") + " would be left to the workflow's default. Launch again with config set to one of: " +
					strings.Join(configs, ", ") + "; or with resource set to a system name. Nothing was submitted.", Summary: "needs a configuration or a system"}, nil
			}
		}
	}
	partitionNote := ""
	if sch, _ := inputs.Get("scheduler"); truthy(resource) && sch != false {
		if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
			var empty []string
			for _, p := range slotsOf(declaredInputs(doc), "slurm-partitions") {
				if !truthy(pathGet(inputs, p)) {
					empty = append(empty, p)
				}
			}
			if len(empty) > 0 {
				if name, why, ok := m.defaultPartition(r, ctx, tmpl(resource)); ok {
					for _, p := range empty {
						setPath(inputs, p, name)
					}
					setInputs()
					partitionNote = "\nPartition " + name + " was chosen for " + tmpl(resource) + " (" + why + "); name one in inputs to override."
				}
			}
		}
	}
	cliArgs = append(cliArgs, wfName, "-o", "json")
	runner := workspace.Runner{CLI: func(c context.Context, a []string, timeout time.Duration) (string, error) {
		return m.pwToolCLI(c, a, timeout, deref(ctx.UserKey))
	}}
	if ms := numJS(m.pf.Getenv("STUDIO_WORKSPACE_RETRY_MS")); ms > 0 {
		runner.RetryEvery = time.Duration(ms * float64(time.Millisecond))
	}
	out, ws, err := workspace.With(r.Context(), runner, func(c context.Context) (string, error) {
		return m.pwToolCLI(c, cliArgs, 180*time.Second, deref(ctx.UserKey))
	})
	if err != nil {
		msg := err.Error()
		if missingField.MatchString(msg) {
			missing := ""
			if g := missingFields.FindStringSubmatch(msg); g != nil {
				missing = jsTrim(g[1])
			} else {
				parts := strings.Split(msg, ":")
				missing = jsTrim(parts[len(parts)-1])
			}
			known := ""
			if doc, err := m.wfDoc(r, ctx, wfName); err == nil {
				var accts []any
				seen := map[string]bool{}
				for _, c := range jsonv.Arr(coalesce(prop(doc, "configurations"), []any{})) {
					a := prop(c, "inputs", "cluster", "slurm", "account")
					if truthy(a) && !seen[stringify(a)] {
						seen[stringify(a)] = true
						accts = append(accts, a)
					}
				}
				if len(accts) > 0 {
					known = " Configurations on this workflow use account " + joinJS(accts, " or ") + "; reuse one if it applies to this system."
				}
			}
			return &Outcome{Result: "The submission was rejected for a missing value: " + missing + "." + known +
				" No earlier completed run of yours on this system carried it either, so ask the user for that value (for example the project account to charge) and launch again with it in inputs.",
				Summary: "missing required input"}, nil
		}
		return nil, err
	}
	trimmed := jsTrim(out)
	where, launched := "", ""
	if v, err := cliJSON(trimmed); err == nil {
		run := coalesce(prop(v, "run"), v)
		id := coalesce(prop(run, "slug"), prop(run, "id"))
		if truthy(prop(run, "slug")) || truthy(prop(run, "id")) {
			if dryRun {
				where = "\nThe inputs are valid; nothing ran. To launch it, call run_workflow again with the same arguments and dry_run false."
			} else {
				where = "\nRun " + tmpl(id) + ". Follow it with watch_run."
				launched = tmpl(id)
				if routes := workflows.Current(); routes != nil && routes.Runs != nil {
					var res *string
					if truthy(resource) {
						res = runs.Str(tmpl(resource))
					}
					routes.Runs.RecordRun(runs.RecordInput{Slug: launched, Workflow: wfName, Resource: res, ConversationID: ctx.ConversationID, Owner: ctx.UserID})
				}
			}
		}
	}
	wsNote := ""
	if ws.Started {
		wsNote = "\n(The platform user workspace was not running; it was started and the launch retried.)"
	}
	result := capText(trimmed)
	if result == "" {
		result = "Run submitted."
		if dryRun {
			result = "Validation passed."
		}
	}
	sum := "run submitted"
	switch {
	case dryRun:
		sum = "dry run ok"
	case launched != "":
		sum = "run submitted: " + launched
	}
	return &Outcome{Result: result + where + partitionNote + fillNote + wsNote, Summary: sum}, nil
}

func orUndefined(s string) string {
	if s == "" {
		return "undefined"
	}
	return s
}

/* ---- sessions ---- */

// runSessionsNote is tools.ts's runSessionsNote, for watch_run.
func (m *Module) runSessionsNote(r *http.Request, ctx ToolCtx, slug string) string {
	key := m.platformKey(ctx)
	if key == "" {
		return ""
	}
	rows, err := sessions.List(r.Context(), m.pf, key)
	if err != nil {
		return ""
	}
	var lines []string
	for _, x := range rows {
		if !sessions.OfRun(x, slug) {
			continue
		}
		if x.Status == "running" {
			lines = append(lines, "Session "+x.Name+" is running. Show it in the reply with this markdown:\n"+sessions.Embed(x))
		} else {
			lines = append(lines, "Session "+x.Name+": "+x.Status+".")
		}
	}
	return strings.Join(lines, "\n")
}

var matchWords = regexp.MustCompile(`[|,\s]+`)

func (m *Module) toolListSessions(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	key := m.platformKey(ctx)
	if key == "" {
		return &Outcome{Result: noPlatformKey, Summary: "no credential"}, nil
	}
	run := ""
	if truthy(arg(args, "run")) {
		run = tmpl(arg(args, "run"))
	}
	match := ""
	if truthy(arg(args, "match")) {
		match = lowerJS(jsTrim(tmpl(arg(args, "match"))))
	}
	var words []string
	for _, w := range matchWords.Split(match, -1) {
		if w != "" {
			words = append(words, w)
		}
	}
	all := arg(args, "all") == true
	list, err := sessions.List(r.Context(), m.pf, key)
	if err != nil {
		return nil, err
	}
	var rows []*sessions.SessionRow
	for _, x := range list {
		if (run != "" && !sessions.OfRun(x, run)) || (!all && x.Status != "running") {
			continue
		}
		if len(words) > 0 {
			hay := []string{lowerJS(x.Name)}
			if x.Run != nil {
				hay = append(hay, lowerJS(x.Run.Slug), lowerJS(x.Run.Workflow))
			}
			hit := false
			for _, w := range words {
				for _, h := range hay {
					if strings.Contains(h, w) {
						hit = true
					}
				}
			}
			if !hit {
				continue
			}
		}
		rows = append(rows, x)
	}
	if len(rows) == 0 {
		state := "running "
		if all {
			state = ""
		}
		res := "No running sessions."
		switch {
		case run != "":
			res = "Run " + run + " has no " + state + "session."
		case match != "":
			res = "No " + state + `session matches "` + match + `".`
		}
		return &Outcome{Result: res, Summary: "none"}, nil
	}
	blocks := make([]string, len(rows))
	for i, x := range rows {
		line := x.Name + " (" + orStr(x.Type, "session") + ", " + x.Status
		if x.User != "" {
			line += ", owner " + x.User
		}
		if x.Resource != "" {
			line += ", on " + x.Resource
		}
		switch {
		case x.Run != nil:
			line += ", opened by run " + x.Run.Slug + " of " + x.Run.Workflow
		case run != "":
			line += ", the session of run " + run
		}
		line += ")"
		if x.Status == "running" {
			if e := sessions.Embed(x); e != "" {
				line += "\n" + e
			}
		}
		blocks[i] = line
	}
	return &Outcome{Result: capText(strings.Join(blocks, "\n\n")), Summary: strconv.Itoa(len(rows)) + " session" + plural(len(rows))}, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

/* ---- agent sites ---- */

func (m *Module) sites() (*agentsites.Sites, error) {
	s := tasks.CurrentSites()
	if s == nil {
		return nil, errNotHere
	}
	return s, nil
}

func (m *Module) agentKey(s *agentsites.Sites, ctx ToolCtx) string {
	return s.AgentKey(deref(ctx.UserKey), m.A.Cfg.AuthEnabled())
}

func (m *Module) toolAgentSites(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	auth := m.A.Cfg.AuthEnabled()
	key := m.agentKey(s, ctx)
	list, err := s.ListSites(r.Context(), key, auth)
	if err != nil {
		return nil, err
	}
	var probe []string
	for _, x := range strings.Split(tmplOr(arg(args, "probe"), ""), ",") {
		if x = jsTrim(x); x != "" && len(probe) < 5 {
			probe = append(probe, x)
		}
	}
	checked := make([]string, len(probe))
	var wg sync.WaitGroup
	for i, site := range probe {
		wg.Add(1)
		go func(i int, site string) {
			defer wg.Done()
			st, err := s.ProbeSite(r.Context(), site, key, auth)
			if err != nil {
				checked[i] = site + ": " + jsval.Slice(err.Error(), 200)
				return
			}
			if truthy(prop(st, "usable")) {
				n := prop(st, "sessions")
				one := ""
				if n != 1.0 {
					one = "s"
				}
				checked[i] = site + ": pw code " + tmpl(prop(st, "version")) + " on " + tmpl(prop(st, "hostname")) + ", " + tmpl(n) + " session" + one
			} else {
				checked[i] = site + ": not usable, " + tmpl(prop(st, "why"))
			}
		}(i, site)
	}
	wg.Wait()
	lines := make([]string, len(list))
	for i, x := range list {
		l := tmpl(prop(x, "name"))
		switch {
		case prop(x, "kind") == "local":
			l += " (this Studio's host)"
		case truthy(prop(x, "type")):
			l += " (" + tmpl(prop(x, "type")) + ")"
		}
		lines[i] = l
	}
	head := orStr(strings.Join(lines, "\n"), "No sites: the user has no active clusters.")
	return &Outcome{Result: capText(strings.Join(append([]string{head}, checked...), "\n")), Summary: strconv.Itoa(len(list)) + " site" + plural(len(list))}, nil
}

func (m *Module) toolAgentSessions(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	rows, err := s.Sessions(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), m.A.Cfg.AuthEnabled())
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(rows))
	for i, x := range rows {
		lines[i] = jsTrim(tmpl(prop(x, "id")) + "  " + tmplOr(prop(x, "status"), "") + "  " + tmplOr(prop(x, "name"), "") + "  " + tmplOr(prop(x, "workspace"), "") + "  " + tmplOr(prop(x, "model"), ""))
	}
	return &Outcome{Result: capText(orStr(strings.Join(lines, "\n"), "No sessions on that site.")), Summary: strconv.Itoa(len(rows)) + " session" + plural(len(rows))}, nil
}

var agentModel = regexp.MustCompile(`^[^\s:]+:[^\s]+/[^\s]+$`)

// optString is v ? String(v) : "" (null).
func optStr(v any) string {
	if truthy(v) {
		return tmpl(v)
	}
	return ""
}

func (m *Module) toolStartAgent(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	model := optStr(arg(args, "model"))
	if model == "" && agentModel.MatchString(deref(ctx.Model)) {
		model = deref(ctx.Model)
	}
	turns := numOr(arg(args, "max_turns"), 0)
	st, err := s.Start(r.Context(), agentsites.StartAgent{Site: tmplOr(arg(args, "site"), ""), Key: m.agentKey(s, ctx), Objective: tmplOr(arg(args, "objective"), ""),
		Goal: optStr(arg(args, "goal")), MaxTurns: turns, Workspace: optStr(arg(args, "workspace")), Model: model, PermissionMode: optStr(arg(args, "permission_mode"))}, m.A.Cfg.AuthEnabled())
	if err != nil {
		return nil, err
	}
	goal := ""
	if truthy(prop(st, "goal")) {
		goal = ", with a goal it works toward until met or bounded"
	}
	site := tmpl(prop(st, "site"))
	return &Outcome{Result: "Started pw code session " + tmpl(prop(st, "session")) + " on " + site + " (" + tmpl(prop(st, "host")) + "), workspace " + tmpl(prop(st, "workspace")) + ", mode " + tmpl(prop(st, "mode")) + goal +
		". Follow it with agent_status; it can also be opened in a terminal there with pw code attach " + site + "/" + tmpl(prop(st, "session")) + ".", Summary: "agent started on " + site}, nil
}

func (m *Module) toolAgentStatus(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	st, err := s.Status(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), tmplOr(arg(args, "session"), ""), numOr(arg(args, "lines"), 12), m.A.Cfg.AuthEnabled())
	if err != nil {
		return nil, err
	}
	goal := "goal: none"
	if g := prop(st, "goal"); truthy(g) {
		state := "not active"
		if truthy(prop(g, "active")) {
			state = "active"
			if truthy(prop(g, "paused")) {
				state = "paused"
			} else if truthy(prop(g, "checking")) {
				state = "checking"
			}
		}
		goal = "goal: " + state
	}
	check := ""
	if c := prop(st, "lastCheck"); truthy(c) {
		check = "last check: turn " + tmplOr(prop(c, "turns"), "?") + ", " + tmpl(either(prop(c, "outcome"), "not met yet"))
		if rs := prop(c, "reason"); truthy(rs) {
			check += ": " + tmpl(rs)
		}
	}
	approvals := jsonv.Arr(prop(st, "approvals"))
	asks := ""
	if len(approvals) > 0 {
		ls := make([]string, len(approvals))
		for i, a := range approvals {
			ls[i] = "  " + tmpl(prop(a, "id")) + ": " + tmpl(prop(a, "text"))
		}
		asks = "waiting for approval:\n" + strings.Join(ls, "\n")
	}
	parts := []string{tmpl(prop(st, "session")) + " on " + tmpl(prop(st, "site")) + ": " + tmpl(prop(st, "status")) + ", workspace " + tmpl(prop(st, "workspace")) + ", model " + tmpl(prop(st, "model")), goal, check, asks, "recent:"}
	for _, l := range jsonv.Arr(prop(st, "recent")) {
		parts = append(parts, tmpl(l))
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	sum := tmpl(prop(st, "status"))
	if len(approvals) > 0 {
		sum += ", needs approval"
	}
	return &Outcome{Result: capText(strings.Join(keep, "\n")), Summary: sum}, nil
}

func (m *Module) toolAgentSteer(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	mode := "direction"
	if arg(args, "mode") == "message" {
		mode = "message"
	}
	out, err := s.Steer(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), tmplOr(arg(args, "session"), ""), tmplOr(arg(args, "text"), ""), mode, m.A.Cfg.AuthEnabled())
	if err != nil {
		return nil, err
	}
	res := "Queued as the next turn."
	if out != nil && out.Has("delivered") {
		res = "Delivered to the working turn."
	} else if truthy(prop(out, "asMessage")) {
		res = "No turn was working; sent as the next message."
	}
	return &Outcome{Result: res, Summary: "steered"}, nil
}

func (m *Module) toolAgentGoal(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	action := tmplOr(arg(args, "action"), "")
	out, err := s.Goal(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), tmplOr(arg(args, "session"), ""), action, optStr(arg(args, "condition")), numOr(arg(args, "max_turns"), 0), m.A.Cfg.AuthEnabled())
	if err != nil {
		return nil, err
	}
	res := orStr(jsTrim(tmplOr(prop(out, "output"), "")), "Done.")
	if st := prop(out, "status"); truthy(st) {
		state := "not active"
		if truthy(prop(st, "active")) {
			state = "active"
			if truthy(prop(st, "paused")) {
				state = "paused"
			}
		}
		res += "\n(goal " + state + ")"
	}
	return &Outcome{Result: capText(res), Summary: "goal " + action}, nil
}

func (m *Module) toolAgentApprove(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	allow := arg(args, "allow") == true
	if _, err := s.Approve(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), tmplOr(arg(args, "session"), ""), tmplOr(arg(args, "approval"), ""), allow, optStr(arg(args, "message")), m.A.Cfg.AuthEnabled()); err != nil {
		return nil, err
	}
	if allow {
		return &Outcome{Result: "Approved.", Summary: "approved"}, nil
	}
	return &Outcome{Result: "Denied.", Summary: "denied"}, nil
}

func (m *Module) toolAgentStop(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	s, err := m.sites()
	if err != nil {
		return nil, err
	}
	if _, err := s.Stop(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), tmplOr(arg(args, "session"), ""), m.A.Cfg.AuthEnabled()); err != nil {
		return nil, err
	}
	return &Outcome{Result: "Interrupted. A goal stays set until paused or cleared with agent_goal.", Summary: "interrupted"}, nil
}

/* ---- delegation ---- */

func (m *Module) toolDelegate(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	agents, _ := arg(args, "subtasks").([]any)
	if !truthy(arg(args, "objective")) || len(agents) == 0 {
		return &Outcome{Result: "objective and subtasks are required", Summary: "error"}, nil
	}
	model := deref(ctx.Model)
	if model == "" {
		return &Outcome{Result: "No model in context to run agents with.", Summary: "error"}, nil
	}
	eff := m.eff()
	if !eff.DelegationEnabled {
		return &Outcome{Result: "Delegation is disabled for this deployment; do the work yourself in this conversation.", Summary: "disabled"}, nil
	}
	maxAgents := float64(eff.DelegationMaxAgents)
	if float64(len(agents)) > maxAgents {
		return &Outcome{Result: "Too many subtasks: this deployment allows " + jsonv.FormatNumber(maxAgents) + ". Combine them into fewer, larger strands.", Summary: "over ceiling"}, nil
	}
	e := tasks.CurrentEngine()
	if e == nil {
		return nil, errNotHere
	}
	execution := "local"
	if arg(args, "execution") == "campaign" {
		execution = "campaign"
	}
	var resource any
	if execution == "campaign" && truthy(arg(args, "resource")) {
		resource = m.resolveResource(r, ctx, tmpl(arg(args, "resource")))
	}
	var runtime any
	switch arg(args, "runtime") {
	case "runner":
		runtime = "runner"
	case "session":
		runtime = "session"
	}
	specs := make([]tasks.AgentSpec, len(agents))
	for i, a := range agents {
		specs[i] = tasks.AgentSpec{Persona: tmplOr(prop(a, "persona"), ""), Objective: tmplOr(prop(a, "objective"), "")}
	}
	t, err := e.Create(tasks.CreateOptions{Objective: tmpl(arg(args, "objective")), Model: model, Agents: specs,
		MaxAgents: maxAgents, MaxDepth: float64(eff.DelegationMaxDepth), Execution: execution, Resource: resource, Runtime: runtime})
	if err != nil {
		return &Outcome{Result: "Could not delegate: " + err.Error(), Summary: "error"}, nil
	}
	sum := e.Summary(t.ID)
	id := tmpl(prop(sum, "id"))
	campaign := ""
	if prop(sum, "execution") == "campaign" {
		campaign = "; campaign on " + tmpl(prop(sum, "resource")) + ", agents run as platform workflow runs and survive restarts"
	}
	return &Outcome{Result: "Task " + id + " started with " + strconv.Itoa(len(jsonv.Arr(prop(sum, "agents")))) + " subtasks (ceiling " + tmpl(prop(sum, "maxAgents")) + ", depth " + tmpl(prop(sum, "maxDepth")) + campaign +
		"). Results land under tasks/" + id + "/ in the knowledge base. In your reply, give the user this link to watch the agents live: [View agents working](#view=agents:" + id + "). Poll with task_status; do not wait idly, tell the user it is running.",
		Summary: "task " + id}, nil
}

func (m *Module) toolTaskStatus(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	e := tasks.CurrentEngine()
	if e == nil {
		return nil, errNotHere
	}
	mid := tmplOr(arg(args, "id"), "")
	if mid == "" {
		all := e.List()
		res := "No delegated tasks."
		if len(all) > 0 {
			res = stringify1(all)
		}
		return &Outcome{Result: res, Summary: strconv.Itoa(len(all)) + " tasks"}, nil
	}
	sum := e.Summary(mid)
	snap := e.Get(mid)
	if sum == nil || snap == nil {
		return &Outcome{Result: "No such task: " + mid, Summary: "error"}, nil
	}
	out := sum.Clone()
	var agents []any
	for _, a := range jsonv.Arr(prop(sum, "agents")) {
		agents = append(agents, jsonv.NewObject("name", prop(a, "name"), "status", prop(a, "state"), "depth", prop(a, "depth"), "parent", prop(a, "parent"), "note", prop(a, "note"), "result", prop(a, "resultPath")))
	}
	if agents == nil {
		agents = []any{}
	}
	out.Set("agents", agents)
	board := snap.Board
	if len(board) > 15 {
		board = board[len(board)-15:]
	}
	tail := make([]string, len(board))
	for i, b := range board {
		tail[i] = "[" + b.Topic + "] " + b.From + ": " + jsval.Slice(b.Body, 200)
	}
	return &Outcome{Result: stringify1(out) + "\n\nRecent board:\n" + strings.Join(tail, "\n"), Summary: "task " + mid + ": " + tmpl(prop(sum, "state"))}, nil
}

/* ---- list_storage ---- */

func (m *Module) toolListStorage(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	key := m.platformKey(ctx)
	if key == "" {
		return &Outcome{Result: noPlatformKey, Summary: "no credential"}, nil
	}
	prepared, _, problem := prepareArgs("list_storage", args, "")
	if problem != "" {
		return &Outcome{Result: problem, Summary: "invalid arguments"}, nil
	}
	result, failed := m.runPlatformTool(r.Context(), "list_storage", prepared, key)
	sum := "storage listed"
	if v, err := parseJSON(result); err == nil {
		if a, ok := v.([]any); ok {
			sum = strconv.Itoa(len(a)) + " storage item" + plural(len(a))
		}
	}
	if failed {
		sum = "failed"
	}
	return &Outcome{Result: result, Summary: sum}, nil
}
