// Package config reads the server's settings from the same environment
// variables as server/src/config.ts, with the same defaults, plus the few
// the Go server adds (STUDIO_NODE_URL and the rest below).
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Config is what the Go server needs at start.
type Config struct {
	// ProjectRoot holds web/, server/ and indexer/ (STUDIO_PROJECT_ROOT, else
	// found from the executable or the working directory).
	ProjectRoot string
	// WebDist is the built web app (STUDIO_WEB_DIST, else ProjectRoot/web/dist).
	WebDist string
	Host    string // HOST, default 0.0.0.0
	Port    int    // PORT, default 4080

	KBRoot    string // KB_ROOT
	IndexBase string // INDEX_BASE
	IndexDir  string // STUDIO_INDEX_DIR, else IndexBase
	GufiIndex string // IndexDir/gufi/<basename of KBRoot>
	GufiBin   string // GUFI_BIN, else the same search as config.ts

	// NodeURL is the Node server routes not yet ported go to
	// (STUDIO_NODE_URL). Empty with NodeSpawn set: the Go server starts Node.
	NodeURL string
	// NodeSpawn (STUDIO_NODE_SPAWN=1) starts `node server/dist/main.js` on a
	// loopback port and stops it on shutdown.
	NodeSpawn bool
	NodeBin   string // STUDIO_NODE_BIN, default node
	NodeMain  string // STUDIO_NODE_MAIN, default ProjectRoot/server/dist/main.js

	LogFormat string // STUDIO_LOG_FORMAT: json (default) or text
}

// Getenv is os.Getenv; tests replace it.
type Getenv func(string) string

// Load reads the configuration from env.
func Load(env Getenv) (Config, error) {
	if env == nil {
		env = os.Getenv
	}
	c := Config{
		Host:      orDefault(env("HOST"), "0.0.0.0"),
		NodeURL:   env("STUDIO_NODE_URL"),
		NodeSpawn: env("STUDIO_NODE_SPAWN") == "1",
		NodeBin:   orDefault(env("STUDIO_NODE_BIN"), "node"),
		LogFormat: orDefault(env("STUDIO_LOG_FORMAT"), "json"),
	}
	port, err := strconv.Atoi(orDefault(env("PORT"), "4080"))
	if err != nil || port < 0 || port > 65535 {
		return c, fmt.Errorf("PORT: not a port number: %q", env("PORT"))
	}
	c.Port = port
	c.ProjectRoot = env("STUDIO_PROJECT_ROOT")
	if c.ProjectRoot == "" {
		c.ProjectRoot = findProjectRoot()
	}
	c.WebDist = orDefault(env("STUDIO_WEB_DIST"), filepath.Join(c.ProjectRoot, "web", "dist"))
	c.NodeMain = orDefault(env("STUDIO_NODE_MAIN"), filepath.Join(c.ProjectRoot, "server", "dist", "main.js"))

	// As config.ts: KB_ROOT, else /data/knowledge-base where it exists,
	// else beside the code.
	c.KBRoot = env("KB_ROOT")
	if c.KBRoot == "" {
		if st, err := os.Stat("/data/knowledge-base"); err == nil && st.IsDir() {
			c.KBRoot = "/data/knowledge-base"
		} else {
			c.KBRoot = filepath.Join(c.ProjectRoot, "knowledge-base")
		}
	}
	c.IndexBase = orDefault(env("INDEX_BASE"), filepath.Join(c.ProjectRoot, "index"))
	c.IndexDir = orDefault(env("STUDIO_INDEX_DIR"), c.IndexBase)
	c.GufiIndex = filepath.Join(c.IndexDir, "gufi", filepath.Base(c.KBRoot))
	c.GufiBin = findGufiBin(env, c.ProjectRoot)
	return c, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// findProjectRoot looks up from the executable, then the working
// directory, for the folder that holds both web/ and server/.
func findProjectRoot() string {
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, s := range starts {
		for d := s; ; d = filepath.Dir(d) {
			if isDir(filepath.Join(d, "web")) && isDir(filepath.Join(d, "server")) {
				return d
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if len(starts) > 0 {
		return starts[len(starts)-1]
	}
	return "."
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// findGufiBin is config.ts's findGufiBin: GUFI_BIN; a bundled prefix
// (STUDIO_GUFI_PREFIX); gufi-install near the code or the working
// directory; PATH; the usual prefixes; else /opt/gufi/bin.
func findGufiBin(env Getenv, projectRoot string) string {
	if v := env("GUFI_BIN"); v != "" {
		return v
	}
	has := func(dir string) bool { return exists(filepath.Join(dir, "gufi_query")) }
	var first []string
	if p := env("STUDIO_GUFI_PREFIX"); p != "" {
		first = append(first, filepath.Join(p, "bin"))
	}
	first = append(first, filepath.Join(projectRoot, "gufi-install", "bin"), filepath.Join(projectRoot, "..", "gufi-install", "bin"))
	if wd, err := os.Getwd(); err == nil {
		first = append(first, filepath.Join(wd, "gufi-install", "bin"))
	}
	for _, c := range first {
		if has(c) {
			return c
		}
	}
	if p, err := exec.LookPath("gufi_query"); err == nil {
		return filepath.Dir(p)
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{"/opt/gufi/bin", "/usr/local/bin", filepath.Join(home, "usr", "local", "bin")} {
		if has(c) {
			return c
		}
	}
	return "/opt/gufi/bin"
}

// GufiAvailable is config.ts's gufiAvailable: gufi_query is installed and
// the main folder's index exists.
func (c Config) GufiAvailable() bool {
	return exists(filepath.Join(c.GufiBin, "gufi_query")) && exists(c.GufiIndex)
}
