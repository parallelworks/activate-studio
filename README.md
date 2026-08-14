# ACTIVATE Studio

A standalone web workspace over a knowledge base directory: a chat assistant with tool calling grounded in the corpus, a library (file tree, viewer, upload and URL ingestion, original-file previews), hybrid search (full text plus semantic, including text inside office documents, PDFs, and images), and a structured query interface over the file index. It runs anywhere Node, Python, and GUFI run, against any OpenAI-compatible model endpoint.

It also integrates with the Parallel Works ACTIVATE platform when present: the platform's AI gateway works with zero configuration, the assistant gains workflow tools (catalog, DAG preview, dry-run validation, execution, run monitoring), and the app can be served as a platform session. None of that is required to use it.

The retrieval layer is built on GUFI, the Grand Unified File Index from LANL (per-directory SQLite index with fts5 and vec0 tables). How the whole system works, including incremental indexing and the need-to-know model, is documented in `docs/ARCHITECTURE.md`. Setup and branding for your own deployment: `docs/CUSTOMIZATION.md` and `.env.example`.

## Layout

- `server/` Fastify + TypeScript: KB API, hybrid search, chat tool loop against an OpenAI-compatible model endpoint, upload and URL ingestion, incremental indexing and background sweep, structured queries.
- `web/` Vite React SPA: Chat (full-canvas `@parallelworks/ai-chat`), Library, Search, Query, Help.
- `indexer/` GUFI toolchain build, full rebuild, enrichment (text extraction, OCR, vision captions), embeddings.
- `deploy/` optional ACTIVATE session serving.
- `docs/` architecture documentation and the in-app help content.

## Running standalone

```
pnpm install
pnpm build
indexer/setup_gufi.sh                          # one-time: GUFI toolchain + embedding model
KB_ROOT=/path/to/corpus indexer/reindex.sh     # first index build
KB_ROOT=/path/to/corpus node server/dist/main.js   # http://localhost:4080
```

Environment: `KB_ROOT` (corpus directory), `KB_LABEL`, `APP_NAME`, `APP_ICON` (path to a brand image), `HELP_FILE` (override `docs/HELP.md`), `SUGGESTED_PROMPTS` (JSON array), `APP_USER_ID`/`APP_USERNAME`/`APP_USER_NAME`, `SWEEP_INTERVAL_SEC` (default 300, 0 disables), `ADE_VISION_MODEL` (enables image captioning), `PORT`. A gitignored `.env` in the repo root is the place for deployment-specific values; `deploy/run_endpoint.sh` sources it.

## Models

The chat talks to any OpenAI-compatible backend.

- Standalone: set `OPENAI_BASE_URL` to the endpoint's `/v1` base and `OPENAI_API_KEY` to its key. OpenAI, vLLM, llama.cpp server, Ollama's OpenAI-compatible endpoint, and similar all work. The endpoint must serve `/models` and streaming `/chat/completions`; tool calling is required for the assistant's knowledge base and workflow tools.
- On ACTIVATE: with an authenticated pw CLI on the host, no configuration is needed at all (the server reads the CLI's credential for the platform gateway); otherwise set `PW_API_KEY`. `PW_ALLOCATION` enables org-provider models.

## ACTIVATE integration (optional)

- The assistant's workflow tools (catalog with descriptions and tags, DAG preview, dry-run validation, execution on explicit request, run monitoring) come from the pw CLI and hide gracefully when it is absent. The knowledge base tools work everywhere.
- Serve as a platform session with `deploy/run_endpoint.sh`, which registers the app under the account's reserved subdomain.

## Ingestion

Files and URLs added through the Library's Add panel (or dragged onto the tree, folders included) land in a chosen corpus directory and are searchable in under a second via incremental subtree indexing. Files that arrive outside the UI (scp, generators, git) are picked up by the background sweep within one interval, or immediately via the sync now button.
