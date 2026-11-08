# Porting the server to Go

This is the plan and the state of the port of the Studio's server (`server/src`, Node and Fastify) and the indexer's Python steps (`indexer/`) to Go. The web app stays TypeScript in the browser. The Go code is the module `github.com/parallelworks/activate-studio/server-go` in `server-go/`, on the same Go version as the desktop shell (`desktop/wails`, go 1.26.0).

## Strategy

The Go server (`server-go/cmd/studio-server`) takes the Node server's place on `HOST:PORT`. It answers the routes ported to Go, serves `web/dist`, and reverse-proxies every other request to the Node server, which runs on a loopback port behind it. Routes move to Go one module at a time; at every step the web app talks to one address and works unchanged, and the same HTTP API is the conformance check.

| Path | Goes to |
|---|---|
| a route registered in `internal/server/server.go` (`routes()`) | Go |
| `/api/*`, `/v1/*`, `/internal/*`, `/manifest.webmanifest` | Node (Node's routes outside `/api` win over a static file of the same name, as in Fastify) |
| any other GET or HEAD | the file in `web/dist`, else `index.html`, with Node's cache rules (`/assets/*-<hash>.*` immutable for a year, everything else `no-store, must-revalidate`); text types of 1 KB or more brotli or gzip compressed, which Node does not do |

The proxy keeps the browser's `Host`, adds `X-Forwarded-*`, and writes `text/event-stream` responses and responses without a `Content-Length` out as it reads them, so the two event streams (`/api/chat/stream`, `/v1/chat/completions`) and the session relay are not buffered; a response of known length goes out in one write when it ends (`FlushInterval: 0`). Static files are compressed once per file and encoding, kept in memory by path, size and modification time, and carry `Vary: Accept-Encoding` and an ETag per encoding (Node's `W/"size-mtime"`, with `-br` or `-gz` for a compressed body). When Node does not answer, an API request gets a 502 with `{error}`. Node has no route that checks the client address, so being behind a proxy changes nothing for it.

Two rules decide what can move when:

1. A module's in-memory state moves with all of its routes and background jobs. Node caches files it owns: a Go route that writes one would leave Node on the old contents. So a state file moves with its writer and every caching reader, or Node first gets a reread on change. `settings.json` has one since stage 1 (below); `semanticMap.ts` does the same for the map's JSON.
2. Auth moves before the first `/api` route. `auth.ts` verifies the platform JWT (`AUTH_JWKS_URL`, `AUTH_HEADER`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_REQUIRED`) on `/api/*` only, with a mission-token exception for `/api/mcp`, and `credentials.ts` restores personal model keys from the sealed `ade_mk`/`ade_mkp` cookies. A Go route under `/api` needs both, with the same error body (`sanitizedErrorHandler`: `KbError` status and `{error}`, 400 for validation, 500 `internal error (ref <id>)`).

Three callers reach the server over loopback and must reach whichever process owns their route: the `gufi_vt` wrapper (`POST /internal/gufi-vt`, at `HOST`/`PORT` from `gufiVt.ts:selfUrl()`), mission workers (`/api/mcp` at `STUDIO_SELF_URL`, default `http://127.0.0.1:$PORT`), and the RAG endpoint forwarder (`/v1/*` at `STUDIO_TARGET_PORT`). With Node behind the Go server, Node's `PORT` is its loopback port, so these reach Node directly; when one of those routes moves to Go, the address it is given must become the Go server's.

## The Go module

```
server-go/
  go.mod                         module .../server-go, go 1.26.0
  cmd/studio-server/             the server: config, Node behind it, signals
  cmd/semantic-map/              port of indexer/semantic_map.py, same CLI
  internal/config/               env vars, as server/src/config.ts reads them
  internal/server/               routing, static files, reverse proxy, access log
  internal/nodeproc/             starts and stops the Node server
  internal/semanticmap/          the semantic map (package; the CLI wraps it)
  internal/apierr/               the API error body (sanitizedErrorHandler)
  internal/settings/             settings.json and effectiveSettings(), reread on change
  internal/auth/                 proxy JWT and JWKS, OIDC sign-in, sessions, the hand-off to Node
  internal/keycookie/            the sealed personal-key cookies (credentials.ts's format)
  internal/gufi/                 GUFI plans, output parsing, executor and admission runner
  scripts/golden/                Node scripts that write the golden files the Go tests compare with
  cmd/studio-extract/            the document extraction pass (preextract plus enrich.py's extraction)
  internal/extract/              the pass: downmark in process, OCR, helpers, cache, external hook
  internal/extract/xls/          Excel 97-2003 text, as enrich.py's extract_xls
  internal/extract/odf/          OpenDocument text, as enrich.py's extract_odf
  internal/extract/ppt/          PowerPoint 97-2003 text, as enrich.py's extract_ppt
  internal/extract/pytext/       Python and JavaScript string semantics for the readers
  scripts/compare_semantic_map.py  Python against Go on the same inputs
```

Dependencies: `modernc.org/sqlite` (pure Go SQLite, no cgo, so cross-compiling for macOS, Linux and Windows stays `GOOS=... go build`) and `gonum.org/v1/gonum` (the PCA's eigendecomposition). The server itself uses the standard library (`net/http`, `httputil.ReverseProxy`, `log/slog`) and `github.com/andybalholm/brotli` (pure Go, no dependencies outside its tests) for brotli.

```
cd server-go
go test ./...
go build -o bin/studio-server ./cmd/studio-server
go build -o bin/semantic-map ./cmd/semantic-map
pnpm build                                   # at the repo root: web/dist and server/dist
STUDIO_NODE_SPAWN=1 ./bin/studio-server      # serves on PORT (4080), Node behind it
```

Stripped (`-trimpath -ldflags "-s -w"`) on linux/amd64: `studio-server` 7.8 MB, `semantic-map` 8.0 MB.

Environment the Go server reads today:

| Variable | Default | Use |
|---|---|---|
| `HOST`, `PORT` | `0.0.0.0`, 4080 | listen address, as Node |
| `KB_ROOT`, `INDEX_BASE`, `STUDIO_INDEX_DIR`, `GUFI_BIN`, `STUDIO_GUFI_PREFIX` | as `config.ts` | paths; `/healthz` reports `gufi` from them |
| `STUDIO_PROJECT_ROOT` | the folder above the binary or working directory holding `web/` and `server/` | where `web/dist` and `server/dist` are |
| `STUDIO_WEB_DIST` | `<root>/web/dist` | the built web app |
| `STUDIO_NODE_URL` | none | a Node server already running |
| `STUDIO_NODE_SPAWN` | off | `1`: start `node server/dist/main.js` on a free loopback port (`HOST=127.0.0.1`), wait until it is ready (a line Node writes once it listens on the pipe it is given as `STUDIO_READY_FD`, else its `/healthz`, polled from 5 ms up to every 100 ms), stop it first on SIGTERM (it lets open streams finish, up to 20 s) |
| `STUDIO_NODE_BIN`, `STUDIO_NODE_MAIN` | `node`, `<root>/server/dist/main.js` | how to start Node |
| `STUDIO_LOG_FORMAT` | `json` | `text` for a terminal; one access-log line per request with `via` = `go`, `node` or `static` |
| `STUDIO_SEMANTIC_MAP_BIN` | none | read by the Node server: run this binary in place of `indexer/semantic_map.py` |
| `STUDIO_FRONT_AUTH` | on with `STUDIO_NODE_SPAWN=1`, off with `STUDIO_NODE_URL` | `1`: verify identity here and hand it to Node (Stage 1); `0`: pass identity headers through to Node |
| `STUDIO_IDENTITY_ADDR` | `127.0.0.1:0` | loopback address of the key set Node verifies the hand-off with |
| `AUTH_*`, `OIDC_*` | as Node | identity, read by the Go server in front-auth mode (DEPLOY.md, Sign-in) |
| `STUDIO_GUFI_CONCURRENCY`, `STUDIO_GUFI_QUEUE`, `STUDIO_GUFI_TIMEOUT_MS`, `STUDIO_GUFI_THREADS` | 2, 256, 60000, cores up to 16 | the GUFI runner (Stage 1) |

When Node exits on its own the Go server exits with Node's code, so exit 76 (a relaunch after an update, `lifecycle.ts`) still reaches the launcher. The desktop shell's supervisor (`desktop/wails/supervisor.go`) starts Node by default. With `STUDIO_GO_SERVER=1` in the shell's environment it starts `studio-server` with `STUDIO_NODE_SPAWN=1` in its place and sets `STUDIO_SEMANTIC_MAP_BIN` to the `semantic-map` beside it: in the Linux payload the two binaries are `app/bin/studio-server` and `app/bin/semantic-map`, which `studio-run.sh` starts (the payload build does not put them there yet), and in a checkout `server-go/bin/`. Without the binary the shell starts Node as before. The process tree is then the shell, `studio-run.sh`, `studio-server`, Node.

## HTTP API inventory

219 method and path pairs from 212 registrations in 32 files (93 `.ts` files, about 32,800 lines in `server/src`; tests about 19,500 lines). Loops expand `act()` in `fleet.ts` to five routes, the `ssh`/`pw` loop in `userLibraries.ts` to four, and the session relay is GET and HEAD. On top of these, `main.ts` serves `web/dist` (`@fastify/static`, wildcard) with an `index.html` fallback outside `/api`, and Fastify answers HEAD for every GET. There are no websocket routes. Streams: SSE in `/api/chat/stream` (761-line handler: the agent loop) and `/v1/chat/completions`, the relay in `/api/session-proxy/:host/*`, file downloads, and multipart uploads (`/api/kb/upload`, `/api/chat/attachments`, `/api/setup/icon`, `/api/extensions/persona-icon`). Ported: `/healthz`, and in front-auth mode `/api/whoami` and the new `/api/auth/*` (Stage 1).

Dependencies, as an upper bound from the static call graph: `gufi` the GUFI binaries (`gufi_query`, `gufi_sqlite3`, `gufi_vt`, `gufi_dir2index`); `ssh` plain ssh or `pw ssh` for libraries on other systems; `pw` the pw CLI, `pw-token` only to renew the deployment token; `python` an indexer script (`enrich.py`, `embed.py`, `embed_query.py`, `semantic_map.py`, `fetch_embed.py`, or `python -c` with `sqlite3`); `node:sqlite` Node's built-in SQLite (`textCleanup.ts`, `refine.ts`; every other SQLite access goes through GUFI's binaries or Python); `llm` the OpenAI-compatible gateway; `platform` the ACTIVATE API; `github`; `mcp` MCP servers; `soffice` LibreOffice and poppler; `xattr` `getfattr`/`setfattr`; `http` another outbound fetch. Almost every handler also reads `settings.json`.

| Source | Lines | Routes | Method and path | Dependencies |
|---|---|---|---|---|
| routes.ts | 998 | 37 | GET `/healthz`, GET `/api/whoami`, GET `/api/config`, GET `/api/version`, GET `/api/help`, GET `/api/brand-icon`, GET `/api/favicon`, GET `/manifest.webmanifest`, GET `/api/kb/tree`, GET `/api/kb/file`, GET `/api/kb/download`, GET `/api/kb/raw`, GET `/api/kb/html`, GET `/api/kb/pdf`, POST `/api/kb/delete`, POST `/api/kb/label-suggestions`, GET `/api/kb/dirs`, POST `/api/kb/move`, POST `/api/kb/copy`, POST `/api/kb/rename`, DELETE `/api/kb/dir`, DELETE `/api/kb/file`, GET `/api/kb/stats`, GET `/api/search`, GET `/api/jobs/:id`, GET `/api/workflows`, GET `/api/kb/dag`, GET `/api/workflows/:name`, GET `/api/kb/pdf-info`, GET `/api/kb/pdf-page`, GET `/api/kb/office-preview`, GET `/api/kb/office-media`, GET `/api/libraries`, POST `/api/libraries`, PUT `/api/libraries/:id/depth`, PUT `/api/libraries/:id/meaning`, DELETE `/api/libraries/:id` | gufi, ssh, pw-token, http, pw, xattr, soffice, python, node:sqlite, llm, platform |
| indexRoutes.ts | 236 | 16 | GET `/api/index/status`, GET `/api/index/extractors`, POST `/api/index/dir`, POST `/api/index/job`, GET `/api/index/job/:id`, POST `/api/index/job/:id/cancel`, POST `/api/index/cancel`, POST `/api/index/build`, POST `/api/reindex`, DELETE `/api/index`, POST `/api/index/sweep`, GET `/api/index/log`, POST `/api/index/log/reveal`, GET `/api/index/settings`, PUT `/api/index/settings`, POST `/api/index/location` | gufi, ssh, python, node:sqlite, pw |
| chat/routes.ts | 1805 | 15 | GET `/api/chat/models`, GET `/api/ai/health`, GET `/api/me/model-key`, POST `/api/me/model-key`, POST `/api/me/model-key/clear`, PUT `/api/me/model-key`, DELETE `/api/me/model-key`, POST `/api/me/model-key/share`, POST `/api/me/model-key/unshare`, POST `/api/me/model-key/test`, GET `/api/runs`, GET `/api/chat/suggestions`, GET `/api/chat/tool-options`, GET `/api/chat/tools`, POST `/api/chat/stream` | pw-token, llm, pw, mcp, gufi, ssh, python, platform, github, bash, xattr |
| userLibraries.ts | 1561 | 13 | GET `/api/user-libraries`, GET `/api/user-libraries/pw/systems`, POST `/api/user-libraries`, POST `/api/user-libraries/ssh/test`, POST `/api/user-libraries/ssh`, POST `/api/user-libraries/pw/test`, POST `/api/user-libraries/pw`, POST `/api/user-libraries/{ssh,pw}/build/test`, POST `/api/user-libraries/{ssh,pw}/build`, POST `/api/user-libraries/:id/refresh`, PUT `/api/user-libraries/:id/ssh-options`, POST `/api/user-libraries/warm`, DELETE `/api/user-libraries/:id` | ssh, pw, python |
| workflowsTab.ts | 587 | 12 | GET `/api/workflows/catalog`, GET `/api/workflows/collection`, GET `/api/workflows/item/icon`, GET `/api/workflows/item/form`, GET `/api/platform/clusters`, GET `/api/platform/partitions`, GET `/api/platform/buckets`, GET `/api/workflows/github/browse`, GET `/api/workflows/github/resolve`, POST `/api/workflows/item/run`, POST `/api/workflows/item/install`, GET `/api/workflows/runs` | pw, platform, github |
| accessRoutes.ts | 282 | 10 | GET `/api/access`, PUT `/api/access/cli/path`, PUT `/api/access/source`, POST `/api/access/cli/login`, GET `/api/access/cli/login`, POST `/api/access/cli/login/cancel`, GET `/api/access/subscriptions`, POST `/api/access/codex`, POST `/api/access/codex/:id`, POST `/api/access/copilot` | pw, pw-token, platform |
| agentSites.ts | 279 | 9 | GET `/api/agent-sites`, GET `/api/agent-sites/:site/status`, GET `/api/agent-sites/:site/sessions`, POST `/api/agent-sites/:site/sessions`, GET `/api/agent-sites/:site/sessions/:id`, POST `/api/agent-sites/:site/sessions/:id/goal`, POST `/api/agent-sites/:site/sessions/:id/steer`, POST `/api/agent-sites/:site/sessions/:id/approvals/:approval`, POST `/api/agent-sites/:site/sessions/:id/interrupt` | pw |
| semanticMap.ts | 885 | 9 | GET `/api/kb/semantic-map`, GET `/api/kb/semantic-map/cluster/:c`, GET `/api/kb/semantic-map/neighbors`, GET `/api/semantic-maps`, GET `/api/libraries/:id/semantic-map`, GET `/api/libraries/:id/semantic-map/cluster/:c`, GET `/api/libraries/:id/semantic-map/neighbors`, POST `/api/libraries/:id/semantic-map/refresh`, POST `/api/kb/semantic-map/refresh` | gufi, ssh, pw-token, python, pw |
| setup.ts | 341 | 8 | GET `/api/setup/status`, GET `/api/setup/browse`, POST `/api/setup/primary-folder`, POST `/api/setup/port`, POST `/api/setup/index`, POST `/api/setup/icon`, DELETE `/api/setup/icon`, POST `/api/setup/complete` | gufi, ssh, pw, python, node:sqlite |
| tasks.ts | 1011 | 8 | GET `/api/tasks`, GET `/api/tasks/:id`, POST `/api/tasks`, GET `/api/tasks/:id/agents/:name/output`, GET `/api/tasks/runtime`, POST `/api/tasks/:id/agents/:name/approvals/:approvalId`, POST `/api/tasks/:id/agents/:name/direction`, POST `/api/tasks/:id/stop` | pw |
| conversations.ts | 454 | 7 | GET `/api/chat/conversations`, GET `/api/chat/conversations/:id`, POST `/api/chat/conversations`, PATCH `/api/chat/conversations/:id`, POST `/api/chat/steer`, POST `/api/chat/conversations/delete`, DELETE `/api/chat/conversations/:id` | gufi, python, node:sqlite |
| extensions.ts | 374 | 7 | POST `/api/extensions/persona`, POST `/api/extensions/skill`, GET `/api/extensions/persona`, GET `/api/extensions/skill`, POST `/api/extensions/persona-icon`, DELETE `/api/extensions/persona`, GET `/api/extensions` | local files |
| history.ts | 360 | 6 | GET `/api/kb/history`, DELETE `/api/kb/history/snapshot`, POST `/api/kb/history/prune`, POST `/api/kb/history/capture`, GET `/api/kb/history/level`, GET `/api/kb/history/diff` | gufi, ssh, pw |
| fleet.ts | 338 | 5 | GET `/api/fleet/templates`, GET `/api/fleet`, POST `/api/fleet`, GET `/api/fleet/:id`, POST `/api/fleet/:id/{tick,pause,resume,retire,message}` | local files |
| query.ts | 781 | 5 | POST `/api/query`, POST `/api/query/sql`, GET `/api/query/saved`, POST `/api/query/saved`, DELETE `/api/query/saved/:id` | gufi, ssh, pw |
| updates.ts | 1245 | 5 | GET `/api/updates`, GET `/api/changelog`, POST `/api/updates/check`, POST `/api/updates/install`, POST `/api/updates/rollback` | github, rpm/launchctl |
| attachments.ts | 127 | 4 | POST `/api/chat/attachments`, GET `/api/chat/attachments`, GET `/api/chat/attachments/:id/download`, DELETE `/api/chat/attachments/:id` | gufi, python, node:sqlite |
| runPane.ts | 196 | 4 | GET `/api/runs/item`, GET `/api/runs/file`, GET `/api/session-proxy/:host`, GET+HEAD `/api/session-proxy/:host/*` | pw-token, platform, pw, http |
| tags.ts | 423 | 4 | POST `/api/kb/tags/recover`, GET `/api/tags`, GET `/api/kb/tags`, POST `/api/kb/tags` | gufi, ssh, python, node:sqlite, xattr |
| libraryBuild.ts | 740 | 3 | POST `/api/libraries/build/check`, POST `/api/libraries/build`, POST `/api/libraries/:id/rebuild` | gufi, ssh, python, node:sqlite, pw |
| ragEndpoint.ts | 179 | 3 | GET `/api/rag/endpoint`, POST `/api/rag/endpoint/start`, POST `/api/rag/endpoint/stop` | pw |
| ragProxy.ts | 441 | 3 | GET `/api/rag/calls`, GET `/v1/models`, POST `/v1/chat/completions` | pw-token, llm, gufi, ssh, pw, python, platform, github, mcp, bash |
| settings.ts | 481 | 3 | GET `/api/settings`, GET `/api/settings/mcp-servers`, PUT `/api/settings` | pw-token, mcp |
| uploads.ts | 151 | 3 | POST `/api/kb/mkdir`, POST `/api/kb/upload`, POST `/api/kb/upload-url` | gufi, ssh, python, node:sqlite, http |
| mcp.ts | 180 | 2 | POST `/api/mcp`, GET `/api/mcp` | gufi, ssh, pw, platform, github, mcp, bash |
| originals.ts | 293 | 2 | PUT `/api/libraries/:id/read-through`, POST `/api/libraries/:id/read-through/test` | gufi, ssh, pw |
| sessions.ts | 167 | 2 | GET `/api/sessions`, GET `/api/sessions/item` | pw-token, platform |
| topology.ts | 135 | 2 | GET `/api/kb/topology`, GET `/api/libraries/:id/topology` | gufi, ssh, pw |
| workflowReadme.ts | 204 | 2 | GET `/api/workflows/item/readme`, GET `/api/workflows/item/asset` | pw, platform, github |
| gufiVt.ts | 396 | 1 | POST `/internal/gufi-vt` | ssh, pw |
| libraryDetails.ts | 197 | 1 | GET `/api/kb/details` | gufi, ssh, pw |
| model.ts | 210 | 1 | GET `/api/kb/model` | local files |

## Background work

| What | Source | Trigger | Runs |
|---|---|---|---|
| Index sweep | indexing.ts | under the index lock, every `sweepIntervalSec` (300, at least 60); debounced kicks | index jobs, then a history snapshot when due |
| Index jobs | indexing.ts, jobs.ts | routes (index, upload, move, copy, rename, delete, attachments, conversation export, setup) and the sweep; one at a time | `node dist/preextract.js`, `gufi_dir2index`, `enrich.py`, `embed.py` with `gufi_sqlite3`, then a map rebuild |
| Library builds and sync | libraryBuild.ts | at start (resume), every `sweepIntervalSec`, build routes | the same tools for each built library |
| Semantic map | semanticMap.ts, remoteMap.ts | 5 s after a build or prune, 10 min after a meaning phase, refresh routes, a missing map when asked for | `semantic_map.py` or `STUDIO_SEMANTIC_MAP_BIN`; for remote libraries two `gufi_query` walks over ssh |
| History snapshots | history.ts | after a sweep, at most every `historyIntervalSec` (1 day) | `gufi_query` |
| Query embedder | embedWorker.ts | first vector search; stops when idle | long-lived `embed_query.py` (ONNX) over stdio |
| Directory scan | dirScan.ts | per scan | a `worker_threads` worker |
| Fleet | fleet.ts | every `STUDIO_FLEET_TICK_MS` (30 s) | mission tasks |
| Mission tasks | tasks.ts | per agent, polls every 20 s and 2 s | pw CLI, the pw code daemon socket; workers call `/api/mcp` |
| Workflow run watcher | runs.ts | per unfinished run, every `STUDIO_RUN_POLL_MS` (60 s) | `pw workflows runs view` |
| Updates | updates.ts | 60 s after start, then every 2 min when due (personal installs only) | GitHub releases, `rpm`, `launchctl`, `lsregister`; restart with exit 76 |
| RAG endpoint | ragEndpoint.ts | at start when enabled, or the start route | long-lived `pw endpoints run ... node -e FORWARDER` (Go: `studio-server rag-forward`) |
| ssh pool and pw code bridge sweepers | remoteExec.ts, pwcodeBridge.ts | every 60 s | close idle channels |
| Remote embed heartbeat | remoteEmbed.ts | during a remote embed, every 60 s | ssh |
| Label overlay recovery | tags.ts | once, 20 s after start | `gufi_query`, xattrs |
| Conversation export reindex | conversations.ts | 5 s after an export, 2 s after deletes | an index job |
| CLI device login | access.ts | the login route | `pw auth login --device` |
| SSE keepalive | chat/routes.ts, ragProxy.ts | every 3 s during a stream | none |

## Environment

The Node server reads 130 variables. By area (defaults and use are in each module, and the settings ones are documented in `docs/REFERENCE.md`):

- Paths and process: `KB_ROOT`, `INDEX_BASE`, `STUDIO_INDEX_DIR`, `STUDIO_DATA_DIR`, `HOST`, `PORT`, `PATH`, `PYTHON_BIN`, `GUFI_BIN`, `STUDIO_GUFI_PREFIX`, `SOFFICE_BIN`, `PW_CLI`, `HELP_FILE`, `STUDIO_CHANGELOG_FILE`, `STUDIO_CONFIG_FILE`, `STUDIO_APP_BUNDLE`, `STUDIO_PAYLOAD_ROOT`, `STUDIO_LSREGISTER`, `STUDIO_RESTARTABLE`, `STUDIO_SUPERVISOR`, `XDG_RUNTIME_DIR`, `XDG_STATE_HOME`, `STUDIO_SEMANTIC_MAP_BIN`.
- Auth and identity: `AUTH_JWKS_URL`, `AUTH_HEADER`, `AUTH_ISSUER`, `AUTH_AUDIENCE`, `AUTH_REQUIRED`, `APP_USER_ID`, `APP_USERNAME`, `APP_USER_NAME`, `DISABLE_PERSONAL_KEYS`.
- Platform and models: `PW_GATEWAY_URL`, `OPENAI_BASE_URL`, `PW_API_KEY`, `OPENAI_API_KEY`, `PW_CONTEXT`, `PW_CREDENTIALS_DIR`, `PW_PLATFORM_HOST`, `PW_ALLOCATION`, `PW_CODE_SOCKET`, `SIDECAR_MODEL_URL`, `SIDECAR_MODEL_KEY`, `SIDECAR_ENDPOINT`, `CHAT_MAX_TOKENS`, `CHAT_NO_STREAM`, `CHAT_SYSTEM_IN_USER`, `STUDIO_PROBE_PROVIDERS`, `STUDIO_STREAM_IDLE_MS`, `RAG_FAIL_OPEN`, `GITHUB_TOKEN`, `GH_TOKEN`, `STUDIO_UPDATE_URL`.
- Network: `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` (and lower case), `STUDIO_SYSTEM_CA`.
- Settings defaults (`settings.ts`, overridden by Settings): `AI_ENABLED`, `AGENT_EXECUTION`, `ALLOW_KEY_SHARING`, `APP_NAME`, `APP_ACCENT`, `APP_SURFACE`, `APP_ICON`, `APP_ICON_DARK`, `APP_FAVICON`, `THEME`, `BANNER_TEXT`, `BANNER_COLOR`, `BANNER_WHEN_EMBEDDED`, `KB_LABEL`, `KB_MISSION`, `KB_STARTER_DIRS`, `CHAT_MODEL_WINDOWS`, `CHAT_TEMPLATE_KWARGS`, `CHAT_SHARED_HISTORY`, `DELEGATION_ENABLED`, `DELEGATION_MAX_AGENTS`, `DELEGATION_MAX_DEPTH`, `MCP_ENABLED`, `HPC_STATUS_URL`, `HPC_STATUS_WORKFLOW`, `HISTORY_INTERVAL_SEC`, `HISTORY_KEEP`, `SWEEP_INTERVAL_SEC`, `ADE_VISION_MODEL`, `RAG_PROXY_ENABLED`, `RAG_PROXY_ALLOW_DEPLOYMENT_KEY`, `RAG_DEFAULT_MODEL`, `RAG_TOP_K`, `RAG_ADVERTISE_AGENT_MODEL`, `RAG_ADVERTISE_RAG_MODEL`, `RAG_ENDPOINT_AUTOSTART`, `RAG_ENDPOINT_NAME`, `REQUIRE_PERSONAL_KEY`, `SERVE_WORKFLOWS`, `STUDIO_WORKFLOWS`, `STUDIO_SECTIONS`, `STUDIO_SEARCH_ENGINE`, `STUDIO_GUFI_LOG`, `STUDIO_MAX_TOOL_ROUNDS`, `SUGGESTED_PROMPTS`, `VOICE_ENABLED`, `VOICE_URL`, `AGENT_LOCAL_SITE`.
- Tuning: `STUDIO_LIBRARIES`, `STUDIO_EXTERNAL_DB_PATTERN`, `STUDIO_REMOTE_MAP_FILES`, `STUDIO_REMOTE_SESSIONS`, `STUDIO_REMOTE_THREADS`, `STUDIO_SSH_PERSIST`, `STUDIO_SSH_SHARE`, `STUDIO_SEARCH_DEADLINE_MS`, `STUDIO_STACK_LOCAL`, `STUDIO_STACK_REMOTE`, `STUDIO_APPROVAL_TTL_MS`, `STUDIO_CAMPAIGN_POLL_MS`, `STUDIO_SESSION_POLL_MS`, `STUDIO_FLEET_TICK_MS`, `STUDIO_RUN_POLL_MS`, `STUDIO_WORKSPACE_RETRY_MS`, `STUDIO_SELF_URL`.
- Set by the server for its own children: `STUDIO_GUFI_VT_URL`, `STUDIO_GUFI_VT_SECRET`, `STUDIO_GUFI_VT_TIMEOUT_MS`, `STUDIO_TARGET_PORT`, `STUDIO_INJECT_KEY`.

## Porting order

By risk and dependency, each step behind the proxy and checked against the same tests:

| Step | What | Why here |
|---|---|---|
| 0 (done) | front server, `/healthz`, static files, proxy; the semantic map binary behind `STUDIO_SEMANTIC_MAP_BIN` | no state shared with Node |
| 1 | shared foundations: config, settings loading with mtime reread on both sides, the error body, auth (JWT/JWKS, mission tokens), key cookies, a GUFI exec helper (`gufi_query`, `gufi_sqlite3`, environment as `gufiEnvFor`) | every `/api` route needs them |
| 2 | read-only routes over local files: version, help, changelog, whoami, brand icon, favicon, manifest, `/api/kb/model`, `/api/index/log`, saved queries, extensions | small, no background state |
| 3 | the semantic map module (9 routes, scheduler, remote fetch) with the Go map in process; topology; library details | the map is already in Go; makes Python optional for maps |
| 4 | the indexer's Python steps: `embed_query.py` (ONNX in Go via onnxruntime bindings, or a pure Go MiniLM), then `embed.py` and `enrich.py` (text extraction; the hardest, since it calls pypdf, xlrd, olefile, tesseract and LibreOffice) | removes the bundled Python (196 MB of the 687 MB Linux tree) |
| 5 | KB file routes (tree, file, raw, download, pdf, office preview, move, copy, rename, delete, upload, tags) with the index jobs and sweep, which own `INDEX_DIR` | one state owner: the index lock and job queue |
| 6 | libraries, library builds, user libraries over ssh and `pw ssh` (`remoteExec.ts` pool, ControlMaster sockets), originals, query, search, `gufi_vt` and its loopback callback | the largest dependency surface |
| 7 | platform and workflow routes (access, sessions, session relay, workflows, runs, agent sites, tasks, fleet, RAG endpoint, updates) | pw CLI and platform API; updates must keep exit 76 |
| 8 | chat: conversations, attachments, settings writes, `/api/chat/stream`, `/v1/*`, `/api/mcp` | the agent loop uses every module above; last, then Node is removed |

`preextract.js` (downmark, a Node package with a native binary) is an index-job child, not a route. `studio-extract` replaces it and the extraction step of `enrich.py` (see Document extraction in Go); switching the index job to it is part of step 4 or 5.

## Stage 1

Stage 1 adds the foundations every ported `/api` route needs and moves no route of the inventory except `/api/whoami`, in front-auth mode. With the Go server not in use nothing changes for a user; with it in use the API answers as Node's does, apart from the differences listed below and the new sign-in routes.

*Settings:* Node's `loadSettings()` now rereads `settings.json` when its size, mtime or inode changes, and every write (`updateSettings`, `PUT /api/settings`) goes to a temporary file renamed over the old one, so a reader never sees half a file; a file that does not parse keeps the last good read. `internal/settings` reads the file the same way and computes `effectiveSettings()` with Node's defaults, environment fallbacks and key order. So a Go writer and Node's reader, or the reverse, stay in step.

*Error body:* `internal/apierr` writes `{error}` with a `KbError`'s status, 400 for validation, and 500 `internal error (ref <8 base36 characters>)` with the detail in the log under that reference.

*Identity:* In front-auth mode (`STUDIO_FRONT_AUTH`, on by default when the Go server starts Node) the Go server runs auth.ts's check on every request: the session proxy's JWT in `AUTH_HEADER` against `AUTH_JWKS_URL` (RS, PS, ES and EdDSA; never `none` or HMAC; the key set kept 10 minutes and fetched again for an unknown `kid` at most every 30 seconds, as jose does), then the OIDC session cookie. It answers `/api/auth/login`, `/api/auth/callback` and `/api/auth/logout` (OIDC, ported from the `oidc-signin` branch with its variables) and `/api/whoami`, and refuses what `AUTH_REQUIRED` or `OIDC_REQUIRED` requires before Node sees it. The verified user goes to Node in `x-studio-identity`: a JWT the Go server signs with an Ed25519 key made at start (issuer `studio-go-front`, audience `studio-node`, 5 minutes, claims `sub`, `preferred_username`, `name`, `studio_source`). Node is started with `AUTH_JWKS_URL` pointing at a loopback key set the Go server serves (`STUDIO_IDENTITY_ADDR`), `AUTH_HEADER=x-studio-identity`, that issuer and audience, `AUTH_REQUIRED` as configured, and no `OIDC_*`. Node's `auth.ts` is unchanged and its tests pass as before; it trusts only the Go server's key, and the Go server drops any `x-studio-identity` a client sends. A bearer token on `/api/mcp` goes on to Node unchecked, since only Node's task board knows mission tokens. With `STUDIO_NODE_URL` (a Node the Go server did not start) front-auth is off and identity headers pass through, as before.

*OIDC sessions:* kept across restarts in `INDEX_BASE/oidc-sessions.enc` (AES-256-GCM, additional data `oidc-sessions`) under a 32-byte key in `INDEX_BASE/.oidc-session-key`, written 0600 and never logged. Sign-out ends the Studio's session, clears the cookie and revokes the provider's token; it ends the provider's session too only with `OIDC_END_SESSION=1`. The branch kept sessions in memory and ended the provider's session by default; its tests were adapted to both.

*Key cookies:* `internal/keycookie` opens and seals `ade_mk` and `ade_mkp` (3,800-character chunks) in `credentials.ts`'s format under `INDEX_BASE/.credentials-secret`; a test opens a cookie Node sealed and Node opens one Go sealed.

*GUFI:* `internal/gufi` ports `onePassStages` and the search parsing from `gufi.ts`, the canned, builder and raw SQL plans, `shellQuote` and the command line from `query.ts` and `gufiTrace.ts`, the output parsing and `fuseRows`, and `gufiEnvFor`. GUFI stays a helper process. An `Executor` runs `gufi_query` or `gufi_sqlite3`; `LocalExecutor` is the one in place, and a cluster's can follow at step 6. A run is killed by its PID when its request ends or its timeout passes, and keeps the last 16 KiB of its error output. The `Runner` admits at most `STUDIO_GUFI_CONCURRENCY` runs (default 2), queues the rest in arrival order up to `STUDIO_GUFI_QUEUE` (256) and refuses past it with a 503, times a run out after `STUDIO_GUFI_TIMEOUT_MS` (60,000), lowers `-n` to `STUDIO_GUFI_THREADS` (cores up to 16), and logs each run's queue depth, wait, run time, exit and output size; `Stats()` keeps the totals. No route uses it yet.

Differences from Node in front-auth mode:

- `/api/whoami` comes from the Go server, in the `oidc-signin` branch's shape (with `signIn`); Node's `req.user.claims` hold only the hand-off's claims, which no Node route other than whoami reads.
- Page loads are sent to sign-in only where OIDC is configured and required; `/v1/` and `/internal/` are not, since Node checks them itself. The branch gated them too.
- A request to `/api/mcp` with a bearer token that is not a mission token, where sign-in is required, gets Node's `{error: 'authentication required'}`; the branch answered `sign-in required` with the sign-in URL.
- `OIDC_OPERATOR_GROUP` is not implemented: the Studio has no operator or owner role to map a group onto (see Open questions).

Tests:

| What | Result |
|---|---|
| Go tests (`go test ./...`, 10 packages) | 78 pass with Node and GUFI on the host; without Node, 74 pass and the 4 that run it skip |
| Golden comparisons with Node | all equal: 8 settings cases; 85 GUFI cases (search parsing, `onePassStages`, passages, fusion, sizes, dates, 28 query plans with their command lines); 5 walks and 10 query plans run on a built index; key cookies sealed by each and opened by the other |
| Node suite direct (`node --test`, concurrency 6) | 850 tests: 830 pass, 0 fail, 20 skipped; 89 s |
| Node suite through the Go server (`npm run test:front`, concurrency 6) | 850 tests: 830 pass, 0 fail, the same 20 skipped; 90 s; 1,751 requests from 54 files went through the Go server |

The golden files are written by `server-go/scripts/golden/*.mjs` from `server/dist`; each package's `TestGoldenIsCurrent` (with `STUDIO_GOLDEN_NODE=<node>`) fails when Node's output has moved from the committed file. The GUFI fixture test indexes `internal/gufi/testdata/tree` (8 files) with `gufi_dir2index`, adds a words table as `enrich.py` does, and runs 5 walks and 10 query plans through Go and through Node's `walkSearch` and `queryScope`; it skips where GUFI is not installed.

Runner overhead: on the 200,000-file test index a canned query took 198.5 and 200.9 ms run directly and 197.0 and 198.6 ms through the runner (10 runs each, two rounds), and on the fixture index 10.5 ms either way; the difference is inside run-to-run spread. Admission and release alone take about 0.1 µs uncontended and 0.6 µs with four callers on two slots.

Routes that could move to Go next at low risk, all read-only over files Go already reads or with no state of their own: `GET /api/version`, `/api/help`, `/api/changelog`, `/api/brand-icon`, `/api/favicon`, `/manifest.webmanifest`, `/api/kb/model`, `/api/index/log`, and the saved queries (`GET` and `POST /api/query/saved`, `DELETE /api/query/saved/:id`; one JSON file Node reads on every request). `POST /api/query/sql` needs only the plans above for the main library but the library list for the rest, so it waits for step 6. `/api/config` reads most of the server's state and is better left to the end of step 2.

## The Node bridge (transitional)

While Go fronts Node, `internal/bridge` is the one channel between them. A ported module that needs state Node still owns, or that Node needs from Go, calls the other side with the bridge secret in `X-Studio-Bridge-Secret` (`bridge.Sign`, `bridge.Authorized`). The Go server makes the secret at start, or takes `STUDIO_BRIDGE_SECRET` (the test harness sets it), and passes it with `STUDIO_FRONT_URL` to the Node it spawns. Modules add the variables that tell a spawned Node which work Go has taken over with `bridge.RegisterNodeEnv`, and mark their bridge paths with `bridge.RegisterPrivatePrefix`; the server answers 404 on those paths to any request without the secret, before sign-in. Nothing here is used in Node-free mode, and the package is removed with Node.

## The platform track

`internal/platform` is the shared layer for the platform routes: the pw CLI (where it is found, `Exec` and `ExecFile` with Node's timeouts, buffer limits and error messages), the CLI's contexts and the `access.json` choice, token renewal (`auth token --print -o json --context <name>`, one at a time per context), the device sign-in (`pw auth login --device`), the platform API client and its reachability probe, and the GitHub client. `internal/jsonv` keeps JavaScript's object key order, `JSON.stringify`'s number and string formatting, and the npm `yaml` package's core schema, so answers built in Go are byte for byte Node's. `internal/platformtrack` registers the track's packages, one line each.

While Node runs behind Go, the server passes `STUDIO_GO_PLATFORM=1` to the Node it starts. Node then leaves the run watcher, the fleet ticks, task rehydration and the RAG endpoint's auto-start to Go, sends a run it records to Go (`/internal/platform/runs`), rereads `runs.jsonl` when its stamp changes, and asks Go's task board (`/internal/platform/board`) about mission tokens on `/api/mcp`. Go asks Node, over `/api/__bridge/`, who the viewer is and which personal key they hold, and tells it when the access choice changes, when provider probes are stale, and when a conversation note is due. Both prefixes are private to the bridge. With `STUDIO_NODE_URL` (a Node the Go server did not start, as in the test harness) Node keeps that background work unless `STUDIO_PLATFORM_JOBS=1`.

The RAG endpoint runs `pw endpoints run ... -- studio-server rag-forward`: the forwarder that was `node -e` is now a subcommand of the Go binary, with the same `STUDIO_TARGET_PORT` and `STUDIO_INJECT_KEY`. `STUDIO_GITHUB_API_URL` and `STUDIO_GITHUB_RAW_URL` point the GitHub client at a local fake in tests.

## Subcommands

`studio-server <name> args...` runs the binary as a helper in place of the server. Work that loads native code or can crash, such as extraction (`extract`) and the ONNX embedder (`embed`, `embed-query`), runs this way as a child of the server, so a failure there cannot take the server down. Each subcommand registers itself from its own file in `cmd/studio-server` (`sub_<name>.go`, an `init` calling `registerSubcommand`); `studio-server help-subcommands` lists them.

## Tests against a server URL

`server/test` has 121 files on `node:test`, run against `server/dist` with `test/setup.mjs` preloaded (it blocks non-loopback fetches). None starts the server or reads a base URL:

| Kind | Files | Can run against another server |
|---|---|---|
| builds a Fastify app in process from `../dist` modules and calls `app.inject` | 53 | not as written: each registers only the plugins it needs, sets env before import, and 34 files reach into module state (`*ForTests` hooks, `setRemoteSpawn`, stubbed `globalThis.fetch`) |
| the same, plus a real listener for the `gufi_vt` callback | 5 | no, for the same reasons |
| calls module functions directly, no Studio HTTP | 63 | no; these test Node internals and are ported as Go unit tests with each module |

What was converted, because it was cheap:

- `testdata/server.test.mjs` boots the built server against a temporary KB and talks HTTP. With `STUDIO_TEST_FRONT=<binary>` it also starts that binary in front of Node (`STUDIO_NODE_URL`) and sends every request through it. Both of its tests pass through the Go server.
- `server/test/semanticMap.test.mjs` runs the map with `STUDIO_SEMANTIC_MAP_BIN` when set; all 10 tests pass with Python and with the Go binary. Its tests of the Python module's own functions (`py()`) stay Python; the Go package has the same cases in `testdata/python_golden.json`.

`npm run test:front` in `server/` (since Stage 1) runs the whole suite through the Go server without changing a test. `test/front.mjs`, preloaded, points `fastify` at `test/frontFastify.mjs` for the tests and `server/dist`: the same Fastify, whose `app.inject` makes the app listen on a loopback port, starts `STUDIO_TEST_FRONT` in front of it (`STUDIO_NODE_URL`, `STUDIO_FRONT_AUTH=0`, a port from 5870 to 5899 or the range in `STUDIO_TEST_FRONT_PORTS`, such as `5900-5919`, no web app), and sends the request over HTTP through it. The test's process, module state and stubs are untouched, which is what kept the 58 `app.inject` files from running against another server. `STUDIO_TEST_FRONT_COUNT=<file>` records how many requests each file sent through the Go server. What it does not cover: the 63 files that call module functions, and front-auth mode, which `internal/server`'s `TestNodeTrustsTheFrontOnly` checks against the real Node server.

A URL switch alone (`STUDIO_TEST_URL`) does not fit these tests: each needs a server started on its own fixture directory. The workable form is the one above: a helper that starts the built server with the test's environment and, with `STUDIO_TEST_FRONT`, the Go server in front. Converting an `app.inject` file means moving its fixtures into that environment and dropping its module hooks; that is worth doing for each module as it is ported, starting with the routes of step 2.

## The semantic map in Go

`cmd/semantic-map` takes the Python script's arguments (`--index`, or `--vectors` with `--model` and `--of`; `--out`, `--sample`, `--fit`) and writes the same `overview.json` (version 3) and `cluster-<c>.json` into a scratch folder swapped in whole, and prints the same summary line. It reads `gvec_rowids`, `gvec_vector_chunks00`, the validity bits in `gvec_chunks`, `gvecmodel` and `gchunks` as plain tables through `modernc.org/sqlite`, read only. Term extraction, whole-word cuts, TF-IDF naming, folder labels, the two links per cluster and the sample quotas follow the Python line by line. Differences: the PCA takes the eigenvectors of the scatter matrix of the fitted rows in place of numpy's SVD (the same axes up to sign; Go fixes each sign so the largest coordinate is positive), the random generator is Go's PCG with seed 0 in place of numpy's, the means and terms are kept in scratch files as the Python does, and it never exits 3, since it has no numpy to miss. The server uses it when `STUDIO_SEMANTIC_MAP_BIN` names it; the default is still Python.

Equivalent means: the same number of files and clusters, the same schema, the sample rule kept (at most `--sample` files, at least 25 or all of each cluster), map axes correlated with Python's (absolute correlation, since the sign of a component is arbitrary), and cluster assignments, names and links that agree with Python's as closely as two Python runs that differ only in their random seed agree with each other. Where k-means is stable that is an exact match.

`scripts/compare_semantic_map.py` builds synthetic indexes the way the Studio stores vectors (with invalid slots) and a `vectors.jsonl` the way `remoteMap.ts` writes it, runs both programs, and measures that, including the adjusted Rand index (ARI) between Python seeds 0 and 1:

| Case | Files | Clusters (Py, Go) | ARI Go to Py | ARI Py seed 0 to 1 | Names equal | Links equal | Axes abs. corr. | Time Py, Go |
|---|---|---|---|---|---|---|---|---|
| 10 separated topics | 200 | 10, 10 | 1.0 | 1.0 | 10/10 | 13/13 | 1.0, 1.0, 1.0 | 0.20 s, 0.12 s |
| 18 subtopics, k = 14 | 2,400 | 14, 14 | 0.607 | 0.607 | 6/14 | 12/20 | 1.0, 1.0, 1.0 | 0.58 s, 0.45 s |
| remote int8 vectors, 14 topics | 5,600 | 14, 14 | 0.908 | 0.908 | 11/14 | 16/20 | 1.0, 1.0, 1.0 | 2.96 s, 0.43 s |
| 14 topics, past the 20,000-file fit | 49,994 | 14, 14 | 1.0 | 0.908 | 14/14 | 18/18 | 0.998, 0.998, 0.950 | 10.5 s, 4.5 s |

Schema and sample rule held in every case. Where k-means has more than one good answer (the second and third rows) Go lands within Python's own seed-to-seed spread; on the largest case Go matched Python's seed 0 exactly while Python's seed 1 did not. The third axis correlates less on the largest case because both fit the PCA on a different random 20,000 of the 50,000 files and the 14 topics give near-equal eigenvalues. On that case reading took 3.2 s of Go's 4.5 s and 6.0 s of Python's 10.3 s. Times include process start (Python's import of numpy). Go now reads `vectors.jsonl` on every core (see Performance).

In the Node test suite `semanticMap.test.mjs` passes 10 of 10 against the binary, and a Node process with `STUDIO_SEMANTIC_MAP_BIN` set and `PYTHON_BIN` pointing nowhere built the main folder's map through `scheduleSemanticMapRebuild`.

To repeat the comparison (numpy from `indexer/.deps`, which `indexer/fetch_embed.py --deps` installs):

```
cd server-go && go build -o bin/semantic-map ./cmd/semantic-map
PYTHONPATH=../indexer/.deps python3 scripts/compare_semantic_map.py --go bin/semantic-map --work /tmp/map-compare
```

## Excel 97-2003 text in Go

`internal/extract/xls` is the Go side of `extract_xls` for step 4; nothing calls it yet. `Text(r io.ReaderAt, size int64, maxText int) (string, error)` gives the same layout (a `Sheet:` line, rows as tab-separated cells, numbers, ISO dates, TRUE and FALSE) with the same `MAX_TEXT` cut. It is its own BIFF reader over `github.com/richardlehane/mscfb` (the OLE container; pure Go), plus `golang.org/x/text` for BIFF5 codepages. `github.com/shakinm/xlsReader` was set aside: it printed dates as serials and dropped every FORMULA cell, and fixing both meant reading the records ourselves anyway.

It reads BIFF8 and BIFF5: RK, MULRK, NUMBER, LABEL, LABELSST, RSTRING, BOOLERR, SST and STRING across CONTINUE records, the cached results of FORMULA cells (shared and array formulas included), dates by XF and FORMAT with xlrd's rules, the 1904 system and serial 60. It does not read BIFF2 to BIFF4 (xlrd does) or encrypted workbooks (xlrd does not either). One deliberate difference: a pure time (0 < serial < 1) prints as `HH:MM:SS`, where xlrd prefixes the epoch day.

Checked cell by cell against xlrd and LibreOffice on 441 workbooks (LibreOffice, xlwt, synthetic, and the Excel-written files in xlrd's and Apache POI's test data): every cell equals xlrd's apart from pure times. The run and its results are in `internal/extract/xls/testdata/README.md`.

## Document extraction in Go

`internal/extract` does what `preextract.js` and the extraction step of `enrich.py` do today, in one pass; `cmd/studio-extract` runs it with the flags of both (`--kb-root`, `--extract-cache`, `--subdir`, `--no-recurse`, `--jobs`, `--progress`, `--no-ocr`, `--no-documents`, `--pdf-reader`, `--soffice`, plus `--extractors` and `--report`), prints the same `@progress` lines and a summary line, and is not called by the launchers or the Node server yet. Built stripped for linux/amd64 it is 11.9 MB, with no cgo.

In the binary: downmark 0.10.0 (`github.com/giraffesyo/downmark`, the version the npm package pins, so the provenance line and the cache keys stay the same) for pdf, docx, pptx, xlsx and doc; the Go readers for xls, odt, ods, odp and ppt (`internal/extract/xls`, `odf`, `ppt`, in place of xlrd and olefile); text files with Python's decoding (`internal/extract/pytext`); the flat OOXML readers `enrich.py` uses after LibreOffice; and image captions through the gateway. Helpers, each found on `PATH` and each optional: tesseract (all OCR), pdftoppm and pdfinfo (the thin-PDF recheck), pdftotext (a PDF downmark cannot open), and LibreOffice (legacy formats and the last fallback). pypdf, `enrich.py`'s last resort for a PDF without poppler, has no counterpart.

Kept as today: the entry format and freshness rule, the 64 MB input cap, the thin-PDF policy (fewer than 50 glyphs a page; 30 pages, 120 s a page, 300 s a document in downmark; `enrich.py`'s recheck under 14,400 bytes at 120 characters a page), the helper timeouts, image rules (8 KB floor outside `uploads/chat`, failed captions not cached) and `OMP_THREAD_LIMIT=1` for every tesseract. `--jobs` bounds the workers, and one semaphore of the same size bounds OCR across them. Two differences: dot folders other than the skipped ones are walked by one pass where today `preextract` leaves them to `enrich.py`, and `enrich.py --jobs 1` does not set `OMP_THREAD_LIMIT` (only its pool workers do), which the Go pass does at every job count.

The external extractor hook (none exists today) is a JSON file, `{"extractors": [{"name", "suffixes", "command", "timeoutSeconds"}]}`: the command gets the file's path (`{path}` or appended, no shell), writes text on stdout, and is killed with its process group after the timeout (120 s, at most 3600 s). Text suffixes cannot be taken. The contract is in `external.go`.

downmark's PDF page workers are goroutines without `recover`, so a panic there ends the process. The pass should run as a child process (`studio-extract`, as `preextract.js` is today) when the server moves to Go.

Conformance, today against Go on the same files, cold, comparing cache entries byte for byte: the 275-file assessment corpus (15 formats) gives 275 of 275 equal with OCR and 235 of 235 without; `enrich.py` run over the Go cache afterwards rewrites nothing. On the Apache POI, ODF Toolkit, pdf.js and Tika test suites (3,522 files), 2,957 of the 2,973 files both sides cache are equal, and 3 files are cached by one side only. The 19: 5 damaged xls on which xlrd raises (today's LibreOffice route then indexes SST indices as words, or gets nothing; Go reads the cells, which is right), 5 with pure times (deliberate), 3 BIFF3 and BIFF4 workbooks Go does not read (today is right; LibreOffice covers them), 2 crafted ODF zips whose central directory and local headers disagree (Python and Go pick different members; LibreOffice opens neither), 1 pdf.js file whose pdftoppm output differs from run to run, and 3 fuzzed .ppt files: one olefile cannot open (Go reads it), two whose directories mscfb rejects and olefile accepts (today is right). Fixtures from POI ship in `ppt/testdata` with a NOTICE; the larger suites stay outside the repository.

Timings on the assessment corpus (shared 20-core host, load average 4.6 to 10.7 during the runs; CPU includes helpers; OCR is tesseract plus pdftoppm CPU; peak RSS is the extractor's own processes, helpers excluded):

| Run | Today wall / CPU / OCR | Go wall / CPU / OCR | Peak RSS today, Go |
|---|---|---|---|
| all, cold, 8 jobs | 68.3 s / 245.7 / 230.5 | 35.5 s / 267.7 / 254.6 | 276, 205 MB |
| all, cold, 1 job | 329.5 s / 523.9 / 510.9 | 233.4 s / 235.6 / 224.3 | 135, 58 MB |
| all, cached, 8 jobs | 0.45 s / 2.5 | 0.20 s / 1.9 | 48, 116 MB |
| all, cached, 1 job | 0.80 s / 1.9 | 0.48 s / 1.5 | 87, 39 MB |
| all, cold, no OCR, 8 jobs | 2.00 s / 12.9 | 1.50 s / 11.5 | 219, 176 MB |
| outline PDFs (5), cold, 1 job | 274.3 s / 459.9 / 454.4 | 177.5 s / 178.6 / 173.2 | 86, 58 MB |
| outline PDFs (5), cold, 8 jobs | 61.1 s / 186.5 / 180.4 | 28.3 s / 206.3 / 200.0 | 100, 121 MB |
| scanned PDFs, cold, 8 jobs | 9.07 s / 45.7 / 45.0 | 7.91 s / 44.4 / 43.9 | |

Without OCR every format takes under 0.5 s wall either way; Go's non-OCR CPU is lower for every format but ods: on the ten generated spreadsheets (26 MB of content.xml) the ODF reader takes 1.15 s of CPU against 0.56 s for `extract_odf`, because `encoding/xml` is about half expat's speed and the reader parses content.xml twice (a well-formedness pass, then the walk). Folding the two passes into one would bring it to about parity. OCR is 94 to 97 percent of cold CPU, so the wall gain at 8 jobs comes from overlapping the two stages today runs one after the other, and at 1 job from tesseract running on one thread. On the real-world suites at 8 jobs (an earlier build) today took 783 s and Go 684 s, both bounded by two pdf.js tiling-pattern files on which pdftoppm reaches its 600 s timeout.

Rendering PDF pages for OCR without poppler: a prototype over `github.com/pdfcpu/pdfcpu` (Apache 2.0) and `golang.org/x/image/vector` fills paths (text drawn as outlines) and paints DCT and 8-bit Flate images (scans), in grey; it ignores text, strokes, clipping, shadings and patterns. That covers the pages the OCR path is for. Its tesseract recall on the five outline PDFs equals pdftoppm's within 0.0001 (0.9995 of the words), it rendered them in 5.7 s against pdftoppm's 41.1 s, and on scans the words agree with pdftoppm's to 0.9988. Over the pdf.js suite (1,088 files, 3 pages each) it rendered 1,053, could not read 29 and panicked on 6. `seehuhn.de/go/pdf` renders more but is GPL-3. The recommendation is to keep pdftoppm where installed and add the Go renderer as the fallback where poppler is missing (the macOS app), after it gains `recover`, strokes, clipping and the other image filters (CCITT, JBIG2, JPX); poppler then becomes optional.

```
cd server-go
go build -o bin/studio-extract ./cmd/studio-extract
OMP_THREAD_LIMIT=1 ./bin/studio-extract --kb-root ~/kb --extract-cache ~/.cache/studio/extract --jobs 8 --progress
ENRICH_PY=../indexer/enrich.py ENRICH_DEPS=../indexer/.deps go test ./internal/extract/...   # compares with Python
```

## Performance

Measured on a shared 20-core Linux host (load average 3 to 12 during the runs, recorded with each run) with the harness kept outside the repository; medians of 3 rounds, 10 runs for startup. "Before" is 3065f7d.

| What | Before | After |
|---|---|---|
| Start to first `/healthz`, warm (Node alone 305 ms) | 425 ms | 311 ms |
| Start to first `/healthz`, cold (Node alone 351 ms) | 433 ms | 362 ms |
| Go CPU per proxied request, c = 20 (`/api/whoami`, `/api/libraries`, `/api/config`) | 300, 202, 169 µs | 93, 78, 94 µs |
| Requests/s through Go, c = 20 (same routes) | 19,444, 22,540, 8,968 | 36,004, 25,883, 10,550 |
| Proxy overhead p50 / p99 at c = 20, front minus Node behind (`/api/whoami`, `/api/config`; 7f69fad before) | 0.27 / 1.68, 0.24 / 1.40 ms | 0.18 / 0.69, 0.15 / 1.04 ms |
| Entry script (2.78 MB), bytes sent with br accepted | 2,782,821 | 789,009 (gzip 907,980) |
| Entry script, c = 50, requests/s and p50 (Node alone 1,075, 46 ms) | 7,132, 6.2 ms | 15,260, 2.9 ms |
| Style sheet (282 KB), bytes sent with br accepted | 281,751 | 45,807 |
| Semantic map from `vectors.jsonl`, 1M files: whole build, reading, peak RSS | 24.0 s, 17.1 s, 372 MB | 9.2 s, 2.6 s, 427 MB |
| Same, 200,000 files | 5.7 s, 3.6 s, 182 MB | 2.6 s, 0.5 s, 262 MB |

Event streams are unchanged by the flushing change (time to first event and the gaps between events through Go match Node's within run-to-run spread). At concurrency 1 the proxy numbers moved by up to 2x between rounds for both builds, in step with Node's own CPU per request, so no change there can be claimed. The first request for the entry script after a start waits about 90 ms for its compression when it arrives before the background pass has done it. The map's output at 1M files is the same as 3065f7d's, byte for byte apart from the build times; what remains of its time is the clustering after the read, still on one core.

## Open questions

- Operator gating with OIDC (`OIDC_OPERATOR_GROUP`): the Studio has no operator or owner role in shared mode to map a group onto; deployment-level controls are closed to everyone once sign-in is on. Whether to add such a role, and what it opens, is a decision before a group can gate it.
- Sign-in with a Node server the Go server did not start (`STUDIO_NODE_URL`): front-auth is off there, so OIDC is not offered. Turning it on needs that Node started with the `AUTH_*` values the Go server logs.
- Retention of persisted OIDC sessions: they last `OIDC_SESSION_MAX_AGE_SEC`, or as long as refresh succeeds with `offline_access`, and are pruned every ten minutes; deleting `INDEX_BASE/.oidc-session-key` ends them all. Whether to offer a setting that ends them at restart, as the branch's in-memory sessions did, is open.
- `/v1/` and `/internal/` are left to Node's own checks under required sign-in, where the `oidc-signin` branch redirected them to sign-in.
- Where the Go server lives in the packaged apps: the desktop supervisor and `deploy/` start `node server/dist/main.js` today. Switching them to `studio-server` with `STUDIO_NODE_SPAWN=1` is one change per launcher.
- The ONNX embedder in Go (step 4): onnxruntime through cgo, or a pure Go MiniLM. cgo makes cross-compiling harder; pure Go is slower.
- Document extraction (step 4): whether the Go pass needs pypdf's role (PDF text without poppler) or the Go renderer covers it; where the external extractor file lives in Settings; whether `enrich.py --jobs 1` should set `OMP_THREAD_LIMIT` as its pool workers do; and whether a Windows cache written by Python (which writes \r\n) and one written by Go (\n) should be treated alike, not yet compared.
- `semantic_map.py` labels the map with the model most files use but maps the files of every model; the Go port does the same. Whether files of other models should be left out, as the docstring says, is a decision for the Python and Go code together.
