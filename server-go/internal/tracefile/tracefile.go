// Package tracefile writes a GUFI trace file (docs/latex/sections/
// tracefile.tex in GUFI) from a listing of an object store: paths with a
// size and a time, in any order. gufi_trace2index builds an index from it
// as gufi_dir2index builds one from a folder.
//
// A trace has one stanza per folder: the folder's line, then a line for
// each file in it. A folder may appear only once (a second stanza for it
// replaces the first), so the files are grouped by folder before they are
// written. Listings can be far larger than memory, so files are kept in
// sorted runs on disk and merged; only the folders are held in memory.
package tracefile

import (
	"bufio"
	"bytes"
	"container/heap"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Delim is the column separator, GUFI's default (ASCII record separator).
const Delim = '\x1e'

// Modes the entries are given: an object store has no permissions, so
// the index is the Studio account's alone.
const (
	DirMode  = 0o40700
	FileMode = 0o100600
)

// maxPath is the longest path below the trace's top folder kept; GUFI
// holds a path in a buffer of 4096 bytes.
const maxPath = 3800

// maxName is the longest folder name kept: each folder becomes a folder
// of the index on this computer.
const maxName = 255

// runSize is how many files are sorted in memory before they go to disk.
var runSize = 500_000

// Stats is what a listing held.
type Stats struct {
	// Objects and Bytes are the files kept and their total size.
	Objects int64
	Bytes   int64
	// Dirs counts the folders written, the top one included.
	Dirs int64
	// Skipped counts entries left out: a path GUFI cannot hold, a name
	// that is "." or "..", or a second entry of the same name.
	Skipped int64
}

type dirInfo struct {
	ino   uint64
	pino  uint64
	mtime int64
}

type rec struct {
	dir, name   string
	size, mtime int64
}

// Writer collects a listing and writes it as a trace.
type Writer struct {
	// NoDev writes the layout of GUFI builds before 0.8.1, which have no
	// device column after the type (see Layout).
	NoDev    bool
	root     string
	tmp      string
	uid, gid int
	now      int64
	dirs     map[string]*dirInfo
	nextIno  uint64
	run      []rec
	runs     []string
	stats    Stats
	finished bool
}

// New is a Writer whose trace has root as its top folder's name, keeping
// its sorted runs in tmp (made if missing; the caller removes it). now is
// the time a folder with no files of its own is given.
func New(tmp, root string, now int64) (*Writer, error) {
	if root == "" || strings.ContainsAny(root, "/\x00") {
		return nil, errors.New("tracefile: the top folder needs a plain name")
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 {
		uid, gid = 0, 0
	}
	w := &Writer{root: root, tmp: tmp, uid: uid, gid: gid, now: now, dirs: map[string]*dirInfo{}, nextIno: 1}
	w.dirs[""] = &dirInfo{ino: w.ino()}
	return w, nil
}

func (w *Writer) ino() uint64 { n := w.nextIno; w.nextIno++; return n }

// clean is a listing path split into its names, or false when it cannot
// be kept. Empty names (a doubled slash) are dropped.
func clean(p string) ([]string, bool) {
	if len(p) > maxPath || strings.IndexByte(p, 0) >= 0 {
		return nil, false
	}
	var parts []string
	for _, s := range strings.Split(p, "/") {
		switch s {
		case "":
			continue
		case ".", "..":
			return nil, false
		}
		parts = append(parts, s)
	}
	return parts, true
}

// dir records a folder and the folders above it.
func (w *Writer) dir(parts []string) (*dirInfo, bool) {
	cur, path := w.dirs[""], ""
	for _, s := range parts {
		if len(s) > maxName {
			return nil, false
		}
		next := s
		if path != "" {
			next = path + "/" + s
		}
		d := w.dirs[next]
		if d == nil {
			d = &dirInfo{ino: w.ino(), pino: cur.ino}
			w.dirs[next] = d
		}
		cur, path = d, next
	}
	return cur, true
}

// Add takes one entry of the listing: its path below the listed folder,
// its size and modification time (seconds), and whether it is a folder.
func (w *Writer) Add(path string, size, mtime int64, isDir bool) error {
	if w.finished {
		return errors.New("tracefile: Add after Write")
	}
	parts, ok := clean(path)
	if !ok {
		w.stats.Skipped++
		return nil
	}
	if isDir {
		if len(parts) == 0 {
			return nil
		}
		if _, ok := w.dir(parts); !ok {
			w.stats.Skipped++
		}
		return nil
	}
	if len(parts) == 0 {
		w.stats.Skipped++
		return nil
	}
	d, ok := w.dir(parts[:len(parts)-1])
	if !ok {
		w.stats.Skipped++
		return nil
	}
	if size < 0 {
		size = 0
	}
	if mtime > d.mtime {
		d.mtime = mtime
	}
	w.run = append(w.run, rec{dir: strings.Join(parts[:len(parts)-1], "/"), name: parts[len(parts)-1], size: size, mtime: mtime})
	if len(w.run) >= runSize {
		return w.flush()
	}
	return nil
}

func less(a, b rec) bool {
	if a.dir != b.dir {
		return a.dir < b.dir
	}
	return a.name < b.name
}

// flush sorts the files held in memory and writes them to a run on disk.
func (w *Writer) flush() error {
	if len(w.run) == 0 {
		return nil
	}
	sort.Slice(w.run, func(i, j int) bool { return less(w.run[i], w.run[j]) })
	f, err := os.CreateTemp(w.tmp, "run-*")
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	var buf [binary.MaxVarintLen64]byte
	put := func(s string) {
		n := binary.PutUvarint(buf[:], uint64(len(s)))
		bw.Write(buf[:n])
		bw.WriteString(s)
	}
	for _, r := range w.run {
		put(r.dir)
		put(r.name)
		n := binary.PutVarint(buf[:], r.size)
		bw.Write(buf[:n])
		n = binary.PutVarint(buf[:], r.mtime)
		bw.Write(buf[:n])
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	w.runs = append(w.runs, f.Name())
	w.run = w.run[:0]
	return nil
}

// source is a stream of files in order: a run on disk, or the files held
// in memory when there was never more than one run.
type source interface {
	next() (rec, bool, error)
}

type memSource struct {
	rs []rec
	i  int
}

func (m *memSource) next() (rec, bool, error) {
	if m.i >= len(m.rs) {
		return rec{}, false, nil
	}
	m.i++
	return m.rs[m.i-1], true, nil
}

type fileSource struct {
	f *os.File
	r *bufio.Reader
}

func (s *fileSource) str() (string, error) {
	n, err := binary.ReadUvarint(s.r)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(s.r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *fileSource) next() (rec, bool, error) {
	dir, err := s.str()
	if err == io.EOF {
		return rec{}, false, nil
	}
	if err != nil {
		return rec{}, false, err
	}
	var r rec
	r.dir = dir
	if r.name, err = s.str(); err != nil {
		return rec{}, false, err
	}
	if r.size, err = binary.ReadVarint(s.r); err != nil {
		return rec{}, false, err
	}
	if r.mtime, err = binary.ReadVarint(s.r); err != nil {
		return rec{}, false, err
	}
	return r, true, nil
}

type head struct {
	r   rec
	src source
}

type heads []head

func (h heads) Len() int           { return len(h) }
func (h heads) Less(i, j int) bool { return less(h[i].r, h[j].r) }
func (h heads) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *heads) Push(x any)        { *h = append(*h, x.(head)) }
func (h *heads) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// merged is every file of the listing in order, from the runs.
type merged struct{ h heads }

func (m *merged) next() (rec, bool, error) {
	if len(m.h) == 0 {
		return rec{}, false, nil
	}
	top := m.h[0]
	r, ok, err := top.src.next()
	if err != nil {
		return rec{}, false, err
	}
	if ok {
		m.h[0].r = r
		heap.Fix(&m.h, 0)
	} else {
		heap.Pop(&m.h)
	}
	return top.r, true, nil
}

// Write writes the trace to out and returns what it held. A Writer is
// done after Write.
func (w *Writer) Write(out io.Writer) (Stats, error) {
	if w.finished {
		return w.stats, errors.New("tracefile: Write twice")
	}
	w.finished = true
	var src source
	if len(w.runs) == 0 {
		sort.Slice(w.run, func(i, j int) bool { return less(w.run[i], w.run[j]) })
		src = &memSource{rs: w.run}
	} else {
		if err := w.flush(); err != nil {
			return w.stats, err
		}
		m := &merged{}
		for _, name := range w.runs {
			f, err := os.Open(name)
			if err != nil {
				return w.stats, err
			}
			defer f.Close()
			fs := &fileSource{f: f, r: bufio.NewReaderSize(f, 1<<20)}
			r, ok, err := fs.next()
			if err != nil {
				return w.stats, err
			}
			if ok {
				m.h = append(m.h, head{r, fs})
			}
		}
		heap.Init(&m.h)
		src = m
	}
	paths := make([]string, 0, len(w.dirs))
	for p := range w.dirs {
		paths = append(paths, p)
	}
	// Byte order puts a folder before everything under it.
	sort.Strings(paths)
	bw := bufio.NewWriterSize(out, 1<<20)
	pending, have, err := src.next()
	if err != nil {
		return w.stats, err
	}
	var prev *rec
	for _, p := range paths {
		d := w.dirs[p]
		mt := d.mtime
		if mt == 0 {
			mt = w.now
		}
		name := w.root
		if p != "" {
			name += "/" + p
		}
		w.line(bw, name, 'd', d.ino, DirMode, 2, 0, mt, d.pino)
		w.stats.Dirs++
		for have && pending.dir == p {
			r := pending
			if prev != nil && prev.dir == r.dir && prev.name == r.name {
				w.stats.Skipped++
			} else {
				w.line(bw, name+"/"+r.name, 'f', w.ino(), FileMode, 1, r.size, r.mtime, d.ino)
				w.stats.Objects++
				w.stats.Bytes += r.size
			}
			prev = &r
			if pending, have, err = src.next(); err != nil {
				return w.stats, err
			}
		}
	}
	if have {
		return w.stats, fmt.Errorf("tracefile: a file of %q has no folder", pending.dir)
	}
	return w.stats, bw.Flush()
}

// line writes one entry in the column order of GUFI 0.8.1's linetowork:
// name, type, dev (not with NoDev), inode, mode, nlink, uid, gid, size, blksize, blocks,
// atime, mtime, ctime, linkname, xattrs, crtime, ossint1-4, osstext1-2,
// pinode, each followed by the delimiter.
func (w *Writer) line(bw *bufio.Writer, name string, typ byte, ino uint64, mode, nlink int, size, mtime int64, pino uint64) {
	col := func(s string) { bw.WriteString(s); bw.WriteByte(Delim) }
	str := func(s string) { col(fmt.Sprintf("%04x", len(s)) + s) }
	num := func(n int64) { col(strconv.FormatInt(n, 10)) }
	str(name)
	col(string(typ))
	if !w.NoDev {
		num(0)
	}
	col(strconv.FormatUint(ino, 10))
	num(int64(mode))
	num(int64(nlink))
	num(int64(w.uid))
	num(int64(w.gid))
	num(size)
	num(4096)
	num((size + 511) / 512)
	num(mtime)
	num(mtime)
	num(mtime)
	str("")
	col("00040000")
	num(mtime)
	num(0)
	num(0)
	num(0)
	num(0)
	str("")
	str("")
	col(strconv.FormatUint(pino, 10))
	bw.WriteByte('\n')
}

// Cleanup removes the runs written to disk.
func (w *Writer) Cleanup() {
	for _, r := range w.runs {
		os.Remove(r)
	}
	w.runs = nil
	os.Remove(w.tmp)
}

// Layout is whether the gufi_trace2index in bin reads traces without the
// device column (GUFI before 0.8.1). Its own flag for older traces names
// a layout older still, so a trace of one folder is built in each layout
// in a folder under tmp, and the one it reads decides.
func Layout(bin, tmp string) (noDev bool, err error) {
	dir, err := os.MkdirTemp(tmp, "layout-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	var last error
	for _, nd := range []bool{false, true} {
		w := &Writer{NoDev: nd, root: "probe", uid: os.Getuid(), gid: os.Getgid()}
		if w.uid < 0 {
			w.uid, w.gid = 0, 0
		}
		var b bytes.Buffer
		bw := bufio.NewWriter(&b)
		w.line(bw, "probe", 'd', 1, DirMode, 2, 0, 1, 0)
		w.line(bw, "probe/f", 'f', 2, FileMode, 1, 1, 1, 1)
		bw.Flush()
		tf := filepath.Join(dir, fmt.Sprintf("t%v", nd))
		if err := os.WriteFile(tf, b.Bytes(), 0o600); err != nil {
			return false, err
		}
		idx := filepath.Join(dir, fmt.Sprintf("i%v", nd))
		out, err := exec.Command(filepath.Join(bin, "gufi_trace2index"), "-n", "1", tf, idx).CombinedOutput()
		if err == nil && !bytes.Contains(out, []byte("Error")) {
			if _, serr := os.Stat(filepath.Join(idx, "probe", "db.db")); serr == nil {
				return nd, nil
			}
		}
		last = fmt.Errorf("gufi_trace2index: %v %s", err, bytes.TrimSpace(out))
	}
	return false, fmt.Errorf("gufi_trace2index read neither trace layout: %w", last)
}
