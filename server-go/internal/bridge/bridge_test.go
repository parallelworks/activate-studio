package bridge

import (
	"net/http/httptest"
	"testing"
)

func TestBridge(t *testing.T) {
	t.Setenv("STUDIO_BRIDGE_SECRET", "")
	RegisterPrivatePrefix("/api/__bridge/")
	RegisterNodeEnv("t", func(front string) map[string]string {
		return map[string]string{"STUDIO_GO_T": "1", "AUTH_HEADER": "module"}
	})
	if !IsPrivate("/api/__bridge/x") || IsPrivate("/api/kb/tree") {
		t.Fatal("prefix")
	}
	env := NodeEnv(map[string]string{"AUTH_HEADER": "x-studio-identity"}, "http://127.0.0.1:1")
	if env["STUDIO_GO_T"] != "1" || env["AUTH_HEADER"] != "x-studio-identity" || env["STUDIO_BRIDGE_SECRET"] == "" || env["STUDIO_FRONT_URL"] != "http://127.0.0.1:1" {
		t.Fatalf("env %v", env)
	}
	r := httptest.NewRequest("GET", "/api/__bridge/x", nil)
	if Authorized(r) {
		t.Fatal("no secret authorized")
	}
	r.Header.Set(Header, "wrong")
	if Authorized(r) {
		t.Fatal("wrong secret authorized")
	}
	Sign(r)
	if !Authorized(r) {
		t.Fatal("signed refused")
	}
}
