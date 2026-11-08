package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndEnv(t *testing.T) {
	env := map[string]string{"STUDIO_PROJECT_ROOT": "/app", "KB_ROOT": "/data/kb", "GUFI_BIN": "/g/bin"}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "0.0.0.0" || c.Port != 4080 {
		t.Fatalf("listen %s:%d", c.Host, c.Port)
	}
	if c.IndexBase != "/app/index" || c.IndexDir != "/app/index" || c.GufiIndex != filepath.Join("/app/index", "gufi", "kb") {
		t.Fatalf("index paths %+v", c)
	}
	if c.WebDist != "/app/web/dist" || c.NodeMain != "/app/server/dist/main.js" || c.GufiBin != "/g/bin" {
		t.Fatalf("paths %+v", c)
	}
	env["STUDIO_INDEX_DIR"] = "/fast"
	env["PORT"] = "5000"
	c, _ = Load(func(k string) string { return env[k] })
	if c.IndexDir != "/fast" || c.IndexBase != "/app/index" || c.Port != 5000 {
		t.Fatalf("STUDIO_INDEX_DIR %+v", c)
	}
	env["PORT"] = "x"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("a bad PORT is an error")
	}
}

func TestStageOneDefaults(t *testing.T) {
	env := map[string]string{"STUDIO_PROJECT_ROOT": "/app"}
	get := func(k string) string { return env[k] }
	c, _ := Load(get)
	if c.Auth.Header != "x-pw-user-token" || c.Auth.Required || c.AuthEnabled() || c.FrontAuth {
		t.Fatalf("auth defaults %+v front %v", c.Auth, c.FrontAuth)
	}
	if c.OIDC.Scopes != "openid profile email groups" || c.OIDC.UsernameClaim != "preferred_username" || c.OIDC.MaxAgeSec != 43200 || c.OIDC.EndSession {
		t.Fatalf("oidc defaults %+v", c.OIDC)
	}
	if c.Gufi.Concurrency != 2 || c.Gufi.QueueMax != 256 || c.Gufi.TimeoutMS != 60000 || c.Gufi.Threads < 1 || c.Gufi.Threads > 16 {
		t.Fatalf("gufi defaults %+v", c.Gufi)
	}
	env["STUDIO_NODE_SPAWN"] = "1"
	env["OIDC_ISSUER"] = "https://idp.example.org/api/oidc/"
	env["OIDC_CLIENT_ID"] = " app "
	env["OIDC_REQUIRED"] = "1"
	env["AUTH_HEADER"] = "Authorization"
	env["OIDC_SESSION_MAX_AGE_SEC"] = "-5"
	env["STUDIO_GUFI_CONCURRENCY"] = "x"
	c, _ = Load(get)
	if !c.FrontAuth || !c.OIDC.Enabled() || c.OIDC.Issuer != "https://idp.example.org/api/oidc" || c.OIDC.ClientID != "app" {
		t.Fatalf("oidc %+v front %v", c.OIDC, c.FrontAuth)
	}
	if !c.Auth.Required || c.Auth.Header != "authorization" || c.OIDC.MaxAgeSec != 43200 || c.Gufi.Concurrency != 2 || !c.AuthEnabled() {
		t.Fatalf("parsed %+v %+v", c.Auth, c.OIDC)
	}
	env["STUDIO_NODE_URL"] = "http://127.0.0.1:1"
	if c, _ = Load(get); c.FrontAuth {
		t.Fatal("a Node server started elsewhere keeps its own auth unless STUDIO_FRONT_AUTH=1")
	}
	env["STUDIO_FRONT_AUTH"] = "1"
	if c, _ = Load(get); !c.FrontAuth {
		t.Fatal("STUDIO_FRONT_AUTH=1")
	}
}
