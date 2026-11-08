package userlibs

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// remotePdfSshd.test.mjs against the Go routes: libraries built on other
// systems over real ssh, one sshd per stand-in host, run by this user on a
// loopback port, each login forced into a HOME and PATH of its own. Nothing
// is downloaded: where the Node file has the Studio send pypdf, a pdftotext
// stand-in on the host's PATH is the reader. Each case is confirmed by
// counting rows in the index itself. Skipped unless STUDIO_TEST_SSHD=1.
//
//   STUDIO_TEST_SSHD=1 go test -run TestRemotePdf ./internal/userlibs/

const pdfBody = "The zeppelin hangar manual describes mooring procedures for the airship crew"

// pdfOf is a one-page PDF with a text layer.
func pdfOf(text string) string {
	stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream), "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	out := "%PDF-1.4\n"
	var at []int
	for i, o := range objs {
		at = append(at, len(out))
		out += fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := len(out)
	out += fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, n := range at {
		out += fmt.Sprintf("%010d 00000 n \n", n)
	}
	return out + fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
}

type sshdHost struct {
	port      int
	home, bin string
}

type sshdRig struct {
	*harness
	dir, user, gufi, python, options, sshd string
	next                                   int
}

func newSshdRig(t *testing.T) *sshdRig {
	if os.Getenv("STUDIO_TEST_SSHD") != "1" {
		t.Skip("set STUDIO_TEST_SSHD=1 to start local sshd processes")
	}
	sshd := ""
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			sshd = p
		}
	}
	python := ""
	for _, p := range []string{"/usr/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(p); err == nil && python == "" {
			python = p
		}
	}
	if sshd == "" || python == "" {
		t.Skip("no sshd or python3 on this host")
	}
	gufi := needGufi(t)
	h := newHarness(t, nil)
	// Short, for ssh's control sockets.
	run, err := os.MkdirTemp("", "ulssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	h.env["XDG_RUNTIME_DIR"] = run
	h.env["PATH"] = os.Getenv("PATH")
	h.env["HOME"] = os.Getenv("HOME")
	// The user's own ssh: no stand-in Spawn, no stand-in local ssh.
	h.m.Pool.Close()
	h.m.Pool = remote.New(remote.Options{IndexBase: h.m.IndexBase, Getenv: func(k string) string { return h.env[k] }})
	t.Cleanup(h.m.Pool.Close)
	h.m.Runners = &remote.Runners{Pool: h.m.Pool}
	h.m.eng = nil
	u, _ := user.Current()
	dir := h.dir
	for _, f := range []string{"id", "host"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, f)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "id.pub"))
	os.WriteFile(filepath.Join(dir, "authorized_keys"), b, 0o600)
	r := &sshdRig{harness: h, dir: dir, user: u.Username, gufi: gufi, python: python, next: 5990}
	r.options = fmt.Sprintf("-i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s -o StrictHostKeyChecking=no -o ConnectTimeout=10", filepath.Join(dir, "id"), filepath.Join(dir, "kh"))
	r.sshd = sshd
	return r
}

func (r *sshdRig) freePort() int {
	for ; r.next <= 5999; r.next++ {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(r.next))
		if err == nil {
			l.Close()
			r.next++
			return r.next - 1
		}
	}
	r.t.Fatal("no free port in 5990-5999")
	return 0
}

var leaveOut = regexp.MustCompile(`^(python.*|pydoc.*|pip.*|pdftotext|pdfinfo|pdftoppm|tesseract|soffice|libreoffice)$`)

// host is a host of its own: every tool here on its PATH but Python, the
// poppler tools and the office ones; a Python that sees no site-packages
// unless noPython; a pdftotext stand-in with poppler.
func (r *sshdRig) host(name string, noPython, poppler bool) sshdHost {
	t := r.t
	h := filepath.Join(r.dir, name)
	bin, home := filepath.Join(h, "bin"), filepath.Join(h, "home")
	os.MkdirAll(bin, 0o755)
	for _, d := range []string{"/bin", "/usr/bin"} {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			n := e.Name()
			if _, err := os.Lstat(filepath.Join(bin, n)); err == nil || leaveOut.MatchString(n) {
				continue
			}
			os.Symlink(filepath.Join(d, n), filepath.Join(bin, n))
		}
	}
	if !noPython {
		r.python3(bin)
	}
	if poppler {
		r.pdftotext(bin)
	}
	write(t, filepath.Join(home, "docs", "manuals", "hangar.pdf"), pdfOf(pdfBody))
	write(t, filepath.Join(home, "docs", "notes.md"), "# Notes\nA short note about the schedule.\n")
	exe(t, filepath.Join(h, "login.sh"), "#!/bin/bash\nexport HOME="+home+" PATH="+bin+"\n"+strings.Join(prefixAll("export ", noNetwork), "\n")+
		"\nunset PYTHONPATH\nif [ -n \"$SSH_ORIGINAL_COMMAND\" ]; then exec bash -c \"$SSH_ORIGINAL_COMMAND\"; fi\nexec bash\n")
	port := r.freePort()
	conf := filepath.Join(h, "sshd.conf")
	write(t, conf, fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\nForceCommand %s\n",
		port, filepath.Join(r.dir, "host"), filepath.Join(h, "sshd.pid"), filepath.Join(r.dir, "authorized_keys"), filepath.Join(h, "login.sh")))
	d := exec.Command(r.sshd, "-D", "-e", "-f", conf)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	// Only the daemon this test started is stopped.
	t.Cleanup(func() { d.Process.Kill(); d.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return sshdHost{port, home, bin}
}

func prefixAll(p string, a []string) []string {
	out := make([]string, len(a))
	for i, s := range a {
		out[i] = p + s
	}
	return out
}

func (r *sshdRig) python3(bin string) {
	exe(r.t, filepath.Join(bin, "python3"), "#!/bin/sh\nexec "+r.python+" -S \"$@\"\n")
}

// pdftotext stands in for poppler's: the body for each PDF but scan.pdf,
// which has no text layer.
func (r *sshdRig) pdftotext(bin string) {
	exe(r.t, filepath.Join(bin, "pdftotext"), "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in *scan.pdf) exit 0 ;; esac; done\necho '"+pdfBody+"'\n")
}

func (r *sshdRig) build(name string, h sshdHost, depth string) map[string]any {
	r.t.Helper()
	a := r.post("/api/user-libraries/ssh/build", map[string]any{
		"destination": r.user + "@127.0.0.1", "port": h.port, "sourceRoot": filepath.Join(h.home, "docs"), "indexRoot": filepath.Join(h.home, "docs-index"),
		"gufiBin": r.gufi, "label": name, "create": true, "depth": depth, "sshOptions": r.options,
	})
	if a.Status != 200 {
		r.t.Fatalf("build %s: %d %v", name, a.Status, a.Body)
	}
	return r.waitFor(a.Body["id"].(string))
}

func (r *sshdRig) rebuild(id string) map[string]any {
	r.t.Helper()
	time.Sleep(1100 * time.Millisecond)
	a := r.post("/api/user-libraries/"+id+"/refresh", nil)
	if a.Status != 200 {
		r.t.Fatalf("rebuild: %d %v", a.Status, a.Body)
	}
	// Past "updating", to the finished state.
	for i := 0; i < 1200; i++ {
		if l := r.library(id); l != nil && l["state"] != "updating" && l["state"] != "building" {
			return l
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("%s did not finish", id)
	return nil
}

// pdfRows is the PDF rows in the index's own words table.
func (r *sshdRig) pdfRows(root string) int {
	r.t.Helper()
	out, err := exec.Command(r.python, "-I", "-c", `
import os, sqlite3, sys
n = 0
for d, _, fs in os.walk(sys.argv[1]):
    if 'db.db' in fs:
        db = sqlite3.connect('file:' + os.path.join(d, 'db.db') + '?mode=ro', uri=True)
        if db.execute("select 1 from sqlite_master where name = 'words'").fetchone():
            n += db.execute("select count(*) from words where fname like '%.pdf'").fetchone()[0]
print(n)
`, root).Output()
	if err != nil {
		r.t.Fatal(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func gapsOf(l map[string]any) []string {
	out := []string{}
	for _, g := range l["gaps"].([]any) {
		out = append(out, g.(string))
	}
	return out
}

func readyOrFail(t *testing.T, l map[string]any) {
	t.Helper()
	if l["state"] != "ready" {
		t.Fatalf("not ready: %v", l["error"])
	}
}

func TestRemotePdfOverRealSSH(t *testing.T) {
	r := newSshdRig(t)
	t.Run("a host with pdftotext reads its PDFs with it, and has no gaps", func(t *testing.T) {
		r.t = t
		l := r.build("poppler", r.host("poppler", false, true), "text")
		readyOrFail(t, l)
		eq(t, r.pdfRows(l["indexRoot"].(string)), 1, "pdf rows")
		eq(t, gapsOf(l), []string{}, "gaps")
	})

	t.Run("where no reader can be sent, the row says so, and a Rebuild with one recovers the library", func(t *testing.T) {
		r.t = t
		h := r.host("unsent", false, false)
		l := r.build("unsent", h, "text")
		readyOrFail(t, l)
		eq(t, []any{l["fullText"], r.pdfRows(l["indexRoot"].(string))}, []any{true, 0}, "no reader")
		eq(t, gapsOf(l), []string{"1 PDF has no text: this system has no PDF reader"}, "gaps")
		r.pdftotext(h.bin)
		again := r.rebuild(l["id"].(string))
		eq(t, []any{r.pdfRows(again["indexRoot"].(string)), gapsOf(again)}, []any{1, []string{}}, "recovered")
	})

	t.Run("a host with no Python says why it has no text, and a Rebuild after one is installed finds it", func(t *testing.T) {
		r.t = t
		h := r.host("nopython", true, true)
		l := r.build("nopython", h, "meaning-documents")
		readyOrFail(t, l)
		eq(t, []any{l["fullText"], l["vectors"]}, []any{false, false}, "no text")
		g := gapsOf(l)
		if len(g) < 2 {
			t.Fatalf("gaps %v", g)
		}
		match(t, g[0], `^No text in this index: this system has no Python 3\.10 or later`, "gap 0")
		match(t, g[1], `^No search by meaning: there is no text to embed`, "gap 1")
		r.python3(h.bin)
		again := r.rebuild(l["id"].(string))
		eq(t, []any{again["fullText"], r.pdfRows(again["indexRoot"].(string))}, []any{true, 1}, "found")
	})

	t.Run("a Meaning depth with no embedding model to be had says so; the words are still there", func(t *testing.T) {
		r.t = t
		l := r.build("nomodel", r.host("nomodel", false, true), "meaning-documents")
		readyOrFail(t, l)
		eq(t, []any{r.pdfRows(l["indexRoot"].(string)), l["vectors"], len(gapsOf(l))}, []any{1, false, 1}, "no model")
		match(t, gapsOf(l)[0], `^No search by meaning: `, "gap")
	})

	t.Run("a PDF with no text layer is counted, with the reader that found nothing", func(t *testing.T) {
		r.t = t
		h := r.host("blank", false, true)
		write(t, filepath.Join(h.home, "docs", "manuals", "scan.pdf"), pdfOf(" "))
		l := r.build("blank", h, "text")
		readyOrFail(t, l)
		eq(t, r.pdfRows(l["indexRoot"].(string)), 1, "pdf rows")
		g := gapsOf(l)
		eq(t, len(g), 1, "one gap")
		match(t, strings.Join(g, ""), `^1 PDF has no text: pdftotext found none, as with scanned pages, and this depth runs no OCR$`, "gap")
	})
}
