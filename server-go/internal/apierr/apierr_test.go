package apierr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// The bytes Fastify sends through sanitizedErrorHandler, from
// scripts/golden/apierr.mjs (testdata/golden.json).
type goldenBody struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
	Message     string `json:"message"`
}

func TestErrorBodiesMatchFastify(t *testing.T) {
	var msgs []string
	raw, _ := os.ReadFile("testdata/messages.json")
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	var g struct {
		Kb         []goldenBody `json:"kb"`
		Validation goldenBody   `json:"validation"`
		Internal   goldenBody   `json:"internal"`
	}
	raw, _ = os.ReadFile("testdata/golden.json")
	if err := json.Unmarshal(raw, &g); err != nil || len(g.Kb) != len(msgs) {
		t.Fatal("golden.json does not fit messages.json: regenerate it", err)
	}
	check := func(name string, rec *httptest.ResponseRecorder, want goldenBody, got []byte) {
		t.Helper()
		if rec.Code != want.Status || rec.Header().Get("Content-Type") != want.ContentType || !bytes.Equal(got, want.Body) {
			t.Errorf("%s: %d %q %q, Fastify %d %q %q", name, rec.Code, rec.Header().Get("Content-Type"), got, want.Status, want.ContentType, want.Body)
		}
		if rec.Header().Get("Content-Length") != fmt.Sprint(rec.Body.Len()) {
			t.Errorf("%s: Content-Length %q for %d bytes", name, rec.Header().Get("Content-Length"), rec.Body.Len())
		}
	}
	for i, m := range msgs {
		rec := httptest.NewRecorder()
		Write(rec, nil, New(400+i, "%s", m))
		check(fmt.Sprintf("message %d", i), rec, g.Kb[i], rec.Body.Bytes())
	}
	rec := httptest.NewRecorder()
	Write(rec, nil, &Validation{Message: g.Validation.Message})
	check("validation", rec, g.Validation, rec.Body.Bytes())
	rec = httptest.NewRecorder()
	Write(rec, nil, errors.New("secret detail"))
	check("internal", rec, g.Internal, regexp.MustCompile(`\(ref [0-9a-z]{8}\)`).ReplaceAll(rec.Body.Bytes(), []byte("(ref <ref>)")))
}

// Invalid UTF-8 in a Go message is the replacement character, written as
// itself as Node writes a string it decoded from the same bytes.
func TestInvalidUTF8IsTheReplacementCharacter(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, nil, New(400, "bad \xff byte \\u2028 kept"))
	if got := rec.Body.String(); got != "{\"error\":\"bad � byte \\\\u2028 kept\"}" {
		t.Fatalf("%q", got)
	}
}

func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/apierr.mjs", "testdata/messages.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(want)) {
		t.Fatal("Fastify's error bodies changed: regenerate testdata/golden.json")
	}
}

// A request whose client left is answered 499 and logs no error; a canceled
// context while the client is still there is still an internal error.
func TestClientGoneIsNotAnError(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	h := Handler(log, func(http.ResponseWriter, *http.Request) error { return fmt.Errorf("gufi: %w", context.Canceled) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/kb/topology", nil).WithContext(ctx))
	if rec.Code != StatusClientClosed || logged.Len() != 0 {
		t.Fatalf("client gone: %d, log %q", rec.Code, logged.String())
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/kb/topology", nil))
	if rec.Code != 500 || !strings.Contains(logged.String(), "unhandled route error") {
		t.Fatalf("client present: %d, log %q", rec.Code, logged.String())
	}
}
