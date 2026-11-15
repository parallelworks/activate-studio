package chat

import (
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// The libraries a chat searches: the request's library field, chosen in
// the chat's Scope menu. A request may send one id, "all", ids separated by
// commas, or a list of ids. The tools see the choice as ToolCtx.Library:
// nil when the request names none (the main library), ["all"] for every
// library, else the ids, each once, in the order sent.

// libraryValues is the request's library field as strings: a string as
// it is, a list's strings, nothing for anything else.
func libraryValues(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// chatLibraries is the set a request's library field selects for this
// viewer. Ids the viewer has no library by are dropped; when none is left,
// or every library is named, the chat searches all of them.
func (m *Module) chatLibraries(v any, viewer libraries.Viewer) []string {
	all, ids := libraries.ScopeIDs(libraryValues(v))
	if all {
		return []string{libraries.All}
	}
	if len(ids) == 0 {
		return nil
	}
	if m.Corpus == nil || m.Corpus.Reg == nil {
		return ids
	}
	libs := m.Corpus.Reg.List(m.primaryLabel(), viewer)
	known := map[string]bool{}
	for _, l := range libs {
		known[l.ID] = true
	}
	var kept []string
	for _, id := range ids {
		if known[id] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 || (len(libs) > 1 && len(kept) == len(libs)) {
		return []string{libraries.All}
	}
	return kept
}

// scopeAll is whether the chat searches every library.
func scopeAll(scope []string) bool {
	return len(scope) == 1 && strings.ToLower(scope[0]) == libraries.All
}

// scopeOne is the one library the chat searches, "" for the main one, all
// of them, or several.
func scopeOne(scope []string) string {
	if len(scope) == 1 && !scopeAll(scope) {
		return scope[0]
	}
	return ""
}

// errOneLibrary is the answer to a read or a listing that names no one
// library where the chat searches several.
const errOneLibrary = `name one library (an id from list_libraries) to read or list; "all" is for search and query`

// defaultLibrary is the library a read, a listing or a label change works
// in when the model names none: the chat's one library, else the main one
// when the chat's libraries include it (as all of them do). Among several
// without the main one, the model is asked to name one.
func defaultLibrary(ctx ToolCtx) (string, error) {
	switch {
	case len(ctx.Library) == 0, scopeAll(ctx.Library):
		return libraries.PrimaryID, nil
	case len(ctx.Library) == 1:
		return ctx.Library[0], nil
	}
	for _, id := range ctx.Library {
		if id == libraries.PrimaryID {
			return id, nil
		}
	}
	return "", &toolError{`name one library (an id from list_libraries) to read or list; this chat searches ` + strings.Join(ctx.Library, ", ") + `, and several libraries are for search and query`}
}

// libraryScopeNote tells the model which libraries the chat searches when
// the user chose some of them: nothing for the main one, one other or all.
func libraryScopeNote(scope []string) string {
	if len(scope) < 2 {
		return ""
	}
	return "\n\nThe user has scoped this conversation to these libraries: " + strings.Join(scope, ", ") + ". search_kb and query_corpus search them by default. read_kb_file, read_original and list_kb_dir work in one library at a time: pass the library a search result names."
}
