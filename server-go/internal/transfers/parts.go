package transfers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A large file reaches a location in parts. They are written into one file
// in a folder of its own, open to no other account, beside where the file
// goes; its last part in, the file takes its name in one rename, as
// anything staged does (Land).

// PartsPrefix starts the name of the folder a file's parts are written in.
const PartsPrefix = ".studio-upload-"

// PartUnit is what a part's offset is a multiple of.
const PartUnit = 1 << 20

// errNoParts is a location whose account runs no commands.
const errNoParts = "this account takes SFTP only, so a file this large cannot be added here"

// partsData is the file in folder dir that takes the parts.
func partsData(dir string) string { return path.Join(dir, "data") }

// StartParts makes dir, a folder named with PartsPrefix, closed to other
// accounts, with an empty file in it, for a file of size bytes that will
// be named final beside it. need is what must be free there, for it and
// for what else is on its way, a 507 when it is not; a folder or a link
// named final already is a 409.
func (r *RemoteFS) StartParts(ctx context.Context, dir, final string, size, need int64) error {
	if r.SFTPOnly(ctx) {
		return r.named(409, errNoParts)
	}
	res, err := r.run(ctx, nil, "pstart", dir, final, strconv.FormatInt((need+1023)/1024, 10))
	if res.Code == 14 {
		free, _ := strconv.ParseInt(lastLine(remote.QuietStderr(res.Stderr)), 10, 64)
		return kb.NoRoom(size, free*1024-(need-size))
	}
	return err
}

// WritePart writes the n bytes src gives at offset off (a multiple of
// PartUnit) of the file in dir. Only in a folder this account made.
func (r *RemoteFS) WritePart(ctx context.Context, dir string, off, n int64, src io.Reader) error {
	if off%PartUnit != 0 {
		return apierr.New(400, "a part starts on a whole megabyte")
	}
	cmd, err := r.command(toolArgvOf(r.script(), marker(), "part", r.Dir, dir, strconv.FormatInt(off/PartUnit, 10), strconv.FormatInt(n, 10)))
	if err != nil {
		return err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)
	if err := cmd.Start(); err != nil {
		return r.named(502, err.Error())
	}
	// Its request over, or its client gone: the command is stopped, which
	// also ends a write to it that waits.
	stop := context.AfterFunc(ctx, cmd.Kill)
	defer stop()
	from := &failing{r: io.LimitReader(src, n)}
	sent, cerr := io.Copy(in, from)
	if cerr == nil && sent < n {
		from.err, cerr = io.ErrUnexpectedEOF, io.ErrUnexpectedEOF
	}
	if from.err != nil {
		// The part did not arrive whole. A command that stopped reading is
		// left to say why instead.
		cmd.Kill()
	}
	in.Close()
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case from.err != nil:
		return from.err
	case err == nil:
		return cerr
	}
	msg := lastLine(remote.QuietStderr(stderr.String()))
	switch {
	case strings.Contains(msg, partNoRoom):
		return r.named(507, partNoRoom)
	case msg == "":
		msg = err.Error()
	}
	// What sending it again would not change is not a 502.
	status := 502
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 4:
			status = 404
		case 5, 6:
			status = 400
		case 13:
			status = 409
		}
	}
	return r.named(status, msg)
}

// failing notes the error its reader gave.
type failing struct {
	r   io.Reader
	err error
}

func (f *failing) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && err != io.EOF {
		f.err = err
	}
	return n, err
}

// partNoRoom is what the helper says when a part did not fit.
const partNoRoom = "there is no space left there"

// LandParts gives the file in dir its name, replacing a plain file of that
// name, once it holds size bytes, and removes dir.
func (r *RemoteFS) LandParts(ctx context.Context, dir, final string, size int64) error {
	data := partsData(dir)
	there, err := r.Stat(ctx, []string{data})
	if err != nil {
		return err
	}
	if st, ok := there[data]; !ok || st.Type != 'f' {
		return r.named(404, "the parts that arrived are no longer there")
	} else if st.Size != size {
		return r.named(409, "the parts that arrived hold "+strconv.FormatInt(st.Size, 10)+" of "+strconv.FormatInt(size, 10)+" bytes")
	}
	if _, err := r.Land(ctx, []LandPair{{Staged: data, Final: final, Replace: true}}); err != nil {
		return err
	}
	_ = r.DropParts(ctx, dir)
	return nil
}

// DropParts removes dir and what it holds.
func (r *RemoteFS) DropParts(ctx context.Context, dir string) error {
	if !strings.HasPrefix(path.Base(dir), PartsPrefix) {
		return apierr.New(400, "not a folder of parts")
	}
	_, err := r.RemoveTrees(ctx, []string{dir})
	return err
}
