package libapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// The files a reply names. A chat draws a path in a reply as a link to the
// file when it is one in the person's libraries: the page asks here for the
// paths it shows, together, and gets back those that are, each with its
// library. Everything is looked up as the person: only among the libraries
// they have, and, in one on another system, with their own key there. A
// path that leads outside its library is nothing.

const (
	maxCited     = 300
	maxCitedAlso = 12
	maxCitedLen  = 1000
	// citedWait is how long an answer waits for libraries on other systems;
	// what they have not answered by then is left out of it.
	citedWait = 8 * time.Second
)

// citedAsk is a path as a reply wrote it, and the paths it may stand for
// where it is a name the chat's own steps gave in full.
type citedAsk struct {
	Path string      `json:"path"`
	Also []citedAlso `json:"also"`
}

type citedAlso struct {
	Path string `json:"path"`
	Lib  string `json:"lib"`
}

// citedMachine is the machine a chat's pw code session runs on, as the page
// lists it: "studio" is the one the Studio drives for the person.
type citedMachine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

type citedBody struct {
	Paths []citedAsk `json:"paths"`
	// Machine and Cwd are the session's; none for a chat the Studio
	// assistant answers.
	Machine *citedMachine `json:"machine"`
	Cwd     string        `json:"cwd"`
	// Libraries is the chat's scope: ids, or none for all.
	Libraries []string `json:"libraries"`
}

// citedFile is a file a path names; Lib is "" for the main library.
type citedFile struct {
	Path string `json:"path"`
	Lib  string `json:"lib,omitempty"`
}

// citedLib is one of the person's libraries as this looks in it.
type citedLib struct {
	libraries.Library
	// root is the folder its files are in, here or on its system.
	root string
	// real is root with its links followed, for one here.
	real string
	// here: its files are on this computer.
	here bool
	// onMachine: its files are on the machine the session runs on.
	onMachine bool
	inScope   bool
	// home: the session's folder is in it.
	home bool
	// near: asking costs no connection that is not there already.
	near   bool
	target *libraries.Target
}

type citedCand struct {
	lib int
	rel string
}

// citedGroup is places a path may be, looked in together: the first that
// has the file, or, for only, the one that has it when no other does.
type citedGroup struct {
	cands []citedCand
	only  bool
}

type citing struct {
	s    *Service
	body citedBody
	libs []citedLib
	// own: the session runs on this computer, as the account the Studio
	// runs as.
	own bool
	// cwd is the session's folder; inLib says a library holds it.
	cwd   string
	inLib bool
	known map[citedCand]bool
}

// citedPaths is POST /api/libraries/cited.
func (s *Service) citedPaths(w http.ResponseWriter, r *http.Request) error {
	raw, err := readBody(r)
	if err != nil {
		return err
	}
	var body citedBody
	if json.Unmarshal(raw, &body) != nil {
		return apierr.New(400, "paths must be a list")
	}
	viewer, err := s.viewer(r)
	if err != nil {
		return err
	}
	if len(body.Paths) > maxCited {
		body.Paths = body.Paths[:maxCited]
	}
	c := s.citing(body, viewer)
	out := map[string]citedFile{}
	for p, cand := range c.find(r.Context()) {
		f := citedFile{Path: cand.rel}
		if l := c.libs[cand.lib]; !l.Primary {
			f.Lib = l.ID
		}
		out[p] = f
	}
	apierr.JSON(w, 200, map[string]any{"files": out})
	return nil
}

func (s *Service) citing(body citedBody, viewer libraries.Viewer) *citing {
	c := &citing{s: s, body: body, known: map[citedCand]bool{}}
	auth := s.a.Cfg.AuthEnabled()
	host, _ := os.Hostname()
	mach := body.Machine
	c.own = mach != nil && !auth && (mach.ID == "studio" || sameHost(mach.Hostname, host))
	scope := map[string]bool{}
	for _, id := range body.Libraries {
		scope[id] = true
	}
	all := len(scope) == 0 || scope["all"]
	for _, l := range s.Reg.List(s.kbLabel(), viewer) {
		e := citedLib{Library: l, inScope: all || scope[l.ID]}
		switch {
		case l.Remote():
			t, err := s.Engine.Target(l.IndexRoot)
			if err != nil || t == nil {
				continue
			}
			e.target, e.root = t, path.Clean(t.SourceRoot)
			e.onMachine = mach != nil && onSite(mach, t, l.Site, auth)
			e.near = (!all && scope[l.ID]) || l.Space || s.Engine.Pool.Live(t.Site, t.Key)
		case l.SourceRoot != "" && l.ReadThrough == nil && l.Bucket == nil:
			e.here, e.near, e.root = true, true, filepath.Clean(l.SourceRoot)
			e.real = e.root
			if real, err := filepath.EvalSymlinks(e.root); err == nil {
				e.real = real
			}
			// A library in a folder on this server is on the Studio's
			// cluster, where the Studio drives a signed-in person's pw code.
			e.onMachine = mach != nil && (c.own || (auth && mach.ID == "studio"))
		default:
			e.near = true
		}
		if e.root == "." {
			e.root = ""
		}
		c.libs = append(c.libs, e)
	}
	if strings.HasPrefix(body.Cwd, "/") && len(body.Cwd) <= maxCitedLen {
		c.cwd = path.Clean(body.Cwd)
		for i := range c.libs {
			l := &c.libs[i]
			if _, ok := l.under(c.cwd); ok && l.onMachine {
				l.home, l.near, c.inLib = true, true, true
			}
		}
	}
	return c
}

// onSite: the library's files are on the session's machine.
func onSite(mach *citedMachine, t *libraries.Target, shown string, auth bool) bool {
	if mach.ID == "studio" {
		home := libraries.HomeCluster()
		return auth && home != "" && remote.ShortName(t.Site) == remote.ShortName(home)
	}
	for _, site := range []string{t.Site, shown} {
		name := remote.ShortName(site)
		if i := strings.LastIndex(name, "@"); i >= 0 {
			name = name[i+1:]
		}
		name, _, _ = strings.Cut(name, ":")
		if name != "" && (strings.EqualFold(name, mach.Name) || sameHost(name, mach.Hostname)) {
			return true
		}
	}
	return false
}

// sameHost: a and b name one host, in full or by its first part.
func sameHost(a, b string) bool {
	if a == "" || b == "" || strings.EqualFold(a, "localhost") {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	a0, _, ad := strings.Cut(a, ".")
	b0, _, bd := strings.Cut(b, ".")
	return strings.EqualFold(a0, b0) && (!ad || !bd)
}

// under is abs's path in the library, when it is in its folder.
func (l citedLib) under(abs string) (string, bool) {
	for _, root := range []string{l.root, l.real} {
		if root == "" || root == "/" {
			continue
		}
		if abs == root {
			return "", true
		}
		if rel, ok := strings.CutPrefix(abs, strings.TrimSuffix(root, "/")+"/"); ok {
			return rel, true
		}
	}
	return "", false
}

// citable is rel as a file the Library shows may be named: no part hidden,
// left out of the Library, or leading out of it.
func citable(rel string) (string, bool) {
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || len(rel) > maxCitedLen || strings.HasSuffix(rel, "/") || strings.ContainsAny(rel, "\x00\r\n") {
		return "", false
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." || kb.HiddenName(p) || kb.Excluded(p, i < len(parts)-1) {
			return "", false
		}
	}
	return rel, true
}

// abs is the places an absolute path on the session's machine may be, the
// innermost folder first. A chat with no session names paths on this
// computer, or in the libraries it searches.
func (c *citing) abs(p string) (out []citedCand) {
	p = path.Clean(p)
	for i, l := range c.libs {
		if c.body.Machine != nil && !l.onMachine || c.body.Machine == nil && !l.here && !(l.inScope && l.near) {
			continue
		}
		if rel, ok := l.under(p); ok {
			if rel, ok := citable(rel); ok {
				out = append(out, citedCand{i, rel})
			}
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j].rel) < len(out[j-1].rel); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// named is the places a path that starts with a library's name may be, as
// the chat's tools and the Library's All view write it.
func (c *citing) named(p string) (out []citedCand) {
	for i, l := range c.libs {
		for _, head := range []string{l.Label + "/", l.ID + "/", l.ID + ":"} {
			if len(head) > 1 && len(p) > len(head) && strings.EqualFold(p[:len(head)], head) {
				if rel, ok := citable(p[len(head):]); ok {
					out = append(out, citedCand{i, rel})
				}
				break
			}
		}
	}
	return out
}

// full is p as an absolute path where it can be said to be one: as written,
// from the session's home on this computer, or from the session's folder.
func (c *citing) full(p string) string {
	switch {
	case strings.HasPrefix(p, "/"):
		return p
	case strings.HasPrefix(p, "~/"):
		if home, err := os.UserHomeDir(); err == nil && (c.own || c.body.Machine == nil && !c.s.a.Cfg.AuthEnabled()) {
			return path.Join(filepath.ToSlash(home), p[2:])
		}
		return ""
	case c.cwd != "":
		return path.Join(c.cwd, p)
	}
	return ""
}

// told is the places a path says itself to be in.
func (c *citing) told(p string) []citedGroup {
	gs := []citedGroup{{cands: c.named(p), only: true}}
	if f := c.full(p); f != "" {
		gs = append(gs, citedGroup{cands: c.abs(f)})
	}
	return gs
}

// foreign: p, relative, may well be a file of the session's own folder,
// which no library holds, and not a library's of the same name.
func (c *citing) foreign(p string) bool {
	if c.body.Machine == nil || c.inLib || path.Base(c.cwd) == "studio-chat" {
		return false
	}
	if !c.own || c.cwd == "" || !strings.Contains(p, "/") {
		return true
	}
	_, err := os.Lstat(path.Join(c.cwd, p))
	return err == nil
}

// everywhere is the libraries a relative path may be in that pick takes.
func (c *citing) everywhere(p string, pick func(l citedLib) bool) (out []citedCand) {
	rel, ok := citable(p)
	if !ok || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return nil
	}
	for i, l := range c.libs {
		if pick(l) {
			out = append(out, citedCand{i, rel})
		}
	}
	return out
}

// find is the file each path names, for those that name one.
func (c *citing) find(ctx context.Context) map[string]citedCand {
	ctx, cancel := context.WithTimeout(ctx, citedWait)
	defer cancel()
	found := map[string]citedCand{}
	left := map[string][]citedAlso{}
	for _, a := range c.body.Paths {
		p := strings.TrimSpace(a.Path)
		if p == "" || p != a.Path || len(p) > maxCitedLen {
			continue
		}
		left[p] = append(left[p], a.Also...)
	}
	home := func(l citedLib) bool { return l.home }
	main := func(l citedLib) bool { return l.Primary && l.inScope }
	rest := func(l citedLib) bool { return !l.Primary && l.inScope && l.near }
	out := func(l citedLib) bool { return !l.inScope && l.here }
	phases := []func(p string, also []citedAlso) []citedGroup{
		func(p string, _ []citedAlso) []citedGroup { return c.told(p) },
		// What the chat's own steps and links call by that name, before
		// any other file that has it.
		func(p string, also []citedAlso) []citedGroup {
			g := citedGroup{only: true}
			for _, a := range also[:min(len(also), maxCitedAlso)] {
				for i, l := range c.libs {
					if a.Lib != "" && l.ID == a.Lib || a.Lib == "" && l.Primary && l.inScope {
						if rel, ok := citable(a.Path); ok {
							g.cands = append(g.cands, citedCand{i, rel})
						}
					}
				}
				if a.Lib == "" {
					for _, t := range c.told(a.Path) {
						g.cands = append(g.cands, t.cands...)
					}
				}
			}
			return []citedGroup{g}
		},
		func(p string, _ []citedAlso) []citedGroup {
			if c.foreign(p) {
				return nil
			}
			return []citedGroup{{cands: c.everywhere(p, home)}, {cands: c.everywhere(p, main)}, {cands: c.everywhere(p, rest), only: true}}
		},
		func(p string, _ []citedAlso) []citedGroup {
			if c.foreign(p) {
				return nil
			}
			return []citedGroup{{cands: c.everywhere(p, out), only: true}}
		},
	}
	for _, phase := range phases {
		groups := map[string][]citedGroup{}
		var ask []citedCand
		for p, also := range left {
			groups[p] = phase(p, also)
			for _, g := range groups[p] {
				ask = append(ask, g.cands...)
			}
		}
		c.look(ctx, ask)
		for p := range left {
			for _, g := range groups[p] {
				var has []citedCand
				seen := map[citedCand]bool{}
				for _, cand := range g.cands {
					if c.known[cand] && !seen[cand] {
						seen[cand] = true
						has = append(has, cand)
					}
				}
				if len(has) == 0 {
					continue
				}
				// Two files one name fits: neither.
				if !g.only || len(has) == 1 {
					found[p] = has[0]
				}
				delete(left, p)
				break
			}
		}
	}
	return found
}

// look finds which of cands are files, each library asked once for all of
// its own and the libraries at the same time.
func (c *citing) look(ctx context.Context, cands []citedCand) {
	per := map[int][]string{}
	seen := map[citedCand]bool{}
	for _, cand := range cands {
		if _, done := c.known[cand]; !done && !seen[cand] {
			seen[cand] = true
			per[cand.lib] = append(per[cand.lib], cand.rel)
		}
	}
	type answer struct {
		lib  int
		rels []string
		has  []bool
	}
	answers := make(chan answer, len(per))
	for i, rels := range per {
		// A system that is slow is not waited for past ctx, and what runs
		// there ends by itself.
		go func() { answers <- answer{i, rels, c.has(context.WithoutCancel(ctx), c.libs[i], rels)} }()
	}
	for range per {
		select {
		case a := <-answers:
			for j, rel := range a.rels {
				c.known[citedCand{a.lib, rel}] = a.has[j]
			}
		case <-ctx.Done():
			return
		}
	}
}

// has says which of rels are files of the library.
func (c *citing) has(ctx context.Context, l citedLib, rels []string) []bool {
	out := make([]bool, len(rels))
	switch {
	case l.here:
		for i, rel := range rels {
			out[i] = localFile(l, rel)
		}
	case l.target != nil && l.ReadThrough == nil && l.root != "" && !c.s.Engine.Pool.IsDirect(l.target.Site):
		for at := 0; at < len(rels); at += 100 {
			part := rels[at:min(at+100, len(rels))]
			r, err := c.s.Engine.Pool.Run(ctx, l.target.Site, l.target.Key, append([]string{"sh", "-c", citedScript, "studio-cited", l.root}, part...), 30*time.Second)
			if err != nil {
				return out
			}
			for _, line := range strings.Fields(r.Stdout) {
				if n, err := strconv.Atoi(line); err == nil && n >= 0 && n < len(part) {
					out[at+n] = true
				}
			}
		}
	case l.Bucket != nil || l.ReadThrough != nil:
		// Its files are elsewhere, read one at a time: what its index
		// lists, a folder at a time.
		dirs := map[string]map[string]bool{}
		for i, rel := range rels {
			dir := strings.TrimSuffix(strings.TrimSuffix(rel, path.Base(rel)), "/")
			names, ok := dirs[dir]
			if !ok && len(dirs) < 12 {
				names = map[string]bool{}
				for _, e := range c.listed(ctx, l, dir) {
					names[e.Name] = e.Type != "dir"
				}
				dirs[dir] = names
			}
			out[i] = names[path.Base(rel)]
		}
	}
	return out
}

func (c *citing) listed(ctx context.Context, l citedLib, dir string) []kb.Entry {
	var es []kb.Entry
	var err error
	if l.Remote() {
		es, err = c.s.Engine.RemoteTree(ctx, l.IndexRoot, dir)
	} else if sub, bad := search.CleanRel(dir); bad == nil {
		es, err = c.s.Engine.LocalTree(ctx, l.IndexRoot, sub)
	}
	if err != nil {
		return nil
	}
	return es
}

// localFile: rel is a plain file in the library's folder here, links
// followed only as far as the folder goes.
func localFile(l citedLib, rel string) bool {
	abs, err := kb.Resolve(rel, l.root)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !strings.HasPrefix(real, strings.TrimSuffix(l.real, string(filepath.Separator))+string(filepath.Separator)) {
		return false
	}
	st, err := os.Stat(real)
	return err == nil && st.Mode().IsRegular()
}

// citedScript prints the number of each path that is a plain file in the
// folder it is given first, and not one a link leads out to.
const citedScript = `root=$(cd -P -- "$1" 2>/dev/null && pwd -P) || exit 0
shift
n=0 was= at=
for p; do
  case "$p" in */*) d=${p%/*} ;; *) d=. ;; esac
  if [ "$d" != "$was" ]; then was=$d; at=$(cd -P -- "$root/$d" 2>/dev/null && pwd -P) || at=; fi
  case "$at/" in "$root"/*) f=$at/${p##*/}; [ -f "$f" ] && [ ! -L "$f" ] && echo $n ;; esac
  n=$((n+1))
done
exit 0
`
