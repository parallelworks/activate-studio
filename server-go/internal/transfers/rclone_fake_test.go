package transfers

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fake rclone is this test binary run again with STUDIO_FAKE_RCLONE
// set: a script named rclone on PATH execs it. It keeps each remote in a
// folder of FAKE_RCLONE_DIR, of the type FAKE_RCLONE_TYPES gives
// ("a=alias,s=s3"): an s3 remote behaves as an object store (no rename, no
// empty folders, no free space, MD5 only), any other as a file system. It
// prints what rclone prints for the commands Studio runs, and appends each
// command line to FAKE_RCLONE_LOG as JSON. Remotes named in
// FAKE_RCLONE_READONLY ("s,t") refuse every write, as a bucket read with
// read-only or anonymous credentials does; those in FAKE_RCLONE_NODELETE
// take writes but refuse deletes.

func TestMain(m *testing.M) {
	if os.Getenv("STUDIO_FAKE_RCLONE") == "1" {
		os.Exit(fakeRclone(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeRcloneOn writes the rclone script into a new folder and returns the
// folder (for PATH), the remotes' folder and the log. remotes is
// "name=type,...".
func fakeRcloneOn(t *testing.T, remotes string) (bin, dir, log string) {
	t.Helper()
	base := t.TempDir()
	bin, dir, log = filepath.Join(base, "bin"), filepath.Join(base, "remotes"), filepath.Join(base, "log")
	os.MkdirAll(bin, 0o755)
	for _, r := range strings.Split(remotes, ",") {
		if name, _, ok := strings.Cut(r, "="); ok {
			os.MkdirAll(filepath.Join(dir, name), 0o755)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nSTUDIO_FAKE_RCLONE=1 exec '" + exe + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RCLONE_DIR", dir)
	t.Setenv("FAKE_RCLONE_TYPES", remotes)
	t.Setenv("FAKE_RCLONE_LOG", log)
	return bin, dir, log
}

// fakeLog is each command line the fake ran.
func fakeLog(t *testing.T, log string) [][]string {
	t.Helper()
	b, _ := os.ReadFile(log)
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var a []string
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

type fakeArgs struct {
	cmd   []string
	flags map[string]string
	pos   []string
}

// valued are the flags that take a value.
var fakeValued = map[string]bool{"--hash-type": true, "--files-from-raw": true, "--stats": true, "--stats-log-level": true,
	"--transfers": true, "--checkers": true, "--max-delete": true, "--metadata-set": true}

func parseFake(args []string) fakeArgs {
	a := fakeArgs{flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x == "--":
			a.pos = append(a.pos, args[i+1:]...)
			return a
		case strings.HasPrefix(x, "-"):
			if fakeValued[x] && i+1 < len(args) {
				a.flags[x] = args[i+1]
				i++
			} else {
				a.flags[x] = "true"
			}
		case len(a.cmd) == 0 || (a.cmd[0] == "backend" && len(a.cmd) == 1):
			a.cmd = append(a.cmd, x)
		default:
			a.pos = append(a.pos, x)
		}
	}
	return a
}

func fakeTypes() map[string]string {
	out := map[string]string{}
	for _, r := range strings.Split(os.Getenv("FAKE_RCLONE_TYPES"), ",") {
		if n, t, ok := strings.Cut(r, "="); ok {
			out[n] = t
		}
	}
	return out
}

// fakePath is a remote spec or a local path as a folder here, and whether
// it is an object store.
func fakePath(spec string) (string, bool) {
	if strings.HasPrefix(spec, "/") {
		return spec, false
	}
	name, rest, _ := strings.Cut(spec, ":")
	if strings.Contains(rest, "..") {
		fmt.Fprintln(os.Stderr, "ERROR : refused ..")
		os.Exit(1)
	}
	return filepath.Join(os.Getenv("FAKE_RCLONE_DIR"), name, rest), fakeTypes()[name] == "s3"
}

func fakeFail(code int, msg string) int {
	fmt.Fprintf(os.Stderr, "2026/11/15 12:00:00 ERROR : %s\n", msg)
	fmt.Fprintf(os.Stderr, "2026/11/15 12:00:00 NOTICE: Failed to run: %s\n", msg)
	return code
}

type fakeEntry struct {
	Path    string            `json:"Path"`
	Name    string            `json:"Name"`
	Size    int64             `json:"Size"`
	ModTime string            `json:"ModTime"`
	IsDir   bool              `json:"IsDir"`
	Hashes  map[string]string `json:"Hashes,omitempty"`
}

func fakeHash(p, kind string) string {
	var h hash.Hash
	switch kind {
	case "md5":
		h = md5.New()
	case "sha1":
		h = sha1.New()
	case "sha256":
		h = sha256.New()
	default:
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// fakeHasFiles: an object store's folder exists only with a file in it.
func fakeHasFiles(dir string) bool {
	found := false
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			found = true
		}
		return nil
	})
	return found
}

func fakeList(root string, recursive, s3 bool, a fakeArgs) ([]fakeEntry, bool) {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, false
	}
	var only map[string]bool
	if f := a.flags["--files-from-raw"]; f != "" {
		only = map[string]bool{}
		b, _ := os.ReadFile(f)
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				only[l] = true
			}
		}
	}
	var out []fakeEntry
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			r := e.Name()
			if rel != "" {
				r = rel + "/" + e.Name()
			}
			p := filepath.Join(dir, e.Name())
			info, err := os.Lstat(p)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				continue // rclone skips links
			}
			isDir := info.IsDir()
			if isDir && s3 && !fakeHasFiles(p) {
				continue
			}
			en := fakeEntry{Path: r, Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().UTC().Format(time.RFC3339Nano), IsDir: isDir}
			if isDir {
				en.Size = -1
			}
			if s3 && !isDir {
				// --use-server-modtime: the upload time.
				en.ModTime = time.Unix(1700000000, 0).UTC().Format(time.RFC3339Nano)
			}
			if !isDir && a.flags["--hash"] != "" {
				en.Hashes = map[string]string{}
				kind := a.flags["--hash-type"]
				if kind == "" {
					kind = "md5"
				}
				// An object uploaded in parts has no MD5: names ending .parts.
				if !(s3 && strings.HasSuffix(e.Name(), ".parts")) {
					en.Hashes[kind] = fakeHash(p, kind)
				}
			}
			keep := only == nil || only[r]
			if isDir && (a.flags["--files-only"] != "" || only != nil) {
				keep = false
			}
			if keep {
				out = append(out, en)
			}
			if isDir && recursive {
				walk(p, r)
			}
		}
	}
	walk(root, "")
	return out, true
}

func fakeRclone(args []string) int {
	if log := os.Getenv("FAKE_RCLONE_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			b, _ := json.Marshal(args)
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	a := parseFake(args)
	if len(a.cmd) == 0 {
		return fakeFail(1, "no command")
	}
	types := fakeTypes()
	if code, denied := fakeDenied(a); denied {
		return code
	}
	switch a.cmd[0] {
	case "version":
		fmt.Println("rclone v1.76.0\n- os/version: test\n- go/version: go1.26")
		return 0
	case "listremotes":
		var names []string
		for n := range types {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("%-8s %s\n", n+":", types[n])
		}
		return 0
	case "backend":
		name := strings.TrimSuffix(a.pos[0], ":")
		if types[name] == "s3" {
			fmt.Print("{\n\t\"Name\": \"s3\",\n\t\"Hashes\": [\n\t\t\"md5\"\n\t],\n\t\"Features\": {\n\t\t\"About\": false,\n\t\t\"CanHaveEmptyDirectories\": false,\n\t\t\"Copy\": true,\n\t\t\"Move\": false,\n\t\t\"SlowHash\": false,\n\t},\n}\n")
		} else {
			fmt.Print("{\n\t\"Name\": \"local\",\n\t\"Hashes\": [\n\t\t\"md5\",\n\t\t\"sha1\",\n\t\t\"sha256\"\n\t],\n\t\"Features\": {\n\t\t\"About\": true,\n\t\t\"CanHaveEmptyDirectories\": true,\n\t\t\"Move\": true,\n\t\t\"SlowHash\": true,\n\t},\n}\n")
		}
		return 0
	case "lsjson":
		p, s3 := fakePath(a.pos[0])
		if a.flags["--stat"] != "" {
			fi, err := os.Lstat(p)
			if s3 && (err != nil || (fi.IsDir() && !fakeHasFiles(p))) {
				// As S3 answers: a path that holds nothing is a folder.
				fmt.Println(`{"Path":"","Name":"","Size":-1,"ModTime":"2026-11-15T12:00:00Z","IsDir":true}`)
				return 0
			}
			if err != nil {
				return fakeFail(3, "object not found")
			}
			en := fakeEntry{Name: filepath.Base(p), Path: filepath.Base(p), Size: fi.Size(), ModTime: fi.ModTime().UTC().Format(time.RFC3339Nano), IsDir: fi.IsDir()}
			if fi.IsDir() {
				en.Size = -1
			}
			b, _ := json.Marshal(en)
			fmt.Println(string(b))
			return 0
		}
		es, ok := fakeList(p, a.flags["-R"] != "", s3, a)
		if !ok {
			if s3 {
				fmt.Println("[\n]")
				return 0
			}
			return fakeFail(3, "directory not found")
		}
		fmt.Println("[")
		for i, e := range es {
			b, _ := json.Marshal(e)
			sep := ","
			if i == len(es)-1 {
				sep = ""
			}
			fmt.Println(string(b) + sep)
		}
		fmt.Println("]")
		return 0
	case "cat":
		p, _ := fakePath(a.pos[0])
		f, err := os.Open(p)
		if err != nil {
			return fakeFail(4, "object not found")
		}
		io.Copy(os.Stdout, f)
		return 0
	case "rcat":
		p, _ := fakePath(a.pos[0])
		os.MkdirAll(filepath.Dir(p), 0o755)
		f, err := os.Create(p)
		if err != nil {
			return fakeFail(1, err.Error())
		}
		io.Copy(f, os.Stdin)
		f.Close()
		if m := a.flags["--metadata-set"]; strings.HasPrefix(m, "mtime=") {
			if t, err := time.Parse(time.RFC3339, strings.TrimPrefix(m, "mtime=")); err == nil {
				os.Chtimes(p, t, t)
			}
		}
		return 0
	case "mkdir":
		p, s3 := fakePath(a.pos[0])
		if !s3 {
			os.MkdirAll(p, 0o755)
		}
		return 0
	case "rmdir":
		p, _ := fakePath(a.pos[0])
		if err := os.Remove(p); err != nil {
			return fakeFail(1, "directory not empty")
		}
		return 0
	case "purge":
		p, s3 := fakePath(a.pos[0])
		if _, err := os.Stat(p); err != nil || (s3 && !fakeHasFiles(p)) {
			// An object store's empty folder is not there; its stand-in goes.
			os.RemoveAll(p)
			return fakeFail(3, "directory not found")
		}
		os.RemoveAll(p)
		return 0
	case "about":
		if types[strings.TrimSuffix(strings.Split(a.pos[0], ":")[0], ":")] == "s3" {
			return fakeFail(1, "about not supported")
		}
		fmt.Println("{\n\t\"total\": 1000000000,\n\t\"used\": 1000,\n\t\"free\": 999999000\n}")
		return 0
	case "moveto":
		from, _ := fakePath(a.pos[0])
		to, _ := fakePath(a.pos[1])
		os.MkdirAll(filepath.Dir(to), 0o755)
		if err := os.Rename(from, to); err != nil {
			return fakeFail(4, "object not found")
		}
		return 0
	case "move", "delete", "copy":
		return fakeBulk(a)
	}
	return fakeFail(1, "unknown command "+a.cmd[0])
}

// fakeDenied refuses a write to a remote in FAKE_RCLONE_READONLY, and a
// delete on one in FAKE_RCLONE_NODELETE.
func fakeDenied(a fakeArgs) (int, bool) {
	in := func(env, spec string) bool {
		name, _, ok := strings.Cut(spec, ":")
		if !ok || strings.HasPrefix(spec, "/") {
			return false
		}
		for _, n := range strings.Split(os.Getenv(env), ",") {
			if n != "" && n == name {
				return true
			}
		}
		return false
	}
	var written, deleted []string
	switch a.cmd[0] {
	case "rcat", "mkdir":
		written = a.pos[:1]
	case "copy", "moveto", "move":
		written = a.pos[1:2]
		if a.cmd[0] != "copy" {
			deleted = a.pos[:1]
		}
	case "delete", "purge", "rmdir":
		deleted = a.pos[:1]
	}
	for _, p := range written {
		if in("FAKE_RCLONE_READONLY", p) {
			return fakeFail(1, "AccessDenied: Access Denied"), true
		}
	}
	for _, p := range deleted {
		if in("FAKE_RCLONE_READONLY", p) || in("FAKE_RCLONE_NODELETE", p) {
			return fakeFail(1, "AccessDenied: Access Denied"), true
		}
	}
	return 0, false
}

// fakeBulk is move, delete and copy over a --files-from-raw list.
func fakeBulk(a fakeArgs) int {
	b, _ := os.ReadFile(a.flags["--files-from-raw"])
	var names []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	from, _ := fakePath(a.pos[0])
	switch a.cmd[0] {
	case "delete":
		if m, err := strconv.Atoi(a.flags["--max-delete"]); err == nil && len(names) > m {
			return fakeFail(7, "--max-delete threshold reached")
		}
		for _, n := range names {
			os.Remove(filepath.Join(from, n))
		}
		return 0
	case "move":
		to, _ := fakePath(a.pos[1])
		for _, n := range names {
			os.MkdirAll(filepath.Dir(filepath.Join(to, n)), 0o755)
			if err := os.Rename(filepath.Join(from, n), filepath.Join(to, n)); err != nil {
				return fakeFail(1, n+": "+err.Error())
			}
		}
		return 0
	}
	// copy, with stats on standard error as JSON log lines.
	to, _ := fakePath(a.pos[1])
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	slow := os.Getenv("FAKE_RCLONE_SLOW") != ""
	var bytes, files int64
	stats := func() {
		s := map[string]any{"bytes": bytes, "transfers": files, "errors": 0, "totalTransfers": len(names), "checks": 0}
		line, _ := json.Marshal(map[string]any{"time": "2026-11-15T12:00:00Z", "level": "notice", "msg": "\nTransferred: ...\n", "stats": s, "source": "accounting/stats.go:554"})
		fmt.Fprintln(os.Stderr, string(line))
	}
	w := bufio.NewWriter(os.Stderr)
	defer w.Flush()
	for _, n := range names {
		select {
		case <-stop:
			return fakeFail(9, "interrupted")
		default:
		}
		src, dst := filepath.Join(from, n), filepath.Join(to, n)
		if _, err := os.Stat(dst); err == nil && a.flags["--ignore-existing"] != "" {
			continue
		}
		in, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, `{"level":"error","msg":"`+n+`: Failed to copy: object not found"}`)
			return 3
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		os.WriteFile(dst, in, 0o644)
		if fi, err := os.Stat(src); err == nil {
			os.Chtimes(dst, fi.ModTime(), fi.ModTime())
		}
		bytes += int64(len(in))
		files++
		stats()
		if slow {
			select {
			case <-stop:
				return fakeFail(9, "interrupted")
			case <-time.After(400 * time.Millisecond):
			}
		}
	}
	stats()
	return 0
}
