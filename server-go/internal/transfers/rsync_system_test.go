package transfers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemRsync runs the rsync this system ships (STUDIO_TEST_RSYNC, else
// /usr/bin/rsync: 3.x on Linux, 2.6.9 or openrsync on macOS) with the
// command lines Studio builds for it: a copy on this computer, and a send
// and a pull through a stand-in remote shell (sh -c over the joined words,
// as ssh does) with the same program at the far end, both with protect
// args where it has them and with the folder quoted for the shell. The
// destination root holds spaces, a single quote, $() and wildcards.
func TestSystemRsync(t *testing.T) {
	prog := os.Getenv("STUDIO_TEST_RSYNC")
	if prog == "" {
		prog = "/usr/bin/rsync"
	}
	if !runnable(prog) {
		t.Skip("no " + prog)
	}
	ctx := context.Background()
	p, ok := probeLocalRsync(ctx, prog)
	if !ok {
		t.Fatalf("%s did not run", prog)
	}
	c, ok := parseRsyncCaps(p.version, p.help, p.trust)
	if !ok {
		t.Fatalf("no version in %q", p.version)
	}
	first := func(s string) string { l, _, _ := strings.Cut(strings.TrimSpace(s), "\n"); return l }
	t.Logf("%s: %q; %s; protect args %v, from0 %q, info %v, escapes %v, checks sender %v, lacks %q",
		prog, first(p.version), localUse(c, prog), c.protectArgs(), c.from0(), c.has("info"), c.escapesArgs(), c.checksSender(), c.lacks(rsyncNeeds...))
	if testing.Verbose() {
		t.Logf("--help:\n%s", p.help)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	names := []string{"plain.txt", "a b.txt", "it's.txt", "$(echo x).txt", "star*.txt", "sub dir/in'side.txt"}
	var list []byte
	for _, n := range names {
		os.MkdirAll(filepath.Dir(filepath.Join(src, n)), 0o755)
		if err := os.WriteFile(filepath.Join(src, n), []byte("data "+n), 0o644); err != nil {
			t.Fatal(err)
		}
		list = append(append(list, n...), 0)
	}
	listFile := filepath.Join(dir, "files")
	os.WriteFile(listFile, list, 0o600)
	rsh := filepath.Join(dir, "rsh")
	os.WriteFile(rsh, []byte("#!/bin/sh\nshift\nexec sh -c \"$*\"\n"), 0o755)

	run := func(name string, cmd rsyncCmd, dst string, env ...string) {
		t.Helper()
		cmd.largest = 1 << 20
		argv := cmd.argv()
		x := exec.Command(argv[0], argv[1:]...)
		x.Env = append(rsyncEnv(os.Environ()), env...)
		out, err := x.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%q\n%s", name, err, argv, out)
		}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dst, n))
			if err != nil || string(b) != "data "+n {
				t.Fatalf("%s: %s: %q %v\n%q\n%s", name, n, b, err, argv, out)
			}
		}
		// Nothing landed beside the destination (a split or expanded path).
		if ents, _ := os.ReadDir(filepath.Dir(filepath.Dir(dst))); len(ents) != 1 {
			var got []string
			for _, e := range ents {
				got = append(got, e.Name())
			}
			t.Fatalf("%s: beside the destination: %q", name, got)
		}
		t.Logf("%s: ok: %q", name, argv)
	}
	root := func(name string) string {
		r := filepath.Join(dir, name, `dst it's $(echo x) *[1]`)
		os.MkdirAll(r, 0o755)
		return r
	}

	o, why := rsyncDecide("here", c, nil, "this computer", "")
	if why != "" {
		t.Skip("rsync is not used here: " + why)
	}
	r := root("here")
	run("here", rsyncCmd{kind: "here", o: o, program: prog, listFile: listFile, from: src + "/", to: r + "/stage/"}, r+"/stage")

	push, why := rsyncDecide("push", c, &c, "this computer", "the host")
	if why != "" {
		t.Fatalf("push refused: %s", why)
	}
	variants := []rsyncOpts{push}
	if push.protect {
		quoted := push
		quoted.protect = false
		variants = append(variants, quoted)
	}
	for i, o := range variants {
		name := "push protect=" + itoa(boolInt(o.protect))
		r := root("push" + itoa(i))
		run(name, rsyncCmd{kind: "push", o: o, program: prog, listFile: listFile, rsh: rsh, farRsync: prog, from: src + "/", to: r + "/stage/"}, r+"/stage")
	}
	if push.escapes {
		// The single-quoted form older clients get, through this newer
		// one with its own escaping off (RSYNC_OLD_ARGS=1, as --old-args).
		o := push
		o.protect, o.escapes = false, false
		r := root("push-quoted")
		run("push quoted", rsyncCmd{kind: "push", o: o, program: prog, listFile: listFile, rsh: rsh, farRsync: prog, from: src + "/", to: r + "/stage/"}, r+"/stage", "RSYNC_OLD_ARGS=1")
	}

	pull, why := rsyncDecide("pull", c, &c, "this computer", "the host")
	if why != "" {
		t.Logf("pull refused, as expected for this rsync: %s", why)
		return
	}
	// The pull's source is the awkward folder this time.
	far := root("pullsrc")
	if err := exec.Command("cp", "-R", src+"/.", far).Run(); err != nil {
		t.Fatal(err)
	}
	variants = []rsyncOpts{pull}
	if pull.protect {
		quoted := pull
		quoted.protect = false
		variants = append(variants, quoted)
	}
	for i, o := range variants {
		name := "pull protect=" + itoa(boolInt(o.protect))
		dst := filepath.Join(dir, "pull"+itoa(i), "kb")
		os.MkdirAll(dst, 0o755)
		run(name, rsyncCmd{kind: "pull", o: o, program: prog, listFile: listFile, rsh: rsh, farRsync: prog, from: far + "/", to: dst + "/stage/"}, dst+"/stage")
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
