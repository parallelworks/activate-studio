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

import "encoding/json"

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

// Def is a library as stored (LibraryDef).
type Def struct {
	ID          string       `json:"id"`
	Label       string       `json:"label,omitempty"`
	IndexRoot   string       `json:"indexRoot"`
	SourceRoot  *string      `json:"sourceRoot"`
	Build       *LocalBuild  `json:"build,omitempty"`
	ReadThrough *ReadThrough `json:"readThrough,omitempty"`
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
		Depth: strOrNil(l.Depth), FormatNote: FormatNote(l.Format)}
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
}
