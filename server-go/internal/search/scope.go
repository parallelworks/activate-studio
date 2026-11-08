package search

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// ScopedHit is a hit with its library (libraryScope.ts ScopedHit).
type ScopedHit struct {
	gufi.SearchHit
	Library HitLibrary `json:"library"`
	Tags    []string   `json:"tags,omitempty"`
}

// HitLibrary names a hit's library.
type HitLibrary struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ScopeOpts is searchScope's options.
type ScopeOpts struct {
	Semantic   bool
	NameLimit  int // 0: the default
	DeadlineMs int // 0: SEARCH_DEADLINE_MS
	Slow       *[]string
	Omitted    *libraries.Omissions
	Refine     *gufi.RefineSpec
}

// DeadlineMs is SEARCH_DEADLINE_MS.
func DeadlineMs() int {
	if n, err := strconv.ParseFloat(os.Getenv("STUDIO_SEARCH_DEADLINE_MS"), 64); err == nil && n != 0 && !math.IsNaN(n) {
		return int(n)
	}
	return 20_000
}

// StackLocal is STACK_LOCAL.
func StackLocal() bool { return os.Getenv("STUDIO_STACK_LOCAL") == "1" }

// ExternalPattern is EXTERNAL_DB_PATTERN || null.
func ExternalPattern() *string {
	v, ok := os.LookupEnv("STUDIO_EXTERNAL_DB_PATTERN")
	if !ok {
		v = `\.db$`
	}
	if v == "" {
		return nil
	}
	return &v
}

var (
	reNear   = regexp.MustCompile(`\bNEAR\s*\(`)
	reDist   = regexp.MustCompile(`,\s*\d+\s*\)`)
	reOps    = regexp.MustCompile(`\b(AND|OR|NOT)\b`)
	reCol    = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*\s*:`)
	rePunct  = regexp.MustCompile(`["()*^+{}]`)
	reSpaces = regexp.MustCompile(`\s+`)
)

// MeaningPhrase is refine.ts meaningPhrase.
func MeaningPhrase(text string) string {
	s := reNear.ReplaceAllString(text, "(")
	s = reDist.ReplaceAllString(s, ")")
	s = reOps.ReplaceAllString(s, " ")
	// JS \b before a letter: only at a word start.
	s = colStart(s)
	s = rePunct.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// colStart takes out column filters (`name:`), each at a word boundary.
func colStart(s string) string {
	locs := reCol.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		if l[0] > 0 && isWordByte(s[l[0]-1]) {
			continue
		}
		b.WriteString(s[last:l[0]])
		b.WriteString(" ")
		last = l[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// RefineOf is refine.ts refineOf.
func RefineOf(name, text, match string) *gufi.RefineSpec {
	n := jsSlice(gufi.JSTrim(name), 500)
	t := jsSlice(gufi.JSTrim(text), 500)
	if n == "" && t == "" {
		return nil
	}
	m := "any"
	if strings.ToLower(match) == "all" {
		m = "all"
	}
	return &gufi.RefineSpec{Name: n, Text: t, Match: m}
}

// NamePathMatches is refine.ts namePathMatches.
func NamePathMatches(name, rel string) bool {
	groups := gufi.ParseNameFilter(name)
	if len(groups) == 0 {
		return true
	}
	parts := strings.Split(rel, "/")
	file := parts[len(parts)-1]
	folders := strings.ToLower(strings.Join(parts[:len(parts)-1], "/"))
	like := func(s, t string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(t)) }
	glob := func(s, g string) bool {
		re := "(?is)^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(g)) + "$"
		ok, _ := regexp.MatchString(re, s)
		return ok
	}
	one := func(kind, text string) bool {
		if kind == "glob" {
			target := file
			if strings.Contains(text, "/") {
				target = rel
			}
			return glob(target, text)
		}
		return like(file, text) || like(folders, text)
	}
	for _, g := range groups {
		all := true
		for _, t := range g {
			m := one(t.Kind, t.Text)
			if t.Neg {
				m = !m
			}
			if !m {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// withoutGaps is withoutGaps.
func (e *Engine) withoutGaps(libs []libraries.Library, o ScopeOpts) []libraries.Library {
	gone := map[string]bool{}
	for _, l := range libs {
		gap := e.Reg.Gap(l, o.Refine == nil || o.Refine.Text != "", o.Semantic)
		if gap == nil {
			continue
		}
		o.Omitted.Add(*gap)
		if !gap.Partial {
			gone[l.ID] = true
		}
	}
	if len(gone) == 0 {
		return libs
	}
	var out []libraries.Library
	for _, l := range libs {
		if !gone[l.ID] {
			out = append(out, l)
		}
	}
	return out
}

const sep = "\x01"

func deadlineText(ms int) string {
	if ms >= 1000 {
		return fmt.Sprintf("%d s", int(math.Round(float64(ms)/1000)))
	}
	return fmt.Sprintf("%d ms", ms)
}

// SearchScope is searchScope, without the gufi_vt form: the route hands a
// search Settings sends to gufi_vt to Node while Node runs.
func (e *Engine) SearchScope(ctx context.Context, libs []libraries.Library, q string, limit int, o ScopeOpts) []ScopedHit {
	if o.Omitted == nil {
		o.Omitted = &libraries.Omissions{}
	}
	refine := o.Refine
	libs = e.withoutGaps(libs, o)
	nameLimit := o.NameLimit
	if nameLimit == 0 {
		nameLimit = min(max(10, limit/5), 50)
	}
	vecLimit := min(limit, 10)
	caps := map[string]int{"name": nameLimit, "path": nameLimit, "fts": limit * 3, "vector": vecLimit}
	total := limit
	if refine != nil {
		caps["fts"] = limit
		total = nameLimit*2 + limit + vecLimit
	}
	keep := func(rel string) bool {
		return refine == nil || refine.Match != "all" || refine.Text == "" || refine.Name == "" || NamePathMatches(refine.Name, rel)
	}
	phrase := q
	if refine != nil {
		phrase = MeaningPhrase(refine.Text)
	}
	failed := func(l libraries.Library, err error, partial bool, what string) {
		o.Omitted.Add(libraries.Omission{ID: l.ID, Label: l.Label, Reason: what + libraries.FailureReason(l, err), Partial: partial})
	}
	wantVec := func(l libraries.Library) bool { return o.Semantic && e.Reg.MeaningSearched(l) }
	external := ExternalPattern()
	var vectors []gufi.QueryVector
	if o.Semantic && phrase != "" {
		any := external != nil
		for _, l := range libs {
			any = any || wantVec(l)
		}
		if any {
			vectors = e.QueryVectors(ctx, phrase)
		}
	}
	limitsFor := func(words, vec bool) gufi.WalkLimits {
		v := 0
		if len(vectors) > 0 {
			v = vecLimit
		}
		return gufi.WalkLimits{Names: nameLimit, Paths: nameLimit, Words: limit * 3, WordsPerDir: limit,
			Vectors: v, VectorsPerDir: 3, StudioText: words, StudioVectors: vec, External: external}
	}
	// The host each remote library reaches, so libraries of one host share a walk.
	hostKeys := map[string]*string{}
	{
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, l := range libs {
			if !l.Remote() {
				continue
			}
			wg.Add(1)
			go func(l libraries.Library) {
				defer wg.Done()
				k, err := e.RemoteHostKey(l.IndexRoot)
				mu.Lock()
				if err == nil {
					hostKeys[l.ID] = &k
				} else {
					hostKeys[l.ID] = nil
				}
				mu.Unlock()
			}(l)
		}
		wg.Wait()
	}
	var order []string
	groups := map[string][]libraries.Library{}
	for _, l := range libs {
		words, vec := e.Reg.WordsSearched(l), wantVec(l) && len(vectors) > 0
		host := "local"
		shared := StackLocal()
		if l.Remote() {
			h := hostKeys[l.ID]
			shared = h != nil
			if h != nil {
				host = *h
			}
		}
		key := "one\x00" + l.ID
		if shared {
			key = fmt.Sprintf("%s\x00%v\x00%v", host, words, vec)
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], l)
	}
	deadline := 0
	if len(libs) > 1 {
		deadline = o.DeadlineMs
		if deadline == 0 {
			deadline = DeadlineMs()
		}
	}
	var slowMu sync.Mutex
	late := func(ls []libraries.Library) {
		for _, l := range ls {
			if o.Slow != nil {
				slowMu.Lock()
				*o.Slow = append(*o.Slow, l.Label)
				slowMu.Unlock()
			}
			o.Omitted.Add(libraries.Omission{ID: l.ID, Label: l.Label, Reason: "did not answer within " + deadlineText(deadline), Partial: false})
		}
	}
	results := make([][]gufi.WalkRow, len(order))
	var wg sync.WaitGroup
	for gi, key := range order {
		members := groups[key]
		wg.Add(1)
		go func(gi int, members []libraries.Library) {
			defer wg.Done()
			words, vec := e.Reg.WordsSearched(members[0]), wantVec(members[0]) && len(vectors) > 0
			done := make(chan []gufi.WalkRow, 1)
			wctx := ctx
			go func() {
				var roots []string
				byRoot := map[string][]libraries.Library{}
				for _, l := range members {
					if _, ok := byRoot[l.IndexRoot]; !ok {
						roots = append(roots, l.IndexRoot)
					}
					byRoot[l.IndexRoot] = append(byRoot[l.IndexRoot], l)
				}
				var vs []gufi.QueryVector
				if vec {
					vs = vectors
				}
				raw, err := e.WalkSearch(wctx, q, limitsFor(words, vec), roots, vs, refine)
				var rows []gufi.WalkRow
				if err != nil {
					for _, l := range members {
						failed(l, err, false, "")
					}
				} else {
					for _, r := range raw {
						for _, l := range byRoot[r.Root] {
							c := r
							c.Path = l.ID + sep + r.Path
							rows = append(rows, c)
						}
					}
				}
				// Meaning for a library whose walk could not carry it.
				for _, l := range members {
					if !wantVec(l) || l.Remote() {
						continue
					}
					if vec {
						has := false
						for _, r := range rows {
							if r.Kind == "vector" && strings.HasPrefix(r.Path, l.ID+sep) {
								has = true
								break
							}
						}
						if has {
							continue
						}
					}
					hits, err := e.SearchVector(wctx, phrase, vecLimit, l.IndexRoot)
					if err != nil {
						failed(l, err, true, "not searched by meaning: ")
						continue
					}
					for _, h := range hits {
						if keep(h.Path) {
							rows = append(rows, gufi.WalkRow{Kind: "vector", R: h.Score, Path: l.ID + sep + h.Path, Name: h.Name, Size: h.Size, Mtime: h.Mtime, Snippet: h.Snippet, Root: l.IndexRoot})
						}
					}
				}
				done <- rows
			}()
			if deadline == 0 {
				results[gi] = <-done
				return
			}
			t := time.NewTimer(time.Duration(deadline) * time.Millisecond)
			defer t.Stop()
			select {
			case r := <-done:
				results[gi] = r
			case <-t.C:
				late(members)
			}
		}(gi, members)
	}
	wg.Wait()
	var rows []gufi.WalkRow
	for _, r := range results {
		rows = append(rows, r...)
	}
	found := func(kind, id string) bool {
		for _, r := range rows {
			if r.Kind == kind && strings.HasPrefix(r.Path, id+sep) {
				return true
			}
		}
		return false
	}
	list := o.Omitted.Items()
	for i := len(list) - 1; i >= 0; i-- {
		w := list[i]
		if !w.Partial {
			continue
		}
		var left []string
		for _, r := range strings.Split(w.Reason, "; ") {
			if (strings.HasPrefix(r, "words not searched") && found("fts", w.ID)) || (strings.HasPrefix(r, "not searched by meaning") && found("vector", w.ID)) {
				continue
			}
			left = append(left, r)
		}
		if len(left) == 0 {
			list = append(list[:i], list[i+1:]...)
		} else {
			list[i].Reason = strings.Join(left, "; ")
		}
	}
	o.Omitted.Set(list)
	byID := map[string]libraries.Library{}
	for _, l := range libs {
		byID[l.ID] = l
	}
	out := []ScopedHit{}
	for _, h := range gufi.FuseRows(rows, caps, total) {
		i := strings.Index(h.Path, sep)
		l := byID[h.Path[:i]]
		h.Path = h.Path[i+1:]
		out = append(out, ScopedHit{SearchHit: h, Library: HitLibrary{ID: l.ID, Label: l.Label}})
	}
	return out
}

// Annotate is the /api/search route's labels: the main library's from
// tagMaps (annotateHits), the others' from their stores
// (annotateLibraryHits); with filter, only hits carrying one of them.
func (e *Engine) Annotate(ctx context.Context, hits []ScopedHit, filter []string) []ScopedHit {
	out := []ScopedHit{}
	if e.Labels == nil {
		for _, h := range hits {
			h.Tags = []string{}
			if wanted(h.Tags, filter) {
				out = append(out, h)
			}
		}
		return out
	}
	var primary *TagMaps
	libs := map[string]TagMaps{}
	for _, h := range hits {
		if h.Library.ID == libraries.PrimaryID {
			if primary == nil {
				m := e.Labels.Primary(ctx)
				primary = &m
			}
			h.Tags = tagsOf(*primary, h.Path, nil, false)
		} else {
			m, ok := libs[h.Library.ID]
			if !ok {
				m = e.Labels.Library(h.Library.ID)
				libs[h.Library.ID] = m
			}
			h.Tags = tagsOf(m, h.Path, nil, true)
		}
		if wanted(h.Tags, filter) {
			out = append(out, h)
		}
	}
	return out
}
