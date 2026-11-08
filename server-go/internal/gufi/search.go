package gufi

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// This file ports the search grammar and the SQL builders of
// server/src/gufi.ts line by line; golden_test.go holds them to Node's
// output. Two JavaScript string rules are kept where the SQL depends on
// them: a string's length counts UTF-16 code units (jsLen), and \s is
// JavaScript's white space (jsSpace), wider than RE2's.

// jsSpace is JavaScript's \s.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsLen is a JavaScript string's length.
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// JSTrim is String.prototype.trim.
func JSTrim(s string) string {
	return strings.Trim(s, jsSpaceChars)
}

// q escapes a string literal for GUFI SQL.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an the and or of to in on for we our is are was be can how what when with up after do does it this that at by from as us you") {
		stopwords[w] = true
	}
}

// QueryItem is one term or phrase.
type QueryItem struct {
	Text   string `json:"text"`
	Phrase bool   `json:"phrase"`
	Prefix bool   `json:"prefix"`
}

// QueryGroup is one OR alternative: every item must match, no negative may.
type QueryGroup struct {
	Items     []QueryItem `json:"items"`
	Negatives []QueryItem `json:"negatives"`
}

// ParsedQuery is parseSearchQuery's result.
type ParsedQuery struct {
	Groups       []QueryGroup `json:"groups"`
	HasOperators bool         `json:"hasOperators"`
	Exact        bool         `json:"exact"`
}

var searchTok = regexp.MustCompile(`(-|NOT[` + jsSpace + `]+)?"([^"]*)"|(-)?([^` + jsSpace + `]+)`)
var quoteChars = regexp.MustCompile(`["'*]`)

// group returns submatch i and whether it took part in the match.
func group(s string, m []int, i int) (string, bool) {
	if m[2*i] < 0 {
		return "", false
	}
	return s[m[2*i]:m[2*i+1]], true
}

// ParseSearchQuery is parseSearchQuery: "quoted words" match as a phrase,
// OR between alternatives, -word or NOT word excludes, word* matches by
// prefix, everything else is a whole word that must be present.
func ParseSearchQuery(raw string) ParsedQuery {
	s := JSTrim(raw)
	type tok struct {
		text                string
		phrase, neg, prefix bool
		op                  string
	}
	var toks []tok
	hasOps, pendingNot := false, false
	for _, m := range searchTok.FindAllStringSubmatchIndex(s, -1) {
		if phrase, ok := group(s, m, 2); ok {
			t := JSTrim(strings.ReplaceAll(phrase, `"`, ""))
			_, neg := group(s, m, 1)
			if t != "" {
				toks = append(toks, tok{text: t, phrase: true, neg: neg || pendingNot})
				hasOps = true
			}
			pendingNot = false
			continue
		}
		w, _ := group(s, m, 4)
		_, neg := group(s, m, 3)
		if w == "OR" || w == "AND" {
			toks = append(toks, tok{text: w, op: w})
			hasOps = true
			continue
		}
		if w == "NOT" {
			pendingNot, hasOps = true, true
			continue
		}
		prefix := strings.HasSuffix(w, "*")
		w = quoteChars.ReplaceAllString(w, "")
		if w == "" {
			continue
		}
		if neg || pendingNot || prefix {
			hasOps = true
		}
		toks = append(toks, tok{text: w, neg: neg || pendingNot, prefix: prefix})
		pendingNot = false
	}
	groups := []QueryGroup{{Items: []QueryItem{}, Negatives: []QueryItem{}}}
	for _, t := range toks {
		if t.op == "OR" {
			groups = append(groups, QueryGroup{Items: []QueryItem{}, Negatives: []QueryItem{}})
			continue
		}
		if t.op == "AND" {
			continue
		}
		item := QueryItem{Text: t.text, Phrase: t.phrase, Prefix: t.prefix}
		g := &groups[len(groups)-1]
		if t.neg {
			g.Negatives = append(g.Negatives, item)
		} else {
			g.Items = append(g.Items, item)
		}
	}
	out := ParsedQuery{Groups: []QueryGroup{}, HasOperators: hasOps}
	for _, g := range groups {
		kept := []QueryItem{}
		for _, i := range g.Items {
			if i.Phrase || i.Prefix || !stopwords[strings.ToLower(i.Text)] {
				kept = append(kept, i)
			}
		}
		if len(kept) == 0 {
			kept = g.Items
		}
		if len(kept) > 8 {
			kept = kept[:8]
		}
		g.Items = kept
		if len(g.Items) > 0 || len(g.Negatives) > 0 {
			out.Groups = append(out.Groups, g)
		}
	}
	out.Exact = len(toks) == 1 && toks[0].phrase && !toks[0].neg
	return out
}

func ftsQuote(t string) string { return `"` + strings.ReplaceAll(t, `"`, "") + `"` }

// FtsExprFor is the fts5 MATCH expression for a parsed query, or "".
func FtsExprFor(p ParsedQuery) string {
	var parts []string
	for _, g := range p.Groups {
		var pos []string
		for _, i := range g.Items {
			e := ftsQuote(i.Text)
			if i.Prefix {
				e += "*"
			}
			pos = append(pos, e)
		}
		if len(pos) == 0 {
			continue
		}
		e := pos[0]
		if len(pos) > 1 {
			e = "(" + strings.Join(pos, " AND ") + ")"
		}
		for _, n := range g.Negatives {
			e = "(" + e + " NOT " + ftsQuote(n.Text) + ")"
		}
		parts = append(parts, e)
	}
	if len(parts) > 1 {
		return strings.Join(parts, " OR ")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}

var bareWord = regexp.MustCompile(`^[\p{L}\p{N}_]+$`)
var ftsKeyword = regexp.MustCompile(`^(AND|OR|NOT|NEAR)$`)

// FtsExprBare is the same expression with every term a bareword, for
// gufi_vt; ok is false when a term is more than letters, digits and
// underscores, or nothing positive remains.
func FtsExprBare(query string) (string, bool) {
	bare := func(t string) (string, bool) {
		s := strings.ReplaceAll(t, `"`, "")
		if !bareWord.MatchString(s) {
			return "", false
		}
		if ftsKeyword.MatchString(s) {
			return strings.ToLower(s), true
		}
		return s, true
	}
	var parts []string
	for _, g := range ParseSearchQuery(query).Groups {
		var pos []string
		for _, i := range g.Items {
			b, ok := bare(i.Text)
			if !ok {
				return "", false
			}
			if i.Prefix {
				b += "*"
			}
			pos = append(pos, b)
		}
		if len(pos) == 0 {
			continue
		}
		e := pos[0]
		if len(pos) > 1 {
			e = "(" + strings.Join(pos, " AND ") + ")"
		}
		for _, n := range g.Negatives {
			b, ok := bare(n.Text)
			if !ok {
				return "", false
			}
			e = "(" + e + " NOT " + b + ")"
		}
		parts = append(parts, e)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " OR "), true
}

const ftsColumns = `rpath(sname, sroll)||'/'||vrpentries.name, vrpentries.name, size, mtime, ` +
	`replace(replace(snippet(words, 2, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ')`

func ftsFrom(expr string) string {
	return `FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT) WHERE wordf MATCH ` + q(expr)
}

// PassageChars is how much of a passage a search by meaning shows.
const PassageChars = 200

var chunkSnippet = func() string {
	flat := `replace(replace(g.ctext, char(10), ' '), char(13), ' ')`
	return fmt.Sprintf(`CASE WHEN g.seq > 0 AND instr(%[1]s, ' ') > 0 THEN substr(%[1]s, instr(%[1]s, ' ') + 1, %[2]d) ELSE substr(%[1]s, 1, %[2]d) END`, flat, PassageChars)
}()

// PassageSnippet ends a passage cut at its full width at its last whole
// word, marked as continuing.
func PassageSnippet(s string) string {
	if utf8.RuneCountInString(s) < PassageChars {
		return s
	}
	u := utf16.Encode([]rune(s))
	end := -1
	for i := len(u) - 1; i >= 0; i-- {
		if u[i] == ' ' {
			end = i
			break
		}
	}
	if end > PassageChars/2 {
		return strings.TrimRightFunc(string(utf16.Decode(u[:end])), func(r rune) bool { return strings.ContainsRune(jsSpaceChars, r) }) + " …"
	}
	return s
}

const jsSpaceChars = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// FtsSQL is full-text search's per-directory SQL for an fts5 expression.
func FtsSQL(expr string, limit int) string {
	return fmt.Sprintf("SELECT %s %s ORDER BY bm25(words) LIMIT %d;", ftsColumns, ftsFrom(expr), limit)
}

// NamesSQL is name search's per-directory SQL, or "" when the query names
// nothing.
func NamesSQL(queryText string, limit int) string {
	p := ParseSearchQuery(queryText)
	where := NameWhere(p)
	if where == "0" {
		return ""
	}
	return fmt.Sprintf("SELECT rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE %s ORDER BY %s, name LIMIT %d;", where, NameRank(p), limit)
}

var likeMeta = regexp.MustCompile(`[%_]`)

// NameRank orders name matches: 0 when the name is the query, 1 when it
// starts with its first word, 2 otherwise.
func NameRank(p ParsedQuery) string {
	var first []QueryItem
	for _, g := range p.Groups {
		if len(g.Items) > 0 {
			first = g.Items
			break
		}
	}
	clean := func(t string) string { return likeMeta.ReplaceAllString(t, "") }
	texts := make([]string, len(first))
	for i, it := range first {
		texts[i] = it.Text
	}
	whole := clean(strings.Join(texts, " "))
	lead := ""
	if len(first) > 0 {
		lead = clean(first[0].Text)
	}
	if whole == "" {
		return "2"
	}
	return fmt.Sprintf("CASE WHEN name LIKE %s OR name LIKE %s THEN 0 WHEN name LIKE %s THEN 1 ELSE 2 END", q(whole), q(whole+".%"), q(lead+"%"))
}

// NameClause is a filename clause for one term.
func NameClause(term string) string { return likeClause("name", term) }

// likeClause matches a term by substring, except that a term of three
// characters or fewer must start a word.
func likeClause(col, term string) string {
	t := strings.ReplaceAll(term, "%", "")
	if strings.ReplaceAll(t, "_", "") == "" {
		return "0"
	}
	if strings.Contains(t, "_") {
		esc := strings.ReplaceAll(strings.ReplaceAll(t, `\`, `\\`), "_", `\_`)
		return fmt.Sprintf(`%s LIKE %s ESCAPE '\'`, col, q("%"+esc+"%"))
	}
	if jsLen(t) > 3 {
		return fmt.Sprintf("%s LIKE %s", col, q("%"+t+"%"))
	}
	starts := []string{"", " ", "-", "_", ".", "(", "[", ","}
	if col != "name" {
		starts = append(starts, "/")
	}
	ors := make([]string, len(starts))
	for i, sep := range starts {
		pre := ""
		if sep != "" {
			pre = "%" + sep
		}
		ors[i] = fmt.Sprintf("%s LIKE %s", col, q(pre+t+"%"))
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

func whereOver(col string, p ParsedQuery) string {
	var groups []string
	for _, g := range p.Groups {
		var cs []string
		for _, i := range g.Items {
			if c := likeClause(col, i.Text); c != "0" {
				cs = append(cs, c)
			}
		}
		if len(cs) == 0 {
			continue
		}
		if len(cs) > 1 {
			groups = append(groups, "("+strings.Join(cs, " AND ")+")")
		} else {
			groups = append(groups, cs[0])
		}
	}
	if len(groups) == 0 {
		return "0"
	}
	return strings.Join(groups, " OR ")
}

// NameWhere is the WHERE clause of a name search.
func NameWhere(p ParsedQuery) string { return whereOver("name", p) }

var acronym = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,7}$`)
var digit = regexp.MustCompile(`\d`)

// WantsSemantic is whether meaning hits belong in a blend: for a question
// or a topic, not for an identifier, an acronym, a quoted phrase or a
// query with operators.
func WantsSemantic(raw string) bool {
	p := ParseSearchQuery(raw)
	if p.HasOperators || p.Exact {
		return false
	}
	var words []string
	for _, g := range p.Groups {
		for _, i := range g.Items {
			words = append(words, i.Text)
		}
	}
	if len(words) == 1 {
		w := words[0]
		if jsLen(w) <= 3 || acronym.MatchString(w) || digit.MatchString(w) {
			return false
		}
	}
	return len(words) > 0
}

// relDirSQL is a directory's path below the root it was reached from.
func relDirSQL(roots []string, rp string) string {
	if rp == "" {
		rp = "rpath(sname, sroll)"
	}
	n := utf8.RuneCountInString
	if len(roots) == 1 {
		return fmt.Sprintf("substr(%s, %d)", rp, n(roots[0])+2)
	}
	whens := make([]string, len(roots))
	for i, r := range roots {
		whens[i] = fmt.Sprintf("WHEN substr(%s, 1, %d) = %s THEN substr(%s, %d)", rp, n(r), q(r), rp, n(r)+2)
	}
	return fmt.Sprintf("CASE %s ELSE %s END", strings.Join(whens, " "), rp)
}

// RefineSpec is a refined search: names and paths from Name, words and
// meaning from Text, Match "any" or "all".
type RefineSpec struct {
	Name  string `json:"name"`
	Text  string `json:"text"`
	Match string `json:"match"`
}

type nameTok struct {
	Kind string `json:"kind"` // glob or word
	Text string `json:"text"`
	Neg  bool   `json:"neg"`
}

var nameFilterTok = regexp.MustCompile(`(-)?"([^"]*)"|([^` + jsSpace + `]+)`)
var globChars = regexp.MustCompile(`[*?]`)

// ParseNameFilter is the name field as OR-separated groups of tokens.
func ParseNameFilter(raw string) [][]nameTok {
	groups := [][]nameTok{{}}
	neg := false
	s := JSTrim(raw)
	for _, m := range nameFilterTok.FindAllStringSubmatchIndex(s, -1) {
		if quoted, ok := group(s, m, 2); ok {
			_, minus := group(s, m, 1)
			if t := JSTrim(quoted); t != "" {
				groups[len(groups)-1] = append(groups[len(groups)-1], nameTok{Kind: "word", Text: t, Neg: neg || minus})
			}
			neg = false
			continue
		}
		t, _ := group(s, m, 3)
		switch t {
		case "OR":
			groups = append(groups, []nameTok{})
			neg = false
			continue
		case "AND":
			continue
		case "NOT":
			neg = true
			continue
		}
		dash := strings.HasPrefix(t, "-") && jsLen(t) > 1
		n := neg || dash
		if dash {
			t = t[1:]
		}
		neg = false
		kind := "word"
		if globChars.MatchString(t) {
			kind = "glob"
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], nameTok{Kind: kind, Text: t, Neg: n})
	}
	out := [][]nameTok{}
	for _, g := range groups {
		if len(g) > 0 {
			out = append(out, g)
		}
	}
	return out
}

var globEscape = regexp.MustCompile(`[\\%_]`)

func globLike(glob string) string {
	s := globEscape.ReplaceAllStringFunc(glob, func(c string) string { return `\` + c })
	return strings.ReplaceAll(strings.ReplaceAll(s, "*", "%"), "?", "_")
}

// nameFilterSQL is the name field as SQL over one table alias's entries.
func nameFilterSQL(groups [][]nameTok, roots []string, pre, words, use string) string {
	name := pre + "name"
	rel := relDirSQL(roots, fmt.Sprintf("rpath(%ssname, %ssroll)", pre, pre))
	one := func(t nameTok) string {
		if t.Kind == "glob" {
			col := name
			if strings.Contains(t.Text, "/") {
				col = fmt.Sprintf("ltrim(%s||'/'||%s, '/')", rel, name)
			}
			return fmt.Sprintf(`%s LIKE %s ESCAPE '\'`, col, q(globLike(t.Text)))
		}
		n := likeClause(name, t.Text)
		if words == "name" {
			return n
		}
		return fmt.Sprintf("(%s OR %s)", n, likeClause(rel, t.Text))
	}
	var sql []string
	for _, g := range groups {
		var pos, negs []string
		for _, t := range g {
			c := one(t)
			if c == "0" {
				continue
			}
			if t.Neg {
				negs = append(negs, c)
			} else {
				pos = append(pos, c)
			}
		}
		if len(pos) == 0 && use == "list" {
			continue
		}
		if len(pos) == 0 && len(negs) == 0 {
			continue
		}
		parts := pos
		if len(parts) == 0 {
			parts = []string{"1"}
		}
		for _, c := range negs {
			parts = append(parts, "NOT "+c)
		}
		if len(parts) > 1 {
			sql = append(sql, "("+strings.Join(parts, " AND ")+")")
		} else {
			sql = append(sql, parts[0])
		}
	}
	if len(sql) == 0 {
		return "0"
	}
	return strings.Join(sql, " OR ")
}

// WalkLimits is gufi.ts's WalkLimits.
type WalkLimits struct {
	Names         int     `json:"names"`
	Paths         int     `json:"paths"`
	Words         int     `json:"words"`
	WordsPerDir   int     `json:"wordsPerDir"`
	Vectors       int     `json:"vectors"`
	VectorsPerDir int     `json:"vectorsPerDir"`
	StudioText    bool    `json:"studioText"`
	StudioVectors bool    `json:"studioVectors"`
	External      *string `json:"external"`
}

// QueryVector is a query embedding for the models a LIKE pattern names.
type QueryVector struct {
	Pattern string `json:"pattern"`
	Hex     string `json:"hex"`
}

// RRFK is reciprocal rank fusion's constant.
const RRFK = 60

// OnePassStages is onePassStages: the stages of one gufi_query walk that
// finds names, folder paths, words and meaning in each directory, trims
// each kind as it goes, and fuses them at the end. nil when the query
// names nothing.
func OnePassStages(queryText string, lim WalkLimits, roots []string, vectors []QueryVector, refine *RefineSpec) []string {
	parsed := ParseSearchQuery(queryText)
	relDir := relDirSQL(roots, "")
	var nWhere, pWhere, expr, rank string
	var filter func(pre string) string
	if refine != nil {
		groups := ParseNameFilter(refine.Name)
		text := JSTrim(refine.Text)
		both := refine.Match == "all" && len(groups) > 0 && text != ""
		nWhere, pWhere = "0", "0"
		if !both && lim.Names > 0 {
			nWhere = nameFilterSQL(groups, roots, "", "name", "list")
		}
		if !both && lim.Paths > 0 {
			pWhere = nameFilterSQL(groups, roots, "", "either", "list")
		}
		if lim.Words > 0 {
			expr = text
		}
		var ws []string
		if len(groups) > 0 {
			for _, t := range groups[0] {
				if t.Kind == "word" && !t.Neg {
					ws = append(ws, t.Text)
				}
			}
		}
		rank = NameRank(ParseSearchQuery(strings.Join(ws, " ")))
		if both {
			f := func(pre string) string { return nameFilterSQL(groups, roots, pre, "either", "filter") }
			if f("") != "0" {
				filter = f
			}
		}
	} else {
		nWhere, pWhere = "0", "0"
		if lim.Names > 0 {
			nWhere = NameWhere(parsed)
		}
		if lim.Paths > 0 {
			pWhere = whereOver(relDir, parsed)
		}
		if lim.Words > 0 {
			expr = FtsExprFor(parsed)
		}
		rank = NameRank(parsed)
	}
	and := func(pre string) string {
		if filter == nil {
			return ""
		}
		return " AND (" + filter(pre) + ")"
	}
	vecs := vectors
	if lim.Vectors <= 0 {
		vecs = nil
	}
	studioWords := lim.StudioText && expr != ""
	studioVecs := lim.StudioVectors && len(vecs) > 0
	external := ""
	if lim.External != nil && *lim.External != "" && (expr != "" || len(vecs) > 0) {
		external = *lim.External
	}
	if nWhere == "0" && pWhere == "0" && !studioWords && !studioVecs && external == "" {
		return nil
	}
	const cols = "k TEXT, r REAL, p TEXT, n TEXT, s INT64, m INT64, x TEXT"
	const rp = "rpath(sname, sroll)||'/'||name"
	held := fmt.Sprintf("CASE k WHEN 'n' THEN %d WHEN 'p' THEN %d WHEN 'f' THEN %d ELSE %d END", lim.Names, lim.Paths, lim.Words, lim.Vectors*3)
	E := []string{fmt.Sprintf("DELETE FROM t WHERE rowid IN (SELECT rowid FROM (SELECT rowid, k, row_number() OVER (PARTITION BY k ORDER BY r, p) AS rn FROM t) WHERE rn > %s);", held)}
	names, paths := "", ""
	if nWhere != "0" {
		names = fmt.Sprintf("SELECT * FROM (SELECT 'n', %s, %s, name, size, mtime, '' FROM vrpentries WHERE %s ORDER BY 2, 3 LIMIT %d)", rank, rp, nWhere, lim.Names)
	}
	if pWhere != "0" {
		not := ""
		if nWhere != "0" {
			not = " AND NOT (" + nWhere + ")"
		}
		paths = fmt.Sprintf("SELECT * FROM (SELECT 'p', length(%s), %s, name, size, mtime, '' FROM vrpentries WHERE (%s)%s ORDER BY 2, 3 LIMIT %d)", relDir, rp, pWhere, not, lim.Paths)
	}
	if names != "" || paths != "" {
		var sel []string
		for _, x := range []string{names, paths} {
			if x != "" {
				sel = append(sel, x)
			}
		}
		E = append(E, "INSERT INTO t "+strings.Join(sel, " UNION ALL ")+";")
	}
	if studioWords {
		E = append(E, fmt.Sprintf("INSERT INTO t SELECT 'f', bm25(words), %s %s%s ORDER BY 2, 3 LIMIT %d;", ftsColumns, ftsFrom(expr), and(""), lim.WordsPerDir))
	}
	var ext []string
	if external != "" && expr != "" {
		ext = append(ext, "INSERT INTO t SELECT 'f', bm25(ext_bm25_by_file), rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, "+
			"replace(replace(snippet(ext_bm25_by_file, -1, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ') "+
			"FROM extdb.ext_bm25_by_file JOIN vrpentries e ON CAST(binode AS INT64) = e.inode AND CAST(bmtime AS INT64) = e.mtime "+
			fmt.Sprintf("WHERE ext_bm25_by_file MATCH %s%s ORDER BY 2, 3 LIMIT %d;", q(expr), and("e."), lim.WordsPerDir))
	}
	if external != "" && len(vecs) > 0 {
		ext = append(ext, "INSERT INTO t SELECT 'v', sqrt(2.0 * vec_distance_cosine(substr(x.embedding, 1, 1536), qv.qe)), rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, "+
			fmt.Sprintf("replace(replace(substr(x.chunk_content, 1, %d), char(10), ' '), char(13), ' ') ", PassageChars)+
			"FROM extdb.ext_vectors_by_file x JOIN qv ON x.vector_model_name LIKE qv.pat "+
			"JOIN vrpentries e ON CAST(x.vinode AS INT64) = e.inode AND CAST(x.vmtime AS INT64) = e.mtime "+
			fmt.Sprintf("WHERE length(x.embedding) >= 1536%s ORDER BY 2, 3 LIMIT %d;", and("e."), lim.VectorsPerDir))
	}
	if studioVecs {
		const qe = "(SELECT qe FROM qv WHERE (SELECT model FROM gvecmodel LIMIT 1) LIKE pat LIMIT 1)"
		if filter != nil {
			E = append(E, fmt.Sprintf("INSERT INTO t SELECT * FROM (SELECT 'v', vec_distance_l2(v.fp384, %s) AS d, rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, ", qe)+
				chunkSnippet+" "+
				fmt.Sprintf("FROM vrpentries e JOIN gchunks g ON g.tinode = e.inode JOIN gvec v ON v.cid = g.cid WHERE %s) WHERE d IS NOT NULL ORDER BY 2, 3 LIMIT %d;", filter("e."), lim.VectorsPerDir))
		} else {
			E = append(E, "INSERT INTO t SELECT 'v', k.distance, rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, "+
				chunkSnippet+" "+
				fmt.Sprintf("FROM (SELECT cid, distance FROM gvec WHERE fp384 MATCH %s ORDER BY distance LIMIT %d) k ", qe, lim.VectorsPerDir)+
				"JOIN gchunks g ON g.cid = k.cid JOIN vrpentries e ON e.inode = g.tinode;")
		}
	}
	capSQL := fmt.Sprintf("CASE k WHEN 'n' THEN %d WHEN 'p' THEN %d WHEN 'f' THEN %d ELSE %d END", lim.Names, lim.Paths, lim.Words, lim.Vectors)
	I := []string{"CREATE TABLE t (" + cols + ");"}
	if len(vecs) > 0 {
		vals := make([]string, len(vecs))
		for i, v := range vecs {
			vals[i] = fmt.Sprintf("(%s, X'%s')", q(v.Pattern), v.Hex)
		}
		I = append(I, "CREATE TABLE qv (pat TEXT, qe BLOB);", "INSERT INTO qv VALUES "+strings.Join(vals, ", ")+";")
	}
	out := []string{"-I", strings.Join(I, " "), "-K", "CREATE TABLE agg (" + cols + ");"}
	if len(ext) > 0 {
		out = append(out, "--external-copy", external, strings.Join(ext, " "))
	}
	return append(out,
		"-E", strings.Join(E, " "),
		"-J", "INSERT INTO agg SELECT * FROM t;",
		"-G", "WITH best AS (SELECT k, min(r) AS r, p, n, s, m, x FROM agg GROUP BY k, p), "+
			"ranked AS (SELECT *, row_number() OVER (PARTITION BY k ORDER BY r, p) AS rn FROM best), "+
			fmt.Sprintf("kept AS (SELECT * FROM ranked WHERE rn <= %s), ", capSQL)+
			fmt.Sprintf("fused AS (SELECT p AS fp, sum(1.0 / (%d.0 + rn)) AS score FROM kept GROUP BY p) ", RRFK)+
			"SELECT k, r, p, n, s, m, x FROM kept JOIN fused ON p = fp ORDER BY score DESC, p, k;",
	)
}
