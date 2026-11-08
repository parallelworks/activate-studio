package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// Providers are the values the core routes (/api/config, /api/settings,
// the setup and extension routes) read from modules other tracks own. Each
// is a function so a track fills one with a single assignment in its
// Register function, for example
//
//	a.Providers.Libraries = func(r *http.Request, kbLabel, viewer string) ([]json.RawMessage, error) { ... }
//
// Until its owner is ported, each one asks the Node server behind the Go
// server for the same value through a route of Node's (the defaults
// below), so the answer's shape is exactly Node's. In Node-free mode the
// default is the plainest true answer there is, named in each comment.
type Providers struct {
	// Libraries is listLibraries(kbLabel, viewer).map(publicLibrary): the
	// libraries a viewer can read, as /api/config sends them. Owner:
	// libraries track. Node: /api/config's libraries. Node-free: the main
	// folder alone (primaryOnly).
	Libraries func(r *http.Request, kbLabel, viewer string) ([]json.RawMessage, error)
	// PlatformAvailable is workflowsTab.ts's platformAvailable(viewer).
	// Owner: platform track. Node: /api/config's platform. Node-free: false.
	PlatformAvailable func(r *http.Request, viewer string) bool
	// GatewayConfigured is chat/gateway.ts's gatewayConfigured(): a
	// deployment credential exists. Owner: chat track. Node: /api/settings's
	// deploymentCredential. Node-free: PW_API_KEY or OPENAI_API_KEY is set.
	GatewayConfigured func(r *http.Request) bool
	// GatewayKey is chat/gateway.ts's gatewayKey(), for a brand image on a
	// platform URL that needs the deployment's credential. Owner: chat
	// track. Default: PW_API_KEY, else OPENAI_API_KEY (no shared key or CLI
	// credential until the chat track lands).
	GatewayKey func() string
	// InvalidateMCP is mcpClient.ts's invalidateRemote(), after a settings
	// write. Owner: chat track. Node rereads its MCP status when the
	// settings file changes (mcpClient.ts), so the default does nothing.
	InvalidateMCP func()
	// StopRagEndpoint is ragEndpoint.ts's stopRagEndpoint(), when the RAG
	// proxy is turned off. Owner: platform track. Node: POST
	// /api/rag/endpoint/stop. Node-free: nothing runs, so nothing.
	StopRagEndpoint func(r *http.Request)
	// GufiLogInfo is gufiLog.ts's gufiLogInfo(): {path, bytes}. Owner:
	// libraries track (gufi). Default: read here, it is one stat.
	GufiLogInfo func() any

	// SetupIndexState is what /api/setup/status reads from indexing.ts and
	// gufi.ts: the whole-folder build running or queued (currentBuild(),
	// JSON null for none), an earlier index of the folder
	// (earlierIndexOf(KB_ROOT), asked only when not indexed), and, with
	// files, the number of files indexed (corpusStats().files). Owner:
	// index track. Node: /api/setup/status. Node-free: no build, no
	// earlier index, files unknown.
	SetupIndexState func(r *http.Request, indexed, files bool) (SetupIndex, error)
	// StartBuild is indexing.ts's startBuild(): the whole-folder build, or
	// the one running. Owner: index track. Node: POST /api/index/build.
	// Node-free: a 501.
	StartBuild func(r *http.Request) (json.RawMessage, error)
	// IndexDir is indexing.ts's incrementalIndexDir(rel). Owner: index
	// track. Node: POST /api/index/dir. Node-free: nothing (the sweep
	// finds it).
	IndexDir func(r *http.Request, rel string) error
	// ApplyLabels is tags.ts's applyTagsCore(paths, add, remove) on the
	// main folder. Owner: files track. Node: POST /api/kb/tags.
	// Node-free: nothing (labels are optional where they are used).
	ApplyLabels func(ctx context.Context, r *http.Request, paths, add, remove []string) error
}

// SetupIndex is the index state /api/setup/status shows.
type SetupIndex struct {
	Job          json.RawMessage // the build, or null
	EarlierIndex json.RawMessage // {builtAt} or null
	Files        *int
}

// Unported is the error a provider without an owner returns in Node-free
// mode where there is no plain answer; routes turn it into a 501.
type Unported struct{ What string }

func (e *Unported) Error() string { return e.What + " is not ported to Go yet" }

func defaultProviders(a *App) Providers {
	p := Providers{
		GatewayKey: func() string {
			if k := os.Getenv("PW_API_KEY"); k != "" {
				return k
			}
			return os.Getenv("OPENAI_API_KEY")
		},
		InvalidateMCP: func() {},
		GufiLogInfo: func() any {
			// gufiLog.ts: LOG_DIR/gufi-commands.log, LOG_DIR being
			// STUDIO_DATA_DIR/logs, else INDEX_BASE/logs (indexLog.ts).
			file := filepath.Join(LogDir(a.Cfg.IndexBase), "gufi-commands.log")
			var bytes int64
			if st, err := os.Stat(file); err == nil {
				bytes = st.Size()
			}
			return map[string]any{"path": file, "bytes": bytes}
		},
	}
	if a.NodeAPI == nil {
		p.Libraries = func(r *http.Request, kbLabel, viewer string) ([]json.RawMessage, error) {
			return []json.RawMessage{a.primaryOnly(kbLabel)}, nil
		}
		p.PlatformAvailable = func(*http.Request, string) bool { return false }
		p.GatewayConfigured = func(*http.Request) bool { return p.GatewayKey() != "" }
		p.StopRagEndpoint = func(*http.Request) {}
		p.SetupIndexState = func(*http.Request, bool, bool) (SetupIndex, error) {
			return SetupIndex{Job: json.RawMessage("null"), EarlierIndex: json.RawMessage("null")}, nil
		}
		p.StartBuild = func(*http.Request) (json.RawMessage, error) { return nil, &Unported{"the index build"} }
		p.IndexDir = func(*http.Request, string) error { return nil }
		p.ApplyLabels = func(context.Context, *http.Request, []string, []string, []string) error { return nil }
		return p
	}
	n := a.NodeAPI
	p.Libraries = func(r *http.Request, _, _ string) ([]json.RawMessage, error) {
		var c struct {
			Libraries []json.RawMessage `json:"libraries"`
		}
		err := n.Do(r, "GET", "/api/config", nil, &c)
		return c.Libraries, err
	}
	p.PlatformAvailable = func(r *http.Request, _ string) bool {
		var c struct {
			Platform bool `json:"platform"`
		}
		return n.Do(r, "GET", "/api/config", nil, &c) == nil && c.Platform
	}
	p.GatewayConfigured = func(r *http.Request) bool {
		var c struct {
			DeploymentCredential bool `json:"deploymentCredential"`
		}
		return n.Do(r, "GET", "/api/settings", nil, &c) == nil && c.DeploymentCredential
	}
	p.StopRagEndpoint = func(r *http.Request) {
		if err := n.Do(r, "POST", "/api/rag/endpoint/stop", map[string]any{}, nil); err != nil {
			a.Log.Warn("rag endpoint stop", "err", err.Error())
		}
	}
	p.SetupIndexState = func(r *http.Request, indexed, files bool) (SetupIndex, error) {
		q := "/api/setup/status"
		if files {
			q += "?files=1"
		}
		var s struct {
			Primary struct {
				Files        *int            `json:"files"`
				EarlierIndex json.RawMessage `json:"earlierIndex"`
			} `json:"primary"`
			Job json.RawMessage `json:"job"`
		}
		if err := n.Do(r, "GET", q, nil, &s); err != nil {
			return SetupIndex{}, err
		}
		out := SetupIndex{Job: orNull(s.Job), EarlierIndex: orNull(s.Primary.EarlierIndex), Files: s.Primary.Files}
		return out, nil
	}
	p.StartBuild = func(r *http.Request) (json.RawMessage, error) {
		var job json.RawMessage
		err := n.Do(r, "POST", "/api/index/build", map[string]any{}, &job)
		return job, err
	}
	p.IndexDir = func(r *http.Request, rel string) error {
		return n.Do(r, "POST", "/api/index/dir", map[string]any{"path": rel}, nil)
	}
	p.ApplyLabels = func(ctx context.Context, r *http.Request, paths, add, remove []string) error {
		if remove == nil {
			remove = []string{}
		}
		return n.Do(r, "POST", "/api/kb/tags", map[string]any{"paths": paths, "add": add, "remove": remove}, nil)
	}
	return p
}

func orNull(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

// primaryOnly is the main folder as publicLibrary writes it, for Node-free
// mode before the libraries track lands: indexed once its index has a
// db.db at its root, with no vectors claimed, no meaning or depth setting
// and no format note.
func (a *App) primaryOnly(kbLabel string) json.RawMessage {
	_, err := os.Stat(filepath.Join(a.Cfg.GufiIndex, "db.db"))
	indexed := err == nil
	b, _ := json.Marshal(struct {
		ID         string `json:"id"`
		Label      string `json:"label"`
		Primary    bool   `json:"primary"`
		Writable   bool   `json:"writable"`
		Pinned     bool   `json:"pinned"`
		Source     bool   `json:"source"`
		Caps       any    `json:"caps"`
		Site       any    `json:"site"`
		Meaning    any    `json:"meaning"`
		Depth      any    `json:"depth"`
		FormatNote any    `json:"formatNote"`
	}{"kb", kbLabel, true, true, true, true, map[string]bool{"index": indexed, "fullText": indexed, "vectors": false}, nil, nil, nil, nil})
	return b
}

// LogDir is indexLog.ts's LOG_DIR: where the Studio's logs go, the data
// folder's, else beside the index.
func LogDir(indexBase string) string {
	if d := os.Getenv("STUDIO_DATA_DIR"); d != "" {
		return filepath.Join(d, "logs")
	}
	return filepath.Join(indexBase, "logs")
}
