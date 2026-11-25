package chat

import (
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// Links to cite, in what a pw code session's calls return. The Studio
// assistant's reply is repaired as it streams (citations.go), from the files
// its tools returned; pw code's is written elsewhere and shown as it is, so
// its model is given each file's link to copy. The assistant's own calls,
// and other MCP clients' (which open no Studio link), return what they did.

// citeRule is how a session cites the libraries' files: the Studio
// assistant's rule (context.go), shorter.
const citeRule = "Cite every file you use, as many as are relevant, as a markdown link, never as bare text or inline code, also in lists and tables. Copy the link a tool result gives; else it is the path as the tools give it, then #open=file: and the path with each space as %20, and &lib= and the id for a library other than the main one: [specs/pump map.pdf](#open=file:specs/pump%20map.pdf&lib=archive)."

// maxCiteLinks is the most links one result carries.
const maxCiteLinks = 25

var citedTools = map[string]bool{"search_kb": true, "read_kb_file": true, "read_original": true, "list_kb_dir": true, "query_corpus": true}

// withCiteLinks is a tool's result with each of its files' links.
func withCiteLinks(name string, out *Outcome) string {
	files := out.Files
	if out.cite != nil {
		files = out.cite
	}
	if !citedTools[name] || out.Failed || len(files) == 0 {
		return out.Result
	}
	link := func(f conversations.File) string { return citeLink(f.Path, f.Path, deref(f.Library), "") }
	if name == "read_kb_file" || name == "read_original" {
		// By each file's head, which a client that cuts a long text keeps.
		res := out.Result
		if len(files) == 1 && !strings.HasPrefix(res, "### ") {
			head, text, more := strings.Cut(res, "\n")
			res = head + "\nTo cite this file, copy its link: " + link(files[0])
			if more {
				res += "\n" + text
			}
			return res
		}
		for _, f := range files {
			res = strings.Replace(res, "### "+f.Path+"\n", "### "+f.Path+"\nTo cite this file, copy its link: "+link(f)+"\n", 1)
		}
		return res
	}
	more := ""
	if len(files) > maxCiteLinks {
		files, more = files[:maxCiteLinks], "\nOther files here are cited the same way."
	}
	lines := make([]string, len(files))
	for i, f := range files {
		lines[i] = link(f)
	}
	return out.Result + "\n\nTo cite these files, copy their links:\n" + strings.Join(lines, "\n") + more
}
