package libapi

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/extractfiles"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Reading text from a bucket (docs/LIBRARIES.md): with BucketOf.Text on,
// each listing of a bucket library is followed by a text pass. The objects
// whose text can be read and that no pass has read at this mtime are
// written to a file list and read by index-extract, a program of its own
// beside studio-server, one request for each object through the remote
// (`index-extract files --rclone`). Its artifact, text.db in the
// library's folder, keeps every object's outcome, so a pass goes on from
// the last and an object is read again only when it changed. What a pass
// added goes into the library's spread tree, keyed by inode (stable for an
// object: a hash of its path), and from there into the index's permission
// shards beside each db.db, which search reads. A new listing's index gets
// its shards from the spread tree before it is swapped in, so the text
// stays searchable through every listing with nothing read again.

// bucketTextJobs is how many objects a pass reads at once: the work is
// mostly waiting on the store.
const bucketTextJobs = 8

// bucketText is the text passes running, each with its cancel.
type bucketTexts struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func (t *bucketTexts) start(ctx context.Context, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	if t.running == nil {
		t.running = map[string]context.CancelFunc{}
	}
	t.running[id] = cancel
	t.mu.Unlock()
	return ctx, func() {
		t.mu.Lock()
		delete(t.running, id)
		t.mu.Unlock()
		cancel()
	}
}

func (t *bucketTexts) stop(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.running[id]; c != nil {
		c()
		return true
	}
	return false
}

func (t *bucketTexts) isReading(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running[id] != nil
}

// bucketReadable is whether a pass reads an object of this name: text and
// documents. Images are left to a later choice (OCR reads each one whole).
func bucketReadable(name string) bool {
	s := extract.Suffix(name)
	return !extract.LockFile(name) && (extract.TextSuffixes[s] || extract.DownmarkSuffixes[s] || extract.NativeSuffixes[s])
}

var fsidUnsafe = regexp.MustCompile(`[^a-z0-9.-]+`)

// BucketFsid is the name a bucket's records go under in a spread tree:
// the store (its endpoint's host), the bucket and the library's prefix,
// as one folder name. Inodes are hashes of paths below the library's
// top, so the name says which top; it never uses the remote's name, which
// each computer chooses for itself.
func BucketFsid(endpoint, p string) string {
	whole := strings.ToLower(strings.Trim(endpoint+"/"+strings.Trim(p, "/"), "/"))
	name := strings.Trim(fsidUnsafe.ReplaceAllString(whole, "_"), "_")
	if name == "" || name == "." || name == ".." {
		name = "bucket"
	}
	const most = 80
	if len(name) > most {
		sum := sha1.Sum([]byte(whole))
		name = name[:most-9] + "-" + fmt.Sprintf("%x", sum[:4])
	}
	return name
}

// bucketTextPaths are the files a bucket library's text passes keep.
func (s *Service) bucketTextPaths(id string) (artifact, spreadTop string) {
	own := s.Reg.BuiltLibraryDir(id)
	return filepath.Join(own, "text.db"), filepath.Join(own, "spread")
}

// bucketSpread is a bucket library's spread tree, naming its fsid when
// it has none yet.
func (b *builds) bucketSpread(id string, bc *libraries.BucketOf, bk *transfers.Bucket) extdb.Spread {
	_, top := b.s.bucketTextPaths(id)
	fsid := bc.Fsid
	if fsid == "" {
		fsid = BucketFsid(bk.Endpoint(b.ctx), bc.Path)
		b.s.Reg.ModifyAdded(id, func(d *libraries.Def) {
			if d.Bucket != nil && d.Bucket.Fsid == "" {
				c := *d.Bucket
				c.Fsid = fsid
				d.Bucket = &c
			}
		})
	}
	return extdb.Spread{Top: top, Prefix: extdb.Prefix, Fsid: fsid}
}

// refillShards gives an index tree the shards of the text the spread
// tree holds for its objects; nothing when no pass has run.
func (b *builds) refillShards(id string, bc *libraries.BucketOf, tree string) (extdb.ShardStats, error) {
	_, top := b.s.bucketTextPaths(id)
	if bc.Fsid == "" || !exists(top) {
		return extdb.ShardStats{}, nil
	}
	spread := extdb.Spread{Top: top, Prefix: extdb.Prefix, Fsid: bc.Fsid}
	b.migrateText(id, spread)
	return extdb.FillShardsWith(spread, tree, bc.Fsid, b.shardOptions())
}

// shardOptions give shards ext_vectors where gufi_sqlite3 is here.
func (b *builds) shardOptions() extdb.ShardOptions {
	bin := filepath.Join(b.s.a.Cfg.GufiBin, "gufi_sqlite3")
	if !exists(bin) {
		return extdb.ShardOptions{Ctx: b.ctx}
	}
	return extdb.ShardOptions{GufiSqlite3: bin, Ctx: b.ctx}
}

// embedder makes the vectors of a bucket's text with the Studio's GGUF
// model, where it and gufi_sqlite3 are here; nil otherwise.
func (b *builds) embedder() *extdb.Embedder {
	bin, model := filepath.Join(b.s.a.Cfg.GufiBin, "gufi_sqlite3"), b.s.embedModel()
	if !exists(bin) || !exists(model) {
		return nil
	}
	return &extdb.Embedder{GufiSqlite3: bin, Model: model, ModelName: extdb.DefaultModelName, Dims: 384, MaxChars: 800}
}

// migrateText moves a bucket library's text from the layout an earlier
// Studio wrote: the spread tree under studio/ goes to ext/, and records
// with an empty extra_info take the bucket's fsid, so nothing is read
// again. The shards are rewritten whole at the next refill, and the
// earlier studio_* shards are removed then.
func (b *builds) migrateText(id string, spread extdb.Spread) {
	moved, err := extdb.MigrateSpread(spread.Top)
	if err != nil {
		b.log(fmt.Sprintf("library %s: moving its text to the ext prefix: %s", id, err))
	} else if moved {
		b.log(fmt.Sprintf("library %s: its text moved to the ext prefix", id))
	}
	artifact, _ := b.s.bucketTextPaths(id)
	n := 0
	if exists(artifact) {
		if n, err = extdb.Rekey(artifact, "", spread.Fsid); err != nil {
			b.log(fmt.Sprintf("library %s: giving its text the fsid: %s", id, err))
		} else if n > 0 {
			b.log(fmt.Sprintf("library %s: %d objects' text given the fsid %s", id, n, spread.Fsid))
		}
	}
	// The spread tree holds old keys only where the artifact did, or
	// where it was just moved: every db is looked at only then.
	if n > 0 || moved {
		if _, err := extdb.RekeySpread(spread, "", spread.Fsid); err != nil {
			b.log(fmt.Sprintf("library %s: giving its spread tree the fsid: %s", id, err))
		}
	}
}

// textObjects calls fn for each object of a bucket library's index tree a
// pass reads, with its path below the library's top.
func textObjects(tree string, fn func(rel string, f extdb.IndexEntry) error) error {
	return extdb.WalkIndex(tree, func(dir string, files []extdb.IndexEntry) error {
		for _, f := range files {
			if !bucketReadable(f.Name) {
				continue
			}
			if err := fn(path.Join(dir, f.Name), f); err != nil {
				return err
			}
		}
		return nil
	})
}

// artifactStates is what text.db records of each object version; none
// before the first pass.
func artifactStates(artifact string) (map[extdb.Key]string, error) {
	if !exists(artifact) {
		return map[extdb.Key]string{}, nil
	}
	a, err := extdb.Create(artifact)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	return a.States()
}

// indexExtractBin is the index-extract program: STUDIO_INDEX_EXTRACT_BIN,
// or the one beside this server.
func indexExtractBin() (string, error) {
	if v := os.Getenv("STUDIO_INDEX_EXTRACT_BIN"); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "index-extract"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if !exists(p) {
		return "", fmt.Errorf("index-extract, which reads the objects' text, is not beside %s", filepath.Base(exe))
	}
	return p, nil
}

// bucketText runs a text pass of a bucket library over its index tree.
func (b *builds) bucketText(id string, bc *libraries.BucketOf, bk *transfers.Bucket, tree string, progress func(phase string, done int, current *string)) {
	ctx, done := b.texts.start(b.ctx, id)
	defer done()
	t0 := time.Now()
	since := t0.Unix() - 1
	st := b.s.Reg.BucketState(id)
	text := &libraries.BucketText{Read: map[string]int64{}, Missing: []string{}}
	record := func(err error) {
		text.At = isoNow()
		text.Seconds = float64(int(time.Since(t0).Seconds()*10)) / 10
		if text.Bytes >= 1<<20 && text.Seconds > 0 {
			text.Rate = float64(int64(float64(text.Bytes) / text.Seconds))
		}
		if err != nil {
			msg := truncRunes(err.Error(), 400)
			text.Error = &msg
			b.log(fmt.Sprintf("library %s: reading its text: %s", id, msg))
		}
		st := b.s.Reg.BucketState(id)
		st.TextPass = text
		if werr := b.s.Reg.WriteBucketState(id, st); werr != nil {
			b.log(fmt.Sprintf("library %s: recording its text pass: %s", id, werr))
		}
	}
	if st.TextPass != nil {
		text.Files, text.Rate = st.TextPass.Files, st.TextPass.Rate
	}
	artifact, _ := b.s.bucketTextPaths(id)
	spread := b.bucketSpread(id, bc, bk)
	// extra_info is the bucket's fsid, in every record.
	extra := spread.Fsid
	b.migrateText(id, spread)
	progress("Choosing objects to read", 0, nil)
	states, err := artifactStates(artifact)
	if err != nil {
		record(err)
		return
	}
	list := artifact + ".list"
	f, err := os.Create(list)
	if err != nil {
		record(err)
		return
	}
	defer os.Remove(list)
	w := bufio.NewWriter(f)
	total := 0
	err = textObjects(tree, func(rel string, e extdb.IndexEntry) error {
		if _, ok := states[extdb.Key{Inode: e.Inode, Mtime: e.Mtime, ExtraInfo: extra}]; ok || e.Size <= 0 {
			return nil
		}
		total++
		_, err := fmt.Fprintf(w, "%d\x1e%d\x1e%d\x1e%s\n", e.Inode, e.Mtime, e.Size, rel)
		return err
	})
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		record(err)
		return
	}
	if total > 0 {
		sum, err := b.runIndexExtract(ctx, id, bc, bk, list, artifact, extra, total, progress)
		text.Read, text.Bytes, text.Missing, text.Stopped = sum.States, sum.Bytes, sum.Missing, sum.Stopped
		for k, n := range sum.States {
			if k != extdb.StateSkipped {
				text.Requests += n
			}
		}
		if text.Read == nil {
			text.Read = map[string]int64{}
		}
		if text.Missing == nil {
			text.Missing = []string{}
		}
		if err != nil {
			record(err)
			return
		}
		// Each new chunk's vector, for search by meaning, where the
		// model is here; text alone otherwise.
		if e := b.embedder(); e != nil && ctx.Err() == nil {
			progress("Computing the meaning of the text", 0, nil)
			if _, err := e.EmbedArtifact(ctx, artifact); err != nil {
				b.log(fmt.Sprintf("library %s: vectors of its text: %s", id, err))
			}
		}
		progress("Adding the text to the index", 0, nil)
		if _, err := extdb.LoadSpreadSince(spread, artifact, since); err != nil {
			record(err)
			return
		}
	}
	sh, err := extdb.FillShardsWith(spread, tree, extra, b.shardOptions())
	if err != nil {
		record(err)
		return
	}
	if sh.VectorError != "" {
		b.log(fmt.Sprintf("library %s: its shards keep text without vectors: %s", id, sh.VectorError))
	}
	text.Files = int64(sh.Files)
	// Search sees the new shards, and the library's capabilities say so.
	b.s.Reg.StaleProbe(tree)
	b.s.Reg.Probe(b.ctx, tree)
	search.InvalidateDbList()
	if sh.Unchowned > 0 {
		b.log(fmt.Sprintf("library %s: %d text shards are owned by this account", id, sh.Unchowned))
	}
	record(nil)
}

// runIndexExtract reads the listed objects into the artifact with
// index-extract, its @progress lines moving the job's progress.
func (b *builds) runIndexExtract(ctx context.Context, id string, bc *libraries.BucketOf, bk *transfers.Bucket, list, artifact, extra string, total int, progress func(phase string, done int, current *string)) (extractfiles.Summary, error) {
	var sum extractfiles.Summary
	bin, err := indexExtractBin()
	if err != nil {
		return sum, err
	}
	args := []string{"files", "--list", list, "--out", artifact, "--extra-info", extra, "--rclone", bk.Spec(bc.Path), "--rclone-bin", bk.Bin(),
		"--max-size", strconv.Itoa(libraries.BucketTextMax), "--jobs", strconv.Itoa(bucketTextJobs), "--no-ocr", "--progress"}
	if s := b.ix().Settings.Built(id); s.LegacyOffice {
		if so := b.ix().Hooks.SofficeFound(); so != "" {
			args = append(args, "--soffice", so)
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	// Stopped, it is asked to end, so a commit is not cut off part way.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 30 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return sum, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return sum, err
	}
	if err := cmd.Start(); err != nil {
		return sum, err
	}
	var errLines []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		logged := 0
		for sc.Scan() {
			line := sc.Text()
			if errLines = append(errLines, line); len(errLines) > 5 {
				errLines = errLines[1:]
			}
			if logged++; logged <= 50 {
				b.log(fmt.Sprintf("library %s: %s", id, line))
			}
		}
	}()
	progress("Reading text", 0, strp(fmt.Sprintf("0 of %s objects", enUS(total))))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var last string
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "@progress "); ok {
			var p struct {
				Done, Total int
				Bytes       int64
			}
			if json.Unmarshal([]byte(rest), &p) == nil {
				progress("Reading text", p.Done, strp(fmt.Sprintf("%s of %s objects, %s read", enUS(p.Done), enUS(total), bytesWords(p.Bytes))))
			}
			continue
		}
		last = line
	}
	io.Copy(io.Discard, stdout)
	wg.Wait()
	werr := cmd.Wait()
	if last != "" {
		json.Unmarshal([]byte(last), &sum)
	}
	if ctx.Err() != nil {
		sum.Stopped = true
		return sum, nil
	}
	if werr != nil {
		msg := strings.Join(errLines, "; ")
		if msg == "" {
			msg = werr.Error()
		}
		return sum, errors.New("index-extract: " + msg)
	}
	return sum, nil
}

// bytesWords is a size in words, as the progress line shows it.
func bytesWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// ---- routes ----

// textEstimate is what a text pass of a bucket library would read.
type textEstimate struct {
	// Objects and Bytes are what a pass would read now; Done and DoneBytes
	// what earlier passes read already; Over and OverBytes those larger
	// than a pass reads; Other the objects no reader reads.
	Objects   int64 `json:"objects"`
	Bytes     int64 `json:"bytes"`
	Done      int64 `json:"done"`
	DoneBytes int64 `json:"doneBytes"`
	Over      int64 `json:"over"`
	OverBytes int64 `json:"overBytes"`
	Other     int64 `json:"other"`
	MaxBytes  int64 `json:"maxBytes"`
	// Provider is the s3 provider the remote names, as for a listing.
	Provider string `json:"provider"`
	// Rate is the bytes a second the last pass read, 0 before one.
	Rate float64 `json:"rate"`
}

// bucketTextEstimate says what turning a bucket library's text on would
// read: GET /api/libraries/{id}/bucket/text.
func (s *Service) bucketTextEstimate(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBucket)
	}
	id := r.PathValue("id")
	d := s.Reg.AddedDef(id)
	if d == nil || d.Bucket == nil {
		return apierr.New(404, "%s is not a bucket library", id)
	}
	if !exists(filepath.Join(d.IndexRoot, "db.db")) {
		return apierr.New(409, "%s has no index yet; its first listing makes one", d.Label)
	}
	artifact, _ := s.bucketTextPaths(id)
	states, err := artifactStates(artifact)
	if err != nil {
		return err
	}
	// Read before is by inode and mtime, whatever the extra_info: an
	// artifact not yet given the bucket's fsid counts the same.
	read := map[[2]int64]bool{}
	for k := range states {
		read[[2]int64{k.Inode, k.Mtime}] = true
	}
	e := textEstimate{MaxBytes: libraries.BucketTextMax}
	if err := extdb.WalkIndex(d.IndexRoot, func(_ string, files []extdb.IndexEntry) error {
		for _, f := range files {
			switch {
			case !bucketReadable(f.Name):
				e.Other++
			case f.Size > libraries.BucketTextMax:
				e.Over++
				e.OverBytes += f.Size
			case f.Size <= 0:
			default:
				if read[[2]int64{f.Inode, f.Mtime}] {
					e.Done++
					e.DoneBytes += f.Size
				} else {
					e.Objects++
					e.Bytes += f.Size
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	st := s.Reg.BucketState(id)
	e.Provider = st.Provider
	if t := st.TextPass; t != nil {
		e.Rate = t.Rate
	}
	apierr.JSON(w, 200, e)
	return nil
}

// bucketTextStop stops a running text pass; the next goes on from where
// it stopped: POST /api/libraries/{id}/bucket/text/stop.
func (s *Service) bucketTextStop(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserBucket)
	}
	apierr.JSON(w, 200, map[string]bool{"stopped": s.builds.texts.stop(r.PathValue("id"))})
	return nil
}
