package gufi

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// The query plans of server/src/query.ts: the canned corpus queries, the
// structured builder, and raw SQL with its guardrails, each compiled to
// gufi_query's stages. Ported line by line; golden_test.go holds the
// stages and the command line to querySql's.

// Delim is the column delimiter gufi_query is given.
const Delim = "\x1e"

// MaxRows is the most rows a query returns.
const MaxRows = 2000

// Stages are gufi_query's SQL stages: -I and -K create a table per thread
// and the result's, -E runs in every directory, -J merges, -G orders.
type Stages struct {
	I string `json:"I,omitempty"`
	E string `json:"E"`
	K string `json:"K,omitempty"`
	J string `json:"J,omitempty"`
	G string `json:"G,omitempty"`
}

// Plan is one query's plan for an index.
type Plan struct {
	Stages  Stages   `json:"stages"`
	Columns []string `json:"columns"`
	Subtree string   `json:"subtree,omitempty"`
	// Raw SQL: per-directory rows with no aggregation and no known order.
	Raw bool `json:"raw,omitempty"`
}

// Stage is one stage as querySql reports it.
type Stage struct {
	Flag string `json:"flag"`
	SQL  string `json:"sql"`
}

// List is the stages in the order gufi_query runs them.
func (p Plan) List() []Stage {
	s := p.Stages
	if p.Raw {
		return []Stage{{"-E", s.E}}
	}
	var out []Stage
	for _, x := range []Stage{{"-I", s.I}, {"-K", s.K}, {"-E", s.E}, {"-J", s.J}, {"-G", s.G}} {
		if x.SQL != "" {
			out = append(out, x)
		}
	}
	return out
}

// Args is planArgs: gufi_query's arguments ahead of the index path.
func (p Plan) Args() []string {
	s := p.Stages
	if p.Raw {
		return []string{"-n", "8", "-d", Delim, "-E", s.E}
	}
	return []string{"-n", "8", "-d", Delim, "-a", "1", "-I", s.I, "-K", s.K, "-E", s.E, "-J", s.J, "-G", s.G}
}

// LabelEntry is one path's labels, in the order Node's Map holds them.
type LabelEntry struct {
	Rel  string
	Tags []string
}

// LabelMaps are what a label condition is compiled from: folders' labels
// (inherited below them) and, for a library the Studio does not write, the
// labels it keeps for its paths. The caller reads them (tags.ts,
// libraryLabels.ts); the planner only compiles them.
type LabelMaps struct {
	Dirs  []LabelEntry
	Files []LabelEntry
}

// Planner compiles queries for one index.
type Planner struct {
	// IndexRoot is the index as rpath() prints it: the local path, or a
	// remote index's own path on its cluster.
	IndexRoot string
	// Labels for label conditions.
	Labels LabelMaps
	// Now is the time in seconds the canned "recent" and relative dates
	// count from; nil is the clock.
	Now func() int64
}

func (p *Planner) now() int64 {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().Unix()
}

// RootPath is rootPath(): path.resolve of a local index.
func (p *Planner) RootPath() string {
	if abs, err := filepath.Abs(p.IndexRoot); err == nil {
		return abs
	}
	return filepath.Clean(p.IndexRoot)
}

func (p *Planner) rootPrefix() string { return p.RootPath() + "/" }

var edgeSlashes = regexp.MustCompile(`^/+|/+$`)

// CleanSubtree is cleanSubtree: a subtree without its edge slashes, or an
// error for one that climbs out of the index.
func CleanSubtree(sub string) (string, error) {
	c := edgeSlashes.ReplaceAllString(sub, "")
	if c == "" {
		return "", nil
	}
	for _, part := range strings.Split(c, "/") {
		if part == ".." {
			return "", apierr.New(400, "scope escapes the index")
		}
	}
	return c, nil
}

// ScopePath is scopePath: the directory gufi_query starts at.
func ScopePath(root, sub string) (string, error) {
	c, err := CleanSubtree(sub)
	if err != nil || c == "" {
		return root, err
	}
	base, _ := filepath.Abs(root)
	abs := filepath.Join(base, c)
	if !strings.HasPrefix(abs, base) {
		return "", apierr.New(400, "scope escapes the index")
	}
	return abs, nil
}

func aggPlan(schema, perDir, final string, columns []string, subtree string) Plan {
	return Plan{
		Stages:  Stages{I: "CREATE TABLE t " + schema + ";", E: perDir, K: "CREATE TABLE agg " + schema + ";", J: "INSERT INTO agg SELECT * FROM t;", G: final},
		Columns: columns, Subtree: subtree,
	}
}

const pathExpr = `rpath(sname, sroll)||'/'||vrpentries.name`

// num formats a number as JavaScript's template literals do.
func num(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 0) {
		if f > 0 {
			return "Infinity"
		}
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// CannedParams are a canned query's parameters.
type CannedParams struct {
	N       float64
	Days    float64
	Subtree string
}

var cannedFiles = "(path TEXT, size INT64, mtime INT64)"

// CannedIDs are the canned queries, in Node's order.
var CannedIDs = []string{"largest", "newest", "oldest", "recent", "by_extension", "big_directories"}

// Canned is a canned corpus query's plan.
func (p *Planner) Canned(id string, c CannedParams) (Plan, error) {
	n := num(c.N)
	files := []string{"path", "bytes", "modified"}
	switch id {
	case "largest":
		return aggPlan(cannedFiles, fmt.Sprintf("INSERT INTO t SELECT %s, size, mtime FROM vrpentries ORDER BY size DESC LIMIT %s;", pathExpr, n),
			fmt.Sprintf("SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY size DESC LIMIT %s;", n), files, c.Subtree), nil
	case "newest":
		return aggPlan(cannedFiles, fmt.Sprintf("INSERT INTO t SELECT %s, size, mtime FROM vrpentries ORDER BY mtime DESC LIMIT %s;", pathExpr, n),
			fmt.Sprintf("SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT %s;", n), files, c.Subtree), nil
	case "oldest":
		return aggPlan(cannedFiles, fmt.Sprintf("INSERT INTO t SELECT %s, size, mtime FROM vrpentries ORDER BY mtime ASC LIMIT %s;", pathExpr, n),
			fmt.Sprintf("SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime ASC LIMIT %s;", n), files, c.Subtree), nil
	case "recent":
		// The cutoff is computed here: GUFI's own strftime(format, seconds)
		// takes the place of SQLite's.
		cut := float64(p.now()) - c.Days*86400
		return aggPlan(cannedFiles, fmt.Sprintf("INSERT INTO t SELECT %s, size, mtime FROM vrpentries WHERE mtime > %s ORDER BY mtime DESC LIMIT %s;", pathExpr, num(cut), n),
			fmt.Sprintf("SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT %s;", n), files, c.Subtree), nil
	case "by_extension":
		return aggPlan("(ext TEXT, cnt INT64, bytes INT64)",
			"INSERT INTO t SELECT lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END), count(*), sum(size) FROM vrpentries GROUP BY 1;",
			fmt.Sprintf("SELECT ext, sum(cnt), sum(bytes) FROM agg GROUP BY ext ORDER BY sum(bytes) DESC LIMIT %s;", n), []string{"extension", "files", "bytes"}, c.Subtree), nil
	case "big_directories":
		return aggPlan("(path TEXT, files INT64, bytes INT64)", "INSERT INTO t SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary;",
			fmt.Sprintf("SELECT path, files, bytes FROM agg ORDER BY bytes DESC LIMIT %s;", n), []string{"directory", "files", "bytes"}, c.Subtree), nil
	}
	return Plan{}, apierr.New(400, "unknown canned query: %s", id)
}

// BuilderFilter is one condition of the builder.
type BuilderFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// BuilderPayload is the structured builder's form.
type BuilderPayload struct {
	Source  string          `json:"source"`
	Filters json.RawMessage `json:"filters"`
	Match   string          `json:"match"`
	GroupBy string          `json:"groupBy"`
	Sort    string          `json:"sort"`
	Dir     string          `json:"dir"`
	Limit   any             `json:"limit"`
	Subtree string          `json:"subtree"`
}

func esc(v string) string { return strings.ReplaceAll(v, "'", "''") }

const extExpr = `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END)`
const ownerExpr = "uidtouser(uid)"

var fileFields = map[string]string{
	"name": "vrpentries.name", "path": pathExpr, "extension": extExpr, "owner": ownerExpr,
	"size": "size", "modified": "mtime", "text": "text", "label": "label",
}

var dirFields = map[string]string{"name": "vrsummary.name", "owner": ownerExpr, "size": "totsize", "modified": "mtime"}

var fieldOps = map[string][]string{
	"name": {"contains", "not_contains", "eq", "gt", "lt"}, "path": {"contains", "not_contains", "eq"},
	"extension": {"contains", "not_contains", "eq"}, "owner": {"contains", "not_contains", "eq"},
	"size": {"eq", "gt", "lt"}, "modified": {"eq", "gt", "lt"}, "text": {"match"}, "label": {"eq", "contains"},
}

var opWords = map[string]string{"contains": "contains", "not_contains": "does not contain", "eq": "equals", "gt": "greater than", "lt": "less than", "match": "matches"}

var sizeUnits = map[string]float64{"": 1, "b": 1, "k": 1024, "kb": 1024, "kib": 1024, "m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30, "t": 1 << 40, "tb": 1 << 40, "tib": 1 << 40}

var sizeRe = regexp.MustCompile(`(?i)^[` + jsSpace + `]*(\d+(?:\.\d+)?)[` + jsSpace + `]*([a-z]*)[` + jsSpace + `]*$`)

// ParseSize is a size in bytes: a number, or one with a unit, in powers
// of 1024.
func ParseSize(v string) (float64, error) {
	m := sizeRe.FindStringSubmatch(v)
	if m != nil {
		if unit, ok := sizeUnits[strings.ToLower(m[2])]; ok {
			f, _ := strconv.ParseFloat(m[1], 64)
			return math.Floor(f*unit + 0.5), nil
		}
	}
	return 0, apierr.New(400, `size takes a number of bytes or a size such as 10 MB, not "%s"`, v)
}

var agoRe = regexp.MustCompile(`(?i)^(\d+)[` + jsSpace + `]*(h|hours?|d|days?|w|weeks?)([` + jsSpace + `]+ago)?$`)
var dateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$`)

// ParseWhen is the span of time a modified value names, in seconds (UTC).
func ParseWhen(v string, now int64) (start, end float64, err error) {
	s := JSTrim(v)
	if m := agoRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		mult := 7 * 86400.0
		switch strings.ToLower(m[2][:1]) {
		case "h":
			mult = 3600
		case "d":
			mult = 86400
		}
		t := float64(now) - n*mult
		return t, t, nil
	}
	if m := dateRe.FindStringSubmatchIndex(s); m != nil {
		at := func(i int) (int, bool) {
			if m[2*i] < 0 {
				return 0, false
			}
			n, _ := strconv.Atoi(s[m[2*i]:m[2*i+1]])
			return n, true
		}
		y, _ := at(1)
		mo, _ := at(2)
		d, _ := at(3)
		h, _ := at(4)
		mi, hasMin := at(5)
		se, hasSec := at(6)
		// Date.UTC reads years 0 to 99 as 1900 to 1999, which the check below
		// then refuses.
		t := time.Date(y, time.Month(mo), d, h, mi, se, 0, time.UTC)
		if y >= 100 && t.Year() == y && int(t.Month()) == mo && t.Day() == d {
			st := float64(t.Unix())
			span := 86400.0
			if hasSec {
				span = 1
			} else if hasMin {
				span = 60
			}
			return st, st + span, nil
		}
	}
	return 0, 0, apierr.New(400, `modified takes a date such as 2026-01-01, or a time ago such as 7 days, not "%s"`, v)
}

var leadDot = regexp.MustCompile(`^\.`)
var leadSlashes = regexp.MustCompile(`^/+`)
var likeEsc = regexp.MustCompile(`([%_])`)
var wsSplit = regexp.MustCompile(`[` + jsSpace + `]+`)

// jsString is String(v) for a JSON value.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return num(x)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

var decimalRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
var radixRe = regexp.MustCompile(`^0([xXoObB])([0-9a-fA-F]+)$`)

// jsNumber is Number(v) for a JSON value.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case []any:
		return jsNumber(jsString(x))
	case string:
		t := JSTrim(x)
		switch {
		case t == "":
			return 0
		case t == "Infinity" || t == "+Infinity":
			return math.Inf(1)
		case t == "-Infinity":
			return math.Inf(-1)
		case decimalRe.MatchString(t):
			f, _ := strconv.ParseFloat(t, 64)
			return f
		}
		if m := radixRe.FindStringSubmatch(t); m != nil {
			base := map[byte]int{'x': 16, 'o': 8, 'b': 2}[m[1][0]|0x20]
			if u, err := strconv.ParseUint(m[2], base, 64); err == nil {
				return float64(u)
			}
		}
	}
	return math.NaN()
}

// truthy is a JSON value's truth in JavaScript.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// clampOr is Math.min(Math.max(Number(v) || d, lo), hi).
func clampOr(v any, d, lo, hi float64) float64 {
	n := jsNumber(v)
	if n == 0 || math.IsNaN(n) {
		n = d
	}
	return math.Min(math.Max(n, lo), hi)
}

func (p *Planner) filterSQL(f BuilderFilter, labels LabelMaps, fields map[string]string) (string, error) {
	field, ok := fields[f.Field]
	if !ok {
		return "", apierr.New(400, "unknown field: %s", f.Field)
	}
	ops := fieldOps[f.Field]
	allowed := false
	for _, o := range ops {
		if o == f.Op {
			allowed = true
		}
	}
	if !allowed {
		words := make([]string, len(ops))
		for i, o := range ops {
			words[i] = opWords[o]
		}
		list := words[0]
		if len(words) > 1 {
			list = strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
		}
		w, ok := opWords[f.Op]
		if !ok {
			w = f.Op
		}
		return "", apierr.New(400, `%s takes %s; "%s" does not apply to it`, f.Field, list, w)
	}
	v := jsString(f.Value)
	if f.Field == "extension" {
		v = strings.ToLower(leadDot.ReplaceAllString(v, ""))
	}
	switch f.Field {
	case "label":
		want := strings.ToLower(v)
		pat := "%" + esc(want) + "%"
		if f.Op == "eq" {
			pat = "%," + esc(want) + ",%"
		}
		match := func(tags []string) bool {
			for _, t := range tags {
				lt := strings.ToLower(t)
				if (f.Op == "eq" && lt == want) || (f.Op != "eq" && strings.Contains(lt, want)) {
					return true
				}
			}
			return false
		}
		own := fmt.Sprintf("EXISTS (SELECT 1 FROM xattrs_pwd xt WHERE xt.inode = vrpentries.inode AND CAST(xt.name AS TEXT) = 'user.studio.tags' AND (','||lower(CAST(xt.value AS TEXT))||',') LIKE '%s')", pat)
		root := p.RootPath()
		parts := []string{own}
		for _, d := range labels.Dirs {
			if d.Rel != "" && match(d.Tags) {
				parts = append(parts, fmt.Sprintf(`(%s) LIKE '%s/%%' ESCAPE '\'`, pathExpr, likeEsc.ReplaceAllString(esc(root+"/"+d.Rel), `\$1`)))
			}
		}
		var kept []string
		for _, e := range labels.Files {
			if match(e.Tags) {
				if e.Rel == "" {
					return "(1=1)", nil
				}
				kept = append(kept, e.Rel)
			}
		}
		if len(kept) > 5000 {
			kept = kept[:5000]
		}
		if len(kept) > 0 {
			in := make([]string, len(kept))
			for i, rel := range kept {
				in[i] = "'" + esc(root+"/"+rel) + "'"
			}
			parts = append(parts, fmt.Sprintf("(%s) IN (%s)", pathExpr, strings.Join(in, ", ")))
		}
		return "(" + strings.Join(parts, " OR ") + ")", nil
	case "text":
		var terms []string
		for _, t := range wsSplit.Split(v, -1) {
			if t = quoteChars.ReplaceAllString(t, ""); t != "" {
				terms = append(terms, t)
			}
		}
		if len(terms) > 8 {
			terms = terms[:8]
		}
		if len(terms) == 0 {
			return "", apierr.New(400, "empty text match")
		}
		qs := make([]string, len(terms))
		for i, t := range terms {
			qs[i] = `"` + t + `"`
		}
		return fmt.Sprintf("vrpentries.inode IN (SELECT CAST(tinode AS TEXT) FROM words WHERE wordf MATCH '%s')", esc(strings.Join(qs, " AND "))), nil
	case "size":
		n, err := ParseSize(v)
		if err != nil {
			return "", err
		}
		op := map[string]string{"eq": "=", "gt": ">", "lt": "<"}[f.Op]
		return fmt.Sprintf("%s %s %s", field, op, num(n)), nil
	case "modified":
		start, end, err := ParseWhen(v, p.now())
		if err != nil {
			return "", err
		}
		switch f.Op {
		case "gt":
			return fmt.Sprintf("%s >= %s", field, num(end)), nil
		case "lt":
			return fmt.Sprintf("%s < %s", field, num(start)), nil
		}
		if end > start {
			return fmt.Sprintf("(%s >= %s AND %s < %s)", field, num(start), field, num(end)), nil
		}
		return fmt.Sprintf("%s = %s", field, num(start)), nil
	}
	like := func(expr string) string {
		return fmt.Sprintf("%s LIKE '%%%s%%'", expr, esc(likeMeta.ReplaceAllString(v, "")))
	}
	if f.Field == "path" {
		rel := fmt.Sprintf("substr(%s, %d)", field, utf8.RuneCountInString(p.rootPrefix())+1)
		if f.Op == "eq" {
			return fmt.Sprintf("%s = '%s'", field, esc(p.rootPrefix()+leadSlashes.ReplaceAllString(v, ""))), nil
		}
		if f.Op == "contains" {
			return like(rel), nil
		}
		return "NOT " + like(rel), nil
	}
	switch f.Op {
	case "contains":
		return like(field), nil
	case "not_contains":
		return "NOT " + like(field), nil
	case "eq":
		return fmt.Sprintf("%s = '%s'", field, esc(v)), nil
	case "gt":
		return fmt.Sprintf("%s > '%s'", field, esc(v)), nil
	case "lt":
		return fmt.Sprintf("%s < '%s'", field, esc(v)), nil
	}
	return "", apierr.New(400, "unknown operator: %s", f.Op)
}

func (p *Planner) whereSQL(filters []BuilderFilter, match string, labels LabelMaps, fields map[string]string) (string, error) {
	if len(filters) == 0 {
		return "", nil
	}
	parts := make([]string, len(filters))
	for i, f := range filters {
		s, err := p.filterSQL(f, labels, fields)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + s + ")"
	}
	join := " AND "
	if match == "any" {
		join = " OR "
	}
	return "WHERE " + strings.Join(parts, join), nil
}

// Builder is builderPlan: the structured builder's form as a plan.
func (p *Planner) Builder(b BuilderPayload) (Plan, error) {
	limit := num(clampOr(b.Limit, 50, 1, MaxRows))
	var filters []BuilderFilter
	if len(b.Filters) > 0 && b.Filters[0] == '[' {
		if err := json.Unmarshal(b.Filters, &filters); err != nil {
			return Plan{}, apierr.New(400, "filters must be a list of conditions")
		}
	}
	dir := "DESC"
	if b.Dir == "asc" {
		dir = "ASC"
	}
	if b.Source == "directories" {
		bad := b.GroupBy != "none"
		for _, f := range filters {
			if _, ok := dirFields[f.Field]; !ok {
				bad = true
			}
		}
		if bad {
			return Plan{}, apierr.New(400, "directories take name, owner, size and modified conditions, without grouping")
		}
		where, err := p.whereSQL(filters, b.Match, LabelMaps{}, dirFields)
		if err != nil {
			return Plan{}, err
		}
		sort := "bytes"
		if b.Sort == "files" {
			sort = "files"
		}
		return aggPlan("(path TEXT, files INT64, bytes INT64)", fmt.Sprintf("INSERT INTO t SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary %s;", where),
			fmt.Sprintf("SELECT path, files, bytes FROM agg ORDER BY %s %s LIMIT %s;", sort, dir, limit), []string{"directory", "files", "bytes"}, b.Subtree), nil
	}
	where, err := p.whereSQL(filters, b.Match, p.Labels, fileFields)
	if err != nil {
		return Plan{}, err
	}
	const from = "FROM vrpentries"
	if b.GroupBy == "extension" || b.GroupBy == "owner" {
		key, col := extExpr, "extension"
		if b.GroupBy == "owner" {
			key, col = ownerExpr, "owner"
		}
		sort := "sum(bytes)"
		if b.Sort == "files" {
			sort = "sum(cnt)"
		}
		return aggPlan("(k TEXT, cnt INT64, bytes INT64)", fmt.Sprintf("INSERT INTO t SELECT %s, count(*), sum(size) %s %s GROUP BY 1;", key, from, where),
			fmt.Sprintf("SELECT k, sum(cnt), sum(bytes) FROM agg GROUP BY k ORDER BY %s %s LIMIT %s;", sort, dir, limit), []string{col, "files", "bytes"}, b.Subtree), nil
	}
	if b.GroupBy == "directory" {
		sort := "bytes"
		if b.Sort == "files" {
			sort = "cnt"
		}
		return aggPlan("(path TEXT, cnt INT64, bytes INT64)", fmt.Sprintf("INSERT INTO t SELECT rpath(sname, sroll), count(*), sum(size) %s %s GROUP BY 1;", from, where),
			fmt.Sprintf("SELECT path, cnt, bytes FROM agg ORDER BY %s %s LIMIT %s;", sort, dir, limit), []string{"directory", "files", "bytes"}, b.Subtree), nil
	}
	sortCol := "size"
	switch b.Sort {
	case "name":
		sortCol = "path"
	case "modified":
		sortCol = "mtime"
	}
	perDirSort := sortCol
	if sortCol == "path" {
		perDirSort = "1"
	}
	return aggPlan(cannedFiles, fmt.Sprintf("INSERT INTO t SELECT %s, size, mtime %s %s ORDER BY %s %s LIMIT %s;", pathExpr, from, where, perDirSort, dir, limit),
		fmt.Sprintf("SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY %s %s LIMIT %s;", sortCol, dir, limit), []string{"path", "bytes", "modified"}, b.Subtree), nil
}

var selectRe = regexp.MustCompile(`(?i)^select[` + jsSpace + `]`)
var disallowed = regexp.MustCompile(`(?i);|\b(attach|detach|pragma|insert|update|delete|drop|create|alter|replace|vacuum|reindex)\b`)
var trailingSemi = regexp.MustCompile(`;[` + jsSpace + `]*$`)

// RawPlan is rawPlan: one read-only SELECT, run in every directory.
func RawPlan(sql, subtree string) (Plan, error) {
	cleaned := trailingSemi.ReplaceAllString(JSTrim(sql), "")
	if !selectRe.MatchString(cleaned) {
		return Plan{}, apierr.New(400, "only SELECT statements are allowed")
	}
	if disallowed.MatchString(cleaned) {
		return Plan{}, apierr.New(400, "statement contains a disallowed keyword")
	}
	return Plan{Stages: Stages{E: cleaned + ";"}, Columns: []string{}, Subtree: subtree, Raw: true}, nil
}

// PlanFor is planFor: one query's plan, whichever form it takes, from
// the request's JSON body (canned, sql, builder, n, days, subtree).
func (p *Planner) PlanFor(raw []byte) (Plan, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return Plan{}, &apierr.Validation{Message: "body must be an object"}
	}
	n := clampOr(body["n"], 25, 1, MaxRows)
	days := clampOr(body["days"], 7, 1, 3650)
	subtree, _ := body["subtree"].(string)
	if truthy(body["builder"]) {
		var b BuilderPayload
		if m, ok := body["builder"].(map[string]any); ok {
			enc, _ := json.Marshal(m)
			if err := json.Unmarshal(enc, &b); err != nil {
				return Plan{}, &apierr.Validation{Message: "builder fields have the wrong types"}
			}
		}
		return p.Builder(b)
	}
	if truthy(body["canned"]) {
		return p.Canned(jsString(body["canned"]), CannedParams{N: n, Days: days, Subtree: subtree})
	}
	if truthy(body["sql"]) {
		return RawPlan(jsString(body["sql"]), subtree)
	}
	return Plan{}, apierr.New(400, "builder, canned, or sql required")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_/.,:=@+%-]+$`)

// ShellQuote is gufiTrace.ts's shellQuote.
func ShellQuote(a string) string {
	if a == Delim {
		return `$'\x1e'`
	}
	if shellSafe.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// Command is planCommand for a local index: the gufi_query command line
// a shell takes.
func (p *Planner) Command(gufiBin string, plan Plan) (string, error) {
	scope, err := ScopePath(p.IndexRoot, plan.Subtree)
	if err != nil {
		return "", err
	}
	argv := append(append([]string{filepath.Join(gufiBin, "gufi_query")}, plan.Args()...), scope)
	for i, a := range argv {
		argv[i] = ShellQuote(a)
	}
	return strings.Join(argv, " "), nil
}
