// Package config reads the server's settings from the same environment
// variables as server/src/config.ts, with the same defaults, plus the few
// the Go server adds (STUDIO_NODE_URL and the rest below).
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

	// FrontAuth: the Go server verifies identity itself (the session
	// proxy's JWT, OIDC sign-in) and hands it to Node in a token of its
	// own (docs/GO-PORT.md, Stage 1). STUDIO_FRONT_AUTH=1 or 0; unset, on
	// when the Go server starts Node (it then sets Node's AUTH_* itself) and
	// off with STUDIO_NODE_URL, where identity headers pass through to Node
	// unchanged as before.
	FrontAuth bool
	// IdentityAddr is the loopback address the key set for Node's tokens is
	// served on (STUDIO_IDENTITY_ADDR, default 127.0.0.1:0, any free port).
	IdentityAddr string

	Auth Auth
	OIDC OIDC
	Gufi Gufi

	// DisablePersonalKeys (DISABLE_PERSONAL_KEYS=1): no personal model keys
	// are restored from the browser's sealed cookies, as credentials.ts.
	DisablePersonalKeys bool
}

// Auth is the session proxy's JWT, as server/src/auth.ts reads it, and the
// claim mapping of server/src/identity.ts.
type Auth struct {
	JWKSURL  string // AUTH_JWKS_URL: verification is on when set
	Header   string // AUTH_HEADER, lower case, default x-pw-user-token
	Issuer   string // AUTH_ISSUER, optional
	Audience string // AUTH_AUDIENCE, optional
	// Required is AUTH_REQUIRED=1 or OIDC_REQUIRED=1: API requests need a
	// verified user.
	Required bool

	AccountClaim string // AUTH_ACCOUNT_CLAIM
	UIDClaim     string // AUTH_UID_CLAIM
	GIDClaim     string // AUTH_GID_CLAIM
}

// OIDC is OpenID Connect sign-in. Enabled when Issuer and ClientID are set.
type OIDC struct {
	Issuer                string // OIDC_ISSUER, trailing slashes removed
	ClientID              string // OIDC_CLIENT_ID
	ClientSecret          string // OIDC_CLIENT_SECRET; empty: a public client
	RedirectURI           string // OIDC_REDIRECT_URI; empty: from the request
	PostLogoutRedirectURI string // OIDC_POST_LOGOUT_REDIRECT_URI
	Scopes                string // OIDC_SCOPES, default "openid profile email groups"
	UsernameClaim         string // OIDC_USERNAME_CLAIM, default preferred_username
	// EndSession (OIDC_END_SESSION=1) also ends the provider's session on
	// sign-out. The default signs out of the Studio only.
	EndSession bool
	MaxAgeSec  int // OIDC_SESSION_MAX_AGE_SEC, default 43200
}

// Enabled reports whether OIDC sign-in is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" && o.ClientID != "" }

// Gufi is how the Go server runs GUFI's tools (internal/gufi).
type Gufi struct {
	// Concurrency is how many gufi_query runs go at once
	// (STUDIO_GUFI_CONCURRENCY, default 2); the rest wait in order.
	Concurrency int
	// Threads is gufi_query's -n (STUDIO_GUFI_THREADS, default one per
	// core up to 16, as gufi.ts).
	Threads int
	// QueueMax is how many runs may wait (STUDIO_GUFI_QUEUE, default 256);
	// past it a run is refused at once.
	QueueMax int
	// TimeoutMS bounds one run once started (STUDIO_GUFI_TIMEOUT_MS,
	// default 60000, gufi.ts's).
	TimeoutMS int
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

	switch env("STUDIO_FRONT_AUTH") {
	case "1":
		c.FrontAuth = true
	case "0":
		c.FrontAuth = false
	default:
		c.FrontAuth = c.NodeSpawn && c.NodeURL == ""
	}
	c.IdentityAddr = orDefault(env("STUDIO_IDENTITY_ADDR"), "127.0.0.1:0")
	c.Auth = Auth{
		JWKSURL:      env("AUTH_JWKS_URL"),
		Header:       strings.ToLower(orDefault(env("AUTH_HEADER"), "x-pw-user-token")),
		Issuer:       env("AUTH_ISSUER"),
		Audience:     env("AUTH_AUDIENCE"),
		Required:     env("AUTH_REQUIRED") == "1" || env("OIDC_REQUIRED") == "1",
		AccountClaim: env("AUTH_ACCOUNT_CLAIM"),
		UIDClaim:     env("AUTH_UID_CLAIM"),
		GIDClaim:     env("AUTH_GID_CLAIM"),
	}
	maxAge, err := strconv.Atoi(strings.TrimSpace(env("OIDC_SESSION_MAX_AGE_SEC")))
	if err != nil || maxAge <= 0 {
		maxAge = 43200
	}
	c.OIDC = OIDC{
		Issuer:                strings.TrimRight(strings.TrimSpace(env("OIDC_ISSUER")), "/"),
		ClientID:              strings.TrimSpace(env("OIDC_CLIENT_ID")),
		ClientSecret:          env("OIDC_CLIENT_SECRET"),
		RedirectURI:           strings.TrimSpace(env("OIDC_REDIRECT_URI")),
		PostLogoutRedirectURI: strings.TrimSpace(env("OIDC_POST_LOGOUT_REDIRECT_URI")),
		Scopes:                orDefault(strings.TrimSpace(env("OIDC_SCOPES")), "openid profile email groups"),
		UsernameClaim:         orDefault(strings.TrimSpace(env("OIDC_USERNAME_CLAIM")), "preferred_username"),
		EndSession:            env("OIDC_END_SESSION") == "1",
		MaxAgeSec:             maxAge,
	}
	c.Gufi = Gufi{
		Concurrency: positive(env("STUDIO_GUFI_CONCURRENCY"), 2),
		Threads:     positive(env("STUDIO_GUFI_THREADS"), min(max(runtime.NumCPU(), 1), 16)),
		QueueMax:    positive(env("STUDIO_GUFI_QUEUE"), 256),
		TimeoutMS:   positive(env("STUDIO_GUFI_TIMEOUT_MS"), 60000),
	}
	c.DisablePersonalKeys = env("DISABLE_PERSONAL_KEYS") == "1"
	return c, nil
}

// positive is v as a whole number above 0, else d.
func positive(v string, d int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return d
	}
	return n
}

// AuthEnabled is auth.ts's authEnabled with the OIDC branch's meaning:
// some source of verified identity is configured.
func (c Config) AuthEnabled() bool { return c.Auth.JWKSURL != "" || c.OIDC.Enabled() }

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
