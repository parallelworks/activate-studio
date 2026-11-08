package chat

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/refine"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// The corpus tools of tools.ts that run on the libraries track's packages:
// search_kb, list_kb_dir, list_libraries and query_corpus. What those
// packages do not have yet stays in Node (errNodeTool): search_kb's grep
// fallback (no GUFI here and no library on another system) and the
// gufi_vt form of a search across libraries, and listing a library on
// another system (userLibraries.ts remoteTree). read_kb_file and
// read_original wait on the index-text read and openOriginal.

func init() {
	goExecutors["search_kb"] = (*Module).toolSearchKB
	goExecutors["list_kb_dir"] = (*Module).toolListKBDir
	goExecutors["list_libraries"] = (*Module).toolListLibraries
	goExecutors["query_corpus"] = (*Module).toolQueryCorpus
}

// citedFiles is tools.ts's citedFiles: at most 60, the main library null.
func citedFiles(rows []conversations.File) []conversations.File {
	if len(rows) > 60 {
		rows = rows[:60]
	}
	out := make([]conversations.File, len(rows))
	for i, r := range rows {
		out[i] = conversations.File{Path: r.Path}
		if r.Library != nil && *r.Library != "" && *r.Library != "kb" {
			out[i].Library = r.Library
		}
	}
	return out
}

func fileIn(path, library string) conversations.File {
	return conversations.File{Path: path, Library: &library}
}

// scopeParam is String(name ?? ”).trim() || the conversation's library.
func scopeParam(v any, ctx ToolCtx) []string {
	if s := jsTrim(jsString(v)); s != "" {
		return []string{s}
	}
	if ctx.Library != nil && *ctx.Library != "" {
		return []string{*ctx.Library}
	}
	return nil
}

// namedLibrary is the library a read or a listing names, else the
// conversation's; "all" is for search and query only.
func (m *Module) namedLibrary(v any, ctx ToolCtx) (libraries.Library, error) {
	want := jsTrim(jsString(v))
	if want == "" && ctx.Library != nil {
		want = *ctx.Library
	}
	if strings.ToLower(want) == "all" {
		return libraries.Library{}, &toolError{`name one library (an id from list_libraries) to read or list; "all" is for search and query`}
	}
	return m.Corpus.Reg.Get(want, m.primaryLabel(), viewerOfCtx(ctx))
}

// sliceEnd is the length of Array.prototype.slice(0, end) over n items.
func sliceEnd(n, end int) int {
	if end < 0 {
		end += n
	}
	return max(0, min(n, end))
}

// jsNumberOr is Number(v) || d.
func jsNumberOr(v any, d float64) float64 {
	n := numberOf(v)
	if n == 0 || math.IsNaN(n) {
		return d
	}
	return n
}

func numberOf(v any) float64 {
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
	case string:
		t := jsTrim(x)
		if t == "" {
			return 0
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	case []any:
		if len(x) == 0 {
			return 0
		}
		if len(x) == 1 {
			return numberOf(jsString(x[0]))
		}
	}
	return math.NaN()
}

// tagList is String(v).split(',').filter(Boolean), nil when v is falsy.
func tagList(v any) []string {
	if !jsTruthy(v) {
		return nil
	}
	out := []string{}
	for _, t := range strings.Split(jsString(v), ",") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// traced runs fn with a GUFI command trace and puts the commands on its
// outcome, as executeTool does (traced('tool', ...)).
func traced(r *http.Request, fn func(ctx context.Context) (*Outcome, error)) (*Outcome, error) {
	ctx, commands := gufitrace.Start(r.Context(), "tool")
	out, err := fn(ctx)
	if err != nil && !errors.Is(err, errNodeTool) {
		// executeToolImpl's catch: the error is the result, and the
		// commands that ran before it stay on it.
		out, err = &Outcome{Result: "Tool error: " + err.Error(), Summary: "error"}, nil
	}
	if out != nil {
		if cs := commands(); len(cs) > 0 {
			out.Gufi, _ = jsonv.Marshal(cs)
		}
	}
	return out, err
}

func (m *Module) toolSearchKB(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	if m.Corpus == nil {
		return nil, errNodeTool
	}
	query := jsval.Slice(jsString(args["query"]), 200)
	limit := int(math.Min(jsNumberOr(args["limit"], 10), 25))
	ref := search.RefineOf(jsString(args["name"]), jsString(args["text"]), jsString(args["match"]))
	if jsTrim(query) == "" && ref == nil {
		return &Outcome{Result: "Give query, or name or text.", Summary: "nothing to search for"}, nil
	}
	if ref != nil {
		if err := refine.ValidateText(ref.Text); err != nil {
			return &Outcome{Result: err.Error(), Summary: "text not readable"}, nil
		}
	}
	reg := m.Corpus.Reg
	libs, omittedList, err := reg.Scope(scopeParam(args["library"], ctx), viewerOfCtx(ctx), m.primaryLabel())
	if err != nil {
		return nil, err
	}
	remoteLib := false
	for _, l := range libs {
		if l.Site != "" {
			remoteLib = true
		}
	}
	if !m.gufiInstalled() && !remoteLib {
		// TODO(libraries track): grepFallback.
		return nil, errNodeTool
	}
	if ref == nil && len(libs) > 1 && m.eff().SearchEngine == "gufi_vt" {
		// TODO(libraries track): searchScopeVt.
		return nil, errNodeTool
	}
	return traced(r, func(c context.Context) (*Outcome, error) {
		eng := m.Corpus.Search
		slow := []string{}
		om := &libraries.Omissions{}
		om.Set(omittedList)
		q := query
		semantic := true
		if ref != nil {
			q, semantic = "", ref.Text != ""
		}
		scoped := eng.SearchScope(c, libs, q, limit+5, search.ScopeOpts{Semantic: semantic, NameLimit: 5, Slow: &slow, Omitted: om, Refine: ref})
		late := ""
		if items := om.Items(); len(items) > 0 {
			late = "\n" + libraries.OmissionsText(items)
			if len(slow) == 1 {
				late += " Search one that did not answer in time on its own to wait for it."
			} else if len(slow) > 1 {
				late += " Search one of those that did not answer in time on its own to wait for it."
			}
		}
		if len(libs) > 1 || libs[0].ID != libraries.PrimaryID {
			var own, other []search.ScopedHit
			for _, h := range scoped {
				if h.Library.ID != libraries.PrimaryID || len(libs) == 1 {
					other = append(other, h)
				}
			}
			if len(libs) > 1 {
				var kbHits []search.ScopedHit
				for _, h := range scoped {
					if h.Library.ID == libraries.PrimaryID {
						kbHits = append(kbHits, h)
					}
				}
				own = eng.Annotate(c, kbHits, tagList(args["tags"]))
				for i := range own {
					own[i].Library = search.HitLibrary{ID: libraries.PrimaryID}
				}
			}
			hits := append(own, other...)
			hits = hits[:sliceEnd(len(hits), limit)]
			var b strings.Builder
			files := []conversations.File{}
			for i, h := range hits {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString("[library " + h.Library.ID + "] " + h.Path + "\n  " + orStr(h.Snippet, "(filename match)"))
				files = append(files, fileIn(h.Path, h.Library.ID))
			}
			result := "No matches."
			if len(hits) > 0 {
				result = b.String() + "\nRead a hit with read_kb_file and its library id."
			}
			noun := "libraries"
			if len(libs) == 1 {
				noun = "library"
			}
			return &Outcome{Result: result + late, Summary: strconv.Itoa(len(hits)) + " hits in " + strconv.Itoa(len(libs)) + " " + noun, Files: citedFiles(files)}, nil
		}
		// The conversation's label scope holds whatever the model asked
		// for; the model's tags narrow it only within the scope.
		var scope []string
		for _, t := range ctx.LabelScope {
			if t != "" {
				scope = append(scope, t)
			}
		}
		filter := tagList(args["tags"])
		if len(scope) > 0 {
			var within []string
			for _, t := range filter {
				if contains(scope, t) {
					within = append(within, t)
				}
			}
			if len(within) > 0 {
				filter = within
			} else {
				filter = scope
			}
		}
		scoped = scoped[:sliceEnd(len(scoped), limit+5)]
		hits := eng.Annotate(c, scoped, filter)
		var b strings.Builder
		files := []conversations.File{}
		for i, h := range hits {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(h.Path)
			if h.Tags != nil && len(*h.Tags) > 0 {
				b.WriteString(" [labels: " + strings.Join(*h.Tags, ", ") + "]")
			}
			b.WriteString("\n  " + orStr(h.Snippet, "(filename match)"))
			files = append(files, fileIn(h.Path, libs[0].ID))
		}
		result := "No matches."
		if len(hits) > 0 {
			result = b.String()
		}
		return &Outcome{Result: result + late, Summary: strconv.Itoa(len(hits)) + " hits", Files: citedFiles(files)}, nil
	})
}

func (m *Module) toolListKBDir(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	if m.Corpus == nil {
		return nil, errNodeTool
	}
	lib, err := m.namedLibrary(args["library"], ctx)
	if err != nil {
		return nil, err
	}
	if lib.Site == "" && lib.SourceRoot == "" {
		return nil, &toolError{lib.Label + " has no files on this host; it can be searched but not listed"}
	}
	if lib.Site != "" {
		// TODO(libraries track): remoteTree.
		return nil, errNodeTool
	}
	entries, err := kb.ListDir(jsString(args["path"]), lib.SourceRoot, false)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	files := []conversations.File{}
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		if e.Type == "dir" {
			b.WriteString("d " + e.Path + "/")
		} else {
			b.WriteString("- " + e.Path + " (" + jsval.NumberString(float64(e.Size)) + "b)")
			files = append(files, fileIn(e.Path, lib.ID))
		}
	}
	return &Outcome{Result: orStr(b.String(), "(empty)"), Summary: strconv.Itoa(len(entries)) + " entries", Files: citedFiles(files)}, nil
}

func (m *Module) toolListLibraries(r *http.Request, _ map[string]any, ctx ToolCtx) (*Outcome, error) {
	if m.Corpus == nil {
		return nil, errNodeTool
	}
	reg := m.Corpus.Reg
	libs := reg.List(m.primaryLabel(), viewerOfCtx(ctx))
	lines := make([]string, len(libs))
	for i, l := range libs {
		kind := " (read only)"
		switch {
		case l.Primary:
			kind = " (the knowledge base, read and write)"
		case l.Site != "":
			kind = " (your library on " + l.Site + ", read only)"
		}
		s := l.ID + ": " + l.Label + kind + "; search: names"
		if reg.WordsSearched(l) {
			s += ", full text"
		}
		if reg.MeaningSearched(l) {
			s += ", semantic"
		}
		lines[i] = s
	}
	noun := "libraries"
	if len(libs) == 1 {
		noun = "library"
	}
	return &Outcome{Result: orStr(strings.Join(lines, "\n"), "No libraries."), Summary: strconv.Itoa(len(libs)) + " " + noun}, nil
}

func (m *Module) toolQueryCorpus(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	if m.Corpus == nil {
		return nil, errNodeTool
	}
	body := map[string]any{
		"canned": jsString(args["canned"]),
		"n":      math.Min(jsNumberOr(args["n"], 25), 100),
		"days":   math.Min(jsNumberOr(args["days"], 7), 3650),
	}
	if jsTruthy(args["subtree"]) {
		body["subtree"] = jsString(args["subtree"])
	}
	if lib := scopeParam(args["library"], ctx); lib != nil {
		body["library"] = lib[0]
	}
	raw, err := jsonv.Marshal(body)
	if err != nil {
		return nil, err
	}
	return traced(r, func(c context.Context) (*Outcome, error) {
		res, err := m.Corpus.Search.QueryScope(c, raw, viewerOfCtx(ctx), m.primaryLabel())
		if err != nil {
			return nil, err
		}
		lines := []string{strings.Join(res.Columns, " | ")}
		for _, row := range res.Rows {
			lines = append(lines, strings.Join(row, " | "))
		}
		return &Outcome{Result: jsval.Slice(strings.Join(lines, "\n"), toolOutputCap),
			Summary: strconv.Itoa(len(res.Rows)) + " rows in " + strconv.FormatInt(res.ElapsedMS, 10) + " ms"}, nil
	})
}

/* ---- the RAG proxy's retrieval (ragProxy.ts buildContext) ---- */

const (
	ragContextCap = 14_000
	ragPerHitCap  = 2_500
)

var ragStopwords = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields("the a an and or but not only what which who whom whose why how when where was were is are be been do does did can could should would will shall may might for from with without about into onto that this these those there their they them its our your you me my we us one two three please tell give show short brief paragraph paragraphs sentence sentences word words line lines answer question explain describe summarize summary list") {
		out[w] = true
	}
	return out
}()

// distill is ragProxy.ts's distill: the informative words of a question,
// at most eight.
func distill(query string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(query) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || isJSSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range strings.FieldsFunc(b.String(), isJSSpace) {
		if len(t) >= 3 && !ragStopwords[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// blendHits is gufi.ts's blendHits: the best words and meaning matches
// first, then names, the rest of each after, each path once, and a path
// marked by the kind of match it was last found as.
func blendHits(fts, names, vec []gufi.SearchHit, limit int) []gufi.SearchHit {
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

// buildContext is ragProxy.ts's buildContext over the main library.
func (m *Module) buildContext(ctx context.Context, query string, topK int, tags []string) (*ragContext, error) {
	eng := m.Corpus.Search
	q := jsval.Slice(query, 300)
	terms := distill(q)
	termQuery := strings.Join(terms, " ")
	if termQuery == "" {
		termQuery = jsval.Slice(q, 100)
	}
	root := m.A.Cfg.GufiIndex
	var vec []gufi.SearchHit
	vecDone := make(chan struct{})
	go func() {
		defer close(vecDone)
		if m.Corpus.Reg.Settings().Meaning(libraries.PrimaryID) != "off" {
			if v, err := eng.SearchVector(ctx, q, min(topK, 8), root); err == nil {
				vec = v
			}
		}
	}()
	fts, names, err := eng.SearchWordsAndNames(ctx, termQuery, topK, min(topK, 5), root)
	<-vecDone
	if err != nil {
		return nil, err
	}
	// An over-specified AND query finds nothing; the strongest terms again.
	if len(fts) == 0 && len(terms) > 3 {
		strong := append([]string(nil), terms...)
		sort.SliceStable(strong, func(i, j int) bool { return len(strong[i]) > len(strong[j]) })
		if f, _, err := eng.SearchWordsAndNames(ctx, strings.Join(strong[:3], " "), topK, 0, root); err == nil {
			fts = f
		}
	}
	blended := blendHits(fts, names, vec, topK)
	scoped := make([]search.ScopedHit, len(blended))
	for i, h := range blended {
		scoped[i] = search.ScopedHit{SearchHit: h, Library: search.HitLibrary{ID: libraries.PrimaryID}}
	}
	var filter []string
	if len(tags) > 0 {
		filter = tags
	}
	hits := eng.Annotate(ctx, scoped, filter)
	if len(hits) == 0 {
		return nil, nil
	}
	var blocks []string
	total := 0
	cache := kb.RootsFrom(m.A.Cfg).ExtractCache()
	for _, h := range hits {
		text := h.Snippet
		if fc, err := kb.ReadFileContent(h.Path, m.A.Cfg.KBRoot, cache); err == nil {
			if v, ok := fc.Get("content"); ok {
				if s, ok := v.(string); ok && s != "" {
					text = jsval.Slice(s, ragPerHitCap)
				}
			}
		}
		text = jsTrim(text)
		if text == "" {
			continue
		}
		n := jsval.Len(text)
		if total+n > ragContextCap {
			break
		}
		total += n
		labels := ""
		if h.Tags != nil && len(*h.Tags) > 0 {
			labels = " (labels: " + strings.Join(*h.Tags, ", ") + ")"
		}
		blocks = append(blocks, "["+strconv.Itoa(len(blocks)+1)+"] "+h.Path+labels+"\n"+text)
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	return &ragContext{Text: strings.Join(blocks, "\n\n"), Terms: terms, Blocks: len(blocks), Chars: total}, nil
}
