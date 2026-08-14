# Setting up your studio

Every deployment-specific choice lives in environment variables, conventionally in a gitignored `.env` at the repo root. Copy `.env.example` to `.env`, adjust, and restart. Nothing brand- or site-specific belongs in the code.

## Point it at your corpus

`KB_ROOT` is the directory the studio works over; `KB_LABEL` is the human name shown at the top of the library tree and in the help page. Build the index once with `indexer/setup_gufi.sh` (toolchain) and `KB_ROOT=... indexer/reindex.sh` (index, extraction, embeddings). After that the background sync keeps the index current; `SWEEP_INTERVAL_SEC` sets the cadence.

## Name and brand

`APP_NAME` is the product name in the header and the browser tab. `APP_ICON` points at an image file (PNG or SVG works) shown beside it; without one, a letter badge from the first character of the name appears instead. `APP_FAVICON` points at an image used as the browser tab icon; it falls back to `APP_ICON`, and with neither set the tab shows the generic bundled favicon. `THEME` picks the default of light or dark; every user can toggle from the sidebar footer, and their choice persists in the browser.

## Chat

The assistant talks to any OpenAI-compatible endpoint: set `OPENAI_BASE_URL` and `OPENAI_API_KEY`. On the ACTIVATE platform with an authenticated pw CLI, leave both unset and the platform gateway is used with no configuration. `SUGGESTED_PROMPTS` (a JSON array) fills the starter cards on the empty chat; write prompts that fit your corpus. `ADE_VISION_MODEL` names a vision-capable model and turns on image captioning during indexing, which makes images findable by what they depict.

The chat greeting uses `APP_USER_NAME` in standalone mode. Behind the platform, a verified JWT identity takes precedence (below).

## Tools, skills, and agent files

The assistant's tool set is managed from the Settings page and from files.

- Built-in tools live in the server (`server/src/chat/tools.ts`). Settings lists each one with an on/off toggle; clicking a tool's name shows its exact call specification, which you can copy as a starting point for your own.
- Custom tools defined in Settings are saved with the runtime settings (`index/settings.json`). Each is a name, a description for the model, and a command; the command runs on the Studio server and its output returns to the conversation.
- File-based extensions load from `index/extensions/` and take effect without a restart. On first start, when that directory does not exist yet, the server seeds it from the repository's `extensions-starter/` set (two example tools, three skills, and an inactive persona template), so a fresh deployment has working examples to copy; delete or edit them freely, they are never re-created once the directory exists:
  - `tools/*.json`, each `{ "name", "description", "command" }`, behave like custom tools.
  - `skills/*.md` are instruction sets the assistant loads on demand through the `use_skill` tool, either when the user names one or when the task matches the skill's frontmatter description.
  - `agents/default.md` is appended to the system prompt on every request, as the deployment's standing instructions.

Commands run with the server's user and environment. Treat tool and skill files as configuration with shell access, and keep the extensions directory outside version control (the default location is beside the index, which is already ignored).

## Help content

The in-app Help page renders `docs/HELP.md`, split into cards at `##` headings, with `{appName}` and `{kbLabel}` substituted. Point `HELP_FILE` at your own markdown to replace it entirely.

## Platform identity (optional)

When served behind the ACTIVATE platform, the session proxy forwards a short-lived (5 minute) RS256 JWT in the `X-PW-User-Token` header: `sub` is `user:<username>`, `iss` is `pw-session-proxy`, and `aud` carries `session:<sessionId>` and `session:<owner>/<sessionName>`. It identifies the user and proves the platform forwarded the request; it carries no credentials. Set `AUTH_JWKS_URL` to the platform's keys endpoint (`https://<platform-host>/api/platform/keys`, the `jwks_uri` from its OIDC discovery document) to verify it; `AUTH_HEADER` names the header (default `x-pw-user-token`), and `AUTH_ISSUER`/`AUTH_AUDIENCE` add the standard claim checks, with `session:<owner>/<sessionName>` the natural audience to pin. With `AUTH_REQUIRED=1`, API requests without a valid token are rejected; without it, verification is best-effort and the verified identity simply personalizes the app. Role-based permissions (who can manage the library versus only chat) are a planned layer on top of this identity.

## Per-user model keys (shared ACTIVATE sessions)

With identity verification enabled, each user can add their own model API key under Settings, "Model access". Their chat model calls, model listing, health checks, and platform CLI tool executions then run on their key instead of the deployment credential; users without a personal key continue on the deployment credential. This surface only appears when `AUTH_JWKS_URL` is configured; a standalone single-user deployment never shows it and never creates the credential files.

Storage and trust, stated plainly: keys are held server-side (browser storage is unreliable inside the platform iframe and the calls are made by the server anyway), either encrypted at rest in `<index>/user-credentials.json` with a locally generated `<index>/.credentials-secret` (both written mode 0600, both outside the repository), or, with "remember" unchecked, in server memory only with a 12 hour expiry. Encryption at rest protects the files, not the running process: a key added here is necessarily available to the server and its operator, so users should prefer a key they can revoke. Key material never goes to the browser (status APIs return only the last four characters), never appears in logs, and never appears in error messages. The platform's delegated-credential flow, when available, replaces this mechanism without changing any call sites.

## Labels

Files and directory subtrees can be labeled from the library (select with ctrl or cmd click, then Labels). Labels are stored as extended attributes on the files themselves and indexed by GUFI, so they survive copies and are visible to any xattr-aware tool. A label on a directory applies to everything beneath it. The chat surfaces labels on retrieved material and can filter by them; use labels for provenance distinctions such as research versus validated.

## RAG endpoint (OpenAI-compatible /v1)

The server exposes an OpenAI-compatible surface at `/v1` (`/v1/models`, `/v1/chat/completions`), so any OpenAI-speaking client can use the knowledge base as a grounded model. Two virtual model families: `studio-agent[/<gateway-model-id>]` runs the full assistant pipeline (system prompt and the retrieval tool loop) on the underlying model and returns the grounded answer; `studio-rag[/<gateway-model-id>]` injects retrieved, citation-numbered context blocks and makes a single model call; it is unlisted in `/v1/models` by default (agent clients driving it with their own tools lose the grounding) but stays callable by name; the Settings RAG section has per-model advertise options (`RAG_ADVERTISE_RAG_MODEL=1` lists studio-rag, `RAG_ADVERTISE_AGENT_MODEL=0` hides studio-agent), and `?all=1` always lists both. Advertise studio-agent where users drive tool-calling harnesses, studio-rag where a plain chat interface wants fast grounded answers. Any other model id behaves like studio-rag with that model. The bare forms use the default model set on the Settings RAG endpoint section.

Callers authenticate with their own gateway API key as the bearer token; the endpoint holds no credentials and simply forwards the caller's key, so per-user credential state (including keys that need periodic unlocking) passes through. The "allow deployment credential" setting opts callers without a key onto the deployment's own credential. Per-request headers: `X-RAG-Top-K` (1 to 20), `X-RAG-Tags` (comma-separated label filter, inheritance respected), `X-RAG-Off: 1` (plain passthrough). Retrieval fails open by default (`RAG_FAIL_OPEN=false` makes retrieval errors hard failures).

The Settings RAG endpoint section can also register the surface into the platform's chat and AI provider catalog (a managed `pw endpoints run --openai` child process around a local forwarder; requires an authenticated pw CLI on the host). The registered model appears as `session:<owner>:<name>-rag/studio-agent` and is callable through the platform gateway like any other model, which is how pw code consumes it. Platform callers reach it with their own credentials; the forwarder injects the deployment credential only for keyless clients such as pw code. Enable "register automatically at startup" to survive restarts.

## Deployment postures

The application runs in one of three postures; state the choice deliberately rather than inheriting it.

*Single user* (the default, and the right shape for on-premises or air-gapped deployments): leave `AUTH_JWKS_URL` unset. There is no identity layer, the personal-key surface never appears, the per-user credential store is never created, and the deployment credential is simply the operator's own. Point `OPENAI_BASE_URL` (or `PW_GATEWAY_URL`) at any OpenAI-compatible endpoint, including a local vLLM or llama.cpp server; indexing, embeddings (local GGUF model), and OCR are already local, so nothing requires outbound network. Disable or localize the vision-captioning model for fully disconnected operation.

*Multi-user, shared credential*: identity verification on (`AUTH_JWKS_URL`), and `DISABLE_PERSONAL_KEYS=1`. Users are identified but everyone's model calls and platform tools run on the deployment credential; the server provably collects no user keys (the flag is env-only by design, so no UI setting can re-enable collection). The risk in this posture is that all activity acts as the deployment owner.

*Multi-user, personal keys*: identity verification on, personal keys enabled (optionally `REQUIRE_PERSONAL_KEY=1` to refuse the deployment credential for chat). Each user's calls run on their own key, which is the correct posture for per-user model entitlements and keys that need periodic unlocking. This is the posture that custodies secrets: keys are encrypted at rest but visible to the server process and its operator, as described above. The platform's delegated-credential flow, when available, replaces the custody entirely.

`REQUIRE_PERSONAL_KEY` and `DISABLE_PERSONAL_KEYS` together would be contradictory; when both are set, the disable flag wins and the requirement is ignored. The ACTIVATE deployment workflow exposes these postures as a dropdown at launch.
