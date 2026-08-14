# ade-studio

Web interface over the Parallel Works knowledge base at `/shared/PW_KNOWLEDGE_BASE`, served as a session in the ACTIVATE account. Three surfaces: a chat assistant with tool calling grounded in the corpus, a library (file tree, viewer, upload and URL ingestion), and hybrid search (full text plus semantic). Prototype of the interaction layer in the AI-Driven Digital Engineering vision (`strategy/ai-de-framework/AI_DE_Framework_Vision.md`, section 7).

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

Environment: `PW_API_KEY` (falls back to the pw CLI credential store), `PW_ALLOCATION` (enables org-provider models), `SWEEP_INTERVAL_SEC` (default 300, 0 disables), `KB_ROOT`, `PORT`.

One-time setup: `indexer/setup_gufi.sh`, then `indexer/reindex.sh` for the first index build.

Serve as a platform session: `deploy/run_endpoint.sh` (stable URL https://ade-studio.activate.pw/).

## Ingestion

Files and URLs added through the Library's Add panel land in a chosen KB directory and are searchable in under a second (incremental subtree indexing). Files that arrive outside the UI (scp, generators, git) are picked up by the background sweep within one interval, or immediately via the sync now button.
