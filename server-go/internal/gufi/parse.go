package gufi

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// The parsing of gufi_query's output: gufi.ts's parseRows and walk rows,
// fuseRows, and query.ts's result shaping.

// ParseRows is parseRows: lines of Delim-separated cells, keeping rows
// with at least fields cells.
func ParseRows(stdout string, fields int) [][]string {
	var rows [][]string
	for _, l := range strings.Split(stdout, "\n") {
		if l == "" {
			continue
		}
		if r := strings.Split(l, Delim); len(r) >= fields {
			rows = append(rows, r)
		}
	}
	return rows
}

var headerRe = regexp.MustCompile(`(?:<!--[` + jsSpace + `]*)?(?:extracted[` + jsSpace + `]+)?(?:by[` + jsSpace + `]+)?downmark[` + jsSpace + `]+\d+(?:\.\d+)*(?:[` + jsSpace + `]*\(no OCR\))?[` + jsSpace + `]*(?:-->|[\x{2013}\x{2014}]>)[` + jsSpace + `]*`)

// WithoutProvenance is textCleanup.ts's withoutProvenance: the converter's
// provenance line off a snippet, whole or cut short.
func WithoutProvenance(text string) string {
	if !strings.Contains(text, "downmark") {
		return text
	}
	return headerRe.ReplaceAllString(text, "")
}

// Hit kinds, most exact first.
const (
	KindFTS    = "fts"
	KindName   = "name"
	KindPath   = "path"
	KindVector = "vector"
)

var kindOrder = []string{KindFTS, KindName, KindPath, KindVector}
var codeKind = map[string]string{"n": KindName, "p": KindPath, "f": KindFTS, "v": KindVector}

func prio(kind string) int {
	for i, k := range kindOrder {
		if k == kind {
			return i
		}
	}
	return -1
}

// WalkRow is one row of a one-pass walk.
type WalkRow struct {
	Kind    string  `json:"kind"`
	R       float64 `json:"r"`
	Path    string  `json:"path"`
	Name    string  `json:"name"`
	Size    float64 `json:"size"`
	Mtime   float64 `json:"mtime"`
	Snippet string  `json:"snippet"`
	Root    string  `json:"root"`
}

// orZero is Number(v) || 0.
func orZero(s string) float64 {
	if n := jsNumber(s); !math.IsNaN(n) {
		return n
	}
	return 0
}

// WalkRows is walkSearch's parsing: the walk's rows under printed (the
// roots as gufi_query printed them), each tagged with roots[i], the root
// it is under; a root inside another claims its own paths.
func WalkRows(stdout string, printed, roots []string) []WalkRow {
	type at struct{ p, root string }
	order := make([]at, len(printed))
	for i, p := range printed {
		order[i] = at{p, roots[i]}
	}
	sort.SliceStable(order, func(a, b int) bool { return jsLen(order[a].p) > jsLen(order[b].p) })
	var rows []WalkRow
	for _, r := range ParseRows(stdout, 7) {
		kind, ok := codeKind[r[0]]
		if !ok {
			continue
		}
		var hit *at
		for i := range order {
			if r[2] == order[i].p || strings.HasPrefix(r[2], order[i].p+"/") {
				hit = &order[i]
				break
			}
		}
		if hit == nil {
			continue
		}
		snippet := r[6]
		if kind == KindVector {
			snippet = PassageSnippet(snippet)
		}
		rel := ""
		if r[2] != hit.p {
			rel = r[2][len(hit.p)+1:]
		}
		rows = append(rows, WalkRow{Kind: kind, R: jsNumber(r[1]), Path: rel, Name: r[3], Size: orZero(r[4]), Mtime: orZero(r[5]), Snippet: WithoutProvenance(snippet), Root: hit.root})
	}
	return rows
}

// SearchHit is a fused search hit.
type SearchHit struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	Size    float64  `json:"size"`
	Mtime   float64  `json:"mtime"`
	Snippet string   `json:"snippet"`
	Score   float64  `json:"score"`
	Mode    string   `json:"mode"`
	Kinds   []string `json:"kinds,omitempty"`
	Rank    *float64 `json:"rank,omitempty"`
}

// jsLess is JavaScript's < on two strings: UTF-16 code unit order.
func jsLess(a, b string) bool {
	if !strings.ContainsFunc(a+b, func(r rune) bool { return r >= 0xe000 }) {
		return a < b
	}
	return compareUTF16(a, b) < 0
}

func compareUTF16(a, b string) int {
	ua, ub := utf16Units(a), utf16Units(b)
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return int(ua[i]) - int(ub[i])
		}
	}
	return len(ua) - len(ub)
}

// FuseRows is fuseRows: reciprocal rank fusion over walk rows. caps keeps
// each kind to its count; a kind absent from caps keeps all its rows.
func FuseRows(rows []WalkRow, caps map[string]int, limit int) []SearchHit {
	type key struct{ kind, path string }
	best := map[key]int{}
	var keys []key
	var kept []WalkRow
	for _, row := range rows {
		k := key{row.Kind, row.Path}
		if i, ok := best[k]; !ok {
			best[k] = len(kept)
			keys = append(keys, k)
			kept = append(kept, row)
		} else if row.R < kept[i].R {
			kept[i] = row
		}
	}
	byKind := map[string][]WalkRow{}
	for _, row := range kept {
		byKind[row.Kind] = append(byKind[row.Kind], row)
	}
	type file struct {
		score float64
		kinds []string
		rows  map[string]WalkRow
	}
	files := map[string]*file{}
	var paths []string
	for _, kind := range kindOrder {
		list := byKind[kind]
		sort.SliceStable(list, func(a, b int) bool {
			if list[a].R != list[b].R {
				return list[a].R < list[b].R
			}
			return jsLess(list[a].Path, list[b].Path)
		})
		n := len(list)
		if c, ok := caps[kind]; ok && c < n {
			n = max(c, 0)
		}
		for i, row := range list[:n] {
			f := files[row.Path]
			if f == nil {
				f = &file{rows: map[string]WalkRow{}}
				files[row.Path] = f
				paths = append(paths, row.Path)
			}
			f.score += 1 / float64(RRFK+i+1)
			f.kinds = append(f.kinds, kind)
			f.rows[kind] = row
		}
	}
	hits := make([]SearchHit, 0, len(paths))
	for _, p := range paths {
		f := files[p]
		mode := f.kinds[0]
		main := f.rows[mode]
		sized := main
		for _, k := range kindOrder {
			if r, ok := f.rows[k]; ok && (r.Size != 0 || r.Mtime != 0) {
				sized = r
				break
			}
		}
		snippet := f.rows[KindFTS].Snippet
		if snippet == "" {
			snippet = f.rows[KindVector].Snippet
		}
		rank := main.R
		hits = append(hits, SearchHit{Path: p, Name: main.Name, Size: sized.Size, Mtime: sized.Mtime, Snippet: snippet, Score: f.score, Mode: mode, Kinds: f.kinds, Rank: &rank})
	}
	sort.SliceStable(hits, func(a, b int) bool {
		x, y := hits[a], hits[b]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if prio(x.Mode) != prio(y.Mode) {
			return prio(x.Mode) < prio(y.Mode)
		}
		return jsLess(x.Path, y.Path)
	})
	if limit < len(hits) {
		hits = hits[:max(limit, 0)]
	}
	return hits
}

// QueryResult is a query's answer.
type QueryResult struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	ElapsedMS int64      `json:"elapsedMs"`
	Truncated bool       `json:"truncated"`
}

// StripIndexPrefix is stripIndexPrefix: a cell relative to the index.
func (p *Planner) StripIndexPrefix(cell string) string {
	return strings.TrimPrefix(cell, p.rootPrefix())
}

// Rows is query.ts's parse and its raw counterpart: the rows of a plan's
// output, cells relative to the index, at most MaxRows. A raw plan's
// columns are col1 to colN for its widest row.
func (p *Planner) Rows(plan Plan, stdout string) QueryResult {
	fields := len(plan.Columns)
	var rows [][]string
	for _, l := range strings.Split(stdout, "\n") {
		if l == "" {
			continue
		}
		r := strings.Split(l, Delim)
		for i := range r {
			r[i] = p.StripIndexPrefix(r[i])
		}
		if plan.Raw || len(r) >= fields {
			rows = append(rows, r)
		}
	}
	cols := plan.Columns
	if plan.Raw {
		width := 1
		if len(rows) > 0 {
			width = 0
			for _, r := range rows {
				width = max(width, len(r))
			}
		}
		cols = make([]string, width)
		for i := range cols {
			cols[i] = "col" + itoa(i+1)
		}
	}
	res := QueryResult{Columns: cols, Rows: rows, Truncated: len(rows) > MaxRows}
	if res.Rows == nil {
		res.Rows = [][]string{}
	}
	if res.Truncated {
		res.Rows = res.Rows[:MaxRows]
	}
	return res
}

var dbPathRe = regexp.MustCompile(`: /[^` + jsSpace + `"]+db\.db`)
var errorWord = regexp.MustCompile(`(?i)Error`)

// RawError is rawRun's error for raw SQL that printed nothing and failed:
// its first error line without the database's path, or nil.
func RawError(stdout, stderr string) error {
	if strings.Trim(stdout, "\n") != "" || !errorWord.MatchString(stderr) {
		return nil
	}
	first := "query failed"
	for _, l := range strings.Split(stderr, "\n") {
		if errorWord.MatchString(l) {
			first = l
			break
		}
	}
	first = dbPathRe.ReplaceAllString(first, "")
	if u := utf16Units(first); len(u) > 300 {
		first = utf16String(u[:300])
	}
	return newBadRequest(first)
}
