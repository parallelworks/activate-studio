package chat

import (
	"path"
	"regexp"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The files a turn wrote are offered as chips that open them in the
// Library viewer. A click opens the file; it sends no message. A document
// written in more than one format (a report's .md beside its .pdf) is one
// chip, which opens the rendered format and names the others.

// maxOpenChips is how many such chips a turn gets; past it the last one
// opens the folder the files were written to.
const maxOpenChips = 2

// openLinkRe reads the links a write's result gives, [shown](#open=file:
// rel&lib=id), which name the file's library as the write placed it.
var openLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(#open=file:([^)&]+)(?:&lib=([a-z0-9_-]+))?\)`)

// openIn is what a link to a file says: "Open a.pdf in the Library".
func openIn(name, library string) string { return "Open " + name + " in " + library }

type writtenFile struct{ rel, lib, shown string }

// writtenBy is the files one write or edit call left: those its result
// links to, or else the path it was given and the formats made beside it.
func writtenBy(args map[string]any, result string) []writtenFile {
	var out []writtenFile
	seen := map[string]bool{}
	add := func(f writtenFile) {
		if f.rel == "" || seen[f.lib+"\x00"+f.rel] {
			return
		}
		seen[f.lib+"\x00"+f.rel] = true
		out = append(out, f)
	}
	for _, m := range openLinkRe.FindAllStringSubmatch(result, -1) {
		add(writtenFile{rel: strings.ReplaceAll(m[2], "%20", " "), lib: m[3], shown: m[1]})
	}
	if len(out) > 0 {
		return out
	}
	rel := strings.Trim(jsTrim(jsString(args["path"])), "/")
	add(writtenFile{rel: rel, shown: rel})
	if also, bad := alsoFormats(args["also"]); bad == "" && rel != "" {
		stem := strings.TrimSuffix(rel, path.Ext(rel))
		for _, f := range also {
			add(writtenFile{rel: stem + "." + f, shown: stem + "." + f})
		}
	}
	return out
}

// renderedRank orders a document's formats for opening: the rendered
// ones first, then whichever was written first.
func renderedRank(rel string) int {
	switch strings.ToLower(path.Ext(rel)) {
	case ".pdf":
		return 0
	case ".docx":
		return 1
	}
	return 2
}

// libraryOf names f's library as its write did ("My library"), or "the
// Library" where there is one library.
func libraryOf(f writtenFile) string {
	if f.shown != f.rel && strings.HasSuffix(f.shown, "/"+f.rel) {
		return strings.TrimSuffix(f.shown, "/"+f.rel)
	}
	return "the Library"
}

// openChips is the chips for the files the turn's calls wrote: one per
// document (a folder and a stem in one library), at most maxOpenChips.
func openChips(calls []conversations.Part, argsOf func(conversations.Part) map[string]any, res func(conversations.Part) string) []*jsonobj.Object {
	type doc struct {
		files []writtenFile
	}
	var docs []*doc
	byKey := map[string]*doc{}
	for _, p := range calls {
		if (p.Name != "write_kb_file" && p.Name != "edit_kb_file") || p.Status == "error" || p.Status == "running" {
			continue
		}
		for _, f := range writtenBy(argsOf(p), res(p)) {
			key := f.lib + "\x00" + strings.TrimSuffix(f.rel, path.Ext(f.rel))
			d := byKey[key]
			if d == nil {
				d = &doc{}
				byKey[key] = d
				docs = append(docs, d)
			}
			had := false
			for _, g := range d.files {
				had = had || g.rel == f.rel
			}
			if !had {
				d.files = append(d.files, f)
			}
		}
	}
	chip := func(d *doc) *jsonobj.Object {
		open := d.files[0]
		for _, f := range d.files[1:] {
			if renderedRank(f.rel) < renderedRank(open.rel) {
				open = f
			}
		}
		title := openIn(open.rel, libraryOf(open))
		var others []any
		var names []string
		for _, f := range d.files {
			if f.rel != open.rel {
				others = append(others, f.rel)
				names = append(names, path.Base(f.rel))
			}
		}
		if len(names) > 0 {
			title += ". Also written as " + strings.Join(names, ", ")
		}
		kv := []any{"text", path.Base(open.rel), "why", "a file was written", "kind", "open", "path", open.rel, "title", title}
		if open.lib != "" {
			kv = append(kv, "lib", open.lib)
		}
		if len(others) > 0 {
			kv = append(kv, "also", others)
		}
		return reqbody.O(kv...)
	}
	var out []*jsonobj.Object
	for i, d := range docs {
		if i == maxOpenChips-1 && len(docs) > maxOpenChips {
			var all []writtenFile
			for _, d := range docs {
				all = append(all, d.files...)
			}
			return append(out, folderChip(all, len(docs)))
		}
		out = append(out, chip(d))
	}
	return out
}

// folderChip opens the folder that holds the files written, in the first
// one's library: "Open the 5 files written".
func folderChip(files []writtenFile, n int) *jsonobj.Object {
	first := files[0]
	dir := path.Dir(first.rel)
	for _, f := range files[1:] {
		if f.lib != first.lib {
			continue
		}
		for dir != "." && path.Dir(f.rel) != dir && !strings.HasPrefix(f.rel, dir+"/") {
			dir = path.Dir(dir)
		}
	}
	title := "Open " + libraryOf(first)
	if dir == "." {
		dir = ""
	} else {
		title = openIn(dir, libraryOf(first))
	}
	kv := []any{"text", "Open the " + itoaInt(n) + " files written", "why", "files were written", "kind", "open", "path", dir, "folder", true, "title", title}
	if first.lib != "" {
		kv = append(kv, "lib", first.lib)
	}
	return reqbody.O(kv...)
}
