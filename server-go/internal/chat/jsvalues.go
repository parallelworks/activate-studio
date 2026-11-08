package chat

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
)

// Decoded JSON read as tools.ts reads it: a missing property is undefined
// (undef), which a template literal writes as "undefined" and
// JSON.stringify leaves out, where null is written "null".

type jsUndefined struct{}

var undef any = jsUndefined{}

// prop is v?.[k]: undef where v is no object or has no k.
func prop(v any, keys ...string) any {
	for _, k := range keys {
		o := jsonv.Obj(v)
		if o == nil {
			return undef
		}
		x, ok := o.Get(k)
		if !ok {
			return undef
		}
		v = x
	}
	return v
}

// arg is args[k], undef when absent.
func arg(args map[string]any, k string) any {
	v, ok := args[k]
	if !ok {
		return undef
	}
	return fromGo(v)
}

// fromGo is a value encoding/json decoded, as jsonv decodes it.
func fromGo(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := jsonv.NewObject()
		for _, k := range keys {
			o.Set(k, fromGo(x[k]))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fromGo(e)
		}
		return out
	}
	return v
}

func nullish(v any) bool { return v == nil || v == undef }

// coalesce is a ?? b ?? ...
func coalesce(vs ...any) any {
	for _, v := range vs {
		if !nullish(v) {
			return v
		}
	}
	return undef
}

// either is a || b || ...
func either(vs ...any) any {
	for _, v := range vs {
		if truthy(v) {
			return v
		}
	}
	if len(vs) == 0 {
		return undef
	}
	return vs[len(vs)-1]
}

func truthy(v any) bool { return v != undef && jsonv.Truthy(v) }

// tmpl is `${v}`, and String(v).
func tmpl(v any) string {
	switch x := v.(type) {
	case jsUndefined:
		return "undefined"
	case nil:
		return "null"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if !nullish(e) {
				parts[i] = tmpl(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return jsonv.String(v)
}

// joinJS is Array.prototype.join: null and undefined as "".
func joinJS(xs []any, sep string) string {
	parts := make([]string, len(xs))
	for i, e := range xs {
		if !nullish(e) {
			parts[i] = tmpl(e)
		}
	}
	return strings.Join(parts, sep)
}

// numJS is Number(v).
func numJS(v any) float64 {
	if v == undef {
		return math.NaN()
	}
	switch x := v.(type) {
	case *jsonv.Object:
		return math.NaN()
	case []any:
		if len(x) == 0 {
			return 0
		}
		if len(x) == 1 {
			return numJS(tmpl(x[0]))
		}
		return math.NaN()
	}
	return numberOf(v)
}

// numOr is Number(v) || d.
func numOr(v any, d float64) float64 {
	n := numJS(v)
	if n == 0 || math.IsNaN(n) {
		return d
	}
	return n
}

// sliceTo is the end Array.prototype.slice(0, end) gives over n items.
func sliceTo(n int, end float64) int {
	if math.IsNaN(end) {
		return 0
	}
	if math.IsInf(end, 1) {
		return n
	}
	e := int(math.Trunc(end))
	return sliceEnd(n, e)
}

// stringify is JSON.stringify(v); undef is left out of objects.
func stringify(v any) string {
	b, err := jsonv.Marshal(dropUndef(v))
	if err != nil {
		return ""
	}
	return string(b)
}

// stringify1 is JSON.stringify(v, null, 1).
func stringify1(v any) string {
	b, err := jsonv.Marshal(dropUndef(v))
	if err != nil {
		return ""
	}
	out, err := jsonobj.IndentJS(b, " ")
	if err != nil {
		return string(b)
	}
	return string(out)
}

// dropUndef copies v without its undefined properties (and with undefined
// array items as null), as JSON.stringify writes it.
func dropUndef(v any) any {
	switch x := v.(type) {
	case *jsonv.Object:
		o := jsonv.NewObject()
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			if e == undef {
				continue
			}
			o.Set(k, dropUndef(e))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			if e == undef {
				out[i] = nil
			} else {
				out[i] = dropUndef(e)
			}
		}
		return out
	case jsUndefined:
		return nil
	}
	return v
}

// cliJSON is tools.ts's jsonFromCli: the first balanced JSON value in the
// CLI's output, whatever trails it.
func cliJSON(text string) (any, error) {
	start := strings.IndexAny(text, "[{")
	if start < 0 {
		return nil, errors.New("no JSON in CLI output: " + jsval.Slice(text, 160))
	}
	open := text[start]
	var closer byte = '}'
	if open == '[' {
		closer = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return parseJSON(text[start : i+1])
			}
		}
	}
	return parseJSON(text[start:])
}

// parseJSON is JSON.parse. Its error text is Go's, not V8's.
func parseJSON(s string) (any, error) {
	v, err := jsonv.DecodeString(s)
	if err != nil {
		return nil, errors.New("Unexpected token in JSON: " + err.Error())
	}
	return v, nil
}

// entries is Object.entries(v ?? {}) for an object.
func entries(v any) ([]string, []any) {
	o := jsonv.Obj(v)
	if o == nil {
		if a := jsonv.Arr(v); a != nil {
			keys := make([]string, len(a))
			for i := range a {
				keys[i] = itoaInt(i)
			}
			return keys, a
		}
		return nil, nil
	}
	keys := o.Keys()
	vals := make([]any, len(keys))
	for i, k := range keys {
		vals[i], _ = o.Get(k)
	}
	return keys, vals
}

// lowerJS is String.prototype.toLowerCase.
func lowerJS(s string) string { return strings.ToLower(s) }

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
