package transfers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Fixtures: --version and --help as each release prints them, cut to the
// lines that matter and kept in each release's own layout. GNU rsync
// before 3.2 prints " -t, --times   ..." with the short form first; 3.2
// and later "--times, -t   ...". openrsync prints a usage synopsis.
type rsyncFixture struct {
	version, help string
	trust         bool // takes --trust-sender (the probe)
}

const helpHead = `rsync comes with ABSOLUTELY NO WARRANTY.  This is free software, and you
are welcome to redistribute it under certain conditions.  See the GNU
General Public Licence for details.

rsync is a file transfer program capable of efficient remote update
via a fast differencing algorithm.

Usage: rsync [OPTION]... SRC [SRC]... DEST
  or   rsync [OPTION]... SRC [SRC]... [USER@]HOST:DEST
  or   rsync [OPTION]... [USER@]HOST:SRC [DEST]
The ':' usages connect via remote shell, while '::' & 'rsync://' usages connect
to an rsync daemon, and require SRC or DEST to start with a module name.

Options
`

const help269 = helpHead + ` -v, --verbose               increase verbosity
 -a, --archive               archive mode; same as -rlptgoD (no -H)
     --no-OPTION             turn off an implied OPTION (e.g. --no-D)
 -r, --recursive             recurse into directories
 -R, --relative              use relative path names
 -t, --times                 preserve times
     --chmod=CHMOD           change destination permissions
 -e, --rsh=COMMAND           specify the remote shell to use
     --rsync-path=PROGRAM    specify the rsync to run on the remote machine
     --max-size=SIZE         don't transfer any file larger than SIZE
     --timeout=TIME          set I/O timeout in seconds
     --files-from=FILE       read list of source-file names from FILE
 -0, --from0                 all *-from/filter files are delimited by 0s
     --progress              show progress during transfer
 -h, --help                  show this help (-h works with no other options)

Use "rsync --daemon --help" to see the daemon-mode command-line options.
`

const help309 = helpHead + ` -v, --verbose               increase verbosity
 -a, --archive               archive mode; equals -rlptgoD (no -H,-A,-X)
     --no-OPTION             turn off an implied OPTION (e.g. --no-D)
 -r, --recursive             recurse into directories
 -t, --times                 preserve modification times
     --chmod=CHMOD           affect file and/or directory permissions
 -e, --rsh=COMMAND           specify the remote shell to use
     --rsync-path=PROGRAM    specify the rsync to run on the remote machine
     --max-size=SIZE         don't transfer any file larger than SIZE
     --timeout=SECONDS       set I/O timeout in seconds
     --files-from=FILE       read list of source-file names from FILE
 -0, --from0                 all *-from/filter files are delimited by 0s
 -s, --protect-args          no space-splitting; only wildcard special-chars
     --progress              show progress during transfer
 -h, --help                  show this help (-h is --help only if used alone)
`

const help313 = helpHead + ` -v, --verbose               increase verbosity
     --info=FLAGS            fine-grained informational verbosity
 -a, --archive               archive mode; equals -rlptgoD (no -H,-A,-X)
 -r, --recursive             recurse into directories
 -t, --times                 preserve modification times
     --chmod=CHMOD           affect file and/or directory permissions
 -e, --rsh=COMMAND           specify the remote shell to use
     --rsync-path=PROGRAM    specify the rsync to run on the remote machine
     --max-size=SIZE         don't transfer any file larger than SIZE
     --timeout=SECONDS       set I/O timeout in seconds
     --files-from=FILE       read list of source-file names from FILE
 -0, --from0                 all *-from/filter files are delimited by 0s
 -s, --protect-args          no space-splitting; only wildcard special-chars
 -h, --help                  show this help (-h is --help only if used alone)
`

const help323 = helpHead + `--verbose, -v            increase verbosity
--info=FLAGS             fine-grained informational verbosity
--archive, -a            archive mode; equals -rlptgoD (no -H,-A,-X)
--recursive, -r          recurse into directories
--times, -t              preserve modification times
--chmod=CHMOD            affect file and/or directory permissions
--rsh=COMMAND, -e        specify the remote shell to use
--rsync-path=PROGRAM     specify the rsync to run on remote machine
--max-size=SIZE          don't transfer any file larger than SIZE
--timeout=SECONDS        set I/O timeout in seconds
--files-from=FILE        read list of source-file names from FILE
--from0, -0              all *-from/filter files are delimited by 0s
--protect-args, -s       no space-splitting; wildcard chars only
--help, -h (*)           show this help (* -h is help only on its own)
`

// help327 is 3.2.7's: --old-args (3.2.4), --trust-sender (3.2.5) and
// --secluded-args, the new name of --protect-args.
const help327 = helpHead + `--verbose, -v            increase verbosity
--info=FLAGS             fine-grained informational verbosity
--archive, -a            archive mode is -rlptgoD (no -A,-X,-U,-N,-H)
--recursive, -r          recurse into directories
--times, -t              preserve modification times
--omit-dir-times, -O     omit directories from --times
--chmod=CHMOD            affect file and/or directory permissions
--rsh=COMMAND, -e        specify the remote shell to use
--rsync-path=PROGRAM     specify the rsync to run on remote machine
--max-size=SIZE          don't transfer any file larger than SIZE
--timeout=SECONDS        set I/O timeout in seconds
--files-from=FILE        read list of source-file names from FILE
--from0, -0              all *-from/filter files are delimited by 0s
--old-args               disable the modern arg-protection idiom
--secluded-args, -s      use the protocol to safely send the args
--trust-sender           trust the remote sender's file list
--help, -h (*)           show this help (* -h is help only on its own)
`

// openrsync as macOS ships it: a usage synopsis on standard error.
const helpOpenApple = `usage: rsync [-0468BCDEFHILOPRSTWVabcdefghiklmnopqrtuvxyz] [-e program] [-f filter]
	[--address=sourceaddr] [--append] [--backup-dir=directory]
	[--bwlimit=limit] [--cache | --no-cache] [--checksum-seed=NUM]
	[--chmod=MODE] [--compare-dest=directory] [--compress-level=NUM]
	[--contimeout=seconds] [--copy-dest=directory] [--del]
	[--exclude pattern] [--exclude-from=file] [--files-from=filespec]
	[--include pattern] [--include-from=file] [--link-dest=directory]
	[--max-delete=MAX] [--max-size=size] [--min-size=size]
	[--no-motd] [--numeric-ids] [--partial] [--port=service]
	[--progress] [--protocol=VER] [--rsync-path=program]
	[--size-only] [--stats] [--timeout=seconds]
	source ... directory
`

// openrsync as OpenBSD ships it: no --files-from or --chmod.
const helpOpenBSD = `usage: openrsync [-aDglnoprtvx] [-e program] [--address=sourceaddr]
	[--compare-dest=dir] [--del] [--exclude] [--exclude-from=file]
	[--include] [--include-from=file] [--no-motd] [--numeric-ids]
	[--port=portnumber] [--rsync-path=program] [--timeout=seconds]
	[--max-size=SIZE] [--min-size=SIZE] source ... directory
`

func gnuVersion(v string, proto int) string {
	return "rsync  version " + v + "  protocol version " + itoa(proto) + "\nCopyright (C) 1996-2022 by Andrew Tridgell, Wayne Davison, and others.\nWeb site: https://rsync.samba.org/\nCapabilities:\n    64-bit files, 64-bit inums, 64-bit timestamps, 64-bit long ints,\n"
}

var fixtures = map[string]rsyncFixture{
	"2.6.9":         {gnuVersion("2.6.9", 29), help269, false},
	"3.0.9":         {gnuVersion("3.0.9", 30), help309, false},
	"3.1.3":         {gnuVersion("3.1.3", 31), help313, false},
	"3.1.3+trust":   {gnuVersion("3.1.3", 31), help313, true}, // RHEL 8 3.1.3-23 and later: the option, no help line
	"3.2.3":         {gnuVersion("3.2.3", 31), help323, false},
	"3.2.7":         {gnuVersion("3.2.7", 31), help327, true},
	"3.4.1":         {gnuVersion("3.4.1", 32), help327, true},
	"openrsync":     {"openrsync: protocol version 29\nrsync version 2.6.9 compatible\n", helpOpenApple, false},
	"openrsync-bsd": {"openrsync: protocol version 27\n", helpOpenBSD, false},
}

func fixtureCaps(t *testing.T, name string) rsyncCaps {
	t.Helper()
	f, ok := fixtures[name]
	if !ok {
		t.Fatalf("no fixture %s", name)
	}
	c, ok := parseRsyncCaps(f.version, f.help, f.trust)
	if !ok {
		t.Fatalf("%s: no version", name)
	}
	return c
}

func TestRsyncCapsFromHelp(t *testing.T) {
	type want struct {
		ver                                    string
		protect, incRec, info, escapes, checks bool
		from0, lacks                           string
	}
	for name, w := range map[string]want{
		"2.6.9":         {"2.6.9", false, false, false, false, false, "--from0", ""},
		"3.0.9":         {"3.0.9", true, true, false, false, false, "--from0", ""},
		"3.1.3":         {"3.1.3", true, true, true, false, false, "--from0", ""},
		"3.1.3+trust":   {"3.1.3", true, true, true, false, true, "--from0", ""},
		"3.2.3":         {"3.2.3", true, true, true, false, false, "--from0", ""},
		"3.2.7":         {"3.2.7", true, true, true, true, true, "--from0", ""},
		"3.4.1":         {"3.4.1", true, true, true, true, true, "--from0", ""},
		"openrsync":     {"2.6.9", false, false, false, false, false, "-0", ""},
		"openrsync-bsd": {"2.6.9", false, false, false, false, false, "", "--files-from"},
	} {
		c := fixtureCaps(t, name)
		got := want{c.version(), c.protectArgs(), c.incRecursive(), c.has("info"), c.escapesArgs(), c.checksSender(), c.from0(), c.lacks(rsyncNeeds...)}
		if got != w {
			t.Errorf("%s: got %+v, want %+v", name, got, w)
		}
	}
	// A description that names an option does not count as having it.
	if c := fixtureCaps(t, "2.6.9"); c.short['H'] || c.short['D'] {
		t.Error("read an option from a description")
	}
	if c := fixtureCaps(t, "3.2.7"); c.has("times") != true || c.short['t'] != true || c.short['O'] != true {
		t.Errorf("3.2.7 short options %v", c.short)
	}
}

// The paths the route table uses: the destination root holds spaces, a
// single quote, $() and wildcards.
const (
	tRoot  = `/data/it's $(id) *[1]`
	tStage = tRoot + "/.stage-1/"
	tList  = "/tmp/l/files"
	tRsh   = "/tmp/l/rsh"
)

var remoteSSHHostForTest = remote.SSHHost{Destination: "user@dst.example.com"}

var envU = []string{"env", "-u", "RSYNC_OLD_ARGS", "-u", "RSYNC_PROTECT_ARGS"}

// argvFor decides a route from two fixtures and builds its command line:
// for here and onhost only a (the rsync that runs); for push, a is this
// computer and b the host; for pull, a is this computer and b the host it
// pulls from; for hostToHost, a is the source host and b the destination.
func argvFor(t *testing.T, kind, a, b string) ([]string, string) {
	t.Helper()
	client := fixtureCaps(t, a)
	var far *rsyncCaps
	if b != "" {
		c := fixtureCaps(t, b)
		far = &c
	}
	o, why := rsyncDecide(kind, client, far, "this computer", "host-b.example.com")
	if why != "" {
		return nil, why
	}
	c := rsyncCmd{kind: kind, o: o, program: "rsync", largest: 2048, preserve: true, farRsync: "rsync"}
	switch kind {
	case "here":
		c.listFile, c.from, c.to = tList, "/src/a b/", "/home/me/Downloads/.stage-1/"
	case "push":
		c.listFile, c.rsh, c.from, c.to = tList, tRsh, "/src/a b/", tStage
	case "pull":
		c.listFile, c.rsh, c.from, c.to = tList, tRsh, tRoot+"/", "/home/me/kb/.stage-1/"
	case "onhost":
		c.from, c.to = "/data/a/", "/data/b/.stage-1/"
	case "hostToHost":
		c.from, c.to = "/data/a/", tStage
		c.dstHost = &remoteSSHHostForTest
	}
	return c.argv(), ""
}

func TestRsyncArgvAcrossVersions(t *testing.T) {
	quoted := `'/data/it'\''s $(id) *[1]/.stage-1/'`
	escaped := `/data/it's $(id) \*\[1\]/.stage-1/`
	quotedRoot := `'/data/it'\''s $(id) *[1]/'`
	escapedRoot := `/data/it's $(id) \*\[1\]/`
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	s := func(a ...string) []string { return a }
	chmod := "--chmod=Du+rwx,Fu+rw"
	ms, to, nir := "--max-size=2048", "--timeout=120", "--no-inc-recursive"
	fl, stdin, prog := "--files-from="+tList, "--files-from=-", "--info=progress2"
	rsh := "--rsh=" + tRsh
	ssh := s("-e", "ssh -o BatchMode=yes", "/data/a/")
	cve := "CVE-2022-29154"
	tests := []struct {
		kind, a, b string
		want       []string
		refused    string
	}{
		// This computer 2.6.9, the host 3.1.3.
		{kind: "here", a: "2.6.9", want: s("rsync", chmod, ms, to, "--from0", "-t", fl, "/src/a b/", "/home/me/Downloads/.stage-1/")},
		{kind: "push", a: "2.6.9", b: "3.1.3", want: s("rsync", chmod, ms, to, "--from0", "-t", fl, rsh, "/src/a b/", "studio:"+quoted)},
		{kind: "pull", a: "2.6.9", b: "3.1.3", refused: cve},
		{kind: "onhost", a: "3.1.3", want: cat(envU, s("rsync", chmod, ms, to, nir, "--from0", "-t", stdin, "/data/a/", "/data/b/.stage-1/"))},
		{kind: "hostToHost", a: "2.6.9", b: "3.1.3", want: cat(envU, s("rsync", chmod, ms, to, "--from0", "-t", stdin), ssh, s("user@dst.example.com:"+quoted))},
		// This computer openrsync, the host 3.2.7.
		{kind: "here", a: "openrsync", want: s("rsync", chmod, ms, to, "-0", "-t", fl, "/src/a b/", "/home/me/Downloads/.stage-1/")},
		{kind: "push", a: "openrsync", b: "3.2.7", want: s("rsync", chmod, ms, to, "-0", "-t", fl, rsh, "/src/a b/", "studio:"+quoted)},
		{kind: "pull", a: "openrsync", b: "3.2.7", refused: cve},
		{kind: "onhost", a: "3.2.7", want: cat(envU, s("rsync", chmod, ms, to, nir, "--from0", "-t", stdin, "/data/a/", "/data/b/.stage-1/"))},
		{kind: "hostToHost", a: "openrsync", b: "3.2.7", want: cat(envU, s("rsync", chmod, ms, to, "-0", "-t", stdin), ssh, s("user@dst.example.com:"+quoted))},
		// This computer 3.4.1, the host 2.6.9: no -s, and 3.4.1 escapes
		// the folder itself but for its wildcards.
		{kind: "here", a: "3.4.1", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, "/src/a b/", "/home/me/Downloads/.stage-1/")},
		{kind: "push", a: "3.4.1", b: "2.6.9", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "/src/a b/", "studio:"+escaped)},
		{kind: "pull", a: "3.4.1", b: "2.6.9", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+escapedRoot, "/home/me/kb/.stage-1/")},
		{kind: "onhost", a: "2.6.9", want: cat(envU, s("rsync", chmod, ms, to, "--from0", "-t", stdin, "/data/a/", "/data/b/.stage-1/"))},
		{kind: "hostToHost", a: "3.4.1", b: "2.6.9", want: cat(envU, s("rsync", chmod, ms, to, nir, "--from0", "-t", stdin), ssh, s("user@dst.example.com:"+escaped))},
		// This computer 3.1.3 with --trust-sender (a backport), the host
		// 3.0.9: both have -s, and the backport may pull.
		{kind: "here", a: "3.1.3+trust", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, "/src/a b/", "/home/me/Downloads/.stage-1/")},
		{kind: "push", a: "3.1.3+trust", b: "3.0.9", want: s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "/src/a b/", "studio:"+tStage)},
		{kind: "pull", a: "3.1.3+trust", b: "3.0.9", want: s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+tRoot+"/", "/home/me/kb/.stage-1/")},
		{kind: "onhost", a: "3.0.9", want: cat(envU, s("rsync", chmod, ms, to, nir, "--from0", "-t", stdin, "/data/a/", "/data/b/.stage-1/"))},
		{kind: "hostToHost", a: "3.1.3+trust", b: "3.0.9", want: cat(envU, s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", stdin), ssh, s("user@dst.example.com:"+tStage))},
		// Both ends 3.4.1.
		{kind: "here", a: "3.4.1", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, "/src/a b/", "/home/me/Downloads/.stage-1/")},
		{kind: "push", a: "3.4.1", b: "3.4.1", want: s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "/src/a b/", "studio:"+tStage)},
		{kind: "pull", a: "3.4.1", b: "3.4.1", want: s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+tRoot+"/", "/home/me/kb/.stage-1/")},
		{kind: "onhost", a: "3.4.1", want: cat(envU, s("rsync", chmod, ms, to, nir, "--from0", "-t", stdin, "/data/a/", "/data/b/.stage-1/"))},
		{kind: "hostToHost", a: "3.4.1", b: "3.4.1", want: cat(envU, s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", stdin), ssh, s("user@dst.example.com:"+tStage))},
		// More releases here.
		{kind: "pull", a: "3.0.9", b: "3.4.1", refused: cve},
		{kind: "pull", a: "3.1.3", b: "3.4.1", refused: cve},
		{kind: "pull", a: "3.2.3", b: "3.4.1", refused: cve},
		{kind: "pull", a: "3.2.7", b: "3.2.3", want: s("rsync", "-s", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+tRoot+"/", "/home/me/kb/.stage-1/")},
		{kind: "push", a: "3.2.3", b: "2.6.9", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "/src/a b/", "studio:"+quoted)},
		{kind: "push", a: "3.0.9", b: "openrsync", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, rsh, "/src/a b/", "studio:"+quoted)},
		{kind: "pull", a: "3.4.1", b: "openrsync", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+escapedRoot, "/home/me/kb/.stage-1/")},
		// A backport here pulling from 2.6.9: no -s, and the folder quoted.
		{kind: "pull", a: "3.1.3+trust", b: "2.6.9", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "studio:"+quotedRoot, "/home/me/kb/.stage-1/")},
		// OpenBSD's openrsync has no --files-from, here or as a sender.
		{kind: "here", a: "openrsync-bsd", refused: "has no --files-from"},
		{kind: "pull", a: "3.4.1", b: "openrsync-bsd", refused: "has no --files-from, which a pull needs there"},
		{kind: "push", a: "3.4.1", b: "openrsync-bsd", want: s("rsync", chmod, ms, to, nir, "--from0", "-t", fl, prog, rsh, "/src/a b/", "studio:"+escaped)},
	}
	for _, tc := range tests {
		got, why := argvFor(t, tc.kind, tc.a, tc.b)
		name := tc.kind + " " + tc.a + " " + tc.b
		if tc.refused != "" {
			if why == "" || !strings.Contains(why, tc.refused) {
				t.Errorf("%s: want refusal with %q, got %q %q", name, tc.refused, why, got)
			}
			continue
		}
		if why != "" {
			t.Errorf("%s: refused: %s", name, why)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
	// A pull that 2.6.9 cannot make says why and what carries it.
	_, why := argvFor(t, "pull", "2.6.9", "3.4.1")
	if want := "the rsync on this computer (rsync 2.6.9) trusts what a host sends (CVE-2022-29154); a pull needs rsync 3.2.5 or a build with the CVE-2022-29154 fix, so studio-copy carries the transfer"; why != want {
		t.Errorf("reason %q", why)
	}
}

// A quoted or escaped folder comes through a POSIX shell as it was.
func TestRemotePathThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, p := range []string{
		`/data/it's $(id) *[1]/`,
		`/data/a b/''/"q"/$HOME/` + "`id`/;|&<>/",
		`/data/back\slash/~x/!x/{a,b}/back\*/`,
		`/plain/`,
	} {
		for _, o := range []rsyncOpts{{}, {escapes: true}} {
			arg := o.remotePath(p)
			if o.escapes {
				// rsync 3.2.4+ adds the shell escapes itself; do the
				// same here as safe_arg does for a file argument.
				arg = safeArgLike(arg)
			}
			out, err := exec.Command(sh, "-c", "printf '%s' "+arg).Output()
			if err != nil || string(out) != p {
				t.Errorf("%q (escapes %v): shell gave %q, %v", p, o.escapes, out, err)
			}
		}
	}
}

// safeArgLike is safe_arg() in rsync 3.2.4+'s options.c for a file
// argument: a backslash before each shell character, wildcards left as
// they are, and a backslash kept as it is before a wildcard.
func safeArgLike(s string) string {
	const shellChars = "!#$&;|<>(){}\"' \t\n\r\\`"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' {
			if !(i+1 < len(s) && strings.IndexByte("*?[]", s[i+1]) >= 0) {
				b.WriteByte('\\')
			}
		} else if strings.IndexByte(shellChars, ch) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// fakeRsync is a script that answers --version and --help as a fixture.
func fakeRsync(t *testing.T, dir, name, fixture string) string {
	t.Helper()
	f := fixtures[fixture]
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, name+".version"), []byte(f.version), 0o644)
	os.WriteFile(filepath.Join(dir, name+".help"), []byte(f.help), 0o644)
	trust := "exit 1"
	if f.trust {
		trust = "exit 0"
	}
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\ncase \"$1\" in\n--version) cat '" + p + ".version' ;;\n--help) cat '" + p + ".help' ;;\n--trust-sender) " + trust + " ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// On a Mac with the default setting, Homebrew's rsync is found before
// PATH's and the best one is used; an explicit path is used as it is.
func TestRsyncFindsHomebrew(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	brew := fakeRsync(t, filepath.Join(dir, "opt", "homebrew", "bin"), "rsync", "3.4.1")
	intel := fakeRsync(t, filepath.Join(dir, "usr", "local", "bin"), "rsync", "3.2.3")
	system := fakeRsync(t, filepath.Join(dir, "usr", "bin"), "rsync", "openrsync")
	oldGOOS, oldBrew := rsyncGOOS, brewRsync
	t.Cleanup(func() { rsyncGOOS, brewRsync = oldGOOS, oldBrew })
	rsyncGOOS, brewRsync = "darwin", []string{brew, intel}

	r := newRig(t)
	r.env["PATH"] = filepath.Dir(system)
	m := r.s.rsyncMover()
	set := DefaultSettings()
	ctx := context.Background()
	if got := rsyncCandidates(r.s.d.Getenv, "rsync"); !reflect.DeepEqual(got, []string{brew, intel, system}) {
		t.Fatalf("candidates %q", got)
	}
	p, c, why := m.local(ctx, set)
	if why != "" || p != brew || c.version() != "3.4.1" {
		t.Fatalf("picked %q %s %q", p, c.version(), why)
	}
	// Without the Apple silicon one, the Intel one (3.2.3, which cannot
	// pull) still beats the system's openrsync.
	brewRsync = []string{filepath.Join(dir, "none"), intel}
	if p, _, _ := m.local(ctx, set); p != intel {
		t.Fatalf("picked %q", p)
	}
	if got := localUse(fixtureCaps(t, "3.2.3"), intel); got != "rsync 3.2.3 at "+intel+": used on this computer and for sends to hosts; pulls use Studio copy (needs rsync 3.2.5 or a build with the CVE-2022-29154 fix)" {
		t.Errorf("text %q", got)
	}
	// An explicit path is respected, even when Homebrew's is better.
	set.Movers.Rsync.Path = system
	if p, c, _ := m.local(ctx, set); p != system || !c.openrsync {
		t.Fatalf("explicit path gave %q", p)
	}
	// Elsewhere than a Mac, PATH decides.
	rsyncGOOS = "linux"
	if got := rsyncCandidates(r.s.d.Getenv, "rsync"); !reflect.DeepEqual(got, []string{system}) {
		t.Fatalf("linux candidates %q", got)
	}
	// Settings shows the program found and what it is used for.
	rsyncGOOS, brewRsync = "darwin", []string{brew}
	d := r.s.detect(ctx, single, r.s.settingsFor(single))
	if !d.RsyncLocal || d.RsyncPath != brew || d.RsyncVersion != "3.4.1" || d.RsyncText != "rsync 3.4.1 at "+brew+": used on this computer, for sends to hosts and for pulls from hosts" {
		t.Fatalf("detected %+v", d)
	}
}

func TestRsyncUseTexts(t *testing.T) {
	for _, tc := range []struct{ fixture, local, host string }{
		{"2.6.9", "rsync 2.6.9 at /usr/bin/rsync: used on this computer and for sends to hosts; pulls use Studio copy (needs rsync 3.2.5 or a build with the CVE-2022-29154 fix)",
			"rsync 2.6.9 there: used for sends and pulls with this computer and for copies on that host"},
		{"openrsync", "openrsync (rsync 2.6.9 compatible) at /usr/bin/rsync: used on this computer and for sends to hosts; pulls use Studio copy (needs rsync 3.2.5 or a build with the CVE-2022-29154 fix)",
			"openrsync (rsync 2.6.9 compatible) there: used for sends and pulls with this computer and for copies on that host"},
		{"3.1.3+trust", "rsync 3.1.3 at /usr/bin/rsync: used on this computer, for sends to hosts and for pulls from hosts",
			"rsync 3.1.3 there: used for sends and pulls with this computer and for copies on that host"},
		{"openrsync-bsd", "openrsync (rsync 2.6.9 compatible) at /usr/bin/rsync: not used, it has no --files-from; Studio copy carries transfers",
			"openrsync (rsync 2.6.9 compatible) there: not used, it has no --files-from; Studio copy carries transfers"},
	} {
		c := fixtureCaps(t, tc.fixture)
		if got := localUse(c, "/usr/bin/rsync"); got != tc.local {
			t.Errorf("%s here: %q", tc.fixture, got)
		}
		if got := hostUse(c); got != tc.host {
			t.Errorf("%s there: %q", tc.fixture, got)
		}
	}
}
