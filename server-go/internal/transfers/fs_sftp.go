package transfers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// An account that takes SFTP only (OpenSSH's ForceCommand internal-sftp)
// answers every command with this line and status 1. Studio then serves
// the location over the SFTP protocol, with the same ssh settings, and
// runs no program on the host: rsync is not offered there, and studio-copy
// reads and writes each file through the SFTP session.
const sftpOnlyLine = "This service allows sftp connections only."

// Subsystemer is the part of a Runner that starts an ssh subsystem; the
// server's pool has it, for plain ssh sites only.
type Subsystemer interface {
	Subsystem(site, key, name string) (Cmd, error)
}

// sftpKnownFor is how long what a site was found to be is kept.
const sftpKnownFor = 10 * time.Minute

type sftpMark struct {
	only bool
	at   time.Time
}

var sftpSites = struct {
	sync.Mutex
	m map[string]sftpMark
}{m: map[string]sftpMark{}}

func sftpSiteKey(site, key string) string { return site + "|" + keyTag(key) }

// markSFTP records what a site was found to be.
func markSFTP(site, key string, only bool) {
	sftpSites.Lock()
	sftpSites.m[sftpSiteKey(site, key)] = sftpMark{only, time.Now()}
	sftpSites.Unlock()
}

// sftpAnswer reports whether a command's answer is an SFTP-only refusal.
// OpenSSH prints the line on standard output; both streams are looked at.
func sftpAnswer(res remote.Result) bool {
	return res.Code != 0 && (strings.Contains(res.Stdout, sftpOnlyLine) || strings.Contains(res.Stderr, sftpOnlyLine))
}

// sftpOnlySite reports whether a site's account takes SFTP only. The
// answer is kept a while; when it is not known, one command that does
// nothing is run to find out. Sites reached through pw ssh, and runners
// that cannot start a subsystem, are never SFTP only.
func sftpOnlySite(ctx context.Context, run Runner, site, key string) bool {
	if _, ok := run.(Subsystemer); !ok || !remote.IsSSH(site) {
		return false
	}
	k := sftpSiteKey(site, key)
	sftpSites.Lock()
	e, ok := sftpSites.m[k]
	sftpSites.Unlock()
	if ok && time.Since(e.at) < sftpKnownFor {
		return e.only
	}
	res, err := run.RunOnce(ctx, site, key, []string{"true"}, 30*time.Second, nil)
	switch {
	case err != nil:
		// ssh itself failed; the call that follows says why.
		return false
	case sftpAnswer(res):
		markSFTP(site, key, true)
		return true
	case res.Code == 0:
		markSFTP(site, key, false)
	}
	return false
}

// SFTPOnly reports whether the location's account takes SFTP only.
func (r *RemoteFS) SFTPOnly(ctx context.Context) bool {
	// A private location's writes need the helper's checks, which SFTP
	// cannot make; it fails there rather than write without them.
	if r.Private || r.NoLinks {
		return false
	}
	return sftpOnlySite(ctx, r.R, r.SiteID, r.KeyText)
}

// isSFTPOnly reports whether an FS is a host location served over SFTP.
func isSFTPOnly(ctx context.Context, f FS) bool {
	r, ok := f.(*RemoteFS)
	return ok && r.SFTPOnly(ctx)
}

// sftpConn is one SFTP session: a client over an ssh process.
type sftpConn struct {
	*sftp.Client
	cmd    Cmd
	stderr *bytes.Buffer
	stop   func() bool
	once   sync.Once
}

func (s *sftpConn) Close() error {
	s.once.Do(func() {
		s.stop()
		s.Client.Close()
		s.cmd.Kill()
		s.cmd.Wait()
	})
	return nil
}

// dialSFTP opens an SFTP session on a site; the context's end closes it.
func dialSFTP(ctx context.Context, run Runner, site, key string) (*sftpConn, error) {
	sub, ok := run.(Subsystemer)
	if !ok {
		return nil, apierr.New(502, "%s: SFTP is not available", remote.SiteName(site))
	}
	cmd, err := sub.Subsystem(site, key, "sftp")
	if err != nil {
		return nil, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.SetStderr(&lockedWriter{w: &stderr})
	if err := cmd.Start(); err != nil {
		return nil, apierr.New(502, "%s: %s", remote.SiteName(site), err.Error())
	}
	stop := context.AfterFunc(ctx, cmd.Kill)
	c, err := sftp.NewClientPipe(out, in)
	if err != nil {
		stop()
		// ssh says why on standard error as it ends: let it.
		in.Close()
		ended := make(chan struct{})
		go func() { cmd.Wait(); close(ended) }()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			cmd.Kill()
			<-ended
		}
		msg := lastLine(remote.QuietStderr(stderr.String()))
		if msg == "" {
			msg = "the SFTP session could not start: " + err.Error()
		}
		return nil, apierr.New(502, "%s: %s", remote.SiteName(site), msg)
	}
	return &sftpConn{Client: c, cmd: cmd, stderr: &stderr, stop: stop}, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// sftpOp is one operation's session with the location's real root and
// the real folders already found in it.
type sftpOp struct {
	r    *RemoteFS
	c    *sftpConn
	root string // real
	dirs map[string]string
}

// open starts an operation: a session, and the root's real path.
func (r *RemoteFS) open(ctx context.Context) (*sftpOp, error) {
	c, err := dialSFTP(ctx, r.R, r.SiteID, r.KeyText)
	if err != nil {
		return nil, r.named(502, messageOf(err))
	}
	op := &sftpOp{r: r, c: c, dirs: map[string]string{}}
	fi, err := c.Stat(r.Dir)
	if err != nil || !fi.IsDir() {
		c.Close()
		return nil, r.named(502, "no such folder: "+r.Dir)
	}
	if op.root, err = op.real(r.Dir); err != nil {
		c.Close()
		return nil, r.named(502, "cannot open "+r.Dir)
	}
	return op, nil
}

func (op *sftpOp) Close() { op.c.Close() }

// real is p's real path, every link in it followed, found by looking at
// each name in turn. A server's own realpath is not relied on: some only
// clean the path. A name that is not there ends the walk, and the rest is
// joined as it is.
func (op *sftpOp) real(p string) (string, error) {
	return op.realFrom("/", strings.Split(strings.Trim(path.Clean(p), "/"), "/"))
}

// realFrom is real for the names todo below cur, a real folder.
func (op *sftpOp) realFrom(cur string, todo []string) (string, error) {
	hops := 0
	for len(todo) > 0 {
		name := todo[0]
		todo = todo[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			cur = path.Dir(cur)
			continue
		}
		next := path.Join(cur, name)
		fi, err := op.c.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return path.Join(append([]string{next}, todo...)...), nil
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > 40 {
			return "", errors.New("too many links")
		}
		t, err := op.c.ReadLink(next)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(t, "/") {
			cur = "/"
		}
		todo = append(strings.Split(strings.Trim(t, "/"), "/"), todo...)
	}
	return cur, nil
}

// abs is a relative path on the host.
func (op *sftpOp) abs(rel string) string {
	if rel == "" || rel == "." {
		return op.r.Dir
	}
	return path.Join(op.r.Dir, rel)
}

// realRel is the real path of rel (relative to the root), found from its
// folder's, which is kept for the operation: each name costs one look.
func (op *sftpOp) realRel(rel string) (string, error) {
	if rel == "" || rel == "." {
		return op.root, nil
	}
	if d, ok := op.dirs[rel]; ok {
		return d, nil
	}
	parent, err := op.realRel(dirOf(rel))
	if err != nil {
		return "", err
	}
	d, err := op.realFrom(parent, []string{baseName(rel)})
	if err != nil {
		return "", err
	}
	op.dirs[rel] = d
	return d, nil
}

// inside reports whether the real folder rel names (relative to the root)
// is inside the real root, as the helper's inside does.
func (op *sftpOp) inside(rel string) bool {
	d, err := op.realRel(rel)
	if err != nil {
		return false
	}
	return d == op.root || strings.HasPrefix(d, strings.TrimSuffix(op.root, "/")+"/")
}

// parentInside checks the folder holding rel.
func (op *sftpOp) parentInside(rel string) error {
	if !op.inside(dirOf(rel)) {
		return op.r.named(400, rel+" leads outside the location")
	}
	return nil
}

func dirOf(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

func sftpInfo(rel string, fi os.FileInfo) FileInfo {
	t := byte('o')
	m := fi.Mode()
	switch {
	case m.IsRegular():
		t = 'f'
	case m.IsDir():
		t = 'd'
	case m&fs.ModeSymlink != 0:
		t = 'l'
	}
	size := fi.Size()
	if t == 'd' {
		size = 0
	}
	return FileInfo{Rel: rel, Type: t, Size: size, Mtime: fi.ModTime().Unix(), Mode: uint32(m.Perm())}
}

func joinRel(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

// sftpErr is a failed SFTP call as a status and a message about rel.
func (op *sftpOp) fail(err error, rel, what string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return op.r.named(404, rel+": no such file or folder")
	}
	if errors.Is(err, fs.ErrPermission) {
		return op.r.named(403, rel+": permission denied")
	}
	return op.r.named(502, rel+" "+what)
}

func (r *RemoteFS) sftpList(ctx context.Context, rel string, max int) ([]Entry, bool, error) {
	op, err := r.open(ctx)
	if err != nil {
		return nil, false, err
	}
	defer op.Close()
	p := dotted(rel)
	fi, err := op.c.Lstat(op.abs(rel))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, r.named(404, p+": no such folder")
		}
		return nil, false, op.fail(err, p, "could not be read")
	}
	if !fi.IsDir() {
		return nil, false, r.named(400, p+" is not a folder")
	}
	if !op.inside(rel) {
		return nil, false, r.named(400, p+" leads outside the location")
	}
	items, err := op.c.ReadDirContext(ctx, op.abs(rel))
	if err != nil {
		return nil, false, op.fail(err, p, "could not be listed")
	}
	var out []Entry
	for _, e := range items {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, entryOf(sftpInfo(e.Name(), e), e.Name()))
	}
	es, more := sortEntries(out, max)
	return es, more, nil
}

func (r *RemoteFS) sftpStat(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	out := map[string]FileInfo{}
	op, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		fi, err := op.c.Lstat(op.abs(rel))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, op.fail(err, rel, "could not be read")
		}
		if err := op.parentInside(rel); err != nil {
			return nil, err
		}
		out[rel] = sftpInfo(rel, fi)
	}
	return out, nil
}

func (r *RemoteFS) sftpWalk(ctx context.Context, rels []string, limit int) (Walked, error) {
	op, err := r.open(ctx)
	if err != nil {
		return Walked{}, err
	}
	defer op.Close()
	var raw []FileInfo
	files := 0
	// Hidden entries are listed, for counting, and not gone into.
	var walk func(rel string) error
	walk = func(rel string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		items, err := op.c.ReadDirContext(ctx, op.abs(rel))
		if err != nil {
			return op.fail(err, dotted(rel), "could not be listed")
		}
		for _, e := range items {
			fi := sftpInfo(joinRel(rel, e.Name()), e)
			raw = append(raw, fi)
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if fi.Type == 'f' {
				// Past the limit the walk can stop: collect says so.
				if files++; limit > 0 && files > limit {
					return errStop
				}
			}
			if fi.Type == 'd' {
				if err := walk(fi.Rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, rel := range rels {
		p := dotted(rel)
		fi, err := op.c.Lstat(op.abs(rel))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return Walked{}, r.named(404, p+": no such file or folder")
			}
			return Walked{}, op.fail(err, p, "could not be read")
		}
		if err := op.parentInside(rel); err != nil {
			return Walked{}, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return Walked{}, r.named(400, p+" is a link; name what it points to")
		}
		raw = append(raw, sftpInfo(rel, fi))
		if fi.IsDir() {
			if err := walk(rel); err == errStop {
				break
			} else if err != nil {
				return Walked{}, err
			}
		} else if fi.Mode().IsRegular() {
			files++
		}
	}
	return collect(rels, raw, limit)
}

var errStop = errors.New("stop")

func (r *RemoteFS) sftpFree(ctx context.Context, rel string) (int64, bool) {
	op, err := r.open(ctx)
	if err != nil {
		return 0, false
	}
	defer op.Close()
	if _, ok := op.c.HasExtension("statvfs@openssh.com"); !ok {
		return 0, false
	}
	st, err := op.c.StatVFS(op.abs(rel))
	if err != nil {
		return 0, false
	}
	return int64(st.Bavail * st.Frsize), true
}

// sftpSha256 reads each file over the session and hashes it here, so every
// byte crosses the network once more.
func (r *RemoteFS) sftpSha256(ctx context.Context, rels []string) (map[string]string, error) {
	op, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	out := map[string]string{}
	for _, rel := range rels {
		if err := op.parentInside(rel); err != nil {
			return nil, err
		}
		fi, err := op.c.Lstat(op.abs(rel))
		if err != nil || !fi.Mode().IsRegular() {
			return nil, r.named(400, rel+" is not a plain file")
		}
		f, err := op.c.Open(op.abs(rel))
		if err != nil {
			return nil, op.fail(err, rel, "could not be read")
		}
		h := sha256.New()
		_, err = io.Copy(h, &ctxReader{ctx, f})
		f.Close()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, r.named(502, rel+" could not be read")
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sftpReader is an open file; Close ends the session too.
type sftpReader struct {
	f  *sftp.File
	op *sftpOp
}

func (s *sftpReader) Read(p []byte) (int, error) { return s.f.Read(p) }

func (s *sftpReader) Close() error {
	err := s.f.Close()
	s.op.Close()
	return err
}

func (r *RemoteFS) sftpRead(ctx context.Context, rel string) (io.ReadCloser, error) {
	op, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	if err := op.parentInside(rel); err != nil {
		op.Close()
		return nil, err
	}
	fi, err := op.c.Lstat(op.abs(rel))
	if err != nil {
		op.Close()
		return nil, op.fail(err, rel, "could not be read")
	}
	if !fi.Mode().IsRegular() {
		op.Close()
		return nil, r.named(400, rel+" is not a plain file any more")
	}
	f, err := op.c.Open(op.abs(rel))
	if err != nil {
		op.Close()
		return nil, op.fail(err, rel, "could not be read")
	}
	return &sftpReader{f: f, op: op}, nil
}

// mkdirs makes rel's folders, each one checked to be inside the root as
// it is reached.
func (op *sftpOp) mkdirs(rel string) error {
	if rel == "" || rel == "." {
		return nil
	}
	cur := ""
	for _, name := range strings.Split(rel, "/") {
		cur = joinRel(cur, name)
		fi, err := op.c.Lstat(op.abs(cur))
		if errors.Is(err, fs.ErrNotExist) {
			if err := op.c.Mkdir(op.abs(cur)); err != nil {
				if fi, err2 := op.c.Stat(op.abs(cur)); err2 != nil || !fi.IsDir() {
					return op.fail(err, cur, "could not be made")
				}
			}
		} else if err != nil {
			return op.fail(err, cur, "could not be read")
		} else if !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
			return op.r.named(502, cur+" could not be made: a file is there")
		}
		if !op.inside(cur) {
			return op.r.named(400, cur+" leads outside the location")
		}
	}
	return nil
}

func (r *RemoteFS) sftpWrite(ctx context.Context, rel string, src io.Reader, mtime int64, mode uint32) (int64, error) {
	op, err := r.open(ctx)
	if err != nil {
		return 0, err
	}
	defer op.Close()
	if err := op.mkdirs(dirOf(rel)); err != nil {
		return 0, err
	}
	p := op.abs(rel)
	// Exclusive: a file already there is never written over.
	f, err := op.c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, op.fail(err, rel, "could not be written")
	}
	n, cerr := io.Copy(f, &ctxReader{ctx, src})
	if err := f.Close(); cerr == nil && err != nil {
		cerr = err
	}
	if cerr != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		var se *sftp.StatusError
		if errors.As(cerr, &se) {
			return n, r.named(502, rel+" could not be written: "+cerr.Error())
		}
		return n, cerr
	}
	if mtime > 0 {
		t := unixTime(mtime)
		op.c.Chtimes(p, t, t)
	}
	if mode&0o100 != 0 {
		if fi, err := op.c.Lstat(p); err == nil {
			op.c.Chmod(p, fi.Mode().Perm()|0o100)
		}
	}
	return n, nil
}

func (r *RemoteFS) sftpMkdir(ctx context.Context, rel string) error {
	if rel == "" {
		return nil
	}
	op, err := r.open(ctx)
	if err != nil {
		return err
	}
	defer op.Close()
	return op.mkdirs(rel)
}

// sftpLand gives each staged file its name as the helper does: never over
// a name that is there unless the plan listed it as replaced and it is a
// plain file; else a hard link then the staged name removed, so nothing
// there is written over, or a rename where the server has no hard links.
func (r *RemoteFS) sftpLand(ctx context.Context, pairs []LandPair) ([]string, error) {
	op, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	_, posix := op.c.HasExtension("posix-rename@openssh.com")
	_, hard := op.c.HasExtension("hardlink@openssh.com")
	var done []string
	for _, pr := range pairs {
		a, b := pr.Staged, pr.Final
		if err := op.mkdirs(dirOf(b)); err != nil {
			return done, err
		}
		if err := op.parentInside(b); err != nil {
			return done, err
		}
		if err := op.parentInside(a); err != nil {
			return done, err
		}
		fi, err := op.c.Lstat(op.abs(b))
		switch {
		case err == nil:
			if !pr.Replace || !fi.Mode().IsRegular() {
				return done, r.named(409, b+" is already there and the plan did not list it")
			}
			if posix {
				err = op.c.PosixRename(op.abs(a), op.abs(b))
			} else if err = op.c.Remove(op.abs(b)); err == nil {
				err = op.c.Rename(op.abs(a), op.abs(b))
			}
			if err != nil {
				return done, r.named(502, b+" could not be replaced")
			}
		case errors.Is(err, fs.ErrNotExist):
			if hard && op.c.Link(op.abs(a), op.abs(b)) == nil {
				op.c.Remove(op.abs(a))
				break
			}
			if _, err := op.c.Lstat(op.abs(b)); err == nil {
				return done, r.named(409, b+" is already there and the plan did not list it")
			}
			// SFTP's own rename does not write over a name that is there.
			if err := op.c.Rename(op.abs(a), op.abs(b)); err != nil {
				return done, r.named(502, b+" could not take its name")
			}
		default:
			return done, op.fail(err, b, "could not be read")
		}
		done = append(done, b)
	}
	return done, nil
}

func (r *RemoteFS) sftpRemove(ctx context.Context, files, dirs []string) ([]string, error) {
	op, err := r.open(ctx)
	if err != nil {
		return nil, err
	}
	defer op.Close()
	var done []string
	for _, f := range files {
		if err := op.parentInside(f); err != nil {
			return done, err
		}
		fi, err := op.c.Lstat(op.abs(f))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := op.c.Remove(op.abs(f)); err != nil {
			return done, r.named(502, f+" could not be removed")
		}
		done = append(done, f)
	}
	for _, d := range deepestFirst(dirs) {
		if d == "" {
			continue
		}
		if err := op.parentInside(d); err != nil {
			return done, err
		}
		op.c.RemoveDirectory(op.abs(d))
	}
	return done, nil
}

func (r *RemoteFS) sftpRemoveStaging(ctx context.Context, rel string) error {
	if !strings.HasPrefix(baseName(rel), stagingPrefix) {
		return r.named(400, "not a staging folder")
	}
	op, err := r.open(ctx)
	if err != nil {
		return err
	}
	defer op.Close()
	fi, err := op.c.Lstat(op.abs(rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return op.fail(err, rel, "could not be read")
	}
	if err := op.parentInside(rel); err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return r.named(400, rel+" is a link")
	}
	return op.removeAll(rel, fi)
}

// removeAll removes a folder and all in it; links are removed, never
// followed.
func (op *sftpOp) removeAll(rel string, fi os.FileInfo) error {
	p := op.abs(rel)
	if !fi.IsDir() {
		if err := op.c.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return op.fail(err, rel, "could not be removed")
		}
		return nil
	}
	items, err := op.c.ReadDir(p)
	if err != nil {
		return op.fail(err, rel, "could not be listed")
	}
	for _, e := range items {
		if err := op.removeAll(joinRel(rel, e.Name()), e); err != nil {
			return err
		}
	}
	if err := op.c.RemoveDirectory(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return op.fail(err, rel, "could not be removed")
	}
	return nil
}

// sftpHome is the folder an SFTP session starts in.
func sftpHome(ctx context.Context, run Runner, site, key string) (string, error) {
	c, err := dialSFTP(ctx, run, site, key)
	if err != nil {
		return "", err
	}
	defer c.Close()
	h, err := c.Getwd()
	if err != nil || !strings.HasPrefix(h, "/") {
		return "", apierr.New(502, "%s: the home folder could not be found", remote.SiteName(site))
	}
	return h, nil
}
