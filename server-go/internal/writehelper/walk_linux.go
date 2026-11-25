package writehelper

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// failure is an error with the exit status it ends the helper with.
type failure struct {
	code int
	msg  string
}

func (f *failure) Error() string { return f.msg }

func fail(code int, format string, a ...any) error {
	return &failure{code, fmt.Sprintf(format, a...)}
}

// beforeUse is called between a name being looked at and being used; the
// tests swap what is there. noOpenat2 has them take the walk used on older
// kernels.
var (
	beforeUse = func(what string) {}
	noOpenat2 = false
)

// tree is a library's folder, held open.
type tree struct {
	fd   int
	root string
	// priv is how what is written is closed to other accounts: 1 in a
	// person's own space, 2 in a folder of their own.
	priv int
	// umask is the one the helper was started with.
	umask int
}

// pin is the folder a library was added for: its inode and its owner; a
// zero inode is any.
type pin struct {
	ino uint64
	uid uint32
	set bool
}

func parsePin(s string) (pin, error) {
	a, b, ok := strings.Cut(s, ":")
	ino, err1 := strconv.ParseUint(a, 10, 64)
	uid, err2 := strconv.ParseUint(b, 10, 32)
	if !ok || err1 != nil || err2 != nil {
		return pin{}, fail(exitUsage, "--pin: INODE:UID")
	}
	return pin{ino, uint32(uid), true}, nil
}

// openTree opens the library's folder. However its path was reached, what
// was opened is the folder itself, so a link or another folder put in its
// place is refused by the pin.
func openTree(root string, p pin, priv int) (*tree, error) {
	fd, err := openDir(unix.AT_FDCWD, root, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		return nil, fail(exitNoRoot, "no such folder: %s", root)
	}
	if err != nil {
		return nil, fail(exitNoRoot, "cannot open %s: %v", root, err)
	}
	t := &tree{fd: fd, root: root, priv: priv, umask: unix.Umask(0)}
	unix.Umask(t.umask)
	st, err := statOf(fd)
	if err != nil {
		return nil, fail(exitNoRoot, "cannot open %s: %v", root, err)
	}
	if p.set && (st.Uid != p.uid || p.ino != 0 && st.Ino != p.ino) {
		return nil, fail(ExitMoved, "%s is not the folder this library was added for: it was moved or replaced, so nothing was changed. Rebuild the library, or add it again, to take the one that is there now.", root)
	}
	return t, nil
}

// openDir opens a folder; one that may be passed through and not read is
// opened for passing through only.
func openDir(at int, name string, how int) (int, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | how
	fd, err := retry(func() (int, error) { return unix.Openat(at, name, flags, 0) })
	if errors.Is(err, unix.EACCES) {
		return retry(func() (int, error) {
			return unix.Openat(at, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|how, 0)
		})
	}
	return fd, err
}

func retry(fn func() (int, error)) (int, error) {
	for {
		n, err := fn()
		if err != unix.EINTR {
			return n, err
		}
	}
}

func statOf(fd int) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(fd, "", &st, unix.AT_EMPTY_PATH)
	if err == unix.EINVAL || err == unix.ENOENT {
		err = unix.Fstat(fd, &st)
	}
	return st, err
}

// statAt is an entry of a folder, the link itself where it is one.
func statAt(dir int, name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}

func kind(st unix.Stat_t) uint32 { return st.Mode & unix.S_IFMT }

// parts is a path in the folder, split. No part goes up, and none is empty.
func parts(rel string) ([]string, error) {
	if strings.ContainsRune(rel, 0) {
		return nil, fail(exitOutside, "unusable path")
	}
	var out []string
	for _, p := range strings.Split(rel, "/") {
		switch p {
		case "", ".":
		case "..":
			return nil, fail(exitOutside, "%s leads outside the location", rel)
		default:
			out = append(out, p)
		}
	}
	return out, nil
}

// split is a path's folder and its name in it.
func split(rel string) ([]string, string, error) {
	p, err := parts(rel)
	if err != nil {
		return nil, "", err
	}
	if len(p) == 0 {
		return nil, "", fail(exitOutside, "refusing the location itself")
	}
	return p[:len(p)-1], p[len(p)-1], nil
}

// dir is a folder held open.
type dir struct{ fd int }

func (d dir) close() { unix.Close(d.fd) }

// file is a path to the object a descriptor holds, for the calls that take
// no descriptor opened with O_PATH.
func procPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

// open is the folder at p, reached from the library's folder through no
// link; with make, what is missing of it is made. rel names it in messages.
func (t *tree) open(p []string, rel string, make bool) (dir, error) {
	beforeUse("open")
	if len(p) > 0 && !make && !noOpenat2 {
		how := unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
		if fd, err := retry(func() (int, error) { return unix.Openat2(t.fd, strings.Join(p, "/"), &how) }); err == nil {
			return dir{fd}, nil
		}
		// Why not, or a kernel without it: the walk below says.
	}
	fd, err := unix.FcntlInt(uintptr(t.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return dir{}, fail(exitCannot, "%s: %v", rel, err)
	}
	for _, name := range p {
		next, err := t.step(fd, name, rel, make)
		unix.Close(fd)
		if err != nil {
			return dir{}, err
		}
		fd = next
	}
	return dir{fd}, nil
}

// step opens the folder named in the open folder at, never through a link.
func (t *tree) step(at int, name, rel string, make bool) (int, error) {
	for try := 0; ; try++ {
		fd, err := openDir(at, name, unix.O_NOFOLLOW)
		switch {
		case err == nil:
			return fd, nil
		case errors.Is(err, unix.ENOENT) && make && try < 8:
			if err := unix.Mkdirat(at, name, 0o777); err != nil && !errors.Is(err, unix.EEXIST) {
				return -1, fail(exitCannot, "%s could not be made: %v", rel, err)
			}
		case errors.Is(err, unix.ENOENT):
			return -1, fail(exitMissing, "%s: no such file or folder", rel)
		case errors.Is(err, unix.ELOOP):
			return -1, fail(exitOutside, "%s goes through a link", rel)
		case errors.Is(err, unix.ENOTDIR):
			if st, e := statAt(at, name); e == nil && kind(st) == unix.S_IFLNK {
				return -1, fail(exitOutside, "%s goes through a link", rel)
			}
			return -1, fail(exitCannot, "%s: a file is where its folder would be", rel)
		default:
			return -1, fail(exitCannot, "%s: the folder cannot be opened: %v", rel, err)
		}
	}
}

// parent is the folder a path is in, and its name there.
func (t *tree) parent(rel string, make bool) (dir, string, error) {
	p, name, err := split(rel)
	if err != nil {
		return dir{}, "", err
	}
	d, err := t.open(p, rel, make)
	return d, name, err
}

func stageName(name string) bool {
	return strings.HasPrefix(name, stagePrefix) || strings.HasPrefix(name, uploadPrefix)
}

// like sets the umask for what a write makes at p. In a private location it
// is closed to other accounts unless the folder it lands in is open to
// them, when it is readable as they read the folder: the nearest folder
// there is decides, so a path made in several levels takes one answer, and
// what is staged takes that of the folder it is for. In a folder of the
// person's own (priv 2) one open to its group is written with their umask.
// A default ACL, where there is one, wins over either.
func (t *tree) like(p []string) {
	unix.Umask(t.umask)
	if t.priv == 0 {
		return
	}
	fd, err := unix.FcntlInt(uintptr(t.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		unix.Umask(0o077)
		return
	}
	for _, name := range p {
		if stageName(name) {
			break
		}
		next, err := openDir(fd, name, unix.O_NOFOLLOW)
		if err != nil {
			break
		}
		unix.Close(fd)
		fd = next
	}
	st, err := statOf(fd)
	unix.Close(fd)
	switch {
	case err == nil && st.Mode&0o004 != 0:
		unix.Umask(0o022)
	case err == nil && t.priv == 2 && st.Mode&0o040 != 0:
	default:
		unix.Umask(0o077)
	}
}

// closed: in a private location, a folder closed to other accounts with no
// ACL of its own, so what lands in it is closed the same way.
func (t *tree) closed(fd int) bool {
	if t.priv == 0 {
		return false
	}
	st, err := statOf(fd)
	if err != nil || st.Mode&0o004 != 0 || t.priv == 2 && st.Mode&0o040 != 0 {
		return false
	}
	return !hasACL(fd)
}

func hasACL(fd int) bool {
	for _, a := range []string{aclAccess, aclDefault, "system.nfs4_acl", "system.gpfs_nfs4_acl"} {
		if n, err := unix.Getxattr(procPath(fd), a, nil); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// ours is whether st was made by this account: its owner is this account's,
// or, on a file system that gives everything another owner (one that maps
// accounts), the owner it gives what is made in at.
func ours(at int, st unix.Stat_t) bool {
	if st.Uid == uint32(os.Geteuid()) {
		return true
	}
	name := tempName()
	if unix.Mkdirat(at, name, 0o700) != nil {
		return false
	}
	made, err := statAt(at, name)
	unix.Unlinkat(at, name, unix.AT_REMOVEDIR)
	return err == nil && made.Uid == st.Uid
}

func tempName() string {
	var b [8]byte
	rand.Read(b[:])
	return uploadPrefix + hex.EncodeToString(b[:])
}
