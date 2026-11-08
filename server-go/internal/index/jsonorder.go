package index

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// Node writes indexing.json with JSON.stringify(all, null, 1), keys in
// insertion order. These keep that order across a read and a write: the
// file's own order, then new keys at the end.

// fileKeyOrder is [[lib1, key, key...], [lib2, ...]] as the file has them.
func fileKeyOrder(b []byte) [][]string {
	var raw struct {
		Libraries json.RawMessage `json:"libraries"`
	}
	if json.Unmarshal(b, &raw) != nil || len(raw.Libraries) == 0 {
		return nil
	}
	libs := objectKeys(raw.Libraries)
	var all map[string]json.RawMessage
	if json.Unmarshal(raw.Libraries, &all) != nil {
		return nil
	}
	out := make([][]string, 0, len(libs))
	for _, l := range libs {
		out = append(out, append([]string{l}, objectKeys(all[l])...))
	}
	return out
}

// objectKeys are a JSON object's keys in order.
func objectKeys(b []byte) []string {
	d := json.NewDecoder(bytes.NewReader(b))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

// keysInOrder are a library's keys: the file's order, then any others
// (sorted, for a stable file).
func keysInOrder(order [][]string, library string, m map[string]any) []string {
	var out []string
	for _, o := range order {
		if o[0] == library {
			for _, k := range o[1:] {
				if _, ok := m[k]; ok {
					out = append(out, k)
				}
			}
		}
	}
	var rest []string
	for k := range m {
		if !slices.Contains(out, k) {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

func setLibraryOrder(order [][]string, library string, keys []string) [][]string {
	for i, o := range order {
		if o[0] == library {
			order[i] = append([]string{library}, keys...)
			return order
		}
	}
	return append(order, append([]string{library}, keys...))
}

// orderedJSON is JSON.stringify({libraries: all}, null, 1) with the
// libraries and keys in order (those not in it after, sorted).
func orderedJSON(all map[string]map[string]any, order [][]string) []byte {
	var libs []string
	keyOf := map[string][]string{}
	for _, o := range order {
		if _, ok := all[o[0]]; ok && !slices.Contains(libs, o[0]) {
			libs = append(libs, o[0])
			keyOf[o[0]] = o[1:]
		}
	}
	var rest []string
	for l := range all {
		if !slices.Contains(libs, l) {
			rest = append(rest, l)
		}
	}
	slices.Sort(rest)
	libs = append(libs, rest...)
	var b strings.Builder
	b.WriteString("{\n \"libraries\": ")
	if len(libs) == 0 {
		b.WriteString("{}\n}")
		return []byte(b.String())
	}
	b.WriteString("{\n")
	for i, l := range libs {
		m := all[l]
		keys := keysInOrder([][]string{append([]string{l}, keyOf[l]...)}, l, m)
		b.WriteString("  " + jsonString(l) + ": ")
		if len(keys) == 0 {
			b.WriteString("{}")
		} else {
			b.WriteString("{\n")
			for j, k := range keys {
				b.WriteString("   " + jsonString(k) + ": " + jsonValue(m[k]))
				if j < len(keys)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString("  }")
		}
		if i < len(libs)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(" }\n}")
	return []byte(b.String())
}

func jsonString(s string) string { return jsonValue(s) }

// jsonValue is JSON.stringify(v) for a decoded JSON value.
func jsonValue(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSuffix(buf.String(), "\n")
}
