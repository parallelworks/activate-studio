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
