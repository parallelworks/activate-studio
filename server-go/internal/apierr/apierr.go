// Package apierr is the error body of the Studio's API, as Node's
// sanitizedErrorHandler (server/src/routes.ts) writes it: a KbError's
// status and {error: message}, 400 with the message for a request that
// fails validation, and for anything else 500 with
// {error: "internal error (ref <id>)"}, the detail going to the log only
// under that id.
package apierr

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
)

// Error is Node's KbError: a status and a message the client may see.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// New is a KbError.
func New(status int, format string, a ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, a...)}
}

// Validation marks a request that does not fit a route's schema (Fastify's
// err.validation): 400 with its message.
type Validation struct{ Message string }

func (e *Validation) Error() string { return e.Message }

// Write sends err as Node does. log receives the detail of an internal
// error under its reference.
func Write(w http.ResponseWriter, log *slog.Logger, err error) {
	var kb *Error
	var v *Validation
	switch {
	case errors.As(err, &kb):
		JSON(w, kb.Status, map[string]string{"error": kb.Message})
	case errors.As(err, &v):
		msg := v.Message
		if msg == "" {
			msg = "invalid request"
		}
		JSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	default:
		id := Ref()
		if log != nil {
			log.Error("unhandled route error", "errorId", id, "err", err.Error())
		}
		JSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error (ref " + id + ")"})
	}
}

// Handler adapts a handler that returns an error to http.Handler.
func Handler(log *slog.Logger, h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			Write(w, log, err)
		}
	}
}

const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"

// Ref is an error reference like Node's Math.random().toString(36).slice(2, 10):
// eight base-36 characters.
func Ref() string {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(36))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = base36[n.Int64()]
	}
	return string(b)
}

// JSON writes v with Fastify's content type.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
