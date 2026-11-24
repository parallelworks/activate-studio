package chat

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// Moving, renaming and copying in a person's space from the chat: the
// space's own operations (kb.SpaceFiles), as the person, as the Library's
// menu does them there.

// opPlace is the library a move or copy works in, from its first path, and
// its paths in that library. Every path must be in the same one.
func (m *Module) opPlace(r *http.Request, tctx ToolCtx, args map[string]any, paths []string) (place, []string, error) {
	if len(paths) == 0 {
		return m.mainPlace(r), paths, nil
	}
	p, first, _, _, err := m.toolPlace(r, tctx, args, paths[0], false)
	if err != nil {
		return place{}, nil, err
	}
	out := []string{first}
	for _, rel := range paths[1:] {
		if q, rest, ok := m.splitNamed(r, rel); ok {
			if q.id() != p.id() {
				return place{}, nil, &toolError{"One call works in one library: " + paths[0] + " and " + rel + " are in different ones. To move or copy between libraries, use transfer_files."}
			}
			rel = rest
		}
		out = append(out, rel)
	}
	return p, out, nil
}

// destIn is a destination folder in p, its library's name taken off; a
// folder in another library is refused.
func (m *Module) destIn(r *http.Request, p place, raw string) (string, error) {
	t := edgeSlashes.ReplaceAllString(jsTrim(raw), "")
	if t == "" {
		return raw, nil
	}
	if q, ok := m.libraryNameAt(r, t); ok && !q.folderAt(t) {
		if q.id() != p.id() {
			return "", &toolError{"To move or copy into " + q.label() + " from " + p.label() + ", use transfer_files: this tool works inside one library."}
		}
		return "/", nil
	}
	if q, rest, ok := m.splitNamed(r, t); ok {
		if q.id() != p.id() {
			return "", &toolError{"To move or copy into " + q.label() + " from " + p.label() + ", use transfer_files: this tool works inside one library."}
		}
		return rest, nil
	}
	return raw, nil
}

func (m *Module) moveInSpace(r *http.Request, tctx ToolCtx, p place, paths []string, name, rawDest string, hasDest bool) (*Outcome, error) {
	fs := kb.SpaceFiles
	for _, rel := range paths {
		if err := p.check(rel); err != nil {
			return nil, err
		}
	}
	parent := parentOf(paths[0])
	dest := parent
	if hasDest && jsTrim(rawDest) != "" {
		dest = folderArg(rawDest)
		if dest != "" {
			if err := p.check(dest); err != nil {
				return nil, err
			}
		}
	}
	if name != "" && (strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." || strings.HasPrefix(name, ".")) {
		return nil, &toolError{"name is a new name only, with no folder: " + name + " is not one."}
	}
	var moved []kb.Moved
	var skipped []kb.Skipped
	renaming := name != "" && dest == parent
	if renaming {
		to, err := fs.Rename(r, p.lib, paths[0], name)
		if err != nil {
			return &Outcome{Result: "Could not rename " + p.shown(paths[0]) + ": " + err.Error(), Summary: "error", Failed: true}, nil
		}
		moved = []kb.Moved{{From: paths[0], To: to}}
	} else {
		if err := p.mkdirs(dest); err != nil {
			return nil, err
		}
		mv, sk, err := fs.Move(r, p.lib, paths, dest)
		if err != nil {
			return &Outcome{Result: "Could not move: " + err.Error(), Summary: "error", Failed: true}, nil
		}
		moved, skipped = mv, sk
		if name != "" && len(moved) == 1 {
			if to, err := fs.Rename(r, p.lib, moved[0].To, name); err != nil {
				skipped = append(skipped, kb.Skipped{Path: moved[0].To, Reason: "moved, but not renamed: " + err.Error()})
			} else {
				moved[0].To = to
			}
		}
	}
	changed := []string{}
	var files []conversations.File
	lines := []string{}
	verb := "Moved"
	if renaming {
		verb = "Renamed"
	}
	for _, mv := range moved {
		changed = append(changed, mv.From, mv.To)
		files = append(files, p.file(mv.To))
		m.note(tctx, p.id(), mv.To)
		lines = append(lines, verb+" "+p.shown(mv.From)+" to "+p.link(mv.To))
	}
	lines = append(lines, skippedLines(skipped)...)
	if len(moved) > 0 {
		fs.Changed(r, p.lib, changed)
	} else {
		lines = append(lines, "Nothing was moved.")
	}
	summary := "moved " + countOf(len(moved), "item", "items") + " in " + p.label()
	if renaming {
		summary = "renamed " + p.shown(paths[0])
	}
	if len(skipped) > 0 {
		summary += ", skipped " + itoaInt(len(skipped))
	}
	return &Outcome{Result: strings.Join(lines, "\n"), Summary: summary, Failed: len(moved) == 0, Files: files}, nil
}

// maxSpaceCopy is the largest file copied in a space from the chat.
const maxSpaceCopy = 64 << 20

func (m *Module) copyInSpace(r *http.Request, tctx ToolCtx, p place, paths []string, dest string) (*Outcome, error) {
	if dest != "" {
		if err := p.check(dest); err != nil {
			return nil, err
		}
	}
	var lines []string
	var files []conversations.File
	var made []string
	for _, rel := range paths {
		if err := p.check(rel); err != nil {
			lines = append(lines, "Skipped "+rel+": not a path in "+p.label())
			continue
		}
		b, err := p.read(rel, maxSpaceCopy)
		switch {
		case errors.Is(err, errIsFolder):
			lines = append(lines, "Skipped "+p.shown(rel)+": a folder in "+p.label()+" is not copied from the chat yet; copy the files in it.")
			continue
		case errors.Is(err, errTooLarge):
			lines = append(lines, "Skipped "+p.shown(rel)+": over 64 MB.")
			continue
		case err != nil:
			lines = append(lines, "Skipped "+p.shown(rel)+": "+err.Error())
			continue
		}
		to := freeName(p, path2(dest, path.Base(rel)))
		if err := p.write(to, b); err != nil {
			lines = append(lines, "Skipped "+p.shown(rel)+": "+err.Error())
			continue
		}
		made = append(made, to)
		files = append(files, p.file(to))
		m.note(tctx, p.id(), to)
		lines = append(lines, "Copied "+p.shown(rel)+" to "+p.link(to))
	}
	if len(made) > 0 {
		kb.SpaceFiles.Changed(r, p.lib, made)
	} else {
		lines = append(lines, "Nothing was copied.")
	}
	return &Outcome{Result: strings.Join(lines, "\n"), Summary: "copied " + countOf(len(made), "item", "items") + " in " + p.label(), Failed: len(made) == 0, Files: files}, nil
}

// freeName is rel, or "<name> copy", "<name> copy 2" and on when taken.
func freeName(p place, rel string) string {
	if !p.exists(rel) {
		return rel
	}
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for i := 1; i < 1000; i++ {
		c := stem + " copy" + ext
		if i > 1 {
			c = stem + " copy " + itoaInt(i) + ext
		}
		if !p.exists(c) {
			return c
		}
	}
	return stem + " copy " + itoaInt(1000) + ext
}
