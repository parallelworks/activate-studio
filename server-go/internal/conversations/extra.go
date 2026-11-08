package conversations

import (
	"encoding/json"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// Extra is what a stored object carries beyond the fields this package
// defines, with the stored order of all its keys. Node keeps a stored
// object as it is and changes its properties in place, so a field another
// version wrote (an approval's question or options, say) survives every
// rewrite of conversations.json, and so does the order of the keys.
type Extra struct {
	order []string
	vals  map[string]json.RawMessage
}

// keySet is the keys a type reads and writes.
type keySet map[string]bool

func keys(ks ...string) keySet {
	s := keySet{}
	for _, k := range ks {
		s[k] = true
	}
	return s
}

// readExtra keeps b's key order and the values of the keys known does
// not hold.
func readExtra(b []byte, known keySet) (Extra, error) {
	o, err := jsonobj.Parse(b)
	if err != nil {
		return Extra{}, err
	}
	x := Extra{order: o.Keys()}
	for _, k := range x.order {
		if !known[k] {
			if x.vals == nil {
				x.vals = map[string]json.RawMessage{}
			}
			x.vals[k] = o.Get(k)
		}
	}
	return x, nil
}

// Has reports whether the stored object had k beyond the known fields.
func (x Extra) Has(k string) bool { _, ok := x.vals[k]; return ok }

// apply is written laid over the stored object, as an object changed in
// place: the stored keys in their order (an unknown one with its stored
// value, a known one where it is still written), then the keys written
// that were not stored, in the order written.
func (x Extra) apply(written *jsonobj.Object) *jsonobj.Object {
	if len(x.order) == 0 {
		return written
	}
	out := jsonobj.New()
	for _, k := range x.order {
		if v, ok := x.vals[k]; ok {
			out.Set(k, v)
		} else if written.Has(k) {
			out.Set(k, written.Get(k))
		}
	}
	for _, k := range written.Keys() {
		if !out.Has(k) {
			out.Set(k, written.Get(k))
		}
	}
	return out
}

// marshalWith writes v (a type without the Extra field's methods) and
// lays it over x.
func marshalWith(x Extra, v any) ([]byte, error) {
	if len(x.order) == 0 {
		return jsonobj.Marshal(v)
	}
	b, err := jsonobj.Marshal(v)
	if err != nil {
		return nil, err
	}
	o, err := jsonobj.Parse(b)
	if err != nil {
		return nil, err
	}
	return jsonobj.Marshal(x.apply(o))
}
