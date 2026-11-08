package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
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

func (m *Module) toolWriteKBFile(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	rel := edgeSlashes.ReplaceAllString(argString(args, "path"), "")
	content := argString(args, "content")
	if rel == "" || content == "" {
		return &Outcome{Result: "path and content are required", Summary: "error"}, nil
	}
	if jsval.Len(content) > 2*1024*1024 {
		return &Outcome{Result: "Content over 2 MB; write it in parts or as multiple files.", Summary: "too large"}, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if kb.ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return &Outcome{Result: "Not writable: " + part + " is excluded.", Summary: "refused"}, nil
		}
	}
	abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
	if err != nil {
		return &Outcome{Result: err.Error(), Summary: "refused"}, nil
	}
	_, statErr := os.Stat(abs)
	exists := statErr == nil
	if exists && args["overwrite"] != true {
		return &Outcome{Result: rel + " already exists. Pass overwrite=true to replace it, or choose another name.", Summary: "exists"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, nodeFSError(err, "mkdir", filepath.Dir(abs))
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return nil, nodeFSError(err, "open", abs)
	}
	ms, _ := kb.ReindexForFile(r.Context(), kb.ProviderIndexer{A: m.A}, rel)
	ext := strings.ToLower(filepath.Ext(rel))
	base := rel[strings.LastIndex(rel, "/")+1:]
	link := "[" + rel + "](#open=file:" + strings.ReplaceAll(rel, " ", "%20") + ")"
	show := "show_in_viewer can display it."
	switch ext {
	case ".stl", ".step", ".stp", ".obj", ".glb", ".gltf":
		show = "Show the interactive 3D model inline in your reply with this markdown, then its link:\n![" + base + "](/?embed=model&path=" + encodeURIComponent(rel) + ")\n" + link + "\nIterate by rewriting the file with overwrite=true; the embed always renders the current version."
	case ".html", ".htm":
		show = "Show the page inline, sandboxed, with this markdown, then its link:\n![" + base + "](/?embed=html&path=" + encodeURIComponent(rel) + ")\n" + link + "\nThe sandbox allows inline scripts and styles only, no network requests, so keep the page self-contained."
	}
	replaced := ""
	if exists {
		replaced = ", replacing the previous version"
	}
	return &Outcome{
		Result:  "Wrote " + rel + " (" + itoaInt(jsval.Len(content)) + " bytes)" + replaced + " and re-indexed in " + itoaInt(int(ms)) + " ms. " + show,
		Summary: "wrote " + rel,
	}, nil
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

func (m *Module) headText(rel string) string {
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

func (m *Module) labelCandidates(ctx context.Context, dir string, limit int, includeLabelled bool) []string {
	var out []string
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		if len(out) >= limit || depth > 6 {
			return
		}
		abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
		if err != nil {
			return
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		sort.SliceStable(entries, func(i, j int) bool { return kb.LocaleCompare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, e := range entries {
			if len(out) >= limit {
				return
			}
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := e.Name()
			if rel != "" {
				child = rel + "/" + e.Name()
			}
			if e.IsDir() {
				if kb.ExcludeDirs[e.Name()] {
					continue
				}
				walk(child, depth+1)
			} else {
				out = append(out, child)
			}
		}
	}
	walk(edgeSlashes.ReplaceAllString(dir, ""), 0)
	if includeLabelled {
		return out
	}
	t := kbTags()
	var unlabelled []string
	for _, rel := range out {
		if t != nil {
			eff := t.Effective(ctx, rel)
			own, _ := eff.Get("own")
			inh, _ := eff.Get("inherited")
			if len(own.([]string)) > 0 || len(inh.([]string)) > 0 {
				continue
			}
		}
		unlabelled = append(unlabelled, rel)
	}
	return unlabelled
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

func (m *Module) suggestLabels(ctx context.Context, dir string, limit int, includeLabelled bool, model string, cred credArgs) (*labelSuggestions, error) {
	limit = min(max(limit, 1), 200)
	files := m.labelCandidates(ctx, dir, limit, includeLabelled)
	var known []string
	counts := map[string]int{}
	if t := kbTags(); t != nil {
		known, counts = t.Counts(ctx)
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
			text := jsval.Slice(wsRun.ReplaceAllString(m.headText(rel), " "), labelHeadChars)
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
	out, err := m.suggestLabels(r.Context(), argString(args, "dir"), limit, reqbody.Truthy(args["include_labelled"]), model, credArgs{key: deref(ctx.UserKey)})
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
			return apierr.New(400, "No model is available to this deployment for labelling. Add your model credential in Settings, Model access.")
		}
	}
	limit := 40
	if v := b.Get("limit"); v != nil {
		limit = int(reqbody.JSNumber(v))
	}
	out, err := m.suggestLabels(r.Context(), b.Str("dir"), limit, reqbody.Truthy(b.Get("includeLabelled")), model, cred)
	if err != nil {
		detail := jsval.Slice(err.Error(), 160)
		if hasCred {
			return apierr.New(502, "The model refused the request (%s). Try a different model on the RAG endpoint settings page.", detail)
		}
		return apierr.New(502, "No personal model credential is loaded, and the deployment credential was refused (%s). Add your key in Settings, Model access.", detail)
	}
	return reqbody.OK(w, out)
}
