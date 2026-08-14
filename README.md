# ACTIVATE Studio

Web interface over a knowledge base directory, served as a session on the ACTIVATE platform. Three surfaces: a chat assistant with tool calling grounded in the corpus, a library (file tree, viewer, upload and URL ingestion), and hybrid search (full text plus semantic). The corpus root is configurable (`KB_ROOT`); deployment-specific labels and starter prompts come from the environment, not the code.

The retrieval layer is built on GUFI (per-directory SQLite index with fts5 and vec0 tables). How the whole system works, including the incremental indexing and the need-to-know model, is documented in `docs/ARCHITECTURE.md`.

## Layout

- `server/` Fastify + TypeScript: KB API, hybrid search, chat tool loop against the ACTIVATE gateway, upload and URL ingestion, incremental indexing and background sweep.
- `web/` Vite React SPA styled on the ACTIVATE design tokens: Chat (full-canvas `@parallelworks/ai-chat`), Library, Search.
- `indexer/` GUFI toolchain build, full rebuild, enrichment, embeddings.
- `deploy/` platform session serving.
- `docs/` architecture documentation.

## Running

```
pnpm install
pnpm build
node server/dist/main.js          # http://localhost:4080
```

Environment: `KB_ROOT`, `KB_LABEL`, `APP_NAME`, `APP_ICON` (path to a brand image), `HELP_FILE` (override `docs/HELP.md`), `SUGGESTED_PROMPTS` (JSON array), `APP_USER_ID`/`APP_USERNAME`/`APP_USER_NAME`, `SWEEP_INTERVAL_SEC` (default 300, 0 disables), `ADE_VISION_MODEL` (enables image captioning), `PORT`. `deploy/run_endpoint.sh` sources a gitignored `.env` in the repo root, which is where deployment-specific values belong.

## Models

The chat talks to any OpenAI-compatible backend; nothing in the app requires the pw CLI for model access.

- Default: the ACTIVATE gateway. With an authenticated pw CLI on the host, no configuration is needed at all (the server reads the CLI's credential); otherwise set `PW_API_KEY`. `PW_ALLOCATION` enables org-provider models.
- Any other backend: set `OPENAI_BASE_URL` (or `PW_GATEWAY_URL`) to the endpoint's `/v1` base and `OPENAI_API_KEY` (or `PW_API_KEY`) to its key. OpenAI, vLLM, llama.cpp server, Ollama's OpenAI-compatible endpoint, and similar all work. The endpoint must serve `/models` and streaming `/chat/completions`; tool calling is required for the assistant's knowledge base and workflow tools.
- The platform workflow tools (catalog, DAG preview, runs) come from the pw CLI and disappear gracefully when it is absent; the knowledge base tools work everywhere.

One-time setup: `indexer/setup_gufi.sh`, then `indexer/reindex.sh` for the first index build.

Serve as a platform session: `deploy/run_endpoint.sh` (registers the endpoint under the account's reserved subdomain).

## Ingestion

Files and URLs added through the Library's Add panel land in a chosen KB directory and are searchable in under a second (incremental subtree indexing). Files that arrive outside the UI (scp, generators, git) are picked up by the background sweep within one interval, or immediately via the sync now button.
