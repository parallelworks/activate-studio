# ACTIVATE Studio architecture

ACTIVATE Studio is a web interface over a knowledge base directory (the configured `KB_ROOT`). It has three surfaces: a chat assistant with tool calling grounded in the corpus, a library (file tree, document viewer, upload and URL ingestion), and search (full text plus semantic). The retrieval layer is built on GUFI, the Grand Unified File Index from LANL (github.com/mar-file-system/GUFI). This document explains how each layer works and why it is shaped this way.

## 1. The index

GUFI represents a filesystem as a parallel tree of SQLite databases. `gufi_dir2index` walks the source tree and creates, for every source directory, a matching directory in the index containing one `db.db`. Each `db.db` holds the metadata of that directory's files: name, inode, size, mtime, mode, owner, xattrs. Views such as `vrpentries` and `vrsummary` present entries and per-directory rollup totals.

The index lives at `index/gufi/<basename of KB_ROOT>/` inside the repo. The build (`indexer/reindex.sh`) excludes `.git`, `node_modules`, `.venv`, `__pycache__`, `dist`, `build`, caches, screenshots, and dot-directories via a `--skip-file`. Working scale for the current deployment is a few thousand files across a few hundred directories, dominated by markdown, office documents, PDF, and source code.

Two properties of this design carry the rest of the system:

- Queries fan out per directory. `gufi_query` walks the index breadth-first with a thread pool and runs your SQL against every `db.db` independently, so a corpus-wide query is hundreds of tiny local queries, not one big scan.
- The index tree replicates the source tree's POSIX permissions. A query running as a user cannot descend into index directories that user could not read in the source. Need-to-know is inherited from the filesystem rather than reimplemented (section 8).

## 2. Enrichment: making file content searchable

GUFI indexes metadata; we add content. `indexer/enrich.py` walks the index tree and, for every directory db, rebuilds an fts5 virtual table:

```
CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)
```

One row per file: `tinode` is the source file's inode, `fname` its name, `wordf` the extracted text (capped at 500 KB). Extraction is by suffix: markdown, text, code, and config files are read directly; DOCX via python-docx (paragraphs plus table cells); PDF via pdftotext with a pypdf fallback; PPTX via python-pptx; XLSX via openpyxl. Extracted text for binary formats is also written to `index/extract/<relpath>.txt`, which the server uses for instant previews of DOCX/PDF/PPTX/XLSX in the viewer.

Every directory db gets the `words` table even when empty, so a corpus-wide `MATCH` never fails on a missing table. Full-text hits join back to file metadata by inode. One GUFI detail matters here: `entries.inode` is stored as TEXT, so the join is `ON inode = CAST(tinode AS TEXT)`; without the cast SQLite compares text to integer and matches nothing.

Images are indexed the same way, because indexing an image means producing text for it. For each image of meaningful size, extraction combines two sources: OCR via tesseract (which carries most of the weight for screenshots and labeled diagrams), and, when `ADE_VISION_MODEL` is set, a two-to-four-sentence description from a vision model through the gateway's streaming Responses API. The combined text flows into the same fts5 tables and vector chunks as any document, so images are findable by what is written on them and by what they depict. Expensive extractions of every kind (PDF, office, OCR, captions) are cached by source mtime in the extract cache, so vision calls happen once per image version; `indexer/warm_image_cache.py` fills the cache in parallel for an existing corpus so enrichment passes never block on captioning.

## 3. Vectors: the semantic layer

The same per-directory dbs carry the embedding store; there is no external vector database. `indexer/embed.py` runs two phases:

Phase A (plain sqlite3): chunk each file's `wordf` into a `gchunks` table (cid, tinode, fname, seq, ctext). Chunks are 1,000 characters with 120 overlap, at most 12 per file. Chunks that are less than 60 percent letters and spaces are dropped, and markup-heavy formats (svg, xml, html, css, json, csv) are excluded from embedding entirely; they remain in full-text search.

Phase B (gufi_sqlite3, which compiles in sqlite-lembed and sqlite-vec): for each db, load the GGUF embedding model once, then

```
CREATE VIRTUAL TABLE gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);
INSERT INTO gvec SELECT cid, lembed('minilm384', substr(ctext, 1, 800)) FROM gchunks;
```

The model is all-MiniLM-L6-v2 quantized to Q8 (25 MB, 384 dimensions), the same model the GUFI master document uses in its RAG walkthrough. Two hard-won constraints are encoded here. First, sqlite-lembed does not truncate input, and token-dense text (source code, markup) can exceed the model's context and segfault the process; 800 characters is safe for prose and nearly all code, and a 400-character retry covers the remainder. Second, each directory db is embedded in its own gufi_sqlite3 process, so one bad row cannot abort the rest of the pass. A full re-embed of the corpus is about 15,000 chunks and takes two to three minutes on this host; a single directory takes well under a second.

## 4. Query paths

Three retrieval modes run against the index, all in `server/src/gufi.ts`:

- Full text: `gufi_query -E` runs a per-directory SQL statement joining `vrpentries` to `words` with an fts5 `MATCH`. The query expression drops stopwords, quotes each remaining term, and joins with AND. `snippet()` produces the highlighted excerpt, with newlines flattened because the transport is delimiter-parsed lines.
- Filename: the same fan-out with a `LIKE` on entry names.
- Semantic: one `gufi_sqlite3` process loads the embedding model, embeds the query text once into a temp table, then ATTACHes each directory db in turn and takes its top 3 chunks by vector distance (`WHERE fp384 MATCH ... ORDER BY distance LIMIT 3`), joined to `gchunks` for the excerpt. Node merges all per-directory candidates and takes the global top k. With ~320 dbs this completes in well under a second; there is no global vector index to maintain, and the per-directory layout means the permission model applies to vectors exactly as it does to metadata.

`blendHits()` merges the three modes without letting one starve the others: full-text hits lead, up to a third of the result budget is reserved for semantic hits, filename matches fill gaps, and duplicates collapse by path.

## 5. Chat: retrieval as tools, not context stuffing

The chat backend (`server/src/chat/`) is a server-side tool-calling loop against the ACTIVATE gateway's OpenAI-compatible surface. The browser never holds a credential; the server reads `PW_API_KEY` or, when unset, the pw CLI's own token store (`~/.config/pw/credentials`, matched by gateway host, re-read when the file changes). Org-provider models need an `X-Allocation` header and are hidden from the model list unless `PW_ALLOCATION` is set.

The system prompt is composed at startup: an instruction to ground answers in the KB and cite paths, corpus statistics, the account's workflow list, and, when the knowledge base carries its own `CLAUDE.md` conventions file, that file in full, so its rules on tone, terminology, and claims not to make bind chat output; text produced in this interface can end up in deliverables. Today's date is injected per request so deadline questions distinguish past from upcoming.

The model chooses among the knowledge tools (`search_kb` hybrid retrieval, `read_kb_file` raw or extracted text, `list_kb_dir`) and the platform tools: `list_workflows` (the account's workflow catalog with descriptions and tags, positioned as composable building blocks for assembly recommendations), `get_workflow` (parsed YAML plus a jobs-and-needs DAG summary for preview), `run_workflow` (dry-run validation by default; a real launch only on explicit user request in the conversation), `workflow_runs` and `workflow_run_detail` (status, errors, log tails), and `pw_help` (discovers the wider pw CLI surface). The loop runs up to 24 gateway turns; identical repeated calls are served from a per-request cache with a do-not-repeat note; on budget exhaustion a final no-tools turn forces an answer from what was gathered. Tool activity streams to the browser over SSE and renders in the thread's reasoning disclosure.

This is retrieval-augmented generation with the model in the driver's seat: instead of stuffing top-k chunks into every prompt, the model searches, reads whole files when snippets are not enough, follows cross-references, and cites the paths it used. The GUFI layer is what makes the tools fast enough for a 46-call research loop to finish in about 80 seconds.

## 6. Ingestion: upload to searchable in under a second

`POST /api/kb/upload?dir=<rel>` accepts multipart files; `POST /api/kb/upload-url` fetches a URL (PDF saved raw; HTML reduced to text with a title and provenance header; other text saved as-is). Files land in the chosen KB directory (`uploads/` by default, created on demand; excluded and dot-directories are refused; filenames are sanitized).

Both endpoints then run the incremental indexer (`server/src/indexing.ts`) before returning:

1. `gufi_dir2index` on just the touched top-level subtree, into a staging directory.
2. Swap the matching subtree of the live index (build aside, then rename).
3. `enrich.py --subdir` and `embed.py --subdir` on that subtree only.

Measured end to end: about 250 to 450 ms for a typical directory. All index mutations serialize through one lock, so concurrent uploads and sweeps queue rather than corrupt.

## 7. The sweep: catching files that arrive outside the UI

Files also land in the KB by scp, generators, and git. A background sweep (default every 300 seconds, `SWEEP_INTERVAL_SEC` to change, 0 to disable) walks the corpus and computes, per directory, the newest mtime among the directory itself and its direct files. Directories newer than the recorded state are re-indexed incrementally; directories that disappeared have their index subtrees removed. A changed parent covers its whole subtree, so nested changes dedupe to subtree roots. State lives in `index/sweep-state.json`.

On first startup with no state file the sweep primes: it records the current scan without indexing, on the assumption that the index is current (full rebuilds handle real drift). Without priming, a fresh deployment would grind through a directory-by-directory rebuild of an index that already exists. Manual triggers: the "sync now" button in the UI, or `POST /api/index/sweep`.

The mtime design has one known blind spot: a file edited in place deep in the tree changes that file's mtime, which the per-directory scan catches; but a file whose content changes without an mtime update (rare; some sync tools) is missed until a full rebuild. `indexer/reindex.sh` remains the full rebuild path and is unchanged.

## 8. Need-to-know

The GUFI security model is that the index is just a filesystem: directory permissions in the index replicate the source, queries run as a user, and a user's queries simply never enter directories they cannot read. Extracted content is protected the same way, because the fts5 and vector tables live inside the per-directory dbs.

This deployment is single-user: the server, the index, and the queries all run as the owning user, and the platform session URL is behind ACTIVATE authentication. A multi-user deployment must run queries under the caller's uid (GUFI's tools support this; the server would need a per-user execution path) and move extracted content into GUFI's permission-permutation external databases, which encode file-level read permissions as database file modes. Until that work is done, do not point a multi-user frontend at this server.

## 9. Runbook

- Full rebuild: `indexer/reindex.sh` (index plus enrichment plus embeddings, a few minutes). `SKIP_EMBED=1` to skip vectors.
- GUFI toolchain: `indexer/setup_gufi.sh` builds from source to `/opt/gufi` and fetches the embedding model. Ubuntu needs cmake, libsqlite3-dev, pkg-config, zlib1g-dev, libpcre2-dev, libattr1-dev, attr, autoconf, automake, libtool.
- Serve as a platform session: `deploy/run_endpoint.sh` wraps `pw endpoints run --name ade-studio --subdomain ade-studio`. The subdomain is reserved to the account; `pw endpoints list` shows the session.
- Web rebuilds do not need a server restart (static assets are served per request). Server code changes need a restart of the endpoint process.
- Index health: `GET /api/index/status` (sweep timing, last changes, last error). Server logs carry every chat tool call and sweep action.
- Known failure modes: lembed segfaults on token-dense chunks are handled by the 800/400 caps; if a directory still fails it is logged and remains covered by full-text search. Endpoint tunnels can occasionally report offline while the local process lives; restart the endpoint process.

## 10. File map

```
server/src/
  main.ts          Fastify bootstrap, static serving, sweep timer
  config.ts        paths, exclusions, limits
  kb.ts            safe path resolution, tree listing, file preview
  gufi.ts          gufi_query/gufi_sqlite3 wrappers, three query paths, blending
  indexing.ts      incremental subtree indexing, sweep, lock, status
  uploads.ts       multipart and URL ingestion
  routes.ts        KB/search/workflow/index REST routes
  chat/gateway.ts  OpenAI-compatible streaming client, credential resolution
  chat/tools.ts    tool schemas and executors
  chat/context.ts  system-prompt composition
  chat/routes.ts   SSE tool loop
web/src/
  App.tsx          left nav shell, view switching
  views/           ChatView (ai-chat components), LibraryView, SearchView
  components/      Explorer, Viewer, UploadMenu, StatusFooter
  adapter.ts       ChatAdapter over the server SSE endpoint
indexer/
  setup_gufi.sh    toolchain build
  reindex.sh       full rebuild
  enrich.py        text extraction into fts5 words tables (--subdir for incremental)
  embed.py         chunking and vec0 embeddings (--subdir for incremental)
```
