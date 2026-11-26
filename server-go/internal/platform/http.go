package platform

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// Helpers for routes that answer as Fastify does.

// JSON writes v as Fastify serializes a returned object: JSON.stringify's
// bytes (jsonv), application/json; charset=utf-8, no trailing newline.
func JSON(w http.ResponseWriter, status int, v any) {
	b, err := jsonv.Marshal(v)
	if err != nil {
		apierr.Write(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b)
}

// Handler adapts a route that returns its answer (or an error, written as
// sanitizedErrorHandler writes it) to http.HandlerFunc.
func Handler(log *slog.Logger, h func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(w, r)
		if err != nil {
			apierr.Write(w, log, err)
			return
		}
		if v == nil {
			return // the handler wrote its own answer
		}
		JSON(w, http.StatusOK, v)
	}
}

// KbError is Node's KbError.
func KbError(status int, msg string) error { return &apierr.Error{Status: status, Message: msg} }

// BodyError is a body Fastify refuses: Error is its words, as logged, and
// Words what a route that answers it with a 4xx says (BadBody).
type BodyError struct {
	Fastify string
	Words   string
	Status  int
}

func (e *BodyError) Error() string { return e.Fastify }

// BadBody is err as a route that does not keep Fastify's 500 for a body it
// cannot read answers it: 400 (413 for one too large) with what is wrong.
// Any other error is as it was.
func BadBody(err error) error {
	var b *BodyError
	if errors.As(err, &b) {
		return apierr.New(b.Status, "%s", b.Words)
	}
	return err
}

// Body is req.body as Fastify parses it: a JSON body as decoded values, a
// text body as a string, none as nil. What Fastify refuses (malformed or
// empty JSON, an unknown content type, more than 1 MiB) is a *BodyError
// without a KbError's status, so it reaches sanitizedErrorHandler as a
// 500, as there, unless the route answers it with BadBody.
func Body(r *http.Request) (any, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, &BodyError{"Request body is too large", "The request body is larger than 1 MiB.", http.StatusRequestEntityTooLarge}
	}
	ct := r.Header.Get("Content-Type")
	if len(b) == 0 && ct == "" {
		return nil, nil
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		if len(b) == 0 {
			return nil, &BodyError{"Body cannot be empty when content-type is set to 'application/json'", "The request says its body is JSON and has none: send {} or no Content-Type.", http.StatusBadRequest}
		}
		v, err := jsonv.Decode(b)
		if err != nil {
			return nil, &BodyError{err.Error(), "The request body is not valid JSON.", http.StatusBadRequest}
		}
		return v, nil
	case mt == "text/plain":
		return string(b), nil
	case len(b) == 0:
		return nil, nil
	}
	return nil, &BodyError{"Unsupported Media Type: " + ct, "The request body must be JSON (Content-Type: application/json).", http.StatusBadRequest}
}

// BodyObject is `(req.body ?? {})` as an object: fields of anything that
// is not an object read as undefined.
func BodyObject(r *http.Request) (*jsonv.Object, error) {
	v, err := Body(r)
	if err != nil {
		return nil, err
	}
	if o := jsonv.Obj(v); o != nil {
		return o, nil
	}
	return jsonv.NewObject(), nil
}

// Query is req.query[k] as Fastify's parser gives it, a repeated key
// joined as String() of an array would; ok is whether it is present.
func Query(r *http.Request, k string) (string, bool) {
	vs, ok := r.URL.Query()[k]
	if !ok {
		return "", false
	}
	return strings.Join(vs, ","), true
}

// Q is String(req.query[k] ?? ”).
func Q(r *http.Request, k string) string { v, _ := Query(r, k); return v }

// Decode reads a JSON body into a Go value, for routes whose fields are
// typed; a body that is absent decodes as the zero value.
func Decode(r *http.Request, out any) error {
	v, err := Body(r)
	if err != nil || v == nil {
		return err
	}
	b, err := jsonv.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
