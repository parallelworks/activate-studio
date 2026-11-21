package libapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
	"github.com/parallelworks/activate-studio/server-go/internal/tracefile"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Bucket libraries (docs/LIBRARIES.md, Buckets): a folder of an rclone
// remote (a bucket of any S3 compatible store, AWS or on premises, or any
// other remote rclone lists) is indexed from its listing alone. One fast
// list (`rclone lsjson -R --fast-list`) gives every object's name, size
// and time; Studio writes it as a GUFI trace and builds the index with
// gufi_trace2index, beside the old one, swapped in when whole, as a
// folder's build is. No object is read to index it. The Library lists
// the bucket's folders from that index, and opening a file reads that
// object alone, its first bytes for a text preview, whole for a download
// or a document preview up to search.DownloadMax. The index holds names,
// sizes and times; the objects' text is read when asked (buckettext.go).

const singleUserBucket = "Bucket libraries use this computer's rclone configuration, so they are kept only on a Studio with sign-in off."

// bucketNamesOnly is why a bucket library's depth stays at names.
const bucketNamesOnly = "A bucket library is indexed from its listing: names, sizes and times. Its objects' text is read when Read text is on for it, in Settings, Indexing, Libraries."

// bucketTick is how often the background loop sees which listings are due.
var bucketTick = time.Minute

// bucketRoot is the top folder of a bucket library's trace, and so of its
// index tree: IndexRoot is IndexDir/files, as a copy library's is.
const bucketRoot = "files"

// listPage is how many keys an object store answers a list request with.
const listPage = 1000

// buckets is what runs for bucket libraries: the listings running, and
// the trace layout of each gufi_trace2index.
type buckets struct {
	mu      sync.Mutex
	listing map[string]bool
	layouts map[string]bool
}

func (c *buckets) setListing(id string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listing == nil {
		c.listing = map[string]bool{}
	}
	if on {
		c.listing[id] = true
	} else {
		delete(c.listing, id)
	}
}

func (c *buckets) isListing(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listing[id]
}

// layout is whether bin's gufi_trace2index reads traces without the
// device column, found once per GUFI.
func (c *buckets) layout(bin, tmp string) (bool, error) {
	c.mu.Lock()
	if v, ok := c.layouts[bin]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	noDev, err := tracefile.Layout(bin, tmp)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	if c.layouts == nil {
		c.layouts = map[string]bool{}
	}
	c.layouts[bin] = noDev
	c.mu.Unlock()
	return noDev, nil
}

// bucketWhere names a bucket library's source for messages.
func bucketWhere(b *libraries.BucketOf) string {
	where := b.Label
	if where == "" {
		where = b.Location
	}
	if b.Path != "" {
		where += ", " + b.Path
	}
	return where
}

// bucketOf is the rclone remote a bucket library lists, as the Studio's
// single owner reaches it.
func (s *Service) bucketOf(ctx context.Context, b *libraries.BucketOf) (*transfers.Bucket, error) {
	xs := transfers.Of(s.a)
	v, ok := xs.OwnerViewer()
	if !ok {
		return nil, apierr.New(403, singleUserBucket)
	}
	bk, err := xs.Bucket(ctx, v, b.Location)
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Status == 404 && strings.HasPrefix(ae.Message, "no location") {
			return nil, fmt.Errorf("its remote, %s, is no longer in Transfers", bucketWhere(b))
		}
		return nil, err
	}
	return bk, nil
}

// objectPath is a library path as the object's path in the remote.
func objectPath(b *libraries.BucketOf, rel string) (string, error) {
	sub, err := search.CleanRel(rel)
	if err != nil {
		return "", err
	}
	if sub == "" {
		return "", apierr.New(400, "name a file")
	}
	if b.Path == "" {
		return sub, nil
	}
	return path.Join(b.Path, sub), nil
}

// listRequests estimates the list requests a fast list of n objects took.
func listRequests(n int64) int64 {
	return max(1, (n+listPage-1)/listPage)
}

// bucketBuild lists a bucket library's remote and builds its index from
// the listing: the trace is written beside the index, built into
// IndexDir/.build and swapped in when GUFI answers from it, so search
// keeps answering from the last listing while a new one runs.
func (b *builds) bucketBuild(id string, own *index.OwnJob) (int64, error) {
	t0 := time.Now()
	d := b.s.Reg.AddedDef(id)
	if d == nil || d.Build == nil || d.Bucket == nil {
		return 0, errors.New("no such library")
	}
	tree, bd, bc := d.IndexRoot, d.Build, d.Bucket
	var jobID int
	progress := func(phase string, done int, current *string) {
		own.Set(func(j *index.IndexJob) {
			jobID = j.ID
			j.Progress = &index.Progress{Phase: phase, Done: done, Current: current, Failed: []string{}}
		})
	}
	own.Set(func(j *index.IndexJob) { jobID = j.ID })
	s := b.ix().Settings.Built(id)
	if index.RecoverSwap(tree, []string{filepath.Join(bd.IndexDir, ".old")}) != "" {
		b.log(fmt.Sprintf("library %s: its index was put aside by a swap a stop cut off; restored it", id))
	}
	state := "building"
	if exists(filepath.Join(tree, "db.db")) {
		state = "updating"
	}
	b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Error = state, nil })
	b.buckets.setListing(id, true)
	defer b.buckets.setListing(id, false)
	st := b.s.Reg.BucketState(id)
	var bk *transfers.Bucket
	err := func() error {
		bin := b.s.a.Cfg.GufiBin
		if !exists(filepath.Join(bin, "gufi_trace2index")) {
			return fmt.Errorf("this GUFI has no gufi_trace2index (in %s), which builds a bucket's index", bin)
		}
		if err := os.MkdirAll(bd.IndexDir, 0o755); err != nil {
			return err
		}
		noDev, err := b.buckets.layout(bin, bd.IndexDir)
		if err != nil {
			return err
		}
		bk, err = b.s.bucketOf(b.ctx, bc)
		if err != nil {
			return err
		}
		where := bucketWhere(bc)
		runs := filepath.Join(bd.IndexDir, ".trace-runs")
		index.RmTree(runs)
		w, err := tracefile.New(runs, bucketRoot, time.Now().Unix())
		if err != nil {
			return err
		}
		defer w.Cleanup()
		w.NoDev = noDev
		progress("Listing "+where, 0, nil)
		var seen int
		listed := time.Now()
		if err := bk.Walk(b.ctx, bc.Path, func(e transfers.BucketEntry) error {
			if err := w.Add(e.Path, e.Size, e.ModTime.Unix(), e.IsDir); err != nil {
				return err
			}
			if seen++; seen%1000 == 0 {
				progress("Listing "+where, seen, strp(enUS(seen)+" entries"))
			}
			return nil
		}); err != nil {
			return fmt.Errorf("listing %s: %w", where, err)
		}
		listSecs := time.Since(listed).Seconds()
		trace := filepath.Join(bd.IndexDir, ".trace")
		f, err := os.OpenFile(trace, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		stats, err := w.Write(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		defer os.Remove(trace)
		if err != nil {
			return err
		}
		w.Cleanup()
		progress("Indexing folders", 0, strp(enUS(int(stats.Dirs))+" folders"))
		stage := filepath.Join(bd.IndexDir, ".build")
		if err := index.RmTree(stage); err != nil {
			return err
		}
		if err := os.MkdirAll(stage, 0o755); err != nil {
			return err
		}
		threads := strconv.Itoa(max(1, b.ix().Settings.Threads("kb")))
		cmd := exec.CommandContext(b.ctx, filepath.Join(bin, "gufi_trace2index"), "-n", threads, trace, stage)
		cmd.Env = gufi.EnvFor(bin, os.Environ(), nil, gufi.EnvOptions{})
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("gufi_trace2index: %v: %s", err, lastLine(string(out)))
		}
		built := filepath.Join(stage, bucketRoot)
		if !exists(filepath.Join(built, "db.db")) {
			return fmt.Errorf("gufi_trace2index wrote no index at %s", built)
		}
		wk := b.work(d, jobID, s)
		if err := wk.VerifyTree(built); err != nil {
			return err
		}
		// The text earlier passes read goes into the new index before it
		// is swapped in, so search finds it through the new listing.
		if bc.Text || bc.Fsid != "" {
			progress("Adding the text read before", 0, nil)
			if _, err := b.refillShards(id, bc, built); err != nil {
				b.log(fmt.Sprintf("library %s: the text read before could not be added to its new index: %s", id, err))
			}
		}
		old := filepath.Join(bd.IndexDir, ".old")
		if err := index.RmTree(old); err != nil {
			return err
		}
		if exists(tree) {
			index.OpenDir(tree)
			if err := os.Rename(tree, old); err != nil {
				return err
			}
		}
		if err := os.Rename(built, tree); err != nil {
			return err
		}
		index.RmTree(old)
		index.RmTree(stage)
		progress("Counting", 0, nil)
		c, err := b.settle(d)
		if err != nil {
			return err
		}
		if err := index.WriteDepthRecord(b.depthFile(id), index.DepthRecordOf(s)); err != nil {
			return err
		}
		now := isoNow()
		st = libraries.BucketState{ListedAt: &now, Objects: stats.Objects, Bytes: stats.Bytes, Folders: stats.Dirs, Skipped: stats.Skipped,
			Requests: listRequests(stats.Objects), Provider: bk.Provider(b.ctx), Seconds: float64(int(listSecs*10)) / 10, TextPass: st.TextPass}
		if stats.Skipped > 0 {
			b.log(fmt.Sprintf("library %s: %d entries of %s could not be indexed (a path longer than GUFI holds, a name \".\" or \"..\", or a name listed twice)", id, stats.Skipped, where))
		}
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) {
			lb.State, lb.Error, lb.BuiltAt, lb.Files, lb.Dirs = "ready", nil, &c.builtAt, &c.files, &c.dirs
		})
		return nil
	}()
	if err != nil {
		msg := truncRunes(libraries.PlainBuildError(err.Error()), 400)
		st.Error = &msg
		if werr := b.s.Reg.WriteBucketState(id, st); werr != nil {
			b.log(fmt.Sprintf("library %s: recording its listing: %s", id, werr))
		}
		if b.stopping.Load() {
			return 0, err
		}
		state := "failed"
		if exists(filepath.Join(tree, "db.db")) {
			state = "ready"
		}
		b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State, lb.Error = state, &msg })
		return 0, err
	}
	if werr := b.s.Reg.WriteBucketState(id, st); werr != nil {
		b.log(fmt.Sprintf("library %s: recording its listing: %s", id, werr))
	}
	// The index is searchable now; the objects' text is read after it.
	if d := b.s.Reg.AddedDef(id); d != nil && d.Bucket != nil && d.Bucket.Text && !b.stopping.Load() {
		b.buckets.setListing(id, false)
		b.bucketText(id, d.Bucket, bk, tree, progress)
	}
	return time.Since(t0).Milliseconds(), nil
}

// bucketDue is whether a bucket library's next listing is due at now.
func bucketDue(bc *libraries.BucketOf, st libraries.BucketState, now time.Time) bool {
	if bc.Every <= 0 {
		return false
	}
	if st.ListedAt == nil {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, *st.ListedAt)
	return err != nil || !now.Before(at.Add(time.Duration(bc.Every)*time.Minute))
}

// startBuckets lists each bucket library again on its interval while the
// server runs.
func (b *builds) startBuckets(ctx context.Context) {
	t := time.NewTicker(bucketTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, id := range b.builtIDs() {
			d := b.s.Reg.AddedDef(id)
			if d == nil || d.Bucket == nil || d.Build == nil || d.Build.State != "ready" || b.jobOf(id) != nil {
				continue
			}
			if bucketDue(d.Bucket, b.s.Reg.BucketState(id), now) {
				if _, err := b.start(id); err != nil {
					b.log(fmt.Sprintf("library %s: listing it again: %s", id, err))
				}
			}
		}
	}
}

// ---- reading objects ----

// bucketFile is an object of a bucket library as the file view shows it:
// its size and time, and for a text file its first PreviewBytes, read as
// that range alone.
func (s *Service) bucketFile(ctx context.Context, l libraries.Library, rel string) (*kb.Obj, error) {
	key, err := objectPath(l.Bucket, rel)
	if err != nil {
		return nil, err
	}
	bk, err := s.bucketOf(ctx, l.Bucket)
	if err != nil {
		return nil, err
	}
	st, err := bk.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	sub, _ := search.CleanRel(rel)
	if st.IsDir {
		return nil, apierr.New(404, "%s is not in %s", sub, bucketWhere(l.Bucket))
	}
	o := kb.O("path", sub, "kind", kb.Classify(path.Base(sub), false), "size", st.Size, "mtime", st.ModTime.Unix())
	if !kb.TextSuffixes[strings.ToLower(path.Ext(sub))] {
		return o.Set("truncated", false).Set("content", nil).Set("source", "none"), nil
	}
	data, err := bk.Read(ctx, key, search.PreviewBytes+1)
	if err != nil {
		return nil, err
	}
	body := kb.DecodeUTF8(data)
	return o.Set("truncated", kb.UTF16Len(body) > search.PreviewBytes).Set("content", kb.SliceUTF16(body, search.PreviewBytes)).Set("source", "raw"), nil
}

// bucketDownload is an object of a bucket library, whole, up to
// search.DownloadMax: the Download button and document previews.
func (s *Service) bucketDownload(ctx context.Context, l libraries.Library, rel string) (string, []byte, error) {
	key, err := objectPath(l.Bucket, rel)
	if err != nil {
		return "", nil, err
	}
	bk, err := s.bucketOf(ctx, l.Bucket)
	if err != nil {
		return "", nil, err
	}
	st, err := bk.Stat(ctx, key)
	if err != nil {
		return "", nil, err
	}
	if st.IsDir {
		return "", nil, apierr.New(404, "%s is not in %s", rel, bucketWhere(l.Bucket))
	}
	if st.Size > search.DownloadMax {
		return "", nil, apierr.New(413, "This object is over %d MB; copy it from %s with Transfers.", search.DownloadMax/1024/1024, bucketWhere(l.Bucket))
	}
	data, err := bk.Read(ctx, key, -1)
	if err != nil {
		return "", nil, err
	}
	return path.Base(key), data, nil
}

// ---- routes ----

// bucketAdd adds a bucket library and starts its first listing.
func (s *Service) bucketAdd(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBucket)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	if !exists(filepath.Join(s.a.Cfg.GufiBin, "gufi_trace2index")) {
		return apierr.New(400, "GUFI here has no gufi_trace2index (in %s), which builds a bucket's index.", s.a.Cfg.GufiBin)
	}
	locID := jsTrim(body.Str("location"))
	if kind, _, _ := strings.Cut(locID, ":"); kind != transfers.KindRclone {
		return apierr.New(400, "location: an rclone remote from Transfers")
	}
	p, err := transfers.CleanRel(body.Str("path"))
	if err != nil {
		return err
	}
	every := libraries.DefaultBucketEvery
	if body.Has("every") {
		n, ok := body.Get("every").(float64)
		if !ok || !slices.Contains(libraries.BucketEvery, int(n)) || float64(int(n)) != n {
			return apierr.New(400, "every: %s minutes", joinInts(libraries.BucketEvery))
		}
		every = int(n)
	}
	xs := transfers.Of(s.a)
	v, ok := xs.OwnerViewer()
	if !ok {
		return apierr.New(403, singleUserBucket)
	}
	bk, err := xs.Bucket(r.Context(), v, locID)
	if err != nil {
		return err
	}
	// The folder must be there and listable before anything is kept: one
	// request for its first page.
	if _, err := xs.Browse(r.Context(), v, locID, &p); err != nil {
		return err
	}
	label := jsTrim(body.Str("label"))
	if label == "" {
		label = bk.Label
		if p != "" {
			label = path.Base(p)
		}
	}
	id := strings.ToLower(jsTrim(body.Str("id")))
	if id == "" {
		id = s.Reg.IDFor(label, bk.Label+"/"+p)
	}
	if err := s.Reg.CheckNewID(id); err != nil {
		return err
	}
	own := s.Reg.BuiltLibraryDir(id)
	indexDir := filepath.Join(own, "index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return err
	}
	if _, err := s.Reg.AddBuilt(libraries.Def{
		ID: id, Label: label, IndexRoot: filepath.Join(indexDir, bucketRoot),
		Build:  &libraries.LocalBuild{IndexDir: indexDir, CacheRoot: filepath.Join(own, "extract"), Managed: true, State: "building"},
		Bucket: &libraries.BucketOf{Location: locID, Path: p, Label: bk.Label, Every: every},
	}); err != nil {
		return err
	}
	if _, err := s.ix().Settings.Save(id, map[string]any{"depth": string(index.DepthNames)}); err != nil {
		return err
	}
	job, err := s.builds.start(id)
	if err != nil {
		return err
	}
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, libraryAndJob{lib, job})
	return nil
}

// bucketSettings changes how often Studio lists a bucket library again,
// and whether it reads the objects' text. Turning text on lists the
// bucket and reads it now; turning it off stops a pass that is running,
// and the text read so far stays searchable.
func (s *Service) bucketSettings(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBucket)
	}
	id := r.PathValue("id")
	d := s.Reg.AddedDef(id)
	if d == nil || d.Bucket == nil {
		return apierr.New(404, "%s is not a bucket library", id)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	if !body.Has("every") && !body.Has("text") {
		return apierr.New(400, "every or text")
	}
	if body.Has("every") {
		n, ok := body.Get("every").(float64)
		if !ok || !slices.Contains(libraries.BucketEvery, int(n)) || float64(int(n)) != n {
			return apierr.New(400, "every: %s minutes", joinInts(libraries.BucketEvery))
		}
		s.Reg.SetBucketEvery(id, int(n))
	}
	if body.Has("text") {
		on, ok := body.Get("text").(bool)
		if !ok {
			return apierr.New(400, "text: true or false")
		}
		if on && !d.Bucket.Text {
			if _, err := indexExtractBin(); err != nil {
				return apierr.New(409, "%s", err.Error())
			}
		}
		s.Reg.SetBucketText(id, on)
		if !on {
			s.builds.texts.stop(id)
		} else if !d.Bucket.Text {
			if _, err := s.builds.start(id); err != nil {
				return err
			}
		}
	}
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, struct {
		Library libraries.SettingsLibrary `json:"library"`
	}{lib})
	return nil
}
