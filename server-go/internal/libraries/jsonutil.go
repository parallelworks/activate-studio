package libraries

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// orderedObject is a JSON object with its keys in the order they came,
// so a file written back keeps the order Node wrote it in.
type orderedObject struct {
	keys []string
	vals map[string]json.RawMessage
}

func (o *orderedObject) UnmarshalJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('{') {
		return fmt.Errorf("not an object")
	}
	o.vals = map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return err
		}
		k := t.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	return nil
}

// indentObject writes m's keys in order (keys first, then the rest) as
// JSON.stringify(x, null, 1) would at depth indent.
func indentObject(m map[string]json.RawMessage, keys []string, indent string) string {
	seen := map[string]bool{}
	var all []string
	for _, k := range keys {
		if _, ok := m[k]; ok && !seen[k] {
			all, seen[k] = append(all, k), true
		}
	}
	for k := range m {
		if !seen[k] {
			all = append(all, k)
		}
	}
	if len(all) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{")
	for i, k := range all {
		if i > 0 {
			b.WriteString(",")
		}
		kj, _ := json.Marshal(k)
		var v bytes.Buffer
		json.Indent(&v, m[k], indent+" ", " ")
		b.WriteString("\n" + indent + " " + string(kj) + ": " + v.String())
	}
	b.WriteString("\n" + indent + "}")
	return b.String()
}

// marshalJS is JSON.stringify(v, null, indent) for Go values: no HTML
// escaping.
func marshalJS(v any, indent string) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if indent != "" {
		e.SetIndent("", indent)
	}
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// fileStamp is size, modification time and inode, as settings.ts's
// fileStamp: a file changed by another process is read again.
func fileStamp(file string) string {
	st, err := os.Stat(file)
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("%d:%d:%d", st.Size(), st.ModTime().UnixNano(), inode(st))
}

// writeAtomic writes through a temporary file renamed over the old one, so
// a reader (Node's included) never sees half a file.
func writeAtomic(file string, b []byte) error {
	tmp := fmt.Sprintf("%s.%d.go.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
