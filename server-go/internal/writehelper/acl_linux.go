package writehelper

import (
	"encoding/binary"
	"errors"
	"sort"

	"golang.org/x/sys/unix"
)

// POSIX ACLs, as the kernel keeps them in a file's extended attributes, so
// they are read and changed on the object a descriptor holds.

const (
	aclAccess  = "system.posix_acl_access"
	aclDefault = "system.posix_acl_default"

	tagUserObj  = 0x01
	tagUser     = 0x02
	tagGroupObj = 0x04
	tagGroup    = 0x08
	tagMask     = 0x10
	tagOther    = 0x20

	noID = 0xffffffff
)

type aclEntry struct {
	tag, perm uint16
	id        uint32
}

type acl []aclEntry

func decodeACL(b []byte) (acl, error) {
	if len(b) < 4 || binary.LittleEndian.Uint32(b) != 2 || (len(b)-4)%8 != 0 {
		return nil, unix.EINVAL
	}
	var out acl
	for b = b[4:]; len(b) > 0; b = b[8:] {
		out = append(out, aclEntry{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:]), binary.LittleEndian.Uint32(b[4:])})
	}
	return out, nil
}

func (a acl) encode() []byte {
	s := append(acl{}, a...)
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].tag != s[j].tag {
			return s[i].tag < s[j].tag
		}
		return s[i].id < s[j].id
	})
	b := binary.LittleEndian.AppendUint32(nil, 2)
	for _, e := range s {
		b = binary.LittleEndian.AppendUint16(b, e.tag)
		b = binary.LittleEndian.AppendUint16(b, e.perm)
		b = binary.LittleEndian.AppendUint32(b, e.id)
	}
	return b
}

// get is an entry's permissions, and whether there is one.
func (a acl) get(tag uint16, id uint32) (uint16, bool) {
	for _, e := range a {
		if e.tag == tag && e.id == id {
			return e.perm, true
		}
	}
	return 0, false
}

func (a *acl) set(tag uint16, id uint32, perm uint16) {
	for i, e := range *a {
		if e.tag == tag && e.id == id {
			(*a)[i].perm = perm
			return
		}
	}
	*a = append(*a, aclEntry{tag, perm, id})
}

func (a *acl) drop(tag uint16, id uint32) bool {
	for i, e := range *a {
		if e.tag == tag && e.id == id {
			*a = append((*a)[:i:i], (*a)[i+1:]...)
			return true
		}
	}
	return false
}

// named is whether an account or group other than the owner's is in it.
func (a acl) named() bool {
	for _, e := range a {
		if e.tag == tagUser || e.tag == tagGroup {
			return true
		}
	}
	return false
}

// remask sets the mask to what the entries it limits are given, as setfacl
// does after each change.
func (a *acl) remask() {
	if _, has := a.get(tagMask, noID); !has && !a.named() {
		return
	}
	var m uint16
	for _, e := range *a {
		if e.tag == tagUser || e.tag == tagGroupObj || e.tag == tagGroup {
			m |= e.perm
		}
	}
	a.set(tagMask, noID, m)
}

// pin keeps the owning group at what the mask leaves it now, so that a mask
// set again does not widen it.
func (a *acl) pin() {
	m, ok := a.get(tagMask, noID)
	if !ok {
		return
	}
	if g, ok := a.get(tagGroupObj, noID); ok && g&m != g {
		a.set(tagGroupObj, noID, g&m)
	}
}

func (a acl) runs() bool {
	for _, e := range a {
		if e.perm&1 != 0 {
			return true
		}
	}
	return false
}

// readACL is the ACL of the object a descriptor holds; ok false where it
// has none. The access ACL of one without is its mode.
func readACL(fd int, name string) (acl, bool, error) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Getxattr(procPath(fd), name, buf)
		switch {
		case err == nil:
			a, err := decodeACL(buf[:n])
			return a, err == nil, err
		case errors.Is(err, unix.ERANGE) && len(buf) < 1<<20:
			buf = make([]byte, 4*len(buf))
			continue
		case !errors.Is(err, unix.ENODATA):
			return nil, false, err
		}
		if name != aclAccess {
			return nil, false, nil
		}
		st, err := statOf(fd)
		if err != nil {
			return nil, false, err
		}
		m := uint16(st.Mode)
		return acl{{tagUserObj, m >> 6 & 7, noID}, {tagGroupObj, m >> 3 & 7, noID}, {tagOther, m & 7, noID}}, false, nil
	}
}

func writeACL(fd int, name string, a acl) error {
	return unix.Setxattr(procPath(fd), name, a.encode(), 0)
}

func dropACL(fd int, name string) error {
	err := unix.Removexattr(procPath(fd), name)
	if errors.Is(err, unix.ENODATA) {
		return nil
	}
	return err
}
