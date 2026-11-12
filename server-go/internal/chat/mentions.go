package chat

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
)

// Files the user picked with @ in the composer. The picker writes each as
// @[path], or @[library:path] for a library other than the main one, which
// reads plainly in the message and which the web app draws as a link to
// the file. A pasted citation link, [name](#open=file:path&lib=id), counts
// the same. Before the first model call the server reads the index's text
// of each file, as read_kb_file would for this user, and puts it in the
// system prompt, so the model starts from the files the user named and
// does not have to find them first.

var (
	mentionAtRe   = regexp.MustCompile(`@\[([^\]\n]{1,500})\]`)
	mentionLinkRe = regexp.MustCompile(`\]\(#open=file:([^)\s&]+)(?:&q=[^)\s&]*)?(?:&lib=([a-z0-9][a-z0-9_-]{0,39}))?\)`)
	mentionLibRe  = regexp.MustCompile(`^([a-z0-9][a-z0-9_-]{0,39}):(.+)$`)
)

const (
	maxMentions     = 8
	mentionBudget   = 16000 // characters of file text across every linked file
	minMentionChars = 1500
)

type mention struct{ path, lib string }

// mentionsIn is the files text links to, in order, each once, at most
// maxMentions.
func mentionsIn(text string) []mention {
	type found struct {
		at        int
		path, lib string
	}
	var all []found
	for _, ix := range mentionAtRe.FindAllStringSubmatchIndex(text, -1) {
		body := strings.TrimSpace(text[ix[2]:ix[3]])
		lib := ""
		if m := mentionLibRe.FindStringSubmatch(body); m != nil {
			lib, body = m[1], m[2]
		}
		all = append(all, found{ix[0], body, lib})
	}
	for _, ix := range mentionLinkRe.FindAllStringSubmatchIndex(text, -1) {
		lib := ""
		if ix[4] >= 0 {
			lib = text[ix[4]:ix[5]]
		}
		all = append(all, found{ix[0], decodeURI(text[ix[2]:ix[3]]), lib})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at < all[j].at })
	var out []mention
	seen := map[string]bool{}
	for _, f := range all {
		p := strings.Trim(f.path, "/ ")
		if p == "" || strings.Contains("/"+p+"/", "/../") {
			continue
		}
		k := f.lib + "\x00" + p
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, mention{path: p, lib: f.lib})
		if len(out) == maxMentions {
			break
		}
	}
	return out
}

// mentionNote is the system prompt's section for the files the user's
// message links, or "" when it links none. Each file is read with the
// reader's own access, through the library the link names (the
// conversation's library when it names none), and a file that cannot be
// read says why in its place.
func (m *Module) mentionNote(r *http.Request, text string, ctx ToolCtx) string {
	ms := mentionsIn(text)
	if len(ms) == 0 || m.Corpus == nil {
		return ""
	}
	each := max(minMentionChars, mentionBudget/len(ms))
	blocks := make([]string, len(ms))
	var wg sync.WaitGroup
	for i, mn := range ms {
		wg.Add(1)
		go func(i int, mn mention) {
			defer wg.Done()
			blocks[i] = m.mentionBlock(r.Context(), mn, ctx, each)
		}(i, mn)
	}
	wg.Wait()
	return "\n\n--- FILES THE USER LINKED ---\nThe user's latest message names these library files with @[path] (or @[library:path]), chosen from the library. Their text as the index holds it follows, so start from them and do not search for them again. A file cut short here can be read further with read_kb_file (path, library and offset), or read_original for the file itself. Cite them as you cite any file, with #open=file: links.\n\n" + strings.Join(blocks, "\n\n")
}

func (m *Module) mentionBlock(c context.Context, mn mention, ctx ToolCtx, each int) string {
	head := "### " + mn.path
	var want any
	if mn.lib != "" {
		head += " [library " + mn.lib + "]"
		want = mn.lib
	}
	lib, err := m.namedLibrary(want, ctx)
	if err != nil && mn.lib != "" {
		// "notes:2024/plan.md" may be a path whose first folder ends in a
		// colon, not a library id; try it as one in the chat's library.
		if l2, err2 := m.namedLibrary(nil, ctx); err2 == nil {
			if t, err3 := m.indexTextForTool(c, l2, mn.lib+":"+mn.path, ctx); err3 == nil && t.has {
				lib, err, mn = l2, nil, mention{path: mn.lib + ":" + mn.path}
				head = "### " + mn.path
			}
		}
	}
	if err != nil {
		return head + "\nnot read: " + err.Error()
	}
	t, err := m.indexTextForTool(c, lib, mn.path, ctx)
	if err != nil {
		return head + "\nnot read: " + err.Error()
	}
	if !t.has {
		return head + "\n" + t.why
	}
	more := ""
	if n := jsval.Len(t.text); n > each {
		more = "\n[first " + strconv.Itoa(each) + " of " + strconv.Itoa(n) + " characters]"
	}
	return head + "\n" + t.label + "\n" + utf16Slice(t.text, 0, each) + more
}
