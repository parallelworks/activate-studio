package transfers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// verify walks the staging folder and compares it with what the plan
// listed at the source: every file there, none more, each its size, and
// for a move or with checksum the same sha256 at both ends. A mover's
// report that it is done counts for nothing until this passes.
func (s *Service) verify(ctx context.Context, r *rec, p *planRec) error {
	dst := p.dst.fs
	w, err := dst.Walk(ctx, []string{r.Stage}, 0)
	if err != nil {
		if statusOf(err) == 404 && len(p.files) == 0 && !p.bare {
			return nil
		}
		return apierr.New(502, "the staging folder could not be read: %s", messageOf(err))
	}
	if w.Skipped.Hidden > 0 || w.Skipped.Links > 0 {
		return apierr.New(502, "the staging folder holds hidden entries or links that were not sent; nothing was put in place")
	}
	got := map[string]FileInfo{}
	for _, f := range w.Files {
		got[strings.TrimPrefix(strings.TrimPrefix(f.Rel, r.Stage), "/")] = f
	}
	if p.bare {
		return s.verifyBare(r, p, got)
	}
	want := map[string]item{}
	for _, f := range p.files {
		want[f.Out] = f
	}
	for out, f := range got {
		x, ok := want[out]
		if !ok {
			return apierr.New(502, "%s arrived but was not sent; nothing was put in place", out)
		}
		if f.Size != x.Size {
			return apierr.New(502, "%s arrived with %d bytes where the source has %d; nothing was put in place", out, f.Size, x.Size)
		}
	}
	var missing []string
	for out := range want {
		if _, ok := got[out]; !ok {
			missing = append(missing, out)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return apierr.New(502, "%d of %d files did not arrive (the first is %s); nothing was put in place", len(missing), len(want), missing[0])
	}
	if p.req.Options.checksum() || p.Action == ActionMove {
		var srcRels, dstRels []string
		for _, f := range p.files {
			srcRels = append(srcRels, f.Rel)
			dstRels = append(dstRels, join(r.Stage, f.Out))
		}
		a, err := p.src.fs.Sha256(ctx, srcRels)
		if err != nil {
			return apierr.New(502, "the source's checksums could not be read: %s", messageOf(err))
		}
		b, err := dst.Sha256(ctx, dstRels)
		if err != nil {
			return apierr.New(502, "the checksums of what arrived could not be read: %s", messageOf(err))
		}
		for i, f := range p.files {
			if a[srcRels[i]] == "" || a[srcRels[i]] != b[dstRels[i]] {
				return apierr.New(502, "%s arrived with different contents (sha256); nothing was put in place", f.Out)
			}
		}
		s.set(r, func() { r.event(s.now(), "sha256 matches for all %d files", len(p.files)) })
	} else {
		s.set(r, func() { r.event(s.now(), "All %d files arrived with their sizes", len(p.files)) })
	}
	return nil
}

// verifyBare checks a copy from typed CONDUIT paths, which Studio could
// not list: something arrived, no file kept a temporary name, and the
// count matches CONDUIT's own where it gave one. What arrived is then what
// lands.
func (s *Service) verifyBare(r *rec, p *planRec, got map[string]FileInfo) error {
	if len(got) == 0 {
		return apierr.New(502, "CONDUIT reported the transfer done, but nothing arrived")
	}
	s.store.mu.Lock()
	reported := r.Files.Done
	s.store.mu.Unlock()
	if reported > 0 && reported != int64(len(got)) {
		return apierr.New(502, "CONDUIT reported %d files and %d arrived; nothing was put in place", reported, len(got))
	}
	p.files = nil
	var bytes int64
	for out, f := range got {
		if conduitTempName.MatchString(out) {
			return apierr.New(502, "%s arrived under a temporary name; nothing was put in place", out)
		}
		p.files = append(p.files, item{Rel: out, Out: out, Size: f.Size, Mtime: f.Mtime})
		bytes += f.Size
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].Out < p.files[j].Out })
	// Names already at the destination were not listed, so a landing over
	// one fails rather than replaces.
	s.set(r, func() {
		r.Files = Count{int64(len(got)), int64(len(got))}
		r.Bytes = Count{bytes, bytes}
		r.event(s.now(), "%d files arrived (%s); checked against CONDUIT's count only", len(got), humanBytes(bytes))
	})
	return nil
}

// land gives each staged file its name, then deletes what a sync with
// deleteExtra listed, or the originals of a move, and tells the indexes.
func (s *Service) land(ctx context.Context, r *rec, p *planRec) error {
	dst := p.dst.fs
	pairs := make([]LandPair, 0, len(p.files))
	for _, f := range p.files {
		pairs = append(pairs, LandPair{Staged: join(r.Stage, f.Out), Final: join(p.dstPath, f.Out), Replace: p.replaceSet[f.Out]})
	}
	var landed []string
	for start := 0; start < len(pairs); start += 1000 {
		done, err := dst.Land(ctx, pairs[start:min(start+1000, len(pairs))])
		landed = append(landed, done...)
		if err != nil {
			s.indexLanded(ctx, p, landed)
			return apierr.New(statusOrDefault(err), "%s. %d of %d files are in place; the rest were removed", messageOf(err), len(landed), len(pairs))
		}
	}
	replaced := 0
	for _, f := range p.files {
		if p.replaceSet[f.Out] {
			replaced++
		}
	}
	s.set(r, func() {
		r.Replaced = replaced
		r.Files.Done, r.Bytes.Done = int64(len(landed)), max(r.Bytes.Total, r.Bytes.Done)
		r.event(s.now(), "%d files in place in %s%s", len(landed), orRoot(p.dstPath), when(replaced > 0, fmt.Sprintf(", %d replaced", replaced)))
	})
	s.indexLanded(ctx, p, landed)

	if p.Action == ActionSync && len(p.deleteList) > 0 {
		// Only what the plan listed, and only what is still extra now.
		w, err := dst.Walk(ctx, []string{p.dstPath}, 0)
		if err != nil {
			return apierr.New(502, "the files are in place, but the destination could not be listed again to delete extras: %s", messageOf(err))
		}
		extra := map[string]bool{}
		for _, f := range w.Files {
			if !p.sourceOuts[strings.TrimPrefix(strings.TrimPrefix(f.Rel, p.dstPath), "/")] {
				extra[f.Rel] = true
			}
		}
		var del []string
		for _, d := range p.deleteList {
			if extra[d] {
				del = append(del, d)
			}
		}
		removed, err := dst.Remove(ctx, del, p.deleteDirs)
		s.set(r, func() {
			r.Deleted = len(removed)
			r.event(s.now(), "%d extra files deleted at the destination", len(removed))
		})
		if p.dst.Indexed && p.dst.libID != "" {
			s.d.Index.Removed(ctx, p.dst.libID, removed, p.deleteDirs)
		}
		if err != nil {
			return apierr.New(502, "the files are in place, but deleting extras stopped: %s", messageOf(err))
		}
	}
	if p.Action == ActionMove {
		var rels []string
		for _, f := range p.files {
			rels = append(rels, f.Rel)
		}
		removed, err := p.src.fs.Remove(ctx, rels, p.srcDirs)
		if p.src.Indexed && p.src.libID != "" {
			s.d.Index.Removed(ctx, p.src.libID, removed, p.srcDirs)
		}
		s.set(r, func() {
			r.event(s.now(), "%d originals removed from %s", len(removed), p.src.Label)
			if p.Skipped.Hidden+p.Skipped.Links > 0 {
				r.event(s.now(), "Folders holding hidden entries or links stay at the source with those in them")
			}
		})
		if err != nil {
			return apierr.New(502, "the copy is in place, but removing the originals stopped: %s", messageOf(err))
		}
		if len(removed) != len(rels) {
			return apierr.New(502, "the copy is in place, but %d of %d originals could not be removed", len(rels)-len(removed), len(rels))
		}
	}
	return nil
}

func (s *Service) indexLanded(ctx context.Context, p *planRec, landed []string) {
	if len(landed) > 0 && p.dst.Indexed && p.dst.libID != "" {
		s.d.Index.Landed(ctx, p.dst.libID, landed)
	}
}

func statusOrDefault(err error) int {
	if st := statusOf(err); st != 0 {
		return st
	}
	return 502
}

func when(b bool, s string) string {
	if b {
		return s
	}
	return ""
}
