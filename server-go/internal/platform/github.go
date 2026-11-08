package platform

import (
	"context"
	"os"
	"strings"
	"time"
)

// GitHub is the anonymous-or-token GitHub client githubWorkflows.ts and
// workflowReadme.ts use: the REST API with GITHUB_TOKEN or GH_TOKEN when
// one is set, and raw files, which need none.
//
// STUDIO_GITHUB_API_URL and STUDIO_GITHUB_RAW_URL point both at a local
// fake in tests; unset, they are GitHub's.
type GitHub struct{}

// APIBase is https://api.github.com, or the test override.
func (GitHub) APIBase() string { return envOr("STUDIO_GITHUB_API_URL", "https://api.github.com") }

// RawBase is https://raw.githubusercontent.com, or the test override.
func (GitHub) RawBase() string {
	return envOr("STUDIO_GITHUB_RAW_URL", "https://raw.githubusercontent.com")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return d
}

// Token is GITHUB_TOKEN || GH_TOKEN.
func (GitHub) Token() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

// API fetches an API path with GitHub's JSON accept header and the token.
func (g GitHub) API(ctx context.Context, path string, timeout time.Duration) (*Response, error) {
	h := map[string]string{"Accept": "application/vnd.github+json"}
	if t := g.Token(); t != "" {
		h["Authorization"] = "Bearer " + t
	}
	return Get(ctx, g.APIBase()+path, h, timeout)
}

// Raw fetches a raw file anonymously.
func (g GitHub) Raw(ctx context.Context, path string, timeout time.Duration) (*Response, error) {
	return Get(ctx, g.RawBase()+path, nil, timeout)
}
