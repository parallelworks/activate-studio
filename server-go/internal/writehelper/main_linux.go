package writehelper

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// Main runs the helper and answers its exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	err := run(args, stdin, stdout)
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	var f *failure
	if errors.As(err, &f) {
		return f.code
	}
	return exitFailed
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintf(stdout, "studio-write %s-%s\n", runtime.GOOS, runtime.GOARCH)
		return err
	}
	priv, p := 0, pin{}
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		k, v, _ := strings.Cut(args[0], "=")
		var err error
		switch {
		case k == "--priv" && v == "1":
			priv = 1
		case k == "--priv" && v == "2":
			priv = 2
		case k == "--pin":
			p, err = parsePin(v)
		default:
			err = fail(exitUsage, "unknown option %s", k)
		}
		if err != nil {
			return err
		}
		args = args[1:]
	}
	if len(args) < 3 {
		return fail(exitUsage, "usage: studio-write [--priv=1|2] [--pin=INODE:UID] MARK COMMAND ROOT [arguments]")
	}
	mark, cmd, root, rest := args[0], args[1], args[2], args[3:]
	arg := func(i int) string {
		if i < len(rest) {
			return rest[i]
		}
		return ""
	}
	if cmd != "read" && cmd != "in" {
		fmt.Fprintln(stdout, mark)
	}
	t, err := openTree(root, p, priv)
	if err != nil {
		return err
	}
	in := bufio.NewReaderSize(stdin, 64<<10)
	done := func(name string) { fmt.Fprintf(stdout, "%s\x00", name) }
	switch cmd {
	case "id":
		st, err := statOf(t.fd)
		if err != nil {
			return fail(exitNoRoot, "cannot open %s: %v", root, err)
		}
		fmt.Fprintf(stdout, "%d:%d\n", st.Ino, st.Uid)
		return nil
	case "read":
		return t.read(arg(0), func() { fmt.Fprintln(stdout, mark) }, stdout)
	case "write":
		return t.write(arg(0), arg(1), arg(2) == "x", stdin)
	case "mkdir":
		return t.mkdir(arg(0))
	case "land":
		for {
			v, err := list(in, 3)
			if v == nil {
				return err
			}
			if err := t.land(v[0], v[1], v[2] == "r"); err != nil {
				return err
			}
			done(v[1])
		}
	case "remove":
		for {
			v, err := list(in, 2)
			if v == nil {
				return err
			}
			gone, err := t.remove(v[1], v[0] == "f")
			if err != nil {
				return err
			}
			if gone {
				done(v[1])
			}
		}
	case "rmstage":
		return t.removeStaging(arg(0))
	case "rmtree":
		for {
			v, err := list(in, 1)
			if v == nil {
				return err
			}
			gone, err := t.removeTree(v[0])
			if err != nil {
				return err
			}
			if gone {
				done(v[0])
			}
		}
	case "pstart":
		return t.startParts(arg(0), arg(1), arg(2))
	case "part":
		return t.part(arg(0), arg(1), arg(2), stdin)
	case "text":
		return t.text(arg(0), arg(1), stdout)
	case "swap":
		return t.swap(arg(0), arg(1), arg(2), arg(3), stdin, stdout)
	case "in":
		return t.in(arg(0), rest[min(1, len(rest)):])
	case "share", "unshare":
		return t.share(cmd == "unshare", rest, in, stdout)
	}
	return fail(exitUsage, "unknown command")
}

// list reads the next n items, each ended by NUL; nil at the end.
func list(r *bufio.Reader, n int) ([]string, error) {
	var out []string
	for range n {
		s, err := r.ReadString(0)
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, fail(exitFailed, "the list could not be read: %v", err)
		}
		out = append(out, strings.TrimSuffix(s, "\x00"))
	}
	return out, nil
}

// lookPath is exec.LookPath without the packages it needs.
func lookPath(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	for _, d := range strings.Split(os.Getenv("PATH"), ":") {
		if !strings.HasPrefix(d, "/") {
			continue
		}
		p := d + "/" + name
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
