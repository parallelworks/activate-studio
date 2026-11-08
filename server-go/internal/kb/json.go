package kb

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// Obj is a JSON object that keeps its keys in the order they were set, so
// an answer has the key order of Node's object literal.
type Obj struct {
	keys []string
	vals map[string]any
}

// O makes an object from key, value pairs.
func O(kv ...any) *Obj {
	o := &Obj{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set sets a key, keeping its first position when it is already there
// (as assigning to an existing property does).
func (o *Obj) Set(k string, v any) *Obj {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

// Get is a key's value.
func (o *Obj) Get(k string) (any, bool) { v, ok := o.vals[k]; return v, ok }

// Merge is {...o, ...p}.
func (o *Obj) Merge(p *Obj) *Obj {
	for _, k := range p.keys {
		o.Set(k, p.vals[k])
	}
	return o
}

// MarshalJSON writes the keys in order.
func (o *Obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal is JSON.stringify's encoding: no HTML escaping.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// writeJSON sends v as Fastify does: its content type, no HTML escaping.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := marshal(v)
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}
