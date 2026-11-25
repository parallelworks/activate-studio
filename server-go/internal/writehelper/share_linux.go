package writehelper

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Sharing a folder of a library (userlibs' share script, on descriptors):
// its owner gives an account, a group or every account access to it, or
// takes it back. Only what this account owns is changed, and only an object
// held by a descriptor, so a link put in the folder is never followed.

// grant is who a share is for. tag and id are its ACL entry; none for every
// account.
type grant struct {
	everyone bool
	tag      uint16
	id       uint32
	name     string
	write    bool
	groups   map[uint32]bool
	me       uint32
	// loose: other accounts can write in the folder, so a file with another
	// name may be one they put there of the owner's elsewhere.
	loose bool
}

// share is the share and unshare commands, with the arguments KIND (user,
// group or everyone), ID (the account's or group's number), NAME, r or w,
// the folder in the library, the numbers of the account's groups, spaces
// between, and 1 where other accounts can write in the folder. unshare reads the folders above to close again, one a
// line.
func (t *tree) share(back bool, args []string, in *bufio.Reader, out io.Writer) error {
	if len(args) < 6 {
		return fail(exitUsage, "usage: share KIND ID NAME r|w FOLDER GROUPS")
	}
	g := grant{name: args[2], write: args[3] == "w", me: uint32(os.Geteuid()), groups: map[uint32]bool{}}
	switch args[0] {
	case "user":
		g.tag = tagUser
	case "group":
		g.tag = tagGroup
	case "everyone":
		g.everyone = true
	default:
		return fail(exitUsage, "unknown kind")
	}
	g.loose = len(args) > 6 && args[6] == "1"
	if !g.everyone {
		id, err := strconv.ParseUint(args[1], 10, 32)
		if err != nil {
			return fail(exitUsage, "unknown account")
		}
		g.id = uint32(id)
	}
	for _, n := range strings.Fields(args[5]) {
		if id, err := strconv.ParseUint(n, 10, 32); err == nil {
			g.groups[uint32(id)] = true
		}
	}
	p, err := parts(args[4])
	if err != nil {
		return err
	}
	d, err := t.open(p, args[4], false)
	if err != nil {
		return fail(exitMissing, "%s is not a folder.", path.Join(t.root, args[4]))
	}
	defer d.close()
	st, err := statOf(d.fd)
	if err != nil {
		return fail(exitMissing, "%s cannot be opened.", path.Join(t.root, args[4]))
	}
	folder := nameOf(d.fd, path.Join(t.root, args[4]))
	if st.Uid != g.me {
		return fail(exitLink, "%s belongs to another account, so it cannot be shared from here.", folder)
	}
	if back {
		if err := g.each(d.fd, func(fd int, st unix.Stat_t) error { return g.takeBack(fd, st) }); err != nil {
			if g.everyone {
				return fail(exitTaken, "%s could not be closed to other accounts.", folder)
			}
			return err
		}
		closing := map[string]bool{}
		for {
			line, err := in.ReadString('\n')
			if l := strings.TrimSuffix(line, "\n"); l != "" {
				closing[l] = true
			}
			if err != nil {
				break
			}
		}
		if err := above(d.fd, folder, func(fd int, name string, st unix.Stat_t) error {
			if closing[name] && st.Uid == g.me {
				g.closeWay(fd, st)
			}
			return nil
		}); err != nil {
			return err
		}
		fmt.Fprintln(out, "revoked")
		return nil
	}
	if err := g.each(d.fd, func(fd int, st unix.Stat_t) error { return g.give(fd, st) }); err != nil {
		switch {
		case g.everyone:
			return fail(exitTaken, "%s could not be opened to every account.", folder)
		case errors.Is(err, errDefault):
			return fail(exitTaken, "The default ACL could not be set on %s.", folder)
		}
		return fail(exitTaken, "The ACL could not be set on %s.", folder)
	}
	if err := above(d.fd, folder, func(fd int, name string, st unix.Stat_t) error {
		opened, err := g.openWay(fd, name, st)
		if opened {
			fmt.Fprintf(out, "opened=%s\n", name)
		}
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintln(out, "shared")
	return nil
}

// nameOf is the path of the folder a descriptor holds.
func nameOf(fd int, otherwise string) string {
	if p, err := os.Readlink(procPath(fd)); err == nil && strings.HasPrefix(p, "/") {
		return p
	}
	return otherwise
}

// each runs fn on the folder and all in it that this account owns, links
// left out.
func (g *grant) each(fd int, fn func(fd int, st unix.Stat_t) error) error {
	st, err := statOf(fd)
	if err != nil {
		return err
	}
	if st.Uid == g.me {
		if err := fn(fd, st); err != nil {
			return err
		}
	}
	if kind(st) != unix.S_IFDIR {
		return nil
	}
	names, err := namesIn(fd)
	if err != nil {
		return nil
	}
	for _, n := range names {
		beforeUse("share")
		c, err := unix.Openat(fd, n, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		if st, serr := statOf(c); serr == nil {
			switch kind(st) {
			case unix.S_IFLNK:
			case unix.S_IFDIR:
				err = g.each(c, fn)
			default:
				if st.Uid == g.me && (!g.loose || st.Nlink < 2) {
					err = fn(c, st)
				}
			}
		}
		unix.Close(c)
		if err != nil {
			return err
		}
	}
	return nil
}

var errDefault = errors.New("the default ACL could not be set")

// give gives the share's access to one object.
func (g *grant) give(fd int, st unix.Stat_t) error {
	isDir := kind(st) == unix.S_IFDIR
	if g.everyone {
		m := st.Mode&0o7777 | 0o004
		if isDir || st.Mode&0o111 != 0 {
			m |= 0o001
		}
		if m == st.Mode&0o7777 {
			return nil
		}
		return unix.Chmod(procPath(fd), m)
	}
	perm := uint16(4)
	if g.write {
		perm |= 2
	}
	a, _, err := readACL(fd, aclAccess)
	if err != nil {
		return err
	}
	// Where an ACL already narrows the owning group, it keeps what it has.
	a.pin()
	p := perm
	if isDir || a.runs() {
		p |= 1
	}
	a.set(g.tag, g.id, p)
	a.remask()
	if err := writeACL(fd, aclAccess, a); err != nil {
		return err
	}
	if !isDir {
		return nil
	}
	d, has, err := readACL(fd, aclDefault)
	if err != nil {
		return errDefault
	}
	if !has {
		for _, e := range a {
			if e.tag == tagUserObj || e.tag == tagGroupObj || e.tag == tagOther {
				d = append(d, e)
			}
		}
	}
	d.set(g.tag, g.id, perm|1)
	if g.write {
		// What a recipient writes there stays open to the owner.
		d.set(tagUser, g.me, 7)
	}
	d.remask()
	if err := writeACL(fd, aclDefault, d); err != nil {
		return errDefault
	}
	return nil
}

// takeBack takes the share's access from one object.
func (g *grant) takeBack(fd int, st unix.Stat_t) error {
	if g.everyone {
		if st.Mode&0o007 == 0 {
			return nil
		}
		return unix.Chmod(procPath(fd), st.Mode&0o7770)
	}
	if a, has, err := readACL(fd, aclAccess); err == nil && has && a.drop(g.tag, g.id) {
		a.remask()
		_ = writeACL(fd, aclAccess, a)
	}
	if kind(st) == unix.S_IFDIR {
		if d, has, err := readACL(fd, aclDefault); err == nil && has && d.drop(g.tag, g.id) {
			// The owner's own entry came with a share for writing, and goes
			// with the last of them.
			if rest := slices.Clone(d); rest.drop(tagUser, g.me) && !rest.named() {
				d = rest
			}
			d.remask()
			_ = writeACL(fd, aclDefault, d)
		}
	}
	tidy(fd)
	return nil
}

// tidy takes off an ACL left with base entries only, so a file or folder is
// as it was before any share: a default ACL with no named entry, which
// would set what new files get in place of the umask, and a mask that is
// the only further entry and equals the owning group's.
func tidy(fd int) {
	if d, has, err := readACL(fd, aclDefault); err == nil && has {
		if d.named() {
			return
		}
		_ = dropACL(fd, aclDefault)
	}
	a, has, err := readACL(fd, aclAccess)
	if err != nil || !has || a.named() {
		return
	}
	m, masked := a.get(tagMask, noID)
	if g, ok := a.get(tagGroupObj, noID); masked && ok && g == m {
		_ = dropACL(fd, aclAccess)
	}
}

// above runs fn on each folder above the one held, nearest first, reached
// from it by its parent.
func above(fd int, name string, fn func(fd int, name string, st unix.Stat_t) error) error {
	at, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return fail(exitCannot, "%s: %v", name, err)
	}
	st, err := statOf(at)
	for err == nil {
		up, oerr := unix.Openat(at, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		unix.Close(at)
		if oerr != nil {
			return fail(exitCannot, "the folder above %s could not be opened: %v", name, oerr)
		}
		at = up
		was := st
		if st, err = statOf(at); err != nil || st.Ino == was.Ino && st.Dev == was.Dev {
			break
		}
		name = path.Dir(name)
		if err := fn(at, name, st); err != nil {
			unix.Close(at)
			return err
		}
	}
	unix.Close(at)
	return nil
}

// openWay opens a folder above the shared one for passing through, and
// answers whether the recipient had no entry there before.
func (g *grant) openWay(fd int, name string, st unix.Stat_t) (bool, error) {
	if st.Uid != g.me {
		if st.Mode&0o001 != 0 || st.Mode&0o010 != 0 && (g.tag == tagGroup && st.Gid == g.id || g.groups[st.Gid]) {
			return false, nil
		}
		return false, fail(11, "%s belongs to another account and is closed to others, so the folder cannot be reached through it.", name)
	}
	if g.everyone {
		if st.Mode&0o001 != 0 {
			return false, nil
		}
		if err := unix.Chmod(procPath(fd), st.Mode&0o7777|0o001); err != nil {
			return false, fail(exitTaken, "%s could not be opened to every account.", name)
		}
		return true, nil
	}
	a, _, err := readACL(fd, aclAccess)
	if err != nil {
		return false, fail(exitTaken, "%s could not be opened to %s.", name, g.name)
	}
	cur, has := a.get(g.tag, g.id)
	if has && cur&1 != 0 {
		return false, nil
	}
	a.pin()
	a.set(g.tag, g.id, cur|1)
	a.remask()
	if err := writeACL(fd, aclAccess, a); err != nil {
		return false, fail(exitTaken, "%s could not be opened to %s.", name, g.name)
	}
	return !has, nil
}

// closeWay closes a folder a share opened for passing through.
func (g *grant) closeWay(fd int, st unix.Stat_t) {
	if g.everyone {
		_ = unix.Chmod(procPath(fd), st.Mode&0o7776)
		return
	}
	a, has, err := readACL(fd, aclAccess)
	if err != nil || !has {
		return
	}
	if p, ok := a.get(g.tag, g.id); ok && p == 1 {
		a.drop(g.tag, g.id)
		a.remask()
		_ = writeACL(fd, aclAccess, a)
		tidy(fd)
	}
}
