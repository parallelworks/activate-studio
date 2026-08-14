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

## Labels

Files and directory subtrees can be labeled from the library (select with ctrl or cmd click, then Labels). Labels are stored as extended attributes on the files themselves and indexed by GUFI, so they survive copies and are visible to any xattr-aware tool. A label on a directory applies to everything beneath it. The chat surfaces labels on retrieved material and can filter by them; use labels for provenance distinctions such as research versus validated.
