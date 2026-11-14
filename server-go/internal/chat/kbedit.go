package chat

import (
	"bytes"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// Changing a text file in the main library in place, and making the
// documents that were made from it again. A report is corrected many
// times after it is written; sending the whole of it again for each
// correction, once per format, costs minutes of the model's time. An edit
// names only the text to change.

func init() {
	goExecutors["edit_kb_file"] = (*Module).toolEditKBFile
}

const maxEditBytes = 2 * 1024 * 1024

func isMarkdownExt(ext string) bool { return ext == ".md" || ext == ".markdown" }

// alsoFormats reads the formats to make beside a Markdown file: "pdf",
// "docx", given as a list or as one string, each once.
func alsoFormats(v any) ([]string, string) {
	var raw []string
	switch x := v.(type) {
	case nil:
		return nil, ""
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	case []string:
		raw = x
	case string:
		raw = strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	default:
		return nil, "also is a list of formats: pdf, docx."
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range raw {
		f = strings.TrimPrefix(strings.ToLower(jsTrim(f)), ".")
		if f == "" || seen[f] {
			continue
		}
		if f == "word" {
			f = "docx"
		}
		if f != "pdf" && f != "docx" {
			return nil, "also makes pdf and docx; " + f + " is not one of them."
		}
		seen[f] = true
		out = append(out, f)
	}
	return out, ""
}

func libraryLink(rel string) string {
	return "[" + rel + "](#open=file:" + strings.ReplaceAll(rel, " ", "%20") + ")"
}

// addNew appends the items of more that list does not hold.
func addNew(list, more []string) []string {
	for _, m := range more {
		found := false
		for _, l := range list {
			if l == m {
				found = true
				break
			}
		}
		if !found {
			list = append(list, m)
		}
	}
	return list
}

// writeLibraryFile writes one file of the main library, making its folder.
func (m *Module) writeLibraryFile(abs string, data []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nodeFSError(err, "mkdir", filepath.Dir(abs))
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return nodeFSError(err, "open", abs)
	}
	return nil
}

// libraryText reads a text file of the main library, or says why not.
// wait is how long to wait for a file that is not there yet: the call
// that writes it may run beside this one.
func (m *Module) libraryText(rel string, wait time.Duration) (string, string) {
	for _, part := range strings.Split(rel, "/") {
		if kb.ExcludeDirs[part] || kb.HiddenName(part) {
			return "", "Not readable: " + part + " is excluded."
		}
	}
	abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
	if err != nil {
		return "", err.Error()
	}
	deadline := time.Now().Add(wait)
	for {
		st, err := os.Stat(abs)
		if err == nil {
			if st.IsDir() {
				return "", rel + " is a folder."
			}
			if st.Size() > maxEditBytes {
				return "", rel + " is over 2 MB."
			}
			break
		}
		if time.Now().After(deadline) {
			return "", rel + " is not in the library. list_kb_dir shows what is there."
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.writeMu.Lock()
	b, err := os.ReadFile(abs)
	m.writeMu.Unlock()
	if err != nil {
		return "", "Could not read " + rel + "."
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", rel + " is not a text file."
	}
	return string(b), ""
}

// sourceText is the Markdown (or rows) a document is made from.
func (m *Module) sourceText(rel string) (string, string) {
	if ext := strings.ToLower(filepath.Ext(rel)); docwrite.Handles(ext) || docwrite.Unsupported(ext) != "" {
		return "", "source is a text file, such as the report's Markdown; " + rel + " is not one."
	}
	text, why := m.libraryText(rel, 3*time.Second)
	if why == "" && strings.TrimSpace(text) == "" {
		return "", rel + " is empty."
	}
	return text, why
}

type textEdit struct {
	find, replace string
	all           bool
}

// editArgs reads the edits of a call: a list of {find, replace, all}, or
// one find and replace given at the top.
func editArgs(args map[string]any) ([]textEdit, string) {
	var out []textEdit
	one := func(o map[string]any) string {
		find := argString(o, "find")
		if find == "" {
			return "each edit needs find: the text to replace, exactly as it is in the file."
		}
		out = append(out, textEdit{find, argString(o, "replace"), o["all"] == true})
		return ""
	}
	switch x := args["edits"].(type) {
	case nil:
	case []any:
		for _, e := range x {
			o, ok := e.(map[string]any)
			if !ok {
				return nil, "edits is a list of {find, replace}."
			}
			if why := one(o); why != "" {
				return nil, why
			}
		}
	case map[string]any:
		if why := one(x); why != "" {
			return nil, why
		}
	default:
		return nil, "edits is a list of {find, replace}."
	}
	if argString(args, "find") != "" {
		if why := one(args); why != "" {
			return nil, why
		}
	}
	return out, ""
}

var spaceRun = regexp.MustCompile(`\s+`)

// loosely is find as a pattern that matches it whatever the spacing: a
// model that read a wrapped paragraph gives its line ends as spaces.
func loosely(find string) *regexp.Regexp {
	words := strings.Fields(find)
	if len(words) == 0 {
		return nil
	}
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	re, err := regexp.Compile(strings.Join(words, `\s+`))
	if err != nil {
		return nil
	}
	return re
}

func clip(s string, n int) string {
	s = spaceRun.ReplaceAllString(strings.TrimSpace(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// applyEdits makes each edit in turn on text. It returns the new text
// and how many places changed, or why an edit could not be made; then
// nothing is to be written.
func applyEdits(text string, edits []textEdit) (string, int, string) {
	changed := 0
	for i, e := range edits {
		which := "Edit " + itoaInt(i+1) + " of " + itoaInt(len(edits))
		find := e.find
		n := strings.Count(text, find)
		if n == 0 && strings.Contains(text, "\r\n") && !strings.Contains(find, "\r\n") {
			// The file has Windows line ends and the model wrote plain ones.
			if crlf := strings.ReplaceAll(find, "\n", "\r\n"); strings.Contains(text, crlf) {
				find, n = crlf, strings.Count(text, crlf)
				e.replace = strings.ReplaceAll(strings.ReplaceAll(e.replace, "\r\n", "\n"), "\n", "\r\n")
			}
		}
		if n == 0 {
			re := loosely(e.find)
			var locs [][]int
			if re != nil {
				locs = re.FindAllStringIndex(text, -1)
			}
			switch {
			case len(locs) == 0:
				return "", 0, which + " did not apply: this text is not in the file: \"" + clip(e.find, 100) + "\". Read the file with read_kb_file and give the text exactly as it is there. Nothing was changed."
			case len(locs) > 1 && !e.all:
				return "", 0, which + " did not apply: \"" + clip(e.find, 100) + "\" is in the file " + itoaInt(len(locs)) + " times. Give more of the text around the one meant, or all=true for every one. Nothing was changed."
			}
			var b strings.Builder
			last := 0
			for _, l := range locs {
				b.WriteString(text[last:l[0]])
				b.WriteString(e.replace)
				last = l[1]
			}
			b.WriteString(text[last:])
			text = b.String()
			changed += len(locs)
			continue
		}
		if n > 1 && !e.all {
			return "", 0, which + " did not apply: \"" + clip(e.find, 100) + "\" is in the file " + itoaInt(n) + " times. Give more of the text around the one meant, or all=true for every one. Nothing was changed."
		}
		text = strings.ReplaceAll(text, find, e.replace)
		changed += n
	}
	return text, changed, ""
}

func (m *Module) toolEditKBFile(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	fail := func(result string) (*Outcome, error) {
		return &Outcome{Result: result, Summary: "error", Failed: true}, nil
	}
	rel := edgeSlashes.ReplaceAllString(jsTrim(argString(args, "path")), "")
	if rel == "" {
		return fail("path is required: the library file to edit.")
	}
	edits, why := editArgs(args)
	if why != "" {
		return fail(why)
	}
	add := argString(args, "append")
	if len(edits) == 0 && add == "" {
		return fail("Give edits, a list of {find, replace}, or append, the text to add at the end.")
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if docwrite.Handles(ext) || docwrite.Unsupported(ext) != "" {
		if rec, ok := m.made.get(rel); ok {
			return fail(rel + " is made from " + rec.Source + ". Edit " + rec.Source + " with edit_kb_file and " + rel + " is made again.")
		}
		return fail("A " + ext + " file is not edited in place. Write it again with write_kb_file; or write the report as a Markdown file with also, so that later changes are edits to the Markdown.")
	}
	if kb.Default != nil {
		if err := mainLibraryWritable(); err != nil {
			return fail(err.Error())
		}
	}
	text, why := m.libraryText(rel, 0)
	if why != "" {
		return fail(why)
	}
	next, changed, why := applyEdits(text, edits)
	if why != "" {
		return fail(why)
	}
	if add != "" {
		if next != "" && !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		next += add
		if !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
	}
	if next == text {
		return &Outcome{Result: rel + " already reads that way; nothing was changed.", Summary: "no change"}, nil
	}
	if len(next) > maxEditBytes {
		return fail("The file would be over 2 MB; nothing was changed.")
	}
	abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
	if err != nil {
		return fail(err.Error())
	}
	if err := m.writeLibraryFile(abs, []byte(next)); err != nil {
		return nil, err
	}
	ix := kb.ProviderIndexer{A: m.A}
	ms, _ := kb.ReindexForFile(r.Context(), ix, rel)
	top := func(p string) string { return strings.SplitN(p, "/", 2)[0] }

	// The documents made from this file, made again; one that was changed
	// or replaced since is left as it is.
	imageDir := path.Dir(rel)
	if imageDir == "." {
		imageDir = ""
	}
	var remade, left, missing, problems []string
	links := []string{libraryLink(rel)}
	roots := map[string]bool{top(rel): true}
	for _, out := range m.made.from(rel) {
		oabs, err := kb.Resolve(out, m.A.Cfg.KBRoot)
		if err != nil {
			m.made.drop(out)
			continue
		}
		rec, _ := m.made.get(out)
		m.writeMu.Lock()
		was, err := os.ReadFile(oabs)
		m.writeMu.Unlock()
		if err != nil {
			m.made.drop(out)
			continue
		}
		if sumOf(was) != rec.Sum {
			m.made.drop(out)
			left = append(left, out)
			continue
		}
		doc, err := docwrite.Render(strings.ToLower(filepath.Ext(out)), next, libraryImages(m.A.Cfg.KBRoot, imageDir))
		if err != nil {
			left = append(left, out+" ("+err.Error()+")")
			continue
		}
		if err := m.writeLibraryFile(oabs, doc.Data); err != nil {
			return nil, err
		}
		m.made.set(out, rel, doc.Data)
		remade = append(remade, out+" ("+doc.Note+")")
		links = append(links, libraryLink(out))
		missing, problems = addNew(missing, doc.Missing), addNew(problems, doc.Problems)
		if !roots[top(out)] {
			roots[top(out)] = true
			more, _ := kb.ReindexForFile(r.Context(), ix, out)
			ms += more
		}
	}
	did := []string{}
	if len(edits) > 0 {
		did = append(did, itoaInt(changed)+" place"+pluralS(changed)+" changed")
	}
	if add != "" {
		did = append(did, "text added at the end")
	}
	res := "Edited " + rel + " (" + strings.Join(did, ", ") + "; " + itoaInt(len(next)) + " bytes) and re-indexed in " + itoaInt(int(ms)) + " ms."
	if len(remade) > 0 {
		res += " Made again from it: " + strings.Join(remade, ", ") + "."
	}
	if len(left) > 0 {
		res += " Left as they are, having been changed since they were made from this file: " + strings.Join(left, ", ") + ". To make one again, call write_kb_file for it with source=" + rel + " and overwrite=true."
	}
	if len(missing) > 0 {
		res += " These images were not embedded, and their alt text stands in their place: " + strings.Join(missing, "; ") + "."
	}
	if len(problems) > 0 {
		res += " Not applied: " + strings.Join(problems, "; ") + "."
	}
	if len(links) > 1 {
		res += " Links: " + strings.Join(links, " ")
	} else {
		res += " Link: " + links[0]
	}
	return &Outcome{Result: res, Summary: "edited " + rel}, nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
