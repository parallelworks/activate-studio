# ade-studio

Web interface pairing knowledge base visualization with a chat/code harness, dogfooded on `/shared/PW_KNOWLEDGE_BASE` (read-only) and served as a session in the ACTIVATE account via `pw endpoints`. Prototype of the interaction layer in the AI-Driven Digital Engineering vision (`strategy/ai-de-framework/AI_DE_Framework_Vision.md`, section 7).

## Layout

- `server/` Fastify + TypeScript backend: KB browse/preview API, GUFI-backed search, chat API with a server-side tool-calling loop against the ACTIVATE gateway.
- `web/` Vite React SPA: explorer, document viewer, search, chat panel built from `@parallelworks/ai-chat` components with a custom `ChatAdapter`.
- `indexer/` GUFI index build and enrichment: `reindex.sh` (gufi_dir2index), `enrich.py` (text extraction into fts5 tables plus an on-disk extract cache), embeddings in a later phase.
- `deploy/` scripts to serve the app as a platform session.

## Data and security model

The GUFI index tree replicates the source tree's POSIX permissions; queries run as the calling user and cannot enter directories that user cannot read, so need-to-know is inherited from the filesystem rather than reimplemented. Extracted content written into the index is protected the same way. This deployment is single-user (the index and server both run as the owning user); a multi-user deployment requires per-caller query uids and GUFI's permission-permutation external databases for extracted content.

The knowledge base is mounted read-only from the server's point of view: no API writes into `/shared/PW_KNOWLEDGE_BASE`.

The chat system prompt loads the KB's CLAUDE.md, including the accuracy guardrails (claims not to make, IL terminology, user-count and headcount rules). Those constraints apply to chat output because chat output over this corpus can end up in deliverables.

## Running

```
pnpm install
pnpm build
PW_API_KEY=... node server/dist/main.js       # http://localhost:4080
```

Dev mode: `pnpm dev` (Vite on 5173 proxying /api to 4080).

Serve as a platform session: `deploy/run_endpoint.sh` wraps `pw endpoints run --name ade-studio`.

Index build: `indexer/setup_gufi.sh` once, then `indexer/reindex.sh` after KB changes (also exposed as POST /api/reindex).
