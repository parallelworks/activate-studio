package libraries

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// RemotePrefix starts the index root of a library on another system:
// "remote:<owner hash>:<id>", an opaque reference.
const RemotePrefix = "remote:"

var userIDRe = regexp.MustCompile(`^u-[a-z0-9][a-z0-9-]{0,38}$`)

// UserDef is a library a user added from one of their systems
// (UserLibraryDef). Optional fields are pointers so absent and false stay
// apart, as they do in Node (vectors undefined is "never looked").
type UserDef struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Transport   string        `json:"transport,omitempty"` // "pw" (default) or "ssh"
	Site        string        `json:"site"`
	Destination *string       `json:"destination,omitempty"`
	Port        *int          `json:"port,omitempty"`
	SSHOptions  []string      `json:"sshOptions,omitempty"`
	Existing    *bool         `json:"existing,omitempty"`
	OS          *string       `json:"os,omitempty"`
	GufiVersion *string       `json:"gufiVersion,omitempty"`
	Resource    string        `json:"resource"`
	SourceDir   string        `json:"sourceDir"`
	SourceRoot  *string       `json:"sourceRoot"`
	IndexRoot   *string       `json:"indexRoot"`
	GufiBin     *string       `json:"gufiBin"`
	Shell       *bool         `json:"shell,omitempty"`
	State       string        `json:"state"`
	Run         *WorkflowRun  `json:"run"`
	Error       *string       `json:"error"`
	BuiltAt     *string       `json:"builtAt"`
	Directories *int64        `json:"directories"`
	Files       *int64        `json:"files"`
	Scheduler   *Scheduler    `json:"scheduler"`
	FullText    *bool         `json:"fullText,omitempty"`
	Vectors     *bool         `json:"vectors,omitempty"`
	IndexFormat *FormatRecord `json:"indexFormat,omitempty"`
	Build       *RemoteBuild  `json:"build,omitempty"`
	BuiltDepth  *string       `json:"builtDepth,omitempty"`
	Gaps        []string      `json:"gaps,omitempty"`
	ReadThrough *ReadThrough  `json:"readThrough,omitempty"`
}

// WorkflowRun is the index workflow's run for a library.
type WorkflowRun struct {
	Slug      string `json:"slug"`
	Mode      string `json:"mode"`
	StartedAt string `json:"startedAt"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// Scheduler is a workflow build's scheduler options.
type Scheduler struct {
	Partition string `json:"partition,omitempty"`
	Account   string `json:"account,omitempty"`
	QOS       string `json:"qos,omitempty"`
	Time      string `json:"time,omitempty"`
}

// RemoteBuild is a build the Studio runs on a remote system.
type RemoteBuild struct {
	ID         string         `json:"id"`
	IndexDir   string         `json:"indexDir"`
	SourceRoot string         `json:"sourceRoot"`
	GufiBin    string         `json:"gufiBin"`
	Python     *string        `json:"python"`
	Model      *string        `json:"model"`
	StartedAt  string         `json:"startedAt"`
	Depth      string         `json:"depth,omitempty"`
	Progress   *BuildProgress `json:"progress,omitempty"`
}

// IsSSH is transport ssh.
func (d *UserDef) IsSSH() bool { return d.Transport == "ssh" }

func ownerHash(owner string) string {
	s := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(s[:])[:16]
}

// Users keeps each owner's libraries (INDEX_BASE/users/<hash>/libraries.json).
// Node reads the file afresh at every use, so it has no cache to fall out
// of step with; writes here are atomic.
type Users struct {
	IndexBase   string
	AuthEnabled bool
	mu          sync.Mutex
	removed     []func(root string)
	built       []func(root string)
}

func (u *Users) fileFor(owner string) string {
	return filepath.Join(u.IndexBase, "users", ownerHash(owner), "libraries.json")
}

// OwnerOf is ownerOf: the viewer's id, or "single" with sign-in off; "" for none.
func (u *Users) OwnerOf(viewer string) string {
	if viewer != "" {
		return viewer
	}
	if u.AuthEnabled {
		return ""
	}
	return "single"
}

// Load is loadUserLibraries.
func (u *Users) Load(owner string) []UserDef {
	b, err := os.ReadFile(u.fileFor(owner))
	if err != nil {
		return []UserDef{}
	}
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return []UserDef{}
	}
	out := []UserDef{}
	for _, r := range raw {
		var d UserDef
		if json.Unmarshal(r, &d) == nil && userIDRe.MatchString(d.ID) {
			out = append(out, d)
		}
	}
	return out
}

// Save is saveUserLibraries.
func (u *Users) Save(owner string, defs []UserDef) error {
	f := u.fileFor(owner)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(filepath.Dir(f), "owner"), []byte(owner), 0o644)
	if defs == nil {
		defs = []UserDef{}
	}
	b, err := marshalJS(defs, "  ")
	if err != nil {
		return err
	}
	return writeAtomic(f, b)
}

// Update is updateDef: patch applied to one library under the lock.
func (u *Users) Update(owner, id string, patch func(*UserDef)) *UserDef {
	u.mu.Lock()
	defer u.mu.Unlock()
	defs := u.Load(owner)
	for i := range defs {
		if defs[i].ID == id {
			patch(&defs[i])
			u.Save(owner, defs)
			d := defs[i]
			return &d
		}
	}
	return nil
}

// Modify loads, changes and saves an owner's list under the lock.
func (u *Users) Modify(owner string, fn func([]UserDef) ([]UserDef, error)) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	next, err := fn(u.Load(owner))
	if err != nil {
		return err
	}
	return u.Save(owner, next)
}

// RemoteRootOf is remoteRootOf.
func RemoteRootOf(owner, id string) string { return RemotePrefix + ownerHash(owner) + ":" + id }

var hash16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func splitRoot(root string) (hash, id string, ok bool) {
	if !strings.HasPrefix(root, RemotePrefix) {
		return "", "", false
	}
	parts := strings.Split(root[len(RemotePrefix):], ":")
	if len(parts) < 2 {
		return parts[0], "", true
	}
	return parts[0], parts[1], true
}

// RemoteMapHome is remoteMapHome: where the Studio keeps what it makes of
// a library on another system, in its owner's folder.
func (u *Users) RemoteMapHome(root string) (string, error) {
	hash, id, ok := splitRoot(root)
	if !ok || !hash16.MatchString(hash) || !userIDRe.MatchString(id) {
		return "", apierr.New(400, "not a library on another system")
	}
	return filepath.Join(u.IndexBase, "users", hash, "maps", id), nil
}

// OnRemoved and OnBuilt are told, by root, when a user library is removed
// and when a build there finished with a new index (semanticMap.ts).
func (u *Users) OnRemoved(fn func(root string)) { u.removed = append(u.removed, fn) }
func (u *Users) OnBuilt(fn func(root string))   { u.built = append(u.built, fn) }

// TellRemoved and TellBuilt run those hooks.
func (u *Users) TellRemoved(root string) {
	for _, f := range u.removed {
		f(root)
	}
}
func (u *Users) TellBuilt(root string) {
	for _, f := range u.built {
		f(root)
	}
}

// Target is what a remote root resolves to (remoteTarget).
type Target struct {
	Owner      string
	Def        UserDef
	Site       string
	Key        string
	GufiBin    string
	IndexRoot  string
	SourceRoot string
	BuiltAt    string
	DyldLib    string
}

// SSHSingleUser is why libraries over ssh are offered only with sign-in off.
const SSHSingleUser = "Libraries over ssh use the ssh keys of the account the Studio runs as, so they are offered only on a Studio with sign-in off."

// GufiTool is gufiTool: a GUFI tool in its bin directory, or by name.
func GufiTool(bin, tool string) string {
	if tool == "" {
		tool = "gufi_query"
	}
	if bin != "" {
		return bin + "/" + tool
	}
	return tool
}

// OwnerOfRoot reads the owner a remote root's hash belongs to.
func (u *Users) OwnerOfRoot(root string) (string, error) {
	hash, _, ok := splitRoot(root)
	if !ok {
		return "", nil
	}
	b, err := os.ReadFile(filepath.Join(u.IndexBase, "users", hash, "owner"))
	if err != nil || !hash16.MatchString(hash) {
		return "", apierr.New(404, "no such library")
	}
	return string(b), nil
}

// FindRoot is the stored definition a remote root names.
func (u *Users) FindRoot(root string) (string, *UserDef, error) {
	owner, err := u.OwnerOfRoot(root)
	if err != nil {
		return "", nil, err
	}
	_, id, _ := splitRoot(root)
	for _, d := range u.Load(owner) {
		if d.ID == id {
			return owner, &d, nil
		}
	}
	return owner, nil, nil
}

// SiteFn makes a site from an ssh library's destination, port and options
// (remote.SSHSite); set by the server, so this package does not import
// internal/remote.
type SiteFn func(destination string, port int, options []string) (string, error)

// Target is remoteTarget: nil, nil for a local path.
func (u *Users) Target(root string, keys Keys, sshSite SiteFn) (*Target, error) {
	if !strings.HasPrefix(root, RemotePrefix) {
		return nil, nil
	}
	owner, d, err := u.FindRoot(root)
	if err != nil {
		return nil, err
	}
	if d == nil || d.IndexRoot == nil || *d.IndexRoot == "" || d.GufiBin == nil {
		return nil, apierr.New(409, "this library is not built yet")
	}
	t := &Target{Owner: owner, Def: *d, GufiBin: *d.GufiBin, IndexRoot: *d.IndexRoot, Site: d.Site}
	if d.SourceRoot != nil {
		t.SourceRoot = *d.SourceRoot
	}
	if d.BuiltAt != nil {
		t.BuiltAt = *d.BuiltAt
	}
	if d.OS != nil && *d.OS == "Darwin" && t.GufiBin != "" {
		t.DyldLib = path.Dir(t.GufiBin) + "/lib"
	}
	if d.IsSSH() {
		if owner != "single" || u.AuthEnabled {
			return nil, apierr.New(403, "%s", SSHSingleUser)
		}
		port := 0
		if d.Port != nil {
			port = *d.Port
		}
		dest := ""
		if d.Destination != nil {
			dest = *d.Destination
		}
		if t.Site, err = sshSite(dest, port, d.SSHOptions); err != nil {
			return nil, err
		}
	} else {
		t.Key = keys.KeyOf(owner)
		if t.Key == "" {
			return nil, apierr.New(409, "This library is read with your own platform key: add your ACTIVATE API key under Settings, Model access.")
		}
	}
	return t, nil
}
