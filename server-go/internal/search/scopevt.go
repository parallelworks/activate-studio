package search

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

func hitNum(r []string, i int) float64 {
	if i >= len(r) {
		return 0
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(r[i]), 64)
	if err != nil || math.IsNaN(f) {
		return 0
	}
	return f
}

func vtCell(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return ""
}

// FtsHit is ftsHit: a full-text row as a hit, its path relative to the
// library.
func FtsHit(r []string, rel string) gufi.SearchHit {
	return gufi.SearchHit{Path: rel, Name: vtCell(r, 1), Size: hitNum(r, 2), Mtime: hitNum(r, 3), Snippet: gufi.WithoutProvenance(vtCell(r, 4)), Score: 1, Mode: "fts"}
}

// NameHit is nameHit: a name row as a hit.
func NameHit(r []string, rel string) gufi.SearchHit {
	return gufi.SearchHit{Path: rel, Name: vtCell(r, 1), Size: hitNum(r, 2), Mtime: hitNum(r, 3), Snippet: "", Score: 0.5, Mode: "name"}
}

// BlendHits is blendHits: full-text, name and meaning hits without one
// starving the rest; a file found more than one way keeps its first place
// and is labelled by the most exact way: words, then name, then meaning.
func BlendHits(fts, names, vec []gufi.SearchHit, limit int) []gufi.SearchHit {
	vecTop := vec[:min(len(vec), min(6, max(2, limit/3)))]
	ftsTop := fts[:min(len(fts), max(4, limit-len(vecTop)))]
	exact := map[string]string{}
	for _, list := range [][]gufi.SearchHit{vec, names, fts} {
		for _, h := range list {
			exact[h.Path] = h.Mode
		}
	}
	seen := map[string]bool{}
	out := []gufi.SearchHit{}
	for _, list := range [][]gufi.SearchHit{ftsTop, vecTop, names, fts, vec} {
		for _, h := range list {
			if seen[h.Path] || len(out) >= limit {
				continue
			}
			seen[h.Path] = true
			h.Mode = exact[h.Path]
			out = append(out, h)
		}
	}
	return out
}

// searchScopeVt is searchScopeVt: the search as it ran before the one
// walk, for the gufi_vt setting: full text and names across libraries as
// one gufi_vt statement each, meaning per library, blended by BlendHits.
func (e *Engine) searchScopeVt(ctx context.Context, libs []libraries.Library, q string, limit int, o ScopeOpts) []ScopedHit {
	failed := func(l libraries.Library, err error, partial bool, what string) {
		o.Omitted.Add(libraries.Omission{ID: l.ID, Label: l.Label, Reason: what + libraries.FailureReason(l, err), Partial: partial})
	}
	const sep = "\x01"
	nameLimit := o.NameLimit
	if nameLimit == 0 {
		nameLimit = min(max(10, limit/5), 50)
	}
	var ftsLibs []libraries.Library
	for _, l := range libs {
		if e.Reg.WordsSearched(l) {
			ftsLibs = append(ftsLibs, l)
		}
	}
	viaVt := map[string]map[string]bool{"fts": {}, "names": {}}
	vtHits := map[string][]gufi.SearchHit{}
	var vmu sync.Mutex
	across := func(which, sql string, members []libraries.Library, cap int, hit func([]string, string) gufi.SearchHit) {
		items := make([]VtItem, len(members))
		for i, l := range members {
			items[i] = VtItem{Lib: l, Plan: gufi.Plan{Stages: gufi.Stages{E: sql}, Columns: []string{}}}
		}
		r, err := e.RunAcross(ctx, items, nil)
		if err != nil {
			return
		}
		bad := map[string]bool{}
		for _, f := range r.Failed {
			bad[f.Library] = true
		}
		vmu.Lock()
		defer vmu.Unlock()
		for _, l := range members {
			if !bad[l.Label] {
				viaVt[which][l.ID] = true
			}
		}
		count := map[string]int{}
		for _, t := range r.Rows {
			n := count[t.ID] + 1
			if n > cap {
				continue
			}
			count[t.ID] = n
			h := hit(t.Row, vtCell(t.Row, 0))
			h.Path = t.ID + sep + vtCell(t.Row, 0)
			vtHits[which] = append(vtHits[which], h)
		}
	}
	var wg sync.WaitGroup
	if bare, ok := gufi.FtsExprBare(q); ok && bare != "" && len(ftsLibs) > 1 {
		wg.Add(1)
		go func() { defer wg.Done(); across("fts", gufi.FtsSQL(bare, limit), ftsLibs, limit*3, FtsHit) }()
	}
	if nSQL := gufi.NamesSQL(q, nameLimit); nSQL != "" {
		wg.Add(1)
		go func() { defer wg.Done(); across("names", nSQL, libs, nameLimit, NameHit) }()
	}
	wg.Wait()
	deadline := 0
	if len(libs) > 1 {
		deadline = o.DeadlineMs
		if deadline == 0 {
			deadline = DeadlineMs()
		}
	}
	type kinds struct{ fts, names, vec []gufi.SearchHit }
	tag := func(l libraries.Library, hs []gufi.SearchHit) []gufi.SearchHit {
		out := make([]gufi.SearchHit, len(hs))
		for i, h := range hs {
			h.Path = l.ID + sep + h.Path
			out[i] = h
		}
		return out
	}
	per := make([]kinds, len(libs))
	var slowMu sync.Mutex
	for i, l := range libs {
		wg.Add(1)
		go func(i int, l libraries.Library) {
			defer wg.Done()
			ftsLimit, namesLimit := 0, 0
			if e.Reg.WordsSearched(l) && !viaVt["fts"][l.ID] {
				ftsLimit = limit
			}
			if !viaVt["names"][l.ID] {
				namesLimit = nameLimit
			}
			done := make(chan kinds, 1)
			go func() {
				var k kinds
				var inner sync.WaitGroup
				if ftsLimit > 0 || namesLimit > 0 {
					inner.Add(1)
					go func() {
						defer inner.Done()
						f, n, err := e.SearchWordsAndNames(ctx, q, ftsLimit, namesLimit, l.IndexRoot)
						if err != nil {
							failed(l, err, false, "")
							return
						}
						k.fts, k.names = tag(l, f), tag(l, n)
					}()
				}
				if o.Semantic && e.Reg.MeaningSearched(l) {
					inner.Add(1)
					go func() {
						defer inner.Done()
						v, err := e.SearchVector(ctx, q, min(limit, 10), l.IndexRoot)
						if err != nil {
							failed(l, err, true, "not searched by meaning: ")
							return
						}
						k.vec = tag(l, v)
					}()
				}
				inner.Wait()
				done <- k
			}()
			if deadline == 0 {
				per[i] = <-done
				return
			}
			t := time.NewTimer(time.Duration(deadline) * time.Millisecond)
			defer t.Stop()
			select {
			case k := <-done:
				per[i] = k
			case <-t.C:
				if o.Slow != nil {
					slowMu.Lock()
					*o.Slow = append(*o.Slow, l.Label)
					slowMu.Unlock()
				}
				o.Omitted.Add(libraries.Omission{ID: l.ID, Label: l.Label, Reason: "did not answer within " + deadlineText(deadline), Partial: false})
			}
		}(i, l)
	}
	wg.Wait()
	per = append(per, kinds{fts: vtHits["fts"], names: vtHits["names"]})
	// Across libraries, word matches go best bm25 first and name matches
	// best nameRank first; a stable sort keeps each library's order among
	// equals, and hits with no rank (gufi_vt's) after the ranked ones.
	ranked := func(hs []gufi.SearchHit) []gufi.SearchHit {
		if len(libs) <= 1 {
			return hs
		}
		rk := func(h gufi.SearchHit) float64 {
			if h.Rank == nil {
				return math.Inf(1)
			}
			return *h.Rank
		}
		out := append([]gufi.SearchHit{}, hs...)
		sort.SliceStable(out, func(i, j int) bool { return rk(out[i]) < rk(out[j]) })
		return out
	}
	var fts, names, vec []gufi.SearchHit
	for _, p := range per {
		fts, names, vec = append(fts, p.fts...), append(names, p.names...), append(vec, p.vec...)
	}
	byID := map[string]libraries.Library{}
	for _, l := range libs {
		byID[l.ID] = l
	}
	out := []ScopedHit{}
	for _, h := range BlendHits(ranked(fts), ranked(names), vec, limit) {
		i := strings.Index(h.Path, sep)
		l := byID[h.Path[:i]]
		h.Path = h.Path[i+1:]
		out = append(out, ScopedHit{SearchHit: h, Library: HitLibrary{ID: l.ID, Label: l.Label}})
	}
	return out
}
