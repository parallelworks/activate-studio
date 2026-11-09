package stepmesh

import (
	"math"
	"strconv"
	"strings"
)

// JavaScript values as embind's emval sees them. The C++ side of
// occt-import-js builds its result through emval (objects, arrays,
// numbers, strings) and reads the file through it (a Uint8Array), so this is
// the subset of JavaScript those calls touch.

type undefinedT struct{}

var undefined = undefinedT{}

type jsObject struct {
	keys []string
	vals map[string]any
}

func newObject() *jsObject { return &jsObject{vals: map[string]any{}} }

func (o *jsObject) get(k string) any {
	if v, ok := o.vals[k]; ok {
		return v
	}
	return undefined
}

func (o *jsObject) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// jsArray is a JavaScript Array; a hole reads as undefined.
type jsArray struct{ items []any }

// bytesView is the Uint8Array the file is handed in as.
type bytesView struct{ b []byte }

// memoryView is a typed array over the module's memory (embind's
// memory_view): kind is the index into Int8Array, Uint8Array, Int16Array,
// Uint16Array, Int32Array, Uint32Array, Float32Array, Float64Array.
type memoryView struct {
	kind, data, size uint32
}

// emval is embind's handle table: handles 2, 4, 6 and 8 are undefined,
// null, true and false and never counted; others are even indexes, with
// the count beside them, reused from a free list as emscripten does.
type emval struct {
	handles []any
	counts  []uint32
	free    []uint32
}

func newEmval() *emval {
	e := &emval{}
	for _, v := range []any{0, undefined, nil, true, false} {
		e.handles = append(e.handles, v, nil)
		e.counts = append(e.counts, 1, 0)
	}
	return e
}

func (e *emval) toHandle(v any) uint32 {
	switch v {
	case undefined:
		return 2
	case nil:
		return 4
	case true:
		return 6
	case false:
		return 8
	}
	var h uint32
	if n := len(e.free); n > 0 {
		h = e.free[n-1]
		e.free = e.free[:n-1]
	} else {
		h = uint32(len(e.handles))
		e.handles = append(e.handles, nil, nil)
		e.counts = append(e.counts, 0, 0)
	}
	e.handles[h] = v
	e.counts[h] = 1
	return h
}

func (e *emval) toValue(h uint32) (any, error) {
	if h == 0 || int(h) >= len(e.handles) {
		return nil, bindingError("Cannot use deleted val. handle = " + strconv.Itoa(int(h)))
	}
	return e.handles[h], nil
}

func (e *emval) incref(h uint32) {
	if h > 9 && int(h) < len(e.counts) {
		e.counts[h]++
	}
}

func (e *emval) decref(h uint32) {
	if h > 9 && int(h) < len(e.counts) {
		e.counts[h]--
		if e.counts[h] == 0 {
			e.handles[h] = undefined
			e.free = append(e.free, h)
		}
	}
}

type bindingError string

func (b bindingError) Error() string { return "BindingError: " + string(b) }

// toNumber is JavaScript's ToNumber for the values here.
func toNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case nil:
		return 0
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return math.NaN()
	}
	return math.NaN()
}

// truthy is JavaScript's ToBoolean.
func truthy(v any) bool {
	switch x := v.(type) {
	case undefinedT, nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// toUint32 is ToUint32 (value >>> 0).
func toUint32(f float64) uint32 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return uint32(int64(math.Mod(math.Trunc(f), 4294967296)))
}

// propertyKey is ToPropertyKey for a key value.
func propertyKey(k any) string {
	switch x := k.(type) {
	case string:
		return x
	case float64:
		return jsNumber(x)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	case undefinedT:
		return "undefined"
	}
	return "[object Object]"
}

// arrayIndex is the index a property key names, when it is one.
func arrayIndex(k string) (int, bool) {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	if err != nil || n == math.MaxUint32 {
		return 0, false
	}
	return int(n), true
}

// getProperty is obj[key].
func getProperty(obj any, key any) (any, error) {
	k := propertyKey(key)
	switch o := obj.(type) {
	case *jsObject:
		return o.get(k), nil
	case *jsArray:
		if k == "length" {
			return float64(len(o.items)), nil
		}
		if i, ok := arrayIndex(k); ok {
			if i < len(o.items) && o.items[i] != nil {
				return deref(o.items[i]), nil
			}
		}
		return undefined, nil
	case *bytesView:
		if k == "length" || k == "byteLength" {
			return float64(len(o.b)), nil
		}
		if i, ok := arrayIndex(k); ok && i < len(o.b) {
			return float64(o.b[i]), nil
		}
		return undefined, nil
	case undefinedT, nil:
		return nil, typeError("Cannot read properties of " + propertyKey(obj) + " (reading '" + k + "')")
	}
	return undefined, nil
}

// An array slot holds a value or, for an explicit undefined, this box, so
// that a hole (nil) and undefined differ as in JavaScript.
type slot struct{ v any }

func deref(v any) any {
	if s, ok := v.(slot); ok {
		return s.v
	}
	return v
}

// setProperty is obj[key] = value.
func setProperty(obj any, key any, v any) error {
	k := propertyKey(key)
	switch o := obj.(type) {
	case *jsObject:
		o.set(k, v)
		return nil
	case *jsArray:
		if i, ok := arrayIndex(k); ok {
			for len(o.items) <= i {
				o.items = append(o.items, nil)
			}
			o.items[i] = slot{v}
			return nil
		}
		if k == "length" {
			n := int(toUint32(toNumber(v)))
			if n < len(o.items) {
				o.items = o.items[:n]
			}
			for len(o.items) < n {
				o.items = append(o.items, nil)
			}
		}
		return nil
	case undefinedT, nil:
		return typeError("Cannot set properties of " + propertyKey(obj) + " (setting '" + k + "')")
	}
	return nil
}

type typeError string

func (t typeError) Error() string { return "TypeError: " + string(t) }

// jsNumber is Number.prototype.toString.
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	a := math.Abs(f)
	if a >= 1e21 || a < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		exp = strings.TrimLeft(exp[1:], "0")
		return mant + "e" + string(sign) + exp
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
