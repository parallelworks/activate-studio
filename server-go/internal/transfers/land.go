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
	if p.direct {
		return s.verifyDirect(ctx, r, p)
	}
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
		return apierr.New(502, "%d of %s did not arrive (the first is %s); nothing was put in place", len(missing), count(len(want), "file", "files"), missing[0])
	}
	if p.req.Options.checksum() || p.Action == ActionMove {
		return s.verifySums(ctx, r, p, r.Stage, "nothing was put in place")
	}
	s.set(r, func() {
		r.event(s.now(), "%s with %s", arrivedAll(len(p.files)), plural(len(p.files), "its size", "their sizes"))
	})
	return nil
}

// verifySums compares the contents of every file at both ends: sha256, or
// a hash both ends share where one is an rclone remote (pairSums).
func (s *Service) verifySums(ctx context.Context, r *rec, p *planRec, under, after string) error {
	var srcRels, dstRels []string
	for _, f := range p.files {
		srcRels = append(srcRels, f.Rel)
		dstRels = append(dstRels, join(under, f.Out))
	}
	a, b, kind, err := pairSums(ctx, p.src.fs, p.dst.fs, srcRels, dstRels)
	if err != nil {
		return apierr.New(502, "the checksums could not be read: %s", messageOf(err))
	}
	for i, f := range p.files {
		if a[srcRels[i]] == "" || a[srcRels[i]] != b[dstRels[i]] {
			return apierr.New(502, "%s arrived with different contents (%s); %s", f.Out, kind, after)
		}
	}
	s.set(r, func() { r.event(s.now(), "%s %s", kind, matchesAll(len(p.files))) })
	return nil
}

// verifyDirect checks files a mover wrote straight to their names: each
// is there with its size, and for a move or with checksum the same
// contents. Other files in the folder are not the transfer's and are not
// looked at.
func (s *Service) verifyDirect(ctx context.Context, r *rec, p *planRec) error {
	const after = "the files already written stay at the destination"
	var finals []string
	for _, f := range p.files {
		finals = append(finals, join(p.dstPath, f.Out))
	}
	got, err := p.dst.fs.Stat(ctx, finals)
	if err != nil {
		return apierr.New(502, "what arrived could not be listed: %s", messageOf(err))
	}
	var missing []string
	for i, f := range p.files {
		fi, ok := got[finals[i]]
		if !ok {
			missing = append(missing, f.Out)
			continue
		}
		if fi.Type != 'f' || fi.Size != f.Size {
			return apierr.New(502, "%s is there with %d bytes where the source has %d; %s", f.Out, fi.Size, f.Size, after)
		}
	}
	if len(missing) > 0 {
		return apierr.New(502, "%d of %s did not arrive (the first is %s); %s", len(missing), count(len(p.files), "file", "files"), missing[0], after)
	}
	if p.req.Options.checksum() || p.Action == ActionMove {
		return s.verifySums(ctx, r, p, p.dstPath, after)
	}
	s.set(r, func() {
		r.event(s.now(), "%s with %s", arrivedAll(len(p.files)), plural(len(p.files), "its size", "their sizes"))
	})
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
		return apierr.New(502, "CONDUIT reported %s and %d arrived; nothing was put in place", count(int(reported), "file", "files"), len(got))
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
		r.event(s.now(), "%s arrived (%s); checked against CONDUIT's count only", count(len(got), "file", "files"), humanBytes(bytes))
	})
	return nil
}

// land gives each staged file its name, then deletes what a sync with
// deleteExtra listed, or the originals of a move, and tells the indexes.
func (s *Service) land(ctx context.Context, r *rec, p *planRec) error {
	// A space whose files changed builds its index again, whatever part of
	// the landing went through.
	defer s.spacesChanged(p)
	dst := p.dst.fs
	pairs := make([]LandPair, 0, len(p.files))
	for _, f := range p.files {
		pairs = append(pairs, LandPair{Staged: join(r.Stage, f.Out), Final: join(p.dstPath, f.Out), Replace: p.replaceSet[f.Out]})
	}
	var landed []string
	if p.direct {
		// Written straight to their names: nothing to land.
		for _, x := range pairs {
			landed = append(landed, x.Final)
		}
		pairs = nil
	}
	for start := 0; start < len(pairs); start += 1000 {
		done, err := dst.Land(ctx, pairs[start:min(start+1000, len(pairs))])
		landed = append(landed, done...)
		if err != nil {
			s.indexLanded(ctx, p, landed)
			return apierr.New(statusOrDefault(err), "%s. %d of %s %s in place; the rest were removed", messageOf(err), len(landed), count(len(pairs), "file", "files"), plural(len(landed), "is", "are"))
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
		r.event(s.now(), "%s in place in %s%s", count(len(landed), "file", "files"), orRoot(p.dstPath), when(replaced > 0, fmt.Sprintf(", %d replaced", replaced)))
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
			r.event(s.now(), "%s deleted at the destination", count(len(removed), "extra file", "extra files"))
		})
		if p.dst.reindexed && p.dst.libID != "" {
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
		if p.src.reindexed && p.src.libID != "" {
			s.d.Index.Removed(ctx, p.src.libID, removed, p.srcDirs)
		}
		s.set(r, func() {
			r.event(s.now(), "%s removed from %s", count(len(removed), "original", "originals"), p.src.Label)
			if p.Skipped.Hidden+p.Skipped.Links > 0 {
				r.event(s.now(), "Folders holding hidden entries or links stay at the source with those in them")
			}
		})
		if err != nil {
			return apierr.New(502, "the copy is in place, but removing the originals stopped: %s", messageOf(err))
		}
		if len(removed) != len(rels) {
			return apierr.New(502, "the copy is in place, but %d of %s could not be removed", len(rels)-len(removed), count(len(rels), "original", "originals"))
		}
	}
	if len(p.IndexNotes) > 0 {
		s.set(r, func() {
			for _, n := range p.IndexNotes {
				r.event(s.now(), "%s", n)
			}
		})
	}
	return nil
}

// spacesChanged queues the index build of each space a landing may have
// changed: the destination, and the source of a move.
func (s *Service) spacesChanged(p *planRec) {
	if s.d.SpaceChanged == nil {
		return
	}
	if p.dst.space != "" {
		s.d.SpaceChanged(p.v, p.dst.space)
	}
	if p.Action == ActionMove && p.src.space != "" && p.src.space != p.dst.space {
		s.d.SpaceChanged(p.v, p.src.space)
	}
}

func (s *Service) indexLanded(ctx context.Context, p *planRec, landed []string) {
	if len(landed) > 0 && p.dst.reindexed && p.dst.libID != "" {
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
