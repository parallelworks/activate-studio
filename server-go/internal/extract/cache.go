package extract

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The extract cache holds one entry per document, <cache>/<rel>.txt, the
// text of <kb>/<rel>. An entry is current when it is at least as new as
// its source. An entry downmark wrote names it on its first line,
//
//	<!-- extracted by downmark 0.10.0 -->
//
// followed by a blank line and the Markdown; a PDF converted without OCR
// says "0.10.0 (no OCR)". Entries from the other readers are plain text
// with no such line. This is the format preextract.ts and enrich.py
// write and read, kept as is so that a cache either of them wrote stays
// valid.

// ProvenancePrefix opens the first line of an entry downmark wrote.
const ProvenancePrefix = "<!-- extracted by downmark "

const (
	provenanceEnd = " -->"
	noOCR         = " (no OCR)"
)

func provenanceLine(ver string) string {
	return ProvenancePrefix + ver + provenanceEnd
}

// EntryVersion is the downmark version that wrote a cache entry, from its
// first 200 bytes as preextract.ts reads it, or "" for an entry another
// reader wrote (or no entry).
func EntryVersion(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 200)
	n, _ := f.Read(buf)
	head := buf[:n]
	if !bytes.HasPrefix(head, []byte(ProvenancePrefix)) {
		return ""
	}
	end := bytes.Index(head, []byte(provenanceEnd))
	if end < 0 {
		return ""
	}
	return string(head[len(ProvenancePrefix):end])
}

// IndexText is a cache entry's text as it is indexed: without the
// provenance line, so it is neither searched nor shown in a snippet
// (enrich.py's index_text).
func IndexText(text string) string {
	if strings.HasPrefix(text, ProvenancePrefix) {
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			return ""
		}
		return strings.TrimLeft(text[nl+1:], "\n")
	}
	return text
}

// CacheFile is the entry for the KB-relative path rel.
func CacheFile(cacheRoot, rel string) string {
	return filepath.Join(cacheRoot, filepath.FromSlash(rel)+".txt")
}

// writeEntry writes an entry whole or not at all: a temporary file in the
// same directory renamed into place, so a pass stopped part way never
// leaves half an entry that a later pass would take as current. The
// temporary name ends in .tmp, which the server's cache prune sweeps when
// a killed pass leaves one behind.
func writeEntry(file, text string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		// CreateTemp makes the file 0600; entries are 0644 as Node and
		// Python write them under the usual umask.
		werr = os.Chmod(name, 0o644)
	}
	if werr == nil {
		werr = os.Rename(name, file)
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

// fresh reports whether an entry is at least as new as its source, with
// the entry's size.
func fresh(file string, src time.Time) (ok bool, size int64, exists bool) {
	st, err := os.Stat(file)
	if err != nil {
		return false, 0, false
	}
	return !st.ModTime().Before(src), st.Size(), true
}
