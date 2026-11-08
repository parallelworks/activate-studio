package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// The label maps Query compiles a label condition from (query.ts
// builderPlan), in the order Node's Maps hold them. Added for the
// libraries track's query routes.

// DirEntries is tagMaps().dirTags: the main library's folders' labels.
func (t *Tags) DirEntries(ctx context.Context) []gufi.LabelEntry {
	m := t.maps(ctx)
	out := make([]gufi.LabelEntry, 0, len(m.dirOrd))
	for _, p := range m.dirOrd {
		out = append(out, gufi.LabelEntry{Rel: p, Tags: m.dirs[p]})
	}
	return out
}

// EntriesForIndexRoot is libraryLabelsForIndexRoot: the labels kept for
// the library whose index is at root, in its file's order; ok false when
// no library there has any.
func (l *LibraryLabels) EntriesForIndexRoot(root string) (entries []gufi.LabelEntry, ok bool) {
	dir := filepath.Join(l.Roots.IndexBase, "libraries")
	ids, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	for _, e := range ids {
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "labels.json"))
		if err != nil {
			continue
		}
		var head struct {
			Version   int             `json:"version"`
			IndexRoot string          `json:"indexRoot"`
			Entries   json.RawMessage `json:"entries"`
		}
		if json.Unmarshal(b, &head) != nil || head.Version != 1 || head.IndexRoot == "" || head.IndexRoot != root {
			continue
		}
		return orderedEntries(head.Entries), true
	}
	return nil, false
}

func orderedEntries(raw json.RawMessage) []gufi.LabelEntry {
	dec := json.NewDecoder(bytesReader(raw))
	out := []gufi.LabelEntry{}
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return out
	}
	idx := map[string]int{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return out
		}
		var e struct {
			Tags []string `json:"tags"`
		}
		if dec.Decode(&e) != nil {
			return out
		}
		key, _ := k.(string)
		if i, seen := idx[key]; seen {
			out[i].Tags = e.Tags
			continue
		}
		idx[key] = len(out)
		out = append(out, gufi.LabelEntry{Rel: key, Tags: e.Tags})
	}
	return out
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
