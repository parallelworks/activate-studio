package libraries

import "github.com/parallelworks/activate-studio/server-go/internal/remote"

// Access is what a library is, in the terms Add a library asks them in:
// where it is, whose account reads it, whether the viewer may write in it,
// and whether the Studio built its index or connected one that was there.
// It is worked out from the library's record each time it is listed, so
// libraries added before it, and library lists exported from them, read
// the same as ones added now; nothing is stored or migrated.
type Access struct {
	// Where: "here" (this computer), "system" (a cluster or host reached
	// over pw ssh, ssh or the platform's workflow) or "bucket".
	Where string `json:"where"`
	// System names the cluster or host, for a library there.
	System string `json:"system,omitempty"`
	// Via: "pw", "ssh" or "workflow", for a library on a system.
	Via string `json:"via,omitempty"`
	// ReadAs: "you", the viewer's own sign-in or ssh keys, or "studio",
	// the account the Studio runs as, which the deployment's libraries
	// are read with.
	ReadAs string `json:"readAs"`
	// Write: the viewer may change its files from the Library.
	Write bool `json:"write"`
	// SharedFrom: the account that shared the folder with the viewer.
	SharedFrom string `json:"sharedFrom,omitempty"`
	// Index: "built" by the Studio, or "connected" as it was.
	Index string `json:"index"`
}

// userAccess is the access of a library a person added on one of their
// systems: read as them, always.
func userAccess(d UserDef, l Library) Access { return UserAccess(d, l.Writable) }

// UserAccess is the access of d, written as its owner when writable.
func UserAccess(d UserDef, writable bool) Access {
	a := Access{Where: "system", ReadAs: "you", Write: writable, Index: "connected", Via: "pw"}
	if d.Shared != nil {
		a.SharedFrom = d.Shared.From
	}
	switch {
	case d.IsSSH():
		a.Via = "ssh"
		if d.Destination != nil {
			a.System = *d.Destination
		}
	case d.Run != nil:
		a.Via = "workflow"
	}
	if a.System == "" {
		a.System = remote.ShortName(d.Site)
	}
	if d.Build != nil || d.Run != nil || d.Space != nil || d.Shared != nil {
		a.Index = "built"
	}
	return a
}

// localAccess is the access of a library on this computer or in a bucket:
// read with the Studio's own account.
func localAccess(l Library) Access {
	a := Access{Where: "here", ReadAs: "studio", Write: l.Writable, Index: "connected"}
	if l.Bucket != nil {
		a.Where = "bucket"
	}
	if l.Primary || l.Build != nil || l.CopyOf != nil || l.Bucket != nil {
		a.Index = "built"
	}
	return a
}
