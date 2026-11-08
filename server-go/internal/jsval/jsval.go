// Package jsval is JavaScript's coercions of JSON values, for routes that
// validate a request body as the Node server does (String(x), Number(x),
// Boolean(x), s.slice(0, n)), so the same bodies give the same answers.
package jsval

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// Present reports a key that is in the body: anything but undefined
// (JSON null is present, as `x !== undefined` sees it).
func Present(v json.RawMessage) bool { return len(bytes.TrimSpace(v)) > 0 }

func decode(v json.RawMessage) any {
	var x any
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	if d.Decode(&x) != nil {
		return nil
	}
	return x
}

// String is String(v); an absent value is "undefined".
func String(v json.RawMessage) string {
	if !Present(v) {
		return "undefined"
	}
	return str(decode(v), true)
}

func str(x any, top bool) string {
	switch t := x.(type) {
	case nil:
		if top {
			return "null"
		}
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t.String()
		}
		return NumberString(f)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = str(e, false)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// NumberString is a number as JavaScript prints it.
func NumberString(f float64) string {
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
		// Go writes e+21 / e-07; JavaScript e+21 / e-7.
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		exp = strings.TrimLeft(exp[1:], "0")
		return mant + "e" + string(sign) + exp
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Number is Number(v); absent is NaN.
func Number(v json.RawMessage) float64 {
	if !Present(v) {
		return math.NaN()
	}
	return num(decode(v))
}

func num(x any) float64 {
	switch t := x.(type) {
	case nil:
		return 0
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		return settings.JSNumber(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case []any:
		if len(t) == 0 {
			return 0
		}
		if len(t) == 1 {
			return settings.JSNumber(str(t[0], false))
		}
		return math.NaN()
	default:
		return math.NaN()
	}
}

// Truthy is Boolean(v) and !!v.
func Truthy(v json.RawMessage) bool {
	if !Present(v) {
		return false
	}
	switch t := decode(v).(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, _ := t.Float64()
		return f != 0 && !math.IsNaN(f)
	default:
		return true
	}
}

// IsFinite is Number.isFinite(n); IsInteger Number.isInteger(n).
func IsFinite(f float64) bool  { return !math.IsNaN(f) && !math.IsInf(f, 0) }
func IsInteger(f float64) bool { return IsFinite(f) && f == math.Trunc(f) }

// Array is v as a JSON array, ok false when it is not one.
func Array(v json.RawMessage) ([]json.RawMessage, bool) {
	t := bytes.TrimSpace(v)
	if len(t) == 0 || t[0] != '[' {
		return nil, false
	}
	var a []json.RawMessage
	if json.Unmarshal(t, &a) != nil {
		return nil, false
	}
	return a, true
}

// IsObject is a value that is a JSON object (not an array or null).
func IsObject(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) > 0 && t[0] == '{'
}

// Slice is s.slice(0, n): the first n UTF-16 code units.
func Slice(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// Len is s.length, in UTF-16 code units.
func Len(s string) int { return len(utf16.Encode([]rune(s))) }

// Trim is String.prototype.trim.
func Trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// Field is the value of key k in a JSON object body (nil when absent or
// when the body is not an object).
func Field(body map[string]json.RawMessage, k string) json.RawMessage {
	if body == nil {
		return nil
	}
	return body[k]
}
