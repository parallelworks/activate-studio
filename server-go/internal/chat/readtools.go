package chat

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libapi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// read_kb_file and read_original (tools.ts readTool, indexTextForTool,
// originalTextForTool), over the index's text (search.IndexedTextState),
// the extraction cache and the original as libapi opens it.

func init() {
	// Traced as executeTool traces every tool: the index reads' GUFI
	// commands go on the outcome.
	goExecutors["read_kb_file"] = func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
		return traced(r, func(c context.Context) (*Outcome, error) {
			return m.readTool(r.WithContext(c), args, ctx, m.indexTextForTool, false)
		})
	}
	goExecutors["read_original"] = func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
		return traced(r, func(c context.Context) (*Outcome, error) {
			return m.readTool(r.WithContext(c), args, ctx, m.originalTextForTool, true)
		})
	}
}

// toolText is ToolText: a file's text with the line saying where it came
// from, or why there is none.
type toolText struct {
	text          string
	has           bool
	source, label string
	why           string
}

type readFn func(c context.Context, lib libraries.Library, rel string, ctx ToolCtx) (toolText, error)

// labelOf is the line that opens a file's text.
func labelOf(what, note string) string {
	if note != "" {
		return "[source: " + what + "; " + note + "]"
	}
	return "[source: " + what + "]"
}

// pathParts is path.posix.normalize(rel).split('/') without empty and "." parts.
func pathParts(rel string) []string {
	var out []string
	for _, p := range strings.Split(path.Clean(rel), "/") {
		if p != "" && p != "." {
			out = append(out, p)
		}
	}
	return out
}

func kindName(kind string) string {
	switch kind {
	case kb.KindImage:
		return "an image"
	case kb.KindExtracted:
		return "a document"
	case kb.KindModel:
		return "a 3D model"
	}
	return "a file"
}

// utf16Slice is String.prototype.slice(from, to) over UTF-16 units.
func utf16Slice(s string, from, to int) string {
	u := utf16.Encode([]rune(s))
	from, to = max(0, min(from, len(u))), max(0, min(to, len(u)))
	if from >= to {
		return ""
	}
	return string(utf16.Decode(u[from:to]))
}

// thousands is n.toLocaleString('en-US') for a whole number.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// fileField is a field of the file an open read, as a template literal writes it.
func fileField(f *kb.Obj, k string) string {
	v, ok := f.Get(k)
	if !ok {
		return "undefined"
	}
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	}
	return tmpl(fromGo(v))
}

// fileContent is the opened file's content, false for null.
func fileContent(f *kb.Obj) (string, bool) {
	v, _ := f.Get("content")
	s, ok := v.(string)
	return s, ok
}

func fileFlag(f *kb.Obj, k string) bool {
	v, _ := f.Get(k)
	b, _ := v.(bool)
	return b
}

func (m *Module) libService() *libapi.Service {
	m.libOnce.Do(func() { m.libs = libapi.New(m.A) })
	return m.libs
}

func (m *Module) openOriginal(c context.Context, lib libraries.Library, rel string, ctx ToolCtx) (libapi.Opened, *libapi.OriginalUnreachable, error) {
	o, err := m.libService().OpenOriginal(c, lib, rel, viewerOfCtx(ctx))
	var ou *libapi.OriginalUnreachable
	if errors.As(err, &ou) {
		return o, ou, nil
	}
	return o, nil, err
}

// indexTextForTool is a file's text for read_kb_file: the index's, then the
// extraction cache's for a library here, then the original's.
func (m *Module) indexTextForTool(c context.Context, lib libraries.Library, rel string, ctx ToolCtx) (toolText, error) {
	parts := pathParts(rel)
	if len(parts) == 0 || contains(parts, "..") {
		return toolText{}, apierr.New(400, "invalid path")
	}
	name := parts[len(parts)-1]
	kind := kb.Classify(name, false)
	remote := strings.HasPrefix(lib.IndexRoot, libraries.RemotePrefix)
	unreadable := ""
	canAsk := lib.Caps.Index && (remote || m.gufiInstalled())
	knownNone := !remote && m.Corpus.Reg.Lacks(lib, "fullText")
	if canAsk && !knownNone {
		res := m.Corpus.Search.IndexedTextState(c, lib.IndexRoot, strings.Join(parts[:len(parts)-1], "/"), name)
		if res.State == "text" {
			note := "it can lag behind the file"
			if jsval.Len(res.Text) >= search.IndexTextCap {
				note = "the index keeps at most the first " + thousands(search.IndexTextCap) + " characters of a file"
			}
			return toolText{text: res.Text, has: true, source: "index", label: labelOf("index", note)}, nil
		}
		if res.State == "unreadable" {
			unreadable = res.Error
		}
	}
	if (kind == kb.KindExtracted || kind == kb.KindImage) && lib.Site == "" {
		cache := lib.CacheRoot
		if cache == "" {
			cache = kb.RootsFrom(m.A.Cfg).ExtractCache()
		}
		if content, _, _, ok := kb.CachedText(rel, cache); ok {
			return toolText{text: content, has: true, source: "extracted", label: labelOf("extracted text kept by the library", "")}, nil
		}
	}
	noIndex := "the index holds no text for it"
	switch {
	case unreadable != "":
		noIndex = "the index could not be read"
	case !canAsk:
		noIndex = "there is no index to read"
	case knownNone:
		noIndex = "the library was indexed without text"
	}
	if kind == kb.KindModel {
		return toolText{why: rel + " is a 3D model: " + noIndex + ", and it has no text. show_in_viewer opens it."}, nil
	}
	o, ou, err := m.openOriginal(c, lib, rel, ctx)
	if ou != nil {
		detail := ""
		if unreadable != "" {
			detail = " (" + unreadable + ")"
		}
		return toolText{}, apierr.New(ou.Err.Status, "%s", upperFirst(noIndex)+detail+", and the original could not be reached. "+ou.Err.Message+" Answer from the search snippets, and say that the file itself was not read.")
	}
	if err != nil {
		return toolText{}, err
	}
	fc := o.File
	if content, ok := fileContent(fc); ok {
		extracted := fileField(fc, "source") == "extracted"
		var notes []string
		if canAsk {
			notes = append(notes, noIndex)
		}
		if fileFlag(fc, "truncated") {
			notes = append(notes, "the first 512 KB of the file")
		}
		what, source := "the original file on "+o.Where, "original"
		if extracted {
			what, source = "extracted text of the original on "+o.Where, "extracted"
		}
		return toolText{text: content, has: true, source: source, label: labelOf(what, strings.Join(notes, "; "))}, nil
	}
	if kind == kb.KindExtracted || kind == kb.KindImage {
		what := kindName(kind)
		if lib.Site != "" || o.Via == "read-through" {
			return toolText{why: "The text of " + rel + " is not available: it is " + what + " in " + lib.Label + " on " + o.Where +
				", where the Studio opens only plain text files, and the index holds no text for it. Answer from the search snippets, and say that the file itself was not read."}, nil
		}
		ocr := ""
		if kind == kb.KindImage {
			ocr = " (no OCR text or description)"
		}
		return toolText{why: "No text has been read out of " + rel + " (" + what + ", " + fileField(fc, "size") + " bytes): " + lib.Label + " has not extracted it" + ocr +
			", or could not. Answer from the search snippets, and say that the file itself was not read."}, nil
	}
	return toolText{why: "Binary file (" + fileField(fc, "kind") + ", " + fileField(fc, "size") + " bytes); no text available."}, nil
}

// originalTextForTool is a file's text for read_original: the original,
// opened where the library keeps it and then on its read-through host.
func (m *Module) originalTextForTool(c context.Context, lib libraries.Library, rel string, ctx ToolCtx) (toolText, error) {
	parts := pathParts(rel)
	if len(parts) == 0 || contains(parts, "..") {
		return toolText{}, apierr.New(400, "invalid path")
	}
	kind := kb.Classify(parts[len(parts)-1], false)
	o, ou, err := m.openOriginal(c, lib, rel, ctx)
	if ou != nil {
		return toolText{}, apierr.New(ou.Err.Status, "%s", ou.Err.Message+" Answer from the search snippets or read_kb_file, and say that the file itself was not read.")
	}
	if err != nil {
		return toolText{}, err
	}
	fc := o.File
	where := o.Where
	if o.Via == "read-through" {
		site := lib.Site
		if site == "" {
			site = "this computer"
		}
		where = o.Where + ", after " + site + " could not give it"
	}
	if content, ok := fileContent(fc); ok {
		note := ""
		if fileFlag(fc, "truncated") {
			note = "the first 512 KB of the file"
		}
		if fileField(fc, "source") == "extracted" {
			return toolText{text: content, has: true, source: "extracted", label: labelOf("extracted text of the original, found on "+where, note)}, nil
		}
		return toolText{text: content, has: true, source: "original", label: labelOf("the original file on "+where, note)}, nil
	}
	if kind == kb.KindExtracted || kind == kb.KindImage {
		what := kindName(kind)
		if o.Where == "this computer" {
			return toolText{why: rel + " is there (" + what + ", " + fileField(fc, "size") + " bytes on " + where + ") but " + lib.Label +
				" has no extracted text for it, and reading a file does not extract it. read_kb_file returns the index's text for it, when it has any."}, nil
		}
		return toolText{why: rel + " is there (" + what + ", " + fileField(fc, "size") + " bytes on " + where +
			"), but the Studio opens only plain text files over ssh and does not extract documents or images from another host. read_kb_file returns the index's text for it, when it has any."}, nil
	}
	return toolText{why: rel + " is there on " + where + " but is not text (" + fileField(fc, "kind") + ", " + fileField(fc, "size") + " bytes); no text available."}, nil
}

// noFilesHere is noFilesHere: what a library with no files here cannot do.
func noFilesHere(lib libraries.Library, needsFiles bool) error {
	if needsFiles && lib.Site == "" && lib.SourceRoot == "" && lib.ReadThrough == nil {
		return &toolError{lib.Label + " has no files on this host; it can be searched and read from its index (read_kb_file), but its originals cannot be opened. Settings, Indexing, Libraries can name another host that has them."}
	}
	if !needsFiles && lib.Site == "" && lib.SourceRoot == "" && !lib.Caps.Index {
		return &toolError{lib.Label + " has no files on this host and no index to read; it can be searched but not read"}
	}
	return nil
}

// readTool is readTool: one file with an offset, or up to 25 in one call.
func (m *Module) readTool(r *http.Request, args map[string]any, ctx ToolCtx, read readFn, needsFiles bool) (*Outcome, error) {
	if m.Corpus == nil {
		return nil, errNotHere
	}
	c := r.Context()
	if list, ok := arg(args, "paths").([]any); ok && len(list) > 0 {
		var paths []string
		for _, p := range list {
			if s := tmpl(p); s != "" && len(paths) < 25 {
				paths = append(paths, s)
			}
		}
		lib, err := m.namedLibrary(args["library"], ctx)
		if err != nil {
			return nil, err
		}
		if err := noFilesHere(lib, needsFiles); err != nil {
			return nil, err
		}
		each := max(1200, int(math.Floor(30000/float64(len(paths)))))
		blocks := make([]string, len(paths))
		read1 := make([]bool, len(paths))
		var wg sync.WaitGroup
		for i, rel := range paths {
			wg.Add(1)
			go func(i int, rel string) {
				defer wg.Done()
				t, err := read(c, lib, rel, ctx)
				if err != nil {
					blocks[i] = "### " + rel + "\nnot read: " + jsval.Slice(err.Error(), 600)
					return
				}
				read1[i] = true
				if !t.has {
					blocks[i] = "### " + rel + "\n" + t.why
					return
				}
				more := ""
				if n := jsval.Len(t.text); n > each {
					more = "\n[first " + strconv.Itoa(each) + " of " + strconv.Itoa(n) + " characters; read it alone with path and offset for the rest]"
				}
				blocks[i] = "### " + rel + "\n" + t.label + "\n" + utf16Slice(t.text, 0, each) + more
			}(i, rel)
		}
		wg.Wait()
		var files []conversations.File
		for i, p := range paths {
			if read1[i] {
				files = append(files, fileIn(p, lib.ID))
			}
		}
		return &Outcome{Result: strings.Join(blocks, "\n\n"), Summary: strconv.Itoa(len(paths)) + " files", Files: citedFiles(files)}, nil
	}
	rel := tmplOr(arg(args, "path"), "")
	if rel == "" {
		return nil, &toolError{"path or paths is required"}
	}
	offset := int(math.Min(math.Max(0, math.Trunc(numOr(arg(args, "offset"), 0))), 1<<40))
	lib, err := m.namedLibrary(args["library"], ctx)
	if err != nil {
		return nil, err
	}
	if err := noFilesHere(lib, needsFiles); err != nil {
		return nil, err
	}
	t, err := read(c, lib, rel, ctx)
	if err != nil {
		return nil, err
	}
	files := citedFiles([]conversations.File{fileIn(rel, lib.ID)})
	if !t.has {
		return &Outcome{Result: t.why, Summary: "no text", Files: files}, nil
	}
	slice := utf16Slice(t.text, offset, offset+toolOutputCap)
	more := ""
	if jsval.Len(t.text) > offset+toolOutputCap {
		more = "\n[truncated; continue with offset=" + strconv.Itoa(offset+toolOutputCap) + "]"
	}
	return &Outcome{Result: t.label + "\n" + slice + more, Summary: strconv.Itoa(jsval.Len(slice)) + " chars (" + t.source + ")", Files: files}, nil
}
