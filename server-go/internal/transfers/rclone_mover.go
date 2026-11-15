package transfers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// MoverRclone is rclone, for transfers with an rclone remote at one end.
const MoverRclone = "rclone"

// rcloneMover runs `rclone copy` on this computer for a transfer between
// an rclone remote and this computer, or between two remotes.
//
// It is always copy, whatever the transfer: the Studio's plan decides
// which files go (for a sync, what is new or changed), rclone is given
// exactly that list (--files-from-raw) and copies it, and the Studio
// checks what arrived before it deletes anything. rclone move would delete
// originals before that check, and rclone sync would delete what its own
// listing found extra, not what the plan showed.
//
// Where the destination can rename a file on the server (a file system,
// SFTP, most drives), rclone writes into the job's staging folder and the
// files take their names after the check, as with every mover. Where it
// cannot (object stores: S3, GCS, Azure Blob), giving a staged object its
// name is a server-side copy of every byte, so rclone writes straight to
// the final names instead (plan.direct): an object store shows an object
// only once its upload is complete, so nothing appears half written, but a
// failed transfer leaves the objects already sent in place, and a replaced
// object is replaced when its new copy arrives. Files that are not to
// replace anything are sent with --ignore-existing, so a name that
// appeared since the plan is not written over.
//
// Progress is rclone's own stats, one JSON log line a second
// (--use-json-log --stats 1s --stats-log-level NOTICE): bytes and files
// done. Cancel stops rclone with an interrupt, which aborts uploads in
// parts, and kills it if it has not stopped in 15 seconds.
type rcloneMover struct {
	s *Service
	inProcess
}

func newRcloneMover(s *Service) *rcloneMover { return &rcloneMover{s: s} }

func (m *rcloneMover) Name() string             { return MoverRclone }
func (m *rcloneMover) PollEvery() time.Duration { return 500 * time.Millisecond }

// rcloneEnd is how rclone names one end: a path on this computer or
// "remote:path".
func rcloneEnd(f FS, rel string) (string, bool) {
	switch x := f.(type) {
	case *RcloneFS:
		return x.spec(rel), true
	case *LocalFS:
		return absUnder(x.Root(), rel), true
	}
	return "", false
}

func (m *rcloneMover) CanReach(ctx context.Context, p *planRec) (bool, string) {
	if p.owner != "" {
		return false, "with sign-in on, rclone is not used: every viewer would act with this server's rclone credentials"
	}
	if p.src == nil || p.dst == nil || p.src.fs == nil || p.dst.fs == nil {
		return false, "it copies between places Studio can list"
	}
	sr, srcR := asRclone(p.src.fs)
	dr, dstR := asRclone(p.dst.fs)
	if !srcR && !dstR {
		return false, "neither end is an rclone remote"
	}
	if _, ok := rcloneEnd(p.src.fs, ""); !ok {
		return false, "rclone here cannot reach " + p.src.fs.Where() + "; studio-copy carries the data, with rclone reading the remote"
	}
	if _, ok := rcloneEnd(p.dst.fs, ""); !ok {
		return false, "rclone here cannot reach " + p.dst.fs.Where() + "; studio-copy carries the data, with rclone writing the remote"
	}
	bin := ""
	if srcR {
		bin = sr.Bin
	} else {
		bin = dr.Bin
	}
	if bin == "" || !runnable(bin) {
		return false, "rclone is not installed on this computer"
	}
	var why string
	switch {
	case srcR && dstR:
		why = "rclone on this computer copies from " + sr.Remote + " to " + dr.Remote
	case srcR:
		why = "rclone on this computer copies from " + sr.Remote
	default:
		why = "rclone on this computer copies to " + dr.Remote
	}
	if dstR && dr.directWrite(ctx) {
		why += "; " + dr.Remote + " has no server-side rename, so files are written straight to their names (each object appears only once whole; a failed transfer leaves those already sent)"
	} else {
		why += "; files arrive in a staging folder and take their names after Studio checks them"
	}
	return true, why
}

// rcloneArgs is one rclone copy: the fixed options, the list, then the
// two ends after "--".
func rcloneArgs(list, src, dst string, streams int, mode string) []string {
	n := strconv.Itoa(streams)
	args := []string{"copy", "--files-from-raw", list, "--no-traverse",
		"--use-json-log", "--stats", "1s", "--stats-log-level", "NOTICE",
		"--transfers", n, "--checkers", n}
	switch mode {
	case "replace":
		// The plan listed these as changed: send them whatever the
		// destination's size and time say.
		args = append(args, "--ignore-times")
	case "new":
		args = append(args, "--ignore-existing")
	}
	return append(args, "--", src, dst)
}

// rcloneBatch is one rclone run: files sent from one source folder, of
// one mode.
type rcloneBatch struct {
	base  string
	mode  string // "" into staging; "new" or "replace" straight to the names
	files []item
}

func rcloneBatches(j *MoveJob, direct bool, replace map[string]bool) []rcloneBatch {
	var out []rcloneBatch
	for _, g := range groupsOf(j.Files) {
		if !direct {
			out = append(out, rcloneBatch{base: g.base, files: g.files})
			continue
		}
		var n, r []item
		for _, f := range g.files {
			if replace[f.Out] {
				r = append(r, f)
			} else {
				n = append(n, f)
			}
		}
		if len(n) > 0 {
			out = append(out, rcloneBatch{base: g.base, mode: "new", files: n})
		}
		if len(r) > 0 {
			out = append(out, rcloneBatch{base: g.base, mode: "replace", files: r})
		}
	}
	return out
}

func (m *rcloneMover) Start(ctx context.Context, j *MoveJob) (string, error) {
	bin := ""
	if r, ok := asRclone(j.Src); ok {
		bin = r.Bin
	} else if r, ok := asRclone(j.Dst); ok {
		bin = r.Bin
	}
	if bin == "" {
		return "", errors.New("neither end is an rclone remote")
	}
	direct := j.Direct
	return m.start(func(ctx context.Context, pj *procJob) error {
		for _, b := range rcloneBatches(j, direct, j.Replace) {
			if err := m.runBatch(ctx, bin, j, b, pj); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	}), nil
}

func (m *rcloneMover) runBatch(ctx context.Context, bin string, j *MoveJob, b rcloneBatch, pj *procJob) error {
	var list bytes.Buffer
	var total int64
	for _, f := range b.files {
		list.WriteString(f.Out)
		list.WriteByte('\n')
		total += f.Size
	}
	lf, err := listFile(list.Bytes())
	if err != nil {
		return err
	}
	defer os.Remove(lf)
	src, _ := rcloneEnd(j.Src, b.base)
	dst, _ := rcloneEnd(j.Dst, j.Stage)
	cmd := exec.CommandContext(ctx, bin, rcloneArgs(lf, src, dst, streams(j.Set), b.mode)...)
	cmd.Env = rcloneEnv()
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = 15 * time.Second
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("rclone did not start: " + err.Error())
	}
	base := struct{ files, bytes int64 }{pj.files.Load(), pj.bytes.Load()}
	var tail bytes.Buffer
	sc := bufio.NewScanner(errPipe)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if st, ok := parseRcloneStats(line); ok {
			pj.bytes.Store(base.bytes + st.Bytes)
			pj.files.Store(base.files + st.Transfers)
			continue
		}
		if tail.Len() < 1<<20 {
			tail.Write(line)
			tail.WriteByte('\n')
		}
	}
	io.Copy(io.Discard, errPipe)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		if msg := rcloneMessage(tail.String()); msg != "" {
			return errors.New("rclone: " + msg)
		}
		return errors.New("rclone ended with status " + strconv.Itoa(code))
	}
	pj.files.Store(base.files + int64(len(b.files)))
	pj.bytes.Store(base.bytes + total)
	return nil
}

// rcloneStats is the part of rclone's stats line the mover uses.
type rcloneStats struct {
	Bytes     int64 `json:"bytes"`
	Transfers int64 `json:"transfers"`
	Errors    int64 `json:"errors"`
}

// parseRcloneStats reads one JSON log line; ok only for a stats line.
func parseRcloneStats(line []byte) (rcloneStats, bool) {
	if !bytes.Contains(line, []byte(`"stats"`)) {
		return rcloneStats{}, false
	}
	var l struct {
		Stats *rcloneStats `json:"stats"`
	}
	if json.Unmarshal(line, &l) != nil || l.Stats == nil {
		return rcloneStats{}, false
	}
	return *l.Stats, true
}

func (m *rcloneMover) Status(ctx context.Context, ref string) (MoverStatus, error) {
	return m.status(ref), nil
}

func (m *rcloneMover) Cancel(ref string) bool { return m.cancel(ref) }

// interrupt asks a process to stop; where there are no signals (Windows)
// it is killed.
func interrupt(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Signal(os.Interrupt); err != nil {
		return p.Kill()
	}
	return nil
}

// directNote is what a plan says when rclone writes straight to the
// final names.
func directNote(remote string) string {
	return remote + " is an object store with no server-side rename, so rclone writes each file straight to its final name. Each object appears only once it is whole, and Studio checks every file after the transfer, but if the transfer fails the files already sent stay, and replaced files are replaced as they arrive"
}
