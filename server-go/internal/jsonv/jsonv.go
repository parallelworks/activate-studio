// Package jsonv holds JSON values the way JavaScript does: objects keep
// their keys in JavaScript's order (integer-like keys first, ascending,
// then the rest in insertion order), numbers are float64, and encoding
// follows JSON.stringify (no HTML escaping, no trailing newline). The Node
// server parses platform and CLI JSON with JSON.parse and sends it on with
// JSON.stringify; a Go route that relays the same JSON uses these values so
// the bytes the browser gets keep Node's key order.
package jsonv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Object is a JavaScript object: string keys in JavaScript's order.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject makes an empty object; pairs are key, value, key, value, ...
func NewObject(pairs ...any) *Object {
	o := &Object{vals: map[string]any{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// arrayIndex reports whether k is a canonical array index, which
// JavaScript orders before every other key.
func arrayIndex(k string) (uint64, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	if err != nil || n > math.MaxUint32-1 {
		return 0, false
	}
	return n, true
}

// Set adds or replaces k. A new key goes where JavaScript would put it.
func (o *Object) Set(k string, v any) *Object {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[k]; ok {
		o.vals[k] = v
		return o
	}
	o.vals[k] = v
	if n, ok := arrayIndex(k); ok {
		// After the last index key smaller than n.
		i := sort.Search(len(o.keys), func(i int) bool {
			m, isIdx := arrayIndex(o.keys[i])
			return !isIdx || m > n
		})
		o.keys = append(o.keys, "")
		copy(o.keys[i+1:], o.keys[i:])
		o.keys[i] = k
		return o
	}
	o.keys = append(o.keys, k)
	return o
}

// Get is the value at k and whether k is there.
func (o *Object) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// Has reports whether k is there.
func (o *Object) Has(k string) bool { _, ok := o.Get(k); return ok }

// Delete removes k.
func (o *Object) Delete(k string) {
	if o == nil {
		return
	}
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

// Keys are the keys in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Clone copies the object one level deep.
func (o *Object) Clone() *Object {
	c := &Object{keys: append([]string(nil), o.Keys()...), vals: make(map[string]any, o.Len())}
	if o != nil {
		for k, v := range o.vals {
			c.vals[k] = v
		}
	}
	return c
}

// MarshalJSON encodes as JSON.stringify does.
func (o *Object) MarshalJSON() ([]byte, error) { return Marshal(o) }

// UnmarshalJSON decodes as JSON.parse does.
func (o *Object) UnmarshalJSON(b []byte) error {
	v, err := Decode(b)
	if err != nil {
		return err
	}
	obj, ok := v.(*Object)
	if !ok {
		return errors.New("jsonv: not an object")
	}
	*o = *obj
	return nil
}

// Decode parses JSON as JSON.parse does: objects as *Object, arrays as
// []any, numbers as float64.
func Decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("jsonv: unexpected data after the value")
	}
	return v, nil
}

// DecodeString is Decode of a string.
func DecodeString(s string) (any, error) { return Decode([]byte(s)) }

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return fromToken(dec, t)
}

func fromToken(dec *json.Decoder, t json.Token) (any, error) {
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("jsonv: object key is not a string")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				// JSON.parse keeps the last of a repeated key, in the first's place.
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("jsonv: unexpected %v", x)
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			// JSON.parse reads an out-of-range number as Infinity.
			var ne *strconv.NumError
			if errors.As(err, &ne) && ne.Err == strconv.ErrRange {
				return f, nil
			}
			return nil, err
		}
		return f, nil
	default:
		return x, nil // string, bool, nil
	}
}

// Marshal encodes v as JSON.stringify does: *Object in its order, float64
// in JavaScript's number format (NaN and Infinity as null), strings with
// only the escapes JSON.stringify writes. Other Go values go through
// encoding/json, without HTML escaping.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := encode(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// MustMarshal is Marshal for values that always encode.
func MustMarshal(v any) []byte {
	b, err := Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func encode(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, x)
	case float64:
		b.WriteString(FormatNumber(x))
	case float32:
		b.WriteString(FormatNumber(float64(x)))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case json.Number:
		f, _ := strconv.ParseFloat(string(x), 64)
		b.WriteString(FormatNumber(f))
	case json.RawMessage:
		if len(x) == 0 {
			b.WriteString("null")
			return nil
		}
		// Re-encoded so its numbers and escapes are JavaScript's too.
		d, err := Decode(x)
		if err != nil {
			return err
		}
		return encode(b, d)
	case *Object:
		if x == nil {
			b.WriteString("null")
			return nil
		}
		b.WriteByte('{')
		first := true
		for _, k := range x.keys {
			val := x.vals[k]
			if _, skip := val.(undefined); skip {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			writeString(b, k)
			b.WriteByte(':')
			if err := encode(b, val); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		if x == nil {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if _, u := e.(undefined); u {
				b.WriteString("null")
				continue
			}
			if err := encode(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, e)
		}
		b.WriteByte(']')
	case []*Object:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := encode(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		// A Go map has no order; sorted, as encoding/json does.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := NewObject()
		for _, k := range keys {
			o.Set(k, x[k])
		}
		return encode(b, o)
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	}
	return nil
}

// Undefined is a value JSON.stringify leaves out of an object (and writes
// as null in an array), for a key a JavaScript object holds as undefined.
var Undefined any = undefined{}

type undefined struct{}

func (undefined) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// FormatNumber is JavaScript's Number.prototype.toString for JSON: NaN and
// the infinities are null.
func FormatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go writes 1e+21 and 1e-07; JavaScript writes 1e+21 and 1e-7.
		mant, exp, _ := cut(s, 'e')
		sign := exp[0]
		digits := exp[1:]
		for len(digits) > 1 && digits[0] == '0' {
			digits = digits[1:]
		}
		return mant + "e" + string(sign) + digits
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func cut(s string, c byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

const hex = "0123456789abcdef"

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					b.WriteString(`\u00`)
					b.WriteByte(hex[c>>4])
					b.WriteByte(hex[c&0xf])
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(`�`)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// Helpers for reading decoded values as JavaScript code does.

// Obj is v as an object, or nil.
func Obj(v any) *Object { o, _ := v.(*Object); return o }

// Arr is v as an array, or nil.
func Arr(v any) []any { a, _ := v.([]any); return a }

// Path walks keys through nested objects (o?.a?.b); nil where one is missing.
func Path(v any, keys ...string) any {
	for _, k := range keys {
		o := Obj(v)
		if o == nil {
			return nil
		}
		v, _ = o.Get(k)
	}
	return v
}

// Str is v as a string when it is one, else "".
func Str(v any) string { s, _ := v.(string); return s }

// String is String(v) in JavaScript for primitives: "" for nil (callers
// write `?? ”` before String()), numbers in JavaScript's format.
func String(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if math.IsNaN(x) {
			return "NaN"
		}
		if math.IsInf(x, 1) {
			return "Infinity"
		}
		if math.IsInf(x, -1) {
			return "-Infinity"
		}
		return FormatNumber(x)
	case *Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = String(e)
			}
		}
		out := ""
		for i, p := range parts {
			if i > 0 {
				out += ","
			}
			out += p
		}
		return out
	}
	return fmt.Sprint(v)
}

// Truthy is JavaScript's truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case undefined:
		return false
	}
	return true
}

// Num is v as a finite number when it is one.
func Num(v any) (float64, bool) {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
