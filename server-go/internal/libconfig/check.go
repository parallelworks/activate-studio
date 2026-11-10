package libconfig

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// Offer is what the viewer may add here, as the Add a library forms offer
// it.
type Offer struct {
	// Auth: the Studio signs people in, so libraries on this computer are
	// the deployment's, for everyone.
	Auth bool
	// Build: folders on this computer can be indexed (sign-in off).
	Build bool
	// SSH: libraries over ssh (the one user of a Studio with sign-in off).
	SSH bool
	// PwWhy is why pw ssh cannot be used now; "" when it can.
	PwWhy string
}

// The states of an entry in the preview.
const (
	StateNew         = "new"         // can be added
	StatePresent     = "present"     // already a library here
	StateInvalid     = "invalid"     // the entry cannot be read
	StateUnavailable = "unavailable" // the viewer may not add it here
)

// Checked is one entry as the preview shows it.
type Checked struct {
	Index int    `json:"index"`
	State string `json:"state"`
	// Why is the reason for invalid and unavailable; for present, the
	// library it matches.
	Why string `json:"why,omitempty"`
	// Where it points, in one line.
	Where string `json:"where"`
	Entry *Entry `json:"entry"`
	// User and Host split an ssh entry's destination, for the user name
	// the preview lets each person set.
	User *string `json:"user,omitempty"`
	Host string  `json:"host,omitempty"`
}

// Existing is what is already a library here, for matching.
type Existing struct {
	Libraries []libraries.Library
	Users     []libraries.UserDef
}

// Where is an entry's location in one line.
func Where(e *Entry) string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case KindIndex:
		return e.IndexRoot
	case KindFolder:
		return e.SourceRoot
	case KindSSH:
		h := e.Destination
		if e.Port != 0 {
			h += " port " + itoa(e.Port)
		}
		return h + ":" + remoteMain(e)
	case KindPw:
		return e.Cluster + ":" + remoteMain(e)
	}
	return ""
}

func remoteMain(e *Entry) string {
	if e.Mode == ModeBuild {
		return e.SourceRoot
	}
	return e.IndexRoot
}

func itoa(n int) string { return strconv.Itoa(n) }

// samePath: the same path, or a path under ~ in the file that ends a path
// already recorded (the connection test stores ~ resolved).
func samePath(file, stored string) bool {
	if file == "" || stored == "" {
		return false
	}
	file, stored = strings.TrimRight(file, "/"), strings.TrimRight(stored, "/")
	if file == stored {
		return true
	}
	if rest, ok := strings.CutPrefix(file, "~/"); ok {
		return strings.HasSuffix(stored, "/"+rest)
	}
	return false
}

func localAbs(p string, home string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok && home != "" && (rest == "" || rest[0] == '/') {
		p = home + rest
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Check is the preview of entries: each one new, already here, unreadable,
// or not one the viewer may add, with the reason. home resolves ~ in paths
// on this computer. A later entry the same as an earlier one is shown as
// present.
func Check(parsed []Parsed, have Existing, offer Offer, home string) []Checked {
	out := make([]Checked, len(parsed))
	for i, p := range parsed {
		c := Checked{Index: i, Entry: p.Entry, Where: Where(p.Entry)}
		if p.Entry != nil && p.Entry.Kind == KindSSH && p.Entry.Destination != "" {
			u, h := UserOf(p.Entry.Destination)
			c.Host = h
			c.User = &u
		}
		switch {
		case p.Error != "":
			c.State, c.Why = StateInvalid, p.Error
		default:
			if why := unavailable(p.Entry, offer); why != "" {
				c.State, c.Why = StateUnavailable, why
			} else if label := present(p.Entry, have, home); label != "" {
				c.State, c.Why = StatePresent, label
			} else if j := earlier(parsed[:i], p.Entry, home); j >= 0 {
				c.State, c.Why = StatePresent, "the same as entry "+itoa(j+1)+" in this file"
			} else {
				c.State = StateNew
			}
		}
		out[i] = c
	}
	return out
}

func unavailable(e *Entry, o Offer) string {
	switch e.Kind {
	case KindIndex:
		if o.Auth {
			return "On a Studio that signs people in, indexes on its own computer are the deployment's, added by whoever runs it."
		}
	case KindFolder:
		if o.Auth || !o.Build {
			return "Folders on this computer are indexed only on a Studio with sign-in off."
		}
	case KindSSH:
		if !o.SSH {
			return libraries.SSHSingleUser
		}
	case KindPw:
		if o.PwWhy != "" {
			return o.PwWhy
		}
	}
	return ""
}

func hostOf(d libraries.UserDef) string {
	dest := d.Site
	if d.Destination != nil && *d.Destination != "" {
		dest = *d.Destination
	}
	return WithoutUser(dest)
}

func port(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// present is the label of the library an entry is already, or "".
func present(e *Entry, have Existing, home string) string {
	switch e.Kind {
	case KindIndex:
		want := localAbs(e.IndexRoot, home)
		for _, l := range have.Libraries {
			if !l.Remote() && l.Build == nil && !l.Primary && filepath.Clean(l.IndexRoot) == want {
				return l.Label
			}
		}
	case KindFolder:
		want := localAbs(e.SourceRoot, home)
		for _, l := range have.Libraries {
			if !l.Remote() && (l.Build != nil || l.Primary) && l.SourceRoot != "" && filepath.Clean(l.SourceRoot) == want {
				return l.Label
			}
		}
	case KindSSH, KindPw:
		for _, d := range have.Users {
			if (e.Kind == KindSSH) != d.IsSSH() {
				continue
			}
			if e.Kind == KindSSH && (hostOf(d) != WithoutUser(e.Destination) || port(d.Port) != e.Port) {
				continue
			}
			if e.Kind == KindPw && d.Site != e.Cluster {
				continue
			}
			if e.Mode == ModeBuild {
				if d.Build != nil && samePath(e.SourceRoot, d.Build.SourceRoot) {
					return d.Label
				}
				continue
			}
			if d.Build == nil && samePath(e.IndexRoot, deref(d.IndexRoot)) {
				return d.Label
			}
		}
	}
	return ""
}

// earlier is the index of an entry before this one that adds the same
// library, or -1.
func earlier(before []Parsed, e *Entry, home string) int {
	key := func(x *Entry) string {
		switch x.Kind {
		case KindIndex:
			return "i\x00" + localAbs(x.IndexRoot, home)
		case KindFolder:
			return "f\x00" + localAbs(x.SourceRoot, home)
		case KindSSH:
			return "s\x00" + WithoutUser(x.Destination) + "\x00" + itoa(x.Port) + "\x00" + x.Mode + "\x00" + remoteMain(x)
		case KindPw:
			return "p\x00" + x.Cluster + "\x00" + x.Mode + "\x00" + remoteMain(x)
		}
		return ""
	}
	k := key(e)
	for j, p := range before {
		if p.Error == "" && p.Entry != nil && key(p.Entry) == k {
			return j
		}
	}
	return -1
}
