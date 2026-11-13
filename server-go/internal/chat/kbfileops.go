package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// Moving, copying and deleting files in the main library from the chat,
// with the Library's own operations (kb.Files): the same checks, caches
// and re-indexing as the Library's menu. A delete waits for the user's
// approval, as a platform change does; a move or a copy never replaces a
// file, so it runs at once. Moves between libraries or systems are the
// data movers' work.

func init() {
	goExecutors["move_kb_files"] = (*Module).toolMoveKBFiles
	goExecutors["rename_kb_file"] = (*Module).toolRenameKBFile
	goExecutors["copy_kb_files"] = (*Module).toolCopyKBFiles
	goExecutors["delete_kb_files"] = (*Module).toolDeleteKBFiles
}

// kbAsksFirst are the library tools whose calls wait for the user's
// approval.
var kbAsksFirst = map[string]bool{"delete_kb_files": true}

func isKBAsksFirst(name string) bool { return kbAsksFirst[name] }

// maxFileOps is how many paths one call may name.
const maxFileOps = 200

// pathArgs reads paths given as a list, or as one string of paths
// separated by new lines or commas: trimmed, without edge slashes, each
// once, in order.
func pathArgs(v any) []string {
	var raw []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				raw = append(raw, s)
			}
		}
	case []string:
		raw = x
	case string:
		sep := ","
		if strings.Contains(x, "\n") {
			sep = "\n"
		}
		raw = strings.Split(x, sep)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range raw {
		p = edgeSlashes.ReplaceAllString(jsTrim(p), "")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// mainLibraryWritable is nil when the main library can be changed, or
// what to tell the model.
func mainLibraryWritable() error {
	if kb.Default == nil {
		return errNotHere
	}
	lib, ok, err := kb.Default.Libs.Get("")
	if err != nil {
		return &toolError{err.Error()}
	}
	if !ok {
		return &toolError{"the main library is not set up here"}
	}
	if err := kb.RequireWritable(lib); err != nil {
		return &toolError{err.Error()}
	}
	return nil
}

// folderArg is a destination folder: "", "/" and "." are the library's top.
func folderArg(s string) string {
	s = edgeSlashes.ReplaceAllString(jsTrim(s), "")
	if s == "." {
		return ""
	}
	return s
}

// ensureFolder makes the destination folder when it is missing.
func (m *Module) ensureFolder(dest string) error {
	if dest == "" {
		return nil
	}
	for _, part := range strings.Split(dest, "/") {
		if kb.ExcludeDirs[part] || strings.HasPrefix(part, ".") || part == ".." {
			return &toolError{"not writable: " + part + " is excluded"}
		}
	}
	abs, err := kb.Resolve(dest, m.A.Cfg.KBRoot)
	if err != nil {
		return &toolError{err.Error()}
	}
	if st, err := os.Stat(abs); err == nil {
		if !st.IsDir() {
			return &toolError{dest + " is a file, not a folder"}
		}
		return nil
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nodeFSError(err, "mkdir", abs)
	}
	return nil
}

func movedLines(verb string, moved []kb.Moved) []string {
	out := make([]string, len(moved))
	for i, mv := range moved {
		out[i] = verb + " " + mv.From + " to " + mv.To
	}
	return out
}

func skippedLines(skipped []kb.Skipped) []string {
	out := make([]string, len(skipped))
	for i, s := range skipped {
		out[i] = "Skipped " + s.Path + ": " + s.Reason
	}
	return out
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoaInt(n) + " " + many
}

func reindexed(ctx context.Context, o *kb.Outcome) string {
	ms, err := kb.ReindexRoots(ctx, kb.Default.Files.Indexer, o.Roots, o.RootDB)
	if err != nil {
		return " The index was not updated: " + err.Error() + "."
	}
	if len(o.Roots) == 0 && !o.RootDB {
		return ""
	}
	return " Re-indexed in " + itoaInt(int(ms)) + " ms."
}

func (m *Module) toolMoveKBFiles(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	if err := mainLibraryWritable(); err != nil {
		return nil, err
	}
	paths := pathArgs(args["paths"])
	name := jsTrim(argString(args, "name"))
	rawDest, hasDest := args["dest"].(string)
	if len(paths) == 0 {
		return &Outcome{Result: "paths is required: one or more library paths to move.", Summary: "error", Failed: true}, nil
	}
	if len(paths) > maxFileOps {
		return &Outcome{Result: "At most " + itoaInt(maxFileOps) + " paths per call.", Summary: "too many", Failed: true}, nil
	}
	if name != "" && len(paths) != 1 {
		return &Outcome{Result: "name renames one item; give a single path with it.", Summary: "error", Failed: true}, nil
	}
	if (!hasDest || jsTrim(rawDest) == "") && name == "" {
		return &Outcome{Result: "dest is required: the folder to move into (\"/\" for the library's top), or give name to rename in place.", Summary: "error", Failed: true}, nil
	}
	files := kb.Default.Files
	parent := path.Dir(paths[0])
	if parent == "." {
		parent = ""
	}
	dest := parent
	if hasDest && jsTrim(rawDest) != "" {
		dest = folderArg(rawDest)
	}
	o := &kb.Outcome{}
	if name != "" && dest == parent {
		ren, err := files.Rename(paths[0], name)
		if err != nil {
			return &Outcome{Result: "Could not rename " + paths[0] + ": " + err.Error(), Summary: "error", Failed: true}, nil
		}
		o = ren
	} else {
		if err := m.ensureFolder(dest); err != nil {
			return nil, err
		}
		mv, err := files.Move(paths, dest, nil)
		if err != nil {
			return &Outcome{Result: "Could not move: " + err.Error(), Summary: "error", Failed: true}, nil
		}
		o = mv
		if name != "" && len(mv.Moved) == 1 {
			ren, err := files.Rename(mv.Moved[0].To, name)
			if err != nil {
				mv.Skipped = append(mv.Skipped, kb.Skipped{Path: mv.Moved[0].To, Reason: "moved, but not renamed: " + err.Error()})
			} else if len(ren.Moved) == 1 {
				mv.Moved[0].To = ren.Moved[0].To
				mv.Roots = append(mv.Roots, ren.Roots...)
				mv.RootDB = mv.RootDB || ren.RootDB
			}
		}
	}
	lines := append(movedLines("Moved", o.Moved), skippedLines(o.Skipped)...)
	summary := "moved " + countOf(len(o.Moved), "item", "items")
	if name != "" && dest == parent {
		lines = movedLines("Renamed", o.Moved)
		summary = "renamed " + paths[0]
	}
	if len(o.Skipped) > 0 {
		summary += ", skipped " + itoaInt(len(o.Skipped))
	}
	result := strings.Join(lines, "\n")
	if len(o.Moved) == 0 {
		result += "\nNothing was moved."
	} else {
		result += "\n" + strings.TrimSpace(reindexed(r.Context(), o))
	}
	return &Outcome{Result: strings.TrimSpace(result), Summary: summary, Failed: len(o.Moved) == 0}, nil
}

// toolRenameKBFile renames one file or folder where it is: the rename that
// move_kb_files does with a name and no dest, under a name the model finds.
func (m *Module) toolRenameKBFile(r *http.Request, args map[string]any, tc ToolCtx) (*Outcome, error) {
	p := pathArgs(args["path"])
	if len(p) != 1 {
		return &Outcome{Result: "path is required: the one library file or folder to rename.", Summary: "error", Failed: true}, nil
	}
	if jsTrim(argString(args, "name")) == "" {
		return &Outcome{Result: "name is required: the new name, without a folder.", Summary: "error", Failed: true}, nil
	}
	return m.toolMoveKBFiles(r, map[string]any{"paths": []any{p[0]}, "name": args["name"]}, tc)
}

func (m *Module) toolCopyKBFiles(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	if err := mainLibraryWritable(); err != nil {
		return nil, err
	}
	paths := pathArgs(args["paths"])
	rawDest, _ := args["dest"].(string)
	if len(paths) == 0 {
		return &Outcome{Result: "paths is required: one or more library paths to copy.", Summary: "error", Failed: true}, nil
	}
	if len(paths) > maxFileOps {
		return &Outcome{Result: "At most " + itoaInt(maxFileOps) + " paths per call.", Summary: "too many", Failed: true}, nil
	}
	if jsTrim(rawDest) == "" {
		return &Outcome{Result: "dest is required: the folder to copy into (\"/\" for the library's top). A copy into the same folder is named \"<name> copy\".", Summary: "error", Failed: true}, nil
	}
	dest := folderArg(rawDest)
	if err := m.ensureFolder(dest); err != nil {
		return nil, err
	}
	o, err := kb.Default.Files.Copy(paths, dest, nil)
	if err != nil {
		return &Outcome{Result: "Could not copy: " + err.Error(), Summary: "error", Failed: true}, nil
	}
	lines := append(movedLines("Copied", o.Moved), skippedLines(o.Skipped)...)
	result := strings.Join(lines, "\n")
	if len(o.Moved) == 0 {
		result += "\nNothing was copied."
	} else {
		result += "\n" + strings.TrimSpace(reindexed(r.Context(), o))
	}
	summary := "copied " + countOf(len(o.Moved), "item", "items")
	if len(o.Skipped) > 0 {
		summary += ", skipped " + itoaInt(len(o.Skipped))
	}
	return &Outcome{Result: strings.TrimSpace(result), Summary: summary, Failed: len(o.Moved) == 0}, nil
}

// toolDeleteKBFiles runs only once the user has approved the call.
func (m *Module) toolDeleteKBFiles(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	if err := mainLibraryWritable(); err != nil {
		return nil, err
	}
	paths := pathArgs(args["paths"])
	if len(paths) == 0 {
		return &Outcome{Result: "paths is required.", Summary: "error", Failed: true}, nil
	}
	d := kb.Default.Files.DeletePaths(paths, nil)
	o := &kb.Outcome{Roots: d.Roots, RootDB: d.RootDB}
	lines := make([]string, 0, len(d.Deleted)+len(d.Skipped))
	for _, p := range d.Deleted {
		lines = append(lines, "Deleted "+p)
	}
	lines = append(lines, skippedLines(d.Skipped)...)
	result := strings.Join(lines, "\n")
	if len(d.Deleted) > 0 {
		result += "\n" + strings.TrimSpace(reindexed(r.Context(), o))
	}
	summary := "deleted " + countOf(len(d.Deleted), "file", "files")
	if len(d.Skipped) > 0 {
		summary += ", skipped " + itoaInt(len(d.Skipped))
	}
	return &Outcome{Result: strings.TrimSpace(result), Summary: summary, Failed: len(d.Deleted) == 0}, nil
}

/* ---- the approval of a library change ---- */

// prepareKBArgs is a held library call's arguments in canonical form, or
// what is wrong with them: every path an existing file in the main
// library, so the card lists what will go.
func prepareKBArgs(name string, raw any) (*jsonobj.Object, string, string) {
	var v any
	switch x := raw.(type) {
	case string:
		if jsTrim(x) == "" {
			x = "{}"
		}
		if json.Unmarshal([]byte(x), &v) != nil {
			return nil, "", "The arguments were not valid JSON."
		}
	case json.RawMessage:
		if json.Unmarshal(x, &v) != nil {
			return nil, "", "The arguments were not valid JSON."
		}
	default:
		b, err := json.Marshal(x)
		if err != nil || json.Unmarshal(b, &v) != nil {
			return nil, "", "The arguments were not valid JSON."
		}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, "", "The arguments were not valid JSON."
	}
	paths := pathArgs(obj["paths"])
	if len(paths) == 0 {
		return nil, "", "paths is required: the library paths of the files to delete."
	}
	if len(paths) > maxFileOps {
		return nil, "", "At most " + itoaInt(maxFileOps) + " files per call."
	}
	if err := mainLibraryWritable(); err != nil {
		return nil, "", err.Error()
	}
	var missing, folders []string
	for _, p := range paths {
		for _, part := range strings.Split(p, "/") {
			if kb.ExcludeDirs[part] || strings.HasPrefix(part, ".") {
				return nil, "", p + " is in a protected folder and cannot be deleted."
			}
		}
		abs, err := kb.Resolve(p, kb.Default.Roots.KBRoot)
		if err != nil {
			return nil, "", p + ": " + err.Error()
		}
		st, err := os.Stat(abs)
		switch {
		case err != nil:
			missing = append(missing, p)
		case !st.Mode().IsRegular():
			folders = append(folders, p)
		}
	}
	if len(missing) > 0 {
		return nil, "", "Not found in the library: " + strings.Join(missing, ", ") + ". Check the paths with list_kb_dir; nothing was asked."
	}
	if len(folders) > 0 {
		return nil, "", "Only files can be deleted, and these are folders: " + strings.Join(folders, ", ") + ". Name the files in them instead."
	}
	list := make([]any, len(paths))
	for i, p := range paths {
		list[i] = p
	}
	args := jsonobj.New()
	args.SetValue("paths", list)
	b, _ := jsonobj.Marshal(args)
	return args, string(b), ""
}

// kbToolHash identifies what a held library call does: its spec.
func kbToolHash(name string) string {
	for _, s := range builtinSpecs {
		if s.Name == name {
			h := sha256.Sum256([]byte(s.Name + "\x00" + s.Description + "\x00" + string(s.Parameters)))
			return hex.EncodeToString(h[:])
		}
	}
	return ""
}

// kbPaths is a canonical call's paths.
func kbPaths(args *jsonobj.Object) []string {
	var v any
	if args != nil && args.Has("paths") {
		_ = json.Unmarshal(args.Get("paths"), &v)
	}
	return pathArgs(v)
}

// kbChangesAvailable is whether a library change can be asked for in this
// chat: someone is here to approve it, and the conversation is theirs.
func (m *Module) kbChangesAvailable(viewer *Viewer, owner, username *string) Changes {
	if viewer == nil {
		return Changes{Why: "nobody in this chat can approve a change"}
	}
	if m.A.Cfg.AuthEnabled() && owner != nil && *owner != "" && deref(owner) != deref(username) {
		return Changes{Why: "this conversation belongs to someone else"}
	}
	return Changes{OK: true}
}

// runKBTool runs an approved library call.
func (m *Module) runKBTool(ctx context.Context, name string, args *jsonobj.Object) (string, bool) {
	exec, ok := goExecutors[name]
	if !ok {
		return "The " + name + " tool is not available here.", true
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	in := map[string]any{"paths": func() []any {
		out := []any{}
		for _, p := range kbPaths(args) {
			out = append(out, p)
		}
		return out
	}()}
	o, err := exec(m, req, in, ToolCtx{})
	if err != nil {
		return "Tool error: " + err.Error(), true
	}
	if o == nil {
		return "The tool returned nothing.", true
	}
	return o.Result, o.Failed
}
