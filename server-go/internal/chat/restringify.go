package chat

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
)

// jsRestringify is JSON.stringify(JSON.parse(raw)): numbers written as
// JavaScript writes them (1.0 is 1), strings with JavaScript's escapes, and
// an object's keys in JavaScript's order (array-index keys ascending, then
// the rest as they first appear, each with its last value). Text that is
// not JSON comes back as it is.
func jsRestringify(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var b bytes.Buffer
	if err := restringifyValue(dec, &b); err != nil {
		return raw
	}
	return b.Bytes()
}

func restringifyValue(dec *json.Decoder, b *bytes.Buffer) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch x := t.(type) {
	case json.Delim:
		if x == '[' {
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := restringifyValue(dec, b); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			b.WriteByte(']')
			return err
		}
		var keys []string
		vals := map[string][]byte{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			k, _ := kt.(string)
			var v bytes.Buffer
			if err := restringifyValue(dec, &v); err != nil {
				return err
			}
			if _, seen := vals[k]; !seen {
				keys = append(keys, k)
			}
			vals[k] = v.Bytes()
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		var index, named []string
		for _, k := range keys {
			if arrayIndex(k) {
				index = append(index, k)
			} else {
				named = append(named, k)
			}
		}
		sort.SliceStable(index, func(i, j int) bool {
			a, _ := strconv.ParseUint(index[i], 10, 32)
			c, _ := strconv.ParseUint(index[j], 10, 32)
			return a < c
		})
		b.WriteByte('{')
		for i, k := range append(index, named...) {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := jsonobj.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.Write(vals[k])
		}
		b.WriteByte('}')
	case string:
		s, _ := jsonobj.Marshal(x)
		b.Write(s)
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		switch {
		case math.IsInf(f, 0):
			// JSON.parse reads 1e400 as Infinity, which stringify writes as null.
			b.WriteString("null")
		case err != nil:
			return err
		default:
			b.WriteString(jsval.NumberString(f))
		}
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	}
	return nil
}

// arrayIndex is whether a key is an array index, which a JavaScript object
// keeps ahead of its other keys: a canonical integer below 2^32 - 1.
func arrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	return err == nil && n < 1<<32-1
}
