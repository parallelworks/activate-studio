package kb

import (
	"io"
	"os"
	"strings"
)

// CachedText is kb.ts's cachedText: the text a library's extraction cache
// holds for a document or an image, without downmark's provenance line,
// or ok false when there is none. It reads the cache entry only.
func CachedText(rel, cacheRoot string) (content string, truncated bool, format string, ok bool) {
	file, err := Resolve(rel+".txt", cacheRoot)
	if err != nil {
		return "", false, "", false
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", false, "", false
	}
	raw := DecodeUTF8(b)
	if JSTrim(raw) == "" {
		return "", false, "", false
	}
	isMarkdown := strings.HasPrefix(raw, ProvenancePrefix)
	text := raw
	if isMarkdown {
		text = raw[strings.IndexByte(raw, '\n')+1:]
		text = strings.TrimPrefix(text, "\n")
	}
	format = "text"
	if isMarkdown {
		format = "markdown"
	}
	return SliceUTF16(text, MaxPreviewBytes), UTF16Len(text) > MaxPreviewBytes, format, true
}

// ReadFileContent is kb.ts's readFileContent: what the viewer shows of a
// file, as the JSON object Node answers with (its keys in Node's order).
// cacheRoot is where the library keeps extracted text.
func ReadFileContent(rel, root, cacheRoot string) (*Obj, error) {
	abs, err := Resolve(rel, root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, Error(400, "path is a directory")
	}
	kind := Classify(abs, false)
	out := O("path", rel, "kind", kind, "size", st.Size(), "mtime", mtimeSec(st))
	size := st.Size()
	switch kind {
	case KindText:
		b, err := readHead(abs, min(size, MaxPreviewBytes))
		if err != nil {
			return nil, err
		}
		return out.Set("truncated", size > MaxPreviewBytes).Set("content", DecodeUTF8(b)).Set("source", "raw"), nil
	case KindExtracted, KindImage:
		if content, truncated, format, ok := CachedText(rel, cacheRoot); ok {
			return out.Set("content", content).Set("truncated", truncated).Set("format", format).Set("source", "extracted"), nil
		}
		return out.Set("truncated", false).Set("content", nil).Set("source", "none"), nil
	case KindBinary:
		head, err := readHead(abs, min(size, 65536))
		if err != nil {
			return nil, err
		}
		if looksLikeText(head) {
			preview := head
			if n := min(size, MaxPreviewBytes); n != int64(len(head)) {
				if preview, err = readHead(abs, n); err != nil {
					return nil, err
				}
			}
			out.Set("kind", KindText)
			return out.Set("truncated", size > MaxPreviewBytes).Set("content", DecodeUTF8(preview)).Set("source", "raw"), nil
		}
	}
	return out.Set("truncated", false).Set("content", nil).Set("source", "none"), nil
}

// readHead reads up to n bytes from the start of a file. Node's fh.read
// into a buffer of n leaves zeros where the file was shorter (it shrank
// since its stat); so does this.
func readHead(abs string, n int64) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// looksLikeText: no NUL and under 2% control characters other than tab,
// newline and carriage return.
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	control := 0
	for _, c := range b {
		if c == 0 {
			return false
		}
		if c < 32 && c != 9 && c != 10 && c != 13 {
			control++
		}
	}
	return float64(control)/float64(len(b)) < 0.02
}
