package extractfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
)

// Rclone reads the listed files from an rclone remote: each file is one
// request for its object (`rclone cat`), written to a temporary file for
// the readers and removed when read. A store answers no time other than
// the listing's without a second request, so the mtime is taken as listed
// and a file whose size is not the listed one counts as changed.
type Rclone struct {
	// Bin is the rclone program; "rclone" on the PATH when empty.
	Bin string
	// Root is where a listed path is found: "remote:bucket/prefix".
	Root string
	// Config is an rclone configuration file to use; rclone's own
	// (RCLONE_CONFIG, or its default) when empty.
	Config string
	// Strip is the leading path removed from each listed path.
	Strip string
	// Tmp is the folder the objects are written to while read.
	Tmp string
}

// ParseRcloneRoot checks a remote and path as rclone names them.
func ParseRcloneRoot(s string) (string, error) {
	name, _, ok := strings.Cut(s, ":")
	if !ok || name == "" || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("%q is not an rclone remote and path, such as store:bucket/prefix", s)
	}
	return s, nil
}

// spec is a listed path as rclone names it.
func (r Rclone) spec(p string) (string, error) {
	rel := strings.TrimPrefix(path.Clean("/"+stripPath(p, r.Strip)), "/")
	if rel == "" || rel == "." {
		return "", errors.New("the listed path names no object")
	}
	root := r.Root
	if !strings.HasSuffix(root, ":") && !strings.HasSuffix(root, "/") {
		root += "/"
	}
	return root + rel, nil
}

// Fetch is Source.Fetch.
func (r Rclone) Fetch(ctx context.Context, e Entry) (string, int64, int64, func(), error) {
	spec, err := r.spec(e.Path)
	if err != nil {
		return "", 0, 0, nil, err
	}
	f, err := os.CreateTemp(r.Tmp, "object-*"+path.Ext(e.Path))
	if err != nil {
		return "", 0, 0, nil, err
	}
	release := func() { os.Remove(f.Name()) }
	bin := r.Bin
	if bin == "" {
		bin = "rclone"
	}
	args := []string{"cat"}
	if r.Config != "" {
		args = append(args, "--config", r.Config)
	}
	cmd := exec.CommandContext(ctx, bin, append(args, "--", spec)...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = f, &stderr
	err = cmd.Run()
	n, _ := f.Seek(0, io.SeekCurrent)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		release()
		if ctx.Err() != nil {
			return "", 0, 0, nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", 0, 0, nil, fmt.Errorf("rclone cat %s: %s", spec, msg)
	}
	return f.Name(), n, e.Mtime, release, nil
}
