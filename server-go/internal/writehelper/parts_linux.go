package writehelper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	exitNoSpace    = 14
	exitChanged    = 13
	exitGone       = 14
	exitNotYours   = 15
	partData       = "data"
	partsGone      = "the parts that arrived are no longer there"
	partsNoSpace   = "there is no space left there"
	partUnit       = 1 << 20
	notPartsFolder = "not a folder of parts"
)

// startParts makes a folder of its own, closed to other accounts, with an
// empty file in it for the parts of a file that will be named final, with
// needKB free for it.
func (t *tree) startParts(rel, final, needKB string) error {
	p, name, err := split(rel)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(name, uploadPrefix) {
		return fail(exitNotPlain, notPartsFolder)
	}
	fp, fname, err := split(final)
	if err != nil {
		return err
	}
	if d, err := t.open(fp, final, false); err == nil {
		st, serr := statAt(d.fd, fname)
		d.close()
		if serr == nil && (kind(st) == unix.S_IFDIR || kind(st) == unix.S_IFLNK) {
			return fail(exitTaken, "%s is already there and is not a file", final)
		}
	}
	need, err := strconv.ParseInt(needKB, 10, 64)
	if err != nil {
		return fail(exitUsage, "needs a size")
	}
	if free, ok := t.free(p); ok && free < need {
		return fail(exitNoSpace, "%d", free)
	}
	t.like(p)
	at, err := t.open(p, final, true)
	if err != nil {
		return err
	}
	defer at.close()
	beforeUse("stage")
	if err := unix.Mkdirat(at.fd, name, 0o700); err != nil {
		return fail(exitCannot, "%s could not be written there", final)
	}
	d, err := t.closedDir(at.fd, name, final)
	if err != nil {
		return err
	}
	defer unix.Close(d)
	f, err := create(d, partData, final, 0o666)
	if err != nil {
		return err
	}
	return f.Close()
}

// free is the KB free where a path's nearest folder that exists is.
func (t *tree) free(p []string) (int64, bool) {
	fd, err := unix.FcntlInt(uintptr(t.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return 0, false
	}
	for _, name := range p {
		next, err := openDir(fd, name, unix.O_NOFOLLOW)
		if err != nil {
			break
		}
		unix.Close(fd)
		fd = next
	}
	defer unix.Close(fd)
	var s unix.Statfs_t
	if err := unix.Fstatfs(fd, &s); err != nil {
		return 0, false
	}
	unit := int64(s.Frsize)
	if unit == 0 {
		unit = int64(s.Bsize)
	}
	// By halves, so a size of exabytes does not run over.
	return int64(s.Bavail / 2 * uint64(unit) / 512), true
}

// closedDir opens a folder just made, checks that it is the one made, and
// closes it to other accounts.
func (t *tree) closedDir(at int, name, rel string) (int, error) {
	d, err := t.step(at, name, rel, false)
	if err != nil {
		return -1, err
	}
	st, err := statOf(d)
	if err != nil || !ours(at, st) {
		unix.Close(d)
		return -1, fail(exitOutside, "%s was replaced by another account", rel)
	}
	// A default ACL on the folder above stays for what is put here; the
	// folder itself is closed whatever it gives.
	if st.Mode&0o077 != 0 {
		_ = unix.Fchmod(d, st.Mode&0o7000|0o700)
	}
	return d, nil
}

// part writes n bytes at MB off of the file in a folder of parts.
func (t *tree) part(rel, off, size string, r io.Reader) error {
	p, name, err := split(rel)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(name, uploadPrefix) {
		return fail(exitNotPlain, notPartsFolder)
	}
	mb, err1 := strconv.ParseInt(off, 10, 64)
	n, err2 := strconv.ParseInt(size, 10, 64)
	if err1 != nil || err2 != nil || mb < 0 || n < 0 || mb > 1<<40 {
		return fail(exitUsage, "needs an offset and a size")
	}
	gone := fail(exitMissing, partsGone)
	at, err := t.open(p, rel, false)
	if err != nil {
		return missingAs(err, gone)
	}
	defer at.close()
	d, err := t.step(at.fd, name, rel, false)
	if err != nil {
		return missingAs(err, gone)
	}
	defer unix.Close(d)
	if st, err := statOf(d); err != nil || !ours(at.fd, st) || t.priv != 0 && st.Mode&0o077 != 0 {
		return gone
	}
	beforeUse("part")
	f, err := openPlain(d, partData, rel, unix.O_WRONLY)
	if err != nil {
		return gone
	}
	got, err := io.Copy(io.NewOffsetWriter(f, mb*partUnit), io.LimitReader(r, n))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return fail(exitNoSpace, partsNoSpace)
	case err != nil:
		return fail(exitCannot, "a part could not be written: %s", said(err))
	case got != n:
		return fail(exitRead, "a part did not arrive whole")
	}
	return nil
}

func missingAs(err, as error) error {
	var f *failure
	if errors.As(err, &f) && f.code == exitMissing {
		return as
	}
	return err
}

// text sends a file's bytes, at most max of them, where swap would take it.
func (t *tree) text(rel, size string, w io.Writer) error {
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return fail(exitUsage, "needs a size")
	}
	d, name, err := t.parent(rel, false)
	if err != nil {
		return missingAs(err, fail(exitMissing, "%s is not there", rel))
	}
	defer d.close()
	f, err := openToChange(d.fd, name, rel, exitMissing, "%s is not there")
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(w, io.LimitReader(f, n)); err != nil {
		return fail(exitRead, "%s could not be read", rel)
	}
	return nil
}

// openToChange opens a plain file the account may read and write.
func openToChange(at int, name, rel string, missing int, gone string) (*os.File, error) {
	st, err := statAt(at, name)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, fail(missing, gone, rel)
	case err != nil:
		return nil, fail(exitCannot, "%s could not be opened: %v", rel, err)
	case kind(st) == unix.S_IFLNK:
		return nil, fail(exitLink, "%s is a link", rel)
	case kind(st) != unix.S_IFREG:
		return nil, fail(exitNotPlain, "%s is not a plain file", rel)
	}
	beforeUse("change")
	f, err := openPlain(at, name, rel, unix.O_RDWR)
	if err == nil {
		return f, nil
	}
	if unix.Faccessat(at, name, unix.W_OK, 0) != nil {
		return nil, fail(exitNotYours, "%s is not yours to change", rel)
	}
	return nil, missingAs(err, fail(missing, gone, rel))
}

func sumOf(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// swap puts the bytes r gives in the place of a file's, only while it still
// has the checksum the change was made from. They go into a file beside it
// first, named staged, and count once their own checksum is right. The file
// keeps its mode, group, ACL and attributes; another account's file is
// written where it is, so it stays that account's.
func (t *tree) swap(rel, old, sum, staged string, r io.Reader, out io.Writer) error {
	if strings.Contains(staged, "/") || !strings.HasPrefix(staged, uploadPrefix) {
		return fail(exitUsage, "needs a name to write first")
	}
	const gone = "%s is not there any more"
	d, name, err := t.parent(rel, false)
	if err != nil {
		return missingAs(err, fail(exitGone, gone, rel))
	}
	defer d.close()
	f, err := openToChange(d.fd, name, rel, exitGone, gone)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := statOf(int(f.Fd()))
	if err != nil {
		return fail(exitRead, "%s could not be read", rel)
	}
	s, err := createRW(d.fd, staged)
	if err != nil {
		return fail(exitFailed, "%s: its folder takes no new file", rel)
	}
	defer s.Close()
	landed := false
	defer func() {
		if !landed {
			removeSame(d.fd, staged, s)
		}
	}()
	if _, err := io.Copy(s, r); err != nil {
		return fail(exitFailed, "%s could not be written: %s", rel, said(err))
	}
	if got, err := sumAt(s); err != nil || got != sum {
		return fail(exitRead, "%s: the change did not arrive whole", rel)
	}
	has, err := sumAt(f)
	if err != nil {
		return fail(exitRead, "%s could not be read", rel)
	}
	if has != old {
		fmt.Fprintln(out, has)
		return fail(exitChanged, "%s has changed", rel)
	}
	if st.Uid != uint32(os.Geteuid()) {
		return t.writeInPlace(d.fd, rel, staged, f, s)
	}
	sfd := int(s.Fd())
	_ = unix.Fchown(sfd, -1, int(st.Gid))
	copyXattrs(int(f.Fd()), sfd)
	if err := unix.Fchmod(sfd, st.Mode&0o7777); err != nil {
		return fail(exitFailed, "%s: its permissions could not be kept", rel)
	}
	beforeUse("swap")
	if now, err := statAt(d.fd, name); err != nil || now.Ino != st.Ino || now.Dev != st.Dev {
		return fail(exitFailed, "%s was replaced meanwhile", rel)
	}
	if err := unix.Renameat(d.fd, staged, d.fd, name); err != nil {
		return fail(exitFailed, "%s could not be replaced", rel)
	}
	landed = true
	return nil
}

func createRW(at int, name string) (*os.File, error) {
	fd, err := retry(func() (int, error) {
		return unix.Openat(at, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// removeSame removes a name while it is still the file held.
func removeSame(at int, name string, f *os.File) {
	a, err1 := statOf(int(f.Fd()))
	b, err2 := statAt(at, name)
	if err1 == nil && err2 == nil && a.Ino == b.Ino && a.Dev == b.Dev {
		_ = unix.Unlinkat(at, name, 0)
	}
}

func sumAt(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return sumOf(f)
}

// writeInPlace writes s over f, another account's file, keeping what f held
// beside it until it is done.
func (t *tree) writeInPlace(at int, rel, staged string, f, s *os.File) error {
	was, err := createRW(at, staged+"-was")
	if err != nil {
		return fail(exitFailed, "%s could not be written", rel)
	}
	defer was.Close()
	if err := overwrite(was, f); err != nil {
		removeSame(at, staged+"-was", was)
		return fail(exitFailed, "%s could not be written", rel)
	}
	if err := overwrite(f, s); err == nil {
		removeSame(at, staged+"-was", was)
		return nil
	}
	if err := overwrite(f, was); err == nil {
		removeSame(at, staged+"-was", was)
		return fail(exitFailed, "%s could not be written", rel)
	}
	return fail(exitFailed, "%s could not be written; what it held is in %s-was beside it", rel, staged)
}

// overwrite has to hold what from does, and no more.
func overwrite(to, from *os.File) error {
	if _, err := from.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := to.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(to, from)
	if err != nil {
		return err
	}
	return to.Truncate(n)
}

// copyXattrs gives one file's attributes, its ACL among them, to another,
// as far as the account may.
func copyXattrs(from, to int) {
	n, err := unix.Flistxattr(from, nil)
	if err != nil || n == 0 {
		return
	}
	names := make([]byte, n)
	if n, err = unix.Flistxattr(from, names); err != nil {
		return
	}
	for _, a := range strings.Split(strings.TrimSuffix(string(names[:n]), "\x00"), "\x00") {
		size, err := unix.Fgetxattr(from, a, nil)
		if err != nil || a == "" {
			continue
		}
		v := make([]byte, size)
		if size, err = unix.Fgetxattr(from, a, v); err == nil {
			_ = unix.Fsetxattr(to, a, v[:size], 0)
		}
	}
}
