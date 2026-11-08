// Package reqbody reads a request's JSON body as Fastify parses it for the
// chat track's routes, and writes Fastify's JSON answers.
package reqbody

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// Limit is Fastify's bodyLimit (1 MiB).
const Limit = 1 << 20

// Body is req.body: the decoded value of a JSON body (an object, or some
// other JSON value), Present false when the request carried none.
type Body struct {
	Value   any
	Raw     []byte
	Present bool
}

// M is the body as an object; empty for anything else (req.body ?? {}).
func (b Body) M() map[string]any {
	if m, ok := b.Value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// Get is a field, nil when absent.
func (b Body) Get(k string) any { return b.M()[k] }

// Has reports whether the field is present (undefined is absent).
func (b Body) Has(k string) bool { _, ok := b.M()[k]; return ok }

// Str is String(body.k ?? ”).
func (b Body) Str(k string) string { return JSString(b.M()[k]) }

// Read reads the body. A body that is not JSON for its content type is
// Fastify's 400; a body past the limit is its 413.
func Read(r *http.Request) (Body, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, Limit+1))
	if err != nil {
		return Body{}, err
	}
	if len(raw) > Limit {
		return Body{}, apierr.New(413, "Request body is too large")
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "" && len(raw) == 0 {
		return Body{}, nil
	}
	if ct == "application/json" || strings.HasSuffix(ct, "+json") {
		t := bytes.TrimSpace(raw)
		if len(t) == 0 {
			return Body{}, apierr.New(400, "Body cannot be empty when content-type is set to 'application/json'")
		}
		var v any
		if err := json.Unmarshal(t, &v); err != nil {
			return Body{}, apierr.New(400, "Body is not valid JSON but content-type is set to 'application/json'")
		}
		return Body{Value: v, Raw: t, Present: true}, nil
	}
	if ct == "text/plain" || ct == "" {
		return Body{Value: string(raw), Raw: raw, Present: true}, nil
	}
	return Body{}, apierr.New(415, "Unsupported Media Type: %s", ct)
}

// JSString is String(v ?? ”) for a decoded JSON value.
func JSString(v any) string {
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
		return Number(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = JSString(e)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	}
	return ""
}

// Number is a JavaScript number's String().
func Number(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// JSNumber is Number(v) for a decoded JSON value.
func JSNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		t := strings.TrimSpace(x)
		if t == "" {
			return 0
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

// Truthy is JavaScript truthiness of a decoded JSON value.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
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

// JSON writes v as Fastify does: application/json; charset=utf-8, no HTML
// escaping.
func JSON(w http.ResponseWriter, status int, v any) error {
	b, err := jsonobj.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(b)
	return err
}

// OK is a 200 JSON answer.
func OK(w http.ResponseWriter, v any) error { return JSON(w, http.StatusOK, v) }

// O builds an ordered object from key, value pairs. A nil value is JSON
// null (use Omit to leave a key out).
func O(kv ...any) *jsonobj.Object {
	o := jsonobj.New()
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		v := kv[i+1]
		if _, omit := v.(omitted); omit {
			continue
		}
		if v == nil {
			o.Set(k, json.RawMessage("null"))
			continue
		}
		if raw, ok := v.(json.RawMessage); ok {
			o.Set(k, raw)
			continue
		}
		b, err := jsonobj.Marshal(v)
		if err != nil {
			b = []byte("null")
		}
		o.Set(k, b)
	}
	return o
}

type omitted struct{}

// Omit leaves a key out of O.
var Omit = omitted{}

// Put sets k on o to v (nil is null).
func Put(o *jsonobj.Object, k string, v any) {
	if v == nil {
		o.Set(k, json.RawMessage("null"))
		return
	}
	if raw, ok := v.(json.RawMessage); ok {
		o.Set(k, raw)
		return
	}
	b, err := jsonobj.Marshal(v)
	if err != nil {
		b = []byte("null")
	}
	o.Set(k, b)
}

// Merge is {...a, ...b}: b's keys after a's, a key of both in a's place.
func Merge(a, b *jsonobj.Object) *jsonobj.Object {
	o := a.Clone()
	if b != nil {
		for _, k := range b.Keys() {
			o.Set(k, b.Get(k))
		}
	}
	return o
}

// Parse is jsonobj.Parse of a value's JSON, for spreading a struct.
func Parse(v any) *jsonobj.Object {
	b, err := jsonobj.Marshal(v)
	if err != nil {
		return jsonobj.New()
	}
	o, err := jsonobj.Parse(b)
	if err != nil {
		return jsonobj.New()
	}
	return o
}
