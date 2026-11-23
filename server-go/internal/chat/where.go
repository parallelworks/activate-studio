package chat

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// Which library a chat's write goes to, on a Studio where people have
// spaces of their own:
//
//  1. A library the person names (the tool's library, or a path that
//     starts with a library's name) always wins.
//  2. Else the place the person chose earlier in this chat.
//  3. Else the chat's own context: the files it has read, opened, listed,
//     edited, moved, copied or written, and the library it is scoped to.
//     Mostly in My library, it writes there; mostly in the main library,
//     it writes there when the person may change it.
//  4. Else (mixed, no context yet, or context the person may not write
//     in) the person is asked, with the places to choose from, and the
//     answer is kept for the chat's later writes.
//
// All of it is decided here from the chat's recorded calls, never left to
// the model.

// mostly is the share of a chat's files one library must hold to be the
// chat's place.
const mostly = 0.75

// ownSpaceLib is the person's own space, when they have one.
func ownSpaceLib(r *http.Request) (kb.Library, bool) {
	if r == nil || kb.LibraryResolver == nil || kb.SpaceFiles == nil || user(r) == nil {
		return kb.Library{}, false
	}
	libs, err := kb.LibraryResolver.All(r)
	if err != nil {
		return kb.Library{}, false
	}
	for _, l := range libs {
		if l.Space && l.SharedFrom == "" {
			return l, true
		}
	}
	return kb.Library{}, false
}

// spacesFor says whether the person in r has a space of their own, so
// that paths name their library.
func (m *Module) spacesFor(r *http.Request) bool {
	_, ok := ownSpaceLib(r)
	return ok
}

// placeNamed is the library a name or id stands for, for the person in r.
func (m *Module) placeNamed(r *http.Request, name string) (place, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return place{}, false
	}
	main := m.mainPlace(r)
	switch n {
	case kb.PrimaryID, "main", "main library", "the main library", "shared", "shared library", "the shared library", strings.ToLower(main.label()):
		return main, true
	}
	own, hasOwn := ownSpaceLib(r)
	if hasOwn {
		switch n {
		case "my library", "my", "mine", "my space", strings.ToLower(own.ID), strings.ToLower(own.Label):
			return place{m: m, r: r, lib: own, space: true}, true
		}
	}
	if kb.LibraryResolver == nil || r == nil {
		return place{}, false
	}
	libs, _ := kb.LibraryResolver.All(r)
	for _, l := range libs {
		if strings.EqualFold(l.ID, n) || strings.EqualFold(l.Label, n) {
			if l.Primary {
				return main, true
			}
			return place{m: m, r: r, lib: l, space: l.AsViewer()}, true
		}
	}
	return place{}, false
}

// splitNamed takes a library's name off the front of rel ("My library/a.md")
// on a Studio with spaces: the library and the path in it.
func (m *Module) splitNamed(r *http.Request, rel string) (place, string, bool) {
	if !m.spacesFor(r) {
		return place{}, rel, false
	}
	first, rest, ok := strings.Cut(rel, "/")
	if !ok || rest == "" {
		return place{}, rel, false
	}
	p, found := m.placeNamed(r, first)
	if !found {
		return place{}, rel, false
	}
	return p, rest, true
}

// namedPlace is the library a call names, by its library argument or the
// front of its path, and the path in it. named is false when it names
// none.
func (m *Module) namedPlace(r *http.Request, libArg, rel string) (p place, rest string, named bool, err error) {
	if libArg = strings.TrimSpace(libArg); libArg != "" {
		p, ok := m.placeNamed(r, libArg)
		if !ok {
			return place{}, rel, false, &toolError{"There is no library named " + libArg + " here. Name My library, or the main library as " + m.primaryLabel() + "; list_libraries lists the others."}
		}
		if q, rest, ok := m.splitNamed(r, rel); ok && q.id() == p.id() {
			rel = rest
		}
		return p, rel, true, nil
	}
	if q, rest, ok := m.splitNamed(r, rel); ok {
		return q, rest, true, nil
	}
	return place{}, rel, false, nil
}

/* ---- the chat's context ---- */

// touch is one file or folder a chat worked with.
type touch struct{ lib, rel string }

// chatTouches is what each chat worked with in this process, seeded from
// its stored calls when first asked, and the place each chat's person
// chose (kept in INDEX_BASE/chat-write-places.json).
type chatTouches struct {
	mu     sync.Mutex
	files  map[string][]touch
	chosen map[string]string
	read   bool
	file   string
}

// countedTools are the calls whose files make a chat's context; a search
// lists files it did not open, so it is not one.
var countedTools = map[string]bool{
	"read_kb_file": true, "read_original": true, "show_in_viewer": true, "list_kb_dir": true,
	"write_kb_file": true, "edit_kb_file": true, "move_kb_files": true, "copy_kb_files": true, "rename_kb_file": true,
}

func (m *Module) touches() *chatTouches {
	m.whereOnce.Do(func() {
		m.where = &chatTouches{files: map[string][]touch{}, chosen: map[string]string{}, file: filepath.Join(m.A.Cfg.IndexBase, "chat-write-places.json")}
	})
	return m.where
}

func (t *chatTouches) loadChosen() {
	if t.read {
		return
	}
	t.read = true
	if b, err := os.ReadFile(t.file); err == nil {
		_ = json.Unmarshal(b, &t.chosen)
	}
	if t.chosen == nil {
		t.chosen = map[string]string{}
	}
}

// choose keeps the library a chat's person chose for its writes.
func (m *Module) choose(conv, lib string) {
	if conv == "" {
		return
	}
	t := m.touches()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadChosen()
	t.chosen[conv] = lib
	if b, err := json.Marshal(t.chosen); err == nil {
		_ = os.MkdirAll(filepath.Dir(t.file), 0o755)
		_ = os.WriteFile(t.file, b, 0o600)
	}
}

func (m *Module) chosen(conv string) string {
	t := m.touches()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loadChosen()
	return t.chosen[conv]
}

// note records that a chat worked with rel in a library.
func (m *Module) note(ctx ToolCtx, lib, rel string) {
	conv := deref(ctx.ConversationID)
	if conv == "" || lib == "" {
		return
	}
	t := m.touches()
	t.mu.Lock()
	defer t.mu.Unlock()
	m.seedLocked(t, conv)
	t.files[conv] = append(t.files[conv], touch{lib, rel})
}

// seedLocked fills a chat's files from its stored calls, once: those of
// earlier turns, which this process may not have seen run.
func (m *Module) seedLocked(t *chatTouches, conv string) {
	if _, ok := t.files[conv]; ok {
		return
	}
	t.files[conv] = []touch{}
	if m.Conv == nil {
		return
	}
	c := m.Conv.Get(conv)
	if c == nil {
		return
	}
	for _, msg := range c.Messages {
		for _, p := range msg.Parts {
			if p.Kind != "tool_call" || !countedTools[p.Name] || p.Status == "error" {
				continue
			}
			t.files[conv] = append(t.files[conv], storedTouches(p)...)
		}
	}
}

// storedTouches is the files a stored call worked with: those its result
// listed, else the paths its arguments name in the library they name (the
// main library when none).
func storedTouches(p conversations.Part) []touch {
	var out []touch
	for _, f := range p.Files {
		lib := kb.PrimaryID
		if f.Library != nil && *f.Library != "" {
			lib = *f.Library
		}
		out = append(out, touch{lib, f.Path})
	}
	if len(out) > 0 {
		return out
	}
	var a map[string]any
	if json.Unmarshal([]byte(p.Args), &a) != nil {
		return nil
	}
	lib, _ := a["library"].(string)
	if lib == "" {
		lib = kb.PrimaryID
	}
	paths := pathArgs(a["paths"])
	if s, ok := a["path"].(string); ok && s != "" {
		paths = append(paths, s)
	}
	for _, rel := range paths {
		out = append(out, touch{lib, rel})
	}
	return out
}

// chatFiles is what a chat worked with: its files, and its scope.
func (m *Module) chatFiles(ctx ToolCtx) []touch {
	conv := deref(ctx.ConversationID)
	var out []touch
	if conv != "" {
		t := m.touches()
		t.mu.Lock()
		m.seedLocked(t, conv)
		out = append(out, t.files[conv]...)
		t.mu.Unlock()
	}
	// A chat scoped to one library is that library's, as two files are.
	if len(ctx.Library) == 1 && !scopeAll(ctx.Library) {
		out = append(out, touch{ctx.Library[0], ""}, touch{ctx.Library[0], ""})
	}
	return out
}

/* ---- the decision ---- */

// whereOption is a place the person may choose for a write.
type whereOption struct {
	Label   string `json:"label"`
	Library string `json:"library"`
	Path    string `json:"path"`
	// Shown is the library and path, as the card names it.
	Shown string `json:"shown"`
	// Args is the call with this place, as JSON text: the answer that
	// chooses it.
	Args string `json:"args"`
}

// whereDecision is where a write with no library named goes, or that
// the person is asked.
type whereDecision struct {
	place   place
	why     string
	ask     bool
	options []whereOption
}

// libOf is a touch's library as one of the person's places: "own", "main",
// or another library's id.
func libOf(lib string, own kb.Library) string {
	switch {
	case lib == "" || lib == kb.PrimaryID:
		return "main"
	case lib == own.ID:
		return "own"
	}
	return lib
}

// decideWhere is where a write of rel goes when the call names no library.
func (m *Module) decideWhere(r *http.Request, ctx ToolCtx, rel string, args map[string]any) whereDecision {
	main := m.mainPlace(r)
	own, hasOwn := ownSpaceLib(r)
	if !hasOwn {
		return whereDecision{place: main}
	}
	mine := place{m: m, r: r, lib: own, space: true}
	mainOK := mainLibraryWritable(r) == nil
	conv := deref(ctx.ConversationID)
	if c := m.chosen(conv); c != "" {
		if c == "own" {
			return whereDecision{place: mine, why: "the person chose " + mine.label() + " for this chat's files"}
		}
		if c == "main" && mainOK {
			return whereDecision{place: main, why: "the person chose " + main.label() + " for this chat's files"}
		}
	}
	counts := map[string]int{}
	folders := map[string]map[string]int{}
	total := 0
	for _, t := range m.chatFiles(ctx) {
		k := libOf(t.lib, own)
		counts[k]++
		total++
		if d := parentOf(t.rel); d != "" {
			if folders[k] == nil {
				folders[k] = map[string]int{}
			}
			folders[k][d]++
		}
	}
	share := func(k string) float64 {
		if total == 0 {
			return 0
		}
		return float64(counts[k]) / float64(total)
	}
	switch {
	case total > 0 && share("own") >= mostly:
		return whereDecision{place: mine, why: "most of the files this chat has worked with are in " + mine.label()}
	case total > 0 && share("main") >= mostly && mainOK:
		return whereDecision{place: main, why: "most of the files this chat has worked with are in " + main.label()}
	}
	why := "this chat has not worked with any files yet"
	switch {
	case total > 0 && share("main") >= mostly:
		why = "this chat works with files in " + main.label() + ", which only its administrators change"
	case total > 0:
		why = "this chat works with files in more than one library"
	}
	// The folder the context points to, in the library it is mostly in.
	lead := "own"
	if counts["main"] > counts["own"] {
		lead = "main"
	}
	folder := topKey(folders[lead])
	d := whereDecision{ask: true, why: why}
	add := func(label string, p place, path string) {
		for _, o := range d.options {
			if o.Library == p.id() && o.Path == path {
				return
			}
		}
		a := map[string]any{}
		for k, v := range args {
			a[k] = v
		}
		a["library"], a["path"] = p.id(), path
		b, _ := json.Marshal(a)
		d.options = append(d.options, whereOption{Label: label, Library: p.id(), Path: path, Shown: p.label() + "/" + path, Args: string(b)})
	}
	add(mine.label(), mine, rel)
	if mainOK {
		add(main.label(), main, rel)
	}
	if folder != "" && !strings.Contains(rel, "/") {
		into := path2(folder, rel)
		if lead == "main" && mainOK {
			add("Beside this chat's files in "+main.label(), main, into)
		} else {
			add("In "+folder+" in "+mine.label(), mine, into)
		}
	}
	return d
}

func path2(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// topKey is the key with the highest count, the first by name on a tie.
func topKey(c map[string]int) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, n := "", 0
	for _, k := range keys {
		if c[k] > n {
			best, n = k, c[k]
		}
	}
	return best
}

// askText is the tool result when a write cannot be asked about in the
// chat (a pw code session's Studio tools): the model asks in its reply.
func (d whereDecision) askText() string {
	var opts []string
	for _, o := range d.options {
		opts = append(opts, o.Shown)
	}
	return "Not written: the library to write in is not clear, because " + d.why + ". Ask the user where to save it, offering " + strings.Join(opts, ", or ") + ", and call again with library set to their answer. Do not choose for them."
}

/* ---- reads name their library too ---- */

func init() {
	for _, name := range []string{"read_kb_file", "read_original", "list_kb_dir", "show_in_viewer"} {
		inner := goExecutors[name]
		if inner == nil {
			continue
		}
		list := name == "list_kb_dir"
		goExecutors[name] = func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
			args = m.libraryFromPath(r, args)
			o, err := inner(m, r, args, ctx)
			if err != nil || o == nil || o.Failed {
				return o, err
			}
			lib, _ := args["library"].(string)
			if lib == "" {
				lib, _ = defaultLibrary(ctx)
			}
			if p, ok := m.placeNamed(r, lib); ok {
				lib = p.id()
			}
			if list {
				// A folder looked at counts once, not each file in it.
				m.note(ctx, lib, path2(edgeSlashes.ReplaceAllString(jsString(args["path"]), ""), "."))
				return o, nil
			}
			for _, f := range o.Files {
				l := lib
				if f.Library != nil && *f.Library != "" {
					l = *f.Library
				}
				m.note(ctx, l, f.Path)
			}
			return o, nil
		}
	}
}

// libraryFromPath reads a library named at the front of a path ("My
// library/notes/a.md") as the call's library, on a Studio with spaces.
func (m *Module) libraryFromPath(r *http.Request, args map[string]any) map[string]any {
	if lib, _ := args["library"].(string); strings.TrimSpace(lib) != "" || !m.spacesFor(r) {
		return args
	}
	out := map[string]any{}
	for k, v := range args {
		out[k] = v
	}
	var p place
	found := false
	if s, ok := args["path"].(string); ok {
		if q, rest, ok := m.splitNamed(r, s); ok {
			p, found, out["path"] = q, true, rest
		}
	}
	if list, ok := args["paths"].([]any); ok {
		var next []any
		for _, v := range list {
			s, _ := v.(string)
			if q, rest, ok := m.splitNamed(r, s); ok && (!found || q.id() == p.id()) {
				p, found = q, true
				next = append(next, rest)
				continue
			}
			next = append(next, v)
		}
		out["paths"] = next
	}
	if found {
		out["library"] = p.id()
	}
	return out
}

/* ---- asking where ---- */

// writeAsks is the choice to put to the person before a write_kb_file
// call runs, or nil when its place is clear (named, chosen, or the chat's).
func (m *Module) writeAsks(r *http.Request, ctx ToolCtx, raw string) *whereDecision {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil || args == nil {
		return nil
	}
	rel := edgeSlashes.ReplaceAllString(argString(args, "path"), "")
	if rel == "" || argTrue(args, "append") || argTrue(args, "overwrite") {
		return nil
	}
	_, _, _, ask, err := m.toolPlace(r, ctx, args, rel, true)
	if err != nil || ask == nil || len(ask.options) == 0 {
		return nil
	}
	return ask
}

// wherePart is the card that asks where a file goes: one button for each
// place, and one to not save it.
func wherePart(toolCallID string, options []whereOption, why, expiresAt string) *jsonobj.Object {
	o := reqbody.O("kind", "approval", "id", toolCallID, "approvalKind", "confirm", "toolName", "write_kb_file", "subject", "call",
		"header", "Where should this be saved?", "command", options[0].Path,
		"reason", "The Studio asks because "+why+". Your answer is used for this chat's later files too.")
	o.SetValue("canAllowDirectory", false)
	o.SetValue("expiresAt", expiresAt)
	o.SetValue("where", options)
	return o
}

// pickedWhere is the offered place whose call args is.
func pickedWhere(options []whereOption, args string) *whereOption {
	for i := range options {
		if options[i].Args == args {
			return &options[i]
		}
	}
	return nil
}

// answerWhere runs a held write in the place the person chose, as them,
// and keeps the choice for the chat's later writes.
func (m *Module) answerWhere(r *http.Request, rec *HeldCall, args string) (string, string, bool) {
	pick := pickedWhere(rec.Where, args)
	if pick == nil {
		return "Choose one of the places offered; nothing was written.", "not run", true
	}
	var a map[string]any
	if json.Unmarshal([]byte(pick.Args), &a) != nil {
		return "The call could not be read; nothing was written.", "not run", true
	}
	own, _ := ownSpaceLib(r)
	m.choose(rec.ConversationID, libOf(pick.Library, own))
	ctx := ToolCtx{ConversationID: &rec.ConversationID}
	if id := userID(r); id != "" {
		ctx.UserID = &id
	}
	o, err := m.toolWriteKBFile(r, a, ctx)
	if err != nil {
		return "Tool error: " + err.Error(), "failed", true
	}
	return "The user chose " + pick.Shown + ". " + o.Result, o.Summary, o.Failed
}

// userNamed is the library the person's latest message names in plain
// words, when it names exactly one: "my library" (or their space's name)
// or the main library ("the shared library", "the main library", or its
// name).
func (m *Module) userNamed(r *http.Request, ctx ToolCtx) (place, bool) {
	conv := deref(ctx.ConversationID)
	if conv == "" || m.Conv == nil {
		return place{}, false
	}
	c := m.Conv.Get(conv)
	if c == nil {
		return place{}, false
	}
	said := ""
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "user" && c.Messages[i].Content != nil {
			said = strings.ToLower(*c.Messages[i].Content)
			break
		}
	}
	if said == "" {
		return place{}, false
	}
	own, hasOwn := ownSpaceLib(r)
	main := m.mainPlace(r)
	mineWords := []string{"my library", "my own library", "my space", "my personal library"}
	if hasOwn && own.Label != "" {
		mineWords = append(mineWords, strings.ToLower(own.Label))
	}
	mainWords := []string{"shared library", "main library", "team library", strings.ToLower(main.label())}
	has := func(words []string) bool {
		for _, w := range words {
			if w != "" && strings.Contains(said, w) {
				return true
			}
		}
		return false
	}
	mine, shared := hasOwn && has(mineWords), has(mainWords)
	switch {
	case mine && !shared:
		return place{m: m, r: r, lib: own, space: true}, true
	case shared && !mine && mainLibraryWritable(r) == nil:
		// For a person who may not change it, naming the shared library
		// is more likely where the material is than where to save: they
		// are asked.
		return main, true
	}
	return place{}, false
}
