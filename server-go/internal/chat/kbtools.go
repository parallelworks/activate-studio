package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libapi"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The knowledge base tools whose owners are on the port: labels and
// writing a file (tools.ts), and label suggestions (labeling.ts).

func init() {
	goExecutors["get_labels"] = (*Module).toolGetLabels
	goExecutors["write_kb_file"] = (*Module).toolWriteKBFile
	goExecutors["suggest_labels"] = (*Module).toolSuggestLabels
}

func kbTags() *kb.Tags {
	if kb.Default == nil {
		return nil
	}
	return kb.Default.Tags
}

func (m *Module) toolGetLabels(r *http.Request, _ map[string]any, _ ToolCtx) (*Outcome, error) {
	var lines []string
	n := 0
	if t := kbTags(); t != nil {
		labels, counts := t.Counts(r.Context())
		n = len(labels)
		for _, l := range labels {
			lines = append(lines, l+": "+itoaInt(counts[l]))
		}
	}
	result := strings.Join(lines, "\n")
	if result == "" {
		result = "No labels in use yet."
	}
	return &Outcome{Result: result, Summary: itoaInt(n) + " labels"}, nil
}

var edgeSlashes = regexp.MustCompile(`^/+|/+$`)

func (m *Module) toolWriteKBFile(r *http.Request, args map[string]any, tctx ToolCtx) (*Outcome, error) {
	rel := edgeSlashes.ReplaceAllString(argString(args, "path"), "")
	content := argString(args, "content")
	source := edgeSlashes.ReplaceAllString(jsTrim(argString(args, "source")), "")
	// A write that did not happen is failed, and its row in the chat says
	// why, not only that it did not.
	fail := func(result, summary string) (*Outcome, error) {
		return &Outcome{Result: result, Summary: summary + ": " + jsval.Slice(result, 160), Failed: true}, nil
	}
	if rel == "" || (content == "" && source == "") {
		return fail("path and content are required (or source in place of content: a Markdown file in the library to make a .pdf or .docx from)", "error")
	}
	if content != "" && source != "" {
		return fail("Give content or source, not both: source makes the file from a Markdown file already in the library.", "error")
	}
	// The library it goes to: the one the call names, else the chat's
	// (where.go). Written to the person's space as them.
	p, rel, why, ask, err := m.toolPlace(r, tctx, args, rel, !argTrue(args, "append") && !argTrue(args, "overwrite"))
	if err != nil {
		return fail(err.Error(), "refused")
	}
	if ask != nil {
		return fail(ask.askText(), "asks where")
	}
	if err := p.writable(); err != nil {
		return fail(err.Error(), "error")
	}
	if source != "" {
		if q, rest, ok := m.splitNamed(r, source); ok && q.id() == p.id() {
			source = rest
		}
	}
	ext := strings.ToLower(filepath.Ext(rel))
	also, bad := alsoFormats(args["also"])
	if bad != "" {
		return fail(bad, "error")
	}
	if len(also) > 0 && !isMarkdownExt(ext) {
		return fail("also makes a .pdf or .docx beside a Markdown file, and "+rel+" is not one. Write the report as a .md file with also, or write this file alone.", "error")
	}
	if len(also) > 0 && source != "" {
		return fail("also goes with content. To make another format from "+source+", call write_kb_file for that path with the same source.", "error")
	}
	adding := argTrue(args, "append")
	if adding && (source != "" || len(also) > 0) {
		return fail("append adds content to one file; it does not go with source or also.", "error")
	}
	if adding && (ext == ".pdf" || ext == ".docx") {
		return fail("A "+ext+" is made whole, not added to. Write its Markdown in parts to a .md file (append=true for each part after the first), then make "+rel+" with source set to that .md file.", "error")
	}
	if source != "" {
		if !docwrite.Handles(ext) {
			return fail("source makes a .pdf, .docx or .xlsx from a text file in the library. To copy a file, use copy_kb_files.", "error")
		}
		text, bad := m.sourceText(p, source)
		if bad != "" {
			return fail(bad, "error")
		}
		content = text
	}
	if jsval.Len(content) > 2*1024*1024 {
		return fail("Content over 2 MB; write it in parts or as multiple files.", "too large")
	}
	for _, part := range strings.Split(rel, "/") {
		if kb.ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return fail("Not writable: "+part+" is excluded.", "refused")
		}
	}
	if err := p.check(rel); err != nil {
		return fail(err.Error(), "refused")
	}
	// One write to a file at a time: an append reads the file and writes
	// it back, and two at once would lose or reorder a part.
	defer lockPath(p.lockKey(rel))()
	overwrite := argTrue(args, "overwrite")
	exists := p.exists(rel)
	var prevHeld []string
	if exists && ext == ".xlsx" && overwrite && !adding {
		if old, err := p.read(rel, 64<<20); err == nil {
			prevHeld = docwrite.Held(old)
		}
	}
	if exists && !overwrite && !adding {
		return fail(p.shown(rel)+" already exists. Pass overwrite=true to replace it, or choose another name, or append=true to add to it.", "exists")
	}
	m.note(tctx, p.id(), rel)
	limit := tctx.rowLimit(rel)
	if adding && exists {
		return m.appendKBFile(r, p, rel, ext, content, limit)
	}
	// The other formats asked for beside a Markdown file: checked before
	// anything is written, so the call writes all of them or none.
	stem := strings.TrimSuffix(rel, filepath.Ext(rel))
	type beside struct{ rel, ext string }
	var extra []beside
	for _, f := range also {
		orel := stem + "." + f
		if err := p.check(orel); err != nil {
			return fail(err.Error(), "refused")
		}
		if p.exists(orel) && !overwrite {
			return fail(p.shown(orel)+" already exists. Pass overwrite=true to replace it, or choose another name.", "exists")
		}
		extra = append(extra, beside{orel, "." + f})
	}
	// A PDF, Word or Excel file is made from what the model wrote
	// (Markdown, or rows); other office formats are refused with what to
	// write instead, so no text lands in a file named as a binary format.
	data, made := []byte(content), ""
	var missing, problems, style []string
	dropped := 0
	if why := docwrite.Unsupported(ext); why != "" {
		return fail(why, "refused")
	}
	// Images are found from the folder of the Markdown that names them.
	imageDir := path.Dir(rel)
	if source != "" {
		imageDir = path.Dir(source)
	}
	if imageDir == "." {
		imageDir = ""
	}
	if ext == ".xlsx" && limit > 0 {
		b, says, d, err := docwrite.RenderXLSXUpTo(content, limit)
		if err != nil {
			return fail("Could not write "+p.shown(rel)+": making the workbook: "+err.Error(), "error")
		}
		data, made, dropped = b, fmt.Sprintf("an Excel workbook of %d sheet%s (%s)", len(says), plural(len(says)), strings.Join(says, "; ")), d
	} else if docwrite.Handles(ext) {
		out, err := docwrite.Render(ext, content, p.images(imageDir))
		if err != nil {
			return fail("Could not write "+p.shown(rel)+": "+err.Error(), "error")
		}
		data, made, missing, problems, style = out.Data, out.Note, out.Missing, out.Problems, out.Style
	} else if isReport(ext) {
		style = docwrite.StyleCheck(content)
	}
	if err := p.write(rel, data); err != nil {
		if p.space {
			return fail("Could not write "+p.shown(rel)+": "+err.Error(), "error")
		}
		return nil, err
	}
	if source != "" {
		m.made.set(p.key(rel), p.key(source), data)
	} else {
		m.made.drop(p.key(rel))
	}
	var alsoMade []string
	links := []string{p.link(rel)}
	files := []conversations.File{p.file(rel)}
	for _, x := range extra {
		out, err := docwrite.Render(x.ext, content, p.images(imageDir))
		if err != nil {
			return fail("Wrote "+p.shown(rel)+", but could not make "+x.rel+": "+err.Error(), "error")
		}
		if err := p.write(x.rel, out.Data); err != nil {
			if p.space {
				return fail("Wrote "+p.shown(rel)+", but could not write "+p.shown(x.rel)+": "+err.Error(), "error")
			}
			return nil, err
		}
		m.made.set(p.key(x.rel), p.key(rel), out.Data)
		alsoMade = append(alsoMade, x.rel+" ("+out.Note+")")
		links = append(links, p.link(x.rel))
		files = append(files, p.file(x.rel))
		missing, problems = addNew(missing, out.Missing), addNew(problems, out.Problems)
		if len(style) == 0 {
			style = out.Style
		}
	}
	ms := p.reindex(r.Context(), rel)
	base := rel[strings.LastIndex(rel, "/")+1:]
	link := links[0]
	show := "show_in_viewer can display it."
	switch ext {
	case ".stl", ".step", ".stp", ".obj", ".glb", ".gltf":
		show = "Show the interactive 3D model inline in your reply with this markdown, then its link:\n![" + base + "](/?embed=model&path=" + encodeURIComponent(rel) + ")\n" + link + "\nIterate by rewriting the file with overwrite=true; the embed always renders the current version."
	case ".html", ".htm":
		show = "Show the page inline, sandboxed, with this markdown, then its link:\n![" + base + "](/?embed=html&path=" + encodeURIComponent(rel) + ")\n" + link + "\nThe sandbox allows inline scripts and styles only, no network requests, so keep the page self-contained."
	}
	wrote := itoaInt(jsval.Len(content)) + " bytes"
	if made != "" {
		wrote = made + ", " + itoaInt(len(data)) + " bytes"
		show = "Give the user its link: " + link + " It opens in the Library viewer."
		if ext == ".xlsx" {
			show = moreRows + show
			if dropped > 0 {
				show = rowsLeftOut(limit, limit, dropped) + show
			}
		}
	}
	if source != "" {
		wrote += ", from " + source
		show += " An edit to " + source + " with edit_kb_file makes it again."
	}
	if len(alsoMade) > 0 {
		wrote += "; made from it: " + strings.Join(alsoMade, ", ")
		show = "Give the user their links: " + strings.Join(links, " ") + " They open in the Library viewer. To change the report, call edit_kb_file on " + rel + ": the documents made from it are made again."
	}
	replaced := ""
	if exists {
		replaced = ", replacing the previous version"
		// A workbook replaced in full may have held rows added in parts:
		// the result says what was dropped, so a model that meant to add
		// rows sees it replaced them.
		if ext == ".xlsx" && prevHeld != nil {
			replaced += ", which held " + strings.Join(prevHeld, "; ") + " (overwrite=true replaces a workbook; append=true adds rows to it)"
		}
	}
	if len(missing) > 0 {
		// Said first, so the model fixes the paths and writes it again
		// (overwrite=true) before it hands the file over.
		show = "These images were not embedded, and their alt text stands in their place: " + strings.Join(missing, "; ") + ". Give each as a path in the library (relative to this file's folder, or from the library's top) and write the file again with overwrite=true. " + show
	}
	if len(problems) > 0 {
		show = "The document was made with these problems: " + strings.Join(problems, "; ") + ". A front matter option that was not understood kept its default. Correct the text or the options and make the document again (edit_kb_file on its Markdown, or this call again with overwrite=true), and tell the user of any that stand. " + show
	}
	summary := "wrote " + p.shown(rel)
	if ext == ".xlsx" {
		if h := docwrite.Held(data); len(h) > 0 {
			summary += ": " + strings.Join(h, "; ")
		}
		if dropped > 0 {
			summary += droppedSummary(limit, dropped)
		}
	}
	indexed := " and re-indexed in " + itoaInt(int(ms)) + " ms. "
	if p.space {
		indexed = "; its index is updated shortly. "
	}
	where := ""
	if m.spacesFor(r) {
		// The library is said, with why when the chat chose it, and the
		// link opens the file there.
		where = "Saved to " + links[0] + "."
		if why != "" {
			where += " It went there because " + why + "; say so, and that it can be moved."
		}
		where += " Tell the user where it is with that link. "
	}
	return &Outcome{
		Result:  "Wrote " + p.shown(rel) + " (" + wrote + ")" + replaced + indexed + where + show + styleAdvice(style),
		Summary: summary,
		Files:   files,
	}, nil
}

// isReport reports a suffix of prose written for people to read, whose
// writing the style check reads: Markdown, and the PDF and Word documents
// made from it.
func isReport(ext string) bool {
	switch ext {
	case ".md", ".markdown", ".pdf", ".docx":
		return true
	}
	return false
}

// styleAdvice is what the style check found in a report's writing, for the
// model to weigh before it hands the file over, or "".
func styleAdvice(style []string) string {
	if len(style) == 0 {
		return ""
	}
	return " A check of the writing found: " + strings.Join(style, "; ") + ". This is advice for plain, factual writing: where it applies, revise the text with edit_kb_file (documents made from it are made again) before you hand the file over; where a word is the right one, keep it."
}

/* ---- label suggestions (labeling.ts) ---- */

const (
	labelHeadChars = 1200
	labelBatch     = 12
)

type labelProposal struct {
	Path   string   `json:"path"`
	Labels []string `json:"labels"`
	Why    string   `json:"why"`
}

type labelSuggestions struct {
	Proposals  []labelProposal   `json:"proposals"`
	NewLabels  []string          `json:"newLabels"`
	Vocabulary []*jsonobj.Object `json:"vocabulary"`
	Considered int               `json:"considered"`
	Model      string            `json:"model"`
}

var wsRun = regexp.MustCompile(`[\s\x{a0}\x{feff}\x{2028}\x{2029}]+`)

// labelTarget is the library label suggestions read: the main one, a
// library on this host, or a library on another system, whose folders and
// files are read through the paths the tree and the file view use.
type labelTarget struct {
	r   *http.Request
	lib kb.Library
}

func (t labelTarget) primary() bool { return t.lib.ID == kb.PrimaryID }

func (m *Module) headText(t labelTarget, rel string) string {
	if !t.primary() && kb.Default != nil {
		return jsval.Slice(m.libraryHead(t, rel), labelHeadChars)
	}
	if b, err := os.ReadFile(kb.RootsFrom(m.A.Cfg).ExtractPath(rel)); err == nil && jsTrim(string(b)) != "" {
		return jsval.Slice(string(b), labelHeadChars)
	}
	fc, err := readFileContent(m, rel)
	if err != nil || fc == nil {
		return ""
	}
	c, _ := fc.Get("content")
	s, _ := c.(string)
	return jsval.Slice(s, labelHeadChars)
}

// libraryHead is a file's text in a library other than the main one. For
// one on another system or read through another host, the text its index
// holds comes first (one query there, and the only text of a PDF), then
// the file itself as the file view reads it.
func (m *Module) libraryHead(t labelTarget, rel string) string {
	if (t.lib.Site != "" || t.lib.ReadThrough) && t.lib.Indexed && m.Corpus != nil && m.Corpus.Search != nil {
		dir, name := "", rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			dir, name = rel[:i], rel[i+1:]
		}
		if res := m.Corpus.Search.IndexedTextState(t.r.Context(), t.lib.IndexRoot, dir, name); res.State == "text" {
			return res.Text
		}
	}
	return kb.Default.LibraryFileText(t.r, t.lib, rel)
}

// labelEntry is a directory entry as the walk needs it.
type labelEntry struct {
	name string
	dir  bool
}

func (m *Module) labelEntries(t labelTarget, rel string) ([]labelEntry, error) {
	var out []labelEntry
	if t.primary() || kb.Default == nil {
		abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !ownfile.At(filepath.Join(abs, e.Name())) {
				out = append(out, labelEntry{e.Name(), e.IsDir()})
			}
		}
	} else {
		entries, err := kb.Default.LibraryListing(t.r, t.lib, rel)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			out = append(out, labelEntry{e.Name, e.Type == "dir"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return kb.LocaleCompare(out[i].name, out[j].name) < 0 })
	return out, nil
}

func (m *Module) labelCandidates(ctx context.Context, t labelTarget, dir string, limit int, includeLabelled bool) ([]string, error) {
	var out []string
	var walk func(rel string, depth int) error
	walk = func(rel string, depth int) error {
		if len(out) >= limit || depth > 6 {
			return nil
		}
		entries, err := m.labelEntries(t, rel)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(out) >= limit {
				return nil
			}
			if strings.HasPrefix(e.name, ".") {
				continue
			}
			child := e.name
			if rel != "" {
				child = rel + "/" + e.name
			}
			if e.dir {
				if kb.ExcludeDirs[e.name] {
					continue
				}
				walk(child, depth+1)
			} else {
				out = append(out, child)
			}
		}
		return nil
	}
	if !t.primary() && t.lib.Site == "" && t.lib.SourceRoot == "" {
		return nil, apierr.New(409, "%s has no files on this host, so labels cannot be suggested for it", t.lib.Label)
	}
	// The main library's missing folder lists as nothing, as in Node; a
	// library elsewhere that cannot be listed (no key, the system refused)
	// says why.
	if err := walk(edgeSlashes.ReplaceAllString(dir, ""), 0); err != nil && !t.primary() {
		return nil, err
	}
	if includeLabelled {
		return out, nil
	}
	var unlabelled []string
	for _, rel := range out {
		if kb.Default != nil && kb.Default.LibraryLabelled(ctx, t.lib, rel) {
			continue
		}
		unlabelled = append(unlabelled, rel)
	}
	return unlabelled, nil
}

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func parseLabelJSON(text string) (any, bool) {
	body := text
	if mm := fencedJSON.FindStringSubmatch(text); mm != nil {
		body = mm[1]
	}
	start := strings.IndexAny(body, "[{")
	if start < 0 {
		return nil, false
	}
	end := max(strings.LastIndex(body, "]"), strings.LastIndex(body, "}"))
	if end < start {
		return nil, false
	}
	var v any
	if json.Unmarshal([]byte(body[start:end+1]), &v) != nil {
		return nil, false
	}
	return v, true
}

func (m *Module) suggestLabels(ctx context.Context, t labelTarget, dir string, limit int, includeLabelled bool, model string, cred credArgs) (*labelSuggestions, error) {
	limit = min(max(limit, 1), 200)
	files, err := m.labelCandidates(ctx, t, dir, limit, includeLabelled)
	if err != nil {
		return nil, &labelReadError{err}
	}
	var known []string
	counts := map[string]int{}
	if t.primary() {
		if tags := kbTags(); tags != nil {
			known, counts = tags.Counts(ctx)
		}
	} else if kb.Default != nil {
		// Another library reuses the labels of every library, its own
		// included, as the label vocabulary lists them.
		if known, counts, err = kb.Default.LabelVocabulary(t.r); err != nil {
			return nil, &labelReadError{err}
		}
	}
	// sorted by count, stable over first-seen order, as Array.sort is
	vocab := append([]string{}, known...)
	sortStableBy(vocab, func(a, b string) bool { return counts[a] > counts[b] })
	vocabulary := []*jsonobj.Object{}
	for _, tag := range vocab {
		vocabulary = append(vocabulary, reqbody.O("tag", tag, "count", counts[tag]))
	}
	known = vocab
	proposals := []labelProposal{}
	for i := 0; i < len(files); i += labelBatch {
		batch := files[i:min(i+labelBatch, len(files))]
		used := "The corpus has no labels yet."
		if len(known) > 0 {
			used = "Labels already in use: " + strings.Join(known, ", ") + "."
		}
		system := strings.Join([]string{
			"You label documents in a knowledge base so they can be filtered later.",
			"Reuse an existing label whenever it fits; propose a new one only when nothing existing describes the material.",
			"Labels are lowercase, hyphenated, one or two words, and describe subject or document type, never the filename.",
			"Give each file one to three labels. Say nothing about files you cannot judge; leave their labels empty.",
			used,
			`Reply with JSON only: [{"path": "...", "labels": ["..."], "why": "short reason"}]`,
		}, "\n")
		var parts []string
		for _, rel := range batch {
			text := jsval.Slice(wsRun.ReplaceAllString(m.headText(t, rel), " "), labelHeadChars)
			if text == "" {
				text = "(no extracted text)"
			}
			parts = append(parts, "PATH: "+rel+"\nNAME: "+filepath.Base(rel)+"\nTEXT: "+text)
		}
		user := strings.Join(parts, "\n\n---\n\n")
		body := reqbody.O("model", model, "messages", []any{reqbody.O("role", "system", "content", system), reqbody.O("role", "user", "content", user)})
		turn, err := m.LLM.StreamTurn(ctx, body, "", llm.Callbacks{OnContent: func(string) {}}, cred.key, cred.base, cred.endpoint)
		if err != nil {
			return nil, err
		}
		v, ok := parseLabelJSON(turn.Content)
		rows, isArr := v.([]any)
		if !ok || !isArr {
			continue
		}
		for _, x := range rows {
			row, _ := x.(map[string]any)
			rel := reqbody.JSString(row["path"])
			if row["path"] == nil || !contains(batch, rel) {
				continue
			}
			var labels []string
			if ls, ok := row["labels"].([]any); ok {
				for _, l := range ls {
					if n := normalizeLabel(l); n != "" && !contains(labels, n) {
						labels = append(labels, n)
					}
				}
			}
			if len(labels) > 3 {
				labels = labels[:3]
			}
			if len(labels) == 0 {
				continue
			}
			why := ""
			if row["why"] != nil {
				why = jsval.Slice(reqbody.JSString(row["why"]), 200)
			}
			proposals = append(proposals, labelProposal{Path: rel, Labels: labels, Why: why})
		}
	}
	newLabels := []string{}
	for _, p := range proposals {
		for _, l := range p.Labels {
			if !contains(newLabels, l) && !contains(known, l) {
				newLabels = append(newLabels, l)
			}
		}
	}
	return &labelSuggestions{Proposals: proposals, NewLabels: newLabels, Vocabulary: vocabulary, Considered: len(files), Model: model}, nil
}

var (
	labelStrip = regexp.MustCompile(`[^a-z0-9 _-]`)
	labelSpace = regexp.MustCompile(`\s+`)
	labelEdge  = regexp.MustCompile(`^-+|-+$`)
)

func normalizeLabel(raw any) string {
	s := ""
	if raw != nil {
		s = reqbody.JSString(raw)
	}
	s = jsTrim(strings.ToLower(s))
	s = labelStrip.ReplaceAllString(s, "")
	s = labelSpace.ReplaceAllString(s, "-")
	s = labelEdge.ReplaceAllString(s, "")
	return jsval.Slice(s, 40)
}

func sortStableBy(s []string, less func(a, b string) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// credArgs is a credential as the gateway call takes it.
type credArgs struct {
	key, base string
	endpoint  *modelkeys.Endpoint
}

func (m *Module) credArgsOf(r *http.Request) (credArgs, bool) {
	c := m.cred(r)
	if c == nil {
		return credArgs{}, false
	}
	return credArgs{key: c.Key, base: c.Base(), endpoint: c.Endpoint}, true
}

func (m *Module) toolSuggestLabels(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	model := deref(ctx.Model)
	if model == "" {
		model = m.eff().RagDefaultModel
	}
	if model == "" {
		return &Outcome{Result: "No model is available for labelling on this deployment.", Summary: "no labelling model"}, nil
	}
	limit := int(reqbody.JSNumber(args["limit"]))
	if limit == 0 {
		limit = 40
	}
	// The library the chat's Scope selects, as apply_labels labels it;
	// the main one when all are selected or it is among those selected,
	// else the first of several.
	t := labelTarget{r: r, lib: kb.Library{ID: kb.PrimaryID}}
	scoped, err := defaultLibrary(ctx)
	if err != nil {
		scoped = ctx.Library[0]
	}
	if scoped != kb.PrimaryID {
		lib, err := m.namedLibrary(scoped, ctx)
		if err != nil {
			return nil, &toolError{err.Error()}
		}
		t.lib = libapi.ToKB(lib)
	}
	dir := argString(args, "dir")
	if t.lib.ID == kb.PrimaryID {
		dir = m.mainRel(r, dir)
	}
	out, err := m.suggestLabels(r.Context(), t, dir, limit, reqbody.Truthy(args["include_labelled"]), model, credArgs{key: deref(ctx.UserKey)})
	var re *labelReadError
	if errors.As(err, &re) {
		return nil, &toolError{re.err.Error()}
	}
	if err != nil {
		return nil, err
	}
	if len(out.Proposals) == 0 {
		return &Outcome{Result: "Looked at " + itoaInt(out.Considered) + " files and had nothing to propose.", Summary: "no proposals"}, nil
	}
	var lines []string
	for _, p := range out.Proposals {
		l := p.Path + ": [" + strings.Join(p.Labels, ", ") + "]"
		if p.Why != "" {
			l += " (" + p.Why + ")"
		}
		lines = append(lines, l)
	}
	fresh := ""
	if len(out.NewLabels) > 0 {
		fresh = "\nNew labels proposed: " + strings.Join(out.NewLabels, ", ")
	}
	return &Outcome{
		Result:  "Proposals for " + itoaInt(len(out.Proposals)) + " of " + itoaInt(out.Considered) + " unlabelled files. Nothing has been applied; use apply_labels once the user agrees." + fresh + "\n" + strings.Join(lines, "\n"),
		Summary: itoaInt(len(out.Proposals)) + " label proposals",
	}, nil
}

// labelSuggestionsRoute is POST /api/kb/label-suggestions.
func (m *Module) labelSuggestionsRoute(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is Node's")
		return nil
	}
	b, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	eff := m.eff()
	if !eff.AiEnabled {
		return apierr.New(409, "AI features are off in this Studio, so labels are not suggested.")
	}
	// Where each person uses their own credential, the Studio's is never
	// used for them: without one they are told what the Assistant says.
	if m.personalRequired(m.cred(r)) {
		return apierr.New(403, "%s", m.signInFirstFor(r))
	}
	cred, hasCred := m.credArgsOf(r)
	model := b.Str("model")
	if model == "" {
		model = eff.RagDefaultModel
	}
	if model == "" {
		key := cred.key
		if !hasCred {
			key = ""
		}
		listing, lerr := m.LLM.ListModels(r.Context(), key, cred.base, cred.endpoint)
		if lerr == nil && listing != nil {
			for _, it := range listing.Items {
				if id := llm.ObjString(it, "id"); id != "" {
					model = id
					break
				}
			}
		}
		if model == "" {
			return apierr.New(400, "No model is available to this deployment for labelling. Add your model credential in Settings, Accounts.")
		}
	}
	limit := 40
	if v := b.Get("limit"); v != nil {
		limit = int(reqbody.JSNumber(v))
	}
	t, dir, err := m.labelTargetOf(r, b.Str("library"), b.Str("dir"))
	if err != nil {
		return err
	}
	out, err := m.suggestLabels(r.Context(), t, dir, limit, reqbody.Truthy(b.Get("includeLabelled")), model, cred)
	var re *labelReadError
	if errors.As(err, &re) {
		return re.err
	}
	if err != nil {
		detail := jsval.Slice(err.Error(), 160)
		if hasCred {
			return apierr.New(502, "The model refused the request (%s). Try a different model on the RAG endpoint settings page.", detail)
		}
		return apierr.New(502, "No personal model credential is loaded, and the deployment credential was refused (%s). Add your key in Settings, Accounts.", detail)
	}
	return reqbody.OK(w, out)
}

// labelReadError is a library that could not be read for suggestions,
// told apart from the model refusing the request.
type labelReadError struct{ err error }

func (e *labelReadError) Error() string { return e.err.Error() }

// labelTargetOf is the library and folder a request names: the body's
// library (the Library view sends the one shown, or the one a path of All
// belongs to), the main one when it names none or all.
func (m *Module) labelTargetOf(r *http.Request, library, dir string) (labelTarget, string, error) {
	t := labelTarget{r: r, lib: kb.Library{ID: kb.PrimaryID}}
	want := strings.ToLower(jsTrim(library))
	if want == "" || want == kb.PrimaryID || kb.Default == nil {
		return t, dir, nil
	}
	if want == kb.All && splitFirst(dir) == "" {
		return t, dir, nil
	}
	lib, rel, err := kb.Default.LabelLibrary(r, want, dir)
	if err != nil {
		return t, "", err
	}
	t.lib = lib
	return t, rel, nil
}

func splitFirst(p string) string {
	p = edgeSlashes.ReplaceAllString(p, "")
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

// appendKBFile adds a part to a file already in the library: text at its
// end, rows to a workbook's sheets.
func (m *Module) appendKBFile(r *http.Request, p place, rel, ext, content string, limit int) (*Outcome, error) {
	old, err := p.read(rel, 64<<20)
	if err != nil {
		return &Outcome{Result: "Could not read " + p.shown(rel) + " to add to it: " + err.Error(), Summary: "error"}, nil
	}
	var data []byte
	added := itoaInt(jsval.Len(content)) + " bytes"
	next, dropped := nextPart, 0
	if ext == ".xlsx" {
		b, sheets, rows, d, err := docwrite.AppendXLSX(old, content, limit)
		if err != nil {
			return &Outcome{Result: "Could not add to " + rel + ": " + err.Error(), Summary: "error"}, nil
		}
		data, dropped = b, d
		added = itoaInt(rows) + " rows; the workbook now holds: " + strings.Join(sheets, "; ")
		if dropped > 0 {
			next = rowsLeftOut(limit, rows, dropped) + "Give the user its link: "
		}
	} else {
		data = append(old, content...)
	}
	if len(data) > 32*1024*1024 {
		return &Outcome{Result: rel + " would be over 32 MB; write the rest as another file.", Summary: "too large"}, nil
	}
	if err := p.write(rel, data); err != nil {
		if p.space {
			return &Outcome{Result: "Could not add to " + p.shown(rel) + ": " + err.Error(), Summary: "error", Failed: true}, nil
		}
		return nil, err
	}
	m.made.drop(p.key(rel))
	ms := p.reindex(r.Context(), rel)
	return &Outcome{
		Result:  "Added to " + p.shown(rel) + " (" + added + "; " + itoaInt(len(data)) + " bytes in all) and re-indexed in " + itoaInt(int(ms)) + " ms. " + next + p.link(rel),
		Summary: "added to " + p.shown(rel) + heldSummary(ext, data) + droppedSummary(limit, dropped),
		Files:   []conversations.File{p.file(rel)},
	}, nil
}

// rowLimit is the most rows after its header a workbook written to rel
// may hold: the count the user's message asked for when it asks for a
// new workbook (by this name, or naming none), else 0 for no limit.
func (t ToolCtx) rowLimit(rel string) int {
	if t.RowsAsked <= 0 || !strings.EqualFold(filepath.Ext(rel), ".xlsx") {
		return 0
	}
	if t.RowsFile != "" && !strings.EqualFold(path.Base(t.RowsFile), path.Base(rel)) {
		return 0
	}
	return t.RowsAsked
}

// rowsLeftOut is said when a workbook write had more rows than the user
// asked for: the rows past the count were not written, so the parts sent
// at once with it stop at the count, and the workbook is finished.
func rowsLeftOut(asked, added, dropped int) string {
	if added <= 0 {
		return fmt.Sprintf("The user asked for %d rows and the workbook already held them, so this part's %d row%s %s not written: it holds the %d asked for and is finished. Send no more rows; tell the user it holds %d. ", asked, dropped, plural(dropped), wereWas(dropped), asked, asked)
	}
	return fmt.Sprintf("The user asked for %d rows, so the last %d row%s of this part %s not written: the workbook holds the %d asked for and is finished. Send no more rows; tell the user it holds %d. ", asked, dropped, plural(dropped), wereWas(dropped), asked, asked)
}

func wereWas(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func droppedSummary(asked, dropped int) string {
	if dropped <= 0 {
		return ""
	}
	return fmt.Sprintf("; %d row%s past the %d asked %s not written, so the workbook is finished", dropped, plural(dropped), asked, wereWas(dropped))
}

// moreRows is said after a workbook is written: a sheet that holds fewer
// rows than were asked for is not finished, and the model adds the rest
// itself rather than leaving the user a partial file.
const moreRows = "If the user asked for more rows than a sheet now holds, the workbook is not finished: send the next rows now with write_kb_file, the same path, append=true and the same sheet name, part after part until it holds them all, before you reply to the user. "

// nextPart is said after a part is added to a file.
const nextPart = "If the file does not yet hold all that was asked for, send the next part now the same way (append=true), before you reply to the user; once the last part is in, give the user its link: "

// argTrue reads a yes/no argument as a model may write it: true, or the
// text "true" or "yes", or 1.
func argTrue(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "yes" || s == "1"
	case float64:
		return v == 1
	case json.Number:
		return v.String() == "1"
	}
	return false
}

// heldSummary is what a workbook's sheets hold, for its row in the chat.
func heldSummary(ext string, data []byte) string {
	if ext != ".xlsx" {
		return ""
	}
	if h := docwrite.Held(data); len(h) > 0 {
		return ": " + strings.Join(h, "; ")
	}
	return ""
}

var pathLocks sync.Map

// lockPath holds a library file for one write and returns its release.
func lockPath(abs string) func() {
	v, _ := pathLocks.LoadOrStore(abs, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
