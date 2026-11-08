package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", rec.Body.String())
	}
	return m
}

func TestKbErrorKeepsStatusAndMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, nil, fmt.Errorf("wrapped: %w", New(409, "%s has no files on this host", "Lib")))
	if rec.Code != 409 || body(t, rec)["error"] != "Lib has no files on this host" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
}

func TestValidationIs400(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, nil, &Validation{Message: "body/name must be string"})
	if rec.Code != 400 || body(t, rec)["error"] != "body/name must be string" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestInternalErrorHidesDetailBehindARef(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	rec := httptest.NewRecorder()
	Write(rec, log, errors.New("open /secret/path: permission denied"))
	msg := body(t, rec)["error"]
	m := regexp.MustCompile(`^internal error \(ref ([0-9a-z]{8})\)$`).FindStringSubmatch(msg)
	if rec.Code != 500 || m == nil {
		t.Fatalf("%d %q", rec.Code, msg)
	}
	if strings.Contains(msg, "secret") {
		t.Fatal("detail reached the client")
	}
	if !strings.Contains(logs.String(), m[1]) || !strings.Contains(logs.String(), "/secret/path") {
		t.Fatalf("log lacks the ref or detail: %s", logs.String())
	}
}
