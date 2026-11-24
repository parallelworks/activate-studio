package chat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// Where the chat's library tools write. On a Studio where people have
// spaces of their own, a path names its library the way the Library's
// All view does ("My library/notes/a.md", or the main library by its
// name), and each library is written as the file browser writes it: the
// person's space as them, through kb.SpaceFiles, and the main library
// here, for those who may change it (mainlib). Without spaces, every path
// is in the main library, as before.

// place is one library a tool reads and writes, for the person in r.
type place struct {
	m     *Module
	r     *http.Request
	lib   kb.Library
	space bool
}

// mainPlace is the main library.
func (m *Module) mainPlace(r *http.Request) place {
	lib := kb.Library{ID: kb.PrimaryID, Label: m.primaryLabel(), Primary: true, Writable: true}
	return place{m: m, r: r, lib: lib}
}

func (p place) id() string { return p.lib.ID }

// label is the library's name as the person sees it.
func (p place) label() string {
	if p.lib.Label != "" {
		return p.lib.Label
	}
	return "the main library"
}

// shown is a path as the chat names it: the library, then the path.
func (p place) shown(rel string) string {
	if !p.space && !p.m.spacesFor(p.r) {
		return rel
	}
	return p.label() + "/" + rel
}

// link opens rel in the Library viewer, in its own library.
func (p place) link(rel string) string {
	if !p.space {
		if !p.m.spacesFor(p.r) {
			return libraryLink(rel)
		}
		return "[" + p.shown(rel) + "](#open=file:" + strings.ReplaceAll(rel, " ", "%20") + ")"
	}
	return "[" + p.shown(rel) + "](#open=file:" + strings.ReplaceAll(rel, " ", "%20") + "&lib=" + p.lib.ID + ")"
}

// key names rel for locks and the made-from records: a space's paths are
// kept apart from the main library's.
func (p place) key(rel string) string {
	if p.space {
		return "space:" + p.lib.ID + ":" + rel
	}
	return rel
}

// unkey is rel from a key of this place.
func (p place) unkey(k string) string {
	return strings.TrimPrefix(k, "space:"+p.lib.ID+":")
}

// writable is nil when the person may change this library.
func (p place) writable() error {
	if p.space {
		if kb.SpaceFiles == nil {
			return &toolError{p.label() + " is read only here."}
		}
		return nil
	}
	return mainLibraryWritable(p.r)
}

// check refuses a path that leaves the library or names a hidden or
// excluded folder.
func (p place) check(rel string) error {
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return &toolError{"Not writable: " + rel + " is not a path in the library."}
		}
		if kb.ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return &toolError{"Not writable: " + part + " is excluded."}
		}
	}
	if !p.space {
		if _, err := kb.Resolve(rel, p.m.A.Cfg.KBRoot); err != nil {
			return &toolError{err.Error()}
		}
	}
	return nil
}

// read is rel's bytes; a missing file is os.ErrNotExist.
func (p place) read(rel string, max int64) ([]byte, error) {
	if !p.space {
		abs, err := kb.Resolve(rel, p.m.A.Cfg.KBRoot)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, os.ErrNotExist
		}
		if st.IsDir() {
			return nil, errIsFolder
		}
		if st.Size() > max {
			return nil, errTooLarge
		}
		p.m.writeMu.Lock()
		defer p.m.writeMu.Unlock()
		return os.ReadFile(abs)
	}
	b, err := kb.SpaceFiles.Get(p.r, p.lib, rel, max)
	var ae *apierr.Error
	if errors.As(err, &ae) {
		switch ae.Status {
		case 404:
			return nil, os.ErrNotExist
		case 409:
			// A folder or a link is a 409, and so is a space that cannot
			// be reached (no key, no folder yet): only a listing tells.
			if p.isFolder(rel) {
				return nil, errIsFolder
			}
			return nil, err
		case 413:
			return nil, errTooLarge
		}
	}
	return b, err
}

var (
	errIsFolder = errors.New("is a folder")
	errTooLarge = errors.New("too large")
)

// exists says whether something is at rel.
func (p place) exists(rel string) bool {
	_, err := p.read(rel, 1)
	return err == nil || errors.Is(err, errIsFolder) || errors.Is(err, errTooLarge)
}

// write puts data at rel whole, making its folder.
func (p place) write(rel string, data []byte) error {
	if !p.space {
		abs, err := kb.Resolve(rel, p.m.A.Cfg.KBRoot)
		if err != nil {
			return err
		}
		return p.m.writeLibraryFile(abs, data)
	}
	if err := p.mkdirs(parentOf(rel)); err != nil {
		return err
	}
	return kb.SpaceFiles.Put(p.r, p.lib, rel, bytes.NewReader(data), int64(len(data))+1)
}

// mkdirs makes dir and the folders above it in a space; an error from a
// folder already there is not one.
func (p place) mkdirs(dir string) error {
	if dir == "" {
		return nil
	}
	if !p.space {
		return p.m.ensureFolder(dir)
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		_ = kb.SpaceFiles.Mkdir(p.r, p.lib, strings.Join(parts[:i+1], "/"))
	}
	return nil
}

// reindex tells the library's index that rels changed; ms is how long a
// re-index here took.
func (p place) reindex(ctx context.Context, rels ...string) int64 {
	if p.space {
		kb.SpaceFiles.Changed(p.r, p.lib, rels)
		return 0
	}
	var ms int64
	seen := map[string]bool{}
	for _, rel := range rels {
		top := strings.SplitN(rel, "/", 2)[0]
		if seen[top] {
			continue
		}
		seen[top] = true
		n, _ := kb.ReindexForFile(ctx, kb.ProviderIndexer{A: p.m.A}, rel)
		ms += n
	}
	return ms
}

// images finds a document's images in this library.
func (p place) images(dir string) docwrite.ImageSource {
	if !p.space {
		return libraryImages(p.m.A.Cfg.KBRoot, dir)
	}
	main := libraryImages(p.m.A.Cfg.KBRoot, dir)
	return func(ref string) ([]byte, error) {
		low := strings.ToLower(strings.TrimSpace(ref))
		if strings.HasPrefix(low, "data:") || strings.HasPrefix(low, "http") || strings.HasPrefix(low, "//") {
			return main(ref)
		}
		for _, cand := range libraryCandidates(strings.Trim(strings.TrimSpace(ref), "<>"), dir) {
			if p.check(cand) != nil {
				continue
			}
			b, err := p.read(cand, maxDocImageBytes)
			if err == nil {
				return b, nil
			}
			if errors.Is(err, errTooLarge) {
				return nil, errors.New("over 25 MB")
			}
		}
		return nil, errors.New("not found in " + p.label())
	}
}

func parentOf(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// lockKey is what one write at a time holds for rel.
func (p place) lockKey(rel string) string {
	if p.space {
		return p.key(rel)
	}
	if abs, err := kb.Resolve(rel, p.m.A.Cfg.KBRoot); err == nil {
		return abs
	}
	return rel
}

// file is rel as a tool's result lists it, in its library.
func (p place) file(rel string) conversations.File {
	id := p.id()
	return conversations.File{Path: rel, Library: &id}
}

// text is a text file of this library, or why not. wait is how long to
// wait for a file that is not there yet: the call that writes it may run
// beside this one.
func (p place) text(rel string, wait time.Duration) (string, string) {
	if !p.space {
		return p.m.libraryText(rel, wait)
	}
	if p.check(rel) != nil {
		return "", "Not readable: " + rel + " is not a path in " + p.label() + "."
	}
	deadline := time.Now().Add(wait)
	for {
		b, err := p.read(rel, maxEditBytes)
		switch {
		case err == nil:
			if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
				return "", p.shown(rel) + " is not a text file."
			}
			return string(b), ""
		case errors.Is(err, errIsFolder):
			return "", p.shown(rel) + " is a folder."
		case errors.Is(err, errTooLarge):
			return "", p.shown(rel) + " is over 2 MB."
		case !errors.Is(err, os.ErrNotExist):
			return "", "Could not read " + p.shown(rel) + ": " + err.Error()
		}
		if time.Now().After(deadline) {
			return "", p.shown(rel) + " is not in " + p.label() + ". list_kb_dir shows what is there."
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// toolPlace is where a tool works on rel: the library the call names (its
// library argument, or the front of its path), else, for a new file, the
// chat's place (decideWhere), and for a file already there, the one of the
// person's libraries that has it. ask is set when the person must say.
func (m *Module) toolPlace(r *http.Request, ctx ToolCtx, args map[string]any, rel string, creating bool) (p place, rest, why string, ask *whereDecision, err error) {
	p, rest, named, err := m.namedPlace(r, argString(args, "library"), rel)
	if err != nil || named {
		return p, rest, "", nil, err
	}
	if !m.spacesFor(r) {
		return m.mainPlace(r), rel, "", nil, nil
	}
	// The person named the library in their own words ("save it to my
	// library"): that wins, whether or not the model passed it on.
	if creating {
		if q, ok := m.userNamed(r, ctx); ok {
			return q, rel, "the user asked for " + q.label(), nil, nil
		}
	}
	own, _ := ownSpaceLib(r)
	mine, main := place{m: m, r: r, lib: own, space: true}, m.mainPlace(r)
	if !creating {
		inMine, inMain := mine.exists(rel), main.exists(rel)
		switch {
		case inMine && !inMain:
			return mine, rel, "", nil, nil
		case inMain && !inMine:
			return main, rel, "", nil, nil
		case !inMine && !inMain:
			return place{}, rel, "", nil, &toolError{rel + " is in neither " + mine.label() + " nor " + main.label() + ". Name its library in the path, such as " + mine.label() + "/" + rel + "; list_kb_dir shows what is there."}
		}
	}
	d := m.decideWhere(r, ctx, rel, args)
	if d.ask {
		if !creating {
			return place{}, rel, "", nil, &toolError{rel + " is in both " + mine.label() + " and " + main.label() + ". Ask the user which one they mean, and name it in the path."}
		}
		return place{}, rel, "", &d, nil
	}
	return d.place, rel, d.why, nil, nil
}

// isFolder says whether rel is a folder in a space, from its parent's
// listing.
// folderAt says whether rel is a folder, in the main library or a space.
func (p place) folderAt(rel string) bool {
	if !p.space {
		abs, err := kb.Resolve(rel, p.m.A.Cfg.KBRoot)
		if err != nil {
			return false
		}
		st, err := os.Stat(abs)
		return err == nil && st.IsDir()
	}
	return p.isFolder(rel)
}

func (p place) isFolder(rel string) bool {
	entries, err := kb.SpaceFiles.List(p.r, p.lib, parentOf(rel))
	if err != nil {
		return false
	}
	base := rel[strings.LastIndex(rel, "/")+1:]
	for _, e := range entries {
		if e.Name == base {
			return e.Type == "dir"
		}
	}
	return false
}
