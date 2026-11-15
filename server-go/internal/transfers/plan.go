package transfers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// item is one file a transfer sends: Rel at the source (relative to its
// root), Out under the destination folder (and so under the staging folder).
type item struct {
	Rel, Out string
	Size     int64
	Mtime    int64
	Mode     uint32
}

// planRec is a plan with everything behind its numbers. A plan shows the
// first 50 replacements and deletions; the record keeps them all.
type planRec struct {
	Plan
	owner      string
	req        Request
	src, dst   *loc
	items      []string
	dstPath    string
	files      []item
	srcDirs    []string
	replaceSet map[string]bool // by Out
	deleteList []string        // relative to the destination's root
	deleteDirs []string
	// sourceOuts is every file the source has, sent or not (sync), by Out.
	sourceOuts map[string]bool
	// bare: the source is typed paths in a CONDUIT namespace that Studio
	// cannot list, so the numbers are counted when the job runs.
	bare    bool
	expires time.Time
	// set is the Transfers settings that applied to the viewer.
	set Settings
}

func newID() string {
	b := make([]byte, 9)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Plan works out what a request would do and keeps it for 15 minutes. An
// error is a 400 (or the location's own status) with a reason a person
// can act on.
func (s *Service) Plan(ctx context.Context, v Viewer, req Request) (*Plan, error) {
	p, err := s.plan(ctx, v, req)
	if err != nil {
		return nil, err
	}
	p.ID = newID()
	p.expires = s.now().Add(PlanTTL)
	p.ExpiresAt = iso(p.expires)
	s.mu.Lock()
	for id, x := range s.plans {
		if s.now().After(x.expires) {
			delete(s.plans, id)
		}
	}
	s.plans[p.ID] = p
	s.mu.Unlock()
	out := p.Plan
	return &out, nil
}

func (s *Service) plan(ctx context.Context, v Viewer, req Request) (*planRec, error) {
	set := s.settingsFor(v)
	if req.Options.Checksum == nil {
		c := set.Defaults.Checksum
		req.Options.Checksum = &c
	}
	if req.Options.PreserveTimes == nil {
		t := set.Defaults.PreserveTimes
		req.Options.PreserveTimes = &t
	}
	switch req.Action {
	case ActionCopy, ActionMove, ActionSync:
	default:
		return nil, apierr.New(400, "action: copy, move or sync")
	}
	if m := req.Options.Mover; m != "" && m != "auto" && s.mover(m) == nil {
		return nil, apierr.New(400, "mover: auto, rsync, studio-copy or conduit")
	}
	if req.Options.DeleteExtra && req.Action != ActionSync {
		return nil, apierr.New(400, "deleteExtra applies to a sync only")
	}
	if n := len(req.Source.Paths); n == 0 || n > MaxPaths {
		return nil, apierr.New(400, "name between 1 and %d source paths", MaxPaths)
	}
	if req.Action == ActionSync && len(req.Source.Paths) != 1 {
		return nil, apierr.New(400, "a sync takes one source folder")
	}
	src, err := s.resolve(ctx, v, req.Source.Location)
	if err != nil {
		return nil, err
	}
	dst, err := s.resolve(ctx, v, req.Destination.Location)
	if err != nil {
		return nil, err
	}
	if !dst.Available {
		return nil, apierr.New(502, "%s cannot be reached: %s", dst.Label, dst.Why)
	}
	if dst.fs == nil {
		return nil, apierr.New(400, "%s cannot be a destination: Studio checks every file that lands, and cannot read this location itself. In Settings, Transfers, mount a location for it and choose that", dst.Label)
	}
	if !dst.Writable {
		return nil, apierr.New(400, "%s is read only", dst.Label)
	}
	if !src.Available {
		return nil, apierr.New(502, "%s cannot be reached: %s", src.Label, src.Why)
	}
	if req.Action == ActionMove && !src.Writable {
		return nil, apierr.New(400, "%s is read only, so nothing can be moved out of it; copy instead", src.Label)
	}
	dstPath, err := CleanRel(req.Destination.Path)
	if err != nil {
		return nil, err
	}
	p := &planRec{owner: v.owner(), req: req, src: src, dst: dst, dstPath: dstPath, set: set,
		replaceSet: map[string]bool{}, sourceOuts: map[string]bool{}}
	p.Action = req.Action
	if _, _, err := dst.fs.List(ctx, dstPath, 1); err != nil {
		if statusOf(err) == 404 {
			return nil, apierr.New(400, "%s: the destination folder is not there", orRoot(dstPath))
		}
		return nil, err
	}
	if src.fs == nil {
		if src.Kind != KindConduit || req.Action != ActionCopy {
			return nil, apierr.New(400, "%s can only be copied from: Studio cannot read it to check a move or a sync", src.Label)
		}
		if err := s.planBare(p); err != nil {
			return nil, err
		}
	} else if err := s.planListed(ctx, p); err != nil {
		return nil, err
	}
	p.Source = End{Location: src.ID, Label: src.Label, Paths: p.items}
	dp := dstPath
	p.Destination = End{Location: dst.ID, Label: dst.Label, Path: &dp}
	p.Label = req.Options.Label
	if p.Label == "" {
		p.Label = defaultLabel(p)
	}
	if free, ok := dst.fs.Free(ctx, dstPath); ok {
		p.FreeBytes = &free
		if p.Bytes > 0 && p.Bytes > free {
			return nil, apierr.New(400, "this transfer needs %s and %s is free at the destination", humanBytes(p.Bytes), humanBytes(free))
		}
	}
	if err := s.chooseMover(ctx, p); err != nil {
		return nil, err
	}
	p.warn()
	return p, nil
}

func defaultLabel(p *planRec) string {
	what := p.items[0]
	if what == "" {
		what = p.src.Label
	} else {
		what = path.Base(what)
	}
	if len(p.items) > 1 {
		what = fmt.Sprintf("%s and %d more", what, len(p.items)-1)
	}
	verb := map[string]string{ActionCopy: "Copy", ActionMove: "Move", ActionSync: "Sync"}[p.Action]
	return fmt.Sprintf("%s %s to %s", verb, what, p.dst.Label)
}

// planBare is a copy from typed CONDUIT paths: checked for form only.
func (s *Service) planBare(p *planRec) error {
	p.bare = true
	seen := map[string]bool{}
	for _, raw := range p.req.Source.Paths {
		c := path.Clean(strings.TrimSpace(raw))
		if !strings.HasPrefix(c, "/") || strings.Contains(raw, "..") || strings.ContainsAny(raw, "\x00\n\r") {
			return apierr.New(400, "%q: a CONDUIT path is absolute, with no \"..\"", short(raw))
		}
		name := path.Base(c)
		if c == "/" || strings.HasPrefix(name, ".") {
			return apierr.New(400, "%q: name a file or folder, not hidden and not the top", short(raw))
		}
		if seen[name] {
			return apierr.New(400, "two paths are named %s; they would land on each other", name)
		}
		seen[name] = true
		p.items = append(p.items, c)
	}
	p.Files, p.Dirs, p.Bytes = -1, -1, -1
	return nil
}

// planListed lists the source and the destination and works out what
// goes, what it replaces and, for a sync with deleteExtra, what goes away.
func (s *Service) planListed(ctx context.Context, p *planRec) error {
	req := p.req
	seen := map[string]bool{}
	for _, raw := range req.Source.Paths {
		c, err := CleanRel(raw)
		if err != nil {
			return err
		}
		if c == "" && req.Action != ActionSync {
			return apierr.New(400, "name files or folders inside %s, not the whole location", p.src.Label)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		p.items = append(p.items, c)
	}
	sort.Strings(p.items)
	names := map[string]bool{}
	for i, it := range p.items {
		if i > 0 && within(it, p.items[i-1]) {
			return apierr.New(400, "%s is inside %s, which is named too", it, p.items[i-1])
		}
		if req.Action != ActionSync {
			n := path.Base(it)
			if names[n] {
				return apierr.New(400, "two paths are named %s; they would land on each other", n)
			}
			names[n] = true
		}
		if p.src.ID == p.dst.ID {
			if within(p.dstPath, it) {
				return apierr.New(400, "%s cannot go into itself", it)
			}
			if req.Action != ActionSync && parentOf(it) == p.dstPath {
				return apierr.New(400, "%s is already in that folder", it)
			}
		}
	}
	w, err := p.src.fs.Walk(ctx, p.items, MaxFiles)
	if err != nil {
		if statusOf(err) == 404 {
			return apierr.New(400, "%s", messageOf(err))
		}
		return err
	}
	if req.Action == ActionSync {
		if st, _ := p.src.fs.Stat(ctx, nonEmpty(p.items)); len(p.items[0]) > 0 && st[p.items[0]].Type != 'd' {
			return apierr.New(400, "a sync takes a folder; %s is not one", p.items[0])
		}
	}
	p.Skipped = w.Skipped
	p.srcDirs = w.Dirs
	outOf := func(rel string) string {
		if req.Action == ActionSync {
			return strings.TrimPrefix(strings.TrimPrefix(rel, p.items[0]), "/")
		}
		for _, it := range p.items {
			if within(rel, it) {
				return join(path.Base(it), strings.TrimPrefix(strings.TrimPrefix(rel, it), "/"))
			}
		}
		return rel
	}
	var all []item
	for _, f := range w.Files {
		all = append(all, item{Rel: f.Rel, Out: outOf(f.Rel), Size: f.Size, Mtime: f.Mtime, Mode: f.Mode})
	}
	srcDirOuts := map[string]bool{}
	for _, d := range w.Dirs {
		srcDirOuts[outOf(d)] = true
	}
	if req.Action == ActionSync {
		return s.planSync(ctx, p, all, srcDirOuts)
	}
	return s.planCopy(ctx, p, all, srcDirOuts)
}

func nonEmpty(a []string) []string {
	var out []string
	for _, s := range a {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// planCopy: each item lands under its own name; a name already there is
// refused unless replace, and then only plain files are written over.
func (s *Service) planCopy(ctx context.Context, p *planRec, all []item, srcDirOuts map[string]bool) error {
	var tops []string
	for _, it := range p.items {
		tops = append(tops, join(p.dstPath, path.Base(it)))
	}
	st, err := p.dst.fs.Stat(ctx, tops)
	if err != nil {
		return err
	}
	dstFiles := map[string]FileInfo{}
	var existing []string
	for _, t := range tops {
		fi, ok := st[t]
		if !ok {
			continue
		}
		name := path.Base(t)
		if !p.req.Options.Replace {
			return apierr.New(400, "%s is already in %s; choose Replace to write over what is there, or another folder", name, orRoot(p.dstPath))
		}
		srcIsDir := srcDirOuts[name]
		switch {
		case fi.Type == 'd' && srcIsDir:
			existing = append(existing, t)
		case fi.Type == 'f' && !srcIsDir:
			dstFiles[name] = fi
		default:
			return apierr.New(400, "%s is a %s at the destination and a %s at the source; it cannot be replaced", name, typeWord(fi.Type), map[bool]string{true: "folder", false: "file"}[srcIsDir])
		}
	}
	if len(existing) > 0 {
		w, err := p.dst.fs.Walk(ctx, existing, 0)
		if err != nil {
			return err
		}
		for _, f := range w.Files {
			dstFiles[strings.TrimPrefix(strings.TrimPrefix(f.Rel, p.dstPath), "/")] = f
		}
		for _, d := range w.Dirs {
			dstFiles[strings.TrimPrefix(strings.TrimPrefix(d, p.dstPath), "/")] = FileInfo{Type: 'd'}
		}
		// A link or other entry where a file would land is refused at landing.
	}
	for _, f := range all {
		if fi, ok := dstFiles[f.Out]; ok {
			if fi.Type != 'f' {
				return apierr.New(400, "%s is a folder at the destination; a file cannot replace it", f.Out)
			}
			p.replaceSet[f.Out] = true
		}
		for d := parentOf(f.Out); d != ""; d = parentOf(d) {
			if fi, ok := dstFiles[d]; ok && fi.Type == 'f' {
				return apierr.New(400, "%s is a file at the destination where a folder must go", d)
			}
		}
	}
	p.files = all
	p.finish()
	return nil
}

// planSync sends what is new or changed (size or modification time, or
// sha256 with checksum) and, with deleteExtra, lists what the destination
// has that the source does not.
func (s *Service) planSync(ctx context.Context, p *planRec, all []item, srcDirOuts map[string]bool) error {
	dstFiles := map[string]FileInfo{}
	var dstDirs []string
	w, err := p.dst.fs.Walk(ctx, []string{p.dstPath}, 0)
	if err != nil {
		return err
	}
	for _, f := range w.Files {
		dstFiles[strings.TrimPrefix(strings.TrimPrefix(f.Rel, p.dstPath), "/")] = f
	}
	for _, d := range w.Dirs {
		if r := strings.TrimPrefix(strings.TrimPrefix(d, p.dstPath), "/"); r != "" {
			dstDirs = append(dstDirs, r)
		}
	}
	dirAt := map[string]bool{}
	for _, d := range dstDirs {
		dirAt[d] = true
	}
	var same []item
	for _, f := range all {
		p.sourceOuts[f.Out] = true
		if dirAt[f.Out] {
			return apierr.New(400, "%s is a folder at the destination and a file at the source", f.Out)
		}
		d, ok := dstFiles[f.Out]
		switch {
		case !ok:
			p.files = append(p.files, f)
		case d.Size != f.Size:
			p.files = append(p.files, f)
			p.replaceSet[f.Out] = true
		case p.req.Options.checksum():
			same = append(same, f)
		case d.Mtime != f.Mtime:
			p.files = append(p.files, f)
			p.replaceSet[f.Out] = true
		}
	}
	if len(same) > 0 {
		var srcRels, dstRels []string
		for _, f := range same {
			srcRels = append(srcRels, f.Rel)
			dstRels = append(dstRels, join(p.dstPath, f.Out))
		}
		a, err := p.src.fs.Sha256(ctx, srcRels)
		if err != nil {
			return err
		}
		b, err := p.dst.fs.Sha256(ctx, dstRels)
		if err != nil {
			return err
		}
		for i, f := range same {
			if a[srcRels[i]] != b[dstRels[i]] {
				p.files = append(p.files, f)
				p.replaceSet[f.Out] = true
			}
		}
	}
	for out := range srcDirOuts {
		if _, ok := dstFiles[out]; ok && out != "" {
			return apierr.New(400, "%s is a file at the destination and a folder at the source", out)
		}
	}
	if p.req.Options.DeleteExtra {
		for out := range dstFiles {
			if !p.sourceOuts[out] {
				p.deleteList = append(p.deleteList, join(p.dstPath, out))
			}
		}
		sort.Strings(p.deleteList)
		for _, d := range dstDirs {
			if !srcDirOuts[d] {
				p.deleteDirs = append(p.deleteDirs, join(p.dstPath, d))
			}
		}
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].Out < p.files[j].Out })
	p.finish()
	return nil
}

// finish fills the numbers the page shows.
func (p *planRec) finish() {
	p.Files = len(p.files)
	dirs := map[string]bool{}
	for _, f := range p.files {
		p.Bytes += f.Size
		for d := parentOf(f.Out); d != ""; d = parentOf(d) {
			dirs[d] = true
		}
	}
	p.Dirs = len(dirs)
	p.Replaces = []string{}
	var reps []string
	for out := range p.replaceSet {
		reps = append(reps, join(p.dstPath, out))
	}
	sort.Strings(reps)
	p.ReplacesCount = len(reps)
	p.Replaces = firstN(reps, listShown)
	p.DeletesCount = len(p.deleteList)
	p.Deletes = firstN(p.deleteList, listShown)
}

func firstN(a []string, n int) []string {
	if a == nil {
		return []string{}
	}
	if len(a) > n {
		return append([]string(nil), a[:n]...)
	}
	return append([]string(nil), a...)
}

func (p *planRec) warn() {
	p.Warnings = []string{}
	if n := p.Skipped.Hidden; n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d hidden %s inside the folders will be left out", n, plural(n, "entry", "entries")))
	}
	if n := p.Skipped.Links; n > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s inside the folders will be left out", n, plural(n, "link", "links")))
	}
	if p.ReplacesCount > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s at the destination will be replaced", p.ReplacesCount, plural(p.ReplacesCount, "file", "files")))
	}
	if p.DeletesCount > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s at the destination that the source does not have will be deleted", p.DeletesCount, plural(p.DeletesCount, "file", "files")))
	}
	if p.bare {
		p.Warnings = append(p.Warnings, "Studio cannot list CONDUIT paths, so files and bytes are counted as the transfer runs, and what lands is checked against CONDUIT's own count")
	}
	if p.Action == ActionSync && p.Files == 0 && p.DeletesCount == 0 {
		p.Warnings = append(p.Warnings, "nothing has changed: the destination already matches")
	}
	if p.Mover == MoverCopy && p.Files > 2000 {
		p.Warnings = append(p.Warnings, "studio-copy sends one file at a time, which is slow on many small files")
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func typeWord(t byte) string {
	switch t {
	case 'd':
		return "folder"
	case 'f':
		return "file"
	case 'l':
		return "link"
	}
	return "special file"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// chooseMover asks each mover whether it can do this transfer and takes
// the one asked for, or the first that can.
func (s *Service) chooseMover(ctx context.Context, p *planRec) error {
	want := p.req.Options.Mover
	off := p.set.off()
	// The settings' default mover is preferred where it can do the
	// transfer; a mover the request names must.
	prefer := ""
	if want == "" || want == "auto" {
		prefer = p.set.Defaults.Mover
	}
	p.Movers = []MoverChoice{}
	for _, m := range s.movers {
		ok, why := false, "turned off in Settings, Transfers"
		if !off[m.Name()] {
			ok, why = m.CanReach(ctx, p)
		}
		p.Movers = append(p.Movers, MoverChoice{Name: m.Name(), OK: ok, Why: why})
		if !ok {
			continue
		}
		if p.Mover == "" && (want == "" || want == "auto") {
			p.Mover, p.MoverWhy = m.Name(), why
		}
		if want == m.Name() || prefer == m.Name() {
			p.Mover, p.MoverWhy = m.Name(), why
		}
	}
	if p.Mover != "" {
		return nil
	}
	if want != "" && want != "auto" {
		for _, c := range p.Movers {
			if c.Name == want {
				return apierr.New(400, "%s cannot do this transfer: %s", want, c.Why)
			}
		}
	}
	var whys []string
	for _, c := range p.Movers {
		whys = append(whys, c.Name+": "+c.Why)
	}
	return apierr.New(400, "no mover can do this transfer (%s)", strings.Join(whys, "; "))
}

// takePlan is a plan of the viewer's, while it holds.
func (s *Service) takePlan(v Viewer, id string, expiry bool) (*planRec, error) {
	s.mu.Lock()
	p := s.plans[id]
	s.mu.Unlock()
	if p == nil || p.owner != v.owner() {
		return nil, apierr.New(404, "no plan %q; plan the transfer again", short(id))
	}
	if expiry && s.now().After(p.expires) {
		return nil, apierr.New(409, "the plan expired; plan the transfer again to see what it would do now")
	}
	return p, nil
}

// confirm lists again what a plan covers and refuses it when it grew past
// what was shown: more files, or a replacement or deletion it did not list.
// The new listing is what runs, with deletions held to those shown.
func (s *Service) confirm(ctx context.Context, v Viewer, old *planRec) (*planRec, error) {
	req := old.req
	req.Options.Mover = old.Mover
	p, err := s.plan(ctx, v, req)
	if err != nil {
		return nil, err
	}
	if !old.bare && p.Files > old.Files {
		return nil, apierr.New(409, "the source has changed since the plan: %d files to send where the plan showed %d; plan it again", p.Files, old.Files)
	}
	for out := range p.replaceSet {
		if !old.replaceSet[out] {
			return nil, apierr.New(409, "%s would now be replaced, which the plan did not show; plan it again", join(p.dstPath, out))
		}
	}
	listed := map[string]bool{}
	for _, d := range old.deleteList {
		listed[d] = true
	}
	for _, d := range p.deleteList {
		if !listed[d] {
			return nil, apierr.New(409, "%s would now be deleted, which the plan did not show; plan it again", d)
		}
	}
	p.ID, p.expires, p.ExpiresAt = old.ID, old.expires, old.ExpiresAt
	p.Label = old.Label
	return p, nil
}

// mover is a mover by name, nil for none.
func (s *Service) mover(name string) Mover {
	for _, m := range s.movers {
		if m.Name() == name {
			return m
		}
	}
	return nil
}
