package transfers

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// rcloneRig is a rig with the fake rclone on PATH and two remotes: a, a
// file system (alias), and s, an object store (s3).
func rcloneRig(t *testing.T) (*rig, string, string) {
	t.Helper()
	r := newRig(t)
	bin, dir, log := fakeRcloneOn(t, "a=alias,s=s3")
	r.env["PATH"] = bin
	return r, dir, log
}

func rfs(t *testing.T, r *rig, id string) *RcloneFS {
	t.Helper()
	l, err := r.s.resolve(context.Background(), single, id)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := asRclone(l.fs)
	if !ok {
		t.Fatalf("%s is not an rclone location", id)
	}
	return f
}

func TestFindRclone(t *testing.T) {
	dir := t.TempDir()
	mk := func(p string) string {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
		return p
	}
	onPath := mk(filepath.Join(dir, "path", "rclone"))
	brew := mk(filepath.Join(dir, "brew", "rclone"))
	set := mk(filepath.Join(dir, "my rclone", "rclone"))
	env := func(k string) string {
		if k == "PATH" {
			return filepath.Join(dir, "path")
		}
		return ""
	}
	oldOS, oldBrew := goos, homebrewDirs
	defer func() { goos, homebrewDirs = oldOS, oldBrew }()
	homebrewDirs = []string{filepath.Join(dir, "none"), filepath.Join(dir, "brew")}

	goos = "linux"
	if got := FindRclone(env, "rclone"); got != onPath {
		t.Fatalf("PATH: %q", got)
	}
	// The path set in Settings comes first, spaces and all.
	if got := FindRclone(env, set); got != set {
		t.Fatalf("setting: %q", got)
	}
	if got := FindRclone(env, filepath.Join(dir, "missing", "rclone")); got != "" {
		t.Fatalf("a missing set path: %q", got)
	}
	// On macOS, Homebrew's folders before PATH.
	goos = "darwin"
	if got := FindRclone(env, ""); got != brew {
		t.Fatalf("Homebrew: %q", got)
	}
	goos = "linux"
	if got := FindRclone(func(string) string { return "" }, "rclone"); got != "" {
		t.Fatalf("none: %q", got)
	}
}

func TestRcloneDetection(t *testing.T) {
	r, _, _ := rcloneRig(t)
	ctx := context.Background()
	d := r.s.SettingsView(ctx, single).Detected.Rclone
	if d == nil || !d.Enabled || !d.Found || d.Version != "1.76.0" || d.Why != "" {
		t.Fatalf("detected %+v", d)
	}
	if len(d.Remotes) != 2 || d.Remotes[0] != (RcloneRemote{"a", "alias"}) || d.Remotes[1] != (RcloneRemote{"s", "s3"}) {
		t.Fatalf("remotes %+v", d.Remotes)
	}
	// Missing: said plainly, and no rclone locations.
	r.set.S.Movers.Rclone.Path = "/nowhere/rclone"
	d = r.s.SettingsView(ctx, single).Detected.Rclone
	if d.Found || !strings.Contains(d.Why, "not installed") {
		t.Fatalf("missing %+v", d)
	}
	for _, l := range r.s.Locations(ctx, single) {
		if l.Kind == KindRclone {
			t.Fatalf("a location without rclone: %+v", l)
		}
	}
	// No remotes.
	r.set.S.Movers.Rclone.Path = "rclone"
	t.Setenv("FAKE_RCLONE_TYPES", "")
	r.s.rc.remotes = map[string]rcloneRemotesEntry{}
	d = r.s.SettingsView(ctx, single).Detected.Rclone
	if !d.Found || len(d.Remotes) != 0 || !strings.Contains(d.Why, "no remotes") {
		t.Fatalf("no remotes %+v", d)
	}
	// Switched off.
	r.set.S.Movers.Rclone.Enabled = false
	if d = r.s.SettingsView(ctx, single).Detected.Rclone; d.Enabled || d.Found {
		t.Fatalf("off %+v", d)
	}
	// Sign-in on: off, and why.
	d = r.s.SettingsView(ctx, Viewer{ID: "u1", AuthOn: true}).Detected.Rclone
	if d.Enabled || !strings.Contains(d.Why, "sign-in") {
		t.Fatalf("sign-in on %+v", d)
	}
}

func TestRcloneSettingsCheck(t *testing.T) {
	s := DefaultSettings()
	if !s.Movers.Rclone.Enabled || s.Movers.Rclone.Path != "rclone" {
		t.Fatalf("default %+v", s.Movers.Rclone)
	}
	for _, p := range []string{"-x", "bin/rclone", "my rclone"} {
		s.Movers.Rclone.Path = p
		if _, err := s.Check(nil); err == nil {
			t.Fatalf("%q accepted", p)
		}
	}
	s.Movers.Rclone.Path = "/Applications/My Tools/rclone"
	s.Defaults.Mover = MoverRclone
	if _, err := s.Check(nil); err != nil {
		t.Fatal(err)
	}
	if managedSettings(func(string) string { return "" }).Movers.Rclone.Enabled {
		t.Fatal("rclone on with sign-in on")
	}
}

func TestRcloneLocationsAndSignIn(t *testing.T) {
	r, _, _ := rcloneRig(t)
	ctx := context.Background()
	by := map[string]Location{}
	for _, l := range r.s.Locations(ctx, single) {
		by[l.ID] = l
	}
	a, s := by["rclone:a"], by["rclone:s"]
	if a.Kind != KindRclone || a.Label != "a" || a.Detail != "rclone remote, alias" || !a.Available || !a.Browsable || !a.Writable {
		t.Fatalf("a %+v", a)
	}
	if s.Detail != "rclone remote, s3" {
		t.Fatalf("s %+v", s)
	}
	// Sign-in on: no rclone locations, and an id is refused.
	on := Viewer{ID: "u1", AuthOn: true, Key: "k"}
	for _, l := range r.s.Locations(ctx, on) {
		if l.Kind == KindRclone {
			t.Fatalf("rclone location with sign-in on: %+v", l)
		}
	}
	if _, err := r.s.Browse(ctx, on, "rclone:a", nil); statusOf(err) != 404 {
		t.Fatalf("browse with sign-in on: %v", err)
	}
	// The mover refuses a plan owned by a signed-in viewer.
	m := newRcloneMover(r.s)
	p := &planRec{owner: "u1", src: &loc{fs: rfs(t, r, "rclone:a")}, dst: &loc{fs: &LocalFS{Dir: r.kb}}}
	if ok, why := m.CanReach(ctx, p); ok || !strings.Contains(why, "sign-in") {
		t.Fatalf("sign-in on: %v %s", ok, why)
	}
}

func TestRcloneFS(t *testing.T) {
	r, dir, log := rcloneRig(t)
	ctx := context.Background()
	f := rfs(t, r, "rclone:a")
	name := `it's a "q" file.txt`
	rel := "in dir/" + name
	mt := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC).Unix()
	if n, err := f.Write(ctx, rel, strings.NewReader("hello"), mt, 0o644); err != nil || n != 5 {
		t.Fatalf("write %d %v", n, err)
	}
	on := filepath.Join(dir, "a", "in dir", name)
	if read(t, on) != "hello" {
		t.Fatal("not written")
	}
	if fi, _ := os.Stat(on); fi.ModTime().Unix() != mt {
		t.Fatalf("mtime %v", fi.ModTime())
	}
	// Never over a file.
	if _, err := f.Write(ctx, rel, strings.NewReader("x"), 0, 0); statusOf(err) != 409 {
		t.Fatalf("write over: %v", err)
	}
	// Every name is one argument, after "--".
	var rcat []string
	for _, a := range fakeLog(t, log) {
		if a[0] == "rcat" {
			rcat = a
		}
	}
	if len(rcat) < 2 || rcat[len(rcat)-2] != "--" || rcat[len(rcat)-1] != "a:"+rel {
		t.Fatalf("rcat argv %q", rcat)
	}
	write(t, filepath.Join(dir, "a", "in dir", ".hidden"), "h")
	es, more, err := f.List(ctx, "in dir", 10)
	if err != nil || more || len(es) != 1 || es[0].Name != name || es[0].Size != 5 {
		t.Fatalf("list %+v %v", es, err)
	}
	if _, _, err := f.List(ctx, "nope", 10); statusOf(err) != 404 {
		t.Fatalf("missing folder: %v", err)
	}
	// Listings take the flags that keep an object store to its listing.
	for _, a := range fakeLog(t, log) {
		if a[0] == "lsjson" && (!slices.Contains(a, "--no-mimetype") || !slices.Contains(a, "--use-server-modtime")) {
			t.Fatalf("lsjson without the fast flags: %q", a)
		}
	}
	st, err := f.Stat(ctx, []string{"in dir", rel, "nope"})
	if err != nil || st["in dir"].Type != 'd' || st[rel].Size != 5 || len(st) != 2 {
		t.Fatalf("stat %+v %v", st, err)
	}
	w, err := f.Walk(ctx, []string{"in dir"}, 0)
	if err != nil || len(w.Files) != 1 || w.Files[0].Rel != rel || w.Skipped.Hidden != 1 || !slices.Contains(w.Dirs, "in dir") {
		t.Fatalf("walk %+v %v", w, err)
	}
	if _, err := f.Walk(ctx, []string{"nope"}, 0); statusOf(err) != 404 {
		t.Fatalf("walk missing: %v", err)
	}
	for i := 0; i < 5; i++ {
		write(t, filepath.Join(dir, "a", "many", "f"+itoa(i)), "x")
	}
	if _, err := f.Walk(ctx, []string{"many"}, 3); err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("walk past the limit: %v", err)
	}
	rc, err := f.Read(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" {
		t.Fatalf("read %q", b)
	}
	if rc, err := f.Read(ctx, "nope.txt"); err == nil {
		_, err = io.ReadAll(rc)
		rc.Close()
		if statusOf(err) != 404 {
			t.Fatalf("read missing: %v", err)
		}
	}
	sums, err := f.Sha256(ctx, []string{rel})
	if err != nil || sums[rel] != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha256 %v %v", sums, err)
	}
	if free, ok := f.Free(ctx, ""); !ok || free != 999999000 {
		t.Fatalf("free %d %v", free, ok)
	}

	// Staging and landing: one move of exactly the staged files.
	stage := "out/" + stagingPrefix + "x1"
	if err := f.Mkdir(ctx, stage); err != nil {
		t.Fatal(err)
	}
	f.Write(ctx, stage+"/"+name, strings.NewReader("new"), 0, 0)
	f.Write(ctx, stage+"/sub/two.txt", strings.NewReader("2"), 0, 0)
	write(t, filepath.Join(dir, "a", "out", "sub", "two.txt"), "old")
	pairs := []LandPair{{Staged: stage + "/" + name, Final: "out/" + name}, {Staged: stage + "/sub/two.txt", Final: "out/sub/two.txt"}}
	done, err := f.Land(ctx, pairs)
	if statusOf(err) != 409 || len(done) != 1 || done[0] != "out/"+name {
		t.Fatalf("land over an unlisted file: %v %v", done, err)
	}
	if read(t, filepath.Join(dir, "a", "out", "sub", "two.txt")) != "old" {
		t.Fatal("an unlisted file was written over")
	}
	pairs[1].Replace = true
	if done, err := f.Land(ctx, pairs[1:]); err != nil || len(done) != 1 {
		t.Fatalf("land replace: %v %v", done, err)
	}
	if read(t, filepath.Join(dir, "a", "out", "sub", "two.txt")) != "2" {
		t.Fatal("not replaced")
	}
	var moves int
	for _, a := range fakeLog(t, log) {
		if a[0] == "move" {
			moves++
			if !slices.Contains(a, "--files-from-raw") || a[len(a)-2] != "a:"+stage || a[len(a)-1] != "a:out" {
				t.Fatalf("move argv %q", a)
			}
		}
	}
	if moves != 2 {
		t.Fatalf("%d moves", moves)
	}
	if err := f.RemoveStaging(ctx, "out"); err == nil {
		t.Fatal("removed a folder that is not a staging folder")
	}
	if err := f.RemoveStaging(ctx, stage); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(dir, "a", stage))
	if err := f.RemoveStaging(ctx, stage); err != nil {
		t.Fatalf("a staging folder already gone: %v", err)
	}
	removed, err := f.Remove(ctx, []string{"out/" + name, "out/sub/two.txt", "out/missing"}, []string{"out", "out/sub"})
	if err != nil || len(removed) != 2 {
		t.Fatalf("remove %v %v", removed, err)
	}
	gone(t, filepath.Join(dir, "a", "out"))
	for _, a := range fakeLog(t, log) {
		if a[0] == "delete" && (!slices.Contains(a, "--max-delete") || a[slices.Index(a, "--max-delete")+1] != "2") {
			t.Fatalf("delete argv %q", a)
		}
	}
}

func TestRcloneObjectStoreFS(t *testing.T) {
	r, dir, _ := rcloneRig(t)
	ctx := context.Background()
	f := rfs(t, r, "rclone:s")
	if !f.directWrite(ctx) {
		t.Fatal("an object store has a rename")
	}
	// No folders: a path with nothing in it lists empty, Mkdir makes
	// nothing, and there is no free space to read.
	if es, _, err := f.List(ctx, "new/folder", 10); err != nil || len(es) != 0 {
		t.Fatalf("empty path %v %v", es, err)
	}
	if err := f.Mkdir(ctx, "made"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s", "made")); err == nil {
		t.Fatal("mkdir made a folder on an object store")
	}
	if _, ok := f.Free(ctx, ""); ok {
		t.Fatal("free space on an object store")
	}
	write(t, filepath.Join(dir, "s", "d", "x.txt"), "hello")
	write(t, filepath.Join(dir, "s", "d", "big.parts"), "parts")
	h, err := f.hashes(ctx, "md5", []string{"d/x.txt", "d/big.parts"})
	if err != nil || h["d/x.txt"] != "5d41402abc4b2a76b9719d911017c592" || h["d/big.parts"] != "" {
		t.Fatalf("md5 %v %v", h, err)
	}
	// sha256 is read through cat.
	sums, err := f.Sha256(ctx, []string{"d/x.txt"})
	if err != nil || sums["d/x.txt"] != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha256 %v %v", sums, err)
	}
}

func TestRcloneArgs(t *testing.T) {
	a := rcloneArgs("/tmp/list", "/home/u/Downloads/data", "s:bucket/out", 4, "")
	want := []string{"copy", "--files-from-raw", "/tmp/list", "--no-traverse", "--use-json-log", "--stats", "1s", "--stats-log-level", "NOTICE",
		"--transfers", "4", "--checkers", "4", "--", "/home/u/Downloads/data", "s:bucket/out"}
	if !slices.Equal(a, want) {
		t.Fatalf("argv %q", a)
	}
	if a := rcloneArgs("l", "x", "y", 2, "new"); !slices.Contains(a, "--ignore-existing") || slices.Contains(a, "--ignore-times") {
		t.Fatalf("new %q", a)
	}
	if a := rcloneArgs("l", "x", "y", 2, "replace"); !slices.Contains(a, "--ignore-times") {
		t.Fatalf("replace %q", a)
	}
}

func TestParseRcloneStats(t *testing.T) {
	line := `{"time":"2026-11-15T16:13:21.358020217Z","level":"notice","msg":"\nTransferred:   \t    1.996 MiB / 2.861 MiB, 70%\n","stats":{"bytes":2093059,"checks":0,"deletedDirs":0,"deletes":0,"elapsedTime":1.000356219,"errors":0,"eta":0,"fatalError":false,"listed":2,"renames":0,"retryError":false,"speed":2092997.5,"totalBytes":3000003,"totalChecks":0,"totalTransfers":2,"transferring":[{"bytes":2093056,"name":"big.bin","percentage":69,"size":3000000}],"transfers":1},"source":"accounting/stats.go:554"}`
	st, ok := parseRcloneStats([]byte(line))
	if !ok || st.Bytes != 2093059 || st.Transfers != 1 {
		t.Fatalf("%+v %v", st, ok)
	}
	if _, ok := parseRcloneStats([]byte(`{"level":"error","msg":"x: Failed to copy: denied"}`)); ok {
		t.Fatal("an error line read as stats")
	}
	if m := rcloneMessage(`{"level":"error","msg":"x: Failed to copy: denied"}` + "\n"); m != "x: Failed to copy: denied" {
		t.Fatalf("message %q", m)
	}
	if m := rcloneMessage("2026/11/15 16:13:06 NOTICE: Failed to lsjson: directory not found\n"); m != "Failed to lsjson: directory not found" {
		t.Fatalf("message %q", m)
	}
}

func TestParseRemotesAndFeatures(t *testing.T) {
	rs := parseRemotes([]byte("zeta:   s3\nalpha:  alias\n\n-bad: x\n"))
	if len(rs) != 2 || rs[0].Name != "alpha" || rs[1].Type != "s3" {
		t.Fatalf("%+v", rs)
	}
	f, err := parseFeatures([]byte("{\n\t\"Hashes\": [\n\t\t\"md5\",\n\t],\n\t\"Features\": {\n\t\t\"Move\": false,\n\t\t\"About\": false,\n\t},\n}\n"))
	if err != nil || f.Move || !f.has("md5") {
		t.Fatalf("%+v %v", f, err)
	}
	if v := parseRcloneVersion("rclone v1.76.0\n- os/version: x"); v != "1.76.0" {
		t.Fatal(v)
	}
}

func TestRcloneMoverCopyMoveSync(t *testing.T) {
	r, dir, log := rcloneRig(t)
	write(t, filepath.Join(r.home, "Downloads", "data", "one.txt"), "one")
	write(t, filepath.Join(r.home, "Downloads", "data", "sub dir", `two "2".txt`), "two")

	// This computer to a file system remote: staged, then landed.
	p := r.mustPlan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:a", ""))
	if p.Mover != MoverRclone || !strings.Contains(p.MoverWhy, "staging folder") {
		t.Fatalf("plan %s: %s", p.Mover, p.MoverWhy)
	}
	for _, m := range p.Movers {
		if m.Name == MoverRsync && m.OK {
			t.Fatal("rsync offered for an rclone remote")
		}
	}
	tr := r.run(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:a", ""))
	if tr.State != StateSucceeded || tr.Mover != MoverRclone {
		t.Fatalf("copy %+v", tr)
	}
	if read(t, filepath.Join(dir, "a", "data", "sub dir", `two "2".txt`)) != "two" {
		t.Fatal("not copied")
	}
	noStaging(t, filepath.Join(dir, "a"))
	var copies [][]string
	for _, a := range fakeLog(t, log) {
		if a[0] == "copy" {
			copies = append(copies, a)
		}
		if a[0] == "sync" {
			t.Fatalf("rclone sync was run: %q", a)
		}
	}
	if len(copies) != 1 || copies[0][len(copies[0])-2] != filepath.Join(r.home, "Downloads") || !strings.HasPrefix(copies[0][len(copies[0])-1], "a:"+stagingPrefix) {
		t.Fatalf("copy argv %q", copies)
	}

	// A move from the remote to an object store: written straight to the
	// names, checked by MD5, then the originals removed.
	p = r.mustPlan(single, req(ActionMove, "rclone:a", []string{"data"}, "rclone:s", "bucket"))
	if !p.hasWarning("no server-side rename") {
		t.Fatalf("no direct note: %q", p.Warnings)
	}
	tr = r.run(single, req(ActionMove, "rclone:a", []string{"data"}, "rclone:s", "bucket"))
	if tr.State != StateSucceeded {
		t.Fatalf("move %+v", tr)
	}
	if read(t, filepath.Join(dir, "s", "bucket", "data", "one.txt")) != "one" {
		t.Fatal("not moved")
	}
	gone(t, filepath.Join(dir, "a", "data", "one.txt"))
	if !hasEvent(tr, "md5 matches") {
		t.Fatalf("events %+v", tr.Events)
	}
	for _, a := range fakeLog(t, log) {
		if a[0] == "copy" && strings.HasPrefix(a[len(a)-1], "s:") && (a[len(a)-1] != "s:bucket" || !slices.Contains(a, "--ignore-existing")) {
			t.Fatalf("direct copy argv %q", a)
		}
	}

	// A sync from this computer to the object store: by size and MD5,
	// since the store's times are upload times; one change sent.
	write(t, filepath.Join(r.home, "Downloads", "data", "one.txt"), "ONE")
	q := req(ActionSync, "computer:Downloads", []string{"data"}, "rclone:s", "bucket/data")
	p = r.mustPlan(single, q)
	if !strings.HasPrefix(p.Compare, "size and md5") || p.Files != 1 || p.ReplacesCount != 1 {
		t.Fatalf("sync plan %d files, %d replaced, %q", p.Files, p.ReplacesCount, p.Compare)
	}
	tr = r.run(single, q)
	if tr.State != StateSucceeded || read(t, filepath.Join(dir, "s", "bucket", "data", "one.txt")) != "ONE" {
		t.Fatalf("sync %+v", tr)
	}
	if p = r.mustPlan(single, q); p.Files != 0 {
		t.Fatalf("sync again sends %d", p.Files)
	}
	last := fakeLog(t, log)
	var replaceRun bool
	for _, a := range last {
		if a[0] == "copy" && slices.Contains(a, "--ignore-times") {
			replaceRun = true
		}
	}
	if !replaceRun {
		t.Fatal("a replaced file was not sent with --ignore-times")
	}

	// A sync into a folder of the object store that holds nothing yet, and
	// a copy there: neither is taken for a name already in place.
	tr = r.run(single, req(ActionSync, "computer:Downloads", []string{"data"}, "rclone:s", "fresh/prefix"))
	if tr.State != StateSucceeded || read(t, filepath.Join(dir, "s", "fresh", "prefix", "one.txt")) != "ONE" {
		t.Fatalf("sync to a new prefix %+v", tr)
	}
	tr = r.run(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:s", "other"))
	if tr.State != StateSucceeded || read(t, filepath.Join(dir, "s", "other", "data", "one.txt")) != "ONE" {
		t.Fatalf("copy to a new prefix %+v", tr)
	}
	if _, err := r.plan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:s", "other")); err == nil || !strings.Contains(err.Error(), "already in") {
		t.Fatalf("a name already in the store: %v", err)
	}
}

func (p *Plan) hasWarning(s string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, s) {
			return true
		}
	}
	return false
}

func TestRcloneSyncFallsBackToTimes(t *testing.T) {
	r, dir, _ := rcloneRig(t)
	// An object with no MD5 (uploaded in parts) is compared by time; the
	// store lists its upload time, so it is sent again.
	write(t, filepath.Join(r.home, "Downloads", "d", "big.parts"), "12345")
	write(t, filepath.Join(r.home, "Downloads", "d", "x.txt"), "x")
	write(t, filepath.Join(dir, "s", "d", "big.parts"), "12345")
	write(t, filepath.Join(dir, "s", "d", "x.txt"), "x")
	p := r.mustPlan(single, req(ActionSync, "computer:Downloads", []string{"d"}, "rclone:s", "d"))
	if p.Files != 1 || p.Replaces[0] != "d/big.parts" {
		t.Fatalf("%d files %v", p.Files, p.Replaces)
	}
}

func TestRcloneCancel(t *testing.T) {
	r, dir, _ := rcloneRig(t)
	for i := 0; i < 6; i++ {
		write(t, filepath.Join(r.home, "Downloads", "slow", "f"+itoa(i)), "data")
	}
	t.Setenv("FAKE_RCLONE_SLOW", "1")
	p := r.mustPlan(single, req(ActionCopy, "computer:Downloads", []string{"slow"}, "rclone:a", ""))
	tr, err := r.s.Start(context.Background(), single, p.ID, ByUser, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		got, _ := r.s.Get(single, tr.ID)
		if got.Files.Done > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ok, why := r.s.Cancel(single, tr.ID); !ok {
		t.Fatalf("cancel: %s", why)
	}
	end := r.wait(single, tr.ID)
	if end.State != StateCancelled {
		t.Fatalf("state %s %s", end.State, end.Error)
	}
	noStaging(t, filepath.Join(dir, "a"))
	if _, err := os.Stat(filepath.Join(dir, "a", "slow")); err == nil {
		t.Fatal("a cancelled copy left files in place")
	}
}

func TestRcloneToClusterUsesStudioCopy(t *testing.T) {
	r, dir, _ := rcloneRig(t)
	write(t, filepath.Join(dir, "a", "x", "f.txt"), "f")
	p := r.mustPlan(single, req(ActionCopy, "rclone:a", []string{"x"}, "saved:a", ""))
	if p.Mover != MoverCopy {
		t.Fatalf("mover %s", p.Mover)
	}
	for _, m := range p.Movers {
		if m.Name == MoverRclone && (m.OK || !strings.Contains(m.Why, "studio-copy")) {
			t.Fatalf("rclone %+v", m)
		}
	}
	tr := r.run(single, req(ActionCopy, "rclone:a", []string{"x"}, "saved:a", ""))
	if tr.State != StateSucceeded || read(t, filepath.Join(r.hostA, "x", "f.txt")) != "f" {
		t.Fatalf("%+v", tr)
	}
	// And back, onto the object store through staging (studio-copy writes
	// with rcat, then the files land).
	tr = r.run(single, req(ActionCopy, "saved:a", []string{"x"}, "rclone:s", "back"))
	if tr.State != StateSucceeded || read(t, filepath.Join(dir, "s", "back", "x", "f.txt")) != "f" {
		t.Fatalf("%+v", tr)
	}
	noStaging(t, filepath.Join(dir, "s"))
}

func TestRcloneOnHost(t *testing.T) {
	bin, _, _ := fakeRcloneOn(t, "a=alias")
	ctx := context.Background()
	t.Setenv("PATH", "/usr/bin:/bin")
	if _, ok := RcloneOnHost(ctx, &localRunner{}, "ssh://user@files.example.com", ""); ok {
		t.Skip("this computer has rclone on its PATH")
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if v, ok := RcloneOnHost(ctx, &localRunner{}, "ssh://user@files.example.com", ""); !ok || v != "1.76.0" {
		t.Fatalf("%q %v", v, ok)
	}
}

// An object store has no folders, so the write check writes and deletes a
// zero-byte staging-named object: a bucket read with read-only credentials
// is refused at the plan, not at run time.
func TestRcloneObjectStoreWriteCheck(t *testing.T) {
	r, dir, log := rcloneRig(t)
	write(t, filepath.Join(r.home, "Downloads", "data", "one.txt"), "one")
	write(t, filepath.Join(dir, "s", "bucket", "keep.txt"), "keep")

	// Writable: the probe is written, then deleted, and nothing is left.
	r.mustPlan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:s", "bucket"))
	var rcat, del []string
	for _, a := range fakeLog(t, log) {
		switch a[0] {
		case "rcat":
			rcat = a
		case "delete":
			del = a
		case "mkdir":
			t.Fatalf("mkdir on an object store: %q", a)
		}
	}
	if len(rcat) == 0 || !strings.HasPrefix(rcat[len(rcat)-1], "s:bucket/"+stagingPrefix+"probe-") || len(del) == 0 {
		t.Fatalf("probe rcat %q delete %q", rcat, del)
	}
	es, _ := os.ReadDir(filepath.Join(dir, "s", "bucket"))
	if len(es) != 1 || es[0].Name() != "keep.txt" {
		t.Fatalf("left in the bucket: %v", es)
	}

	// Read only: the plan is refused with the write-check message.
	t.Setenv("FAKE_RCLONE_READONLY", "s")
	_, err := r.plan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:s", "bucket"))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "you cannot write in s:bucket on the rclone remote s") {
		t.Fatalf("read-only bucket: %v", err)
	}
	// A move out of it is refused too: the originals could not be removed.
	_, err = r.plan(single, req(ActionMove, "rclone:s", []string{"bucket/keep.txt"}, "computer:Downloads", ""))
	if statusOf(err) != 400 || !strings.Contains(messageOf(err), "originals could not be removed") {
		t.Fatalf("move out of a read-only bucket: %v", err)
	}

	// Writes allowed, deletes refused: the plan goes ahead and the probe
	// is left (a warning is logged).
	t.Setenv("FAKE_RCLONE_READONLY", "")
	t.Setenv("FAKE_RCLONE_NODELETE", "s")
	r.mustPlan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:s", "bucket"))
	es, _ = os.ReadDir(filepath.Join(dir, "s", "bucket"))
	if len(es) != 2 {
		t.Fatalf("expected the probe left behind: %v", es)
	}

	// A file system remote still checks with a folder.
	t.Setenv("FAKE_RCLONE_NODELETE", "")
	r.mustPlan(single, req(ActionCopy, "computer:Downloads", []string{"data"}, "rclone:a", ""))
	sawMkdir := false
	for _, a := range fakeLog(t, log) {
		if a[0] == "mkdir" && strings.HasPrefix(a[len(a)-1], "a:"+stagingPrefix+"probe-") {
			sawMkdir = true
		}
	}
	if !sawMkdir {
		t.Fatal("no folder probe on a file system remote")
	}
}
