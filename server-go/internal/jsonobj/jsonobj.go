// Package jsonobj is a JSON object that keeps its keys in order, as a
// JavaScript object does: a parsed file's keys stay where they were, a new
// key goes at the end, and JSON.stringify(obj, null, n) writes it back in
// that order. The Go server writes files Node also writes (settings.json,
// snapshots), and keeping their order keeps the two servers' files alike.
package jsonobj

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Object is an ordered JSON object. The zero value is empty.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// Parse reads a JSON object. Keys repeated in the text keep their first
// position and their last value, as JSON.parse does.
func Parse(b []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := New()
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.Set(k, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b[dec.InputOffset():])) > 0 {
		return nil, errors.New("text after the object")
	}
	return o, nil
}

// New is an empty object.
func New() *Object { return &Object{vals: map[string]json.RawMessage{}} }

// Keys is the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Len is the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Get is a key's value as written, or nil.
func (o *Object) Get(k string) json.RawMessage {
	if o == nil {
		return nil
	}
	return o.vals[k]
}

// Has reports whether k is set.
func (o *Object) Has(k string) bool { _, ok := o.vals[k]; return ok }

// Set sets k to v (already JSON), keeping k's place if it has one.
func (o *Object) Set(k string, v json.RawMessage) {
	if o.vals == nil {
		o.vals = map[string]json.RawMessage{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// SetValue sets k to v marshaled; a nil v deletes k, as a property set to
// undefined is left out by JSON.stringify.
func (o *Object) SetValue(k string, v any) error {
	if v == nil {
		o.Delete(k)
		return nil
	}
	b, err := Marshal(v)
	if err != nil {
		return err
	}
	o.Set(k, b)
	return nil
}

// Delete removes k.
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Clone is a copy.
func (o *Object) Clone() *Object {
	c := New()
	for _, k := range o.keys {
		c.Set(k, o.vals[k])
	}
	return c
}

// MarshalJSON writes the object compactly, keys in order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		var c bytes.Buffer
		if err := json.Compact(&c, o.vals[k]); err != nil {
			return nil, err
		}
		b.Write(c.Bytes())
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Indent is JSON.stringify(o, null, indent).
func (o *Object) Indent(indent string) ([]byte, error) {
	if o.Len() == 0 {
		return []byte("{}"), nil
	}
	c, err := o.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return IndentJS(c, indent)
}

// IndentJS indents compact JSON as JSON.stringify does: an empty array or
// object stays [] or {}, which json.Indent also does.
func IndentJS(compact []byte, indent string) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, compact, "", indent); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Marshal is json.Marshal without HTML escaping, as JSON.stringify writes
// <, > and & as they are.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// String is a raw value as a string, when it is one.
func String(v json.RawMessage) (string, bool) {
	var s string
	if len(v) == 0 || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// IsNullish reports a missing value or JSON null.
func IsNullish(v json.RawMessage) bool {
	return len(v) == 0 || strings.TrimSpace(string(v)) == "null"
}
