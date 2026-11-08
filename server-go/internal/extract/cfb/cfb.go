// Package cfb reads streams from OLE2 compound files (Microsoft's Compound
// File Binary format, [MS-CFB]) as olefile 0.47 does with its default
// tolerance, which indexer/enrich.py relies on for .ppt files: a defect
// olefile reports as "incorrect" (a sibling index out of range, an entry
// referenced twice, a chain that runs off the FAT, a last sector cut
// short, a stream shorter than its declared size) is passed over, and
// only what it treats as fatal (no header, a FAT or DIFAT sector it cannot
// read, no root entry) is an error. That is what lets it read damaged
// files github.com/richardlehane/mscfb refuses.
//
// The directory is walked as olefile walks it: the root's red-black tree
// of children in order, each entry used once, then each child's own tree,
// and the root's children sorted by name. Stream returns a root-level
// stream by a case-insensitive name, the first in that order.
//
// Deliberate differences, on headers no Office application writes: a
// sector shift below 2 or above 30 is an error (olefile fails on nearly
// all of them in other ways), and a chain that loops is followed only
// until it has read as many bytes as its source holds (olefile repeats
// the loop up to the declared size, which a small file can set to 4 GB).
package cfb

import (
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

var (
	// ErrNotCFB means the input has no compound file header.
	ErrNotCFB = errors.New("cfb: not an OLE2 compound file")
	// ErrCorrupt means a defect olefile treats as fatal.
	ErrCorrupt = errors.New("cfb: corrupt compound file")
	// ErrNotFound means no root-level stream has the name.
	ErrNotFound = errors.New("cfb: stream not found")
)

// MaxStream bounds the bytes one stream read gathers.
const MaxStream = 1 << 30

const (
	endOfChain  = 0xFFFFFFFE
	freeSect    = 0xFFFFFFFF
	noStream    = 0xFFFFFFFF
	unknownSize = 0x7FFFFFFF
	typeStorage = 1
	typeStream  = 2
	typeRoot    = 5
	// olefile formats the sector sizes into debug messages as it opens a
	// file; from 2**14285 on Python refuses to turn the number into a
	// string of more than 4300 digits, and the open fails.
	pyDigitLimitShift = 14285
	// Python's recursion limit is 1000 frames; walking the directory tree
	// recurses once per level, below the frames of enrich.py's own calls.
	maxTreeDepth = 980
)

var magic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// File is an open compound file.
type File struct {
	r          io.ReaderAt
	size       int64
	ss         int64 // sector size
	miniShift  uint16
	cutoff     uint32
	nbSect     int64
	fat        []uint32
	minifat    []uint32
	ministream []byte
	miniLoaded bool
	numMini    uint32
	firstMini  uint32
	dir        []byte
	entries    []*entry
	root       *entry
}

type entry struct {
	sid                uint32
	name               string
	typ                byte
	left, right, child uint32
	start              uint32
	size               int64
	used               bool
	kids               []*entry
}

// Open reads the header, the FAT and the directory.
func Open(r io.ReaderAt, size int64) (*File, error) {
	h := make([]byte, 512)
	if n, _ := r.ReadAt(h, 0); n != 512 || string(h[:8]) != string(magic) {
		return nil, ErrNotCFB
	}
	le := binary.LittleEndian
	shift, miniShift := le.Uint16(h[30:]), le.Uint16(h[32:])
	if shift >= pyDigitLimitShift || miniShift >= pyDigitLimitShift || shift < 2 || shift > 30 {
		return nil, ErrCorrupt
	}
	f := &File{r: r, size: size, ss: 1 << shift, miniShift: miniShift}
	f.cutoff = le.Uint32(h[56:])
	if f.cutoff != 0x1000 {
		f.cutoff = 0x1000 // olefile's fix for the mandatory value
	}
	f.firstMini, f.numMini = le.Uint32(h[60:]), le.Uint32(h[64:])
	f.nbSect = (size+f.ss-1)/f.ss - 1
	if err := f.loadFAT(h); err != nil {
		return nil, err
	}
	dir, _ := f.read(f.fat, f.ss, f.ss, nil, le.Uint32(h[48:]), unknownSize)
	f.dir = dir
	f.entries = make([]*entry, len(dir)/128)
	if len(f.entries) == 0 {
		return nil, ErrCorrupt // no root entry
	}
	f.root = f.load(0)
	if err := f.tree(f.root, 0); err != nil {
		return nil, err
	}
	return f, nil
}

// getsect reads one whole sector; olefile fails the file on less.
func (f *File) getsect(sect uint32) ([]byte, error) {
	b := make([]byte, f.ss)
	if n, _ := f.r.ReadAt(b, f.ss*(int64(sect)+1)); int64(n) != f.ss {
		return nil, ErrCorrupt
	}
	return b, nil
}

func words(b []byte) []uint32 {
	w := make([]uint32, len(b)/4)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return w
}

// loadSects appends the FAT sectors listed in sects, up to the first
// end-of-chain or free marker.
func (f *File) loadSects(sects []uint32) error {
	for _, s := range sects {
		if s == endOfChain || s == freeSect {
			break
		}
		b, err := f.getsect(s)
		if err != nil {
			return err
		}
		f.fat = append(f.fat, words(b)...)
		// olefile cuts the FAT to the file's sector count once it is
		// loaded; cutting as it grows gives the same table.
		if n := max(f.nbSect, 0); int64(len(f.fat)) > n {
			f.fat = f.fat[:n]
		}
	}
	return nil
}

func (f *File) loadFAT(h []byte) error {
	le := binary.LittleEndian
	if err := f.loadSects(words(h[76:512])); err != nil {
		return err
	}
	numFAT, first, numDIFAT := int64(le.Uint32(h[44:])), le.Uint32(h[68:]), int64(le.Uint32(h[72:]))
	if numDIFAT != 0 {
		if int64(first) >= f.nbSect {
			return ErrCorrupt
		}
		per := f.ss/4 - 1
		// Python's floor division, for a FAT count below 109 too.
		nb := floorDiv(numFAT-109+per-1, per)
		if numDIFAT != nb {
			return ErrCorrupt
		}
		if nb > f.nbSect+1 {
			// The chain has to revisit a sector, so it cycles and never
			// ends on a marker: olefile reads it through and then fails.
			return ErrCorrupt
		}
		isect := first
		for i := int64(0); i < nb; i++ {
			b, err := f.getsect(isect)
			if err != nil {
				return err
			}
			w := words(b)
			if err := f.loadSects(w[:per]); err != nil {
				return err
			}
			isect = w[per]
		}
		if isect != endOfChain && isect != freeSect {
			return ErrCorrupt
		}
	}
	if int64(len(f.fat)) > f.nbSect {
		f.fat = f.fat[:max(f.nbSect, 0)]
	}
	return nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// read follows a chain as olefile's OleStream does: sectors of size ss at
// offset+ss*sect in src (the file when mem is nil), through fat, for size
// bytes or to the end of the chain when the size is unknown. A sector cut
// short or missing is taken as it is, a chain leaving the FAT ends the
// stream, and the stream is what was read, even if less than declared.
func (f *File) read(fat []uint32, offset, ss int64, mem []byte, sect uint32, size int64) ([]byte, error) {
	unknown := size == unknownSize
	if unknown {
		size = int64(len(fat)) * ss
	}
	nb := (size + ss - 1) / ss
	// Every sector of the source is read at most once by a chain that
	// does not loop; past that much, a looping chain only repeats it.
	limit := f.size + ss
	if mem != nil {
		limit = int64(len(mem)) + ss
	}
	var data []byte
	buf := make([]byte, ss)
	for i := int64(0); i < nb; i++ {
		if sect == endOfChain || int64(sect) >= int64(len(fat)) {
			break
		}
		pos := offset + ss*int64(sect)
		var n int
		if mem == nil {
			n, _ = f.r.ReadAt(buf, pos)
		} else if pos < int64(len(mem)) {
			n = copy(buf, mem[pos:])
		}
		data = append(data, buf[:n]...)
		if int64(len(data)) > min(limit, MaxStream) {
			break
		}
		sect = fat[sect]
	}
	if int64(len(data)) >= size {
		data = data[:size]
	}
	return data, nil
}

// load is olefile's _load_direntry: the entry at sid, parsed once.
func (f *File) load(sid uint32) *entry {
	if e := f.entries[sid]; e != nil {
		return e
	}
	b := f.dir[int(sid)*128 : int(sid)*128+128]
	le := binary.LittleEndian
	nl := int(le.Uint16(b[64:]))
	if nl > 64 {
		nl = 64
	}
	// Python's name_raw[:namelength-2], negative ends counting back.
	end := nl - 2
	if end < 0 {
		end += 64
	}
	e := &entry{sid: sid, name: pytext.DecodeUTF16LE(b[:end]), typ: b[66],
		left: le.Uint32(b[68:]), right: le.Uint32(b[72:]), child: le.Uint32(b[76:]),
		start: le.Uint32(b[116:])}
	e.size = int64(le.Uint32(b[120:]))
	if f.ss != 512 {
		e.size += int64(le.Uint32(b[124:])) << 32
	}
	f.entries[sid] = e
	return e
}

// tree is build_storage_tree: e's children, in the order olefile visits
// them, then sorted by name.
func (f *File) tree(e *entry, depth int) error {
	if e.child == noStream {
		return nil
	}
	if err := f.kids(e, e.child, depth+1); err != nil {
		return err
	}
	sort.SliceStable(e.kids, func(i, j int) bool { return e.kids[i].name < e.kids[j].name })
	return nil
}

// kids is append_kids: an in-order walk of the red-black tree at sid.
func (f *File) kids(parent *entry, sid uint32, depth int) error {
	if sid == noStream || int64(sid) >= int64(len(f.entries)) {
		return nil
	}
	if depth > maxTreeDepth {
		return ErrCorrupt
	}
	c := f.load(sid)
	if c.used {
		return nil
	}
	c.used = true
	if err := f.kids(parent, c.left, depth+1); err != nil {
		return err
	}
	parent.kids = append(parent.kids, c)
	if err := f.kids(parent, c.right, depth+1); err != nil {
		return err
	}
	return f.tree(c, depth+1)
}

// Stream is the root-level stream named name (case does not matter), as
// olefile's openstream reads it.
func (f *File) Stream(name string) ([]byte, error) {
	want := lower(name)
	var e *entry
	for _, k := range f.root.kids {
		if lower(k.name) == want {
			e = k
			break
		}
	}
	if e == nil {
		return nil, ErrNotFound
	}
	if e.typ != typeStream {
		return nil, ErrCorrupt
	}
	if e.size < int64(f.cutoff) {
		if err := f.loadMini(); err != nil {
			return nil, err
		}
		if f.miniShift > 30 {
			return nil, nil // every mini sector lies past the mini stream
		}
		return f.read(f.minifat, 0, 1<<f.miniShift, f.ministream, e.start, e.size)
	}
	return f.read(f.fat, f.ss, f.ss, nil, e.start, e.size)
}

// loadMini reads the MiniFAT and the mini stream (the root's data).
func (f *File) loadMini() error {
	if f.miniLoaded {
		return nil
	}
	f.miniLoaded = true
	b, err := f.read(f.fat, f.ss, f.ss, nil, f.firstMini, int64(f.numMini)*f.ss)
	if err != nil {
		return err
	}
	if len(b)%4 != 0 {
		return ErrCorrupt // Python's array() of a partial word
	}
	f.minifat = words(b)
	if f.miniShift <= 30 {
		n := (f.root.size + (1 << f.miniShift) - 1) >> f.miniShift
		if int64(len(f.minifat)) > n {
			f.minifat = f.minifat[:n]
		}
	} else if f.root.size > 0 {
		f.minifat = f.minifat[:min(len(f.minifat), 1)]
	} else {
		f.minifat = nil
	}
	f.ministream, err = f.read(f.fat, f.ss, f.ss, nil, f.root.start, f.root.size)
	return err
}

// lower is Python's str.lower() for matching stream names: Go's, plus
// the one letter whose lowercase is two code points.
func lower(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.ReplaceAll(s, "İ", "i̇")), "İ", "i̇")
}
