// Package ppt extracts the text of PowerPoint 97-2003 presentations for
// the indexer, as indexer/enrich.py's olefile-based extract_ppt does: the
// text atoms of the "PowerPoint Document" stream ([MS-PPT]), in stream
// order, each paragraph block once. TextCharsAtom (UTF-16) and
// TextBytesAtom (8-bit, read as Latin-1) records hold the text; slide and
// notes containers are walked, the main master, handouts and the master
// slide list are not, since they hold placeholder prompts rather than
// content. A lone "*" (the stand-in for a date, footer or slide number
// field) is left out.
//
// The OLE container is read by github.com/richardlehane/mscfb. The stream
// is found at the root of the container by a case-insensitive name, as
// olefile finds it.
package ppt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/richardlehane/mscfb"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

var (
	// ErrNotPPT means the input is not an OLE container with a
	// "PowerPoint Document" stream.
	ErrNotPPT = errors.New("ppt: not a PowerPoint 97-2003 presentation")
	// ErrCorrupt means the stream could not be read whole.
	ErrCorrupt = errors.New("ppt: corrupt presentation")
)

// maxStream bounds the stream read into memory.
const maxStream = 1 << 30

const (
	recTextChars    = 0x0FA0
	recTextBytes    = 0x0FA8
	recMainMaster   = 0x03F8
	recHandout      = 0x0FC9
	recListWithText = 0x0FF0 // instance 0 slides, 1 masters, 2 notes
	maxDepth        = 32
)

// Text returns the presentation's text, one paragraph block to a line.
// It stops after the block that brings the text to maxText characters or
// more (code points, one more for each line break), as enrich.py does;
// maxText <= 0 means no limit. The caller cuts the result to its limit.
// It never panics; on input it cannot read it returns "" and an error.
func Text(r io.ReaderAt, size int64, maxText int) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			text, err = "", fmt.Errorf("%w: %v", ErrCorrupt, p)
		}
	}()
	stream, err := documentStream(r, size)
	if err != nil {
		return "", err
	}
	return StreamText(stream, maxText), nil
}

// documentStream reads the root "PowerPoint Document" stream.
func documentStream(r io.ReaderAt, size int64) ([]byte, error) {
	doc, err := mscfb.New(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, ErrNotPPT
	}
	var found *mscfb.File
	for e, err := doc.Next(); err == nil; e, err = doc.Next() {
		if len(e.Path) == 0 && strings.EqualFold(e.Name, "PowerPoint Document") {
			found = e
			break
		}
	}
	if found == nil {
		return nil, ErrNotPPT
	}
	if found.Size < 0 || found.Size > size || found.Size > maxStream {
		return nil, ErrCorrupt
	}
	buf := bytes.NewBuffer(make([]byte, 0, found.Size))
	// olefile raises on a stream it cannot follow to its end, and
	// enrich.py then has no text; a short read here is the same failure.
	if _, err := io.Copy(buf, io.LimitReader(found, found.Size)); err != nil {
		return nil, ErrCorrupt
	}
	if int64(buf.Len()) != found.Size {
		return nil, ErrCorrupt
	}
	return buf.Bytes(), nil
}

// StreamText walks the records of a "PowerPoint Document" stream.
func StreamText(data []byte, maxText int) string {
	w := &walker{data: data, max: maxText, seen: map[string]bool{}}
	w.walk(0, len(data), 0)
	return strings.Join(w.out, "\n")
}

type walker struct {
	data []byte
	max  int
	out  []string
	seen map[string]bool
	size int
}

func (w *walker) full() bool { return w.max > 0 && w.size >= w.max }

func (w *walker) walk(start, end, depth int) {
	pos := start
	for pos+8 <= end && !w.full() {
		verInst := binary.LittleEndian.Uint16(w.data[pos:])
		rtype := binary.LittleEndian.Uint16(w.data[pos+2:])
		rlen := int64(binary.LittleEndian.Uint32(w.data[pos+4:]))
		bodyStart := pos + 8
		bodyEnd := end
		if int64(bodyStart)+rlen < int64(end) {
			bodyEnd = bodyStart + int(rlen)
		}
		ver, inst := verInst&0x0F, verInst>>4
		if ver == 0x0F {
			skip := rtype == recMainMaster || rtype == recHandout || (rtype == recListWithText && inst == 1)
			if !skip && depth < maxDepth {
				w.walk(bodyStart, bodyEnd, depth+1)
			}
		} else if rtype == recTextChars || rtype == recTextBytes {
			t := textOf(rtype, w.data[bodyStart:bodyEnd])
			// The slide list and a slide's own shapes can hold the same
			// text; each block is kept once, in the order first met.
			if t != "" && t != "*" && !w.seen[t] {
				w.seen[t] = true
				w.out = append(w.out, t)
				w.size += pytext.Len(t) + 1
			}
		}
		if int64(bodyStart)+rlen >= int64(end) {
			return // the next record would start at or past the end
		}
		pos = bodyStart + int(rlen)
	}
}

// textOf decodes a text atom as enrich.py does: UTF-16LE or Latin-1 with
// Python's replacement for what does not decode, carriage returns and
// vertical tabs (paragraph and line breaks) as newlines, stripped.
func textOf(rtype uint16, body []byte) string {
	var s string
	if rtype == recTextChars {
		u := make([]uint16, len(body)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(body[2*i:])
		}
		if len(body)%2 == 1 {
			// Python's utf-16-le 'replace' gives one U+FFFD for the odd
			// byte, which takes in a high surrogate left waiting for it.
			if n := len(u); n > 0 && u[n-1] >= 0xD800 && u[n-1] <= 0xDBFF {
				u = u[:n-1]
			}
			s = string(utf16.Decode(u)) + "\ufffd"
		} else {
			s = string(utf16.Decode(u))
		}
	} else {
		r := make([]rune, len(body))
		for i, b := range body {
			r[i] = rune(b)
		}
		s = string(r)
	}
	s = strings.NewReplacer("\r", "\n", "\x0b", "\n").Replace(s)
	return pytext.Strip(s)
}
