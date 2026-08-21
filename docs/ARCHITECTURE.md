# ACTIVATE Studio architecture

ACTIVATE Studio is a web interface over a knowledge base directory (the configured `KB_ROOT`): a chat assistant with tool calling grounded in the corpus, a library (file tree, document viewer, upload, reorganisation, labels), search (full text plus semantic), a structured query interface, and an OpenAI-compatible endpoint that serves the corpus as a model. It runs against any OpenAI-compatible model endpoint and integrates with the Parallel Works ACTIVATE platform for model access, workflow and cluster tools, per-user identity, and session serving. The retrieval layer is built on GUFI, the Grand Unified File Index from LANL (github.com/mar-file-system/GUFI). This document explains how each layer works and why it is shaped this way.

## 1. The index

GUFI represents a filesystem as a parallel tree of SQLite databases. `gufi_dir2index` walks the source tree and creates, for every source directory, a matching directory in the index containing one `db.db`. Each `db.db` holds the metadata of that directory's files: name, inode, size, mtime, mode, owner, xattrs. Views such as `vrpentries` and `vrsummary` present entries and per-directory rollup totals.

The index lives at `$INDEX_BASE/gufi/<basename of KB_ROOT>/`, where `INDEX_BASE` defaults to `index/` beside the code and is usually pointed at durable storage instead. That extra level is the one `gufi_dir2index` adds for the source directory, and both the full rebuild (`indexer/reindex.sh`) and the server's incremental passes write into it; flattening it leaves a rebuilt index the server never reads. Alongside the tree, `INDEX_BASE` holds the extract cache, rendered PDF pages, settings, the credential vault, saved queries, conversations, and the embedding model, so a deployment can rebuild its working directory without losing state. The build excludes `.git`, `node_modules`, `.venv`, `__pycache__`, `dist`, `build`, caches, screenshots, and dot-directories via a `--skip-file`.

Two properties of this design determine the rest of the system:

- Queries fan out per directory. `gufi_query` walks the index breadth-first with a thread pool and runs your SQL against every `db.db` independently, so a corpus-wide query is hundreds of small local queries.
- The index tree replicates the source tree's POSIX permissions. A query running as a user cannot descend into index directories that user could not read in the source. Need-to-know is inherited from the filesystem rather than reimplemented (section 10).

## 2. Enrichment: making file content searchable

GUFI indexes metadata; we add content. `indexer/enrich.py` walks the index tree and, for every directory db, rebuilds an fts5 virtual table:

```
CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)
```

One row per file: `tinode` is the source file's inode as text, `fname` its name, `wordf` the extracted text (capped at 500 KB). The inode is text rather than an integer because parallel filesystems issue inode numbers above SQLite's signed 64-bit limit, which raised OverflowError and failed the whole pass; every consumer joins on `CAST(tinode AS TEXT)` anyway, since GUFI stores `entries.inode` as TEXT and an uncast comparison matches nothing.

Extraction is by suffix. Markdown, text, code, and config files are read directly. DOCX, PPTX, XLSX and legacy DOC convert to Markdown through [downmark](https://github.com/giraffesyo/downmark), a pure-Go document converter shipped as WebAssembly in the server's npm dependencies (`@giraffesyo/downmark`), so Office extraction needs nothing from the host: `server/src/preextract.ts` runs as a child process before each `enrich.py` pass (and from `reindex.sh`) and writes the cache entries `enrich.py` then reuses. Headings, tables, slide boundaries, speaker notes, chart data and equations survive, and the viewer renders the result. Should downmark fail on a document, `enrich.py` falls back to a standard-library reader, since the three formats are zipped XML; no Python document libraries are involved. Cache entries are refreshed once per downmark version (`extract/.downmark-version`). PDFs use `pdftotext -layout`, which keeps columns and table cells apart, with pypdf as a fallback; a PDF whose text layer is thin for its page count is a scan, so its pages are rendered with `pdftoppm` and read with tesseract. Extracted text is written to `$INDEX_BASE/extract/<relpath>.txt` and reused for instant previews.

Expensive extractions are cached by source mtime. Only images cache an empty result, where a picture with no text is a real answer; a document that extracted to nothing is retried on the next pass, so it indexes once the reader for its format is installed. `GET /api/index/extractors` reports which readers this host has, and the Stats page names any format that can only be indexed by filename.

Images are indexed the same way, because indexing an image means producing text for it: OCR via tesseract, plus a two-to-four-sentence description from a vision model when one is configured. Both flow into the same fts5 tables and vector chunks as any document.

## 3. Vectors: the semantic layer

The same per-directory dbs carry the embedding store; there is no external vector database. `indexer/embed.py` runs two phases:

Phase A (plain sqlite3): chunk each file's `wordf` into a `gchunks` table (cid, tinode, fname, seq, ctext). Chunks are 1,000 characters with 120 overlap, at most 12 per file. Chunks that are less than 60 percent letters and spaces are dropped, and markup-heavy formats (svg, xml, html, css, json, csv) are excluded from embedding entirely; they remain in full-text search.

Phase B (gufi_sqlite3, which compiles in sqlite-lembed and sqlite-vec): for each db, load the GGUF embedding model once, then

```
CREATE VIRTUAL TABLE gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);
INSERT INTO gvec SELECT cid, lembed('minilm384', substr(ctext, 1, 800)) FROM gchunks;
```

The model is all-MiniLM-L6-v2 quantized to Q8 (25 MB, 384 dimensions). Two constraints are encoded here. First, sqlite-lembed does not truncate input, and token-dense text (source code, markup) can exceed the model's context and segfault the process; 800 characters is safe for prose and nearly all code, and a 400-character retry covers the remainder. Second, each directory db is embedded in its own gufi_sqlite3 process, so one bad row cannot abort the rest of the pass.

## 4. Query paths

Three retrieval modes run against the index, all in `server/src/gufi.ts`:

- Full text: `gufi_query -E` runs a per-directory SQL statement joining `vrpentries` to `words` with an fts5 `MATCH`. The query expression drops stopwords, quotes each remaining term, and joins with AND. `snippet()` produces the highlighted excerpt, with newlines flattened because the transport is delimiter-parsed lines.
- Filename: the same fan-out with a `LIKE` on entry names.
- Semantic: one `gufi_sqlite3` process loads the embedding model, embeds the query once into a temp table, then ATTACHes each directory db in turn and takes its top chunks by vector distance, joined to `gchunks` for the excerpt. A directory whose db has no `gvec` table is skipped by reading `sqlite_master` first, because one such directory used to abort the whole vector pass and silently reduce search to full text.

`blendHits()` merges the three modes without letting one starve the others: full-text hits lead, a share of the result budget is reserved for semantic hits, filename matches fill gaps, and duplicates collapse by path. Every result set is annotated with labels (section 5), and a label filter can be applied to any of them.

## 5. Labels

Labels are stored on the files themselves. The primary store is the `user.studio.tags` xattr, so a label survives a move or rename because it belongs to the inode, and GUFI indexes it with the rest of the metadata. Filesystems that refuse xattrs (several network filesystems do) fall back to an overlay keyed by `fsid.inode` with the birth time recorded for validation, which is the convention GUFI's own tooling uses; `reapplyTagOverlay` rejoins the overlay to the rebuilt index on inode after every incremental pass and refreshes the path it stores.

A label on a directory applies to everything beneath it, so a corpus can be organised by folder and filtered by label without labelling each file. `GET /api/tags` returns the vocabulary with counts, which the chat scope selector, the query builder, and search filters all read.

`server/src/labeling.ts` proposes labels for material that has none. It reads the opening text of each unlabelled file, reuses the existing vocabulary wherever it fits, and proposes new labels only where nothing existing describes the material. Nothing is applied: proposals return for review in the Library, or to the assistant through the `suggest_labels` tool, which is told to apply them with `apply_labels` once the user agrees. It reads with whichever model is answering the conversation, so it needs no configuration of its own.

## 6. Chat: retrieval as tools

The chat backend (`server/src/chat/`) is a server-side tool-calling loop against an OpenAI-compatible model endpoint, selected by `OPENAI_BASE_URL`/`PW_GATEWAY_URL` and defaulting to the ACTIVATE gateway. The browser never holds a credential.

The system prompt is composed at startup: an instruction to ground answers in the KB and cite paths, corpus statistics, the account's workflow list, and, when the knowledge base carries its own `CLAUDE.md` conventions file, that file in full, so its rules on tone, terminology, and claims not to make bind chat output. Today's date is injected per request so deadline questions distinguish past from upcoming.

The model chooses among the knowledge tools (`search_kb`, `read_kb_file`, `list_kb_dir`, `query_corpus`, `get_labels`, `suggest_labels`, `apply_labels`), the platform tools (`list_workflows`, `get_workflow`, `compose_workflow`, `run_workflow`, `workflow_runs`, `workflow_run_detail`, `pw_help`, `list_clusters`, `cluster_command`), and the display and extension tools (`show_in_viewer`, `use_skill`, `studio_docs`). Any of them can be disabled per deployment, and file-based extensions can add more (section 9).

The loop runs up to `MAX_TOOL_ITERATIONS` (24) gateway turns. Tool calls within one turn execute concurrently, six at a time, because a research question routinely issues six searches whose latency used to add up in series. Identical repeated calls are served from a per-request cache with a do-not-repeat note; on budget exhaustion a final no-tools turn forces an answer from what was gathered. Tool activity streams to the browser over SSE, with keepalives so a long turn is not cut by an idle proxy.

Top-k chunks are not stuffed into every prompt. The model searches, reads whole files when snippets are not enough, follows cross-references, and cites the paths it used.

## 7. Identity and credentials

With `AUTH_JWKS_URL` set, the platform's session proxy supplies a verified JWT per request, and the app knows who is asking. That identity drives three things: conversations are stamped with an owner and the chat rail defaults to showing your own, exported transcripts record who wrote them, and model credentials are per user.

Personal credentials live in a vault beside the index (`server/src/credentials.ts`), encrypted with AES-256-GCM under a key file written 0600, and never leave the server: status APIs expose the last four characters and timestamps only. A key can be stored or held in memory for a session. The interface states the trust model: a key pasted here is available to the server and its operator.

Four postures are supported. A single-user deployment runs on the deployment credential. `DISABLE_PERSONAL_KEYS` refuses personal keys entirely. The default lets each user add their own and falls back to the deployment credential. `REQUIRE_PERSONAL_KEY` refuses the deployment credential for chat and model listing while leaving browsing, search, and ingestion open to everyone.

One user can also promote their key for everyone else. A promoted key is stored under a reserved vault subject and returned by `gatewayKey()` ahead of the deployment credential, so it covers every path that would otherwise have none, including keyless callers to the `/v1` surface and captioning during a sweep. It satisfies a deployment that requires personal keys. Personal keys take precedence for whoever has one, any verified user can stop the sharing, and the panel names who provided it.

## 8. The /v1 surface

`server/src/ragProxy.ts` serves an OpenAI-compatible endpoint at `/v1`, so any OpenAI-speaking client can use the corpus as a model. Two models are offered. `studio-agent[/<model>]` runs the full assistant pipeline on the server and returns a finished, cited answer: slower, several model calls per question, and unaffected by a calling harness that would otherwise take over the retrieval. `studio-rag[/<model>]` injects retrieved context blocks into a single call: fast, and suited to plain chat interfaces. Either can be hidden from model listings while remaining callable by name. Callers authenticate with their own key; `X-RAG-Top-K`, `X-RAG-Tags` and `X-RAG-Off` tune retrieval per request. `server/src/ragEndpoint.ts` registers the surface in the platform's model catalog through `pw endpoints run --openai`, and the settings page shows the last fifty calls with their model, credential source, duration, and retrieval terms.

## 9. Ingestion, reorganisation, and the sweep

`POST /api/kb/upload?dir=<rel>` accepts multipart files; `POST /api/kb/upload-url` fetches a URL (PDF saved raw; HTML reduced to text with provenance; other text saved as-is). Files land in the chosen directory, which is created on demand; excluded and dot-directories are refused and filenames are sanitized.

Indexing ran inside the upload request until recently, holding it open for the whole pass. With `index=0` the request returns as soon as the files are on disk, and the browser asks for one indexing pass afterwards through `POST /api/index/job`, polling it as its own phase. Uploads send several requests at a time with byte-level progress and can be cancelled; files already sent remain, and the panel reports how many.

Material can be reorganised in place: `POST /api/kb/move`, `/api/kb/copy` and `/api/kb/rename` move, duplicate and rename files and directories, refusing a move into a directory's own subtree and reporting refusals per path rather than failing the batch. Derived artefacts (extracted text, rendered PDF pages, parsed 3D models) are keyed by path, so a move or rename carries them; a copy leaves them to be rebuilt. Both ends are re-indexed before the response, and labels follow because they belong to the inode. A bulk move runs as a watchable job (`server/src/jobs.ts`) rather than one long request.

Incremental indexing (`server/src/indexing.ts`) runs `gufi_dir2index` on the touched subtree into a staging directory, swaps it into the live index, then runs `enrich.py --subdir` and `embed.py --subdir` on that subtree. All index mutations serialize through one lock.

Files also arrive by scp, generators, and git. A background sweep (default every 300 seconds) walks the corpus and computes, per directory, the newest mtime among the directory and its direct files; newer directories are re-indexed and disappeared ones removed. On first startup with no state file the sweep primes rather than rebuilding an index that already exists. A file whose content changes without an mtime update is missed until a full rebuild.

When the corpus directory is missing or empty, the app seeds it with a small starting layout and a README naming the deployment. It only ever runs on an empty corpus.

## 10. Need-to-know

The GUFI security model is that the index is just a filesystem: directory permissions in the index replicate the source, queries run as a user, and a user's queries never enter directories they cannot read. Extracted content is protected the same way, because the fts5 and vector tables live inside the per-directory dbs.

The server itself runs as one user, and every query runs as that user. Platform identity separates people for credentials, conversation ownership, and attribution, but it does not yet run queries under the caller's uid. A deployment where the corpus contains material some readers must not see needs that work first: queries executed under the caller's uid (GUFI's tools support this) and extracted content moved into GUFI's permission-permutation external databases, which encode file-level read permissions as database file modes. Until then, point the app at a corpus its whole audience may read.

## 11. Deployment

Three paths, all driven by `deploy/workflow.yaml` as a platform workflow:

- **github**: clones the public repository on the resource and builds it there. The usual choice while the app is changing; a rerun pulls and restarts.
- **bundle**: unpacks a tarball staged in a bucket, for hosts without GitHub access.
- **container**: runs the Apptainer image built from `deploy/app.def`, staged in a bucket. The image pins Node, GUFI, the embedding model, and the document extractors, so it does not depend on what the host provides, such as an old default python, a stale corepack shim, or an assembler that rejects the CPU's newest instructions. The recipe runs under `set -e` and imports every extractor before the image is sealed; an image missing one indexes Word files by filename with no error.

The corpus and the index live on shared storage; everything derived (node_modules, builds, GUFI) belongs on local disk, which measured about 55 times faster for small-file writes on the deployment that prompted the split. Presentation is deployment configuration: app name, brand icons for light and dark, starter prompts, starter directories, and a classification banner with its own text and colour, drawn only when the app is open on its own so it is not duplicated inside the platform frame.

`docs/CUSTOMIZATION.md` covers file-based extensions: `tools/*.json` add chat tools, `skills/*.md` are instruction sets loaded on demand, and `agents/default.md` is appended to the system prompt as standing instructions.

## 12. Runbook

- Full rebuild: `indexer/reindex.sh` (index plus enrichment plus embeddings). `SKIP_EMBED=1` to skip vectors.
- GUFI toolchain: `indexer/setup_gufi.sh` builds from source and fetches the embedding model.
- Serve as a platform session: `deploy/run_endpoint.sh` wraps `pw endpoints run`.
- Web rebuilds do not need a server restart (static assets are served per request). Server code changes need a restart of the endpoint process, which owns the session: killing the node process alone deletes the session.
- Health: `GET /healthz`, `GET /api/index/status` (sweep timing, last changes, last error), `GET /api/index/extractors` (what this host can read).
- Known failure modes: lembed segfaults on token-dense chunks are handled by the 800/400 caps; a directory that still fails is logged and remains covered by full-text search. Endpoint tunnels can report offline while the local process lives; restart the endpoint process.

## 13. File map

```
server/src/
  main.ts          Fastify bootstrap, static serving, seeding, sweep timer
  config.ts        paths, exclusions, limits, tool-iteration budget
  kb.ts            safe path resolution, tree listing, file preview, cache paths
  gufi.ts          gufi_query/gufi_sqlite3 wrappers, three query paths, blending
  indexing.ts      incremental subtree indexing, sweep, lock, status, extractor report
  jobs.ts          background jobs with progress (moves, indexing passes)
  move.ts          move, copy, rename plus their bookkeeping
  uploads.ts       multipart and URL ingestion
  tags.ts          label read/write, inheritance, vocabulary, overlay reapply
  tagsOverlay.ts   inode-keyed label store for filesystems without xattrs
  labeling.ts      label proposals for unlabelled material
  credentials.ts   per-user credential vault, shared-key promotion
  auth.ts          platform JWT verification
  conversations.ts chat history, ownership, transcript export
  attachments.ts   chat attachments
  preview.ts       office-to-PDF conversion, PDF page rendering
  model.ts         3D model parsing and cache
  query.ts         structured query builder backend
  ragProxy.ts      OpenAI-compatible /v1 surface
  ragEndpoint.ts   platform registration of that surface
  settings.ts      runtime-editable deployment settings
  extensions.ts    file-based tools, skills, agent instructions
  seed.ts          first-run corpus layout
  workflowCompose.ts  compose workflows into one that runs them as subworkflows
  routes.ts        KB/search/workflow/index/label REST routes
  chat/gateway.ts  OpenAI-compatible streaming client, credential resolution
  chat/tools.ts    tool schemas and executors
  chat/context.ts  system-prompt composition
  chat/routes.ts   SSE tool loop, concurrent tool execution, key management
web/src/
  App.tsx          left nav shell, view switching, hash navigation
  lastLocation.ts  per-tab restore of the open view
  views/           Chat, Library, Search, Query, Stats, Settings, Help
  components/      Explorer, Viewer, ClassificationBanner, DagViewer, TagMenu
  upload.ts        batched upload with progress and cancellation
  accents.ts       theme tokens derived from the platform's palette
  adapter.ts       ChatAdapter over the server SSE endpoint
indexer/
  setup_gufi.sh    toolchain build
  reindex.sh       full rebuild
  enrich.py        text extraction into fts5 words tables (--subdir for incremental)
  embed.py         chunking and vec0 embeddings (--subdir for incremental)
deploy/
  workflow.yaml    platform workflow: github, bundle, or container deployment
  app.def          Apptainer image recipe
  run_endpoint.sh  serve as a platform session
```
