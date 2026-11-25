package writehelper

import (
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// read sends a plain file's bytes, never a link's. begin is called once the
// file is open, before its first byte.
func (t *tree) read(rel string, begin func(), w io.Writer) error {
	d, name, err := t.parent(rel, false)
	if err != nil {
		return err
	}
	defer d.close()
	beforeUse("read")
	f, err := openPlain(d.fd, name, rel, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	begin()
	if _, err := io.Copy(w, f); err != nil {
		return fail(exitRead, "%s could not be read: %v", rel, err)
	}
	return nil
}

// openPlain opens a plain file in an open folder, never a link, and never
// waiting as a pipe would have it.
func openPlain(at int, name, rel string, flags int) (*os.File, error) {
	fd, err := retry(func() (int, error) {
		return unix.Openat(at, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	})
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, fail(exitMissing, "%s: no such file", rel)
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENXIO), errors.Is(err, unix.EISDIR):
		return nil, fail(exitNotPlain, "%s is not a plain file", rel)
	case err != nil:
		return nil, fail(exitCannot, "%s could not be opened: %v", rel, err)
	}
	st, err := statOf(fd)
	if err != nil || kind(st) != unix.S_IFREG {
		unix.Close(fd)
		return nil, fail(exitNotPlain, "%s is not a plain file", rel)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// write makes a new file, never over one, with what r gives; its folders
// are made as needed. touch is its time, as touch -t takes it in UTC.
func (t *tree) write(rel, touch string, exec bool, r io.Reader) error {
	p, _, err := split(rel)
	if err != nil {
		return err
	}
	t.like(p)
	d, name, err := t.parent(rel, true)
	if err != nil {
		return err
	}
	defer d.close()
	beforeUse("write")
	f, err := create(d.fd, name, rel, 0o666)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = finishFile(f, touch, exec)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(exitCannot, "%s could not be written: %s", rel, said(err))
	}
	return nil
}

// said is an error without the path os puts before it.
func said(err error) string {
	var e unix.Errno
	if errors.As(err, &e) {
		return e.Error()
	}
	return err.Error()
}

func create(at int, name, rel string, mode uint32) (*os.File, error) {
	fd, err := retry(func() (int, error) {
		return unix.Openat(at, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	})
	if errors.Is(err, unix.EEXIST) {
		return nil, fail(exitCannot, "%s could not be written: something is there already", rel)
	}
	if err != nil {
		return nil, fail(exitCannot, "%s could not be written: %v", rel, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// finishFile sets a written file's time and its owner's execute bit.
func finishFile(f *os.File, touch string, exec bool) error {
	if touch != "" {
		if when, err := time.Parse("200601021504.05", touch); err == nil {
			tv := unix.NsecToTimeval(when.UnixNano())
			_ = unix.Futimes(int(f.Fd()), []unix.Timeval{tv, tv})
		}
	}
	if exec {
		if st, err := statOf(int(f.Fd())); err == nil {
			_ = unix.Fchmod(int(f.Fd()), st.Mode&0o7777|0o100)
		}
	}
	return nil
}

// mkdir makes a folder and those above it; a staging folder is new, this
// account's and closed to every other.
func (t *tree) mkdir(rel string) error {
	p, err := parts(rel)
	if err != nil || len(p) == 0 {
		return err
	}
	t.like(p)
	name := p[len(p)-1]
	if !strings.HasPrefix(name, stagePrefix) {
		d, err := t.open(p, rel, true)
		if err == nil {
			d.close()
		}
		return err
	}
	at, err := t.open(p[:len(p)-1], rel, true)
	if err != nil {
		return err
	}
	defer at.close()
	beforeUse("stage")
	if err := unix.Mkdirat(at.fd, name, 0o700); err != nil {
		return fail(exitCannot, "%s could not be made: %v", rel, err)
	}
	d, err := t.closedDir(at.fd, name, rel)
	if err != nil {
		return err
	}
	return unix.Close(d)
}

// staged refuses a path through a staging folder, or a folder of parts, that
// another account made.
func (t *tree) staged(p []string, rel string) error {
	for i, name := range p {
		if !stageName(name) {
			continue
		}
		at, err := t.open(p[:i], rel, false)
		if err != nil {
			return err
		}
		defer at.close()
		d, err := t.step(at.fd, name, rel, false)
		if err != nil {
			return err
		}
		defer unix.Close(d)
		st, err := statOf(d)
		if err != nil || !ours(at.fd, st) {
			return fail(exitOutside, "%s was replaced by another account", rel)
		}
	}
	return nil
}

// land gives what is at from the name to, over a plain file only with
// replace, and over nothing else.
func (t *tree) land(from, to string, replace bool) error {
	fp, _, err := split(from)
	if err != nil {
		return err
	}
	tp, _, err := split(to)
	if err != nil {
		return err
	}
	if err := t.staged(fp, from); err != nil {
		return err
	}
	src, a, err := t.parent(from, false)
	if err != nil {
		return err
	}
	defer src.close()
	st, err := statAt(src.fd, a)
	if err != nil {
		return fail(exitMissing, "%s is not there", from)
	}
	if strings.HasPrefix(a, uploadPrefix) && (kind(st) != unix.S_IFREG || !ours(src.fd, st)) {
		return fail(exitOutside, "%s was replaced by another account", from)
	}
	t.like(tp)
	dst, b, err := t.parent(to, true)
	if err != nil {
		return err
	}
	defer dst.close()
	taken := fail(exitTaken, "%s is already there and the plan did not list it", to)
	there, err := statAt(dst.fd, b)
	over := err == nil
	if over && (!replace || kind(there) != unix.S_IFREG) {
		return taken
	}
	beforeUse("land")
	if over {
		err = unix.Renameat(src.fd, a, dst.fd, b)
	} else {
		err = renameNew(src.fd, a, dst.fd, b, st)
	}
	if errors.Is(err, unix.EXDEV) {
		// Another file system: copied beside the name first.
		err = t.copyIn(src.fd, a, from, dst.fd, b, st, over)
	}
	switch {
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY), errors.Is(err, unix.EISDIR), errors.Is(err, unix.ENOTDIR):
		return taken
	case err != nil:
		var f *failure
		if errors.As(err, &f) {
			return err
		}
		return fail(exitFailed, "%s could not take its name: %s", to, said(err))
	}
	// What took the name is what was looked at, or it is not called landed.
	now, err := statAt(dst.fd, b)
	if err != nil || kind(now) != kind(st) || now.Dev == st.Dev && now.Ino != st.Ino && !ours(dst.fd, now) {
		return fail(exitFailed, "%s was changed by another account as it landed", to)
	}
	if t.closed(dst.fd) && kind(now) != unix.S_IFLNK {
		chmodAt(dst.fd, b, now, now.Mode&0o7700)
	}
	return nil
}

// chmodAt changes the mode of what st describes, and of nothing else that
// may have its name by now.
func chmodAt(at int, name string, st unix.Stat_t, mode uint32) {
	if st.Mode&0o7777 == mode {
		return
	}
	fd, err := unix.Openat(at, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	if now, err := statOf(fd); err != nil || now.Ino != st.Ino || now.Dev != st.Dev {
		return
	}
	_ = unix.Chmod(procPath(fd), mode)
}

// renameNew renames over nothing. Where the file system has no such rename,
// a file is linked to its name, which fails when the name is taken, and
// anything else is renamed once the name is seen to be free.
func renameNew(from int, a string, to int, b string, st unix.Stat_t) error {
	err := unix.Renameat2(from, a, to, b, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	if kind(st) == unix.S_IFREG {
		if err := unix.Linkat(from, a, to, b, 0); err == nil {
			return unix.Unlinkat(from, a, 0)
		} else if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.EXDEV) {
			return err
		}
	}
	if _, err := statAt(to, b); err == nil {
		return unix.EEXIST
	}
	return unix.Renameat(from, a, to, b)
}

// copyIn copies an entry from another file system beside its new name,
// renames it, then removes the original.
func (t *tree) copyIn(from int, a, rel string, to int, b string, st unix.Stat_t, over bool) error {
	tmp := tempName()
	if err := copyEntry(from, a, rel, to, tmp); err != nil {
		rmAt(to, tmp)
		return err
	}
	made, err := statAt(to, tmp)
	if err == nil {
		if over {
			err = unix.Renameat(to, tmp, to, b)
		} else {
			err = renameNew(to, tmp, to, b, made)
		}
	}
	if err != nil {
		rmAt(to, tmp)
		return err
	}
	if err := rmAt(from, a); err != nil {
		return fail(exitFailed, "%s was copied, and the original could not be removed: %s", rel, said(err))
	}
	return nil
}

// copyEntry copies a file, a link as a link, or a folder with what it
// holds, to a new name in another open folder.
func copyEntry(from int, a, rel string, to int, b string) error {
	st, err := statAt(from, a)
	if err != nil {
		return fail(exitMissing, "%s is not there", rel)
	}
	switch kind(st) {
	case unix.S_IFREG:
		src, err := openPlain(from, a, rel, unix.O_RDONLY)
		if err != nil {
			return err
		}
		defer src.Close()
		f, err := create(to, b, rel, 0o666)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, src)
		if err == nil && st.Mode&0o100 != 0 {
			err = finishFile(f, "", true)
		}
		if err == nil {
			tv := unix.NsecToTimeval(st.Mtim.Nano())
			_ = unix.Futimes(int(f.Fd()), []unix.Timeval{tv, tv})
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		return nil
	case unix.S_IFLNK:
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlinkat(from, a, buf)
		if err == nil {
			err = unix.Symlinkat(string(buf[:n]), to, b)
		}
		if err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		return nil
	case unix.S_IFDIR:
		src, err := openDir(from, a, unix.O_NOFOLLOW)
		if err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		defer unix.Close(src)
		if err := unix.Mkdirat(to, b, 0o777); err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		dst, err := openDir(to, b, unix.O_NOFOLLOW)
		if err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		defer unix.Close(dst)
		names, err := namesIn(src)
		if err != nil {
			return fail(exitFailed, "%s could not be copied: %s", rel, said(err))
		}
		for _, n := range names {
			if err := copyEntry(src, n, rel+"/"+n, dst, n); err != nil {
				return err
			}
		}
		return nil
	}
	return fail(exitNotPlain, "%s is not a file, a folder or a link", rel)
}

// namesIn is the names in an open folder.
func namesIn(fd int) ([]string, error) {
	n, err := retry(func() (int, error) {
		return unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(n), ".")
	defer f.Close()
	return f.Readdirnames(-1)
}

// remove removes a plain file, or a folder that is empty, which it does not
// answer; what is not there, or not that, stays.
func (t *tree) remove(rel string, file bool) (bool, error) {
	d, name, err := t.parent(rel, false)
	var f *failure
	if errors.As(err, &f) && f.code == exitMissing {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer d.close()
	if !file {
		_ = unix.Unlinkat(d.fd, name, unix.AT_REMOVEDIR)
		return false, nil
	}
	st, err := statAt(d.fd, name)
	if err != nil || kind(st) != unix.S_IFREG {
		return false, nil
	}
	beforeUse("remove")
	if err := unix.Unlinkat(d.fd, name, 0); err != nil {
		return false, fail(exitFailed, "%s could not be removed: %v", rel, err)
	}
	return true, nil
}

// removeStaging removes a staging folder of this account's with all in it.
func (t *tree) removeStaging(rel string) error {
	p, name, err := split(rel)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(name, stagePrefix) {
		return fail(exitNotPlain, "not a staging folder")
	}
	d, err := t.open(p, rel, false)
	var f *failure
	if errors.As(err, &f) && f.code == exitMissing {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.close()
	st, err := statAt(d.fd, name)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil
	case err != nil:
		return fail(exitFailed, "%s could not be removed: %v", rel, err)
	case kind(st) == unix.S_IFLNK:
		return fail(exitLink, "%s is a link", rel)
	case !ours(d.fd, st):
		return fail(exitOutside, "%s was replaced by another account", rel)
	}
	beforeUse("rmstage")
	if err := rmAt(d.fd, name); err != nil {
		return fail(exitFailed, "%s could not be removed: %s", rel, said(err))
	}
	return nil
}

// removeTree removes an entry: a folder with all in it, and a link itself.
func (t *tree) removeTree(rel string) (bool, error) {
	d, name, err := t.parent(rel, false)
	var f *failure
	if errors.As(err, &f) && f.code == exitMissing {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer d.close()
	if _, err := statAt(d.fd, name); err != nil {
		return false, nil
	}
	beforeUse("rmtree")
	if err := rmAt(d.fd, name); err != nil {
		return false, fail(exitFailed, "%s could not be removed: %s", rel, said(err))
	}
	return true, nil
}

// rmAt removes an entry of an open folder, one that is a folder by what it
// holds, reached through its own descriptor.
func rmAt(at int, name string) error {
	err := unix.Unlinkat(at, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	beforeUse("rm")
	fd, derr := openDir(at, name, unix.O_NOFOLLOW)
	if derr != nil {
		return err
	}
	defer unix.Close(fd)
	beforeUse("rm")
	for range 8 {
		names, err := namesIn(fd)
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := rmAt(fd, n); err != nil {
				return err
			}
		}
		err = unix.Unlinkat(at, name, unix.AT_REMOVEDIR)
		if !errors.Is(err, unix.ENOTEMPTY) && !errors.Is(err, unix.EEXIST) {
			if errors.Is(err, unix.ENOENT) {
				return nil
			}
			return err
		}
	}
	return unix.ENOTEMPTY
}

// in runs a program with a staging folder held open as InDir, which is
// where the program writes: that path leads to the folder held here,
// wherever its name leads by now. (A program given the folder as its working
// folder would not do: rsync reads that folder's path, and changes to it by
// the path.) The folder must be this account's, and closed, so that nothing
// under it is another account's doing either.
func (t *tree) in(rel string, argv []string) error {
	p, name, err := split(rel)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(name, stagePrefix) || len(argv) == 0 {
		return fail(exitUsage, "usage: in STAGING-FOLDER PROGRAM [arguments]")
	}
	at, err := t.open(p, rel, false)
	if err != nil {
		return err
	}
	beforeUse("in")
	d, err := t.step(at.fd, name, rel, false)
	if err != nil {
		return err
	}
	st, err := statOf(d)
	if err != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0o077 != 0 {
		return fail(ExitOpen, "%s is not closed to other accounts", rel)
	}
	prog := lookPath(argv[0])
	if prog == "" {
		return fail(127, "%s was not found", argv[0])
	}
	if err := unix.Chdir("/"); err != nil {
		return fail(exitCannot, "%v", err)
	}
	// Nothing of this process is needed past here: whatever has the number
	// gives it up.
	if d != inFD {
		if err := unix.Dup2(d, inFD); err != nil {
			return fail(exitCannot, "%s: the folder cannot be opened: %v", rel, err)
		}
	} else if _, err := unix.FcntlInt(inFD, unix.F_SETFD, 0); err != nil {
		return fail(exitCannot, "%s: the folder cannot be opened: %v", rel, err)
	}
	if now, err := os.Stat(InDir); err != nil || !now.IsDir() {
		return fail(ExitOpen, "%s cannot be reached through /proc", rel)
	}
	err = unix.Exec(prog, argv, os.Environ())
	return fail(126, "%s could not be run: %v", argv[0], err)
}
