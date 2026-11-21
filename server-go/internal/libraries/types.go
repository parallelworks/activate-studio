// Package libraries is the library registry of the Studio (libraries.ts,
// libraryScope.ts, omissions.ts and the stored side of userLibraries.ts):
// the main folder's library, the deployment's pinned ones
// (STUDIO_LIBRARIES), those added in Settings (INDEX_BASE/libraries.json),
// and each viewer's own on other systems (INDEX_BASE/users/<hash>/
// libraries.json); what each tree supports (INDEX_BASE/library-caps.json);
// and which libraries a request's scope means.
//
// Every file is the one Node reads and writes, in the same format, and is
// read again when it changes on disk, so Node and Go stay in step while
// both run.
package libraries

import (
	"encoding/json"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Caps is what a tree supports (LibraryCaps).
type Caps struct {
	Index    bool `json:"index"`
	FullText bool `json:"fullText"`
	Vectors  bool `json:"vectors"`
}

// LocalBuild is an index the Studio builds of a folder on this computer.
type LocalBuild struct {
	IndexDir  string  `json:"indexDir"`
	CacheRoot string  `json:"cacheRoot"`
	Managed   bool    `json:"managed"`
	State     string  `json:"state"` // building, ready, updating, failed
	Error     *string `json:"error"`
	BuiltAt   *string `json:"builtAt"`
	Files     *int64  `json:"files"`
	Dirs      *int64  `json:"dirs"`
	// Pending is "depth" while a change of depth is applied. Node's
	// updateBuild adds the key last and leaves it null once cleared, so
	// HasPending says whether the stored entry has the key.
	Pending    *string `json:"pending"`
	HasPending bool    `json:"-"`
}

type localBuildJSON struct {
	IndexDir  string  `json:"indexDir"`
	CacheRoot string  `json:"cacheRoot"`
	Managed   bool    `json:"managed"`
	State     string  `json:"state"`
	Error     *string `json:"error"`
	BuiltAt   *string `json:"builtAt"`
	Files     *int64  `json:"files"`
	Dirs      *int64  `json:"dirs"`
}

// MarshalJSON writes the keys in the order Node's entries have them, with
// pending last and only where the entry has it.
func (b LocalBuild) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(localBuildJSON{b.IndexDir, b.CacheRoot, b.Managed, b.State, b.Error, b.BuiltAt, b.Files, b.Dirs})
	if err != nil || !(b.HasPending || b.Pending != nil) {
		return base, err
	}
	p, err := json.Marshal(b.Pending)
	if err != nil {
		return nil, err
	}
	return append(append(base[:len(base)-1], `,"pending":`...), append(p, '}')...), nil
}

// UnmarshalJSON notes whether the entry has a pending key.
func (b *LocalBuild) UnmarshalJSON(data []byte) error {
	var j localBuildJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	json.Unmarshal(data, &keys)
	*b = LocalBuild{IndexDir: j.IndexDir, CacheRoot: j.CacheRoot, Managed: j.Managed, State: j.State, Error: j.Error, BuiltAt: j.BuiltAt, Files: j.Files, Dirs: j.Dirs}
	if raw, ok := keys["pending"]; ok {
		b.HasPending = true
		var s *string
		if json.Unmarshal(raw, &s) == nil {
			b.Pending = s
		}
	}
	return nil
}

// ReadThrough is another host over ssh that has a library's files.
type ReadThrough struct {
	Destination string   `json:"destination"`
	Port        *int     `json:"port"`
	SSHOptions  []string `json:"sshOptions,omitempty"`
	Root        *string  `json:"root"`
}

// CopyOf is where a copy library's files come from: a folder in a
// Transfers location (an SFTP or ssh host, an rclone remote, a cluster),
// which Studio copies into the library's own folder, builds like a folder
// on this computer, and keeps in step (docs/LIBRARIES.md, Copies of a
// location).
type CopyOf struct {
	// Location is the Transfers location id; Path the folder in it, "" for
	// its top.
	Location string `json:"location"`
	Path     string `json:"path"`
	// Label is the location's label when the library was added, for
	// messages once the location is renamed or gone.
	Label string `json:"label,omitempty"`
	// Every is how often Studio looks for changes, in minutes; 0 only when
	// asked.
	Every int `json:"every"`
}

// CopyEvery is the choices of CopyOf.Every, in minutes.
var CopyEvery = []int{0, 5, 15, 60, 360, 1440}

// DefaultCopyEvery is a new copy library's CopyOf.Every.
const DefaultCopyEvery = 15

// CopyState is what the last look at a copy library's location found
// (INDEX_BASE/libraries/<id>/copy.json).
type CopyState struct {
	// CheckedAt is when Studio last finished looking for changes.
	CheckedAt *string `json:"checkedAt"`
	// ChangedAt, Transfer, Files and Deleted are the last look that found
	// changes: when it ended, its transfer, the files it brought and the
	// files it removed.
	ChangedAt *string `json:"changedAt"`
	Transfer  *string `json:"transfer"`
	Files     int     `json:"files"`
	Deleted   int     `json:"deleted"`
	// Error is why the last look failed; nil when it did not.
	Error *string `json:"error"`
}

// BucketOf is where a bucket library's listing comes from: a folder (a
// bucket, or a prefix in one) of an rclone remote in Transfers. Studio
// indexes the listing alone and reads an object only when it is opened
// (docs/LIBRARIES.md, Buckets). The library has no folder on this host.
type BucketOf struct {
	// Location is the Transfers location id (rclone:<remote>); Path the
	// bucket and prefix in it.
	Location string `json:"location"`
	Path     string `json:"path"`
	// Label is the location's label when the library was added.
	Label string `json:"label,omitempty"`
	// Every is how often Studio lists it again, in minutes; 0 only when
	// asked.
	Every int `json:"every"`
	// Text is whether Studio reads the text of the objects after each
	// listing (docs/LIBRARIES.md, Reading text from a bucket): off until
	// asked, since each object read is a request and its bytes leave the
	// store.
	Text bool `json:"text"`
	// Fsid names the bucket's records in a spread tree; set at the first
	// text pass (libapi.BucketFsid).
	Fsid string `json:"fsid,omitempty"`
}

// BucketTextMax is the largest object a bucket library's text pass reads;
// a larger one is left unread.
const BucketTextMax = 100 << 20

// BucketText is what the last text pass of a bucket library did.
type BucketText struct {
	// At is when it ended.
	At string `json:"at"`
	// Read counts the objects it read, by outcome (text, empty, skipped,
	// changed, missing, failed); Bytes their size, Requests the GET
	// requests that took (one an object).
	Read     map[string]int64 `json:"read"`
	Bytes    int64            `json:"bytes"`
	Requests int64            `json:"requests"`
	Seconds  float64          `json:"seconds"`
	// Files is how many objects of the library have text now.
	Files int64 `json:"files"`
	// Rate is the bytes a second of the last pass that read at least a
	// megabyte, kept by passes that read less; 0 before one.
	Rate float64 `json:"rate"`
	// Missing says what was not read for want of a helper program.
	Missing []string `json:"missing"`
	// Stopped is set when it was stopped before the end; the next pass
	// goes on from there.
	Stopped bool `json:"stopped,omitempty"`
	// Error is why it failed; nil when it did not.
	Error *string `json:"error"`
}

// BucketEvery is the choices of BucketOf.Every, in minutes. Each listing
// is a request for every 1,000 objects, so the shortest is an hour.
var BucketEvery = []int{0, 60, 360, 1440}

// DefaultBucketEvery is a new bucket library's BucketOf.Every.
const DefaultBucketEvery = 1440

// BucketState is what the last listing of a bucket library found
// (INDEX_BASE/libraries/<id>/bucket.json).
type BucketState struct {
	// ListedAt is when the last listing that was indexed ended.
	ListedAt *string `json:"listedAt"`
	// Objects, Bytes and Folders are what it held; Skipped the entries the
	// index cannot hold (a path past GUFI's limit, a name "." or "..").
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
	Folders int64 `json:"folders"`
	Skipped int64 `json:"skipped"`
	// Requests estimates the list requests the listing took: an object
	// store answers up to 1,000 keys a request.
	Requests int64 `json:"requests"`
	// Provider is the s3 provider rclone's configuration names (AWS,
	// Minio, Ceph, ...), "" when it names none.
	Provider string `json:"provider"`
	// Seconds is how long the listing took.
	Seconds float64 `json:"seconds"`
	// Error is why the last listing failed; nil when it did not.
	Error *string `json:"error"`
	// TextPass is what the last text pass did; nil before the first.
	TextPass *BucketText `json:"textPass,omitempty"`
}

// Def is a library as stored (LibraryDef).
type Def struct {
	ID          string       `json:"id"`
	Label       string       `json:"label,omitempty"`
	IndexRoot   string       `json:"indexRoot"`
	SourceRoot  *string      `json:"sourceRoot"`
	Build       *LocalBuild  `json:"build,omitempty"`
	ReadThrough *ReadThrough `json:"readThrough,omitempty"`
	CopyOf      *CopyOf      `json:"copyOf,omitempty"`
	Bucket      *BucketOf    `json:"bucket,omitempty"`
}

// Library is a mounted library, as every module sees it.
type Library struct {
	ID        string
	Label     string
	IndexRoot string
	// SourceRoot is where the files are, when reachable from this host.
	SourceRoot string
	CacheRoot  string
	Build      *LocalBuild
	// Meaning and Depth are set for a library the Studio builds; "" for an
	// index it only reads.
	Meaning  string
	Depth    string
	Writable bool
	Primary  bool
	Pinned   bool
	Caps     Caps
	// VectorsUnknown: a library on another system whose vectors were never
	// looked for; Caps.Vectors false then means unknown.
	VectorsUnknown bool
	// Site is the system a user's own library lives on.
	Site        string
	Format      *FormatRecord
	ReadThrough *ReadThrough
	// CopyOf is set for a copy library: SourceRoot is its copy here.
	CopyOf *CopyOf
	// Bucket is set for a bucket library: its index is here, its objects
	// are read from the remote one at a time.
	Bucket *BucketOf
	// Space: written as the viewer (their own space, or a folder shared
	// with them for writing).
	Space bool
	// Mine: a library the viewer built on one of their systems, written
	// as them through the same path as a space (UserDef.Mine).
	Mine bool
	// SharedFrom is the account that shared the folder, for a library made
	// from someone else's share.
	SharedFrom string
}

// Remote reports whether the library is on another system.
func (l *Library) Remote() bool {
	return len(l.IndexRoot) >= len(RemotePrefix) && l.IndexRoot[:len(RemotePrefix)] == RemotePrefix
}

// BuildProgress is how far a build has got.
type BuildProgress struct {
	Phase   string  `json:"phase"`
	Done    int64   `json:"done"`
	Total   int64   `json:"total"`
	Current *string `json:"current"`
}

// PublicLibrary is the shape every viewer's config sees (publicLibrary).
type PublicLibrary struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Primary    bool    `json:"primary"`
	Writable   bool    `json:"writable"`
	Pinned     bool    `json:"pinned"`
	Source     bool    `json:"source"`
	Caps       Caps    `json:"caps"`
	Site       *string `json:"site"`
	Meaning    *string `json:"meaning"`
	Depth      *string `json:"depth"`
	FormatNote *string `json:"formatNote"`
	Space      bool    `json:"space,omitempty"`
	Mine       bool    `json:"mine,omitempty"`
	SharedFrom string  `json:"sharedFrom,omitempty"`
	// FromBucket: its files are objects read from a bucket one at a time.
	FromBucket bool `json:"fromBucket,omitempty"`
	// Host is the cluster a library in a folder on this server is on, by
	// name, where the Studio knows the cluster it runs beside, so it is
	// named as a library on a remote system is.
	Host *string `json:"host,omitempty"`
}

func ptr[T any](v T) *T { return &v }

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Public is publicLibrary.
func Public(l Library) PublicLibrary {
	return PublicLibrary{ID: l.ID, Label: l.Label, Primary: l.Primary, Writable: l.Writable, Pinned: l.Pinned,
		Source: l.SourceRoot != "" || l.Site != "", Caps: l.Caps, Site: strOrNil(l.Site), Meaning: strOrNil(l.Meaning),
		Depth: strOrNil(l.Depth), FormatNote: FormatNote(l.Format), Space: l.Space, Mine: l.Mine, SharedFrom: l.SharedFrom, FromBucket: l.Bucket != nil,
		Host: localHost(l)}
}

// localHost is the home cluster's name for a library in a folder here: no
// remote site and no bucket.
func localHost(l Library) *string {
	if l.Site != "" || l.Bucket != nil {
		return nil
	}
	return strOrNil(remote.ShortName(HomeCluster()))
}

// PublicReadThroughView is what Settings shows of a read-through host.
type PublicReadThroughView struct {
	Destination string  `json:"destination"`
	Port        *int    `json:"port"`
	SSHOptions  *string `json:"sshOptions"`
	Root        *string `json:"root"`
}

// PublicReadThrough is publicReadThrough.
func PublicReadThrough(rt *ReadThrough) *PublicReadThroughView {
	if rt == nil {
		return nil
	}
	v := &PublicReadThroughView{Destination: rt.Destination, Port: rt.Port, Root: rt.Root}
	if len(rt.SSHOptions) > 0 {
		v.SSHOptions = ptr(joinSpace(rt.SSHOptions))
	}
	return v
}

func joinSpace(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}

// PublicBuild is what Settings shows of a build.
type PublicBuild struct {
	IndexDir string         `json:"indexDir"`
	Managed  bool           `json:"managed"`
	State    string         `json:"state"`
	Error    *string        `json:"error"`
	BuiltAt  *string        `json:"builtAt"`
	Files    *int64         `json:"files"`
	Dirs     *int64         `json:"dirs"`
	Progress *BuildProgress `json:"progress"`
}

// SettingsLibrary is a library as Settings shows it (settingsLibrary).
type SettingsLibrary struct {
	PublicLibrary
	IndexRoot   string                 `json:"indexRoot"`
	SourceRoot  *string                `json:"sourceRoot"`
	ReadThrough *PublicReadThroughView `json:"readThrough"`
	Build       *PublicBuild           `json:"build,omitempty"`
	CopyOf      *PublicCopyOf          `json:"copyOf,omitempty"`
	Bucket      *PublicBucket          `json:"bucket,omitempty"`
}

// PublicCopyOf is what Settings shows of a copy library: where its files
// come from, how often Studio looks, what the last look found, whether a
// look is running now, and its transfer once it has one.
type PublicCopyOf struct {
	CopyOf
	CopyState
	Looking bool    `json:"looking"`
	Running *string `json:"running"`
}

// PublicBucket is what Settings shows of a bucket library: where its
// listing comes from, how often Studio lists it, what the last listing
// found, and whether one is running now.
type PublicBucket struct {
	BucketOf
	BucketState
	Listing bool `json:"listing"`
	// Reading is set while the objects' text is being read.
	Reading bool `json:"reading"`
}
