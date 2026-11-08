package search

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// QueryFailure is a library a query across several could not ask.
type QueryFailure struct {
	Library string `json:"library"`
	Error   string `json:"error"`
}

// QueryResult is query.ts's QueryResult, in its key order.
type QueryResult struct {
	Columns       []string             `json:"columns"`
	Rows          [][]string           `json:"rows"`
	RowLibraries  []string             `json:"rowLibraries,omitempty"`
	ElapsedMS     int64                `json:"elapsedMs"`
	Truncated     bool                 `json:"truncated"`
	Failed        *[]QueryFailure      `json:"failed,omitempty"`
	Engine        string               `json:"engine,omitempty"`
	MergeReason   string               `json:"mergeReason,omitempty"`
	Omitted       []libraries.Omission `json:"omitted,omitempty"`
	hasRowLibrary bool
}

// MarshalJSON keeps rowLibraries: [] across several libraries.
func (q QueryResult) MarshalJSON() ([]byte, error) {
	type plain QueryResult
	b, err := marshalJS(plain(q))
	if err != nil || !q.hasRowLibrary || len(q.RowLibraries) > 0 {
		return b, err
	}
	s := string(b)
	i := strings.Index(s, `,"elapsedMs"`)
	return []byte(s[:i] + `,"rowLibraries":[]` + s[i:]), nil
}

// QuerySQL is querySql's answer.
type QuerySQL struct {
	Stages   []gufi.Stage   `json:"stages"`
	Commands []QueryCommand `json:"commands"`
}

// QueryCommand is a GufiCommand of querySql.
type QueryCommand struct {
	Engine  string `json:"engine"`
	Site    string `json:"site,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Command string `json:"command"`
}

func (e *Engine) planner(ctx context.Context, l libraries.Library, labels bool) (*gufi.Planner, error) {
	p := &gufi.Planner{IndexRoot: l.IndexRoot}
	if l.Remote() {
		t, err := e.Target(l.IndexRoot)
		if err != nil {
			return nil, err
		}
		p.IndexRoot = t.IndexRoot
	}
	if labels && e.Tags != nil {
		a, _ := filepath.Abs(l.IndexRoot)
		b, _ := filepath.Abs(e.GufiIndex)
		if !l.Remote() && a == b {
			p.Labels.Dirs = e.Tags.DirEntries(ctx)
		} else if ents, ok := e.Tags.Library.EntriesForIndexRoot(l.IndexRoot); ok {
			p.Labels.Dirs, p.Labels.Files = ents, ents
		} else {
			p.Labels.Dirs = []gufi.LabelEntry{}
		}
	}
	return p, nil
}

func labelFilter(body []byte) bool {
	var b struct {
		Builder *struct {
			Filters []struct {
				Field any `json:"field"`
			} `json:"filters"`
		} `json:"builder"`
	}
	if json.Unmarshal(body, &b) != nil || b.Builder == nil {
		return false
	}
	for _, f := range b.Builder.Filters {
		if f.Field == "label" {
			return true
		}
	}
	return false
}

func (e *Engine) planFor(ctx context.Context, l libraries.Library, body []byte) (*gufi.Planner, gufi.Plan, error) {
	p, err := e.planner(ctx, l, labelFilter(body))
	if err != nil {
		return nil, gufi.Plan{}, err
	}
	plan, err := p.PlanFor(body)
	return p, plan, err
}

func capThreads(args []string) []string {
	out := append([]string(nil), args...)
	for i := 1; i < len(out); i++ {
		if out[i-1] == "-n" {
			n, err := strconv.Atoi(out[i])
			if err != nil || n == 0 {
				n = 4
			}
			out[i] = strconv.Itoa(min(n, 4))
		}
	}
	return out
}

// planCommand is planCommand.
func (e *Engine) planCommand(p *gufi.Planner, l libraries.Library, plan gufi.Plan) (QueryCommand, error) {
	if l.Remote() {
		t, err := e.Target(l.IndexRoot)
		if err != nil {
			return QueryCommand{}, err
		}
		sub, err := gufi.CleanSubtree(plan.Subtree)
		if err != nil {
			return QueryCommand{}, err
		}
		target := t.IndexRoot
		if sub != "" {
			target = path.Join(t.IndexRoot, sub)
		}
		argv := append(append([]string{libraries.GufiTool(t.GufiBin, "")}, capThreads(plan.Args())...), target)
		for i, a := range argv {
			argv[i] = gufi.ShellQuote(a)
		}
		return QueryCommand{Engine: "gufi_query", Site: remote.SiteName(t.Site), Shell: strings.Join(remote.ShellFor(t.Site), " "), Command: strings.Join(argv, " ")}, nil
	}
	c, err := p.Command(e.GufiBin, plan)
	return QueryCommand{Engine: "gufi_query", Command: c}, err
}

// QuerySQL is querySql: the SQL and commands of a query, without running it.
func (e *Engine) QuerySQL(ctx context.Context, body []byte, viewer libraries.Viewer, label string) (*QuerySQL, error) {
	libs, _, err := e.Reg.Scope(libraryParam(body), viewer, label)
	if err != nil {
		return nil, err
	}
	out := &QuerySQL{Stages: []gufi.Stage{}, Commands: []QueryCommand{}}
	for i, l := range libs {
		p, plan, err := e.planFor(ctx, l, body)
		if err != nil {
			return nil, err
		}
		c, err := e.planCommand(p, l, plan)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			out.Stages = plan.List()
			if out.Stages == nil {
				out.Stages = []gufi.Stage{}
			}
		}
		out.Commands = append(out.Commands, c)
	}
	if len(libs) == 0 {
		return nil, apierr.New(404, "no library to query")
	}
	return out, nil
}

// libraryParam is the body's library: a string, or a list of them.
func libraryParam(body []byte) []string {
	var b struct {
		Library any `json:"library"`
	}
	json.Unmarshal(body, &b)
	switch v := b.Library.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, jsStr(x))
		}
		return out
	}
	return nil
}

func jsStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// run is query.ts's run: the plan's gufi_query on the library's index.
func (e *Engine) run(ctx context.Context, l libraries.Library, args []string, subtree string) (stdout, stderr string, err error) {
	if l.Remote() {
		sub, err := gufi.CleanSubtree(subtree)
		if err != nil {
			return "", "", err
		}
		out, err := e.RemoteQuery(ctx, l.IndexRoot, capThreads(args), sub)
		return out, "", err
	}
	scope, err := gufi.ScopePath(l.IndexRoot, subtree)
	if err != nil {
		return "", "", err
	}
	argv := append(append([]string{}, args...), scope)
	t0 := time.Now()
	res, rerr := e.Local.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: argv, Timeout: 60 * time.Second})
	o := gufitrace.Run{Start: t0, Elapsed: res.Elapsed}
	if res.Signal != "" {
		o.Signal = res.Signal
	} else if rerr != nil && res.Elapsed == 0 {
		o.Error = rerr.Error()
	} else {
		c := res.ExitCode
		o.Code = &c
	}
	se := res.Stderr
	o.Stderr, o.Cut = &se, res.StderrCut
	gufitrace.Note(ctx, append([]string{filepath.Join(e.GufiBin, "gufi_query")}, argv...), o)
	so := string(res.Stdout)
	failed := rerr != nil || res.ExitCode != 0 || res.Signal != ""
	if failed && so == "" {
		msg := se
		if len(msg) > 500 {
			msg = utf16String(utf16Units(msg)[:min(500, len(utf16Units(msg)))])
		}
		if msg == "" {
			if rerr != nil {
				msg = rerr.Error()
			} else {
				msg = "Command failed: gufi_query"
			}
		}
		return "", se, errors.New(msg)
	}
	return so, se, nil
}

// RunPlan is runPlan.
func (e *Engine) RunPlan(ctx context.Context, p *gufi.Planner, l libraries.Library, plan gufi.Plan) (QueryResult, error) {
	t0 := time.Now()
	so, se, err := e.run(ctx, l, plan.Args(), plan.Subtree)
	if err != nil {
		return QueryResult{}, err
	}
	if plan.Raw {
		if err := gufi.RawError(so, se); err != nil {
			return QueryResult{}, err
		}
	}
	r := p.Rows(plan, so)
	return QueryResult{Columns: r.Columns, Rows: r.Rows, ElapsedMS: time.Since(t0).Milliseconds(), Truncated: r.Truncated}, nil
}

func readsWords(plan gufi.Plan) bool {
	for _, s := range []string{plan.Stages.I, plan.Stages.E, plan.Stages.K, plan.Stages.J, plan.Stages.G} {
		if wordsRe.MatchString(s) {
			return true
		}
	}
	return false
}

func (e *Engine) queryGap(l libraries.Library, plan gufi.Plan) *libraries.Omission {
	gap := e.Reg.Gap(l, false, false)
	if gap != nil && !gap.Partial {
		return gap
	}
	building := l.Build != nil && (l.Build.State == "building" || l.Build.State == "updating")
	if readsWords(plan) && !l.Primary && !l.Caps.FullText && (building || e.Reg.Lacks(l, "fullText")) {
		reason := "its index has no extracted text, so text conditions match nothing there"
		if building {
			reason = "its text is being read, so text conditions match nothing there yet"
		}
		if gap != nil {
			g := *gap
			g.Reason += "; " + reason
			return &g
		}
		return &libraries.Omission{ID: l.ID, Label: l.Label, Reason: reason, Partial: true}
	}
	return gap
}

type mergeOrd struct {
	col     int
	numeric bool
	desc    bool
	limit   int
	groupBy int // -1: none
	sum     []int
}

func clampNum(v any, d, lo, hi int) int {
	f := d
	switch x := v.(type) {
	case float64:
		if x != 0 {
			f = int(x)
		}
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil && n != 0 {
			f = int(n)
		}
	case bool:
		if x {
			f = 1
		}
	}
	return min(max(f, lo), hi)
}

func mergeOrder(body []byte) *mergeOrd {
	var b struct {
		N       any `json:"n"`
		Canned  any `json:"canned"`
		Builder *struct {
			Limit   any `json:"limit"`
			Dir     any `json:"dir"`
			Source  any `json:"source"`
			Sort    any `json:"sort"`
			GroupBy any `json:"groupBy"`
		} `json:"builder"`
	}
	json.Unmarshal(body, &b)
	n := clampNum(b.N, 25, 1, gufi.MaxRows)
	switch b.Canned {
	case "largest":
		return &mergeOrd{1, true, true, n, -1, nil}
	case "newest", "recent":
		return &mergeOrd{2, false, true, n, -1, nil}
	case "oldest":
		return &mergeOrd{2, false, false, n, -1, nil}
	case "big_directories":
		return &mergeOrd{2, true, true, n, -1, nil}
	case "by_extension":
		return &mergeOrd{2, true, true, n, 0, []int{1, 2}}
	}
	bb := b.Builder
	if bb == nil {
		return nil
	}
	limit := clampNum(bb.Limit, 50, 1, gufi.MaxRows)
	desc := bb.Dir != "asc"
	byFiles := 2
	if bb.Sort == "files" {
		byFiles = 1
	}
	switch {
	case bb.Source == "directories":
		return &mergeOrd{byFiles, true, desc, limit, -1, nil}
	case bb.GroupBy == "extension" || bb.GroupBy == "owner":
		return &mergeOrd{byFiles, true, desc, limit, 0, []int{1, 2}}
	case bb.GroupBy == "directory":
		return &mergeOrd{byFiles, true, desc, limit, -1, nil}
	case bb.Sort == "name":
		return &mergeOrd{0, false, desc, limit, -1, nil}
	case bb.Sort == "modified":
		return &mergeOrd{2, false, desc, limit, -1, nil}
	}
	return &mergeOrd{1, true, desc, limit, -1, nil}
}

type tagged struct {
	id  string
	row []string
}

func numOr0(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		if strings.TrimSpace(s) == "" {
			return 0
		}
		return 0
	}
	return f
}

func cell(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return "undefined"
}

func mergeRows(rows []tagged, o *mergeOrd) []tagged {
	out := rows
	if o != nil && o.groupBy >= 0 {
		var order []string
		sums := map[string]*tagged{}
		for _, t := range out {
			k := cell(t.row, o.groupBy)
			cur := sums[k]
			if cur == nil {
				sums[k] = &tagged{"all", append([]string(nil), t.row...)}
				order = append(order, k)
				continue
			}
			for _, c := range o.sum {
				for len(cur.row) <= c {
					cur.row = append(cur.row, "")
				}
				cur.row[c] = jsNum(numOr0(cur.row[c]) + numOr0(cell(t.row, c)))
			}
		}
		out = nil
		for _, k := range order {
			out = append(out, *sums[k])
		}
	}
	if o != nil {
		c := o.col
		s := append([]tagged(nil), out...)
		sort.SliceStable(s, func(i, j int) bool {
			var x float64
			if o.numeric {
				x = numOr0(cell(s[i].row, c)) - numOr0(cell(s[j].row, c))
			} else {
				x = float64(strings.Compare(cell(s[i].row, c), cell(s[j].row, c)))
			}
			if o.desc {
				return x > 0
			}
			return x < 0
		})
		if len(s) > o.limit {
			s = s[:o.limit]
		}
		out = s
	}
	return out
}

func jsNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// QueryScope is queryScope: a query on one library, or merged across
// several. gufi_vt is not used: across several libraries the rows are
// merged here (engine "merge").
func (e *Engine) QueryScope(ctx context.Context, body []byte, viewer libraries.Viewer, label string) (*QueryResult, error) {
	libs, omittedList, err := e.Reg.Scope(libraryParam(body), viewer, label)
	if err != nil {
		return nil, err
	}
	om := &libraries.Omissions{}
	om.Set(omittedList)
	if len(libs) == 1 {
		l := libs[0]
		if !l.Remote() && !e.GufiInstalled() {
			return nil, apierr.New(503, "GUFI is not installed here")
		}
		p, plan, err := e.planFor(ctx, l, body)
		if err != nil {
			return nil, err
		}
		if gap := e.queryGap(l, plan); gap != nil {
			if !gap.Partial {
				return nil, apierr.New(409, "%s cannot be queried: %s", gap.Label, gap.Reason)
			}
			om.Add(*gap)
		}
		r, err := e.RunPlan(ctx, p, l, plan)
		if err != nil {
			var kb *apierr.Error
			if errors.As(err, &kb) {
				if hint := libraries.FormatFailure(kb.Message, l.Format); hint != "" {
					return nil, apierr.New(kb.Status, "%s: %s", l.Label, hint)
				}
			}
			return nil, err
		}
		r.Omitted = om.Items()
		return &r, nil
	}
	t0 := time.Now()
	order := mergeOrder(body)
	type planned struct {
		l    libraries.Library
		p    *gufi.Planner
		plan gufi.Plan
	}
	var all, plans []planned
	for _, l := range libs {
		p, plan, err := e.planFor(ctx, l, body)
		if err != nil {
			return nil, err
		}
		all = append(all, planned{l, p, plan})
	}
	for _, x := range all {
		gap := e.queryGap(x.l, x.plan)
		if gap != nil {
			om.Add(*gap)
		}
		if gap == nil || gap.Partial {
			plans = append(plans, x)
		}
	}
	if len(plans) == 0 {
		cols := []string{"library"}
		if len(all) > 0 {
			cols = append(cols, all[0].plan.Columns...)
		}
		failed := []QueryFailure{}
		for _, o := range om.Items() {
			if !o.Partial {
				failed = append(failed, QueryFailure{o.Label, o.Reason})
			}
		}
		return &QueryResult{Columns: cols, Rows: [][]string{}, hasRowLibrary: true, Failed: &failed, Engine: "merge", Omitted: om.Items()}, nil
	}
	type part struct {
		rows []tagged
		err  string
		ok   bool
	}
	parts := make([]part, len(plans))
	done := make(chan struct{}, len(plans))
	for i, x := range plans {
		go func(i int, x planned) {
			defer func() { done <- struct{}{} }()
			if !x.l.Remote() && !(e.GufiInstalled() && exists(x.l.IndexRoot)) {
				parts[i] = part{err: "index not built yet"}
				return
			}
			r, err := e.RunPlan(ctx, x.p, x.l, x.plan)
			if err != nil {
				parts[i] = part{err: jsSlice(err.Error(), 300)}
				return
			}
			var rows []tagged
			for _, row := range r.Rows {
				rows = append(rows, tagged{x.l.ID, row})
			}
			parts[i] = part{rows: rows, ok: true}
		}(i, x)
	}
	for range plans {
		<-done
	}
	var rows []tagged
	failed := []QueryFailure{}
	for i, p := range parts {
		if p.ok {
			rows = append(rows, p.rows...)
		} else {
			failed = append(failed, QueryFailure{plans[i].l.Label, p.err})
		}
	}
	labels := map[string]string{}
	byLabel := map[string]libraries.Library{}
	for _, l := range libs {
		labels[l.ID] = l.Label
		byLabel[l.Label] = l
	}
	width := 1
	for _, t := range rows {
		width = max(width, len(t.row))
	}
	cols := []string{"library"}
	if plans[0].plan.Raw {
		for i := 1; i <= width; i++ {
			cols = append(cols, "col"+strconv.Itoa(i))
		}
	} else {
		cols = append(cols, plans[0].plan.Columns...)
	}
	merged := mergeRows(rows, order)
	for _, f := range failed {
		if l, ok := byLabel[f.Library]; ok {
			om.Add(libraries.Omission{ID: l.ID, Label: l.Label, Reason: libraries.FailureReason(l, errors.New(f.Error)), Partial: false})
		}
	}
	for _, o := range om.Items() {
		if o.Partial {
			continue
		}
		seen := false
		for _, f := range failed {
			seen = seen || f.Library == o.Label
		}
		if !seen {
			failed = append(failed, QueryFailure{o.Label, o.Reason})
		}
	}
	res := &QueryResult{Columns: cols, Rows: [][]string{}, hasRowLibrary: true, ElapsedMS: time.Since(t0).Milliseconds(),
		Truncated: len(merged) > gufi.MaxRows, Failed: &failed, Engine: "merge", Omitted: om.Items()}
	for i, t := range merged {
		if i >= gufi.MaxRows {
			break
		}
		name := labels[t.id]
		if t.id == "all" {
			name = "all"
		} else if name == "" {
			name = t.id
		}
		res.Rows = append(res.Rows, append([]string{name}, t.row...))
		res.RowLibraries = append(res.RowLibraries, t.id)
	}
	if len(plans) > 1 {
		res.MergeReason = e.mergeReason(ctx, plans[0].plan)
	}
	return res, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
