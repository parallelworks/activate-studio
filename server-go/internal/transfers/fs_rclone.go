package transfers

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// RcloneFS is an rclone remote as a location: every call is an rclone
// subcommand, run here with each name as its own argument (never through
// a shell) and "--" before the remote paths. Every path is relative to
// the remote's root, already checked by CleanRel, so nothing names a place
// above it; on rclone's local backend links are not followed (rclone skips
// them unless told to), so a link cannot lead outside either.
//
// Listings take --no-mimetype and --use-server-modtime, so an object store
// answers from its listing alone, with no request per object. With
// --use-server-modtime an object's time is when it was uploaded, not the
// file's own modification time; a sync therefore compares by size and a
// hash both ends share where there is one (plan.go, syncRule).
//
// Object stores (S3, GCS, Azure Blob and the like) have no folders: a
// folder is the common start of object names. There Mkdir makes nothing
// (rclone makes a bucket at the top), an empty folder does not exist, and
// a folder disappears when its last object goes, so Remove has no folders
// to remove. A path that holds no objects lists as empty.
type RcloneFS struct {
	Bin    string
	Remote string // the remote's name, without the colon
	Type   string // its backend type, for messages
	// Base is a folder inside the remote the location starts at; "" for
	// the remote's top.
	Base     string
	features func(ctx context.Context) rcloneFeatures
}

func (r *RcloneFS) Where() string { return "the rclone remote " + r.Remote }
func (r *RcloneFS) Root() string  { return r.Remote + ":" + r.Base }

// spec is rel on the remote as rclone names it: "remote:path".
func (r *RcloneFS) spec(rel string) string { return r.Remote + ":" + join(r.Base, rel) }

func (r *RcloneFS) feat(ctx context.Context) rcloneFeatures {
	if r.features == nil {
		return rcloneFeatures{}
	}
	return r.features(ctx)
}

// directWrite: the remote has no server-side rename, so giving a staged
// file its name would copy every byte again (object stores).
func (r *RcloneFS) directWrite(ctx context.Context) bool { return !r.feat(ctx).Move }

// run runs an rclone subcommand; errors name the remote, 404 for rclone's
// "not found" statuses.
func (r *RcloneFS) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	out, err := runRclone(ctx, r.Bin, input, args...)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		status := 502
		if notFound(err) {
			status = 404
		}
		return out, apierr.New(status, "%s: %s", r.Remote, messageOf(err))
	}
	return out, nil
}

// lsEntry is one entry of rclone lsjson.
type lsEntry struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
	Hashes  map[string]string
}

func (e lsEntry) info(rel string) FileInfo {
	fi := FileInfo{Rel: rel, Type: 'f', Size: e.Size, Mtime: e.ModTime.Unix(), Mode: 0o644}
	if e.IsDir {
		fi.Type, fi.Size, fi.Mode = 'd', 0, 0o755
	}
	return fi
}

// lsFlags are the flags every listing takes, so an object store answers
// from its listing alone.
var lsFlags = []string{"--no-mimetype", "--use-server-modtime"}

func lsArgs(extra ...string) []string {
	return append(append([]string{"lsjson"}, lsFlags...), extra...)
}

// lsjson lists one folder (or, with -R in extra, everything under it).
func (r *RcloneFS) lsjson(ctx context.Context, rel string, extra ...string) ([]lsEntry, error) {
	args := append(lsArgs(extra...), "--", r.spec(rel))
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var es []lsEntry
	if err := json.Unmarshal(out, &es); err != nil {
		return nil, apierr.New(502, "%s: rclone's listing could not be read", r.Remote)
	}
	return es, nil
}

func (r *RcloneFS) List(ctx context.Context, rel string, max int) ([]Entry, bool, error) {
	if rel != "" {
		st, err := r.Stat(ctx, []string{rel})
		if err != nil {
			return nil, false, err
		}
		fi, ok := st[rel]
		if !ok {
			// An object store has no folders: a path that holds no objects
			// lists as empty (that is how a new folder looks there).
			if !r.feat(ctx).EmptyDir {
				return []Entry{}, false, nil
			}
			return nil, false, apierr.New(404, "%s: no such folder", rel)
		}
		if fi.Type != 'd' {
			return nil, false, apierr.New(400, "%s is not a folder", rel)
		}
	}
	es, err := r.lsjson(ctx, rel)
	if err != nil {
		if statusOf(err) == 404 && rel == "" {
			return []Entry{}, false, nil
		}
		return nil, false, err
	}
	var out []Entry
	for _, e := range es {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		out = append(out, entryOf(e.info(""), e.Name))
	}
	sorted, more := sortEntries(out, max)
	return sorted, more, nil
}

// statOneByOne is the most paths Stat asks rclone for one by one; above
// it, it lists the folders that hold them.
const statOneByOne = 16

// Stat asks `lsjson --stat` for each of a few paths on a file system. An
// object store answers --stat for a path that holds nothing with a folder
// and status 0, so there, and for many paths, Stat lists the folders that
// hold them: a folder is listed only while objects are under it.
func (r *RcloneFS) Stat(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	if len(rels) > statOneByOne || !r.feat(ctx).EmptyDir {
		return r.statByFolder(ctx, rels)
	}
	return r.statEach(ctx, rels)
}

// statEach asks `lsjson --stat` for each path, eight at once.
func (r *RcloneFS) statEach(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	out := map[string]FileInfo{}
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, rel := range rels {
		if rel == "" {
			mu.Lock()
			out[""] = FileInfo{Type: 'd', Mode: 0o755}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			o, err := r.run(ctx, nil, append(lsArgs("--stat"), "--", r.spec(rel))...)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if statusOf(err) != 404 && first == nil {
					first = err
				}
				return
			}
			var e lsEntry
			if json.Unmarshal(o, &e) == nil {
				out[rel] = e.info(rel)
			}
		}()
	}
	wg.Wait()
	return out, first
}

// statByFolder stats many paths by listing each folder that holds them
// once.
func (r *RcloneFS) statByFolder(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	out := map[string]FileInfo{}
	by := map[string][]string{}
	for _, rel := range rels {
		if rel == "" {
			out[""] = FileInfo{Type: 'd', Mode: 0o755}
			continue
		}
		by[parentOf(rel)] = append(by[parentOf(rel)], rel)
	}
	for dir, want := range by {
		es, err := r.lsjson(ctx, dir)
		if err != nil {
			if statusOf(err) == 404 {
				continue
			}
			// The folder above cannot be listed (an account that may read a
			// bucket but not list the buckets): ask for each path itself.
			got, err := r.statEach(ctx, want)
			if err != nil {
				return out, err
			}
			for k, v := range got {
				out[k] = v
			}
			continue
		}
		names := map[string]lsEntry{}
		for _, e := range es {
			names[e.Name] = e
		}
		for _, rel := range want {
			if e, ok := names[baseName(rel)]; ok {
				out[rel] = e.info(rel)
			}
		}
	}
	return out, nil
}

func (r *RcloneFS) Walk(ctx context.Context, rels []string, limit int) (Walked, error) {
	st, err := r.Stat(ctx, nonEmpty(rels))
	if err != nil {
		return Walked{}, err
	}
	var raw []FileInfo
	files := 0
	for _, rel := range rels {
		fi, ok := st[rel]
		if rel == "" {
			fi, ok = FileInfo{Type: 'd'}, true
		}
		if !ok {
			return Walked{}, apierr.New(404, "%s: no such file or folder", rel)
		}
		if fi.Type != 'd' {
			raw = append(raw, fi)
			files++
			continue
		}
		if rel != "" {
			raw = append(raw, fi)
		}
		err := r.walkDir(ctx, rel, func(e lsEntry) error {
			fi := e.info(join(rel, e.Path))
			raw = append(raw, fi)
			if fi.Type == 'f' && !hiddenSeg(e.Path) {
				files++
				if limit > 0 && files > limit {
					return errTooMany(limit)
				}
			}
			return nil
		})
		if err != nil {
			if statusOf(err) == 404 && !r.feat(ctx).EmptyDir {
				continue
			}
			return Walked{}, err
		}
	}
	return collect(rels, raw, limit)
}

// walkDir streams `lsjson -R` of a folder entry by entry, so a listing
// past a limit stops rclone at once.
func (r *RcloneFS) walkDir(ctx context.Context, rel string, fn func(lsEntry) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Bin, append(lsArgs("-R"), "--", r.spec(rel))...)
	cmd.Env = rcloneEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return apierr.New(502, "%s: rclone did not start: %s", r.Remote, err.Error())
	}
	dec := json.NewDecoder(pipe)
	var stop error
	if tok, err := dec.Token(); err == nil && tok == json.Delim('[') {
		for dec.More() {
			var e lsEntry
			if err := dec.Decode(&e); err != nil {
				stop = apierr.New(502, "%s: rclone's listing could not be read", r.Remote)
				break
			}
			if err := fn(e); err != nil {
				stop = err
				break
			}
		}
	}
	if stop != nil {
		cancel()
		cmd.Wait()
		return stop
	}
	io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		status := 502
		if code == 3 || code == 4 {
			status = 404
		}
		return apierr.New(status, "%s: %s", r.Remote, (&rcloneError{code: code, msg: rcloneMessage(stderr.String())}).Error())
	}
	return nil
}

func (r *RcloneFS) Free(ctx context.Context, rel string) (int64, bool) {
	if !r.feat(ctx).About {
		return 0, false
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := r.run(c, nil, "about", "--json", "--", r.spec(""))
	if err != nil {
		return 0, false
	}
	var a struct {
		Free *int64 `json:"free"`
	}
	if json.Unmarshal(out, &a) != nil || a.Free == nil {
		return 0, false
	}
	return *a.Free, true
}

// hashKinds is the hashes the remote gives.
func (r *RcloneFS) hashKinds(ctx context.Context) []string { return r.feat(ctx).Hashes }

// hashes is each file's hash of one kind, from rclone's listing of the
// folders that hold them (--files-from-raw names just those files). On an
// object store an MD5 comes from the listing; on a file system rclone
// reads each listed file. A file with no such hash (an S3 object uploaded
// in parts) has none in the map.
func (r *RcloneFS) hashes(ctx context.Context, kind string, rels []string) (map[string]string, error) {
	out := map[string]string{}
	if len(rels) == 0 {
		return out, nil
	}
	base := commonDir(rels)
	var list bytes.Buffer
	for _, rel := range rels {
		list.WriteString(strings.TrimPrefix(strings.TrimPrefix(rel, base), "/"))
		list.WriteByte('\n')
	}
	f, err := listFile(list.Bytes())
	if err != nil {
		return nil, err
	}
	defer os.Remove(f)
	es, err := r.lsjson(ctx, base, "-R", "--files-only", "--hash", "--hash-type", kind, "--files-from-raw", f)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if h := e.Hashes[kind]; h != "" {
			out[join(base, e.Path)] = strings.ToLower(h)
		}
	}
	return out, nil
}

func (r *RcloneFS) Sha256(ctx context.Context, rels []string) (map[string]string, error) {
	out := map[string]string{}
	if r.feat(ctx).has("sha256") {
		got, err := r.hashes(ctx, "sha256", rels)
		if err != nil {
			return nil, err
		}
		out = got
	}
	var rest []string
	for _, rel := range rels {
		if out[rel] == "" {
			rest = append(rest, rel)
		}
	}
	// The remote has no sha256 (S3 has MD5 only): read each file through
	// rclone cat and hash it here.
	got, err := readHashes(ctx, r, rest, sha256.New)
	for k, v := range got {
		out[k] = v
	}
	return out, err
}

// readHashes hashes files by reading them through the FS, four at once.
func readHashes(ctx context.Context, f FS, rels []string, newHash func() hash.Hash) (map[string]string, error) {
	out := map[string]string{}
	var mu sync.Mutex
	var first error
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, rel := range rels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h := newHash()
			rc, err := f.Read(ctx, rel)
			if err == nil {
				_, err = io.Copy(h, rc)
				if cerr := rc.Close(); err == nil {
					err = cerr
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			out[rel] = hex.EncodeToString(h.Sum(nil))
		}()
	}
	wg.Wait()
	return out, first
}

// procReader is a running rclone's output: Close stops it, and an error
// it ends with is the reader's last error.
type procReader struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *bytes.Buffer
	cancel context.CancelFunc
	remote string
	once   sync.Once
	err    error
	done   bool
}

func (p *procReader) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF {
		if werr := p.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (p *procReader) wait() error {
	p.once.Do(func() {
		if err := p.cmd.Wait(); err != nil {
			code := -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
			status := 502
			if code == 3 || code == 4 {
				status = 404
			}
			p.err = apierr.New(status, "%s: %s", p.remote, (&rcloneError{code: code, msg: rcloneMessage(p.stderr.String())}).Error())
		}
		p.done = true
	})
	return p.err
}

func (p *procReader) Close() error {
	p.cancel()
	p.out.Close()
	p.wait()
	return nil
}

// Read is rclone cat of one file.
func (r *RcloneFS) Read(ctx context.Context, rel string) (io.ReadCloser, error) {
	if rel == "" {
		return nil, apierr.New(400, "name a file")
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, r.Bin, "cat", "--", r.spec(rel))
	cmd.Env = rcloneEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, apierr.New(502, "%s: rclone did not start: %s", r.Remote, err.Error())
	}
	return &procReader{cmd: cmd, out: out, stderr: &stderr, cancel: cancel, remote: r.Remote}, nil
}

// Write is rclone rcat of a new file, with its modification time set as
// metadata in the same upload. A name already there is refused first;
// rclone itself would write over it.
func (r *RcloneFS) Write(ctx context.Context, rel string, src io.Reader, mtime int64, mode uint32) (int64, error) {
	if rel == "" {
		return 0, apierr.New(400, "name a file")
	}
	st, err := r.Stat(ctx, []string{rel})
	if err != nil {
		return 0, err
	}
	if _, ok := st[rel]; ok {
		return 0, apierr.New(409, "%s is already there", rel)
	}
	args := []string{"rcat"}
	if mtime > 0 {
		args = append(args, "--metadata", "--metadata-set", "mtime="+time.Unix(mtime, 0).UTC().Format(time.RFC3339))
	}
	args = append(args, "--", r.spec(rel))
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Env = rcloneEnv()
	cr := &counter{r: src}
	cmd.Stdin = cr
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return cr.n, ctx.Err()
		}
		if errors.Is(err, errTooLong) {
			return cr.n, err
		}
		return cr.n, apierr.New(502, "%s: %s could not be written: %s", r.Remote, rel, rcloneMessage(stderr.String()))
	}
	return cr.n, nil
}

type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += int64(k)
	return k, err
}

// Mkdir is rclone mkdir. On an object store it makes nothing below a
// bucket: a folder there exists once a file is in it.
func (r *RcloneFS) Mkdir(ctx context.Context, rel string) error {
	if rel == "" {
		return nil
	}
	_, err := r.run(ctx, nil, "mkdir", "--", r.spec(rel))
	return err
}

// landSplit finds the staging folder and the destination folder every
// pair shares (Staged = stage/x, Final = dest/x), which is how land.go
// builds them; ok false when they do not.
func landSplit(pairs []LandPair) (stage, dest string, rest []string, ok bool) {
	if len(pairs) == 0 {
		return "", "", nil, false
	}
	a, b := strings.Split(pairs[0].Staged, "/"), strings.Split(pairs[0].Final, "/")
	n := 0
	for n < len(a)-1 && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	if n == 0 {
		return "", "", nil, false
	}
	stage = strings.Join(a[:len(a)-n], "/")
	dest = strings.Join(b[:len(b)-n], "/")
	if !strings.HasPrefix(baseName(stage), stagingPrefix) {
		return "", "", nil, false
	}
	for _, p := range pairs {
		x := strings.TrimPrefix(p.Staged, stage+"/")
		if x == p.Staged || join(dest, x) != p.Final {
			return "", "", nil, false
		}
		rest = append(rest, x)
	}
	return stage, dest, rest, true
}

// Land gives staged files their names with one rclone move of exactly the
// files listed (a rename where the remote has one, else a server-side copy
// and delete). Names already at the destination are checked first: only
// a plain file the plan listed as replaced may be written over.
func (r *RcloneFS) Land(ctx context.Context, pairs []LandPair) ([]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	var finals []string
	for _, p := range pairs {
		finals = append(finals, p.Final)
	}
	there, err := r.Stat(ctx, finals)
	if err != nil {
		return nil, err
	}
	upTo := len(pairs)
	var conflict error
	for i, p := range pairs {
		if fi, ok := there[p.Final]; ok && (!p.Replace || fi.Type != 'f') {
			upTo, conflict = i, apierr.New(409, "%s is already there and the plan did not list it", p.Final)
			break
		}
	}
	done, err := r.landPairs(ctx, pairs[:upTo])
	if err != nil {
		return done, err
	}
	return done, conflict
}

func (r *RcloneFS) landPairs(ctx context.Context, pairs []LandPair) ([]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	stage, dest, rest, ok := landSplit(pairs)
	if !ok {
		var done []string
		for _, p := range pairs {
			if _, err := r.run(ctx, nil, "moveto", "--", r.spec(p.Staged), r.spec(p.Final)); err != nil {
				return done, err
			}
			done = append(done, p.Final)
		}
		return done, nil
	}
	f, err := listFile([]byte(strings.Join(rest, "\n") + "\n"))
	if err != nil {
		return nil, err
	}
	defer os.Remove(f)
	_, merr := r.run(ctx, nil, "move", "--files-from-raw", f, "--no-check-dest", "--ignore-times", "--no-traverse",
		"--transfers", "16", "--checkers", "16", "--", r.spec(stage), r.spec(dest))
	if merr == nil {
		var done []string
		for _, p := range pairs {
			done = append(done, p.Final)
		}
		return done, nil
	}
	// Those no longer in the staging folder took their names.
	var staged []string
	for _, p := range pairs {
		staged = append(staged, p.Staged)
	}
	left, err := r.Stat(context.WithoutCancel(ctx), staged)
	if err != nil {
		return nil, merr
	}
	var done []string
	for _, p := range pairs {
		if _, ok := left[p.Staged]; !ok {
			done = append(done, p.Final)
		}
	}
	return done, merr
}

// Remove deletes plain files with one rclone delete of exactly those
// files (--files-from-raw, and --max-delete their number, so rclone can
// delete no more), then folders that are empty, deepest first. An object
// store has no empty folders, so there the folders are left to vanish
// with their last object.
func (r *RcloneFS) Remove(ctx context.Context, files, dirs []string) ([]string, error) {
	var removed []string
	var plain []string
	if len(files) > 0 {
		st, err := r.Stat(ctx, files)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if fi, ok := st[f]; ok && fi.Type == 'f' && f != "" {
				plain = append(plain, f)
			}
		}
	}
	if len(plain) > 0 {
		base := commonDir(plain)
		var list bytes.Buffer
		for _, f := range plain {
			list.WriteString(strings.TrimPrefix(strings.TrimPrefix(f, base), "/"))
			list.WriteByte('\n')
		}
		lf, err := listFile(list.Bytes())
		if err != nil {
			return nil, err
		}
		defer os.Remove(lf)
		_, derr := r.run(ctx, nil, "delete", "--files-from-raw", lf, "--max-delete", strconv.Itoa(len(plain)), "--", r.spec(base))
		left, serr := r.Stat(context.WithoutCancel(ctx), plain)
		if serr != nil {
			return nil, serr
		}
		for _, f := range plain {
			if _, ok := left[f]; !ok {
				removed = append(removed, f)
			}
		}
		if derr != nil {
			return removed, derr
		}
	}
	if r.feat(ctx).EmptyDir {
		for _, d := range deepestFirst(dirs) {
			if d != "" {
				r.run(ctx, nil, "rmdir", "--", r.spec(d)) // only when empty
			}
		}
	}
	return removed, nil
}

// RemoveStaging is rclone purge of a staging folder; one that is not
// there is already gone.
func (r *RcloneFS) RemoveStaging(ctx context.Context, rel string) error {
	if !strings.HasPrefix(baseName(rel), stagingPrefix) {
		return apierr.New(400, "not a staging folder")
	}
	_, err := r.run(ctx, nil, "purge", "--", r.spec(rel))
	if err != nil && statusOf(err) == 404 {
		return nil
	}
	return err
}

// commonDir is the deepest folder holding every path.
func commonDir(rels []string) string {
	if len(rels) == 0 {
		return ""
	}
	d := parentOf(rels[0])
	for _, r := range rels[1:] {
		for d != "" && !within(r, d) {
			d = parentOf(d)
		}
	}
	return d
}

// listFile writes a list for --files-from-raw to a private temporary file.
func listFile(b []byte) (string, error) {
	f, err := os.CreateTemp("", "studio-rclone-list-")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

// ---- hashes both ends share ----

// hashKinder is an FS that gives hashes other than sha256: an rclone
// remote, and this computer (which reads the file).
type hashKinder interface {
	hashKinds(ctx context.Context) []string
	hashes(ctx context.Context, kind string, rels []string) (map[string]string, error)
}

// localKinds are the hashes this computer computes for an rclone
// comparison.
var localKinds = []string{"md5", "sha1", "sha256"}

func (l *LocalFS) hashKinds(context.Context) []string { return localKinds }

func (l *LocalFS) hashes(ctx context.Context, kind string, rels []string) (map[string]string, error) {
	nh := map[string]func() hash.Hash{"md5": md5.New, "sha1": sha1.New, "sha256": sha256.New}[kind]
	if nh == nil {
		return map[string]string{}, nil
	}
	return readHashes(ctx, l, rels, nh)
}

// hashOrder is the order a shared hash is chosen in: MD5 first, which an
// object store gives from its listing.
var hashOrder = []string{"md5", "sha1", "sha256", "crc32"}

// commonHash is the hash an rclone comparison uses: one both ends give,
// when at least one end is an rclone remote; "" for none.
func commonHash(ctx context.Context, a, b FS) string {
	_, ra := asRclone(a)
	_, rb := asRclone(b)
	if !ra && !rb {
		return ""
	}
	ha, ok1 := a.(hashKinder)
	hb, ok2 := b.(hashKinder)
	if !ok1 || !ok2 {
		return ""
	}
	ka, kb := ha.hashKinds(ctx), hb.hashKinds(ctx)
	for _, h := range hashOrder {
		if contains(ka, h) && contains(kb, h) {
			return h
		}
	}
	return ""
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

// pairSums is each file's hash at both ends for a check: a hash both ends
// share where one end is an rclone remote (so an object store's MD5 is
// used, not a download), else sha256; a file with no shared hash at either
// end falls back to sha256 for that file. It returns the hash's name.
func pairSums(ctx context.Context, a, b FS, ra, rb []string) (map[string]string, map[string]string, string, error) {
	kind := commonHash(ctx, a, b)
	if kind == "" || kind == "sha256" {
		x, err := a.Sha256(ctx, ra)
		if err != nil {
			return nil, nil, "", err
		}
		y, err := b.Sha256(ctx, rb)
		return x, y, "sha256", err
	}
	x, err := a.(hashKinder).hashes(ctx, kind, ra)
	if err != nil {
		return nil, nil, "", err
	}
	y, err := b.(hashKinder).hashes(ctx, kind, rb)
	if err != nil {
		return nil, nil, "", err
	}
	var fa, fb []string
	for i := range ra {
		if x[ra[i]] == "" || y[rb[i]] == "" {
			fa, fb = append(fa, ra[i]), append(fb, rb[i])
		}
	}
	name := kind
	if len(fa) > 0 {
		sa, err := a.Sha256(ctx, fa)
		if err != nil {
			return nil, nil, "", err
		}
		sb, err := b.Sha256(ctx, fb)
		if err != nil {
			return nil, nil, "", err
		}
		for i := range fa {
			x[fa[i]], y[fb[i]] = "sha256:"+sa[fa[i]], "sha256:"+sb[fb[i]]
			if sa[fa[i]] == "" {
				x[fa[i]] = ""
			}
		}
		name = kind + " (sha256 where there was none)"
	}
	return x, y, name, nil
}

// noFolders: an object store, where a folder that holds nothing is not
// there, so a destination folder may be missing and still be one.
func noFolders(ctx context.Context, f FS) bool {
	r, ok := asRclone(f)
	return ok && !r.feat(ctx).EmptyDir
}
