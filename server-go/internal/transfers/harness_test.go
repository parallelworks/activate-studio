package transfers

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// recIndex records what the index heard.
type recIndex struct {
	mu      sync.Mutex
	landed  []string
	removed []string
}

func (x *recIndex) Landed(_ context.Context, lib string, files []string) {
	x.mu.Lock()
	x.landed = append(x.landed, prefixed(lib, files)...)
	x.mu.Unlock()
}

func (x *recIndex) Removed(_ context.Context, lib string, files, dirs []string) {
	x.mu.Lock()
	x.removed = append(x.removed, prefixed(lib, files)...)
	x.mu.Unlock()
}

func prefixed(lib string, a []string) []string {
	var out []string
	for _, s := range a {
		out = append(out, lib+":"+s)
	}
	sort.Strings(out)
	return out
}

// rig is a service over a temporary world: a main library on this
// computer (writable, indexed), a second library in a folder on this
// computer (writable by access with sign-in off, read only with sign-in
// on, not indexed by Studio), an index-only library, Downloads, and two
// saved hosts reached through localRunner (stand-ins for ssh: the
// commands run here, against folders of their own).
type rig struct {
	t                               *testing.T
	s                               *Service
	dir, kb, ro, home, hostA, hostB string
	ix                              *recIndex
	set                             *MemorySettings
	env                             map[string]string
	runner                          *localRunner
	// more are libraries added to the rig's three; moreFor, when set, adds
	// libraries that depend on the viewer.
	more    []LibraryLoc
	moreFor func(v Viewer) []LibraryLoc
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, dir: dir, kb: filepath.Join(dir, "kb"), ro: filepath.Join(dir, "ro"), home: filepath.Join(dir, "home"),
		hostA: filepath.Join(dir, "host-a"), hostB: filepath.Join(dir, "host-b"), ix: &recIndex{}, env: map[string]string{}}
	for _, d := range []string{r.kb, r.ro, filepath.Join(r.home, "Downloads"), r.hostA, r.hostB} {
		os.MkdirAll(d, 0o755)
	}
	set := DefaultSettings()
	set.SavedLocations = []SavedLocation{
		{ID: "a", Label: "Host A", Host: "user@host-a", Root: r.hostA},
		{ID: "b", Label: "Host B", Host: "user@host-b", Root: r.hostB},
	}
	r.set = &MemorySettings{S: set}
	// rsync's remote shell for the stand-in hosts: the command runs here.
	shell := filepath.Join(dir, "rsh")
	os.WriteFile(shell, []byte("#!/bin/sh\nexec bash -c \"$1\"\n"), 0o755)
	run := &localRunner{}
	r.runner = run
	r.s = New(Deps{
		IndexBase: filepath.Join(dir, "ix"),
		Getenv:    func(k string) string { return r.env[k] },
		Runner:    run,
		Shell:     func(site, key string) ([]string, []string, error) { return []string{shell}, nil, nil },
		Libraries: func(ctx context.Context, v Viewer) []LibraryLoc {
			return append([]LibraryLoc{
				{ID: "kb", Label: "Knowledge base", Writable: true, Indexed: true, FS: &LocalFS{Dir: r.kb}},
				refLib(r.ro, v),
				{ID: "far", Label: "Index only", Why: "Studio has only its index, not its files"},
			}, append(append([]LibraryLoc{}, r.more...), r.libsFor(v)...)...)
		},
		Clusters: func(ctx context.Context, v Viewer) ([]string, error) { return []string{"gpu-" + v.ID}, nil },
		Settings: r.set,
		Index:    r.ix,
		Home:     r.home,
	})
	return r
}

// refNote is when the rig's second library shows a transfer's change.
const refNote = "after the index is rebuilt where it is built"

// refLib is a library other than the main one in a folder on this
// computer, as libraryLocs makes it for v.
func refLib(dir string, v Viewer) LibraryLoc {
	w, why := LibraryWritable(false, AccessServer, v.AuthOn)
	return LibraryLoc{ID: "ro", Label: "Reference", Writable: w, ReadOnly: why, Refresh: refNote, FS: &LocalFS{Dir: dir}}
}

func (r *rig) libsFor(v Viewer) []LibraryLoc {
	if r.moreFor == nil {
		return nil
	}
	return r.moreFor(v)
}

var single = Viewer{}

func (r *rig) plan(v Viewer, req Request) (*Plan, error) {
	return r.s.Plan(context.Background(), v, req)
}

func req(action, src string, paths []string, dst, dstPath string) Request {
	var q Request
	q.Action = action
	q.Source.Location, q.Source.Paths = src, paths
	q.Destination.Location, q.Destination.Path = dst, dstPath
	return q
}

func (r *rig) mustPlan(v Viewer, q Request) *Plan {
	r.t.Helper()
	p, err := r.plan(v, q)
	if err != nil {
		r.t.Fatalf("plan: %v", err)
	}
	return p
}

// wait is the transfer once it ends.
func (r *rig) wait(v Viewer, id string) Transfer {
	r.t.Helper()
	for i := 0; i < 600; i++ {
		t, err := r.s.Get(v, id)
		if err != nil {
			r.t.Fatal(err)
		}
		if finished(t.State) {
			return t
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("transfer %s did not end", id)
	return Transfer{}
}

func (r *rig) run(v Viewer, q Request) Transfer {
	r.t.Helper()
	p := r.mustPlan(v, q)
	t, err := r.s.Start(context.Background(), v, p.ID, ByUser, "")
	if err != nil {
		r.t.Fatalf("start: %v", err)
	}
	return r.wait(v, t.ID)
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func gone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err == nil {
		t.Fatalf("%s is still there", p)
	}
}

// noStaging fails when a staging folder is left anywhere under dir.
func noStaging(t *testing.T, dir string) {
	t.Helper()
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.IsDir() && len(fi.Name()) > len(stagingPrefix) && fi.Name()[:len(stagingPrefix)] == stagingPrefix {
			t.Fatalf("staging folder left: %s", p)
		}
		return nil
	})
}

var yes = func() *bool { b := true; return &b }()
