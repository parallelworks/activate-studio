# Change log

Every version, newest first. Each entry is the description of the pull request that made the change, which is written as a change note when the change is made.

## v1.182 (2026-10-28)

### fix(chat): fit a Studio with no platform and a small local model (#616)

A Studio running on a computer of its own, with a small model served on that computer and no platform, ran into four problems. This change fixes each one.

**No platform, no platform tools.** Small models called `list_clusters`, `hpc_status`, `pw_help`, `workflow_runs`, and `list_workflows` and got errors back, because the workflows group's cue matches words like "storage" and "file systems", and only the platform API tools were checked for credentials. The chat now uses `platformAvailable` (the same test the Workflows page uses). That test now also requires a gateway that belongs to a platform: the one the pw CLI context or the default names, or one named in the environment whose path is `/api/openai/v1`. `OPENAI_BASE_URL` pointing at a local model server, even with `OPENAI_API_KEY` set, does not count. Without a platform:
- the workflows, changes, and agents tools are not offered, and `more_tools` and the Tools menu don't list those groups
- the system prompt leaves out the platform section and its mentions of workflow DAGs, sessions, and delegation
- the workflow catalog is not requested from the pw CLI
- a call to a tool that was not offered does not run, and the model gets one line saying it is not available in this Studio

With a platform connected, behavior is unchanged.

**Citations.** The prompt's `[relative/path.md](#open=file:relative/path.md)` placeholder got copied into replies. The prompt now uses a concrete example and explains `&lib=<id>` for files in other libraries. Citations are also repaired on the server as the reply streams (`server/src/chat/citations.ts`). A bracket is held back only until it closes on its line, so what is stored matches what was shown. These forms become `[path](#open=file:path)`, with `%20` in the target only:
- a path starting with `relative/path/`
- a link whose target is a bare path that a tool returned
- a bare `[path]` naming a file a tool returned in the conversation (tool-call parts now keep the files they returned)
- percent-encoding in the link text

A file from another library gets `&lib=<id>`, which the web app's `#open` parser already reads, so the link opens in the right library. The repair runs on the server and not in the web renderer because only the server knows which files the tools returned and which library each came from.

**read_kb_file returned "binary".** Root cause: it called `readFileContent` without the library's `cacheRoot`, so it looked for every library's extracted text in the main folder's cache. It now uses the library's own cache, the same way the viewer does. If that cache has no text (an index the Studio only reads, or a library on another system), it falls back to the file's row in the `words` table of the library's index, read with `gufi_query` (on the remote host for a remote library). If neither has text, the model is told plainly, with the reason.

**Snippets starting mid-word.** `embed.py` cuts passages at fixed 880-character steps, and search-by-meaning snippets were the first 200 characters of a passage. Snippets for later passages now start at the first space, and a snippet cut at full width ends on a whole word followed by `…`. The chunker is unchanged, because changing it would change every passage's cache key and force re-embedding.

Tests: new `standaloneChat.test.mjs` drives the chat route in a standalone setup:
- nothing platform-related is offered or run
- the Tools menu has no platform switches
- citations split across streamed pieces are repaired
- read_kb_file returns a built library's document text, and the bare path it returned is linked into that library

Other new or extended tests:
- `citations.test.mjs`: each repair, what is left alone, and the same output for every split point of a stream
- `toolApproval.test.mjs`: platform-connected case, with the tools and the platform section still offered
- `sshLibraries.test.mjs`: document text from the remote index, and the no-text message
- `searchOnePass.test.mjs`: a passage cut mid-word

Docs: HELP.md (chat), REFERENCE.md (chat, search).

Server: 721 tests, 708 pass, 13 skipped. Web: 330 pass.

### feat(model-access): name the sources, take portal options for endpoints, and check tool calling (#617)

A site with its own AI portal (an OpenAI-compatible proxy: Bearer virtual key, base `https://<host>/v1`) was entered on the API key tab, which is for ACTIVATE platform keys. That tab strips any path and asks `https://<host>/api/openai/v1/models`, so every attempt (host, host/v1, host/v1/chat/completions) failed with "…does not exist (404). An OpenAI-compatible base URL usually ends in /v1", including when the URL already ended in /v1. Separately, a model without tool calling passed Test, which only lists models, and then produced unusable chat.

- The tabs are ACTIVATE (pw CLI), ACTIVATE API key and OpenAI-compatible endpoint; with sign-in on, ACTIVATE (deployment) replaces the first. Each panel opens with one line on what it is for. Where the row is narrower than the names (a phone, or the page beside the status panel) the choices stack instead of truncating (container query). The setup wizard's AI step uses the same names and mentions a site AI portal.
- On the ACTIVATE API key tab, an address with a path other than `/api/openai/v1` is read as an endpoint: a callout says so, Save is held, and "Use as an endpoint" moves the address (trimmed to its base URL) and the key to the endpoint tab. Test on such an address tests it as an endpoint and says so. For a host only, a 404 on the platform path is followed by one test of `<host>/v1` (tools check off), and the note says whether that answers as an OpenAI-compatible endpoint, with the same one-click move. An explicitly typed `http://` is kept.
- A 404 message (`notFoundMessage`) names the URL tried; on the ACTIVATE gateway path, says the host is not an ACTIVATE platform; on a base ending in `/chat/completions` or `/models`, gives the base without it; on a base already ending in a version, says the endpoint may not list its models and points to typed model names; otherwise suggests `<base>/v1`. The test answer carries `notFound` with the URL.
- The endpoint tab has a collapsed Advanced part: key header (`Authorization: Bearer`, `api-key`, `x-api-key`, none), extra headers (name and value rows), model names typed by hand, and a query string. With model names set, nothing asks for `/models`: the names are the listing, and the health check is a streamed completion with the first name, cached five minutes and dropped on Re-check. Validation refuses reserved headers (Host, Content-Type and the like), a header that duplicates the key header, CR/LF in values, and malformed query strings.
- Options are stored with the key and protected like it: in the vault, encrypted with the vault secret (`opts` beside `blob`) when sign-in is off; in memory and inside the sealed key cookie when sign-in is on. The browser only ever gets `{auth, headers: [names], models, query}`; header values are not returned or logged, and the completion cache is keyed by a digest. Saving or testing with an empty key keeps the stored key only for the address it was stored for; a header row sent without a value keeps its stored value; a test with no key against another address is refused and sends nothing there. Platform keys carry no options and endpoint keys are still never shared or given to platform tools.
- `listModels`, `aiHealth`, `streamTurn` and the new `probeToolCalling` take them, so they cover the chat model listing (`/api/chat/models`, also used by Settings), `/api/ai/health`, the key test, chat turns (native and emulated paths), label suggestions (listing and completions), and the `/v1` proxy. The `/v1` proxy used `resolveUserKey` and always called the gateway, so a signed-in caller with a stored endpoint had that endpoint's key sent to the gateway; it now forwards to the endpoint (studio-rag passthrough, studio-agent turns, `/v1/models?all=1`, the default model) and gives no endpoint key to tools. Gateway provider probes no longer run against an endpoint's model ids. Image captioning during indexing uses the deployment gateway and is unchanged.
- After a test answers ok, one completion offers a single `get_utc_time` tool with a prompt that needs it (`max_tokens` 256, retried without a cap if refused, 15 s limit), streamed or not and with template kwargs as chat would send for that model. The model is the chat's remembered pick when listed, else `ragDefaultModel`, else the first non-embedding model. Results: calls; answered without calling it (noting when it wrote the call as text); endpoint refused the tool (chat would fail every turn); not checked (timeout, or a reasoning model that spent the cap). Models named in `CHAT_SYSTEM_IN_USER` are reported as driven by the prompt-level emulation (`emulatedPrompt`, which also pre-runs a library search) instead of probed. Nothing is refused on the result. The page shows it under Test; the wizard shows it under the model list.
- Docs: HELP Model access (tab names, Advanced, what Test reports), REFERENCE (test route fields, new Endpoint options section on storage, application, the typed-models health check and the tool check), DEPLOY (a site's AI portal), README (one line).

Tests:
- `server/test/endpointOptions.test.mjs` (new, against a loopback portal stand-in that wants `api-key`, an extra header and an `api-version` query): option validation and stored-value merge; header and query helpers; each 404 message; tool check model choice and wording; Test refused without options and passing with them; tool check calls / no-call / refused; save encrypts the options, the browser sees names only, and every request (listing, health, chat, `/v1` models, studio-rag, studio-agent) carries the key header, extra header and query and no Authorization; empty-key saves keep the key and header values for the same endpoint only; typed model names replace the listing and the cached completion health check; options survive the sealed cookie.
- `server/test/aiFeatures.test.mjs`: the new 404 text and `notFound`, and the tool check request on Test.
- `web/test/modelAccess.test.tsx`: tab names and lead lines; `readPlatformHost`, `endpointBase`, `testNote`; the endpoint-address callout and one-click move; the host-only 404 then `/v1` flow; stored headers shown by name with an empty value kept on save and test; key header None enabling save without a key.
- `web/test/setupWizard.test.tsx`: the tool check line and the 404 message.
- Server suite: 715 tests, 702 passed, 0 failed, 13 skipped. Web suite: 42 files, 337 tests passed.
- Checked by hand against a built Studio and a local portal stand-in, at 1280 and 400 px: both API key tab cases, the move to the endpoint tab, a no-tools model warning, Advanced options tested and saved, and a chat turn through the saved endpoint making a tool call.

### feat: GUFI commands in chat tool calls, with their error output, and a command log (#618)

## What changed

**GUFI commands in chat.** The chat's `search_kb`, `query_corpus` and `list_kb_dir` calls show the GUFI commands they ran under their row, in full, each with a copy button, using the same `GufiCommands` block as Search and Query. Before, the tool block's result text carried the commands as plain text, cut at 4,000 characters with `[truncated]`.

- `executeTool` returns the commands as structured records (`gufi`) instead of formatted text. The chat route sends them on the tool's `result` event and keeps them on the stored call; the call's `result` is now the summary alone. The model's tool message is unchanged and never includes them.
- `GET /api/chat/conversations/:id` returns a call's commands only to the viewer whose request ran them (`withOwnCommands`); anyone else who opens a shared conversation sees the call without them. With sign-in off there is one viewer.
- The web registers a tool renderer for those three tools. The adapter keeps streamed commands by call id, since the package's part does not carry extra fields. The result summary stays on the call's row, and the commands toggle sits on its own line under the tool name. Those calls no longer fold into the "Ran N tools" line, because a call the host draws is kept out of it by the package.

**Error output, exit status and time on Search and Query.** Every recorded command (`server/src/gufiTrace.ts`) now has `at`, `ms`, `exit` (or `failed`: the signal, or why it did not run), and its error output.

- `stderr` keeps what the command wrote, as it came, without the expected lines, up to its last 8 KB. A "no such table" line for the text or vector tables (`words`, `gvec`, `gchunks`, `gvecmodel`, `gvec_rowids`, `ext_bm25_by_file`, `ext_vectors_by_file`) is counted in `expected`, with one kept as an example. The UI labels these as expected, from directories without text or vectors.
- `stderrCut` is set when only the end was kept: a walk keeps 16 KB (`runQuery`, unchanged), and a remote command 4,000 bytes (`remoteExec.ts` now reports when its tail was cut). `stderr` is absent when it was not kept (for example, a command whose ssh session failed), and the UI says so.
- Local `gufi_query` (execFile and spawn), remote commands over the session loop and direct ssh, `gufi_vt` statements, and each `gufi_vt` location are covered. A new `remoteGufi()` runs and notes remote commands, including ones whose session fails. The remote tree listing that `list_kb_dir` uses is now recorded too.
- A remote command records `shell` (`pw ssh <cluster>`, or `ssh` with the library's options), so a command over plain ssh no longer says `pw ssh`.
- `POST /api/query` that fails returns the commands it ran with its error, and the Query page shows them under the error.

**Optional command log.** Settings, General, Indexing has "Log GUFI commands" (`gufiLog`; `STUDIO_GUFI_LOG=1` turns it on by default). When on, every GUFI command the Studio runs to read an index is appended to `gufi-commands.log` beside the index log (`STUDIO_DATA_DIR/logs`, else `INDEX_BASE/logs`). Each line is one JSON object: `time`, `source` (`search`, `query`, `tool`, `stats`, `library`, `labels`, `map`, `history`, `libraries`, `other`), `engine`, `site`, `ms`, `exit`, `failed`, the command (first 16 KB), and the end of its error output (2 KB, with the expected lines counted). One previous file is kept once it passes 10 MB. The settings page shows the path and size when the log is on. Checks around a command (a `gufi_vt` location's check, library table probes, the label lookups) are logged but not shown in the views. The indexing programs already write their output to the index log, so the GUFI command log does not repeat them. Commands and error output stay in this file and in the requesting viewer's views. They are not sent anywhere else.

Docs: `docs/HELP.md` (chat, Search, Query, the index settings), `docs/REFERENCE.md` (a "GUFI commands" section), `docs/LIBRARIES.md` (The GUFI commands).

## Why

A user who deploys GUFI asked to see the full commands the assistant runs, to see each command's error output and exit status on Search and Query, and to keep a log of every command for debugging a deployment.

## Tests

- Server: new `test/gufiCommands.test.mjs` covers:
  - splitting expected from other error output
  - records with exit, signal and failure
  - log-only notes and sources
  - turning the log on and off
  - the commands a failed request ran
  - a remote command's cut stderr tail, through a local session loop
  - a local `gufi_query` with expected lines (needs GUFI)
  - a chat turn in which the tool's commands go to the requester, never to the model, and not to another viewer of the conversation (needs GUFI)

  `test/gufiVt.test.mjs` is updated for the structured records and checks exit status for the `gufi_vt` statement and its location.
- Web: new `test/gufiCommands.test.tsx` covers the meta line, the error output labels and panel, the adapter keeping streamed commands, and a stored conversation drawing the commands under a `search_kb` row. `test/settingsGeneral.test.tsx` covers the log switch.
- Full suites: server 715 tests, 702 pass, 13 skipped (they need ONNX, GGUF or numpy, which this machine does not have), 0 fail. Web 336 pass across 43 files.
- Checked by hand on a local Studio with a stand-in model, at 1280 px and 400 px: the chat tool rows (closed, commands open, error output open, after a reload), Search, Query (a successful query with expected lines, and a failed one), and the settings switch.

## v1.181 (2026-10-27)

### feat: export query and search results as CSV (#613)

Query and Search results get **Export CSV** and **Copy**.

- **Export CSV** saves the rows as a CSV file (RFC 4180, values exactly as the query returned them, sizes in bytes). Query exports its columns as shown, with the library column when several libraries were queried. Search exports one row per result in the order listed: library (when several), path, name, how it was found, size, modified time in UTC, labels and snippet.
- **Copy** puts the same rows on the clipboard as tab-separated text, which a spreadsheet pastes into columns.
- The file is a browser download; in the macOS app's window it goes to Downloads and is shown in the Finder (the existing WKDownload handling).
- Exports take the rows returned: Query up to its limit (2,000 at most), Search what is listed (up to 1,000 with Load more). The guide says so and points to the GUFI commands for more.
- The results toolbar wraps at narrow widths.

Tests: web/test/tableExport.test.tsx (CSV quoting, TSV, file names, the search table, and the Search page's Export CSV producing the file). Checked in Chromium against a running Studio: both downloads arrive with the expected names and contents, and the toolbars at 1280 and 400 px.

### fix: search and query other libraries before the main folder is indexed (#614)

Before the main folder's first build its index directory can be absent. Search (`/api/search`), Query, the assistant's search tool and the capability probes of other libraries all called `gufiAvailable()`, which also requires the main folder's index, so every library on this computer answered "index not built yet" until the main folder was built. Libraries on other hosts were unaffected.

- `config.ts`: `gufiInstalled()` (gufi_query is here) and `gufiIndexAvailable(root)` (that and the given index); `gufiAvailable()` keeps its meaning for the main folder.
- Search, Query (one library or several), the assistant's search and the library probes now need GUFI and the library's own index. The main folder's own state is reported as before through the omissions ("not indexed yet"), and a single-library Query of it still returns 409 with that reason.
- `vectorModels` and `corpusStats` check the index they were asked about.
- Only GUFI missing on this computer stops a local search, with "GUFI is not installed here".

Test: omissions.test.mjs gains a case with the main index directory removed, where search and Query of another library return its rows and name the main folder. It failed before the fix ("index not built yet", 0 hits). The existing "never indexed" case created an empty main index folder first, which is why it passed. Full server suite: 706 tests, 0 failed.

### Change log through v1.181 (#615)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.181` before tagging v1.181.

## v1.180 (2026-10-27)

### Default new libraries built here to Meaning on documents (#609)

A library built of a folder on this computer started at Text + OCR, so it had no search by meaning and no semantic map until its depth was raised by hand. It now starts at Meaning on documents, as the main folder does. A library built on a remote system stays at Text + OCR, for the reason below.

**Defaults** (`server/src/indexSettings.ts`, `web/src/components/IndexDepth.tsx`)

- `NEW_LIBRARY_DEPTH` is `meaning-documents`, for a library built here when no depth is chosen: `POST /api/libraries/build` without `depth`, the Add a library form, and the setup wizard's other-libraries step. The build check (`POST /api/libraries/build/check`) describes extraction at that depth for a library not added yet.
- `NEW_REMOTE_LIBRARY_DEPTH` is `ocr`, for `POST /api/user-libraries/ssh/build` and `.../pw/build` without `depth`.
- The web form defaults follow: `DEFAULT_LIBRARY_DEPTH` is `meaning-documents`, and `DEFAULT_REMOTE_LIBRARY_DEPTH` is `ocr` for the remote build form.
- Connected indexes are unchanged.

**Upgrade**

- The depth is now stored with the library when it is added, the default included. A later change of the default leaves an existing library as it was built.
- A library added by an earlier version has no depth stored. It still reads as before: Text + OCR, or what its saved level of search by meaning said.
- Reading the list writes nothing to `indexing.json`.
- The Libraries list's fallback for a library without a depth is `UNSTORED_LIBRARY_DEPTH`, `ocr`, to match the server.

**Remote builds stay at Text + OCR**

- The build on another system runs `embed.py` there with only that script copied. Without the ONNX packages it uses the GGUF model through sqlite-lembed, and its vectors are `minilm384`. That model is used only where `~/.activate-studio/models/minilm384.gguf` exists there; the Studio does not copy the model.
- Search reads a remote index's vectors only in its walk. The walk embeds the query with the ONNX model (`queryVectors` in `gufi.ts`), so it does not match `minilm384` vectors. The `searchVector` fallback skips remote roots (`libraryScope.ts`). The semantic map returns `reason: 'remote'`.
- So a Meaning stop on a remote build adds embedding time there, and search does not use the result.
- The form says why it starts at Text + OCR and how to have the vectors made anyway: choose a Meaning stop, and they are made where the model is on that system.

**Meaning comes last, and the UI says so**

- The phases run in order: folders, text, then meaning. A first build swaps its tree in once the folders are indexed, as before.
- The meaning phase now shows on the row as **Computing search by meaning**. It was "Embedding text", locally and on remote builds.
- On a first build at a Meaning stop, the library is probed again once its text is read. Search then reads its words while meaning is computed.
- Omissions (`server/src/omissions.ts`) during a build at a Meaning depth:
  - before the text is read: `not searched by meaning yet: its text is being read`
  - after it: `not searched by meaning yet: meaning is still being computed`
  - `not searched by meaning: its index has no vectors yet` is kept for a library that is not building.
- After a local build starts, the toast says the library is searchable by name once its folders are indexed. Text and meaning, where its depth has them, follow in that order.
- At a Meaning stop, the local form adds a note: search by meaning is the slowest part of indexing and runs last, in the background. For a folder of more than about 500,000 files, Text + OCR is faster, and meaning can be added later in Settings, Libraries.
- Nothing counts files before a build, so the note is static and the default does not change with folder size.

**Semantic map**

- A library built at the new default is scheduled for its map 5 s after the build, through the existing `remapAfterMeaning` (#607).

**Tests**

- `server/test/newLibraryDepth.test.mjs`:
  - the two defaults;
  - three libraries seeded as an earlier version left them (no settings, a meaning level, a depth), which keep `ocr`, `meaning-documents` and `text`, with `indexing.json` unchanged;
  - the check's extract line at the default;
  - a build with no depth stored as `meaning-documents`, with a map scheduled;
  - a folder of read-only files at the same default;
  - a chosen depth kept.
- `remoteBuild.test.mjs`: a pw ssh build with the model present on the remote still starts at `ocr`, stored, and `embed.py` is not sent.
- `omissions.test.mjs`: the wording during a build, before and after the text, for building and updating.
- `libraryBuild.test.mjs` and `libraryMaps.test.mjs`: updated for the new default; the map test asks for `ocr` where it needs a library without meaning.
- Web:
  - the local form starts at Meaning on documents, and the note shows there and goes at Text;
  - the toast after a local build;
  - the remote form starts at Text + OCR with its note;
  - the wizard's other-libraries step starts at Meaning on documents.

**Docs**: HELP.md (the defaults, the order, the large-folder note), LIBRARIES.md (defaults, upgrade, the phase order and omission, the remote rule with the reason), REFERENCE.md (the omission reasons), README (the depth table).

**Not changed**

- A source folder whose top directory has no write permission for its owner (mode 0555) fails to build here: `gufi_dir2index -x` copies that mode to the index's top directory, and moving the tree into place then fails with EACCES.
- This happens at any depth and is the same on main.
- It is not addressed here.

### Read-only source folders, and remote builds with the ONNX model (#611)

Two fixes to library builds.

**A source folder its owner may not write.** gufi_dir2index gives each index folder its source folder's mode as it finishes that folder, before the folders inside it make theirs. Run by a user other than root, a source folder of mode 0555 (a read-only share, a snapshot, a tree unpacked read only) got an index folder the build could not write in. Every folder inside it was left out, with a `mkdir <index folder>/db.db failure: 13 Permission denied` line and an exit status of 0, and the build then failed moving the tree into place (`EACCES` on the rename of `.build/<name>`). The main folder failed the same way. A sync, the text pass and a change of depth could not write into such an index either.

The folder scan that the build and the sync already take (`dirScan.ts`, or the names count for the main folder) now also lists the folders that lack owner read, write or search. Before gufi_dir2index runs, the index folders inside those are made, since it accepts a folder that already exists. After it, those index folders get `u+rwx`, parents first, and keep their group and other bits (`server/src/indexModes.ts`). GUFI's record of the source is unchanged: `vrsummary` and `vrpentries` keep 0555 and 0444.

Index trees are now removed with a removal that opens folders when a removal is refused, so a tree an earlier version left with 0555 folders can be removed, rebuilt or deleted with the library. A sync opens such a tree and indexes the folders inside a locked folder that it left out. A `mkdir ... failure` line that remains names the folder as left out, by its source path, instead of being dropped. On a remote system, the build runs gufi_dir2index again after making those index folders when its log has such lines, then opens the index folders with `find` before moving the tree.

**Remote builds embed with the Studio's ONNX model.** The one-pass walk matches only ONNX vectors (`gvecmodel` `minilm-l6-mean-int8`). Remote builds embedded with the GGUF model or not at all, so their vectors were never searched. The build check now reads the platform of the Python there as `fetch_embed.py` names it, and which versions are installed in `~/.activate-studio/embed/<platform>-<digest of the pins>`. A build that needs a version that is not there starts at once, and the Studio sends the missing files while it runs:

- the wheels from the bundle's `indexer/wheels`, or from the Studio's own `INDEX_BASE/embed-wheels`, filled by `fetch_embed.py --download --platform` where this computer reaches the package index;
- `model.onnx` and `tokenizer.json` from its own model folder.

Each file is checked against `embed_deps.json`. A marker there holds the build's meaning phase until the sending ends. The build installs with `fetch_embed.py --deps --no-readers --model`, with no network when every file arrived, or downloads the rest where that system can. It checks the imports and moves the install into place. Later builds send nothing. Where no wheels are pinned for that Python (3.10, say) or they do not import, the build uses the GGUF model as before. Each build makes the index anew, so earlier GGUF vectors are made again with the ONNX model. New remote libraries now start at Meaning on documents, and the form note says what the first build sends.

Tests:
- `server/test/readOnlySource.test.mjs`: a library and the main folder whose top folder and folders inside are 0555, with files 0444, run as a non-root user. Covers the build, GUFI's record of the modes, a change of depth, a sync after a change (a subfolder, the top folder's files, and a removed folder), an index left by the earlier version, and deleting the library. Before the fix, 7 of these 8 tests fail with the errors above.
- `remoteBuild.test.mjs`:
  - a read-only tree built over ssh and rebuilt;
  - the new default depth and the check message;
  - the GGUF fallback with no network anywhere;
  - the first ONNX build, which sends 3 wheels and 2 model files, installs once with downloads blocked on both sides, and leaves no sent files or marker; the one-pass walk then finds the passage by meaning;
  - a second build that sends nothing.

  The ONNX tests run where `STUDIO_TEST_ONNX_DIR` and `STUDIO_TEST_WHEELS` are set, and the fallback test where `STUDIO_TEST_GGUF` is set.

### Semantic maps of libraries on another system (#610)

A library on another system now has a semantic map. The Studio fetches a sample of its vectors over the library's own connection and draws the map here, in place of the "is on another system, which keeps its vectors" note.

## Fetch

`server/src/remoteMap.ts` runs two aggregate `gufi_query` walks and nothing else, so a host whose login runs only `gufi_query` answers them.

1. The count walk returns, per model, the files with vectors, their passages and a digest of passage ids and hashes. It reads `gchunks` joined to `gvec_rowids`, and `ext_vectors_by_file` for all-MiniLM-L6-v2 through `--external-copy`. A few rows come back.
2. The fetch walk covers the model most files use. Each file comes back as one row:
   - its path;
   - the mean of its passage vectors, folded in SQL with `vec_add` in passage order and normalized, then quantized with `vec_quantize_int8(..., 'unit')` to 768 hex characters;
   - that mean's length, so the mean the local map uses can be rebuilt (cosine with the true mean above 0.998);
   - the first 1,000 characters of the first passage, for about 10,000 files.

The fold carries each file's passage count, so the last sum is selected without a join. The first version joined, and SQLite reran the fold for every file: 7.4 s became 0.22 s on 20,000 files.

Measured over ssh to localhost, on 20,000 files with 50,000 passages:

| Step | Result |
|---|---|
| Count | about 60 ms |
| Fetch | 0.8 s for 27 MB of rows |
| Fetch, wire size (loopback) | 34 MB as base64 in the session loop; 24 MB compressed, 37 MB uncompressed |
| Map | 1.9 s |

## Changes

- **ssh compression:** ssh connections now pass `-o Compression=yes`. A library's own `-o Compression=no` comes first and wins.
- **Session loop:** it now looks for the end of a large answer only in the pieces that contain one.
- **Sample:** above `STUDIO_REMOTE_MAP_FILES` files (default 50,000), the sample is the files whose inodes hash lowest. The walk selects slightly more than the cap with a hash threshold, and the Studio trims the result to the cap. The same files come back each time. The map's status line gives the display cap and the fetch sample together, for example "3,000 of 20,000 files shown (a sample of 5,000 fetched from host, made with minilm-l6-mean-int8, just now); click a cluster for all of its files.", with Refresh beside it.
- **Build:** `semantic_map.py --vectors <file> --model <m> --of <M>` builds the map from the fetched file and adds `of` to the overview. Without numpy, the server builds its smaller fallback map from the same file. The map and `vectors.jsonl` are kept in the owner's folder (`INDEX_BASE/users/<hash>/maps/<id>`) and removed with the library.
- **Names:** clusters are named by TF-IDF over the fetched text. A cluster without text falls back to its folder names, with parents, as before.
- **Neighbors:** the file's fetched vector is the query. One `gufi_query` walk runs vec0's nearest-neighbor search (or cosine distance for the external schema) in each directory, for the same model, and keeps each thread's best results. Answers are cached until the map is rebuilt.
- **Refresh:** the map is checked when someone views it and the last check is over an hour old. It is also checked after a remote build finishes, and when a connection test finds the index rebuilt. A check runs only the count walk. The Studio fetches again only when the counts or the digest changed. **Refresh** on the map (`POST /api/libraries/:id/semantic-map/refresh`) always fetches again.
- **States:**
  - progress while counting, fetching and drawing;
  - a failed fetch keeps the old map and shows the error, or with no map shows **Try again**;
  - `remote-none` for no vectors, or too few for a map;
  - the model the vectors come from, and how many files were left out because another model embedded them;
  - for a remote library the Studio builds, a depth control that applies at the next Rebuild.
- **Picker:** remote libraries with a map are listed in `/api/semantic-maps`, which never starts a fetch.

## Tests

- **Server** (`libraryMaps.test.mjs`): a fake ssh host whose login runs only `gufi_query`, on real GUFI indexes with sqlite-vec. Covered:
  - the first map is built from one count and one fetch;
  - other models are filtered out;
  - the rebuilt mean and its length;
  - storage in the owner's folder and removal with the library;
  - neighbors searched on the remote host and then cached;
  - the 5-file cap selects the same lowest-hash files twice;
  - the hourly check fetches only after a change on the remote host;
  - failure keeps the old map, a first map that fails can be retried;
  - no vectors, and too few.
- **Server** (`semanticMap.test.mjs`): `--vectors` reads int8 rows and names clusters from their text; the no-numpy fallback works from fetched vectors.
- **Web:** the one status line with the sample, model, time and Refresh; refresh progress; failure with Try again; no vectors and too few; the depth offer for a remote library the Studio builds.
- **macOS** (`check_app.sh`): builds an index with vectors through the app's own enrich and embed, adds it on the `gufi_query`-only host, then checks the map, the neighbors, and that only `gufi_query` ran on that host.
- Docs: HELP.md, REFERENCE.md, LIBRARIES.md, README.md.

### Change log through v1.180 (#612)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.180` before tagging v1.180.

## v1.179 (2026-10-27)

### Maps for any library: a semantic map and a corpus topology per library (#607)

The corpus topology and the semantic map on the Stats page were drawn from the main folder only. A Studio whose main folder holds a few files and whose large libraries are elsewhere showed a nearly empty map. Each map now shows one library, picked beside its title.

**Picker**

- `LibraryScope` gains a one-library form (`single`): the same button, with a radio list that closes on a pick. It shows only when there is more than one library.
- Each map keeps its own choice in this browser (`web/src/mapLibrary.ts`, `ade-map-library-topology` and `ade-map-library-semantic`). A library that is gone falls back to the default.
- Defaults: the topology starts on the main folder. The semantic map starts on the library whose map has the most files (`GET /api/semantic-maps`), else a library with vectors, else the main folder.
- With one library the page is unchanged, and it still uses `/api/kb/topology` and `/api/kb/semantic-map`.

**Semantic map per library** (`server/src/semanticMap.ts`)

- Each library on this computer with vectors has its own map state and map folder. The main folder's map stays in `INDEX_DIR/semantic-map`. Another library's goes in `INDEX_BASE/libraries/<id>/semantic-map`, and `removeLibrary` deletes it through a new `onLibraryRemoved` hook in `libraries.ts`.
- A library built here is mapped 5 s after its build when its depth includes meaning, 5 s after a sync that applied a new depth with meaning, and 10 minutes after other syncs. Rebuilds are coalesced per library, as before.
- Routes, keyed by the viewer as other library routes are:
  - `GET /api/libraries/:id/semantic-map`, `.../cluster/:c` and `.../neighbors`. These return 404 for an unknown library.
  - `GET /api/semantic-maps` for the picker.
  - The `/api/kb/semantic-map` routes are kept.
- The overview carries `library` (`kind`: main, built, connected or remote; `depth`; `vectors`). The reasons for no map are:
  - `off`: a depth without search by meaning. The page says "Desktop has no search by meaning. Set its depth to Meaning on documents to draw it here; that reads its documents once, in the background." For the main folder or a library built here, a **Set to Meaning on documents** button calls `PUT /api/libraries/:id/depth`. The page then follows the build.
  - `connected`: an index connected as it is, with no vectors the Studio reads. The page says "This index was built elsewhere and has no meaning vectors the Studio can read."
  - `remote`: a library on another system, which keeps its vectors there.
  - `none` and `failed`, as before. While a map is being built the page says so and polls every 8 s, or every 30 s while it waits for embeddings.
- Not covered: a connected index whose vectors are only in another schema's tables (`ext_vectors_by_file`). The map reads the Studio's own `gvec` tables. Such an index reports `connected`, including when a build attempt found nothing it could read.
- The 3D view, links, legends, cluster names and nearest-file lines from #590 work per library. Neighbor lookups and their caches are per library, and opening a file from another library's map opens it in that library.

**Topology per library** (`server/src/topology.ts`)

- One aggregate `gufi_query` per folder asked for: `-I`/`-K` create the tables, `-E` reads `vrsummary`, `-J` and `-G` sum.
  - The path below the folder is taken by string length from `rpath(sname, sroll)`. `level()` reports a rolled-in directory at its parent's level, so it is not used.
  - A recursive CTE cuts the path to six parts, so each directory's totals go to its ancestor six levels down. A folder at that depth holds everything below it.
  - The answer is cut a level shallower while it has more than 20,000 rows.
- The query runs where the index is: here, or over ssh through the existing remote transport as `gufi_query` alone, so a host that runs nothing else answers it.
- Answers are kept 2 minutes for a library here and 10 for a remote one. `refresh=1` asks again.
- `GET /api/libraries/:id/topology?dir=&refresh=1` is added, and `/api/kb/topology` keeps working.
- The treemap asks again below a folder as you open one near the last level fetched. It merges the rows and shows the library's name as the first crumb.

**Measured**

- A 22,001-file index (2,448 folders) on the test's fake host, whose login runs only `gufi_query`, answers the topology in 91 ms. That is one command; the cached read sends none, and a refresh sends one.
- In a local demo, a 14,599-file library at Names depth answered in 26 ms (582 rows).
- In the demo:
  - The 219-file library built at Meaning on documents got its map after its build.
  - A names-only library set to Meaning on documents from the map's button drew its map 16 s after the click.

**Tests**

- Server, `test/libraryMaps.test.mjs` (real GUFI, map builder stubbed, ssh through the spawn seam):
  - a built library's map is made after its build with meaning, into its own folder, and is read at its address;
  - a depth without meaning gives `off`, and a new depth with meaning makes the map;
  - the picker listing;
  - a connected index without vectors gives `connected` and refuses a depth;
  - nearest files come from the library's own index (sqlite-vec);
  - removal deletes the map;
  - topology of the main folder and of a built library;
  - the 22,001-file remote topology: totals, the six-level cut, the cache and refresh, and drill-in;
  - a remote library's semantic map and neighbors give `remote`.
- Web:
  - the default library;
  - switching and remembering a library;
  - each empty state, and the depth button's request;
  - opening a file in its library;
  - the topology picker with drill-in.
  - The old empty-state test expects the new wording.
- `check_app.sh`: on the macOS runner's gufi_query-only ssh host, reads that library's topology and checks that nothing but `gufi_query` ran there.
- Full suites pass locally: server 672 tests (663 pass, 9 skipped), testdata 21, web 299.

Docs: the Stats paragraph in `docs/HELP.md`, and the semantic map and new endpoints in `docs/REFERENCE.md`.


**Follow-up commits: three fixes found while testing this**

- *What a library holds, counted.* Words and vector tables were looked for in a sample of twelve directory databases. A library whose vectors sit in a few folders past the sample was recorded with none, so Search skipped it by meaning and said its index had no vectors. In the demo, a 219-file library with vectors in 184 of its 1,300 directories lost every meaning result. What the sample misses is now counted over the whole tree in the background: one aggregate `gufi_query` over each directory's `tree.sqlite_master`, which takes about 50 ms for 1,300 directories. The count runs after every build or sync and at most hourly otherwise, is recorded as `exactAt`, and is discarded and redone if the tree changed during the count.
  - The rule (`lacks`): Search, the left-out notes, Query's note, the semantic map and the assistant's library list treat a local library as missing words or vectors only once that count found none. Until then Search looks there.
  - Libraries on other systems are unchanged.
  - Tests: vectors in one directory and words in another, both past the sample; an index with none; a stale mark; a change during a count.
- *Cluster names from whole words.* `embed.py` cuts passages every 880 characters wherever that falls, so names included fragments such as "eef" and "onar". Where passages overlap, the characters around the cut now say whether a word was split; otherwise the cut word is dropped. A test runs `embed.py`'s own chunker over 400 texts, including texts with skipped passages, and finds no fragments; a map built from those passages names only whole words.
- *Treemap cells fill their parent.* Each squarified row took its share of the full byte total out of the remaining rectangle instead of the whole rectangle. Every row after the first came out too small, and one dominant folder left most of the rectangle empty. Area is now bytes at one scale, and the last row and last cell take what is left. Layout tests cover skewed sizes (90%, 99.9%, 97%, a 200-folder power law) in three rectangle shapes. They check that cells stay inside the parent, each cell's area matches its byte share, no two cells overlap, and the areas add up to the parent's. The old layout fails these tests.

Suites after these commits: server 676 (672 pass, 4 skipped, with numpy here), testdata 21, web 318.

### Change log through v1.179 (#608)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.179` before tagging v1.179.

## v1.178 (2026-10-27)

### A connection per set of ssh options, and the Mac app check on server changes (#605)

The v1.177 Mac app build failed in `deploy/mac/check_app.sh` with "a library's ssh options were not used" (run 40455265494). Nothing was published.

## Cause

#603 named the shared ssh socket by ssh's `%C` alone. `%C` is a hash of the local host, remote host, port, user and jump host, and does not cover a library's other options. In the check, the gufi_query-only library is first added without options, and Query opens a connection to it. Its options are then changed to `-o UserKnownHostsFile=... -o StrictHostKeyChecking=accept-new`. The connection test with the new options rode on the open connection, so ssh never logged in with them and the library's own known_hosts file was not written. #600 had kept one socket per set of options for this reason.

## Fix

- The socket name is `%C-<8 hex>` when the library has ssh options, the hash taken over its options with each `-o` keyword in lower case (`optionsTag` in `server/src/remoteExec.ts`). With no options it stays `%C`, so a library given as an alias and one given as `user@host` still share one connection. The 12-character fallback for a long socket folder is unchanged.
- Grouping for stacked search and Query uses the same rule: the effective destination from `ssh -G` (user, host name, port, jump hosts, proxy command) plus the options hash. A stacked location runs one ssh with the first member's options, so its members must have the same options.
- The mac-app workflow now also runs on pull requests that change `server/src/**` or `indexer/**`. Before, it ran only for `deploy/mac/**`, which is why #603 did not run `check_app.sh`. `check_app.sh` does not exercise the web client, so `web/src` is not added. The release input stays on `workflow_dispatch` only.

## Tests

`server/test/sshOptions.test.mjs`:

- Two libraries on one host, one with `-o` options and one without, get two sockets: `%C` and `%C-<hash>`. As `ssh -G -F /dev/null` expands them, the sockets differ. An alias and `user@host` with no options expand to the same socket, and the same holds when both have the same options. A keyword in another case gives the same socket.
- End to end with GUFI: two libraries on one host, one with `-o UserKnownHostsFile=... -o StrictHostKeyChecking=accept-new` and one without. Query runs them as two locations, and every ssh for the library with options carries them while the other's carry none. They also have different ControlPaths. Search makes two walks, one with the options.
- Both tests fail on main and pass with this change. The test that checks a jump host gets its own socket now also checks that an options-only library gets its own.

Server suite on Node 26: 665 tests, 656 pass, 9 skipped, 0 fail.

### Change log through v1.178 (#606)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.178` before tagging v1.178.

## v1.177 (2026-10-27)

### Converter line out of indexed text, same-host libraries by where ssh lands, Libraries table that fits (#603)

Three fixes from a GUFI user's screenshots of the macOS app at v1.175.

## Converter line in the indexed text

Each cache entry the document converter writes starts with `<!-- extracted by downmark 0.10.0 -->`. `enrich.py` copied the whole entry into the `words` table, so the line was the start of every converted document's text: "downmark" and "extracted" matched every converted document, snippets began with it, and `embed.py` cut it into the first passage.

- `enrich.py` and `embed.py` drop the line (`index_text`) before writing words or cutting passages. The cache entry keeps it, since the converter uses it to decide when to reconvert.
- Search snippets drop it at read time (`withoutProvenance` in `server/src/textCleanup.ts`), which also covers indexes built on other systems until their next Rebuild.
- An existing local index is cleaned in place at its next sync, once: one `UPDATE` per directory database removes the line from the start of each `words` row, nothing is extracted again, and a `.studio-text-clean` file in the index root records that it is done. A build writes the file too. The sync's job reads "Tidying the indexed text" when that is all it does. Vectors already embedded from a passage that held the line are kept.

## Libraries on one host that are written differently

Two libraries on the same account were walked separately when one was given as `user@host` and the other as a Host alias whose User comes from `~/.ssh/config`, because grouping compared the destination as typed.

- `reachedHost` runs `ssh -G` with the library's options and port (it reads the config and connects nowhere) and keeps the user, host name, port, jump hosts and proxy command for ten minutes. Libraries with the same answer are one host for the search walk and for the `gufi_vt` location in Query.
- The shared connection's `ControlPath` now ends in ssh's `%C`, which ssh computes from the same host, port, user and jump, so those libraries also share one socket. Where the socket folder is too long for the 40 characters `%C` expands to, the 12-character name is used as before.
- Library lists and `GET /api/user-libraries` carry `reached` (`user@host`), and Settings shows it before each index path, so both libraries read the same.

## Settings, Libraries table

At 1300 and 1440 px with the settings menu and the status panel open, the table had about 460 px. Path values were about 60 px wide and 260 to 334 px tall, and the table was 605 px wide inside a 460 px wrapper, so it ran under the status panel.

- The block layout is chosen by the table's own width (a container query at 720 px) instead of the window width, so it applies with the side panels open as well as on a phone.
- Labels sit above their values (`DescriptionList stacked`), so a path gets the full cell width.
- host:path values wrap only after a dot in the host, the colon, or a slash in the path.
- An index with no files reachable from here showed "Files: none: names and metadata only", even when it held text. That row is now "Holds" with what the index can be searched by: names and metadata, plus text, plus meaning.

After the change: 434 px values and no overflow at 1300 and 1440 px, 686 px at 740 px, and the table layout with columns at wider windows. Checked in headless Chromium with a remote library with a long host and path and a read-only local index.

## Tests

- `server/test/textCleanup.test.mjs`: "downmark" matches nothing after a build, snippets start with the document's text, an index from before is cleaned at its next sync without reconverting and only once, and the cleanup skips directories without a words table.
- `server/test/sshOptions.test.mjs`: with a stand-in `ssh -G`, a library typed as `me@host` and one typed as an alias are shown as the same account, make one `gufi_vt` location and one search walk, and get the same `ControlPath`.
- `server/test/largeIndexes.test.mjs` and `server/test/sshTransport.test.mjs` check the socket that ssh expands from `%C`.
- `web/test/librariesPage.test.tsx`: the Holds wording, `reached` shown in place of the destination, and the break points in a long host and path.

### Change log through v1.177 (#604)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.177` before tagging v1.177.

## v1.176 (2026-10-26)

### Libraries over ssh: options per library, warm connections, one gufi_query per host in Query (#600)

Three changes for remote GUFI indexes over ssh, from a user who connects indexes behind firewalls and saw most of a Query's two seconds go to ssh setup.

## SSH options per library

The Over SSH form has a new **SSH options** field, for example `-J me@jumphost -o ConnectTimeout=20`. The Host field now says that a Host from `~/.ssh/config` works, ProxyJump included, since the Studio runs the system ssh with the account's config.

- Allowed: `-J` (jump hosts `[user@]host[:port]`, comma separated), `-p`, `-i` and `-F` (a path on this computer, absolute or under `~/`), `-l`, `-o Keyword=value`, `-4`, `-6`, `-C`.
- `-o` takes an allowlist of keywords about reaching and authenticating to the host: ProxyJump, HostName, Port, User, IdentityFile, IdentitiesOnly, CertificateFile, IdentityAgent, UserKnownHostsFile, StrictHostKeyChecking, HostKeyAlias, the GSSAPI keywords, PreferredAuthentications, PubkeyAuthentication, the algorithm lists, timeouts and keepalives, and a few more (full list in `docs/LIBRARIES.md`).
- Refused, with the reason in a 400:
  - anything that runs a command on this computer (ProxyCommand, LocalCommand, PermitLocalCommand, KnownHostsCommand) or replaces the remote command (RemoteCommand);
  - what the Studio sets itself (BatchMode, ControlMaster, ControlPath, ControlPersist);
  - forwarding, SendEnv and SetEnv, and any keyword not on the list.
- No value may contain spaces, quotes or backslashes, and nothing reaches a shell: the options go to ssh as argv. A ProxyCommand belongs in a Host entry in `~/.ssh/config`, whose alias is then the host.
- Kept with the library as argv and carried in its site (`ssh://dest[:port]#<base64url argv>`), so sessions, connection sharing, gufi_vt locations and grouping by host tell two ways of reaching a host apart. The site's options are checked again whenever it is used.
- Passed first on every ssh the library makes (sessions, gufi_query-only direct calls, the gufi_vt wrapper, the connection test, warming), so a library's ConnectTimeout wins over the default.
- The connection test names the jump host: from the options, or from `ssh -G`, which reads `~/.ssh/config` and connects nowhere. For example "Connected to me@lab through gw@bastion, a Linux host."
- Settings, Libraries shows a library's options. `PUT /api/user-libraries/:id/ssh-options` changes them on a connected index after a connection test with them passes.
- Kerberos is unchanged: BatchMode only refuses prompts, and nothing disables GSSAPI.

## Warm connections

- `ControlPersist` comes from `STUDIO_SSH_PERSIST` (seconds, default 600, at most a day; 0 closes the connection with its last session).
- `POST /api/user-libraries/warm` opens each of the viewer's ssh hosts' shared connections with `ssh -N -f` when `ssh -O check` finds none, at most once a minute per host, without waiting. `ssh -N -f` runs no command on the host, so gufi_query-only hosts are warmed too.
- Search and Query call the route as they open, at most every two minutes. Nothing changes in the UI.

## One gufi_query per host in Query

Remote libraries on one host, reached the same way (site and key) and asked the same plan, share one gufi_vt location with an `index=` per root. gufi_vt takes `index=` more than once and writes every root at the end of a remote location's command line, at the bundled commit and at 09109d1.
- **Pre-check:** one `gufi_query -S` over all the roots, one row per root. When a row is missing, each root is asked alone and the failures are reported.
- **The wrapper's check:** it accepts the location's roots in order and passes each as its own argument.
- **Rows:** each goes back to the library whose root its path is under. Grouped totals carry no path, but they are summed across libraries afterwards anyway.
- **Not stacked:** raw SQL, which has no known shape. `STUDIO_STACK_REMOTE=0` turns stacking off.
- **Unchanged:** gufi_query-only hosts (the remote line still starts with gufi_query) and the char(34) quoting.

## Measurements

ssh to a private sshd on localhost with key login, so these show no network or Kerberos latency:

| | Time |
|---|---|
| One ssh call, cold | 186 to 210 ms |
| One ssh call over a live master | 8 to 18 ms |
| Opening a master with `ssh -N -f` | 137 to 159 ms |

Query across two libraries on one host, median of 7:

| | Apart | Stacked |
|---|---|---|
| Cold | 526 ms | 430 ms |
| Warm master | 336 ms | 248 ms |

On a host whose login runs only gufi_query, so each command is counted, a Query over two libraries on one host sent 4 remote commands apart and 2 stacked, with the same rows.

## Tests

- New `server/test/sshOptions.test.mjs` (11 tests):
  - option parsing: allowed forms, refused keywords with reasons, injection attempts, and a tampered site;
  - the site round trip, shell form, sockets per option set, and the port given once;
  - ControlPersist and STUDIO_SSH_PERSIST;
  - options on every ssh a library makes (test, sessions, tree, search);
  - changing options;
  - the jump host from `ssh -G`;
  - warming and the warm route;
  - Query stacking with real GUFI: one check and one gufi_query for two libraries, rows back to each, a missing index reported;
  - the wrapper's check of stacked roots.
- `sshTransport.test.mjs` expects the new `options` and `sshOptions` fields.
- New `web/test/sshOptions.test.tsx`: the form field, its inline validation, the request bodies, no field over pw ssh, and the warm throttle.
- `deploy/mac/check_app.sh`, on the CI runner's localhost forced-command host:
  - changes the library's options to a `UserKnownHostsFile` of its own and checks that file was written;
  - adds a second index on the same host with the same options;
  - requires one Query to add exactly two commands to the host's log, with rows from both libraries.
- Server suite: 634 passed, 9 skipped. Web suite: 283 passed.

Docs: `docs/LIBRARIES.md` (Host aliases and ProxyJump, SSH options, warming, Query stacking), `docs/HELP.md`, `docs/REFERENCE.md`.

### Search and Query take any set of libraries, and search refines names apart from text (#601)

Two changes from user feedback on searching several GUFI indexes at once.

## Any set of libraries

- Search and Query each have a library picker where any of the libraries can be ticked, with **All libraries** as a shortcut. The button reads All libraries, the one library's name, or "N libraries". At least one stays ticked, and ticking every one is All.
- Each page keeps its own choice in localStorage (`ade-scope-search`, `ade-scope-query`), starting at All libraries. The Library page's tree and Stats keep their one-or-all switch, since the tree shows one library or all of them.
- Server: `GET /api/search`, `POST /api/query` and `POST /api/query/sql` take `library` as one id, `all`, or several ids: a list in a body, a repeated query parameter, or ids with commas. Ids are taken once each, in order, `all` among them means all, and an unknown id is a 404. Among several, a library with nothing to answer from is named in `omitted`, as with all. Query over several runs on gufi_vt exactly as over all (tested with real GUFI: two of three libraries, engine `gufi_vt`, the same rows as all, filtered).
- A result or row opens in its own library: in the combined tree when the Library page shows all, else by switching the Library page to it.

## Refined search

- **Refine** beside the search button splits the box into two fields:
  - **Name or path**: words, which match any part of a name or a folder below the library's root, and patterns with `*` or `?` (`*.docx`, `reports/*.pdf`, where a slash matches the path), with OR between groups and -word or NOT word to exclude.
  - **Text or meaning**: an fts5 MATCH expression for the words in files (`AND`, `OR`, `NOT`, `"phrase"`, `prefix*`, `NEAR`). The same words with the operators, quotes, brackets and column filters taken out are the phrase searched by meaning.
- A switch, **Search text and meaning only in files whose name or path matches**, chooses between:
  - off (the default): matches of either kind are listed, each to its own limit (names and folders as before, words `limit`, meaning 10), and the answer holds all of them, so many name matches never crowd out the text;
  - on: only files matching both are listed, such as the .docx files that mention a budget.
- Built into the one-pass walk (`onePassStages` in `server/src/gufi.ts`): with the switch on and both fields filled, no name rows are listed, and every statement that reads words (the Studio's `words`, `ext_bm25_by_file`) or vectors (`ext_vectors_by_file`, the Studio's `gvec`) carries the name and path filter in the same directory visit. vec0's nearest neighbours would be picked before the filter, so the Studio's vectors are scored as `vec_distance_l2` over the kept files' chunks. That is the distance vec0 reports for these unit vectors; a test checks the two give the same number. Meaning searched the earlier way, for an index from before the current model, is filtered by the same rules in `server/src/refine.ts`.
- The text is checked before a search runs, since a malformed fts5 expression otherwise fails in every directory and the search finds nothing without saying why. The check uses fts5 in `node:sqlite` where this Node has it, plus a hand check of quotes, brackets and operators at the ends. A mistake answers 400 `The text search cannot be read (...)`, with how to quote a word with punctuation, and the page shows it.
- The page puts the search in its address (`#view=search?q=`, or `?name=&text=&match=all`, with `in=` naming the libraries when not all) and runs it on arrival, so a refined search can be bookmarked or shared. App routing now accepts `?fields` after a view.
- The assistant's `search_kb` takes `name`, `text` and `match` beside `query`, and `library` as ids with commas.
- With no refine fields the single box is unchanged (a test compares its stages).

## Tests

- New `server/test/searchRefine.test.mjs`, with real GUFI and sqlite-vec:
  - the name field's parsing and SQL, patterns escaped for LIKE;
  - the meaning phrase;
  - the text check and its messages;
  - any and all on a real index, NOT and -word, a word matching a folder, all with no text;
  - meaning filtered to the kept files' chunks, with the same distance as vec0;
  - each kind keeping its own limit against 30 name matches;
  - the single box unchanged;
  - the fallback filter.
- `libraryScope.test.mjs`: ids as a list and with commas, order, dedupe, all among them, 404; Query over two libraries; search over two through the route, repeated and comma forms, a refined search, and the 400.
- `gufiVtSpaces.test.mjs`: Query over two of three libraries on gufi_vt.
- New `web/test/libraryScope.test.tsx`:
  - the scope store and settling;
  - the picker's ticks, All, and last-one rule;
  - Search sending the ticked libraries;
  - the refine row and switch, and the address round trip;
  - the 400 message;
  - opening a match in its library;
  - Query sending a list.
- Server suite: 636 passed, 9 skipped. Web suite: 288 passed.
- Checked in headless Chromium at 1300 and 740 px on a test server with three libraries built by gufi_dir2index: the picker, a two-library search with library badges, a refined search (`*.md` with `budget NOT draft`, both on) and the same search opened from its address, and the Query picker.

Docs: HELP.md (the picker, Refine) and REFERENCE.md (scope parameters, the refined search's rules and limits).

### Change log through v1.176 (#602)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.176` before tagging v1.176.

## v1.175 (2026-10-25)

### Search reads each index once for names, folders, words and meaning (#596)

Search reads each library's index once for every kind of match: names, files below a matching folder, the Studio's words, the Studio's vectors, and external databases in the shared text and vector schema. The kinds are fused with reciprocal rank fusion. This follows the one-pass pattern GUFI's authors recommend: one `gufi_query` whose per-directory step adds every kind while the directory's tables are open, merged per thread and ranked once at the end.

## What changed

- `walkSearch` (server/src/gufi.ts): one aggregate `gufi_query` per library, or per remote host, with these stages:
  - `-I`: a per-thread table, plus the query's embedding as a blob literal, one copy per thread. That works on every GUFI in use; `--global-db` would do the same on newer ones.
  - `-E`: four statements at most, in a fixed order:
    1. a trim that keeps each kind's best;
    2. names and folder-path matches in one insert;
    3. the Studio's words (fts5 `words`, by `bm25`);
    4. the Studio's vectors (vec0 `gvec`, by distance), with the query vector picked by the directory's `gvecmodel`.

    A directory without a `words` or vector table fails the step that needs it, and only the later steps are skipped there. No empty tables are written. gufi_query compiles every statement again in every directory, so there are few.
  - `--no-print-sql-on-err`: each such failure prints one short line, and only the last 16 KB of error output is kept. A GUFI from before 2026 that does not know it, or `--external-copy`, is asked again without them.
  - `-G`: ranks each kind, keeps each file once per kind and to the kind's limit, and fuses the kinds with RRF, k = 60.
- `fuseRows`: the same fusion across libraries and walks. Ties go to the more exact kind (words, name, folder, meaning), then the path, so the order is deterministic. Hits carry `mode` (the most exact kind, now including `path`), `kinds` (every way the file was found) and `score`.
- Folder-path matches: files below a folder whose path below the library root matches, with the same term rules as names. A file whose name matches counts as a name match only.
- External databases in the shared schema, read with `--external-copy`. Each file in an index directory other than `db.db` whose name matches `STUDIO_EXTERNAL_DB_PATTERN` (default `\.db$`; empty turns it off) is attached, and two tables are read:
  - `ext_bm25_by_file`: text by `bm25`.
  - `ext_vectors_by_file`: for a `vector_model_name` whose query vector the Studio has (all-MiniLM-L6-v2, which the bundled ONNX model quantizes), by cosine distance put on vec0's scale.

  Rows join the directory's entries on `binode` and `bmtime`, so a file changed since extraction is not matched. A library's "words not searched" note is withdrawn when its external databases answered.
- Remote: libraries on one host and account share one `gufi_query` with their roots stacked, one round trip per host. Before, each library ran one remote command for names and words, and none for meaning. Rows go back to their library by the root they are under. A walk past its deadline names every library in it.
- Local: libraries keep a walk each. Stacked local roots measured no faster; `STUDIO_STACK_LOCAL=1` turns stacking on.
- Fallback for meaning: a library whose index has vectors but whose walk returns none (embedded with the earlier GGUF model) is searched by meaning the earlier way. The gufi_vt search setting keeps its earlier path.
- The assistant's `search_kb` uses `searchScope`'s fused order directly.
- The Search page labels a folder-path match "folder", and the label's tooltip names every way a hit was found.

## Measurements

On a synthetic index of 24,000 directories and 100,000 files, words in a quarter of the directories and vectors in a tenth, warm cache, median of five, on a shared host (load average 1 to 10):

| Search | Before | After |
|---|---|---|
| Names, words and meaning | about 2.9 s (the name and word walk, 0.7 to 0.8 s, beside the meaning search, 2.9 s: a file walk for databases with vectors, then one attach each) | about 1.0 s in one walk |
| Names, words and folder paths, no vectors | 0.7 s (names and words) | 0.85 s |
| Same index rolled up (1 directory left to walk) | 3.0 s | 0.24 s, names and paths only (see below) |
| Four libraries of 6,000 directories | 1.17 s | 0.83 to 0.85 s, the same stacked or one walk each |

What each part adds to the walk: folder-path matching about 0.15 s, vectors about 0.1 s, the external database lookup about 0.05 s.

Remote, by round trips: one per host per search, with meaning included where the remote index has vectors. Before, it was one per library, with no meaning.

## Notes

- A rolled-up index's walk does not visit directories rolled into their parents, so their own tables are not read there; names and paths are. The Studio does not roll up the indexes it builds.
- Empty tables were measured and not written. An empty fts5 `words` table takes a 48 KB directory database to 68 KB, and empty vector tables take it to 108 KB. That is about 0.4 GB and 0.8 GB more on this test index.

## Tests

- `server/test/searchOnePass.test.mjs`, with real GUFI where installed:
  - RRF order and ties;
  - statement order;
  - a tree with words in some directories only;
  - folder-path matches below the root only;
  - meaning in the same walk, and a model with no query vector failing alone;
  - the shared external schema by text and by model, with a stale mtime not matched;
  - several roots in one walk mapped back.
- `server/test/searchStackedRemote.test.mjs`: two libraries on one host give one remote `gufi_query` with both roots, another host has its own, and a failing host names its library.
- Updated fakes in `libraryScope.test.mjs`.
- Web: the folder label and the tooltip.
- Server suite: 619 passed, 9 skipped, including the restricted-host test (#594), whose search sends only gufi_query. Web suite: 278 passed.

### Query: text conditions run through gufi_vt; an unbuilt main folder is "not indexed yet" (#597)

Two Query issues found by the macOS end-to-end test on v1.174.

**Text conditions fell back to the merge.** A Query across libraries with a "has the words" condition answered with `engine: "merge"`, while name and size conditions used gufi_vt. gufi_vt takes each SQL stage as a double-quoted value (`E="..."`) read verbatim, so `fitsVt` refused any stage holding a double quote, and a text condition always holds them: #583 writes its FTS5 phrase query as `wordf MATCH '"a" AND "b"'`.

- `vtStage()` in `server/src/gufiVt.ts` writes each double quote inside a single-quoted string as `'||char(34)||'`, wrapping the literal in parentheses. The string's value is unchanged: FTS5 matches the computed phrase string the same as the literal (checked with gufi_sqlite3). A double quote outside a string (a quoted identifier, a comment) or an unterminated string still leaves the query to the merge.
- `vtScript` passes every stage through it, so remote locations' command lines carry no inner double quotes either.
- A Query across libraries answered by the merge now carries `mergeReason`: a double quote outside a string, a library path gufi_vt cannot be given, no gufi_vt with remote locations, or the gufi_vt statement's error.

**An unbuilt main folder.** The main folder counted as indexed whenever its index folder existed (`gufiAvailable()`), including the empty folder that exists before the first build. Search then found no databases and named nothing, while Query ran gufi_query, failed, and listed it as "index not found".

- The main folder now counts as indexed once its index has a `db.db` at the root, as `indexStatus().indexed` already does.
- `libraryGap` says `not indexed yet` for the main folder before its first build, and keeps `index not found` for one built before whose index is gone (indexing.ts reports whether the last build of the main folder finished).
- Search lists it in `omitted` and does not ask it; Query across libraries lists it the same way, and asked alone answers 409 `<label> cannot be queried: not indexed yet`.

**Tests**
- `gufiVtSpaces.test.mjs`: `vtStage` rewrites (phrase quotes, a doubled single quote, a quoted identifier, a comment, an unterminated string); with real GUFI, a words condition across a local library and one through the remote wrapper answers `engine: "gufi_vt"` with the rows the merge gives; the merge carries its reason.
- `omissions.test.mjs`: with real GUFI, search and Query name a never-built main folder `not indexed yet` (its empty index folder present), and `index not found` once built and its `db.db` is moved away; nothing is named once it is back.
- `querySql.test.mjs`: its stand-in main index gets a `db.db`, as a built index has.
- `deploy/mac/check_app.sh`: after the existing gufi_vt check across two libraries, a Query with a words condition on the OCR sample word must answer `engine: "gufi_vt"` with the sample in its rows.
- Server suite 614 passed, 9 skipped; web suite 277 passed.

`docs/REFERENCE.md` describes both.

### Change log through v1.175 (#598)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.175` before tagging v1.175.

## v1.174 (2026-10-24)

### A remote build that ends as its status is read counts as finished (#592)

The remote build tests (server/test/remoteBuild.test.mjs) failed now and then, a different test each time. On this machine it was about 1 run in 20, and more on a loaded one. The cause is a race in the product, not the tests.

**The race.** BUILD_STATUS checked for the run's exit file, then whether the run's process was alive. A build that ended between those two reads had no exit file at the first and no process at the second, so it was reported as "the build stopped without finishing". In the captured failure, the remote state folder showed phase `done` and exit `0` while the library was marked failed. A first build ended as failed. A rebuild kept the old index and showed the error, though the build had succeeded. The later tests in the file failed in turn, because they use the first build's library.

**The fix.** BUILD_STATUS reads whether the run is alive before reading its exit file. The run writes its exit file as it stops, so a run found stopped has written it. A run that stops between the two reads counts as running and is read as finished at the next look.

**A related case.** A process id means something only on the machine that started the run. Where one host name reaches several login nodes, a status read landing on another node took a running build for one that stopped. The run now writes `uname -n` to its state folder, and a status read from another machine waits for the exit file instead of judging by the process. Builds started before this keep being judged by their process.

**Tests:**
- A new test stands in for `kill` in the status script, so the run writes its exit file and stops exactly as its liveness is read. It fails on the old order (`gone`, "the build stopped without finishing") and passes now.
- A second test covers the machine rule: gone on its own machine, running as seen from another, and an older build without a host file judged by its process.

**Results:**
- Before the fix: 1 failure in 20 runs of the file here.
- After the fix: 0 in 30 runs, and 0 in 15 more with other test files loading the machine.
- Server suite: 592 passed, 9 skipped. Web suite: 277 passed.

### Outbound HTTPS trusts the system's certificate authorities and follows the proxy (#593)

On a network that inspects TLS with its own certificate authority, every call the server made out failed with "unable to get local issuer certificate": Settings, Updates showed "The check for updates failed: unable to get local issuer certificate.", and the update download, model providers, the platform API and MCP servers failed the same way. Node trusts its bundled list of authorities, not the ones the computer trusts, even when macOS trusts the network's authority.

**Trust.** The server's first import (`netStart.ts`, from `netTrust.ts`) adds the system's trusted certificates to Node's own with `tls.setDefaultCACertificates`.
- This is the same set `node --use-system-ca` reads: the keychains' trusted roots on macOS, including an authority an administrator or the Mac's management installed, and the system bundle on Linux.
- Certificates named by `NODE_EXTRA_CA_CERTS` are kept. `STUDIO_SYSTEM_CA=0` turns it off.
- It runs at runtime rather than through a Node flag, so the macOS app, the container, the hosted bundle and a clone started with `pnpm start` all get it the same way. All of them start `dist/main.js`.

**Proxy.** With `HTTPS_PROXY` or `HTTP_PROXY` set (either case), requests go through the proxy via `http.setGlobalProxyFromEnv`, which covers `fetch`.
- `localhost`, `127.0.0.1` and `::1` are added to `NO_PROXY`.
- Credentials in the proxy URL are sent as Basic auth.
- The macOS run loop (`studio-run.sh`) exports the system proxy from `scutil --proxy` (web and secure web proxies, and the exceptions list) before each start when `studio.env` sets none.
- A PAC file is not followed, and the log names it.
- `studio-run.sh --system-proxy` prints what it would set.

**Messages.** `net.ts` gains `trustFailure`, `proxyFailure` and `connectionProblem`. The update check, the update download, the model gateway and its health check, the platform routes and MCP servers use them before their offline handling.
- An untrusted chain reads: "The connection to api.github.com used a certificate this computer does not trust (unable to get local issuer certificate). If your network inspects HTTPS, its certificate authority has to be trusted by macOS (in Keychain Access, or installed by the Mac's management); the Studio uses the system's trusted certificates." The wording differs for Linux and Windows.
- An expired certificate, a certificate for another name, an unreachable proxy, and a proxy that refuses the tunnel (`Proxy response (407)`, which is named as a sign-in request) each get their own sentence.
- None of them counts as offline. The update check reports `state: "error"`, not a quiet retry.
- The server logs one line at start about what it trusts and which proxy it uses.

**Tests**
- `server/test/outbound.test.mjs` runs a local HTTPS server with a certificate from a throwaway authority, and a local CONNECT proxy. Each request runs in a child Node, starting from Node's own list:
  - plain Node refuses the certificate, with the plain message;
  - on Linux, adding the system store (`SSL_CERT_FILE`) makes it work, and `STUDIO_SYSTEM_CA=0` turns that off;
  - `NODE_EXTRA_CA_CERTS` is kept;
  - requests go through the proxy, localhost goes direct, and a given `NO_PROXY` is kept;
  - a 407 proxy and an unreachable proxy get their messages;
  - each certificate code maps to the right kind, and the `NO_PROXY` matching works.
- `updates.test.mjs`: a release server behind an untrusted authority gives the error sentence with `offline: false`.
- `macApp.test.mjs`: with a stand-in `scutil`, the run loop gives the server the system proxy, `studio.env`'s own proxy wins, and the PAC line is logged.
- `deploy/mac/check_app.sh` (macOS, with passwordless sudo, as on the runner):
  - adds a throwaway authority to the System keychain with `security add-trusted-cert -d`;
  - shows the app's Node refusing a certificate from it on its own, and accepting it with the system's certificates added;
  - points the run loop's server at a release server behind it through `STUDIO_UPDATE_URL`, and requires the update check to succeed;
  - removes the authority again.
  It skips with a note where sudo needs a password.

Full suites: server 600 passed with 9 skipped; web 277 passed.

Not covered: image descriptions, which `indexer/enrich.py` requests from the gateway with Python's `urllib` when AI features and a vision model are on. Python uses its own certificate list. `fetch_embed.py` downloads only at build time. Both are noted in REFERENCE.md.

### Libraries over ssh work where the login runs only gufi_query (#594)

A user connecting an existing GUFI index read only over ssh got "bash is not installed on user@host; the Studio runs its commands there through bash." The host's login runs nothing but `gufi_query`: some sites give no bash, a restricted shell, or a forced command. A connected index needs nothing else there, so this makes the Studio work with `gufi_query` alone.

**How it works.** The session loop (`remoteExec.ts`) needs bash. When the login refuses it, the site is marked direct:
- Refusal means any exit but ssh's own 255 over plain ssh, or a "not found", "restricted" or "not allowed" message over pw ssh.
- Each command is then its own ssh call over the shared ControlMaster connection, written as one command line. The program name stays bare, so a forced command that checks for a leading `gufi_query` finds it, and each other argument is a POSIX shell word.
- Only `gufi_query` and `gufi_sqlite3` are sent. Anything else is refused in the Studio with "<host> runs only gufi_query for this login (no shell), so this cannot run there."
- The library is saved with `shell: false`, so after a restart its first command is `gufi_query`, not another try at the shell.

**Remote commands for a connected library, and how each runs now:**

| What | Before | Now |
|---|---|---|
| Connection test: reach the host | `sh -c 'uname -s; echo $HOME'` in the loop | the same where a shell runs; refused, the host is marked and the test continues |
| Find and run GUFI | `sh -c` search, then `<bin>/gufi_query --version` | the same with a shell; otherwise `gufi_query --version` from the login PATH, or the bin directory given |
| Index check | `sh -c` test -d, db.db, stat | the same with a shell; otherwise the aggregate `gufi_query` count, whose stderr says what is wrong |
| Source directory check | `sh -c` test -d -r | the same with a shell; otherwise a note that files cannot be opened |
| Tree | a bash script running `gufi_query` twice | two `gufi_query` calls side by side |
| Details, statistics, search (names, and the one-walk words search) | `gufi_query` | unchanged |
| Query through gufi_vt | a `bash -c` test -x/test -d pre-check, then the wrapper's `gufi_query` | a one-row `gufi_query` pre-check, then the wrapper's `gufi_query` |
| Processor count | `sh -c nproc` | not read on such a host; 4 threads, or `STUDIO_REMOTE_THREADS` |
| File preview and download | `bash -c` stat, head, cat | refused there with the reason |
| Build check | `sh -c` | refused at its ssh step: "This host runs only gufi_query: connect existing indexes read only; building an index there is not available." |

**Test connection on such a host:**
- ssh passes with that note, as a warning.
- Then "gufi_query <version> on the login's PATH" (or in the given directory).
- Then the index count.
- With no shell, `~` cannot be resolved there, so paths must be absolute, and the test says so.
- A restricted shell refuses a path to a program, and the message says to leave the GUFI bin directory empty.
- The test is ordered session first, then `gufi_query --version`. The opposite order would add a separate call to every host: about 5 s over pw ssh, and on macOS hosts a call without the GUFI library path.

**ssh stderr notices** (the post-quantum key exchange warning, "Permanently added") are dropped from messages and never fail a step.

**Words on connected indexes:** the test also looks for the Studio's `words` tables in the index's top levels. Where it finds them, the library is saved with `fullText`, so search covers the words in its files, with or without a shell.

**Kerberos:**
- The Studio runs the system ssh with the account's `~/.ssh/config`, and its options (`-T`, `BatchMode`, `ConnectTimeout`, `ServerAliveInterval`, ControlMaster) do not turn GSSAPI off. `BatchMode` only refuses prompts.
- On macOS the server runs in the user's session, both under the LaunchAgent (gui domain) and when the app starts it, so it finds the default `API:` credential cache.
- The run loop now also passes on a `KRB5CCNAME` that the login shell sets, captured with the login PATH it already reads, unless one is set already. That is the only `studio-run.sh` change.

**Tests:**
- New `server/test/restrictedRemote.test.mjs`. Its fake host's login logs each command, writes the post-quantum warning on stderr, and runs a command only when its first word is `gufi_query`, refusing paths as rbash does. It covers:
  - the test's steps and messages;
  - each failing step (a `~` path, a missing index, a folder without `db.db`, a refused bin path);
  - adding the library;
  - the tree, Details, statistics, search by name, and search by words with a `words` table;
  - Query through gufi_vt across the main index and this host;
  - refusing files;
  - Check;
  - a restart, after which nothing but `gufi_query` reaches the host;
  - a refused build check;
  - a host with a shell but no bash;
  - a pw ssh cluster without bash;
  - shell word quoting read back by /bin/sh.
- Existing remote tests pass unchanged.
- Server suite: 598 passed, 9 skipped. Web suite: 277 passed.

**macOS check:**
- `check_app.sh`, on CI runners with Remote Login, adds a localhost host. A test key's `authorized_keys` line forces a wrapper that runs only `gufi_query` lines.
- Through the app's own server, it checks:
  - the connection test answers `shell: false`;
  - the library adds;
  - Query across libraries reaches it through gufi_vt;
  - nothing but `gufi_query` ran there after the first refused session.
- The key line and the alias in the runner account's `~/.ssh/config` are removed afterwards. Elsewhere the check is skipped.

Not covered: a macOS host whose GUFI tools need `DYLD_LIBRARY_PATH` and whose login runs only `gufi_query`. With no shell or env, the Studio cannot set the variable; GUFI built with rpaths runs.

### Change log through v1.174 (#595)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.174` before tagging v1.174.

## v1.173 (2026-10-24)

### Chat: the My chats / All chats switch sits at the top of the conversation list (#586)

With shared chat history on a multi-user deployment, the My chats / All chats switch was absolutely positioned at the chat area's bottom-left corner, which put it over the conversation sidebar's collapse button.

It now renders in ChatLayout's sidebarTop slot, under Select chats and above the list it filters. It flows with the list in the inline column and in the phone drawer, so the phone-only overlay rules are gone, and it is left out on the collapsed rail.

Checked in headless Chromium at 1300 px with multi-user mode on: the switch sits inside the sidebar, does not overlap the collapse control, and nothing covers the collapse button. Web suite passes.

### Query across libraries uses gufi_vt in the macOS app (#587)

In the macOS app, a query across libraries never used gufi_vt. `server/src/gufiVt.ts` refused any path with whitespace (`SAFE_PATH = /^\/[^'"\s\0]*$/`), and every path there has a space: node and the wrapper inside `ACTIVATE Studio.app`, the index under `Application Support/ACTIVATE Studio`. `gufiVtAvailable()` returned false, and `POST /api/query` always answered `engine: "merge"`. A macOS end-to-end run of v1.172 showed exactly that. The rows were the same, but the mechanism was not the one we describe.

**How gufi_vt uses each value**, at the GUFI commit the app bundles (`1713537`):
- `popen_argv.c` runs every command with `fork` and `execvp(argv[0], argv)`, never a shell.
- A local location: `gufi_query` with each flag, stage SQL and index root as its own argv entry.
- A remote location: `argv = [remote_cmd, remote_arg..., line]`, where `line` is the whole gufi_query command as one string. Stage SQL is double-quoted, and the index roots are appended unquoted (`"%s "`).
- `set_refstr` in `gufi_vt.c` strips a value's leading and trailing quote characters and does not undo doubled ones. So `'it''s'` reaches gufi_query as `it''s`.

Checked by hand with GUFI's `gufi_sqlite3`:
- a double-quoted index with a space and a single quote works;
- a single-quoted one with the quote doubled fails ("Could not stat directory .../it''s ...");
- commas and parentheses in a path work;
- `remote_cmd` and `remote_arg` with spaces reach the command as single arguments.

**The change**
- `vtPath()` quotes a path for gufi_vt instead of refusing it: single quotes, or double quotes when the path holds a single quote. It returns null for a path holding both quotes, one starting or ending with a quote (gufi_vt would strip it), and a NUL or line break. Spaces need no escaping, since nothing goes through a shell.
- `vtScript` quotes `index`, `remote_cmd` and `remote_arg` with it. `gufiVtAvailable()` checks node and the wrapper with it.
- A remote location's index reaches the wrapper unquoted at the end of gufi_vt's line, and the wrapper splits on spaces. `checkRemoteArgs` now joins the trailing words back and requires that they spell the token's index exactly. `vtRemoteIndex()` allows single spaces but no other whitespace, and no word starting with a quote.
- `vtLocatable(lib)`: when a library's path cannot be given to gufi_vt, `queryScope` merges with that library included. Before, `runAcross` reported the library as failed and left it out. This is the one change in `query.ts`.
- The command gufi_vt runs for a remote location is held in one place, with a test hook to point it at the app's layout.

I chose quoting over symlinks into a private runtime folder. gufi_vt passes these values as argv, so exact quoting is enough and leaves the GUFI commands panel showing the real paths.

**Tests**
- New `server/test/gufiVtSpaces.test.mjs` builds the app's layout with real GUFI:
  - `.../ACTIVATE Studio.app/Contents/Resources/{gufi/bin, node/bin/node, app/server/dist/gufiVtRemote.js}`, with `KB_ROOT` and `INDEX_BASE` under `Documents/ACTIVATE Studio` and `Application Support/ACTIVATE Studio`;
  - a local library at `other idx/local it's`;
  - a remote library (a local bash session) at `cluster idx/it's proj`.
- It checks:
  - `gufiVtAvailable()` is true;
  - a query across the three answers `engine: "gufi_vt"` with the same rows as the merge;
  - a fourth library whose path holds both quotes sends the query to the merge with its rows included;
  - with gufi_vt forced off, the query still merges.
- It also unit-tests the quoting rules and the remote index join.
- Server suite: 573 passed, 7 skipped. Web suite: 253 passed.
- `deploy/mac/check_app.sh`, in its server run from the app's `studio-run.sh`, indexes the corpus again into `second idx's` and adds it as a library. It then requires `POST /api/query` across all libraries to answer `engine: "gufi_vt"`, with rows from both and nothing failed.

`docs/REFERENCE.md` says when Query uses gufi_vt and when it merges.

**A second cause, found by the macOS build job.** Even with the paths fixed, the app's server still merged. `check_app.sh`'s diagnostics showed why:
- the app's `gufi_sqlite3` has the gufi_vt modules, and a local location works;
- the availability probe failed with `Failed to exec command: No such file or directory`.

The probe's `remote_cmd` was `/bin/true`, and macOS has only `/usr/bin/true`. The probe now runs whichever of `/usr/bin/true` and `/bin/true` exists. With both fixes, the macOS build job's server run passes "a query across two libraries runs through gufi_vt".

### No library drops out of search or Query silently (#588)

A user reported that after their Mac restarted, a read-only library of about 12,000 files dropped out of their results until they chose its index depth again in Settings. #581 fixed the two causes. This makes the class of problem visible and harder to reach: nothing told them a library was left out.

**Every response names what it left out.** `GET /api/search`, `POST /api/query` and the assistant's `search_kb` carry `omitted` when a library in scope was left out, or searched only in part: `{ id, label, reason, partial }`, one per library (`server/src/omissions.ts`). Reasons:
- `being built; searchable once its first pass is done`
- `its build failed: ...`
- `index not found`
- `words not searched yet: its text is being read`, and `words not searched: its index has no text yet`
- `not searched by meaning: its index has no vectors yet`
- `did not answer within 20 s`
- `not reachable over ssh: ...`
- `search failed: ...`
- on macOS `macOS has not allowed ACTIVATE Studio to read <folder>; allow it in System Settings, Privacy & Security, Files and Folders (or Full Disk Access)`, elsewhere `permission denied reading <folder>`; `a folder could not be read (...)` for folders inside a library.

Query adds `its index has no extracted text, so text conditions match nothing there` when it reads the `words` table. A library with nothing to answer from is not asked: across libraries it is in `omitted` (and Query's `failed`), and alone Query answers 409 `<label> cannot be queried: <reason>`. Search failures were swallowed before (`() => none`); they are now named.

The Search and Query pages show one line above the results. Each name opens the library's row in Settings, Libraries and highlights it, whether that page is already open or not. Settings, Libraries shows a built library whose index is gone as "Index not found" instead of Ready.

**What a library holds is kept, not forgotten.** Capabilities (`index`, `fullText`, `vectors`) are kept in `INDEX_BASE/library-caps.json`. `forgetProbe` is replaced by `staleProbe`, which marks them stale and probes again but never resets them to none. A probe that can open none of its sampled databases keeps the last known capabilities, marked stale with the error, and retries within 30 s. A restart reads them before any probe. Whether the index exists is looked at on every listing, so a removed index is named at once. Each library's state goes to the index log at start.

**A stop resumes.** SIGTERM now ends the programs of index jobs and library builds (`stopLibraryJobs`, `cancelAllIndexJobs`), and an interrupted library build keeps `building` or `updating` for the next start. A change of depth cut off part way (`pending: "depth"`) is finished by a sync, not a rebuild. Library build programs now run in a process group of their own (spawn, since `execFile` drops `detached`), and a stop or time limit ends the group. Before, `enrich.py`'s worker processes outlived a stopped build, held its output open, and the build never finished.

Measured on 3,000 files with 300 images to OCR, 20 cores:

| Run | Time |
|---|---|
| Cold build | 5.4 s |
| Stopped at | 2.7 s, 152 of 300 OCR results cached |
| Resumed at the next start | 3.2 s, reading only the rest |
| Warm rebuild | 0.8 s |

**Folders the Studio may not read** (EPERM, which macOS answers until the app is allowed; or EACCES) are named by the folder scan, `gufi_dir2index` and `enrich.py`. They are recorded in `denied.json` per library and named in search and Query. `gufi_dir2index` exits 1 when it cannot open a folder, after writing the rest of the tree. Until now that failed the whole build; when every error is a permission error, the build now stands. A sync keeps an unreadable folder as last indexed instead of removing it.

Tests:
- `server/test/omissions.test.mjs` (14), with real GUFI:
  - each reason;
  - a library being built;
  - capabilities across a stale probe, an unreadable index and a restart;
  - a depth change not yet applied;
  - the deadline;
  - a vanished index;
  - a locked folder (chmod 000 standing in for EPERM) in a sync and a rebuild;
  - a stop that leaves nothing running and resumes;
  - the start-up line.
- Web: the line, its buttons, the Search and Query pages, the row focus, and "Index not found".
- Server suite: 580 passed, 7 skipped. Testdata: 21 passed. Web suite: 262 passed.
- Checked in headless Chromium at 1300 and 740 px on a test server with a missing index, a library without its text yet, and a locked folder.

Only a real Mac can confirm:
- that macOS answers EPERM for Files and Folders denials as Linux answers EACCES here;
- which process macOS asks about. In the no-LaunchAgent mode the server is started by the app, so the request should come from the app. Under the LaunchAgent, launchd starts `/bin/bash studio-run.sh`, which runs node, so the request may name node instead of the app.

### Updates: checked every 15 minutes, shown when the window opens, and checked from the footer and the app menu (#589)

User feedback on the macOS app: with a new version published, the footer said "update available" but no notice appeared, and checking meant going to Settings, Updates.

**Why the notice did not show.** With a faked status in a browser, the notice and the footer read the same state and both appear on every page, at 1300 and 740 px, and a Later stored for an older version does not hide it. The one thing that hides the notice while the footer still says "update available" is a Later for that same version, which lasted until a newer version came out. Later now lasts a day, and a Later stored by an earlier Studio (a bare version string) hides nothing.

**Cadence.**
- Server: a check a minute after start, then every 15 minutes by the clock (5 while a new release waits for its macOS build, or while offline), looked at every two minutes. That is at most four requests an hour to GitHub's unauthenticated API (60 an hour per address), most answered 304 against the stored ETag; a 403 or 429 already waits out the interval.
- Page: reads the status every two minutes instead of ten, and when the window is shown or focused (at most every 10 s). A read that finds the server checking reads again until the check ends, so a version found while the Mac slept shows within seconds of opening the window.

**Footer.** Beside the version:
- nothing known: "check for updates", which checks now, shows "checking…", then "up to date" for a few seconds (or "check failed", with the error on hover);
- a newer version: "update available", which now opens the Build panel instead of Settings;
- on the collapsed rail, a dot marks a newer version and opens Settings, Updates;
- a hosted deployment shows neither.

**Build panel.** A new updates section: "Up to date" with when it was last checked and Check now; or "Version X is available" with Update now (the app), the update commands (a clone), Changes and Check now; "Checking for updates…" while a check runs; the error after a failed check; the install's progress during an update. The deployment note ("Compare this against the release you deployed…") and GET /api/version show only for a hosted deployment, and in the app the source reads "stamped when the app was built".

**macOS app menu.** Check for Updates… under About opens Settings, Updates and starts a check (the page's update flow listens for an event the shell sends). A server test keeps the menu item and the event name in step.

**Tests.**
- Server: the default 15-minute cadence (not due at 14 minutes, due at 16, with the clock moved), and the menu wiring.
- Web: Later lasting a day; an expired Later, a Later for an older version and a legacy Later not hiding the notice; the re-read on focus and visibility, following a check to its end; the check event; the footer's check control, up to date, and becoming "update available"; "update available" opening the Build panel; the panel's up to date, checking, failed and hosted states; the rail dot.
- Server suite: 568 passed, 7 skipped. Web suite: 260 passed.
- Checked in headless Chromium with faked statuses at 1300 px (available, up to date, checking, failed, hosted) and 740 px (through More).

Docs: HELP.md (how updates appear and how to check), MACOS.md and REFERENCE.md (cadence), and the Settings, Updates description.

### Semantic map: 3D zoom that magnifies, links between clusters and files, word names, compact legends (#590)

**3D zoom.**
- Cause: `project3d` computed `f = dist / (dist + z2)`, and zoom only changed `dist`, clamped to 1.05 to 8. Points on the centre plane (`z2 = 0`) kept `f = 1` at any distance, so zooming changed how strong the perspective was, not the size. `onDetail` got at most 2.6 / 1.05, so detail loading barely started.
- Now the camera keeps its distance and magnifies: `k` runs from 1 to 40, as in 2D. Zooming moves the camera's target toward the pointer, so a point under the pointer stays put (`zoom3d`).
- The map turns about that target, so it turns about what you zoomed into, and dragging with shift or the right button moves it (`pan3d`). Back at `k = 1` the map is centred again.
- Wheel, pinch, + and -, the buttons and double-click all use it.
- Zooming into the 3D overview loads more of the clusters in view, up to `CLUSTER_CAP_3D` (6,000) files.
- Dots grow a little with zoom, up to twice their size.

**Links.**
- `semantic_map.py` links each cluster to the two clusters whose mean vectors, in the full 384 dimensions, have the highest cosine, each pair once. They go in `overview.json` as `links: [{a, b, sim}]`, overview version 3.
- The map draws them as a light gray skeleton under the dots, heavier for nearer pairs. A hovered cluster, on the map or in the legend, shows its own links in its color and dims the rest.
- A "Show links" / "Hide links" button is on by default in 3D and off in 2D, and is kept per viewer in localStorage.
- Maps made before this have no links and draw without them.

**Links from a file.**
- Hovering a file, after 220 ms, draws lines to its five nearest files by meaning, with a ring where a neighbor is not among the dots loaded. Answers are cached per file and dropped when the map changes.
- They come from a new route, `GET /api/kb/semantic-map/neighbors?file=<path>&k=5`:
  - It averages the file's own passage vectors and normalizes them, and every directory whose vectors come from the same model (`gvecmodel`) answers through sqlite-vec, as a search by meaning does.
  - It returns `p`, `sim` (the cosine, since the vectors are unit length) and the neighbor's place on the map.
  - It returns an empty list with a reason where the file has no vectors, and a 400 for a path outside the main folder.
  - Answers are kept until the map is made again.

**Cluster names.**
- Clusters are named by their distinctive terms: the best two terms, or three where they fit in 26 characters. They come from the files' passages (`gchunks.ctext`, the text the vectors were made from), scored by TF-IDF against the whole map.
- Each term must appear in at least 5% of the cluster's files, and in two files where the cluster has four or more.
- Names are unique across clusters: a term another name uses goes behind unused ones, and a taken name takes the next term.
- A cluster with fewer than two such terms keeps a folder name. That name now keeps the parent where two folders share a name (`pw/docs + gs/docs`).
- The map's labels also keep folder names apart. "docs + docs" came from the web cutting each part to its last folder.
- The folder name stays available on hover, in the legend and the cluster tip.
- Old maps keep their names.

**Legends.** Both maps' legends show their eight largest entries, then "+ N more", which opens the rest in place and becomes "Show fewer". Hovering an entry highlights that directory or cluster on its map and dims the rest. Clicking it opens it, as clicking it on the map does.

**Checked.**
- **Corpus:** generated, 330 files on eight topics, built and embedded with the ONNX backend, then viewed in headless Chromium at 1300 px, 740 px and full screen.
- **Names it produced:** "coral, current, estuary", "bake, bread, crust", "audit, bracket, credit". The two wind clusters, both under `energy/turbines`, are named "anemometer, blade, farm" and "rotor, gearbox, generator", and the two halves of split topics are the strongest links (0.98 and 0.99).
- **Frame time, 6,000 points turning in 3D:**

| Run | Frames | Draw, median | Draw, p95 |
|---|---|---|---|
| Links on | 60 fps | 2.9 ms | 4.4 ms |
| Links off | 60 fps | 2.9 ms | 4.3 ms |
| Zoomed 10x, links on | 60 fps | 4.3 ms | 5.8 ms |

**Tests.**
- **server `semanticMap.test.mjs`:**
  - unique names and the folder fallback with numbers on the existing fixture;
  - the links (pairs once, each cluster linked, nearest first, the strongest joining two halves of a topic);
  - the TF-IDF terms (a word in every file names nothing, each cluster named by its own words);
  - `name_clusters` uniqueness and the one-term fallback;
  - the parent kept in folder names;
  - the neighbors route against real `vec0` tables made with GUFI's sqlite3: nearest first, across folders, one model only, with places, cached, and the error cases.
- **web `corpusMapsView.test.tsx`:**
  - zoom math (the point under the pointer stays; a point on the same plane moves away by the zoom factor; 1 to 40; recentred at 1);
  - pan;
  - short labels;
  - links on and off per view and kept;
  - no links for an old map;
  - lines and a ring from a hovered file;
  - both legends folding, opening and highlighting.
- Server suite: 571 passed, 4 skipped. Web suite: 261 passed.

Docs: HELP.md (the maps) and REFERENCE.md (names, links, the neighbors route).

### Change log through v1.173 (#591)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.173` before tagging v1.173.

## v1.172 (2026-10-24)

### A built library stays searchable and queryable after a build, a restart, and a rebuild (#581)

A user reported that a read-only library built from a folder on their Mac, at Text + OCR, dropped out of their searches after the Mac restarted, and came back after they changed its depth in Settings. Two causes, both in `server/src/libraryBuild.ts`.

**Capabilities forgotten after every build or sync.** `settle()`, which ends every build and every sync that changed something, called `forgetProbe()` on the library's index. Until the next probe, `fromDef` reports `fullText: false` and `vectors: false`, so `wordsSearched` and `meaningSearched` leave the library's words and vectors out of search. In practice the next probe came from `GET /api/libraries`, that is, from opening Settings, Libraries, which is why choosing the depth there brought the library back. The server probes every library at start, so a plain restart was fine; the loss followed the first build or sync after it. `settle()` now probes the index again right after forgetting it.

**A rebuild swapped in an index with names only.** A build that a restart interrupted (state `building` or `updating`) runs again from `startLibrarySync` at start. `fullBuild` wrote the new tree beside the old one but swapped it in before reading the text, so the library's words were missing from search and from text queries until the text pass finished, which on a large folder with OCR takes a long time. Where the library already has an index, the new tree now gets its text in the staging folder before the swap. A first build is still swapped in at once, so its names answer while the text is read.

**What each cause did to Query.** The Query page, across every library, takes its libraries (and its gufi_vt locations) from `scopeLibraries('all')`, which keeps a library whose `caps.index` is set: a probed value, else whether `db.db` exists at the index root. It never reads `fullText` or `vectors`, so the forgotten probe left Query whole: size, name and words queries all returned the library's rows. The rebuild was the one gap in Query: during it a words query returned no rows from the library and listed no failure, while size and name queries were unaffected. Measured on the code before this change, with the same test reporting each stage:

| Stage | Search, words | Query, size | Query, name | Query, words |
|---|---|---|---|---|
| after the build | 0 of 12 | 12 | 12 | 12 |
| after a restart | 12 | 12 | 12 | 12 |
| while an interrupted build runs again | 0 | 12 | 12 | 0 |
| after that rebuild | 0 | 12 | 12 | 12 |

With this change every cell is 12, and the test asserts it.

Remote libraries (ssh and pw ssh) are not affected: their definitions and capabilities are stored with them and read on each request, and gufi_vt's location tokens are made per query.

Tests: `server/test/libraryRestart.test.mjs`, with GUFI's real tools:
- a built library is searched by its words and queried right after its build (failed before: its words were left out);
- after a restart (passed before too: the start-up probe);
- while an interrupted build runs again, with the text pass held open (failed before: no word hits while it ran);
- after that rebuild, with no visit to Settings;
- an existing index connected as it is stays in search by name and in Query across a restart.

Server suite: 521 passed, 7 skipped. Web suite: 239 passed. `docs/LIBRARIES.md` says what a rebuild keeps answering.

### Search walks each library once and ranks text matches by bm25 (#582)

From user feedback on search over large GUFI indexes.

**One walk per library.** Name and full-text search of a library were two `gufi_query` walks of its index, and for a library on another system two remote commands. They now run as one aggregate `gufi_query` (`-a 1`). Its per-directory step (`-E`) looks up the names and then the words while that directory's database is open. Search by meaning still runs apart, through `gufi_sqlite3`. `searchWordsAndNames` in `server/src/gufi.ts` does both kinds; `searchFts` and `searchNames` are built on it, and the Search page, the assistant's search tool and the chat context all use it.

**Best rows, not first found.** Each kind keeps its best rows where the index is:
- Word matches are ranked by SQLite's `bm25` over the directory's fts5 `words` table. Each thread keeps its best rows after every directory (`DELETE ... OFFSET n`), and the merge returns the best of the whole tree. `bm25` is computed within each directory's table, so scores from two directories compare roughly.
- Name matches are ranked by how closely they match: the query as the name (with or without an extension), then names starting with its first word, then names containing a term, then by path.
- Across libraries each kind is merged in rank order. Hits carry `rank`, lower first within its kind.
- The `gufi_vt` search path orders each directory's rows the same way.

**Measured** on a synthetic index of 102,000 files in 3,032 directories, a quarter of them with words rows, local with a warm cache, median of seven:

| Search | Before | After |
|---|---|---|
| One library, names and words | 145 to 153 ms | 115 to 117 ms |
| One library, a word in most documents | 211 ms | 202 ms |
| Two such libraries | 281 to 297 ms | 199 to 218 ms |

For the very common word, ranking every match costs part of what the single walk saves. There was no ssh to localhost here to time a remote library; for one, the change halves the remote commands per search.

**Several paths in one run.** `gufi_query` takes several index paths and walks them in parallel in one thread pool. Two paths in one run took 132 ms, two runs side by side 136 ms, and one path 75 ms. It is no faster, so each library keeps its own run, which also keeps its own deadline and `slow` report.

**Parity.** With limits high enough that nothing is cut off, the new search returns the same files as the old queries. This was checked for ten queries on the synthetic index (up to 20,116 word matches and 5,403 name matches), and the new tests check it against the old SQL on a fixture.

Tests (`server/test/searchRanking.test.mjs`, which needs GUFI and a Python with fts5):
- one `gufi_query` per search;
- a document mentioning the term twenty times ranks above thirty that mention it once, though the walk meets those first;
- name order;
- parity with the old queries;
- an index without words tables still answers names, and a names-only search does not touch the words;
- order across libraries.

The fakes in three existing tests now answer the tagged rows. The ssh deadline test passes unchanged. Server suite: 525 passed, 7 skipped. Web suite: 239 passed.

Docs: REFERENCE (how search runs, ranking, the measurements, several paths) and HELP (result order).

### Query builder: conditions joined by all or any, presets, and dates that work (#583)

The builder's "equals" on a path and on a modified date matched nothing, and every modified comparison was wrong:

- A path in a directory's WHERE clause is absolute (rpath includes the index's own folder), so a relative path never equaled it, and "path contains" matched the index folder's own name.
- GUFI registers its own `strftime(format, seconds)` in place of SQLite's, so `strftime('%s', '2026-01-01')` read the date as the number 2026 and `strftime('%s', 'now')` read now as 0. Modified before/after/equals gave wrong rows, and the canned "changed in the last N days" query returned every file.

Server (`server/src/query.ts`):

- Dates are worked out by the server: on, after and before a day (`2026-01-01`), a minute or second, or a time ago (`7 days`, `12 hours`, `2 weeks`), in UTC as results show times. The canned recent query computes its cutoff the same way.
- Paths compare relative to the library: equality against the whole path, contains below the index's folder.
- Sizes take units (`10 MB`, `1.5G`, `500k`).
- Each field takes its own conditions; others answer 400 naming the ones it takes.
- Owner is a field and a grouping (GUFI's `uidtouser`).
- `builder.match: "any"` joins the rows with OR (default all, AND). A text condition is now a subquery on `words`, so it can stand in an OR.
- Directory conditions use the summary's own columns instead of rewriting the WHERE text, which turned a value such as "size" into "totsize".
- Sorting by name works in each directory's ORDER BY.

Page (`web/src/views/QueryView.tsx`, `web/src/queryBuilder.ts`):

- Opens on the builder. Presets under "Start from" fill the form and run it: biggest files, oldest files, changed in the last 7 days, largest directories, totals by extension, totals by owner. Each is the overall top N across directories and libraries (per-directory top N in `-E`, global order in `-G`, outer ORDER BY or merge across libraries).
- "Match all/any of these conditions", rows with conditions worded for each field and a hint for the value, add and remove.
- The Canned tab is gone. A query saved from it opens as the builder form that runs it; the API still takes `canned`.
- Show SQL shows the SQL of the form on screen (the saved-query SQL popover).
- New installs no longer seed the two starter queries that the presets repeat.

Tests:

- `server/test/queryBuilder.test.mjs`: the SQL for AND and OR, dates, sizes and quoting, the per-field refusals, and runs against two real GUFI indexes on one library and across both: path and day equality, before/after/time ago, any, the presets' global top N, owner totals, and the canned recent query. 18 tests.
- `web/test/queryBuilder.test.tsx`: presets, rows, match, per-field conditions, a saved canned query opening as a builder form, Show SQL, directory fields. 7 tests.
- Server suite: 537 passed, 7 skipped. Web suite: 246 passed.
- Checked in headless Chromium at 1300 and 740 px on a test server with two libraries.

Known limit, in REFERENCE: a directory with no `words` table answers nothing for a text condition, and under "any" nothing for its other conditions either, as the join did before.

### Office previews without LibreOffice, labels on read-only libraries, and Delete under All libraries (#584)

Three items from user feedback on the macOS app.

## Office preview without LibreOffice

The Preview tab of a .pptx showed a column of broken-image icons. With "All libraries" selected, or any library other than the main one, the viewer asked for page images at `/api/kb/pdf-page` without the library, so the server looked for the file in the main library and every image failed. `pdf-info` carried the library and succeeded, which is why the tab showed images at all.

- Page images now carry the library, and another library's rendered pages are cached under its id. Before, two libraries' files at the same path could share a cache entry.
- Where LibreOffice is not installed (the macOS app), a .pptx, .docx, .xlsx, .odp, .odt or .ods is read from its own XML by `server/src/officePreview.ts`. It uses a small zip reader on zlib and an XML tree, with no new dependency, inflating only the members it needs, under size caps.
  - Slides: each slide's title and paragraphs by level, plus tables and pictures, in the deck's order.
  - Documents: headings, paragraphs, list items, tables and pictures.
  - Workbooks: each sheet's first 200 rows and 30 columns, with shared and inline strings.
  - `GET /api/kb/office-preview?path=` returns the outline. `GET /api/kb/office-media?path=&name=` serves only PNG, JPEG, GIF, WebP and BMP media in the package.
- The tab is named **Pages**, **Slides**, **Document** or **Sheets**. A file with nothing to preview (a 97-2003 .ppt, .doc or .xls without LibreOffice) loses the tab and opens on **Indexed text**, with one line saying a preview needs LibreOffice. A page image that fails to load switches to the outline (Office) or the extracted text (PDF), never to a broken image.

## Labels on read-only libraries

Labelling a file in a read-only library failed with "read only".

- Labels on any library other than the main one are now kept by the Studio in `INDEX_BASE/libraries/<id>/labels.json`, keyed by the file's path in the library. The files are never written. An entry is that path's own label and is inherited below it, so a remote library never has to be asked whether a path is a folder.
- Search hits, the tree, Details and the label vocabulary merge them in.
- A Query label filter on that library compiles them into its SQL: files as `path IN (...)`, labeled folders as path prefixes. Before, Query applied the main library's folder labels to every library's index.
- They are not written in GUFI's external database form, the way the main library's `words` table is. A read-only or remote index cannot be written, and an external database is found through the index's own directories.
- A local library's labels are pruned when their files are gone, at most every ten minutes. Nothing is pruned when every labeled path is missing at once, as with an unmounted folder, and a backup is kept. A remote library's labels are not pruned. Removing a library removes its labels.
- `GET /api/kb/tags` answers `stored: files | studio`. The Labels panel then says "Saved in the Studio; the files are read only."
- With `library=all`, one `POST /api/kb/tags` may span libraries, each applied in its own.
- The main library is unchanged: labels go on the files as xattrs, or into the existing overlay.
- New folder, Rename, Cut, Paste, Delete, the selection's Move to and Delete, + Add, and the viewer's Delete are disabled on a read-only library, with the reason on hover. Labels stays enabled.

## Delete under All libraries

With "All libraries" selected, deleting a file showed "Cannot open this file … no library named all", and the file was not deleted. The client sends paths on `/api/kb/file` as-is because the GET handler takes the combined `<library>/<path>` form, but it applied that to the DELETE too, so the server's `getLibrary('all')` threw before anything was removed.

- The client now addresses the file's own library for any non-GET request.
- The server's DELETE also accepts the combined path with `library=all`.
- Any action that names `all` where one library is needed answers 400: "This action works in one library at a time".
- I checked the other actions under All libraries. Labels, Find, Download, Move and Rename already reached the right library; a move or rename spanning two libraries now gets the clear message.

## Tests

- New `server/test/readOnlyLabels.test.mjs` (12 tests): applying and reading labels with inheritance, unchanged files, an All-libraries request spanning two libraries, the tree, Details, the vocabulary, the search filter, Query SQL, a remote library, pruning and the unmounted case, Delete under All, and removal with the library.
- New `server/test/officePreview.test.mjs` (7 tests), with fixtures made by Python's zipfile and deflated: pptx order, levels, groups, a table and pictures (EMF left out); docx; xlsx; odp and odt; media served only from the package; 97-2003 and non-zip files refused; `pdf-info` without LibreOffice.
- New `web/test/viewerOffice.test.tsx` (7 tests): the Slides tab and outline, the fallback to Indexed text, page images carrying the library, Delete disabled with its reason, the Labels panel line, and the All-libraries delete URL.
- Server suite: 538 passed, 7 skipped. testdata: 21 passed. Web: 246 passed.
- Checked in headless Chromium at 1300 and 740 px against a test server with a read-only library: the deck outline, the disabled Delete and its tooltip, the Labels panel note, and the context menu with Rename, Cut and Delete disabled and Labels enabled.

Not changed: on a Mac without poppler, a PDF still shows its extracted text in Pages, as before.

### Change log through v1.172 (#585)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.172` before tagging v1.172.

## v1.171 (2026-10-23)

### The macOS app installs itself in ~/Applications from its disk image (#577)

The disk image held the app beside a link to `/Applications`, which needs administrator rights on many Macs. The app is per user (its data, server and LaunchAgent are the person's own), so it now installs in `~/Applications` by default.

**Disk image** (`make_dmg.sh`): the app and a short note, `Installing ACTIVATE Studio.txt`, with no `/Applications` link. The note:

> Double-click ACTIVATE Studio and choose Install. macOS may first ask whether to open an app downloaded from the internet; choose Open.
>
> Install copies it to the Applications folder in your home folder and opens it from there. No administrator rights are needed, and it updates itself there. Spotlight finds it there, and you can keep it in the Dock.
>
> Eject this disk image afterwards.
>
> An administrator who prefers /Applications can drag the app there instead. That copy updates itself only for users who can write to /Applications.

**Opened from the image** (a path under `/Volumes`, or `/AppTranslocation/`, which is how macOS runs an app opened from a downloaded image or folder):

- The window (`StudioShell.swift`) shows a dialog with **Install** and **Cancel**. Install puts the app in `~/Applications`, created if missing. If a copy is there already, a second dialog asks **Replace** or **Cancel** and names both versions. The window then runs `launcher.sh --install`, opens the installed copy as a new instance, and quits. Cancel quits. A failure shows the launcher's message, and Try Again offers the install again.
- Without the window (the script build opened from Finder), the launcher offers the same two dialogs through `osascript`.
- In a terminal, the launcher prints how to install and starts nothing.

**`launcher.sh --install [DIR]`** (also through the app's executable, for scripts and CI) installs into `~/Applications` or `DIR`, without asking and without opening the app.

- It copies with `ditto` into `.ACTIVATE Studio.app.install` beside the target, then moves it into place, so a copy there is replaced whole or not at all.
- A server running from the copy being replaced is stopped first, whether a LaunchAgent or the app's own run loop. Opening the new copy starts it again.
- A folder that is not writable, and a copy that fails part way, are reported in plain words, with nothing left behind.
- The uninstall plan lists the staging copies if any are left.

**Quarantine**: the quarantine attribute is removed from the installed copy only (`xattr -dr com.apple.quarantine`), so macOS does not translocate it, which would leave it unable to update itself. It is the same notarized bundle Gatekeeper assessed when the copy on the image opened. The updater and `install.sh` already install without the mark.

**Checks:**

- `check_app.sh --dmg FILE` mounts the image where macOS mounts a download (`/Volumes`) and checks the app, the note, and that there is no link.
- With `--launch` it also checks:
  - run from the image in a terminal, the app says to install and starts nothing;
  - `--install` creates `~/Applications` and installs there;
  - the copy's signature verifies (`codesign --verify --deep --strict`) and it carries no quarantine mark;
  - the installed copy starts the server;
  - `--install` again stops that server and replaces the copy whole, and the replacement verifies and starts again;
  - a quarantined source installs without the mark.
- The build job passes its image, the hardened job its throwaway-signed one, and the sign job the notarized, stapled one (only where the tag's `check_app.sh` takes `--dmg`).
- `server/test/macApp.test.mjs` has three new tests:
  - `--install`: the default folder, a named folder, replacing whole, the same copy, an unwritable folder, a failed copy, and the uninstall plan;
  - the not-installed path in a terminal and through the dialogs (Cancel, Install, Replace and its Cancel);
  - installing over the copy the server runs from.
- Server suite: 519 passed, 7 skipped. Web suite: 239 passed.

**Docs:** README, HELP and MACOS describe the double-click install, the note for administrators, and `--install`.

Only a Mac with a browser download can show these, which CI does not exercise:

- how the dialogs look;
- macOS's prompt about an app from the internet, which comes before the app's own dialog;
- that the copy opened after the install is not translocated.

### Change log through v1.171 (#578)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.171` before tagging v1.171.

## v1.170 (2026-10-23)

### The macOS app runs without a LaunchAgent where it cannot install one (#575)

On a managed Mac where `~/Library/LaunchAgents` is read-only, opening the app showed only an error. The launcher's write of the agent's plist failed with "Permission denied", and it stopped there. Users on such Macs often have no administrator rights at all, and none should be needed.

**What changes**

Where the LaunchAgent cannot be used, the launcher now starts the run loop (`studio-run.sh`) itself. That covers three cases: the folder or the plist is not writable, the write fails, or `launchctl bootstrap` does not load the agent.

- It is started through the app's Python with `setsid` and hangups ignored. That gives it a session and process group of its own, so it outlives the launcher, the window and the app's process group. Its stdin, stdout and stderr are redirected to `logs/agent.log`, so the app's window, which reads the launcher's output to its end, is not held open.
- Its PID goes in `server.pid` in the data folder. Before the PID is ever signalled or reused, it is checked against the process's command line (`studio-run.sh <data folder>`).
- The next opening reuses it. A server started from another copy of the app is restarted from this copy. Every `launchctl kickstart -k` in `start()` is now `restart_server`, which restarts whichever way the server runs.
- `STUDIO_SUPERVISOR=detached` tells the server how it was started.
- It starts when the app is opened, not at login. The docs say so.
- Where the LaunchAgent works, behavior is unchanged: the same calls in the same order, which the existing tests check.

**Updates**

In this mode, `restart()` in `updates.ts` asks for exit status 76 instead of a `launchctl kickstart`. On 76, the run loop `exec`s the new copy's own `studio-run.sh`. That means:

- the new script runs, and bash never reads the old file that was replaced;
- the PID in `server.pid` stays the same;
- `pending.json` is left for the new loop, so the rollback after three stops within two minutes still works.

The port and folder restarts (exit 75) are unchanged.

**Stop and uninstall**

- `--stop` stops the server through `server.pid`: TERM, which the loop passes to the server, then KILL to its process group after 35 seconds.
- The uninstall plan lists the agent only when there is one, and lists `server.pid` when the app started the server itself.
- An agent file in a read-only folder is reported and left disabled, and the uninstall no longer fails on it.

**Messages and docs**

- An app opened from the disk image is told to move to an Applications folder, and that Applications in the home folder works without admin rights.
- A failed start names both logs.
- MACOS.md, HELP.md and the README say to use `~/Applications` without admin rights, and that a copy there updates itself. MACOS.md describes the fallback.

**Tests**

- `check_app.sh --launch` (the build, hardened and sign jobs) runs the launcher again under a home folder whose `Library/LaunchAgents` is `chmod 555`. It checks that:
  - the server starts without a plist and answers after the launcher has exited;
  - its run loop has its own process group;
  - a second opening reuses it;
  - a restart the server asks for (a new port, as Settings, Server does) comes back from the same run loop;
  - `--stop` stops the server and the loop;
  - the uninstall dry run lists it and no agent.
- `server/test/macApp.test.mjs`, on any host:
  - the read-only folder;
  - a refused `launchctl bootstrap`;
  - a stale `server.pid` naming another process, which is left untouched;
  - the exit 76 handover to the new copy's run loop in one process;
  - the launcher returning without holding its output open.
- `updates.test.mjs`: an update installed in this mode restarts with 76 and no kickstart, and keeps `pending.json`.
- `lifecycle.test.mjs`: the new exit code.
- Server suite: 516 passed, 7 skipped. Web suite: 239 passed.

**Only a real managed Mac can confirm**

- That its MDM does not also stop a user's background process from outliving the app. Some sites restrict login items, but not processes.
- That quitting the native window leaves the server running there.

### Change log through v1.170 (#576)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.170` before tagging v1.170.

## v1.169 (2026-10-23)

### A change of index depth applies at the next sync (#573)

Sync now, and the background sync, re-index only folders whose files changed since the last pass. A change of index depth touches no file, so a library raised to Text + OCR or to a meaning stop stayed at its old depth until **Update index**.

**What changes**

- The depth an index was last built or synced at is recorded:
  - `INDEX_BASE/index-depth.json` for the main folder, put aside and brought back with its sync state when the folder changes, and deleted with the index;
  - `INDEX_BASE/libraries/<id>/depth-state.json` for a library built on this computer.
  - The record holds `depth`, `ocr` (the depth reads images and tesseract was found) and `meaningSpeed`.
  - A build writes it.
- Each sync (`POST /api/index/sweep`, which Sync now calls, and the background one) compares the record with the settings. A higher depth runs only what it adds, over the whole index in place, using the caches:

  | Change | What runs |
  |---|---|
  | From Names to Text or higher | documents and text phases |
  | OCR newly possible (Text to Text + OCR, or tesseract installed since) | documents and text phases: images are read, and PDFs converted without OCR are converted again |
  | Search by meaning newly on, wider (documents to all files), at another speed, or after new text | meaning phase, then the semantic map soon after |
  | Lower depth | nothing; it is recorded |

  - The names are not read again.
  - The job is a `sweep` labelled `Applying the new index depth: ...`.
  - The record is written only on success, so a failed or stopped run is tried again at the next sync.
- An index with no record (built before this) counts as built at its settings now, so upgrading re-indexes nothing.
- Lowering the depth keeps what was read before. Search by meaning stops at the new depth. Words read by OCR stay searchable until their folder is indexed again; nothing is deleted.
- Saving a depth asks for that sync two seconds later:
  - `PUT /api/libraries/:id/depth`, `/meaning`, and `PUT /api/index/settings` with a depth field or `meaningSpeed`.
  - It runs after any running job, and once for several saves in a row (a slider dragged across stops).
  - Not when the background sync is paused (interval 0); Sync now applies it then.
  - `meaningSpeed` also asks each library built here, since they follow it.
- **PDF cache entries.** downmark reused a PDF's cache entry by date alone, so one converted without OCR (a typed header over a scanned body, say) was never read again with OCR. An entry converted without OCR now carries ` (no OCR)` after the version on its provenance line, and a pass with OCR converts it again. Entries from before carry no mark and are reused as they are.
- **Remote builds** (ssh, pw ssh) have no incremental sync; their Rebuild always runs whole on the remote system. Starting one on every depth change was not cheap, so a remote library still takes a new depth at its next Rebuild. The Libraries page and REFERENCE say so.
- **UI copy.**
  - Settings, Indexing: a new depth is applied in the background at once, to the files already indexed, or at Sync now while the sync is paused. Update index reads everything again.
  - The Libraries Depth change says "It is applied in the background now", or "It applies at the next Rebuild" for a remote library.
  - The note under the table says the same.

**Tests**

- `server/test/depthSync.test.mjs` (new), with real GUFI, tesseract and the native downmark here, so the search checks ran:
  - a build records its depth;
  - an index with no record runs nothing;
  - Text to Text + OCR through Sync now runs documents and text only (`enrich.py`, no `embed.py`, names skipped), after which the picture and the scanned body of a PDF are found by their text;
  - lowering runs nothing;
  - a failed run keeps the old record and the next sync retries;
  - raising to a meaning stop runs the meaning phase alone;
  - the PDF cache mark is written, reused without OCR, and converted again with it;
  - saving the depth three times in a row starts one apply job;
  - the same for a built library through `syncLibrary` and through the route.
- `web/test/librariesPage.test.tsx`: changing a built library's depth says it is applied now and asks for no rebuild.
- Server suite with the extraction tests: 532 passed, 7 skipped. Web suite: 239 passed.
- End to end on a test server with a temp folder:
  - built at Text; the picture and the scanned PDF were not found;
  - saved Text + OCR through the settings route, with no Sync now and no Update index;
  - 2 s later the record read `ocr` and the last job was `Applying the new index depth: OCR`: names skipped, documents 1 converted, text 3 files, meaning skipped;
  - both were then found;
  - lowering to Text started no job.

Docs: HELP (Search: when a depth change applies) and REFERENCE (the record, the sync, the routes).

### Change log through v1.169 (#574)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.169` before tagging v1.169.

## v1.168 (2026-10-23)

### The minimum macOS covers the Python packages' native libraries (#570)

`build_app.sh` listed the site-packages `.so` and `.dylib` files with an unquoted `$(find ...)`. The app's path (`ACTIVATE Studio.app`) has a space in it, so every path was split into pieces that `macho_check.py --print-minos` skipped as missing. None of the native libraries in onnxruntime, numpy and tokenizers counted toward `LSMinimumSystemVersion`.

Changes:

- `build_app.sh` reads the list NUL-separated into an array and logs how many files it found.
- `check_app.sh` adds the same files to its Mach-O check (arm64, signature, minimum macOS, library resolution), which left them out before.
- `macho_check.py --print-minos` writes a line to stderr for a path that does not exist, so a repeat of this shows in the build log.

Checked on Linux against the pinned macOS wheels for cp312, unpacked under a path with a space: the old expression produced 66 fragments and 0 files, the new one 22 files. `--print-minos` gives 14.0, and the full check at `--max-minos 14.0` reports no problems, so the app's minimum stays macOS 14.0. The PR's mac-app build job runs both scripts on macOS.

### Updates: the app checks by the clock, hourly, and when its window opens (#571)

The macOS app scheduled its next update check with a single six-hour timer. Node's timers on macOS stop while the Mac sleeps, so on a laptop that sleeps the wait stretched well past six hours, and a new release was not offered until **Check now** was pressed.

Changes in `server/src/updates.ts`:

- A check is due when an hour has passed by the clock since the last one started or succeeded, or 30 minutes while a new release waits for its macOS build or the release server is unreachable, as before.
- The first check still runs a minute after start. After that the server looks every five minutes whether a check is due.
- `GET /api/updates`, which the window reads when it opens and then periodically, starts a due check in the background and answers `state: "checking"`. The window opening after a sleep therefore checks at once.
- A failed check counts as an attempt, so a refusal such as GitHub's hourly limit waits out the interval instead of being retried at every poll.

The interval goes from six hours to one. That is one unauthenticated request an hour per app, usually answered 304 against the stored ETag.

Tests:

- New: a clock jump with no timer running (as across a sleep) makes the next poll check.
- New: the window's status request starts a due check once.
- New: a failed check is not retried at every poll.
- The two existing schedule tests set a short poll interval.
- Server suite: 503 passed, 7 skipped. Web suite: 239 passed.

Docs: HELP, MACOS, REFERENCE and the Settings, Updates description.

### Change log through v1.168 (#572)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.168` before tagging v1.168.

## v1.167 (2026-10-23)

### Index depth: one scale per library, from names to meaning on all files (#568)

Each library the Studio builds gets an index depth, set on a slider with five stops, each adding to the one before:

| Stop | What the index holds |
|---|---|
| Names | Names, sizes, dates and owners; nothing read from inside files. Only `gufi_dir2index` runs. |
| Text | The words inside files: text, code, PDFs, Office documents. `enrich.py --no-ocr`, no image descriptions. |
| Text + OCR | Text in images and scanned PDF pages (tesseract, where installed), and image descriptions when AI features make them. Default for libraries other than the main folder. |
| Meaning on documents | Search by meaning over the first four passages of PDF, Office, text, Markdown and image files. Default for the main folder. |
| Meaning on all files | Search by meaning over up to twelve passages of every file with text. |

This replaces the main folder's "Text of PDFs and Office documents" and OCR switches and the per-library Off / Documents / Full level.

Server
- `depth` is kept per library in `indexing.json`; `documents`, `ocr` and `meaning` in the settings answer follow it.
- `PUT /api/libraries/:id/depth {depth}`; `PUT /api/libraries/:id/meaning {level}` and the old `meaning`, `embeddings`, `documents`, `ocr` settings fields still work and move the depth to the nearest stop. Settings saved by an earlier version (a level per library, the main folder's reader switches) are read the same way, and choosing the main folder's depth leaves other libraries as they were.
- Local and remote builds (ssh and pw ssh) take `depth`; a remote build at Names is sent no `enrich.py`, and at Text runs it with `--no-ocr` (a new `$10` to the run script).
- Library listings carry `depth`; word search skips a main folder at Names.

Web
- `IndexDepthControl`: the `@parallelworks/ui` Slider, stop names under it in a five-column grid (long names wrap, not shortened), notches between the ends, the chosen stop's description below, and a note when tesseract is not installed here. Dragging or arrow keys save once the value settles; clicking a stop name saves at once.
- Used in Settings > Indexing, setup's folder step, and the build forms for a folder here and on a remote system. Settings > Libraries has a Depth column with a select; an index connected as it is shows what it has, as built.
- Search help and the semantic map's "off" note use the depth's words.

Docs: README, HELP, REFERENCE and LIBRARIES.

Tests: server suite (526, 520 pass and 6 skip), web suite (239 pass), including new `indexDepth.test.mjs`, a Names-depth build of the main folder, a Names-depth remote build over ssh, the depth route, and the slider settling before it saves. Checked by eye at 400, 740 and 1200 px, light and dark.

Bundling tesseract into the macOS app is a separate change; this one only reads whether tesseract is present.

### The macOS app carries tesseract for OCR (#567)

The macOS app now reads text in pictures and on scanned PDF pages with nothing installed.

`deploy/mac/build_ocr.sh` builds tesseract 5.5.3 and leptonica 1.87.0 from pinned sources (SHA-256 checked) for macOS 14 on Apple silicon. libpng 1.6.59, libjpeg-turbo 3.2.0, libtiff 4.7.2, libwebp 1.6.0 and giflib 6.1.3 are built as static libraries into leptonica; zlib is the system's. tesseract is built without training tools, the legacy engine, OpenMP, libarchive or libcurl. The English model is `eng.traineddata` from tessdata_fast 4.1.0. The tree in `Contents/Resources/ocr` is `bin/tesseract` (run path `@executable_path/../lib`), `lib/libleptonica.6.dylib` (install name `@rpath/libleptonica.6.dylib`), `share/tessdata` (the model and tesseract's configs, `tsv` among them, which downmark asks for), licenses, README and manifest. The build stops on any library from Homebrew or `/usr/local` or built for a newer macOS, checks the references with otool and `macho_check.py`, and reads a sample of each image format before packing.

`studio-run.sh` puts `ocr/bin` first on the server's PATH and sets `TESSDATA_PREFIX` (unless `studio.env` sets another). `enrich.py` then OCRs `.png`, `.jpg`, `.jpeg`, `.gif` and `.webp`, and `preextract.ts` has downmark OCR scanned PDF pages: downmark decodes each page's image itself and runs `tesseract stdin stdout --dpi N tsv`, so poppler is not needed. A PDF downmark cannot parse at all still gets no OCR on the Mac (`enrich.py`'s own PDF OCR needs `pdftoppm`).

`build_app.sh` takes `OCR_TARBALL`, or on a Mac runs `build_ocr.sh` and keeps the archive in its cache; the `mac-app` workflow builds it on each run. `check_app.sh` checks the files and Mach-O layout, and with `--run` lists the English model, reads each sample format, checks with dyld that tesseract loads only the app's library and the system's, reads the scanned sample PDF through downmark, and has the server (started from `studio-run.sh`) report the bundled tesseract and find the picture and the scanned PDF by a word only OCR could read. The hardened and sign jobs get the samples too, so the same checks run under the hardened runtime.

The readers report (`server/src/extractors.ts`) shows the app's tesseract as bundled, notes that scanned PDF pages are OCR'd, and poppler's install entry then offers only the PDFs downmark cannot read. NOTICE lists the new components; `docs/MACOS.md` has an "OCR in the app" section.

Checked on Linux with the same CMake steps (macOS flags and checks left out): every sample format reads, and downmark's scanned-PDF path works with the built tesseract and `TESSDATA_PREFIX`. The macOS build, signing and run checks are this PR's `mac-app` job.

Also fixed: `indexer/semantic_map.py` failed with a shape error for a library of 3 to 15 embedded files, since n files span at most n of the 16 directions it projects onto. The app check found it once its sample folder held four files with vectors; `semanticMap.test.mjs` now covers a five-file library.

### Change log through v1.167 (#569)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.167` before tagging v1.167.

## v1.166 (2026-10-23)

### perf(libraries): faster search, counts and sync on large indexes (#563)

Search, the Details card, Stats, connecting a library and the folder sync were measured against large GUFI indexes, and the slow paths were changed. Search results are the same before and after; the one change in behavior is that a search across several libraries leaves out a library that has not answered in 20 seconds and names it.

## How it was measured

Two synthetic GUFI indexes were reached over ssh to localhost with a dedicated key: A, 449,064 folders and 1.5 million files (names only), and B, 300,000 files with full-text tables. The main folder was a local index. The host has 20 cores. Times are milliseconds through the Studio's API. Cold is the first request after a server start, with the OS file cache left warm; warm is the same request again. Before is main at 5335103.

| Path | Before, cold | After, cold | Before, warm | After, warm |
|---|---:|---:|---:|---:|
| Name search, A, common word (10 hits) | 46,567 | 14,482 | 45,533 | 9,036 |
| Name search, A, no match | 45,504 | 12,485 | 44,826 | 8,998 |
| Word search, B | 12,041 | 3,273 | 5,459 | 2,066 |
| Search across all libraries, common word | 59,102 | 14,547 | 50,464 | 11,897 |
| Search across all libraries, two words | 57,356 | 14,542 | 50,779 | 11,874 |
| Name search, local main folder | 2,238 | 2,377 | 2,239 | 2,157 |
| Library tree, top of A (1,968 entries) | 816 | 226 | 259 | 211 |
| Library tree, a deep folder of A | 119 | 43 | 78 | 27 |
| Details card, A selected | 100,573 | 16 | 101,222 | 6 |
| Details card, A, Count size and types | | 21,470 | | 6 |
| Details card, B selected | 19,923 | 5 | 14,160 | 6 |
| Details card, B, Count size and types | | 4,450 | | 6 |
| Details card, a folder of A | 391 | 70 | 164 | 64 |
| Stats, A | 51,321 | 10,890 | 1 | 1 |
| Stats, all libraries | 13,434 | 2,822 | 969 | 1 |
| Index status | 2 | 2 | 1 | 1 |
| Connect A: test, then add | 23,106 | 4,675 | | |
| Folder sync pass over the tree of A | 21,536 to 24,257 | 5,682 | | |
| A further ssh session to the same host | 293 | 6 | | |

The sync pass before ran on the server's thread, one await per folder and file; after, it runs on a worker thread, and the server's thread was never held more than 50 ms during it. The name search for a common word on A used to bring back 61,944 rows (13 MB) over ssh to keep 10; it now brings back the 10 (1.9 KB).

## What was slow

A remote `gufi_query` always ran with 4 threads; at 16 the same name search on A takes 14 s against 47 to 57 s at 4. Name and full-text search sent every folder's matches back and cut them to the limit in the server. Each remote host had one session, so a search's name, text and meaning queries and the card's two counts ran one after another. The Details card counted the whole index each time a library was selected, about 100 s for A, and kept the answer 20 seconds. Stats for A took 51 s cold. Connecting a library ran the connection test's count twice, once for Test and again for Add. The sync walked the folder with one await per entry, 22 to 24 s per pass for this tree. Listing a folder and the index status were already quick.

## What changed

- `remoteExec.ts`: a remote `gufi_query`, and a remote build, runs with the host's processor count up to 16 (`nproc`, else `getconf`, else `sysctl`; 4 when none answers, asked again later). `STUDIO_REMOTE_THREADS` sets one count for every host. Up to three sessions per host (`STUDIO_REMOTE_SESSIONS`, 1 to 8): a request takes an idle session, else opens one, else waits on the least busy.
- Plain ssh sessions share one connection: `ControlMaster=auto`, `ControlPersist=600`, and a socket named with 12 hex characters of the destination and port, quoted in the option since a folder may have spaces in it. The socket's folder is the first of `$XDG_RUNTIME_DIR/activate-studio` (where set), `/tmp/activate-studio-<uid>`, and `INDEX_BASE/ssh` that is short enough and the user's alone: it is made with mode 700 when missing and checked with `lstat` before each session as a directory, not a link, owned by the user, with mode 700, so a folder another user made first in `/tmp`, or a link put in its place, is passed over. ssh binds the socket at its path plus 17 characters and macOS allows 104 bytes, so the path stays within 86; the `/tmp` folder fits every macOS user name, where the data folder under `~/Library/Application Support` did not fit names over about 12 characters. With no usable folder, or `STUDIO_SSH_SHARE=0`, each session logs in on its own. `~/.ssh/config` is not changed.
- `gufi.ts`: name and full-text search run as one aggregate `gufi_query` that keeps only the rows asked for, in each thread and in the merge. Local `gufi_query` runs use one thread per core up to 16 (8 before). Folder and library counts are kept until the index changes: an index job ending here, or a remote library's recorded index time; ten minutes here and an hour for a remote library at most. Two requests for the same counts share one query.
- `libraryScope.ts`: a search across several libraries gives each 20 seconds (`STUDIO_SEARCH_DEADLINE_MS`). A late library is left out and named in `slow`; the Search page and the chat's `search_kb` say so. One library searched alone is waited for. The `gufi_vt` engine is unchanged.
- The Details card does not count a remote library's whole index when it is selected. It shows the files and folders from the connection test and a **Count size and types** button (`count=1`). Folders are counted when clicked, as before.
- Adding a library takes the result of a connection test of the same index that passed in the last two minutes. Test and Check always run afresh.
- `dirScan.ts`: the sync walks the folder with synchronous calls on a worker thread, for the main folder and for libraries built from a folder here.

Remote libraries stay read only, and nothing is written on the remote host apart from what a build already wrote.

## Tests

New `server/test/largeIndexes.test.mjs`: with a data folder over 90 bytes, the socket goes in the runtime folder, and the options read back through `ssh -G` with a space in the folder; without a runtime folder, or with a link in its place, it goes in `/tmp/activate-studio-<uid>`, made with mode 700; a folder that is a link, has mode 755, belongs to another uid, or is too long is passed over in order down to the data folder, and none at all turns sharing off; a dead master's socket (its process killed) is removed by the next ssh, which goes on to connect; the sync walk against a tree with known times, dot names and an excluded folder; a name search over 120 matches in 40 folders returning the limit, with the limit in the command; counts made once, shared while running, and made again after an index change. `sshLibraries.test.mjs`: thread counts from a host's `nproc` (3, and 64 capped at 16), 4 when nothing answers and asked again, and `STUDIO_REMOTE_THREADS`; adding after a test does not count the index again and Check does; a search across two libraries with one whose `gufi_query` answers 2 s late and a 500 ms deadline returns the other's hits and names the late one, and searching the late one alone waits for it. `remoteBuild.test.mjs`: the uncounted and counted Details answers for a remote library. Web: the Search page's note for a late library, and the card's counts and Count button.

Server suite: 522 tests, 516 pass, 6 skipped (the four embedding-model tests as before, and the two semantic map tests from #561, which need numpy here and run in CI). Web suite: 237 pass. Both run after rebasing on main at 1c0e448, with #555, #556 and #561; the one conflict, the imports in `indexing.ts`, keeps both. The ssh tests use a runtime folder of their own under `/tmp` and remove it.

Checked by hand against sshd on localhost: a session made the master in `/tmp/activate-studio-<uid>`; after `kill -9` of the master its socket stayed (`Control socket connect: Connection refused`), and the next ssh logged in (316 ms), removed the socket and became the new master; a session after that took 21 ms through it.

## Not checked here

The tests reach a host over ssh to localhost. Not checked against a remote cluster: the time an ssh login takes over a wide-area network and how much sharing saves there; `pw ssh` sessions with three per cluster; ControlMaster behavior after a network change or a sleeping laptop (a dead master is found by the keepalives within about 90 s); the processor count `nproc` reports on a login node with a cgroup or CPU quota policy, where `STUDIO_REMOTE_THREADS` is the fallback; how 16 threads affect other users of a shared login node; and indexes with rolled-up directories.

### Change log through v1.166 (#566)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.166` before tagging v1.166.

## v1.165 (2026-10-23)

### feat(stats): a fast semantic map with clusters, drill-down and 3D; both maps zoom, pan and go full screen (#561)

The semantic map on the Stats page did not scale, and both maps were fixed-size and static. This PR rebuilds the map's computation and makes both maps zoom, pan, go full screen, and (for the semantic map) turn in 3D.

**Why**

`server/src/topology.ts` dumped every passage vector as JSON text (about 4 KB a passage) through `gufi_sqlite3`, averaged and projected them in the server, and cached the result keyed on the newest db's time. So any index change recomputed it on the next page load. On a Full index of the 17,000-file test corpus (144,736 passages, 14,472 files with embeddings), the dump passed V8's maximum string length. The server threw `RangeError: Invalid string length` inside a child-process callback, an uncaught exception that ends the server, at 674 MB resident.

**What changes**

- `indexer/semantic_map.py` (numpy, from the ONNX packages):
  - It reads each folder's vectors straight from sqlite-vec's tables, without the extension: `gvec_rowids` gives a passage's chunk and offset, `gvec_vector_chunks00` holds the float32 vectors, and `gvec_chunks` has the validity bits. I checked these values against `vec_to_json`.
  - It averages them per file into a scratch file, so memory stays bounded.
  - It fits a PCA on at most 20,000 files and projects every file to 3 components for the map and 16 for k-means (4 to 14 clusters, largest first, named for their folders as before).
  - It writes `INDEX_DIR/semantic-map/overview.json` (each cluster's label, count and x/y/z centroid; a sample of at most 3,000 files with at least 25 per cluster) and `cluster-<c>.json` (each cluster's full list, shuffled). The result is written to a scratch folder and swapped in whole.
  - It exits 3 without numpy. The server then draws a smaller map itself from at most 4,000 files and marks it `fallback`.
- `server/src/semanticMap.ts`:
  - `GET /api/kb/semantic-map` reads the overview, and `GET /api/kb/semantic-map/cluster/:c?offset=&limit=` pages a cluster (2,000 by default, at most 5,000). Neither ever computes.
  - Rebuilds run in the background, 5 s after a build's meaning phase embeds anything or prunes, and at most every 10 minutes after a sync's. They are coalesced, and a rebuild requested while one runs runs once more afterwards. A missing map is made once on first read.
  - It reports `reason: off` when search by meaning is off for the main folder.
  - The map folder is deleted with the index, put aside on a folder change, and moved with the index.
- `web/src/components/CorpusMaps.tsx`:
  - **Semantic map, overview and drill-down.** Clusters are drawn as regions sized by file count over the sample, with names in screen space kept inside the canvas. A legend gives short names and counts. Clicking a cluster or its chip opens all of its files, loaded a page at a time; zooming the overview loads more files for the clusters in view (capped). Back returns to the overview. The status line reads, for example, "2,999 of 14,472 files shown; click a cluster for all of its files."
  - **3D.** A 2D/3D toggle, using a canvas projection of my own with no new dependency.
    - Drag orbits; shift-drag or right-drag pans; wheel or pinch zooms; arrows turn.
    - Points are shaded by depth and sorted far to near. Labels sit at the projected centroids.
    - An optional slow turn is off by default and hidden under prefers-reduced-motion.
    - Hover and click work on the projected points.
  - **Topology.** The treemap moves to a canvas with one nested layout four levels deep. Zooming draws the deeper levels where their parent has room, with names kept on screen. Clicking a directory still opens it on its own, and the view resets on a new directory.
  - **Both maps.** Wheel or pinch zooms about the pointer, drag pans, and double-click zooms in. The keys are +, -, the arrows and 0. A toolbar has Zoom in, Zoom out, Reset view and Full screen. Full screen is a full-window overlay (the UI package's Modal stops at 672 px); Escape or Close leaves it.
- CI installs the pinned embedding packages (`fetch_embed.py --deps`), so the map's Python step runs in the tests. That needs `files.pythonhosted.org:443` on the Linux job's egress allowlist. The macOS runner's Python 3.14 has no pinned wheels, so there the step is optional and the map's build tests skip. `check_app.sh` builds a map from the app's own Python and numpy, and in this PR's macOS app build it reports "ok, the semantic map is made from those vectors with the app's numpy".

**Measured** on the Full index of the 17,000-file test corpus (1,300 folders, 144,736 passages, 14,472 files with embeddings), on a shared 20-core Linux host:

| | before | after |
|---|---|---|
| computing the map | crashed after 4.6 s (`RangeError`), 674 MB resident | 2.5 s (1.8 s reading vectors), 299 MB peak, once, in the background |
| overview response | would have carried every file | 504 KB (116 KB gzipped), 12 ms |
| a cluster page (2,000 files) | none | 290 KB, 5 ms |

In headless Chromium:
- **3D frame rate:** turning held 60 fps with 7,678 points (the sample plus pages loaded by an earlier zoom) and in a 1,794-file cluster. 3D caps a cluster at 6,000 points.
- **1300 px wide, then 740 px wide:** overview; keyboard zoom (+ +, then 0) with the extra files loading (2,999 to 6,256); drill-down via the legend and Back; 3D with orbit drag; full screen and Escape; treemap zoom to 9.5× showing nested levels.
- **Screenshots:** I checked them for label clipping and overlap. Two fixes came out of that: names kept inside the canvas, and parent names kept in their header strip.

**Tests**
- Server: `semanticMap.test.mjs` covers the Python step on a fixture index in sqlite-vec's layout, including a passage in an invalid slot; the read-only routes and paging; coalesced rebuilds; one build for a missing map; and `off`. Suite: 498 pass, 4 skip (ONNX model tests).
- Web: `corpusMapsView.test.tsx` covers zoom about the pointer, clamping, the 3D projection (known coordinates at known rotations), nearest-point hover with the front point winning, overview, drill-down and Back, keyboard zoom and reset, the 2D/3D toggle, hover and click-to-open, full screen and Escape, the "off" message, and the treemap's zoom, drill and full screen. Suite: 227 pass.

**Not verified here:** the Mac app build runs the new `check_app.sh` step in CI. WebKit rendering and trackpad pinch can only be checked on a Mac.

### Change log through v1.165 (#565)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.165` before tagging v1.165.

## v1.164 (2026-10-23)

### fix: Delete index and Remove ask in a dialog, red where they delete; Search says what it can match (#556)

**Delete index and Remove**

- **Delete index** was a plain button that opened an inline question. It is now red (`danger-outline`) and asks in an alert dialog (the package's ConfirmModal, destructive).
  - The dialog focuses Keep it, so a stray Enter deletes nothing.
  - It says what stops working and that the folder's files are not touched.
  - A failure shows in the dialog, which stays open for another try.
- A library's **Remove** asked nothing, even where it deletes the index the Studio keeps in a folder of its own. It now asks first and says what goes and what stays:
  - Removing a built library whose index sits in a chosen folder: the index stays in that folder.
  - Removing a connected index, or a remote one: the index stays where it is.
  - Only when the Studio's own index is deleted is the button red and the dialog an alert.

**Search wording**

The search box's hint listed only the search operators, and the page's note always promised meaning. Both now follow the libraries in scope, through `web/src/searchReach.ts`:

| What the libraries in scope support | Search box hint | Page note |
|---|---|---|
| Search by meaning, in any of them | Ask in plain words, or "exact phrase", OR, -exclude, prefix* | Names, words and meaning |
| Full text only | Whole words; "exact phrase", OR, -exclude, prefix* | Names and words |
| Names only | Part of a file or folder name | File and folder names |

**Tests:** web 217 pass, with new tests for the dialogs, the red buttons, and the hint and note in each case.

### feat(index): read .ppt, .xls and OpenDocument without LibreOffice, and show how to install what is missing (#555)

`.ppt`, `.xls`, `.odt`, `.odp` and `.ods` indexed by name only unless LibreOffice was installed, and the macOS app does not carry it (about 1 GB). This adds small readers instead, and makes the tools that remain optional easy to install.

**Readers** (`indexer/enrich.py`; nothing to install beyond what ships):
- OpenDocument (`.odt`, `.ods`, `.odp`, `.odg`), with the standard library. It reads paragraphs, headings, lists, tables as tab-separated rows, sheets under their names, slides with their speaker notes, and a text document's headers and footers. Comments are left out, as for Word documents.
- `.xls` through xlrd 2.0.2 (BSD-3-Clause). It reads each sheet under its name, numbers as Excel shows them (15 significant digits) and dates as dates. Its warnings go to `/dev/null`, since stdout carries the progress lines.
- `.ppt` through olefile 0.47 (BSD-2-Clause). It reads TextCharsAtom and TextBytesAtom records in the "PowerPoint Document" stream: slide text, text boxes, tables and speaker notes. Masters and handouts are skipped, and so are field stand-ins.
- A file these cannot read goes to LibreOffice where it is installed and the setting allows. LibreOffice keeps rendering previews. The setting now reads "LibreOffice, for previews and for Office files the built-in readers cannot read".

**Packaging:** xlrd and olefile are pinned with their SHA-256 in `indexer/embed_deps.json` (`readers`). `fetch_embed.py --deps` installs them wherever the ONNX packages go:
- the macOS app's Python; `check_app.sh` checks that they import;
- the container (`app.def`);
- a clone (`setup_gufi.sh`);
- the hosted bundle's wheel cache and the workflow's per-launch step, which now also checks `import xlrd, olefile`.

`--readers-only` installs just those two; CI runs it, so their tests fail rather than skip. Size: 211 KB of wheels, 1.3 MB unpacked.

**Installing what is still missing** (Settings, General, Indexing, under "What this computer can read"):
- One line per missing tool, saying what it adds:
  - tesseract: "OCR reads scanned PDFs and text in images"
  - poppler: PDFs the bundled readers cannot read, and OCR of scanned pages
  - LibreOffice: "renders previews of Office files"
- The install command for this machine, with a copy button:
  - `brew install tesseract`, `brew install poppler`, `brew install --cask libreoffice` when Homebrew is at `/opt/homebrew/bin/brew` or `/usr/local/bin/brew`;
  - a link to brew.sh when it is not;
  - `sudo apt-get install tesseract-ocr poppler-utils libreoffice` package by package on Debian or Ubuntu.
- **Check again** re-reads `/api/index/extractors?fresh=1`. A tool found since is named, with "The next index update uses it" and **Update the index now**, the latter on a single-user Studio when nothing is running.
- The Studio shows these commands and runs none of them. The report carries this as `install` (`platform`, `brew`, `apt`, `items`).

**Comparison with LibreOffice** (24.2, on this host).

Each file was converted by LibreOffice to the matching OOXML format, then read two ways:
- with python-docx (paragraphs, tables, headers and footers), openpyxl (sheet names and computed cell values) and python-pptx (every shape, groups, tables and notes): what LibreOffice extracts;
- with enrich.py's own OOXML regex reader: today's path.

Recall is the share of that word set the new reader also gets.

The files:
- generated with python-docx, openpyxl and python-pptx, with headings, lists, a merged table cell, a header and footer, several sheets, dates, a formula, merged cells, speaker notes, a text box, a table and non-ASCII text, then converted by LibreOffice to each format;
- 28 public test documents: Apache POI's `test-data` (Apache-2.0) for `.ppt` and `.xls`, and LibreOffice core's `qa` documents (MPL-2.0) for OpenDocument. These were downloaded for this comparison and are not committed.

| Format | Files | Recall vs LibreOffice's content | Recall vs today's path | What it misses |
|---|---|---|---|---|
| .odt | 7 | 90/90 = 100% | 75/80 = 93.8% | nothing; the gap to today's path is that path gluing words across line breaks ("One" + break + "list" becomes `onelist`) |
| .ods | 7 | 65/68 = 95.6% | 52/126 = 41.3% | a stale cached formula result in a test file (`=SUM(2;2)` stored as 5), which LibreOffice recalculates; a zero shown blank; a sheet name LibreOffice assigns to an unnamed sheet |
| .xls | 9 | 359/359 = 100% without the fuzzed file; 359/407 = 88.2% with it | 338/584 = 57.9% | a fuzzer-minimized file, which LibreOffice reads as binary junk (`_x0001_...`) and xlrd refuses |
| .odp | 6 | 47/48 = 97.9% | 36/39 = 92.3% | the word "number" from a slide-number field's `<number>` stand-in, dropped on purpose |
| .ppt | 6 | 59/59 = 100% | 48/48 = 100% | nothing |

The "today's path" column is low for spreadsheets because that path's regex reads shared-string index numbers, formula text and header codes (`&C`, `&P`, `&12`) as words.

Beyond today's path, the readers also index speaker notes (`.ppt`, `.odp`) and `.odt` headers and footers.

Two gaps were found and closed along the way: `.odt` headers and footers (from `styles.xml`), and numbers printed at full float precision rather than 15 significant digits.

**Robustness:** 120 Apache POI `.ppt` and `.xls` files, including malformed, fuzzed, password-protected and empty ones:
- nothing raised, and nothing was written to stdout;
- the slowest took 0.02 s;
- 51 of 60 `.ppt` and 58 of 60 `.xls` files gave text; the rest are protected, empty or fuzzed.

**Tests:**
- `server/test/officeReaders.test.mjs`:
  - OpenDocument text, spreadsheet and presentation files built in the test;
  - committed fixtures written with LibreOffice from text made for them: `testdata/office/budget.xls` (7.7 KB) and `harbor-survey.ppt.gz` (22.7 KB gzipped, 650 KB raw);
  - damaged files through the dispatcher;
  - the readiness report and the install commands for macOS with and without Homebrew, and Debian.
- `web/test/readerInstalls.test.tsx`: the commands, the copy button, the Homebrew link, Check again, the found message, and Update the index now.
- Server: 502 passed, 4 skipped (the ONNX tests that need model files). Web: 219 passed.

**Docs:** HELP, REFERENCE, MACOS and NOTICE are updated, and `indexer/requirements.txt`.

### Change log through v1.164 (#564)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.164` before tagging v1.164.

## v1.163 (2026-10-23)

### feat(brand): with no icon uploaded, the Studio mark takes the accent (#559)

Where no icon has been uploaded, the sidebar and the empty chat show the Studio mark. It now follows the accent chosen in Settings, Appearance.

- **Default accent (Navy):** the mark is exactly the app icon (`/favicon.svg`), so it matches the macOS app's Dock icon.
- **Any other accent, custom ones included:** the same drawing, with the tile running from a light to a deep shade of the accent's ink and the spark in the accent's lighter, dark-theme ink. `studioMark.ts` builds it as an SVG data URL, cached per accent. `accents.ts` provides the colors (`markColors`).
- **Unchanged:** an uploaded icon still takes the mark's place. The macOS app icon and the browser tab's icon stay the app icon, since they cannot change at runtime.

**Tests:**

- The default accent gives the app icon.
- Another accent gives the same drawing with its colors.
- A custom accent differs from both.
- An uploaded icon still wins.

Web suite: 218 pass.

### fix(setup): the libraries step asks for one folder; optional fields wait under More options (#558)

The setup wizard's Other libraries step opened on three tabs (this machine, SSH, ACTIVATE) and four fields for connecting an existing GUFI index. That is rarely what someone setting up the Studio has. Feedback was that the step was overwhelming.

The step now:

- Leads with the common case: another folder on this computer, indexed here in the background. It shows one field, **Folder to index**, with Browse, then Check and Build the index.
- Opens an existing GUFI index, an index over SSH, or one on ACTIVATE through one link: **Other kinds: an existing GUFI index, or one on another machine**. That link shows the previous tabs.
- Says in one sentence that the step is optional and that libraries can be added any time in Settings, Libraries.

The library form on this computer, in setup and in Settings, Add a library, now shows only the folder that has to be given. The index folder (build) or source directory (connect), the label and the identifier sit under **More options**, collapsed.

Checked in headless Chromium (dark, 1100 px wide): the step is the sentence, one field, Browse, More options, the two buttons and the link. Web suite: setup and libraries tests updated and passing.

### feat(setup): a new personal install starts with AI off, Workflows hidden, a mission and ten questions (#560)

**Defaults for a new install on a computer of its own** (sign-in off, not deployed by the platform, setup not finished):

- AI features start off.
- The Workflows page starts hidden.
- `/api/setup/complete` (Finish, or Finish later) saves `aiEnabled` and `sections` as setup showed them where they were still unset, so they stay that way.
- An install that finished setup before this keeps its own settings.
- Deployments that sign people in are unchanged.

**The mission and questions** (feedback: a Mac install showed 3 questions and no mission):

- A blank saved mission, or the three deployment questions an earlier version saved, no longer hide the personal defaults.
- Saving the defaults as they are stores nothing, so later defaults reach them.
- The personal list grows from 5 to 10 questions. The empty chat shows a random 4.

**Workflows wording** (Settings, Workflows, and the setup sidebar step) names the two ways to run workflows:

- ACTIVATE workflows, with an ACTIVATE credential.
- Workflow YAML files of your own, without ACTIVATE, with Meridian, the open-source engine.

**Tests:** the server suite's new tests cover the defaults, the old saved values, saving defaults unchanged, and what finishing setup saves. The tests that use a model run with `AI_ENABLED=1`, as a Studio whose AI features were turned on. Web suite: 216 pass. HELP is updated.

### Change log through v1.163 (#562)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.163` before tagging v1.163.

## v1.162 (2026-10-23)

### fix(index): an unrunnable pdftotext falls back to pypdf, lock files are skipped, staging starts empty (#553)

Three problems from a Full build's log on a Mac:

- **`extract failed …: [Errno 13] Permission denied: 'pdftotext'`.** With no poppler installed, a PATH entry the server cannot search makes Python's lookup fail with EACCES. `extract_pdf` caught only FileNotFoundError, so the PDF failed instead of falling back to pypdf. pdftotext is now resolved with `shutil.which`, and any OSError leaves the file to pypdf. To reproduce, put an unrunnable `pdftotext` alone on PATH: v1.161 raises the PermissionError; this branch returns the text through pypdf.
- **`~$RevisedBOM_pcbway.xlsx: … zip: not a valid zip file`.** That is Office's lock file, a few bytes naming who has the document open. Office (`~$`) and LibreOffice (`.~lock.`) lock files are now skipped by the converter (preextract.ts) and the text pass (enrich.py). They stay searchable by name.
- **`gufi.staging-1 Already exists!`.** The staging folder is named after the job number, which starts again at 1 with the Studio. A build that stopped partway left one that the next build wrote over, keeping folders deleted since. It is emptied before gufi_dir2index runs. The tool's note that the folder exists is kept out of the log on stderr too, where it actually arrives.

**Tests:** `server/test/indexerReaders.test.mjs` covers the lock-file check, a lock file extracting to nothing, and an unrunnable pdftotext as the only one on PATH. Server suite: 496 pass, 4 skip (ONNX model tests).

### fix(brand): the sidebar shows the Studio's own mark when no icon is uploaded (#554)

With no icon uploaded, the top of the sidebar showed a letter badge built from the Studio's name, such as "M" for a Studio named after its owner. The macOS app icon and the empty chat show the Studio mark (`/favicon.svg`, the same artwork as the app icon). The sidebar now shows that mark too. An uploaded icon, light or dark, still takes its place. The letter badge and its styles are removed.

A web test checks the default mark in the rendered sidebar. Web suite: 216 pass.

### Change log through v1.162 (#557)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.162` before tagging v1.162.

## v1.161 (2026-10-23)

### fix(search): a file found several ways is labelled by the most exact one (#550)

Found while testing search end to end. On a 337-file test corpus, the query `sourdough` (a word only in the file's name, `sourdough-notes.md`) ranked that file first, but labelled it `meaning`.

The cause: blending keeps the first entry for each file, and matches by meaning come before name matches. So a file whose name matched was shown as a match by meaning whenever its text was also close. That contradicts the Search page, which says each result shows how it matched.

The fix: a file keeps its place in the results and is labelled by the most exact way it was found. Words come first, then name, then meaning.

On the test corpus after the fix:
- `sourdough`: `reports/sourdough-notes.md` [name]
- `blade-inspection`: `reports/blade-inspection-2026.md` [name]
- `zephyrine`: `reports/quarterly-budget-zephyrine.txt` [words]
- `why did the rotor parts break`: `reports/blade-inspection-2026.md` [meaning]

A new unit test covers the labels and the order. The server suite passes: 494, with 4 skipped (ONNX model tests).

### Change log through v1.161 (#552)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.161` before tagging v1.161.

## v1.160 (2026-10-23)

### fix(deploy): the ONNX backend is set up on every launch, from wheels the bundle carries (#549)

A workflow deployment launched with indexing on launch turned off kept the older GGUF model for search by meaning. Two gaps caused this:

- The ONNX packages were fetched only inside the index step, which exits early when `run_index` is false.
- A system without outbound network could not download them at all.

A relocated index also got the ONNX model only in that step.

**Changes** (`deploy/workflow.yaml` and `deploy/workflow-cfd-studio.yaml`):

- A new `pull_software` step, **Search by meaning packages and models**, runs on every launch. It is skipped in cleanup and container mode.
  - It places `minilm384.gguf` and a link to `minilm-l6` under a relocated `INDEX_BASE/models`.
  - It installs the packages for the Python the server will run: the newest python3.x from 3.10 up, the same choice `server/src/config.ts` makes.
  - It installs only what is missing, and its log names the backend in use.
- The index step no longer does any of this.
- `deploy/make_bundle.sh` fills `app/indexer/wheels` with the pinned Linux x86_64 wheels for Python 3.11 to 3.13, checked against their SHA-256. That adds 120 MB to the bundle. `fetch_embed.py` installs from that cache with no network; a Python with no bundled wheels downloads its own.
- `docs/DEPLOY.md` describes this.

**Checked:**

- With the network blocked, `fetch_embed.py` installed onnxruntime, tokenizers and numpy from the bundled cache, and they import.
- The step, run twice against a copy of the app tree with a relocated index:
  - The first run installs from the wheels, copies the GGUF model, links `minilm-l6`, and reports "the ONNX backend, for Python 3.12.3".
  - The second run finds everything in place and skips the install.
- Both workflow files parse.
- Server suite: 493 pass and 4 skip, the ONNX tests that need the model files.

### Change log through v1.160 (#551)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.160` before tagging v1.160.

## v1.159 (2026-10-22)

### feat(index): a Faster speed for search by meaning, and the README on its levels (#546)

Settings, Indexing gains **Speed of search by meaning**:

| Speed | Embeds per passage | Time per passage, one core | Ranking (MRR, 82 doc queries) |
|---|---|---|---|
| Standard (default) | the first 256 tokens | 26.6 ms | 0.540 |
| Faster | the first 128 tokens (`embed.py --max-tokens 128`) | 11.5 ms | 0.521 |

The model before v1.158 scored 0.491 on the same queries.

Which libraries follow the setting:

- The main folder sets it.
- Libraries built of folders here follow it, as they follow the threads and readers.
- Remote builds pass it too.

The setting is disabled while the main folder's level is Off. A change embeds each index again, once, at its next build or sync, since the two speeds are different models to an index (`minilm-l6-mean-int8` and `-t128`). Queries need nothing new: the query worker already serves both.

The README gains a short section on search by meaning: the three levels, the two speeds, and how a clone fetches the ONNX backend. HELP and REFERENCE are updated.

**Tests**

- Server: 492 pass and 4 skip, the ONNX tests that need the model files. The new tests cover the setting's validation and default and the arguments embed.py gets.
- Web: 214 pass.

### feat(mac): Uninstall ACTIVATE Studio… in the app menu, listing everything it removes (#547)

Before this, the app could be removed only from Terminal (`--uninstall [--purge]`). `--purge` also missed three things: an index moved elsewhere with Settings, Indexing; the indexes of libraries built into folders chosen for them; and the window's own data.

**App menu:** a new item, **Uninstall ACTIVATE Studio…**, opens a dialog on the window.

- The dialog lists what is removed and what is kept. **Also remove indexes, settings and logs** is ticked by default, and the list follows the checkbox.
- **Uninstall** is styled as destructive. Return cancels.
- Once confirmed, the window stops saving its frame and restorable state. With the data chosen, it clears its web data. It then runs the launcher, shows the launcher's output and quits.

**Launcher plan:** `launcher.sh --uninstall --dry-run [--purge]` prints the plan without removing anything, one `kind<TAB>path` line each:

- `server`, `app`, `update-copy`
- with `--purge`: `data`, `index`, `library-index`, `window-data`
- `keep-data` and `keep-documents` for what stays

The dialog reads this plan, and the removal follows the same plan, so the two never differ.

**`--purge` now also removes:**

- **A moved index (`STUDIO_INDEX_DIR`).** Only the Studio's entries (`gufi`, `extract`, `embed-cache.db*`, `folders`, `indexed-root`, build leftovers), and the folder only when it is left empty. The folder must hold the Studio's index.
- **Libraries built into chosen folders** (from `INDEX_BASE/libraries.json`). Only folders holding the `.studio-index.json` manifest; there, the tree it names, the manifest and staging folders are removed, and the folder only when it is left empty.
- **The window's data** under `~/Library`: `WebKit`, `Caches`, `HTTPStorages`, `Saved Application State` and `Preferences` for the bundle id.

The documents folder is never removed. Without `--purge`, behavior is unchanged.

**Settings, Server:** in the macOS app (`/api/setup/status` now carries `macApp`, the bundle path), a line says to use the menu item and gives the Terminal command.

**Docs:** README, MACOS (the full list) and HELP.

**Tests:**

- `macApp.test.mjs` runs the dry run (with and without `--purge`) and the real removal against a fake home. The setup includes a moved index beside a file of the person's, a chosen library folder with a manifest and a file of the person's, one without a manifest, and window data. It asserts the exact lines, that only the Studio's entries go, and that the documents stay.
- `setup.test.mjs` covers `macApp`, and `settingsGeneral.test.tsx` covers the Settings line.
- `check_app.sh --launch` runs the dry run in the built app with its own Python.
- Server suite: 493 passed, 4 skipped (the ONNX model tests, which need the models). Web suite: 215/215.

**Mac only:** the Swift compiles only in the macOS CI job. The dialog itself, and whether WebKit or preferences rewrite anything on quit, can only be checked on a Mac.

### Change log through v1.159 (#548)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.159` before tagging v1.159.

## v1.158 (2026-10-22)

### feat(index): embed with ONNX Runtime and the int8 MiniLM model, with mean pooling (#539)

Search by meaning used sqlite-lembed inside gufi_sqlite3 with a Q8 GGUF of all-MiniLM-L6-v2. Its vectors match that model with CLS pooling (cosine 0.9997 against an ONNX run with CLS pooling). The model is trained for mean pooling, so ranking was below what the model gives. This PR embeds with ONNX Runtime and the int8 export of the same model, with mean pooling, and packages it for the macOS app, the container image, clones and the hosted workflow.

**Indexing** (`indexer/embed.py`, `indexer/embed_onnx.py`)
- Backend choice. ONNX is used when onnxruntime, tokenizers and numpy import and `INDEX_BASE/models/minilm-l6` holds `model.onnx` and `tokenizer.json`. Otherwise it falls back to lembed, as on a remote host or an older install. `--backend onnx|lembed` forces one.
- The model: int8, mean pooling and L2 normalization, cut at 256 tokens. `--max-tokens 128` is an option, at about 40 percent of the CPU and slightly lower ranking.
- Threads: the `--jobs` budget is split across at most eight spawned processes of several ONNX Runtime threads each. Tokenizer and BLAS thread pools are limited to one.
- The embedding cache is keyed by model id: `minilm-l6-mean-int8`, `-t128`, or `minilm384`.
- Each folder's `db.db` names its model in a one-row `gvecmodel` table; a folder without one holds `minilm384` vectors.
- A pass over one folder of an index on another model widens to the whole index, so an index never mixes models. Old vectors keep answering until then, and the libraries probe still sees `gvec`.

**Queries** (`server/src/embedWorker.ts`, `indexer/embed_query.py`, `server/src/gufi.ts`)
- `searchVector` reads each folder's model and embeds the query once per model.
- For the ONNX model it uses a persistent Python worker, speaking JSON lines on stdin and stdout. It is started at the first such search, stopped after ten idle minutes, and ends with the server. A worker that cannot start is not retried for five minutes.
- The query vector goes into the vec0 KNN as a blob literal. `minilm384` folders still use lembed.
- `vectorModels(indexRoot)` reports the mix.

**Packaging**
- `indexer/fetch_embed.py` (standard library only) installs the packages and the model as pinned in `indexer/embed_deps.json`: wheel URLs and SHA-256 for Linux x86_64 and aarch64 and macOS arm64, CPython 3.11 to 3.13, and the model at Hugging Face revision 751bff37. Downloads are cached; offline, it fails with a plain message and the GGUF path keeps working.
- The macOS app gets the packages in its Python's site-packages and the model in `Resources/models/minilm-l6`. `studio-run.sh` links it into the index folder, and the minimum macOS check covers the new binaries (macOS 14, as now).
- `check_app.sh` checks:
  - the pinned model's hash and the imports;
  - an ONNX embed with its marker, and the lembed fallback;
  - the query worker;
  - a search by meaning through the running server that only the vectors can answer.
- The container gets `/app/indexer/.deps` for bookworm's Python 3.11, the model is linked into `INDEX_BASE`, and the final stage proves an embed. `container.yaml` allows `files.pythonhosted.org`.
- Clones: `setup_gufi.sh` runs the fetcher into `indexer/.deps` (gitignored).
- Hosted: `make_bundle.sh` carries the model, and the workflow's index step fetches the packages for the resource's Python into `cache/`.
- NOTICE lists the model (Apache-2.0), ONNX Runtime (MIT), tokenizers (Apache-2.0) and NumPy (BSD-3-Clause).

**Measured** (a shared 12-core host, load about 8 from other work, so the absolute times are high)

| | per passage, pinned to one core | 17,000-file corpus (119,180 passages to embed), `--jobs 8` |
|---|---|---|
| lembed, GGUF Q8, CLS | 81 ms | 899 s |
| ONNX int8, mean, 256 tokens (the default) | 26.6 ms | 668 s |
| ONNX int8, mean, 128 tokens (`--max-tokens 128`) | 11.5 ms | 301 s |

Per core, ONNX uses about a third of lembed's CPU at 256 tokens and a seventh at 128. On this shared host, lembed starts about 3.5 threads per process, about 28 at `--jobs 8` against ONNX's 8, so it takes a larger share of a busy machine: the corpus times understate the gain. The per-core figures are the better guide to an idle Mac. Batch 16 was as fast as 32 and used about 370 MB per embedding process against 630 MB.

Ranking: 82 queries made from the docs' section headings, retrieving the file among 166 passages:

| | top-1 | top-3 | MRR |
|---|---|---|---|
| CLS, as the GGUF path (256 tokens) | 0.32 | 0.60 | 0.491 |
| ONNX int8, mean, 256 tokens | 0.39 | 0.60 | 0.540 |
| ONNX int8, mean, 128 tokens | 0.35 | 0.59 | 0.521 |

int8 ranks no worse than fp32 (MRR 0.540 against 0.517). This is a small evaluation.

**Size**: the macOS app grows by about 118 MB unpacked (onnxruntime 71 MB, numpy 14 MB, tokenizers 9.5 MB, the model 23 MB), about 41 MB compressed.

**Tests**: `server/test/embedOnnx.test.mjs` covers the worker protocol with a stand-in worker (it runs everywhere): vectors, ids, a worker that cannot start, and one that stops mid-request. Where `STUDIO_TEST_ONNX_DIR` (and `STUDIO_TEST_GGUF`) name the models, it also covers:
- an ONNX-embedded index and its markers;
- searches by paraphrase;
- a GGUF-embedded index searched with lembed;
- widening in both directions;
- the 128-token model id.

Rebased on v1.156: #540's `--max-chunks` and `--documents-only` work with the ONNX backend, and the cache is shared between them. With the models present, the server suite passes 496 of 496, including 8 of 8 in the new file. The web suite passes 214 of 214. Without the models, as in CI, the 4 ONNX tests in the new file skip. `macApp.test.mjs` checks the new link.

The mac-app workflow runs on this PR because `deploy/mac/**` changed. On the head before the rebase, its build passed every app check on a macOS 15 runner, including the ONNX embed with its marker and a search by meaning through the app's own server. The copy signed with a throwaway identity under the hardened runtime ran too. The container image built with the packages and its embed check passed. Notarization of the new binaries is left to the next release's sign job. Remote libraries keep the GGUF path, since their builds copy only `enrich.py` and `embed.py`.

### Change log through v1.158 (#544)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.158` before tagging v1.158.

## v1.157 (2026-10-22)

### fix(chat): in a narrow window the conversation list has a button again (#542)

Under 760 px wide the chat uses its phone layout, where the conversation list is a drawer. The drawer opened only from the bottom bar's Chat item, tapped again while already on Chat, and nothing showed that. A macOS app window can be as narrow as 720 px, so at half a laptop screen there was no visible way to reach the list or its collapse control.

The package's own drawer button (Open sidebar) now shows above the thread whenever the list is a drawer. The bottom-bar shortcut stays. Wider windows are unchanged: the list is inline with its collapse control at the bottom.

Checked in Chromium at 740 x 820: the button shows at the top left and opens the list. At 1300 px the inline list and its control are as before. Web suite passes.

### Change log through v1.157 (#543)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.157` before tagging v1.157.

## v1.156 (2026-10-22)

### Updates: the app reopens after an update that changed its window, and shows the new icon (#535)

The in-app update swaps the whole app bundle and restarts the server. The window that is open still runs the old copy's executable until the app opens again, so a new title bar color or Dock icon did not appear after updating. The only hint was a line in Settings, Updates.

- **Window.** `StudioShell.swift` answers a new `relaunch` request from the Studio's own page. It replies true and starts `/bin/sh`, which waits for this process to exit (30 seconds at most) and then runs `open` on the bundle path the app was launched from. Then the app quits, so only one Dock icon remains.
- **Page.** If an install or rollback was started on the page, it stores the target version in session storage before reloading. The page that loads on that version sends `relaunch` right away and does not record the version as seen, so the reopened window shows **Updated to**. After an automatic install while idle, the post-update notice (still one line) and Settings, Updates offer **Reopen now**. In a browser, or in a window from before this change (which rejects the unknown request), the line says to quit and reopen instead.
- **Icon.** After a swap or rollback, the updater touches the bundle and runs `lsregister -f` on it, so Finder and the Dock show the new copy's icon. This is best effort and only runs on macOS; `STUDIO_LSREGISTER` names a stand-in for tests.
- **Docs.** HELP, MACOS and REFERENCE are updated.

Tests: the server suite passes (479/479). The updater test checks that LaunchServices is called on the new copy. The web suite passes (203/203), with new tests for: the reopen marker set by Update now; the automatic reopen leaving the version unrecorded; Reopen now falling back to words when the window cannot reopen; and Settings, Updates sending the request.

Needs a Mac to verify: the Swift change compiles in the macOS CI job, but whether the relaunch helper survives the app quitting and reopens the window, and whether `lsregister` refreshes the Dock icon, can only be checked on a Mac. The first update after this ships still runs the old window, which does not know `relaunch`, so that one update ends with the words; updates after it reopen on their own.

### feat(search): search by meaning is a level per library, and the Search page says how search works (#540)

Search by meaning was one switch for the main folder, on by default, and libraries the Studio built followed it. It is the slowest part of indexing, and many folders (code, data, simulation output) gain little from it.

**Levels.** Each library the Studio builds has its own level, kept with its index settings (`meaning` in `INDEX_BASE/indexing.json`) and applied at its next build or sync:

- Off: names and words only.
- Documents: the first four passages of PDF, Office, text, Markdown and image files. This is `embed.py --documents-only --max-chunks 4`.
- Full: up to twelve passages of every file with text, as before.

The main folder starts at Documents and other libraries at Off. The earlier `embeddings` true or false is still read: false becomes Off, true the library's usual level.

On the 6.5 GB benchmark corpus (17,000 files, 15,272 with text), Full embeds 144,736 passages and Documents 41,737, which is 3.5 times fewer.

**Where it is set:**

- Settings, Libraries: a selector in the Meaning column, for the main folder and libraries built here or on a remote system. `PUT /api/libraries/:id/meaning` stores it; an index the Studio only reads answers 409 and uses the vectors it has.
- Settings, Indexing, for the main folder.
- The setup folder step.
- `meaning` on a local or remote build request.

Remote builds pass the level to embed.py there, and Off skips embedding.

**Search:**

- It looks in a library by meaning only where its level is not Off. A read-only index is searched by meaning when it has vectors. The RAG proxy follows the main folder's level.
- The Search page says in one line what a search matches (names and words always, and meaning in the libraries where it is on). **How search works** explains each kind, with each library's level and a link to Settings, Libraries.
- Result badges read name, words and meaning.

**Also:**

- The Libraries page says where a library the deployment set is removed (its `STUDIO_LIBRARIES` setting), and that a Studio with sign-in builds and deletes its own main index.
- An explicit `PW_CLI` path is taken as it is, found or not, rather than falling through to another pw.
- The pw-not-found reason points at the location setting in Model access.

**Tests:** server 488/488 (new: per-library levels, the route, the legacy switch, embed.py's selection run in Python); web 211/211 (the Search page line and help, the Libraries selector, the setup control). HELP, REFERENCE and LIBRARIES are updated.

### Change log through v1.156 (#541)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.156` before tagging v1.156.

## v1.155 (2026-10-22)

### fix(libraries): a built library follows the indexing settings and keeps an embedding cache (#534)

A library built of a folder on this computer ignored Settings, General, Indexing. It ran gufi_dir2index on a fixed 8 threads, and enrich.py and embed.py with their own defaults. So the thread count, the PDF reader, OCR, document text, LibreOffice, image descriptions and the embeddings switch reached only the main folder, and every embedding pass started cold.

- `enrichSteps` builds the extract and embed steps from the indexing settings, as the main folder's phases do. It skips documents and embeddings when they are switched off.
- The GUFI runs take the thread count (the single-level run caps at 4).
- `embed.py` keeps a cache at `INDEX_BASE/libraries/<id>/embed-cache.db`, pruned on a whole build, so a rebuild or a sync embeds only new passages.
- downmark's OCR runs one thread per document, as in the documents phase.
- HELP and REFERENCE say that built libraries follow the settings.

Tests: a unit test of the steps under settings on and off. The server suite passes (480/480, GUFI installed, nothing skipped). A build with the real embedding model wrote `gvec` tables in each folder and filled the library's cache.

### feat(libraries): choose SSH or ACTIVATE for a remote library, with a list of systems (#536)

Until now, Add a library offered pw ssh for a remote system only when the Studio found the pw CLI and had a sign-in to act with. On a computer where the Studio did not find the pw CLI, or the CLI was not signed in, only ssh showed and nothing said why.

- On a Studio with sign-in off, **Connect with** always shows **SSH** and **ACTIVATE (pw ssh)**. The setup wizard's tab follows the same rule.
- When pw ssh cannot be used yet, the form says why and has an **Open Model access** button. The reasons are that the Studio does not find the pw CLI (the message names where it looked), or that the CLI is not signed in.
- The system comes from the new `GET /api/user-libraries/pw/systems`, which runs `pw clusters ls -o json` with the same key pw ssh uses. Running systems come first, stopped ones are marked off, and a display name is shown over the name pw ssh takes. **Another system…** lets you type a name.
- When the list cannot be had, the form shows the error and takes a typed name.
- `GET /api/user-libraries` adds `pwOffered` and `pwWhy`. On a hosted Studio, pw ssh still shows only when it can be used.

The form no longer uses `/api/agent-sites` (active clusters only) for this list. The workflow form still does.

Tests: new `server/test/pwSystems.test.mjs` covers the cases with no CLI, with no sign-in, the order and key of the list, and a refused list. New web tests cover the reason with Model access, a stopped system and a typed name, a failed list, and the choice on the libraries page. Server suite 483/483, web suite 204/204, web build clean.

Not verified against a live platform: the status values other than `active` and `off`. Any other status is shown as it comes and counts as not running.

### fix(access): find the pw CLI the macOS app's server misses, and say which credential each part uses (#537)

The macOS app's server is started by launchd with a short PATH. A pw CLI installed where only the person's shell looks was not found, which caused four visible problems:

- The Workflows page failed with `spawn pw ENOENT`.
- The pw ssh choice for libraries stayed hidden, since it is offered only when the CLI is found.
- pw CLI sign-ins could not be renewed (`pw auth token --print`), so a device sign-in read inactive until the CLI in a terminal renewed the credentials file.
- After an update, models were unavailable until that happened.

Changes:

- `findPwCli` (config.ts) looks for the CLI in this order: `PW_CLI`, a location chosen in Model access (`INDEX_BASE/pw-cli.json`), `pw` on PATH, then `~/.local/bin`, `~/bin`, `~/pw`, `/opt/homebrew/bin`, `/usr/local/bin` and `/usr/bin`. `PW_CLI` becomes a live binding, so every caller runs the program found. It is looked for again when Model access loads (`?fresh=1` on Re-check), when a renewal or a Workflows call finds the program gone, and in `pwCliFound`.
- `deploy/mac/studio-run.sh` puts the login shell's PATH (`$SHELL -ilc`, five seconds at most) ahead of the defaults.
- `PUT /api/access/cli/path` takes a full path, or null to forget it. It refuses a program that does not answer `--version` as the CLI does. Sign-in off only.
- Model access gains a "What each part uses" table: models, the Workflows page and platform tools, libraries on ACTIVATE systems, and subscriptions. Each row names its credential, with a "pw CLI not found" badge where the CLI is needed and missing. The pw CLI tab shows where the CLI runs from, or, when it is not found, where the Studio looked and an input for its location.
- The Workflows page and renewal errors say the CLI cannot be found and how to fix it, rather than `spawn pw ENOENT`.
- HELP, REFERENCE, LIBRARIES and MACOS docs are updated.

Tests: the server suite passes (480/480); the new test covers the lookup order, choosing, refusing and forgetting a location. The web suite passes (202/202), with tests for the table and the location input. Not verifiable here: the login-shell PATH on a real Mac. The function was exercised with bash on Linux.

### Change log through v1.155 (#538)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.155` before tagging v1.155.

## v1.154 (2026-10-22)

### Indexing: phased parallel jobs, one status everywhere, stop, delete, log, and PDFs out of the box on the Mac (#530)

Indexing as jobs with phases, run side by side, with one status for every view.

**What changes**

- Every change to the main folder's index (a whole-folder build, one folder after an upload, the background sync) is a job in one queue, with four phases that each count their own work: names (gufi_dir2index), documents (downmark), text (enrich.py) and meaning (embed.py). Each phase runs its work side by side on the thread count in Settings, General, Indexing (one per core by default).
- A first build puts the names in place as soon as they are read, so search by name works within a second; text and embeddings follow. A rebuild works on a copy and swaps it in at the end.
- Text is cached by file date and embeddings by the hash of the text embedded, so updating an index, or pointing at a folder indexed before (its index is put aside, not deleted, when the folder changes), redoes only what changed.
- `GET /api/index/status` is the one answer the footer's index line, the status panel, Settings, Libraries and Stats read; Stats follows a running build and has a refresh button and the record of index runs with each phase's time.
- Stop a running job (its programs too), delete the index after an inline confirmation (never the files), view the index log from the footer's index panel, Libraries and Stats.
- Settings, General, Indexing: threads, which readers run, OCR, image descriptions by the vision model chosen under AI features, embeddings, and where the index is kept.
- The macOS app's Python carries pypdf (1.8 MB), so PDFs downmark's binary cannot read are still read without poppler; an installed LibreOffice.app reads .ppt, .xls and OpenDocument files and renders Office previews. The readiness note lists formats nothing reads, not readers another one covers.

**Measured** on a generated corpus of 17,000 files (6.5 GB: PDFs, Office files, text, photos, diagrams, 1,300 folders) on a shared 20-core Linux host:

| | before | after, 8 threads | after, 16 threads | update, nothing changed |
|---|---|---|---|---|
| names | 0.1 s | 0.08 s | 0.05 s | 0.08 s |
| documents | 361 s | 29 s | 21 s | 0.3 s |
| text | 1,739 s | 163 s | 114 s | 3.3 s |
| meaning | 1,405 s | 920 s | 771 s | 7.1 s |
| total | 3,505 s | 1,112 s | 906 s | 11.4 s |
| searchable by name | at the end | 0.25 s | 0.25 s | |

This draft is open for the mac-app workflow; build_app.sh and check_app.sh --run --launch pass on macOS 15, including a PDF read by downmark's binary and by the bundled pypdf, and a full index built through the server.

### Change log through v1.154 (#533)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.154` before tagging v1.154.

## v1.153 (2026-10-22)

### Icons: the Studio mark on a midnight gradient (#531)

The Studio's icon keeps its mark (three text lines and a sparkle) on a darker, deeper gradient: navy to blue on a diagonal, a soft highlight at the top left, a thin light edge, slightly lighter bars, and a cyan sparkle. It replaces the flat light blue everywhere the icon appears: `web/icons/icon.svg` (the web icon, served as `favicon.svg`, and the empty chat's mark), `web/icons/icon-maskable.svg` (installed web apps and the Apple touch icon), and `deploy/mac/AppIcon.svg` (the macOS app on Apple's grid, and the disk image's volume icon). The PNGs are rendered from those sources at the sizes each file names.

Verified: the full suite; the icons checked at 1024, 512, 192, 180, 32 and 16 px on light and dark grounds and in a Dock-like strip; the Mac build on this pull request.

### Change log through v1.153 (#532)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.153` before tagging v1.153.

## v1.152 (2026-10-21)

### Setup: appearance step, optional AI, background index build; Settings tabs and port (#524)

Setup and Settings changes from review of the macOS app.

**Setup wizard**
- New Appearance step: Light, Dark or System, and the accent, applied as they are picked. The choice is kept in this window and saved as the default theme (System saves `light`, which follows the device's dark setting).
- The AI step says it is optional and can be set up later in Settings, Model access, mentions an ACTIVATE account (there, or as a base URL with an API key here), and when switched off says AI features can be turned back on later.
- The footer's Skip setup is now Finish later (closes setup and records it as done). Skip this step appears on the AI, folder and other-libraries steps while nothing is set there.
- Folder step: in the macOS app's window, Choose another folder… opens the Mac's folder chooser (new `studioShell` message handler); elsewhere the in-page folder list stays. After a folder change it offers the index build. The build never holds a step: Next is available at once and later steps show its state in one line. The step says indexes can be added or changed later in Settings, Libraries.
- The sidebar step points to Meridian for workflow YAML files of one's own.
- Re-entry: every step starts from the values in effect and saves only what is changed; `#view=settings:setup` opens the wizard, used by a new Run Setup… item in the macOS app menu. Help mentions both ways back in.

**Settings, General** is split into tabs (Appearance, Branding, Sidebar, AI features, Indexing, Updates, Server), each with its own address. `#view=settings:general` opens Appearance; the update notice and footer link now open `#view=settings:updates`. The tab row wraps on narrow pages instead of scrolling tabs out of sight. Run setup again sits beside the General heading on every tab.
- Server: the port the Studio and its MCP endpoint (`/api/mcp`) share. MCP has no port of its own. In the macOS app, `POST /api/setup/port` saves `PORT` for the launcher and restarts; the window follows the Studio to the new address. Elsewhere it names the `PORT` variable to set.
- Feature previews: voice says what it does and shows its setup only when switched on (still off by default).
- Workflows settings link to Meridian.

**Defaults**: on a single-user install (sign-in off, not deployed by the platform), the suggested questions (five) and the mission default to ones written for one person's own files. `SUGGESTED_PROMPTS`, `KB_MISSION` and saved settings still take precedence.

**Status panel**: the Model card is hidden while AI features are off. Connections says what it lists and groups rows as the ACTIVATE platform, attached MCP servers, and machines with remote libraries. It is still hidden when there is nothing to list.

**macOS app** (needs the Mac CI build): the `studioShell` handler (folder chooser, follow to a new port), the Run Setup… menu item, and the launcher now restarts a running server that does not answer on the port `studio.env` names.

Tests: wizard steps, skip and finish-later, non-blocking index build, native chooser and fallback, re-entry without writes, the `#view=settings:setup` route, tab addresses with old links, appearance saves, voice details, port save/restart/follow in the server and page, launcher port restart, personal defaults, status panel cards. Docs: HELP, REFERENCE, MACOS, CUSTOMIZATION.

Note: the Meridian repository is private at the moment, so the links return 404 for other people until it is public.

### Change log through v1.152 (#529)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.152` before tagging v1.152.

## v1.151 (2026-10-21)

### Libraries: build or connect, here or on a remote system, and a details card (#527)

Adding a library offered several unrelated forms, building an index meant submitting a workflow to a cluster, and an existing index on another machine could only be connected, not built.

**One Add a library flow.** Where (this computer, or a remote system), then How (build an index of a folder, or connect an existing index); for a remote system, Connect with ssh (`user@host:port`) or pw ssh, with a dropdown of the systems the pw CLI reaches. A connection test checks each step before anything is added.
- Building on this computer (sign-in off) runs the main folder's passes (gufi_dir2index, text extraction, embeddings when the model is installed) as an index job with progress; the index goes in a folder you choose or the Studio's data folder, and the sync re-indexes changed folders. These libraries are read only for now: uploads, renames, deletes, labels and the caches assume the main folder, and making them per library belongs with the indexing changes in progress.
- Building on a remote system first checks that gufi_dir2index and gufi_query are there and says plainly when GUFI is missing; the build runs there detached, survives a restart of the Studio, and extracts text when Python 3.10 or later is present.
- Missing folders are listed by the test and made only after a confirm; the server refuses without it.
- Built libraries show their phase and progress in the list, with Rebuild and Remove.

**Platform workflow builds** are offered only on a hosted Studio (sign-in on, or `PW_PLATFORM_HOST`); a standalone install sees remote systems only.

**Details.** On the Library page, the status panel's first card describes the folder or file last clicked, or the library: size, file and folder counts, latest change, largest types, labels on it and inside it, and the library and index it comes from, from the GUFI index.

Also: at phone width the libraries table's rows no longer overlap.

Verified: the full suite after rebasing on main; 27 new tests (both add flows against ssh and pw stand-ins, create-if-missing with and without the confirm, the remote build job, the details route and card); screenshots of each step and the details card in light, dark and at 400 px, against a server whose ssh and pw stand-ins run real builds with GUFI and Python.

### Change log through v1.151 (#528)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.151` before tagging v1.151.

## v1.150 (2026-10-21)

### Update notice: one line, with the changes in a dialog, so the chat keeps its room (#521)

The update notice listed every newer version's changes, and for a clone the update commands, above every page: about 325 px at an 820 px window, so the chat's thread lost 40% of its height and on a new chat the composer and starters were pushed down to the bottom edge. In the macOS app, with several versions to list, it was taller still.

The notice is now one line: the version, the version running, What's new (with the number of changes), Update now in the app, and Later. What's new opens a dialog with the changes newest first (each opening to its description), the commands for a clone, or the reason the app cannot install, with Update now and Later in its footer. The once-after-an-update notice is one line too, with how many changes came since and What's new.

Measured at 1300×820 with a long thread: the thread's visible height is 767 px with the notice (495 before, 820 without one), the composer stays at the bottom, and a new chat's composer sits in the middle again.

Verified: the notice tests (13), the full suite, and screenshots in light, dark and at 400 px.

### Mac title bar in the page's color, About with Parallel Works, a quick start in Help, and SQL for saved queries (#522)

The macOS window's title bar now takes the color an installed web app's title bar takes, the Studio's own mark shows in the empty chat when no icon is uploaded, an About page says who builds the Studio, Help opens with a short quick start, and a saved query shows the SQL it runs.

**macOS title bar.** The window's title bar follows the color the page names in its `theme-color` tag, which is the color the installed web app's title bar takes (`webManifest` in `server/src/routes.ts`): the classification banner's when one is set, otherwise the page's ground in its light or dark theme. The shell observes `WKWebView.themeColor`, so a theme toggle, a change of system appearance, or a new banner color repaints the bar while the app is open; the last color is kept in the app's defaults so the next launch opens with it. The bar is transparent over the window's background color, and the title and window controls take the light or dark appearance that reads on the color (the WCAG rule the banner text uses). That appearance is set on the title bar view only, since the page follows the window's appearance for its own light and dark. Popped-out session windows do the same.

**About.** The app menu's About ACTIVATE Studio shows the standard panel with "Built and maintained by Parallel Works" (linked to parallelworks.com) as its credits, and `Info.plist` carries `NSHumanReadableCopyright` from `NOTICE`; `check_app.sh` checks it. In the web app, Help has an About page at the foot of its list, also opened from the footer's version panel: the version and commit, the license, the source repository, GUFI, and the same line.

**Empty chat.** Without an uploaded icon, the empty chat shows the Studio's mark (`/favicon.svg`) above its greeting, where an uploaded icon shows.

**Help.** The overview opens with a quick start of about 240 words: the problem, what the Studio does about it, why it works the way it does, and four first steps. On a phone, Help's groups now lay their pages along the strip of pages instead of hanging below it.

**Saved query SQL.** `POST /api/query/sql` takes the body `POST /api/query` takes and answers the query's `gufi_query` stages (`-I`, `-K`, `-E`, `-J`, `-G`, or `-E` alone for raw SQL) and its command for each library in scope, as a run would record it, without running anything. The code button beside a saved query shows them, each with a copy button.

**Verified.** `pnpm test` passes, with new tests for the compiled SQL (stages for each query form, the command matching what a run records, a cluster library's command), the saved query's SQL panel, the default chat icon, and About from Help and the footer. Screenshots in light and dark at desktop and phone widths of the empty chat, Help's overview and About, the version panel, and the SQL panel. The page reports `#f4f4f5` in light and `#111214` in dark, switching when the system appearance changes, and the banner's color when one is set. `build_app.sh` run on Linux writes `NSHumanReadableCopyright` and passes `check_app.sh`. The native window cannot be captured on CI; this pull request's macOS build compiles the shell and runs the app's checks.

### Model access: where it comes from, pw CLI contexts and sign-in, and working offline (#525)

Model access did not say where models and platform access came from. With the pw CLI signed in, chat used the CLI's credential while Settings showed nothing, and the Workflows page appeared only some time after Save settings.

**Where access comes from.** Model access names the source in use (a stored key, an endpoint, the deployment credential, or the pw CLI and its context) and, without sign-in, lists the CLI's contexts: use one, or follow whatever `pw context use` sets. The choice moves the gateway to that platform and sets `PW_CONTEXT` for every `pw` command the server runs, so chat, the Workflows page and the platform tools agree. An expired CLI sign-in is renewed through `pw auth token --print --context <name>`.

**Workflows appearing late.** A change in Model access now announces itself, and the app configuration, footer and chat re-read it, so Workflows appears without a reload. With sign-in off, a key saved in Model access now reaches the Workflows page and sessions too.

**Signing in.** The platform's device authorization is the pw CLI's own client, so Sign in runs `pw auth login --device <host>` and shows its address and code. ChatGPT plans and GitHub Copilot connect through the platform's own provider connections with the user's platform credential; no other product's OAuth client is used. GitHub Models is an endpoint preset with a personal access token.

**Offline.** No network reads as one plain sentence with short timeouts: the model list and health say offline (a local model server keeps working), the Workflows page checks the platform answers before running `pw`, MCP listing is limited to 5 seconds a step, and the update check says it will try again later.

Verified: 450 server and 159 web tests, including blocked-network tests (ENOTFOUND, ECONNRESET, timeouts) and the context and sign-in flows against stand-ins; the context listing and switching against a real pw CLI; screenshots of each state in light, dark and at 400 px. Together with #521 and #522 merged locally, the full suite passes (454 server, 167 web).

### Change log through v1.150 (#526)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.150` before tagging v1.150.

## v1.149 (2026-10-21)

### ci: bump the actions group across 1 directory with 2 updates (#476)

Bumps the actions group with 2 updates in the / directory: [step-security/harden-runner](https://github.com/step-security/harden-runner) and [actions/setup-node](https://github.com/actions/setup-node).

Updates `step-security/harden-runner` from 2.22.0 to 2.22.1
<details>
<summary>Release notes</summary>
<p><em>Sourced from <a href="https://github.com/step-security/harden-runner/releases">step-security/harden-runner's releases</a>.</em></p>
<blockquote>
<h2>v2.22.1</h2>
<h2>What's Changed</h2>
<ul>
<li>Fixed security rules not initializing on GHES self-hosted runners</li>
<li>Fixed GHES connectivity in block mode by implicitly allowing the GHES hostname</li>
</ul>
<p><strong>Full Changelog</strong>: <a href="https://github.com/step-security/harden-runner/compare/v2.22.0...v2.22.1">https://github.com/step-security/harden-runner/compare/v2.22.0...v2.22.1</a></p>
</blockquote>
</details>
<details>
<summary>Commits</summary>
<ul>
<li><a href="https://github.com/step-security/harden-runner/commit/ccd8616d44fd3846e67624a50d5aad6d37bf2d25"><code>ccd8616</code></a> Merge pull request <a href="https://redirect.github.com/step-security/harden-runner/issues/704">#704</a> from h0x0er/jatin/ghes-endp</li>
<li><a href="https://github.com/step-security/harden-runner/commit/037ccfd0b8acd0743d7ce4b3e53f67dc16f831b9"><code>037ccfd</code></a> implicitly add ghes endpoint in allowList</li>
<li><a href="https://github.com/step-security/harden-runner/commit/e7438c7b4d9fde75004beb1805ca407c067d5340"><code>e7438c7</code></a> Merge pull request <a href="https://redirect.github.com/step-security/harden-runner/issues/703">#703</a> from h0x0er/jatin/ghes-codebuild-fixes</li>
<li><a href="https://github.com/step-security/harden-runner/commit/afb0b33c8978810916155d52997450fe76eea40a"><code>afb0b33</code></a> fixes for armour rules not getting initialised</li>
<li>See full diff in <a href="https://github.com/step-security/harden-runner/compare/351661ca32ac09a36dc5ee2d536e3128f2a3c8ed...ccd8616d44fd3846e67624a50d5aad6d37bf2d25">compare view</a></li>
</ul>
</details>
<br />

Updates `actions/setup-node` from 7.0.0 to 7.1.0
<details>
<summary>Release notes</summary>
<p><em>Sourced from <a href="https://github.com/actions/setup-node/releases">actions/setup-node's releases</a>.</em></p>
<blockquote>
<h2>v7.1.0</h2>
<h2>What's Changed</h2>
<h3>Enhancements:</h3>
<ul>
<li>Add support for mise.toml file by <a href="https://github.com/gperdomor"><code>@​gperdomor</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1421">actions/setup-node#1421</a></li>
<li>Support absolute paths in node-version-file by <a href="https://github.com/v-mahabaleshwars"><code>@​v-mahabaleshwars</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1633">actions/setup-node#1633</a></li>
</ul>
<h3>Bug fixes:</h3>
<ul>
<li>Add Node version validation and manifest fetch retry by <a href="https://github.com/v-jitenderpalsingh"><code>@​v-jitenderpalsingh</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1580">actions/setup-node#1580</a></li>
</ul>
<h3>Documentation updates:</h3>
<ul>
<li>README.md: bump actions/checkout to latest version 7 by <a href="https://github.com/deining"><code>@​deining</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1578">actions/setup-node#1578</a></li>
<li>docs: add V7 ESM migration and NODE_AUTH_TOKEN behavior change by <a href="https://github.com/v-gowridurgad"><code>@​v-gowridurgad</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1593">actions/setup-node#1593</a></li>
<li>Fixed cache recommendation syntax by <a href="https://github.com/SubJunk"><code>@​SubJunk</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1603">actions/setup-node#1603</a></li>
</ul>
<h3>Dependency updates:</h3>
<ul>
<li>Update workflow pins and migrate Node matrices to modern versions by <a href="https://github.com/v-HarithaVattikuti"><code>@​v-HarithaVattikuti</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1583">actions/setup-node#1583</a></li>
<li>Upgrade  <code>@​actions/cache</code> to 6.2.0 by <a href="https://github.com/philip-gai"><code>@​philip-gai</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1584">actions/setup-node#1584</a></li>
<li>Pin <code>@​types/node</code> to v24 by <a href="https://github.com/v-priyagupta108"><code>@​v-priyagupta108</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1585">actions/setup-node#1585</a></li>
<li>Override brace-expansion to 5.0.8 by <a href="https://github.com/v-priyagupta108"><code>@​v-priyagupta108</code></a> with <a href="https://github.com/Copilot"><code>@​Copilot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1599">actions/setup-node#1599</a></li>
<li>Upgrade  actions/checkout from 7.0.0 to 7.0.1 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1598">actions/setup-node#1598</a></li>
<li>Upgrade  prettier from 3.8.4 to 3.9.6 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1590">actions/setup-node#1590</a></li>
<li>Upgrade  <code>@​vercel/ncc</code> from 0.44.0 to 0.45.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1610">actions/setup-node#1610</a></li>
<li>Upgrade  pnpm/action-setup from 6.0.9 to 6.0.10 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1602">actions/setup-node#1602</a></li>
<li>Upgrade  baseline-browser-mapping from 2.10.38 to 2.11.22 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1629">actions/setup-node#1629</a></li>
<li>Upgrade <code>@​types/semver</code> from 7.7.1 to 7.8.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1616">actions/setup-node#1616</a></li>
<li>Upgrade <code>@​typescript-eslint/parser</code> from 8.62.0 to 8.70.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1615">actions/setup-node#1615</a></li>
<li>Upgrade pnpm/action-setup from 6.0.10 to 6.1.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1631">actions/setup-node#1631</a></li>
<li>Upgrade jest-each from 30.4.1 to 30.5.2 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1636">actions/setup-node#1636</a></li>
<li>Upgrade eslint-plugin-n from 18.1.0 to 18.3.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1638">actions/setup-node#1638</a></li>
<li>Update dependencies and add license files by <a href="https://github.com/v-HarithaVattikuti"><code>@​v-HarithaVattikuti</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1642">actions/setup-node#1642</a></li>
<li>Upgrade eslint from 10.5.0 to 10.11.0 and package.json from 7.0.0 to 7.1.0 by <a href="https://github.com/dependabot"><code>@​dependabot</code></a> in <a href="https://redirect.github.com/actions/setup-node/pull/1635">actions/setup-node#1635</a></li>
</ul>
<h2>New Contributors</h2>
<ul>
<li><a href="https://github.com/v-jitenderpalsingh"><code>@​v-jitenderpalsingh</code></a> made their first contribution in <a href="https://redirect.github.com/actions/setup-node/pull/1580">actions/setup-node#1580</a></li>
<li><a href="https://github.com/philip-gai"><code>@​philip-gai</code></a> made their first contribution in <a href="https://redirect.github.com/actions/setup-node/pull/1584">actions/setup-node#1584</a></li>
<li><a href="https://github.com/gperdomor"><code>@​gperdomor</code></a> made their first contribution in <a href="https://redirect.github.com/actions/setup-node/pull/1421">actions/setup-node#1421</a></li>
<li><a href="https://github.com/SubJunk"><code>@​SubJunk</code></a> made their first contribution in <a href="https://redirect.github.com/actions/setup-node/pull/1603">actions/setup-node#1603</a></li>
</ul>
<p><strong>Full Changelog</strong>: <a href="https://github.com/actions/setup-node/compare/v7.0.0...v7.1.0">https://github.com/actions/setup-node/compare/v7.0.0...v7.1.0</a></p>
</blockquote>
</details>
<details>
<summary>Commits</summary>
<ul>
<li><a href="https://github.com/actions/setup-node/commit/949feb2413d6458794dcd2491c4babbbce0c15c1"><code>949feb2</code></a> Bump eslint from 10.5.0 to 10.11.0 and package.json from 7.0.0 to 7.1.0 (<a href="https://redirect.github.com/actions/setup-node/issues/1635">#1635</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/7207c4f1189c7ee83ddf20c7c68bd1af6fdd751c"><code>7207c4f</code></a> chore: update dependencies and add license files (<a href="https://redirect.github.com/actions/setup-node/issues/1642">#1642</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/54337e0d02de9202d0948aaff36ad556375497b9"><code>54337e0</code></a> Bump eslint-plugin-n from 18.1.0 to 18.3.0 (<a href="https://redirect.github.com/actions/setup-node/issues/1638">#1638</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/fb54fa8c14c63410cc752f0ae3b7b3ca6af161fc"><code>fb54fa8</code></a> Bump jest-each from 30.4.1 to 30.5.2 (<a href="https://redirect.github.com/actions/setup-node/issues/1636">#1636</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/7f84f1d52d0c56afb45c62fee31a93b843cea4d6"><code>7f84f1d</code></a> Bump pnpm/action-setup from 6.0.10 to 6.1.0 (<a href="https://redirect.github.com/actions/setup-node/issues/1631">#1631</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/0c54c7b07420dcba69a7d832587f55ebdf771c9e"><code>0c54c7b</code></a> Bump <code>@​typescript-eslint/parser</code> from 8.62.0 to 8.70.0 (<a href="https://redirect.github.com/actions/setup-node/issues/1615">#1615</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/a8294190049a9c296f01b9a432aff41dd83d8284"><code>a829419</code></a> Bump <code>@​types/semver</code> from 7.7.1 to 7.8.0 (<a href="https://redirect.github.com/actions/setup-node/issues/1616">#1616</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/680d1e489b82f1243de15bc7665c0007e3c9a860"><code>680d1e4</code></a> Support absolute paths in node-version-file (<a href="https://redirect.github.com/actions/setup-node/issues/1633">#1633</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/db0fb3a98ed314b69026b6e8c9be26a7e2fe65a8"><code>db0fb3a</code></a> Bump baseline-browser-mapping from 2.10.38 to 2.11.22 (<a href="https://redirect.github.com/actions/setup-node/issues/1629">#1629</a>)</li>
<li><a href="https://github.com/actions/setup-node/commit/94196ee1d15439c1b6651cd87ef14e88ec435966"><code>94196ee</code></a> Bump pnpm/action-setup from 6.0.9 to 6.0.10 (<a href="https://redirect.github.com/actions/setup-node/issues/1602">#1602</a>)</li>
<li>Additional commits viewable in <a href="https://github.com/actions/setup-node/compare/820762786026740c76f36085b0efc47a31fe5020...949feb2413d6458794dcd2491c4babbbce0c15c1">compare view</a></li>
</ul>
</details>
<br />

### chore(deploy): bump base image digests (#435)

The pinned base images in `deploy/app.def` have new digests upstream, usually Node or Debian security fixes. Opened by the base-images workflow; ci and container run on this branch by dispatch.

### notarize.sh: retry stapler validate, which a dropped connection to Apple fails (#519)

The v1.148 macOS build was notarized, its ticket stapled, and Gatekeeper accepted the app and the disk image, but `stapler validate` on the disk image lost its connection to Apple's ticket service ("The network connection was lost", error 68) and failed the sign job. Validation fetches the ticket again to compare, so it is now tried up to five times with a growing wait, as stapling already is.

Verified: `bash -n`; the sign job uses the scripts at the release's tag, so the next release with a macOS build exercises it.

### UI package 0.89; treemap labels readable on light cells (#520)

@parallelworks/ui 0.84.2 to 0.89.0. The releases in between fix the chat input losing focus after the first message, password managers offering to fill the chat input, and a held call whose answer the server refuses losing its choices; status dots become the lit lamp; chart series take colors in a fixed order; lists gain filter toggles. The bundle is 6 KB larger in total.

Every view was compared against v1.148 in light and dark on the same data: the differences are the status dots. The comparison also showed the corpus treemap's white labels hard to read on the palette's lighter cells; each label now takes dark or white text by contrast with its cell. The treemap reads the accent as the browser resolves it, since a theme may define it as `light-dark(...)` rather than a hex value.

Verified: 420 server and 151 web tests (one new, for the label ink); the screenshot tour in both themes with no console errors.

### Change log through v1.149 (#523)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.149` before tagging v1.149.

## v1.148 (2026-10-21)

### Mac app: signed with the Developer ID certificate, notarized and stapled for releases (#516)

The macOS app was signed ad hoc, so a copy downloaded with a browser, or dragged from the disk image, was blocked until the user found Open Anyway in System Settings. A release build now signs the app with the Developer ID certificate, under the hardened runtime, notarizes the disk image, and staples the ticket to the image and the app, when the repository has the signing secrets; macOS then opens it with only its usual question about an app from the internet. Without the secrets, the release carries the ad hoc app as before.

**Scripts.** `deploy/mac/sign_app.sh` signs every Mach-O file in the bundle (42: Node, Python and its libraries, GUFI, the document converter), then the bundle, with `--options runtime --timestamp` and the entitlements in `deploy/mac/entitlements`, and checks every signature. `deploy/mac/make_dmg.sh` is the disk image code moved out of `build_app.sh`, which calls it and keeps its ad hoc seal. `deploy/mac/notarize.sh` signs the image, submits it with `notarytool --wait`, prints the notary log on rejection, staples the image and the app, and checks them with `stapler validate` and `spctl`. `deploy/mac/keychain.sh` holds the certificate in a temporary keychain.

**Entitlements.** Node: `allow-jit` and `allow-unsigned-executable-memory`. The window: `device.audio-input`, for the chat's voice input. Python, GUFI and the converter: none. Nothing has `disable-library-validation`, since every library the bundled programs load is in the bundle and signed by the same team.

**Workflow.** A `sign` job runs after `build` for a release dispatch when `vars.APPLE_TEAM_ID` is set. It checks out only those scripts and the entitlements at the release's tag, signs and notarizes the build's app, packs the archive from the stapled app, runs `check_app.sh --run --launch` on it, and uploads the signed files; `release` attaches them. It is the only job that sees the signing secrets. When a secret is missing, or the tag predates these scripts, it signs nothing and `release` attaches the ad hoc app.

**Pull requests.** A `hardened` job signs a copy of the app the same way with a throwaway self-signed identity and runs it. That catches a file left unsigned, wrong entitlements, or a signed app that fails to run or that running changes. It cannot catch a missing entitlement: on the macos-15 runner, macOS treats a signature without Apple's certificate chain as ad hoc and enforces neither the hardened runtime nor library validation for it (Node signed without `allow-jit` still compiled code, and `DYLD_INSERT_LIBRARIES` was honored). Only the `sign` job's run with the certificate shows that.

**Bytecode caches.** Python wrote about 70 `.pyc` files into the app on first use, which breaks the signature's seal. The run loop now sets `PYTHONDONTWRITEBYTECODE`, and `check_app.sh --run` ends by checking that the app still verifies.

**Docs.** `docs/MACOS.md` describes the signed and notarized app, keeps the Open Anyway steps for ad hoc builds, and lists the scripts. `CONTRIBUTING.md` lists the five secrets and the variable, how to create each, and the `gh` commands to set them.

Verified: with the real certificate and API key, the `sign` job (run from this branch through a temporary commit that let it run without a release, since dropped; no release was touched) signed the app, had the disk image notarized (Accepted, about 30 minutes for the team's first submission), stapled both, and `spctl` accepted the app and the image as `source=Notarized Developer ID`; `check_app.sh --run --launch` passed every check on the signed app ([run](https://github.com/parallelworks/activate-studio/actions/runs/39618053188)). This pull request's build and `hardened` jobs pass ([run](https://github.com/parallelworks/activate-studio/actions/runs/39623868324)); the runner probes are [here](https://github.com/parallelworks/activate-studio/actions/runs/39614182064) and [here](https://github.com/parallelworks/activate-studio/actions/runs/39616562185). 420 server and 150 web tests pass.

Not covered: the app inside the disk image carries no ticket of its own, so a copy dragged out of the image is checked with Apple's servers on first open. The archive the installer and updates use has the stapled app.

### Change log through v1.148 (#518)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.148` before tagging v1.148.

## v1.147 (2026-10-21)

### Mac app: built for the releases it is asked for, with the newest at a fixed macos release (#515)

Releases go out several times a day, and building, signing and attaching the macOS app to every one of them would make Mac users update as often. The macOS build now runs only for the releases it is asked for, and installed apps update to the newest release that has the app.

**Workflow.** `mac-app.yml` no longer runs on every published release. Run with a `release` input (`gh workflow run mac-app.yml -f release=vX.Y`, or the Actions tab), it builds that tag on a Mac, signs the update manifest, and attaches the app to that release. It also puts the same files in the `macos` release, a prerelease that is never latest and that the app's update check skips, which always holds the newest app; a build for an older version than the one there leaves it alone. Pull requests that change the macOS app still build and check it.

**Fixed address.** The installer, the README and `docs/MACOS.md` point at `releases/download/macos/…` instead of `releases/latest/download/…`, which would stop working on the first release without the app.

**Updates.** The app already takes the newest release that carries the signed app. Its sooner check (30 minutes instead of six hours) now applies only while a release without the app is less than two hours old, since most such releases will never get one. The update notice lists the changes of the releases in between.

**Docs.** CONTRIBUTING.md says how to build the app for a release; `docs/MACOS.md` says not every release carries the app.

Verified: 419 server and 150 web tests, including a test that an older release without the app does not bring the next check forward.

### Change log through v1.147 (#517)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.147` before tagging v1.147.

## v1.146 (2026-10-21)

### Change log through v1.145 (#513)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.145 release.

### Mac app: GUFI from its published release, and a page on building GUFI for macOS (#511)

The app's macOS build took GUFI from a gufi-macos workflow run, whose archive GitHub deletes after 30 days (2026-11-20). The archive is now published as the prerelease `gufi-1713537-macos14`, built and checked by the `gufi-macos` workflow on its tag (macOS 15 build, macOS 14 check), and `mac-app.yml` takes it from there (`GUFI_RELEASE`). The run input stays for trying a new GUFI build.

`docs/GUFI-MACOS.md` is new: the published archive and its contents and versions, how to use it outside the app, how to build and check it, what each step of `build_gufi.sh` does and why, and how a new archive is published. `docs/MACOS.md`'s GUFI section now points to it, and the README lists it.

Verified: the Mac build on this pull request takes GUFI from the release.

Also: the build names its version with `git describe --tags`, which now also matches the `gufi-*` tag, so a commit whose nearest tag is the GUFI one would be named after it (and report `dev`). `server/src/version.ts`, `deploy/make_bundle.sh` and `deploy/mac/build_app.sh` now pass `--match 'v*'`.

### Updates: a notice for new versions, What's new in the app, and the change log in every build (#512)

A newer version was announced only by a small link in the footer, and the change log existed only in the repository.

**Update notice** (the macOS app and a clone; never a hosted deployment). When a newer version is available, a notice at the top of every page names it and lists the changes in every version since the running one, newest first, each title opening to its description. In the app it offers Update now (or says why it cannot install, with the release page), Later and Settings, and shows the install's progress in place; a clone gets the update commands. Later hides it for that version; a newer version shows it again. Automatic install stays an option that defaults to off.

**What's new.** The change log ships in every build (the macOS app, the workflow bundle and the container image) and is served by `GET /api/changelog`. Help has a What's new page, also reached from the footer's version panel and Settings, Updates.

**After an update.** The first time the app opens on a newer version than this browser last saw, a notice lists the changes since then, from the bundled change log, once.

**Server.** `server/src/changelog.ts` parses the change log and release notes into versions with `{title, pr, body}` entries, leaving out "Change log through" entries. `GET /api/updates` gains `releases` (every release newer than the running one up to the one it would install, including one whose macOS build failed) and `command` (the update commands, for a clone only). The install flow moved into one shared store (`web/src/updateFlow.ts`), so the notice, the footer and Settings follow one install.

**Release order.** `scripts/release-notes.mjs --changelog --next vX.Y` writes the untagged commits as vX.Y, so a release regenerates the change log before it tags and the version's own build carries its notes. CONTRIBUTING.md describes the order.

Verified: 419 server and 150 web tests (notes parsing, failed-build versions, a missing change log, the notice per mode, Later per version, install progress, What's new, the after-update notice); CHANGELOG.md checked present in the app, the bundle and the image; screenshots in light and dark at desktop and phone width.

### Change log through v1.146 (#514)

Regenerated with `node scripts/release-notes.mjs --changelog --next v1.146` before tagging v1.146.

## v1.145 (2026-10-21)

### Change log through v1.144 (#510)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.144 release.

### Mac app: sign the bundle as a whole, so a downloaded copy is not reported as damaged (#509)

The app's programs were signed one by one, but the bundle itself had no signature (no `Contents/_CodeSignature`). Opened from a browser download, which carries the quarantine flag, macOS reported the app as damaged and offered no way to open it.

`build_app.sh` now signs the bundle ad hoc as its last change on a Mac, which seals the Info.plist and every file in it. Before that it removes links that lead nowhere: pnpm leaves one in `.pnpm/node_modules` for the workspace's own server package, pointing at the source checkout the app was built from, and a signature check of the bundle fails on it. `check_app.sh` checks for such links on any host and, on a Mac, verifies the seal with `codesign --verify --deep --strict`, printing codesign's reason when it fails.

A downloaded copy is still not signed by an identified developer, so its first open goes through Privacy & Security, Open Anyway, as `docs/MACOS.md` describes; the docs now say that releases up to v1.144 are reported as damaged and that clearing the quarantine flag opens them.

Verified: the Mac build on this pull request (both new checks pass, and the app runs from its run loop and LaunchAgent).

## v1.144 (2026-10-21)

### Change log through v1.143 (#507)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.143 release.

### Updates: the newest release that carries the signed app (#508)

A release is published before its macOS build attaches the app, about ten minutes later, and a release whose build fails never gets it. The app took the newest release regardless, so a check in that window, or after a failed build, reported that the release has no signed macOS app and offered nothing until the next check six hours later.

The app now takes the newest release that carries the signed app (archive, manifest and signature), and while a newer release is still waiting for its build it checks again after 30 minutes. A clone is still told of the newest release, since it updates with git.

Verified: 412 server and 137 web tests, including a check that sees a release without the app, offers the one before it, and finds the new one on the sooner check; that test fails on the old code.

## v1.143 (2026-10-21)

### Change log through v1.142 (#505)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.142 release.

### Icons: a drawn Studio icon for the app, the disk image and web apps (#506)

The app, the disk image and installed web apps showed a plain letter S, and the disk image's volume had no icon at all, so Finder showed a generic disk in its title bar and sidebar.

**Icon.** A drawn icon (three text lines and a sparkle on a blue rounded square), from SVG sources:
- `web/icons/icon.svg` renders `icon-192.png`, `icon-512.png` and is served as `favicon.svg`.
- `web/icons/icon-maskable.svg` fills the square with the mark inside the crop-safe circle, for `icon-maskable-512.png` and `apple-touch-icon.png`.
- `deploy/mac/AppIcon.svg` is the macOS icon on Apple's grid (an 824-pixel rounded square on a 1024 canvas, with a shadow), rendered to `deploy/mac/AppIcon-1024.png`.

**Mac app.** `build_app.sh` makes the `.icns` from the 1024-pixel image: every size with `sips` and `iconutil` on a Mac, the single 1024-pixel entry elsewhere. `check_app.sh` checks that the app's `.icns` holds the 1024-pixel image.

**Disk image.** `build_app.sh` puts the app's icon at the volume root as `.VolumeIcon.icns` and sets the custom-icon Finder flag on the mounted volume (made writable, then compressed), and fails the build if the finished image lacks either.

Also: the automatic-install update test allowed no slack for the server's idle poll, so it failed now and then; it now allows one poll interval.

Verified: 410 server and 137 web tests; the icons at 16 to 1024 pixels; the Mac build on this pull request.

## v1.142 (2026-10-21)

### Change log through v1.141 (#503)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.141 release.

### Chat: platform storage and cluster tools, with changes approved first (#504)

The chat gains the platform tools the platform's own web chat has and the Studio lacked, with the same names and descriptions: `list_storage`, and five that change something, `start_cluster`, `resume_cluster`, `stop_cluster`, `destroy_cluster` and `cancel_workflow_run`. They call the platform API with the viewer's own key (`server/src/chat/platformTools.ts`). Results keep only the listed fields and are capped at 16 KB; a failure passes back the platform's own message.

**Approval** (`server/src/chat/approvals.ts`)
- A call to a changing tool does not run. The server holds it with the conversation, the tool call, the tool, its arguments in canonical form, a hash of the tool's definition, and the viewer, then pauses the reply.
- The client's answer names the call and whether it is allowed; the server runs what the hold records, once, and the reply continues with the result. Deny tells the model the user declined, with their note if they wrote one.
- Refused: an answer from another viewer or from nobody, changed arguments, an alternative the hold does not allow, an expired hold (30 minutes), or a tool whose definition changed since the hold.
- `stop_cluster` and `destroy_cluster` each offer the other in their approval, with the same arguments.
- The changing tools are offered only to a verified person with their own platform key, in their own conversation. The OpenAI-compatible endpoint, MCP, and the general tool runner never run them.
- The cluster tools' organization comes from the viewer's platform account, never from the model.

**UI.** The approval uses the chat package's own approval block (`ToolApproval.tsx`), with a button under it for the alternative, which the block has no control for. Settings lists the changing tools under a "Platform changes" switch marked as asking first.

**Verified:** 410 server and 137 web tests, including hold, approve, deny, run instead, changed arguments, expiry, changed definition, another viewer, someone else's conversation, no key or no person, and field projection. `list_storage` and the account lookup were run read-only against a platform. The changing tools were run only against a stub.

## v1.141 (2026-10-21)

### Change log through v1.140 (#502)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.140 release.

### Standalone macOS Studio: app, setup, libraries over ssh, updates (#494)

A Studio someone downloads and runs on a Mac without a platform account: install it, open it as an app, set it up in a first-run wizard, index a local folder (read/write), and connect GUFI indexes on other machines over ssh (read-only). Draft until it has run on a Mac by hand.

**macOS app** (`deploy/mac`)
- `ACTIVATE Studio.app` carries Node, GUFI, a standalone Python for the indexer, and the embedding model. GUFI is built for macOS 14 by `build_gufi.sh` (below); GUFI's own macOS package, relinked inside the app, is the fallback.
- A LaunchAgent keeps the server on 127.0.0.1, restarts it on exit 75 (setup changed the folder), and backs off after a crash.
- Built on a Mac, the app is its own window (`shell/StudioShell.swift`, WKWebView). It handles file pickers, downloads and pop-outs, and outside links open in the browser. Built elsewhere, it opens a browser's app window.
- `install.sh` installs without admin rights or a quarantine prompt; a .dmg is built on a Mac.

**Setup** (`server/src/setup.ts`, `SetupWizard.tsx`)
- A seven-step first-run wizard on a single-user install, every step skippable: the Studio's name, a light and an optional dark icon (PNG, JPEG, SVG or WebP up to 1 MB, also the tab icon), and the accent; AI features on or off; the sidebar pages; the folder to index, with the first build and its progress; other libraries. Hosted deployments never see it.
- AI features: an OpenAI-compatible base URL and key, with Test listing the endpoint's models or saying what failed, and a default model. The key is optional for a model server that takes none. Off, Chat and Agents leave the sidebar, the chat routes answer without calling a model, and label suggestions and Add to chat leave the Library menu (`aiEnabled`, or `AI_ENABLED=0`).
- Other libraries: an index on this machine (typed or picked), over ssh, or on a running cluster over `pw ssh` when the pw CLI is signed in (`/api/user-libraries/pw`), each with a connection test before it is added.
- Everything in the wizard is also in Settings, and Settings, General, Run setup again reopens it.

**Libraries over ssh** (`remoteExec.ts`, `userLibraries.ts`, `SshLibraryForm.tsx`)
- Plain ssh is a second transport beside `pw ssh`, with a loop portable to macOS hosts.
- An existing GUFI index on another machine is a read-only library that works in the tree, previews, search, Query, Stats and gufi_vt across libraries.
- A connection test checks ssh, finds GUFI off the PATH, and validates the index.
- Refused where users sign in, since the ssh identity is the server account's.

**Libraries page.** One list of every library (the main folder, indexes on this machine, over ssh, built on a cluster), with one Add flow that offers only the kinds the install supports. No platform wording in a standalone install.

**Updates** (`server/src/updates.ts`, `UpdatesSection.tsx`, `sign_release.sh`)
- The app checks this repository's releases and installs only a manifest signed with ed25519. The public key is in the app; the private key is the repository secret `STUDIO_UPDATE_SIGNING_KEY`.
- It checks version, size and SHA-256, then swaps the bundle and restarts the agent.
- It keeps the previous version for Roll back, and rolls back on its own after three failed starts.
- Policy: check, install when idle, channel.
- A clone run with pnpm start is told the update command. Hosted deployments never check.

**GUFI for macOS 14 and later** (`build_gufi.sh`, `check_gufi.sh`, `gufi-macos.yml`)
- GUFI's own package needs macOS 27. `build_gufi.sh` builds GUFI from source with a macOS 14 deployment target, with libomp and pcre2 built for the same floor, relinked to load only from the tree, ad hoc signed, with the bundled components' licenses.
- The vendored llama.cpp needs standard headers it no longer gets through others under LLVM 23, so a compiler wrapper force-includes them. GUFI's install writes an example config to `/etc/GUFI`, so it installs under a `DESTDIR`.
- `mac-app.yml` builds and checks the app on a Mac for pull requests that change it, and for a published release signs the update manifest and attaches the app. It currently takes GUFI from gufi-macos run 39542085829 (`GUFI_RUN`), whose artifact expires on 2026-11-20; a `gufi-*` release of the archive replaces it before this merges.

**Verified:**
- 389 server and 130 web tests.
- A real sshd with two remote indexes beside a local library.
- A real-key signature verified by the key in the app.
- gufi-macos run 39542085829: GUFI built for macOS 14 passed indexing, query, gufi_vt through the shell, embeddings, vector search, and a dyld check that every library loads from the tree or the system, on macos-14 and macos-15 runners.
- mac-app run 39543812184 on macos-15 with that GUFI: the native window compiles and the .dmg builds; the bundled Node, Python and GUFI run; the indexer extracts and embeds; the server starts from the app's run loop, builds a full index, and search finds a file by its text; TERM stops the loop and the server; the launcher loads the LaunchAgent, finds it running when opened again, and --stop stops it.
- The unit tests on macos (ci run 39543812246).

**Not yet verified:**
- An update's swap and the rollback after failed starts, on a Mac (covered by tests with a stand-in launchctl)
- macOS App Management when the app replaces itself
- Opening the .dmg and the window by hand on a Mac

## v1.140 (2026-10-20)

### Change log through v1.139 (#500)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.139 release.

### Status panel: activity, libraries, model and connections at a glance (#501)

A collapsible panel on the right of the Studio shows what is going on without leaving the page. It is toggled from the top right of each page; while it is open, the toggle sits in the panel's own bar. Each card has a one-line summary on its header row and opens for detail. Open and closed states are remembered, and a card with nothing to show is left out.

**Activity**
- Agent tasks that are running or waiting on you, and standing agents. A task opens on the Agents page, a standing agent on the Fleet page (new `#view=agents:fleet`).
- Your workflow runs, running ones first. A run opens in the side view.
- Open sessions, the first six, then a link to the rest.
- Agent rows follow the Agents page's visibility. Run and session rows need Workflows and a platform.

**Libraries.** Each library with its kind, state and file count, the sync line with Sync now, and a link to Settings, Libraries.

**Model.** The chat's selected model, its provider and its health.

**Connections.** The platform, attached MCP servers (from a new cached form of `GET /api/settings/mcp-servers?cached=1`, so polling contacts no server), and the clusters behind your libraries. The header counts what needs attention.

**Layout and load**
- The panel is 300 px. It starts closed below 1200 px wide, overlays the page below 1000 px, and is absent on phones.
- On the chat and Workflows pages it gives way to the run and session side view, and the toggle brings it back.
- It refreshes every 10 s only while open and the tab is visible, from routes the Studio answers from its own state. The platform session list is read once a minute.
- No context meter: the chat package reports tokens per reply, not how full the conversation is.

Tested: web tests for the toggle, each card's summary, visibility rules and opening a run; a server test for the cached route. Checked in a browser in light and dark, on the chat and Library pages, on UI 0.84.2.

## v1.139 (2026-10-20)

### Change log through v1.138 (#498)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.138 release.

### Scrubber: the end of a thread is its last message (#499)

The scrubber's current tick was the last message whose top had reached the top of the thread. The final few messages are shorter than the window and never get there. So at the very end of a 40-message thread the highlight sat on tick 35, and a click on ticks 36 to 40 changed nothing visible, since the thread could not scroll further.

Now:
- at the end, the last message is current;
- a click on one of the final ticks keeps that tick current until the thread is scrolled away from the end.

The rule is a small pure function with unit tests. It was also checked in a browser on a 40-message thread:
- opened at the end: 40 of 40 current (was 35);
- clicked 38 at the end: 38;
- scrolled up 1500 px: the highlight follows;
- back at the end: 40;
- tick 1 still goes to the top.

## v1.138 (2026-10-20)

### Change log through v1.137 (#492)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.137 release.

### Remove a stray Python wheel committed with the UI 0.65 update (#493)

A 9.5 MB zizmor wheel, downloaded while auditing a workflow, was committed at the repository root with #485. Nothing uses it. This removes it and ignores `*.whl`.

### A daily watch on the @parallelworks packages (#495)

The UI package releases several times a day. Dependabot proposes npm updates weekly with a seven-day cooldown, so it would always trail it, and it has not opened an npm pull request in this repository. Its settings are unchanged here.

`foundation-watch` runs once a day and on demand. It reads the `@parallelworks` versions the lockfile resolves and compares them with npm. While the Studio is behind, it keeps one issue labeled `foundation-update` open:
- The issue gives a table of the packages behind, and each package's changelog between the two versions, from the repository its npm metadata names. Breaking changes are called out.
- A newer release updates the issue and adds a comment, so watchers are notified.
- When the lockfile catches up, the issue closes.

The job has `contents: read` and `issues: write` only. It runs behind the same egress allowlist as CI plus raw.githubusercontent.com for the changelogs, and passes nothing from the event into a shell. zizmor reports no findings. CONTRIBUTING describes how to take a release.

Tested with `DRY_RUN=1` against an older lockfile (ui 0.59.0 behind 0.65.0, with the changelog for each release) and against a current one.

### UI package 0.84.2 (#497)

Moves `@parallelworks/ui` from 0.65 to 0.84.2, the release the foundation watch reported in #496. No release in between is marked breaking, and the typecheck needed no changes.

The theme adds a backdrop token, so the shipped theme defaults are regenerated (`UPDATE_THEME_DEFAULTS=1`).

Every view was compared against main on the same data, in light and dark mode, including an open conversation, the library's context menu and the footer's build panel. The only visible difference is relative times: "last week" for "7 days ago", and dates such as "Oct 13" in the conversation list. There were no console errors.

Adopting what the new releases add follows in separate changes:
- chat holding a changing tool call until the reader allows it, for the platform tools;
- tool calls named in words;
- the danger-outline and file-picker buttons;
- toned cards, and the shared context menu.

Closes #496.

## v1.137 (2026-10-19)

### Change log through v1.136 (#490)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.136 release.

### Stats: count a library in one aggregated pass, and keep a cluster library's counts across restarts (#491)

The Stats page was slow for a library on a cluster, enough to look stuck on a large one.

The totals (files, bytes, directories) and the file-type breakdown were two per-directory `gufi_query` runs. Every row came back to the Studio to be summed: one row per directory, and one per directory and extension. Over `pw ssh` to a large cluster library, that transfer was most of the wait.

They are now one query in GUFI's aggregate form (`-I`, `-K`, `-E`, `-J`, `-G`), as the Largest files and Changed lists already were. Each directory adds its counts to a per-thread table, the merge happens where the index is, and a few dozen rows come back. That is one traversal of the index, not two, and almost nothing to transfer. On the test index the one-pass answer equals the two-query answer exactly.

A cluster library's counts were already kept in memory until its next build. They are now also written to `remote-stats.json` beside the index, so the first Stats view after a restart or deploy does not count the library again.

Tested: the cluster library test now requires one `gufi_query` run per count, checks the type breakdown and directory count, and checks the counts reach disk.

## v1.136 (2026-10-19)

### Change log through v1.135 (#488)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.135 release.

### Stats: read the file lists by column name, across libraries too (#489)

With **All libraries** chosen and more than one library mounted, `/api/query` answers with a leading `library` column. The Stats page's Largest files and Changed in the last 7 days lists read their rows by position, so each row showed the library's name where the path goes, "NaN GB" for the size, and the file size where the date goes. A deployment with one library never showed it.

The lists now read the `path`, `bytes` and `modified` columns by name, show the library on hover, and open a file in its own library (`<library>/<path>`, as the Query page does). A test covers both shapes.

## v1.135 (2026-10-17)

### Change log through v1.134 (#486)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.134 release.

### Readers switch the assistant's tools by kind from the composer (#487)

UI 0.65 gives the chat composer a **+** menu with **Tools** when the adapter lists tools. The Studio now lists one switch per kind of tool, not per tool: Knowledge base, Platform workflows, Agents, Editing the library, Extensions, and each attached MCP server. Only kinds this deployment offers appear. The package remembers the choice per reader in the browser and sends the ids left on with each request.

On the server, `GET /api/chat/tool-options` lists the switches, and `/api/chat/stream` takes `tools`:
- A group switched off is not sent to the model, not offered by `more_tools`, and not added because the question mentions its subject.
- A call to a tool switched off does not run; the model is told the reader switched that kind off, and answers accordingly.
- With Knowledge base off, the first-turn search seed is skipped too.
- A request without `tools` gets every tool, as before.

Switching off workflows keeps a chat from launching anything, and switching off editing makes it read only.

Tested: unit tests for the mapping and for `more_tools` offering only what is on. The turn loop was also run against a stand-in model that calls `run_workflow` with only Knowledge base on: the model was offered the eight knowledge base tools, the call did not run, and the model was told why. A screenshot of the menu with all five switches was checked.

## v1.134 (2026-10-17)

### Change log through v1.133 (#484)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.133 release.

### UI package 0.65; the theme test can rewrite its own defaults (#485)

Moves `@parallelworks/ui` from 0.59 to 0.65.

- 0.64 puts the package on the scale the platform's apps share: 32px fields, a 48px header, 16px titles, 12px panels and 16px padding. Every view was compared against v1.133 on the same data in light mode, and the key views in dark, with no breakage or console errors.
- 0.63 shows a run's step summaries as cards under its graph, and 0.64.1 shows a step's whole summary table; both work through the run file route added in v1.132.
- 0.65's derived theme changes the sidebar's active colors, so the shipped theme defaults are regenerated.

The theme defaults test fails whenever a package release changes `deriveTheme`. With `UPDATE_THEME_DEFAULTS=1` it now rewrites the block from the release in use, so a bump no longer needs the block edited by hand.

0.65 also lets readers choose which of the host's tools the chat assistant may use. Adopting that needs the Studio's adapter to list its tools and the server to honor the choice, so it follows in its own pull request.

## v1.133 (2026-10-16)

### Change log through v1.132 (#482)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.132 release.

### Run files behind a stopped workspace: request a start and say so (#483)

The platform serves run files from the user's workspace, which scales down when idle. When it is down, `GET /api/workflow-runs/<slug>/files` answers 400 "User workspace not found or is not running", and the step dialog's route reported that as a missing file.

The route now recognizes that answer (the same matcher launches use), requests one `pw workspace start` per credential at most every three minutes, and answers 503 with an explanation. The step dialog shows that explanation where the log goes; it polls every two seconds, so the log replaces it once the workspace is up. Other platform failures now carry the platform's message instead of reading as a missing file.

Tested: a stopped workspace answers 503 with the explanation and two polls request one start.

## v1.132 (2026-10-16)

### Change log through v1.131 (#480)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.131 release.

### UI library 0.59 and its components throughout the Studio (#481)

Moves @parallelworks/ui from 0.45 to 0.59 and replaces the Studio's hand-built controls with the package's components wherever one fits.

Library update. CopyToClipboard became CopyButton and CopyCodeBlock; streaming parts moved from useChat to useChatStream; chat links gained a newChat target. monaco-editor is added as the package expects; it loads only when an editor opens. The main script grows by about 50 KB and the stylesheet loaded at start shrinks by about 6 KB. The derived theme defaults are regenerated for the new tokens.

Run view. A step in the run graph now opens its summary, its log as it grows, and its script. The package reads these through the provider's useRunFile, bound to a new /api/runs/file route that relays the platform's run file API with the viewer's key and refuses paths outside the run directory.

Components. Across 30 files the Studio now uses the package's Button, IconButton, RefreshButton, Input, Textarea, NumberInput, Select, Autocomplete, ActionMenu, Checkbox, Switch, Callout, EmptyState, Loader, Skeleton, StatusDot, StatusBadge, Tabs, Disclosure, DescriptionList, Table, Popover, Modal, ConfirmModal, the context menu, SearchInput, SegmentedControl, ToggleGroup, Pagination, ProgressBar, CopyButton, CopyCodeBlock, LetterBadge and RunStatusIcon, plus LogViewer for an agent's live output, a read-only Editor for workflow YAML, and EditorField for workflow code inputs. About 600 lines of the Studio's own CSS are removed.

Kept hand-built because the package has no fit: the library tree (drag to move, range selection, label chips per row), the file viewer's source view and find bar, the side view's closable tabs, the full-screen session view, the sidebar and its mobile sheet, the vertical Help and Settings rails, the treemap and 3D viewer, and the SQL box (the code editor would take over Ctrl/Cmd+K and Tab).

Behavior changes worth knowing:
- Deleting files, folders and conversations confirms in a dialog instead of an inline second button.
- New folder and Rename open a small dialog from the right-click menu.
- Transient notes (copied, saved, applied) are toasts.
- A search box clears on Escape.
- Number fields clamp to their range when you leave them.
- The row checkbox in the library tree works from the keyboard.
- The page-wide link color now sits in the package's base layer, so a Button rendered as a link keeps its colors.

Not adopted: @parallelworks/problem and @parallelworks/i18n assume a localized client that shows messages by error code from a catalog. The Studio has no locales and its server errors carry specific English text, so adopting them now would make errors less specific. The Go packages (server, pgdb, spa) do not apply to a Node server.

Checked: typecheck, 312 server and 77 web tests, the production build, and a screenshot tour of every view in light and dark mode against the current release on the same synthetic data, with no console errors.

## v1.131 (2026-10-15)

### Change log through v1.130 (#478)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.130 release.

### Search across libraries: parallel by default, gufi_vt as a setting (#479)

Settings, General gains *Search across libraries*, which picks how a search over several libraries runs. The default runs each library in parallel and merges the results, as before #477. The other option, gufi_vt, runs name matches and bareword full-text matches as one gufi_vt statement per kind, as #477 introduced.

gufi_vt answers its locations one after another, so on local libraries it measured two to three times slower than the parallel path. Running gufi_vt locations in parallel (for example through pdsh) would make the comparison fair and is a possible follow-up. Query is unchanged and uses gufi_vt either way.

`STUDIO_SEARCH_ENGINE=gufi_vt` sets the default for a deployment. Both paths return the same matches; the tests cover the default (no gufi_vt statement) and the setting (gufi_vt statement, same hits as per library).

## v1.130 (2026-10-15)

### Change log through v1.129 (#475)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.129 release.

### Search across libraries runs through gufi_vt (#477)

Query has used `gufi_vt` across libraries since v1.109. Search still ran `gufi_query` per library and merged the results, so cross-library searches never showed `gufi_vt`.

**What runs through gufi_vt now.** For a search across several libraries:
- Name matches run as one `gufi_vt` statement.
- Full-text matches also run as one statement, when every search word is a bareword. `gufi_vt` carries a statement's SQL in double quotes, so fts5's quoted terms can't go through it; `ftsExprBare` writes the same expression with barewords (an uppercase operator word is lowercased), or returns null. A word with punctuation keeps the per-library path.
- A library `gufi_vt` can't reach, and results by meaning, still run per library.

The statement is recorded through `runAcross`, so it shows in the GUFI commands panel as Query's does. The SQL builders (`ftsSql`, `namesSql`) and row mappers are shared with the per-library path.

**Tests.**
- End to end with GUFI installed, on the existing two synthetic trees: one local library and one cluster library through the remote wrapper. Name searches run as `gufi_vt` and return the same hits as the per-library path.
- Bareword expressions.
- On the dev indexes, with caps lifted so both paths return every hit, four searches matched exactly (710, 54, 319 and 43 hits).

**Speed.** `gufi_vt` answers its locations one after another, so these searches took 110 to 190 ms against 40 to 70 ms for the parallel per-library path.

## v1.129 (2026-10-15)

### Change log through v1.128 (#473)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.128 release.

### Chat: steer a running reply; launches fill site settings and place plain-named inputs (#474)

**Steering a running reply.** Until now, a message sent while a reply was running waited in the chat's queue until the reply ended, so there was no way to correct course mid-turn. Now:
- The Studio also hands the message to the running reply (`POST /api/chat/steer`).
- The reply takes it in as a user message before its next model call, and notes "You added" in the reply.
- A `steer` stream event then removes it from the queue, so it isn't sent again.
- A message the reply doesn't reach (it was already writing its final answer), or one with attachments, still goes out afterwards.

Tested on a bench with a multi-round stand-in model: a message sent two seconds into a reply reached the next model call, the reply showed the note, and the conversation stored one question and one reply.

**Launches that small models can make.** A smaller model kept failing to build a workflow launch: nested input paths, plus a scheduler account and QoS that the old refusal described as impossible to infer. `run_workflow` now accepts the workflow, a system name and inputs by plain name, and fills in the rest:
- Inputs are placed where the workflow declares them (`batch_size` → `optimizer.batch_size`) when exactly one declared input has that name.
- An empty scheduler account, QoS or partition is filled from the user's most recent completed run on that system, checking the same workflow's runs first. The result names the run the values came from.
- A dry run says that nothing ran and how to launch it.
- A refusal names the missing field and, if no earlier run had it, says to ask the user.
- The run list's `{runs: [...]}` shape is now read.

Tested against a live platform as a dry run: only the workflow, its saved configuration, the system name, and `{batch_size, max_iterations}` validated. The account, QoS and partition were filled from the user's earlier run, and both inputs were placed under the optimizer group.

Tests: plain-name placement, including an ambiguous name that stays put. The full suite passes.

## v1.128 (2026-10-14)

### Change log through v1.127 (#471)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.127 release.

### Chat: send only the tools a reply needs; read many files in one call (#472)

A user found Studio chat much slower than pw code. With the same model and the same question about documents, the Studio took 40 seconds and pw code 24.

**What the timing showed.** Per-round timing (added here to the `reply timing` log) found the cause. Every round carried all 50 tool definitions (about 45,000 characters), plus the system prompt's workflow catalog and HPC guidance (about 8,000). Each file read was also its own tool call.

**Tool groups.**
- The knowledge-base tools, skills, and custom and remote tools always go.
- The workflow and HPC tools, the agent tools, and the file-writing and labeling tools are sent when any of these holds: the last two user messages name their subject, the conversation used them before, or the model asks with the new `more_tools` tool.
- A tool from a group that wasn't sent still runs, and its group is added.
- The prompt's platform section goes with the workflow and agent groups. Otherwise a one-line pointer to `more_tools` takes its place.

**Batch reads.** `read_kb_file` takes up to 25 `paths` in one call and splits a 30,000-character budget between them.

**Measured.** The question "list every in-process proposal with a line from its intake", against a copy of the same index with the same model, two runs each:

| | Run 1 | Run 2 | Proposals named |
|---|---|---|---|
| Before | 43.7 s | 39.9 s | 34 of 34 |
| After | 34.6 s | 25.2 s | 34 of 34 |

Without a cue word, "Is anything I started yesterday still going?" called `more_tools` and then the run and session tools. An account question now matches its cue words directly.

Tests: group selection for document and platform questions, groups carried over from earlier tool use, and a batch read with a missing file. The full suite passes.

## v1.127 (2026-10-14)

### Change log through v1.126 (#469)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.126 release.

### Chat: tool rounds per reply is a setting (default 50); replies log their timing (#470)

A user found that long chat tasks stopped after 24 tool rounds, a constant in `config.ts` with no setting.

**Tool rounds setting.** **Settings > Assistant tools > Most tool rounds per reply** now controls it, from 5 to 200 with a default of 50; `STUDIO_MAX_TOOL_ROUNDS` sets the deployment default. At the limit the assistant still answers from what it gathered, as before, and now ends with a note that it stopped at the limit and that asking it to continue picks up from there.

**Reply timing.** The same user found the Studio slow next to coding agents. Each reply now logs `reply timing`: rounds, tool calls, model time, tool time, pre-search time, time to the first model answer, and total. That shows where a deployment's time goes before anything is changed.

Tests: the default and the environment override. The full suite passes.

## v1.126 (2026-10-14)

### Change log through v1.125 (#467)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.125 release.

### Sessions full screen in the Studio; the relay streams what it passes (#468)

**Full screen.** Session cards in chat and the side view (chat and Workflows page) get **Full screen**. It shows the session through the relay over everything below the classification banner, so the marking stays visible, with **Close** (or Escape), **Pop out** and **New tab**. A card is a frame inside the chat, so it posts `ade-full-view` to the app around it; `FullViewHost` sits in `.app-body`. `openFullView` lives in `sideBySide.ts` so the card and the side view share it without an import cycle.

**Streaming relay.** The session relay now streams every response other than a page (`Readable.fromWeb`) as it arrives. HTML is still buffered so the scroll guard can be injected. A study viewer's gallery requests dozens of full-size images through the session tunnel, and buffering each one until its last byte made the gallery slow to fill.

Checked in headless Chrome: Full screen from a chat card covers the shell below the banner and shows the relayed viewer. Close and Escape both return, and Full screen from the side view works the same. The full suite passes.

## v1.125 (2026-10-14)

### Change log through v1.124 (#465)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.124 release.

### Platform refusals reach the model; session match takes several words (#466)

When the platform refuses a request, the pw CLI exits non-zero with the platform's problem document on stdout and nothing on stderr. The chat tools passed stderr (empty, or the CLI's upgrade notice) or Node's "Command failed" to the model. On a live deployment a model then made 13 launch attempts guessing at input syntax and credentials, when the platform had said `Missing required fields: Quality of Service [QoS]`.

The problem document's `detail` and each `errors[].pointer` and `detail` now form the error, and the CLI's upgrade notice is dropped from stderr. Since the message names a missing required field, `run_workflow`'s existing remedy now applies: take the account and QoS from a saved configuration for that system, or ask the user.

`list_sessions` `match` now takes several words, any of which may match. A model passed `"MODO|dettrain|00006"` and matched nothing.

Tests: `problemMessage` on the 400 document the platform returned, on a JSON listing, and on plain text.

## v1.124 (2026-10-14)

### Change log through v1.123 (#463)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.123 release.

### Keep the Studio from sliding out of view; find sessions by word or run (#464)

On a live deployment the Studio kept sliding up and sideways out of view, leaving only the composer at the top of a blank page. There were two triggers.

**A study viewer in a session frame.** The design explorer calls `scrollIntoView({ block: 'nearest' })` to keep a row in view, and calls `focus()`. In a same-origin frame (the session relay), both also scroll every container around the frame. That includes boxes with `overflow: hidden`, which the user can't scroll back. The relay now injects a small script at the top of every HTML page it serves. Inside a frame, the script reimplements `scrollIntoView`, and `scrollIntoViewIfNeeded` where it exists, so they scroll only the page's own containers, and it gives `focus()` `preventScroll`. Outside a frame it does nothing. A probe two frames deep confirmed the effect: on v1.123 an element scrolled into view inside the explorer moved the chat's containers; with the guard, nothing outside the explorer moved.

**The conversation scrubber.** Its jump used `scrollIntoView({ block: 'start' })`. For a message near the end, that asks for more scroll than the thread has, and the browser takes the rest from the surrounding panes. The jump now scrolls the thread only.

**Backstop.** The chat canvas and the shell under the banner use `overflow: clip`. On the root element `clip` still counts as `hidden`, so the window itself can still be scrolled by script. A capturing scroll listener therefore resets the window, the shell, the chat canvas and the sidebar's horizontal scroll whenever a script moves them. None of those is meant to scroll; each view scrolls inside its own container.

**Sessions in chat.**
- `list_sessions` with `run` now matches a run's session by name (`<prefix>-<run id>`), the way the session cards and the Workflows page already do. A viewer a job registered for itself has no run recorded, so the old filter never found it.
- A new `match` parameter keeps sessions whose name, run or workflow contains a word, and the tool description points requests like "show me the modo sessions" at it.

Tests: the relay output starts with the guard and then the page, and the guard lands ahead of the page's own scripts (with or without a doctype, or no `<head>`). The full suite passes.

## v1.123 (2026-10-14)

### Change log through v1.122 (#460)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.122 release.

### Fix follow-up messages after tool use, the stacked side view, and the tree header (#462)

Three fixes found while using v1.122 on a live deployment.

**Follow-ups after tool use failed.** Starting with the 0.45 chat client, an assistant message's earlier tool calls go into the request history as an assistant message carrying `tool_calls`, followed by tool messages carrying `tool_call_id`. The server rebuilt the history as role and text only, which stripped those IDs, and the gateway refused the request: `Tool message missing tool_call_id`. So every follow-up to a reply that had used tools failed. Earlier turns now contribute only their text, as they did before 0.45. The current turn's tool calls are sent by the turn loop with their IDs, unchanged. The stand-in gateway used for testing now rejects tool messages without an ID, as real gateways do. With it, v1.122 reproduces the failure and this branch answers the follow-up.

**Stacked side view.** Below 1000 px wide, the side view stacks under the chat. As a flex item with `min-height: auto` it grew to its content's full height, because the graph and the session frame each ask for 420 to 480 px. In a 700 px window it reached 1,323 px, squeezed the chat out of view, and pushed the shell past the window. Since v1.122 the shell is `overflow: hidden`, which still lets `scrollIntoView` scroll it, and the user then cannot scroll back. Now the stacked side view and the chat share the height, each scrolling inside its share. The shell is `overflow: clip`, which nothing can scroll.

**Tree header.** The library picker had `max-width: 60%` and couldn't shrink, so a long library name pushed **+ Add** and the collapse button past the pane's edge. It now takes the width the buttons leave and truncates its text.

Checked in headless Chrome:
- A two-message conversation against the strict stand-in gateway gets its follow-up answered.
- At 1000 px the side view takes the bottom 55%, with no overflow and no shift after `scrollIntoView`; at 1400 px the side-by-side layout is unchanged.
- With a long library name, every tree header button stays inside a 240 px or 300 px pane.

## v1.122 (2026-10-14)

### Change log through v1.121 (#459)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.121 release.

### Workflow forms: one box per field, one rule per heading, no page scroll; show the source (#458)

Follow-ups to the 0.45 form upgrade, from using the workflow form on a live deployment.

**One box per field.** 0.45 draws every field in its own box (`fieldBox`). The Studio still had a rule from the 0.34 era, when the library's inputs had no border, that gave every input in `.wf-form` a border. On a number field or a dropdown the visible box is a wrapper, so that rule drew a second, smaller box inside it. The rule is removed. The Studio's own pickers (cluster, partition, bucket, plain text) now use `fieldBox` too, so all fields share the same box and height.

**One rule per group heading.** The old override that restyled section headings added a bottom border to the heading's button, under the rule the library already draws, which made a double line. The other 0.34 overrides (label column, multi-select trigger, empty-table panel, narrow-screen label stacking) no longer match anything in 0.45's markup. All of them are removed.

**No page-level scroll.** `html`, `body` and `#root` now refuse a page scroll at every width, as the phone layout already did, and the sidebar scrolls inside itself. Before, in a short window, the sidebar footer or a long workflow form could scroll the whole shell up. That left a band of background below it and moved the form's sticky Run bar off the bottom edge. In a 640 px window every view had overflowed the page by 9 px, all of it the sidebar footer; now none do, and each view scrolls in its own container as before.

**Where the workflow comes from.** The form route returns `source`, shown under the title:
- local, when the workflow is stored in the account;
- remote, with the record's `remote` repo, branch and workflow file, or a GitHub entry's repository, ref and path;
- marketplace, with the item's slug.

Tests: `recordSource` for local, remote and missing records. Checked in headless Chrome on a laid-out form with number fields, a dropdown, text fields and the Studio's pickers:
- one box per field and one rule per heading;
- no page overflow in any view at 640 px;
- the sidebar scrolls;
- the source line shows for a remote record.

## v1.121 (2026-10-13)

### Change log through v1.120 (#456)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.120 release.

### Chat: retry cut-off replies, library in Scope, steady tool rows; Workflows side view (#457)

Five chat and Workflows changes, all from using the Studio on a live deployment.

**Retry a reply that was cut off.** A turn interrupted by a restart, or one that failed or was stopped before any text, left the conversation ending on the user's message with nothing to click. Such a message now comes back from `GET /api/chat/conversations/:id` with an `error`. The chat package already renders that as a notice with **Retry**, which resends the message. The error is computed on read, only for conversations with no reply streaming (`beginAnswer` marks one while it streams), and is never stored. In the browser, a stream that closes with neither `done` nor `error` and no text throws, so the reply shows the error at once. A stopped or failed turn now keeps its tool calls along with its text, so its errors can still be read.

**Library inside Scope.** The chat's library picker is now part of the Scope menu, which has a Library section (each library, or all of them) and a Labels section. The button reads "Scope" followed by the library's name.

**Steady streaming.** While a reply streams, the package showed the last lines of the model's thinking and dropped them when a tool or the answer started. With a model that reasons between tools, that opened and closed on every round. It also drew each tool call as two lines that then folded into a one-line "Ran N tools" row. The thinking now stays one line until clicked. A tool call's result summary sits on the same row as its name, so a call is the height of the row it folds into. Measured on a four-round stand-in model, the thinking held at 36 px and each tool row at 36 px through the whole turn; before, they moved between 36, 57, 65 and 88 px. These are CSS rules on the package's markup.

**Side view on the Workflows page.** The chat's side view is split into a shared panel, the chat's host (which follows runs) and a page host.
- On the Workflows page, a session name opens the session in the side view, with **Window** and **New tab** beside it.
- A run id opens the run's Form, Graph and Session tabs, and a run launched from a tile's form opens there.
- A session the Studio cannot relay still opens in a window.
- Side view messages now carry a target, because the chat stays mounted behind other pages and would otherwise pick them up too.

**Credential failures named.** When a platform tool's `pw` command is refused for its credential (a 401 problem document, `no context configured`, or `could not extract platform host from credential`), the tool now tells the model whose credential failed and that the fix is a current key. Previously the model got the raw output and treated it as an input or syntax error. The check matches only the problem-document shape, so a listing that contains the word "Unauthorized" is left alone.

Tests: no-reply flagging, including while a reply streams and that the flag is never stored, and the credential-failure messages.

Checked in headless Chrome:
- The Scope menu with two libraries.
- A reply cut off by killing the server mid-stream, then restarting it: the message showed the notice and Retry, and Retry produced the reply.
- Block heights traced while a reply streamed.
- The Workflows page opening a session and a run in the side view, with the chat's side view left empty.

## v1.120 (2026-10-13)

### Change log through v1.119 (#454)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.119 release.

### Workflow forms: render the layout schema (#455)

Workflow forms that define `$meta.layout` now render with it in the Workflows tab and in the side view's Form tab. The layout can define sections, stacks and responsive grids, at the top of the inputs or inside a group. The layout's scoped `css` is enabled; the renderer accepts only layout and appearance properties.

The layout renderer ships in `@parallelworks/ui` 0.45, so this PR moves from 0.34 to 0.45. In 0.45 the form engine is react-hook-form instead of Formik, so the Studio code changed to match:

- The launch form creates its own `useForm()`, hands it to `DynamicForm`, and submits `getValues()`. Formik is removed.
- The Studio's own fields (cluster, partition, bucket, text) bind with `useFieldValue` and are framed with `Field`.
- The partition field now uses the cluster value the form has already resolved from `resource: ${{ inputs.resource }}`. It watches the input only when the expression arrives unresolved.
- Settings uses `Switch`, with readable labels. The tooltip provider and the chat notices now come from the UI provider.
- The theme fallbacks in `styles.css` are regenerated from 0.45's `deriveTheme`, which now derives 55 tokens. One visible effect: body text is #0a101b on light and #dee9f4 on dark, where it used to be pure black and white.

On the server, the chat tools and the workflow composer skip `$meta` when they list a workflow's inputs. The composer also leaves the source workflows' layouts out of a composed form, because each layout names its own workflow's fields.

The schema test fixture is refreshed from the published `workflow.schema.json`, which now defines `formLayout` and the per-input `width`.

Tests added:
- The form route keeps `$meta.layout`, at the top level and inside a group.
- A composed workflow carries no `$meta`.
- A laid-out workflow passes the platform schema, and an undefined layout node fails it.

Checked in headless Chrome against a stand-in CLI with a laid-out study form:
- Sections, the two-column cluster and partition row, the group's grid and a CSS-bordered grid render in both themes and collapse at phone width.
- The partition list fills from the chosen cluster.
- A saved configuration loads into the form.
- Validate submits the cluster, partition and nested group values.
- Chat, the Settings switches and the run pane's Form tab work on 0.45, with no console errors.

## v1.119 (2026-10-13)

### Change log through v1.118 (#452)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.118 release.

### Session embeds by run: show the run's session card instead of a note (#453)

A model embedded the session a run opened as `![…](/?embed=session&run=<run id>)`. That form has no session name, so since #445 the reply showed "This reply asked for a \"session\" view, which the Studio does not have." Before #445, the same form loaded the whole Studio inside the reply.

`/api/sessions/item` now takes `run=` and returns that run's session, preferring a running one; it matches with `sessionOfRun`, the same rule the side view and the run watcher use. The session card passes `run=` through, titles itself "Session of <run>" until the session loads, and keeps asking while the run's session is not up yet.

Tests: `server/test/sessions.test.mjs` covers the run lookup and the not-yet case. On the local stand-in platform, a stored reply with a `run=` embed rendered the session card with the study viewer inline at full height.

## v1.118 (2026-10-13)

### Change log through v1.117 (#450)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.117 release.

### Follow runs whatever their id, link sessions by name on the Workflows tab, wrap session cards (#451)

Found by an end-to-end test on a live platform. A chat model launched the design explorer workflow from its GitHub file on a test cluster over a synthetic 16-case study whose cases finished one at a time. The test then checked the side view, the session in chat, Beside chat, Pop out, and the Workflows tab in headless Chrome.

- **Run ids:** a run of a workflow file from GitHub gets an id with no number on the end (`gh-<owner>-<repo>`, then `-2` for the next), so the side view, which looked for ids ending in five digits, never opened for it. It now takes the id from the run tools' summaries as written: `run submitted: <id>` from `run_workflow`, and `<id>: <status>` from the watchers.
- **Session column:** the Workflows tab's runs table now matches a session to its run by name when the platform records no run (`sessionOfRun`, the same rule the server applies since #444), so the study viewer appears beside its run.
- **Card buttons:** a session card's bar wraps in a narrow chat column. With the side view open, Hide, Beside chat, and Open in new tab were cut off.

After the fixes, the same test showed:
- the side view opening on the run within seconds of the launch;
- the Session tab showing the study viewer through the relay, which counted cases as they solved (4, then 6, then 14 of 16);
- the session card inline at full height with all four buttons visible;
- Beside chat adding the session as a second tab;
- Pop out opening a window at the session's own address;
- the runs table naming the session.

Tests: `web/test/runPane.test.ts` covers ids with and without a number and the client's session match.

## v1.117 (2026-10-13)

### Change log through v1.116 (#448)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.116 release.

### Ask for a configuration or a system before launching a workflow that has them (#449)

A launch from chat named neither a saved configuration (`config`) nor a system (`resource`), so the workflow's compute-clusters input was left to its default. The run went out with an empty resource field, and every case failed minutes later when a step built its scheduler script from that field's details.

For a workflow with saved configurations, `run_workflow` now submits nothing until the launch names a configuration, names a system, or fills the compute-clusters input. Otherwise it returns the configurations to choose from, and the model launches again with one. Workflows without saved configurations launch as before.

Tests: `server/test/runWorkflowResource.test.mjs` covers the refusal and the three ways through. The workspace-retry test now ignores the workflow read the check adds to its CLI call sequence.

## v1.116 (2026-10-13)

### Side view with tabs and pop out, sessions shown in chat, no app inside an unknown embed (#445)

The run pane from #442 becomes a side view with tabs, and sessions can now be shown in chat itself.

- **Side view tabs:** the run the conversation launched, and any session sent there with **Beside chat** on a session card in a reply. The card is a same-origin frame of the chat, so it posts the session to the page that holds the side view. Tabs close individually, and **Hide** folds the side view to a button.
- **Pop out:** opens the current tab in a window on the right half of the screen (`sideBySide.ts`). Where the Studio is an installed app (standalone or window-controls-overlay display mode), its own window moves to the left half so the two sit side by side. A browser tab cannot be moved by its page, so there only the new window is placed. A popped-out session opens at its own address, where it signs in the way the platform's pages do, so websocket sessions work there. A popped-out run opens `/?embed=run&slug=`, the same three tabs in a window. The session card's **Open** uses the same placement.
- **Sessions in chat:** a session card in a reply shows the session under its bar, through the relay, with **Hide** to fold it.
- **Session embeds by address:** a session embed resolves by `host=` or `url=` as well as `name=`, and `/api/sessions/item` accepts both.
- **Unknown embeds:** an embed address the Studio has no view for now shows a one-line note. It used to render the entire Studio inside the reply. A model that asked for an embed of a session by its address produced exactly that.

Tests:
- `web/test/sideBySide.test.ts` covers the window placement.
- `server/test/sessions.test.mjs` covers finding a session by address. It also adds route tests for the relay from #442: the key is sent; the host's framing header and cookies are dropped; the platform's own host, an unknown host, and a session served under the platform address are refused; and the folder address redirects to its trailing slash.

Checked in headless Chrome against a stand-in platform serving the study viewer:
- the unknown embed showed its note and no app;
- the session card showed the viewer inline at full height (it first sized itself to its frame, a loop fixed by keeping the inner frame from shrinking);
- Beside chat added a session tab next to the run tab;
- Pop out, with a user gesture, opened the run in a window of its own.


A second commit adds a guard: any Studio page framed by the Studio itself shows a one-line note, whatever its address. Without it, the unknown-embed path above recursed. The framed copy opened the same conversation, which framed itself again, with every copy polling the server, until the deployment stopped answering. The platform's page framing the Studio is another site and is not affected.

Rebased onto #444. Both added tests at the end of `server/test/sessions.test.mjs`, and both are kept.

## v1.115 (2026-10-13)

### Change log through v1.114 (#446)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.114 release.

## v1.114 (2026-10-13)

### Change log through v1.113 (#443)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.113 release.

### Match a session to its run by name when the platform records none (#444)

A job that registers its own endpoint, such as a study viewer or a model server, comes back from the platform's session list (`GET /api/sessions`) with no `workflowRun` recorded. On the customer platform this held for every endpoint session. As a result, the run pane's Session tab (#442), the chat's "session is up" note from the run watcher, and `/api/sessions?run=` never found the session a run opened.

`sessionOfRun` now decides membership in all three places. A recorded run decides when there is one. Without one, a session belongs to a run whose id is its name or the end of its name after a dash, which is how those workflows name their endpoints (`<prefix>-<run id>`).

Tests: `server/test/sessions.test.mjs` covers the name match, a near-miss number, a recorded run overriding the name, and an empty id.

## v1.113 (2026-10-13)

### Change log through v1.112 (#441)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.112 release.

### Show a launched run beside the chat: its form, live graph, and session (#442)

When a reply launches a workflow, a pane opens beside the chat and follows the run:

- **Form:** the workflow's form, filled with the inputs the run was given and marked as executed from the conversation.
- **Graph:** the platform's own graph of the run (`DependencyGraph` from the UI package), with jobs colored by state and refreshed every four seconds until the run ends. The data comes from `GET /api/runs/item?slug=`, which reads `/api/workflow-runs/<slug>` with the viewer's key.
- **Session:** the web session the run opened, such as a study viewer, inside the pane.

The pane moves from the form to the graph to the session as each becomes worth watching, unless the viewer picks a tab. A conversation opened later starts with the pane closed and a **Run** button to bring it back.

A session's host signs browsers in through the platform's login page, which refuses frames, but it accepts the platform key as a bearer token from a server. The Studio therefore fetches the session's pages with the viewer's key and serves them at `/api/session-proxy/<host>/` on its own origin:
- only hosts of sessions the viewer's key can see, and that are served on their own host, are relayed;
- the address fetched comes from the platform's session record, never from the request;
- GET and HEAD only, with no cookies passed, and a redirect from the host (its sign-in) is reported instead of followed.

A page that loads its data by relative address works unchanged, which covers the study viewers. Sessions that need websockets open in a new tab from the pane's footer.

`run_workflow` now includes the run's id in its summary (`run submitted: <slug>`), which is what the pane follows.

Each model turn's text now opens a new paragraph (`turnSeparator`). A model that narrates before its tool calls produced one reply with sentences run together across turns, and a heading or table that opened a turn landed mid-line, where markdown cannot render it.

Tests:
- `web/test/runPane.test.ts` covers which run the pane follows.
- `server/test/emptyReply.test.mjs` covers the turn separator.
- Two run-tool tests now expect the summary with the run's id.

Checked end to end in headless Chrome against a stand-in platform serving the study viewer on a synthetic study. The form showed the submitted values, the graph tracked workers finishing, and the session tab showed the viewer updating as cases solved. A scripted tool-calling model's narration and following heading came out as separate paragraphs.

## v1.112 (2026-10-13)

### Change log through v1.111 (#439)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.111 release.

### Offer other models when a reply cannot be given, and wait out rate limits (#440)

When a reply cannot be given, the chat now offers the user's other models instead of ending at an explanation, and a provider that is rate limiting gets a short wait first.

A rate-limited call (`rate_limit_exceeded`, 429, "too many requests") is retried twice, after 3 and 8 seconds, with a line in the thinking area while it waits. If the limit outlasts the waits, the reply says so in place of an error.

A reply that ends empty (#438) or rate limited closes with `Retry with:` links. They list the user's other models from their cached listing, leaving out any whose last call failed in the last ten minutes. A rate-limited reply, or one where the model reasoned without answering, also links a retry of the same model; an empty completion does not, since that model has declined. Nothing is sent to another model until a link is clicked. The chat view (`RetryWithModel`) then selects that model and, once the selection has taken, asks the same question again the way the thread's own retry does: as a branch from its parent, or as the next message when the question started the conversation.

Tests: `server/test/emptyReply.test.mjs` covers the links and rate-limit detection, and `web/test/retryWithModel.test.tsx` covers selecting before sending and the fallback to the last question. End to end, against a stand-in gateway with one model that returns an empty completion, one that answers, and one that returns 429:
- the empty reply listed the other two models, and clicking one switched the selection and returned that model's answer;
- the rate-limited model was called three times over 11 seconds and its reply ended with the three retry links.

## v1.111 (2026-10-13)

### Change log through v1.110 (#437)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.110 release.

### Say why a chat reply is empty, and keep the evidence (#438)

A chat turn whose model returned an empty completion (HTTP 200, finish reason `stop`, zero completion tokens) was reported as "The model spent this turn reasoning without producing an answer." The model had not reasoned: the server's own pre-search line ("pre-searched the knowledge base") is appended to the turn's reasoning text, and the note treated any reasoning text as the model's. A provider can return such an empty completion when it declines a request, so the wording sent people looking at reasoning settings when the model had declined.

`emptyReplyNote` now distinguishes four cases: the model reasoned without answering; it ran tools and returned no answer; a content filter blocked the reply; or the model returned an empty reply. The last three include the finish reason and say when no tokens were generated. Only reasoning that arrives from the model counts as reasoning.

So the next empty reply can be diagnosed after the fact:
- non-streamed turns keep the `usage` the gateway returns (it was dropped, so those turns stored no token counts);
- the forced final-answer turns add their usage to the turn's total;
- the stored message records its finish reason;
- an empty reply is logged with the model, finish reason, usage, and tool count;
- a workflow launch moves the previous server log to `app.log.prev` instead of truncating it.

Tests: `server/test/emptyReply.test.mjs` covers the four notes.

## v1.110 (2026-10-13)

### Change log through v1.109 (#434)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.109 release.

### Say what a single-user Studio exposes on its port (#433)

The single-user posture has no identity layer, and the server listens on all interfaces by default. CUSTOMIZATION.md now says what that exposes (chat and platform tools on the operator's credential, queries on the operator's cluster libraries, agents on the host) and how to limit it: the platform's session proxy, a host firewall, or `HOST=127.0.0.1`. Documentation only; the default is unchanged.

### Default to All libraries per tab, keep files writable there, and show Stats loading (#436)

All libraries is now the default where there is more than one library. Each browser tab keeps its own choice in session storage, so a reload keeps it and a new tab starts from the default. The library count is cached from the last configuration so the first requests already use the right default.

With All selected, a request on a path goes to that path's library: `withLibrary` and `withLib` in `web/src/api.ts` take the library's folder off the path, address that library, and the write helpers put the folder back on the paths they return. Routes that already read All (tree, file, download, search, stats) are unchanged. Before this change, raw, HTML, and PDF views, "sync now", and every write refused `library=all`. Now files of every type open from the All tree, and the writable library stays writable inside its folder: uploads, new folders, rename, move, delete, and labels. A library's own folder takes uploads and new folders and is not renamed or deleted, and a request whose paths span two libraries stays addressed to All, which refuses it.

Stats clears its figures and shows a loader while a library loads, and drops answers for a library no longer selected. Previously a cluster library took seconds to count while the last library's numbers stayed on screen. The server keeps a cluster library's counts until its next build (keyed on `builtAt`), and still checks the owner's key on every call.

The library picker on Search and Query moves inside the padded controls, aligned with the search box and the query modes.

Tests: `web/test/libraries.test.tsx` covers the default and the per-tab choice, All-mode addressing for reads and uploads, and writes and their returned paths. `server/test/gufiVt.test.mjs` checks that a cluster library is counted once per build and again after a rebuild. A local run on synthetic data in headless Chrome confirmed the default, the alignment, the loader on switching, and the choice surviving a reload.

## v1.109 (2026-10-11)

### Change log through v1.108 (#430)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.108 release.

### Query across libraries as one gufi_vt statement, and show the GUFI commands (#431)

A query over all libraries now runs as one gufi_vt statement where the Studio host's GUFI has gufi_vt: a virtual table per library, unioned, with the overall order and limit in the outer SELECT. A library on a cluster is a remote location whose command is the Studio's wrapper (`gufiVtRemote.js`). gufi_vt hands it the gufi_query command line, the wrapper splits it without a shell and passes it back to the server over loopback, and the server runs gufi_query on the cluster through the user's session. The server accepts only gufi_query with gufi_vt's stages on that location's own index, under a per-process secret and a per-query token.

Each location is checked first (the index exists, and on a cluster GUFI is installed), and one that cannot answer is left out and named beside the rows, since gufi_vt fails the whole statement when a location exits nonzero with no rows. Without gufi_vt on the host, for SQL containing a double quote, or when the statement fails, the per-library merge answers as before. The response's `engine` says which one answered.

Search, the Query page, and the chat's `search_kb` and `query_corpus` tool blocks now show the GUFI commands behind their results, as a shell takes them. Commands on the Studio host are shown as run. A cluster's command is shown under its name, to run there after `pw ssh <cluster>`. A gufi_vt statement is shown as a here-document in gufi_vt's usual remote form (`remote_cmd='pw', remote_arg='ssh', remote_arg='<cluster>'`, with the cluster's GUFI added to the PATH there), so it runs from a terminal with GUFI and the pw CLI. The chat model does not receive the commands.

Tests: `server/test/gufiVt.test.mjs` covers the wrapper's splitting, the endpoint's checks, the generated script, and the command formatting. With GUFI installed, it indexes two synthetic trees, one local and one behind a fake remote session, and checks that gufi_vt returns the same rows as the merge for a ranked query, grouped totals, a builder filter, and raw SQL, with a missing library left out and named.

### Draw the classification banner on the loading screen (#432)

In an installed app with the window controls overlay, the classification banner is the title bar. The loading screen drew before the app had its configuration, so the strip between the window controls showed the page's ground until the app mounted.

The app now caches the banner's text and colors beside the theme color, and the loading screen draws them from that cache: in the title bar area in that display mode, across the top otherwise, and not inside the platform frame unless the deployment shows the banner there. The first launch after this change still shows the gap, since the cache is written once the app has loaded.

## v1.108 (2026-10-11)

### Change log through v1.107 (#428)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.107 release.

### One library or all: search, queries, statistics, the tree, and the chat (#429)

Search, the Query page, statistics, the Library tree, and the assistant's tools can now cover one library or all of them, whether read and write or read only, on this host or on a user's cluster. Until now the Query page and the chat used only the knowledge base, and nothing covered every library.

**The Library switcher** gains **All libraries**. The tree, Search, Query, Stats, and the index health popup follow it, and Search, Query, and Stats now show the switcher at the top, so the scope is visible where it applies.

**What `library=all` does:**
- **The tree** has one top-level folder per library, named by its id, with that library's own tree below. A path in it is `<library id>/<path>`. Files open and download from their library, and a cluster library's file comes from the cluster.
- **Search** runs in each library what that library supports: full text and semantic search where its index has them, and names always. It blends the hits and tags each with its library. A library that fails adds nothing, and the search goes on. Labels belong to the knowledge base, so a label filter keeps knowledge-base hits.
- **The Query page** has each library answer on its own, and the rows gain a leading library column.
  - For canned queries and the builder, whose sort order the Studio knows, rows are re-ranked into the overall top rows. Grouped totals, such as per extension, are added up.
  - Raw SQL rows are listed library by library.
  - A library that cannot answer is named beside the rows.
  - The Query page now works on any single library too, including a cluster library, with gufi_query run there.
- **Statistics** show a total and a line per library.

The All entry is read only.

**The chat** has a Library control beside Persona, which sets the library the assistant searches and queries in that conversation. The web client now sends that choice, which the server already accepted. The assistant's tools take a `library` argument: `search_kb` and `query_corpus` accept `all`, `read_kb_file` and `list_kb_dir` take one library's id, and a new `list_libraries` tool says which ids exist. A cluster library's files are read and listed on the cluster.

**Fixes in the same area:**
- **Stale statistics:** the Stats page and the footer loaded statistics once, so deleting a directory or pressing **sync now** left them stale until a full reload. Both now reload when the index changes and when the library changes, and deletes in the Library announce the change.
- **Cluster file dates:** a file opened from a cluster library showed 1970-01-01, because the preview did not read the file's size and modified time. It now reads them on the cluster in the same call.
- **Cluster downloads:** a cluster library's file now downloads through the session, up to 50 MB.
- **Library paths:** Your libraries rows show the index and source paths on the cluster, as the host libraries do.

Tests cover scope resolution, re-ranking and summed totals across two cluster libraries, a library that cannot answer, a single library on its own, tagged search hits, and the combined tree. The clusters are faked, through the remote session and a fake gufi_query.

A query across libraries as one gufi_vt statement is next. The Studio's own merge above stays as the fallback and as the check on its answers.

## v1.107 (2026-10-11)

### Change log through v1.106 (#426)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.106 release.

### Your libraries: directories on your clusters, indexed and searched there (#427)

Users can add a directory on one of their clusters as their own library. The Studio indexes it there with the index workflow (#409), then browses and searches it on the cluster, as that user.

**Adding.** Settings, Libraries, "Your libraries" takes four inputs:
- a cluster from the user's active clusters;
- a directory, `$WORKDIR` by default;
- a label;
- optionally, a Slurm partition and account.

The Studio registers the workflow in the user's account the first time, named after its content hash so a changed workflow registers afresh. It runs the workflow with the user's own key, the one their chat uses. It follows the run while Settings is open, and when the run completes it reads the index summary from the run log, resolves the index and GUFI paths on the cluster, and marks the library ready.

**Using.** A ready library joins that user's Library switcher, labeled with its cluster, and no one else's. It is read only. The tree lists each folder from the index on the cluster, name search runs there, and text files open as a preview read from the cluster. **Refresh** reruns the workflow in update mode. **Remove** drops the library from the Studio and leaves the index on the cluster.

**Remote commands.** One long-lived `pw ssh` session per user and cluster carries the commands (`server/src/remoteExec.ts`). Each request is the command's argv, every argument base64-encoded, and the cluster decodes and runs it without a shell, so SQL and paths never meet shell quoting. On a customer cluster the first request took about 5 seconds for the ssh setup and later ones about 0.3 seconds.

**Wiring.** A user library's index root is an opaque reference (`remote:<owner>:<id>`), and `gufi.ts` sends any query on one to the cluster, so search and statistics work unchanged. The bundle and the container image now carry `deploy/gufi-index.yaml` for the Studio to register.

**Live test on a customer cluster.** The test used a synthetic directory of 6 folders and 6 files, and was removed afterwards. Adding it took 73 seconds to reach ready. The root listing then took 343 ms, a subfolder 308 ms, a name search 309 ms, and a text preview worked. The first try showed that this GUFI build names its depth flags `--min-level` and `--max-level`, and the tree uses those.

**Not yet.** Scheduled refresh, which needs the user's key when it falls due. Full-text and semantic search, which need enrichment. Searching several libraries at once through gufi_vt.

Tests cover the summary parsing, the workflow registration and its run inputs, a finished run making the library ready, its place in the library list, the tree listing, a name search through the remote path, and path checks. The fakes are pw, the remote session, and gufi_query.

## v1.106 (2026-10-11)

### Change log through v1.105 (#425)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.105 release.

### A workflow that builds or updates a GUFI index on a cluster (#409)

`deploy/gufi-index.yaml` is an ACTIVATE workflow that builds or updates a GUFI index of a directory on a cluster, for remote libraries. It runs as the user who launches it, so the index covers what that user can read. The Studio does not launch it yet. It is written so the Studio can register it under each user's account and run it with that user's platform key, the pattern the agent runner uses.

It has four jobs. `prepare` checks the inputs and writes the index script. `install_gufi` builds GUFI's core tools from source on the login node into `~/.activate-studio/gufi/<commit>`, once per commit, without the AI extensions. `index` hands the script to the platform's `script_submitter` subworkflow, which runs it on the login node or submits it to Slurm or PBS. `record` checks the script's exit status and prints the manifest the script wrote, `.studio-index.json`, between `STUDIO_INDEX_BEGIN` and `STUDIO_INDEX_END` in the run log.

The directory defaults to `$WORKDIR`, resolved on the cluster, or the home directory where `$WORKDIR` is not set. The index defaults to `~/.activate-studio/indexes/<name>-<hash>` and must be outside the directory it indexes. `update` mode rescans the directories changed since the previous run with `gufi_incremental_update`; `build` writes a new index beside the old one and swaps it in. GUFI's incremental update creates a temporary file in the directory that holds the indexed one, so where that directory is not writable, as for a work directory under a site-owned parent, an update rebuilds instead. The GUFI source is its GitHub repository at a pinned commit, or a `pw://` tarball for clusters without GitHub access.

The install step handles three differences found on a test cluster. The site's default modules set `CC` to a vendor compiler, so the build uses the system `gcc`. GUFI's configure needs Python 3.9 or later and the system `python3` was 3.6, so it picks an installed `python3.X` that is new enough. `setfattr` and `getfattr` are required at configure time but used only by GUFI's tests, and were missing, so `/bin/false` stands in for them.

Tested on a Slurm cluster at an HPC center: the install from GitHub (about three minutes) and from a `pw://` tarball; runs on the login node and as Slurm jobs; an update after files were added and removed and a directory created, which matched `find`'s counts; a rebuild over an existing index; and a work directory of 3,510 directories and 21,764 files, indexed in 4 seconds on a compute node into a 180 MB index. The YAML validates against the platform's workflow schema. `docs/LIBRARIES.md` lists the inputs.

## v1.105 (2026-10-11)

### Change log through v1.104 (#424)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.104 release.

### Inside the platform's page, a session opens on its ACTIVATE page (#423)

Sessions did not open when the Studio was itself open inside the platform's page. The Studio opened a scripted window, and a window opened from inside the platform's sandboxed frame inherits that frame's restrictions. When the browser refused the window, the Workflows list did nothing.

- **Inside the platform's page:** **Open** now reads **Open in ACTIVATE** and opens the session's page on the platform (`/sessions/<user>/<name>`, from the session record's `internalHref`) in a new tab, where the platform shows the session embedded. The tab opens through a link, which a sandboxed frame allows.
- **Elsewhere:** the session gets a window of its own as before, and now a new tab when the browser blocks the window, so a click always opens something.

Session records carry the new `platformUrl` field. `docs/WORKFLOWS.md` explains why the platform can embed sessions and the Studio cannot yet: the platform reads its own login on its own page and gets a one-time sign-in pass through `/me/embed`. The browser does not send that login to the route from the Studio's site, and the route takes no API key or CLI token.

## v1.104 (2026-10-11)

### Change log through v1.103 (#421)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.103 release.

### pw code agents on the user's clusters, with goals (#422)

The assistant can run pw code agents on the user's clusters and follow, steer, and bound them with pw code's `/goal`.

**Transport.** A cluster's pw code daemon is reached the way `pw code attach` reaches it. The Studio server runs `pw ssh <cluster> pw code daemon proxy --ensure`, which bridges the ssh session to that machine's daemon socket and starts the daemon if none runs. The Studio then speaks the daemon's HTTP API over the pipe (`server/src/pwcodeBridge.ts`). One keep-alive bridge per user and cluster carries every request. On a customer cluster, the first request took 5.4 seconds for the ssh setup and later ones about 0.24 seconds. The bridge closes after 10 idle minutes. It needs only `pw ssh` and a pw CLI with `pw code daemon proxy` on the cluster, so it works where the platform's internal agent routes are not served. It also reaches the whole daemon API, goals included.

**Credentials.** A user's agents act with the key their chat uses, so each connection is that user's and runs as them on the cluster. The deployment's key stands in only when sign-in is off and the Studio has one user. On a shared Studio, a viewer without their own key gets no remote agent. Over the bridge the Studio has the owner's authority on that daemon, so it sets each session's permission mode: accept-edits by default, or read-only or plan.

**Goals.** `start_agent` creates a session in a workspace and sends the objective. The workspace is a new directory under `~/studio-agents` unless one is named. When a goal is given, `start_agent` sets it through the daemon's `/goal` route, and pw code then checks it after every turn and continues until it is met. pw code puts no limit on a goal, so the Studio writes the bound into the condition, 20 turns by default. `agent_status` reports the goal's state, the last check, waiting approvals, and the end of the transcript.

**Tools.** `agent_sites`, `agent_sessions`, `start_agent`, `agent_status`, `agent_steer`, `agent_goal`, `agent_approve`, and `agent_stop`, with matching routes under `/api/agent-sites` for a page to come. Steering reaches a working turn, or becomes the next message when none is working. Approvals are answered only on the user's decision.

**The local site.** This host's daemon belongs to the account the Studio runs as. It is offered only when sign-in is off, or when `AGENT_LOCAL_SITE=1`.

The daemon client now takes a transport, and its request timeout is a timer, since a pipe has no socket timeout.

**Tests.** A fake daemon speaks HTTP over the bridge's stdio. The tests cover one bridge reused across calls, the site list, a start with a bounded goal, the status summary, steering fallback, goal controls, input checks, and the key rule.

**Live run on a customer cluster.** The bridge, session creation, the objective turn, and the goal check all ran. The check then reported the provider's monthly token limit, and the status showed it.

## v1.103 (2026-10-11)

### Change log through v1.102 (#419)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.102 release.

### Workflow forms keep the declared input order; tests cannot reach the network (#420)

Workflow forms in the Studio listed their inputs in a different order from the platform's form. The Studio read each workflow's definition with `pw workflows get -o json`, and the CLI returns the YAML as a Go map, so every level of inputs comes back sorted by name. On one workflow the top-level groups arrived as cluster, doe, postprocessing, resource, software, study, while the workflow declares resource, cluster, doe, study, postprocessing, software. The platform API (`GET /api/workflows/<name>`) returns the declared order. Compared against the workflow's YAML file, it matched at all 39 levels of nesting.

The Studio now reads workflow records through the API with the viewer's key, and uses the CLI only when the API cannot answer. This covers:
- the workflow form;
- **Add to my workflows**, which writes the copy into the viewer's account. A copy written from the sorted map also showed its form alphabetically on the platform.
- the assistant's workflow tools, and model serving's input listing;
- composed workflows, so a composed form lists each source's inputs in their own order.

The shared reader is `server/src/platformRecords.ts`. The workflow graph view already read the API first.

Server tests now run with `server/test/setup.mjs` loaded first. It makes any `fetch` to a non-loopback host fail, so a test that forgets to stub `fetch` cannot reach a platform with the host's credentials. The full suite passes with it. A new form test serves inputs in declared order from the API and sorted from the CLI, and checks that the form follows the API. It fails without the change.

## v1.102 (2026-10-10)

### Change log through v1.101 (#418)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.101 release.

### Sessions open in a window of their own; Workflows in the sidebar returns to its main page (#417)

Sessions now open in a window of their own instead of a frame inside the Studio. The frame did not hold up on a customer platform: a session the browser had just signed in to still refused its frame. Meanwhile the sign-in window loaded the session immediately, and that window is what users wanted.

- **Opening:** clicking a session under Workflows, Sessions, or in the runs table opens it in a window sized for work. A second click focuses the window the session already has. The window has no `opener`, so the session gets no handle back to the Studio, and the Studio keeps its own reference to reuse it.
- **In chat:** a session embed is a compact card with the session's state, the run that opened it, **Open**, and **Open in new tab**. The card reports its height to the chat, so the reply shows no empty box, and the full-screen button is hidden for embeds that report a height.
- **Removed:** the framed view, the sign-in step, and the browser-side framing rules (`web/src/sessionFrame.ts` and its tests). The assistant's session markdown is the card for every session.

Clicking Workflows in the sidebar now always returns to the Workflows main page, including from an open workflow form.

The user guide, `docs/WORKFLOWS.md`, and the README describe sessions this way.

## v1.101 (2026-10-10)

### Change log through v1.100 (#416)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.100 release.

### Workflow form tooltips show their text (#414)

The help icons on the workflow form showed nothing on hover. The UI package's tooltips only mark their anchors with `data-tooltip-id` and the text. The text is shown by one shared tooltip, `GlobalTooltip`, which the host app renders once at its root, and the Studio never rendered it. It now renders inside `UIProvider` in `web/src/main.tsx`. This also covers the package's other anchored tooltips, such as the full text of a truncated label.

Checked in a browser against a workflow whose inputs carry `tooltip:` text. Before the change, hovering a help icon showed no tooltip element. After it, the input's text appears above the icon.

## v1.100 (2026-10-10)

### Change log through v1.99 (#415)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.99 release.

### A workflow's README beside its form (#413)

The workflow page now shows the workflow's README beside the form, or below it on a narrow window. READMEs usually say how a workflow works and what each input changes. **Hide README** puts it away, and the choice is remembered.

Each kind of entry names where its README lives:
- An account workflow from a repository, including an account copy of a marketplace workflow, names its repository, branch, and README path in its record's `remote` block.
- A marketplace entry names them for each version; the Studio uses the version tagged `latest`.
- A GitHub entry reads the `README.md` in its workflow's directory.

The README is read through the platform's repository route with the viewer's key, which reaches private GitHub repositories and projects on registered GitLab servers. A GitHub README falls back to the raw file host. A workflow defined only on the platform has no README, and the page shows its description as before.

Relative links are rewritten so they work outside the repository. Images go through a new route, `GET /api/workflows/item/asset`, which reads image files from the same repository with the same key and nothing else. Other relative links go to the file's page on the repository's site. Code blocks and code spans are left as written. Only a definite answer is cached, either a README or a workflow with none, so a failed platform read is retried on the next view.

The user guide and `docs/WORKFLOWS.md` describe it. Tests cover the address forms, the README location for each kind of entry, the link rewriting, the routes, and the retry.

## v1.99 (2026-10-10)

### Change log through v1.98 (#411)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.98 release.

### A session asks for its sign-in before it is framed (#412)

Opening a session in the Studio for the first time showed a refused frame. The browser had not yet signed in to the session's own host, so the frame bounced to the platform's login page, and that page refuses to load inside another page. The session itself allows framing; only the sign-in was missing.

The session view now asks for that sign-in before it shows the frame. **Sign in** opens the session in a small window, and closing the window loads the frame. If the browser blocks the window, opening the session in a new tab does the same: the frame loads when the Studio window has focus again.

The sign-in is a cookie the Studio cannot read, so the Studio records when it last sent the browser through it and asks again after 12 hours. With the platform login still current, a repeat sign-in is a redirect that takes a moment. Under the frame, **Sign in again** covers a sign-in that ended sooner.

The user guide and `docs/WORKFLOWS.md` describe the step, and a web test covers the remembered sign-in.

## v1.98 (2026-10-10)

### Change log through v1.97 (#408)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.97 release.

### Neutral names in comments, tests, examples, and the change log (#410)

This removes the names of specific deployment sites, providers, and internal test systems from the code's comments, test fixtures, examples, the e2e defaults, and the change log. Behavior is unchanged.

- **Comments:** comments about a provider that discards the system role, ignores the tools field, or locks keys on a schedule now describe that behavior without naming the provider or platform.
- **User-facing text:** the locked-key messages say the provider locks keys on a schedule, without naming one.
- **Fixtures and examples:** test fixtures use neutral provider ids, hosts, and user names. Examples and placeholders use `my-cluster`.
- **E2E script:** `server/scripts/e2e-hpc.mjs` no longer has a default resource. It takes the resource from its first argument or `E2E_RESOURCE`.
- **Unchanged defaults:** `CHAT_NO_STREAM`, `CHAT_SYSTEM_IN_USER`, and `STUDIO_PROBE_PROVIDERS` keep their model-matching defaults.

The change log is regenerated from the pull request descriptions, which were edited the same way.

## v1.97 (2026-10-10)

### Change log through v1.96 (#406)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.96 release.

### Workflow sessions open in the Studio and in chat (#407)

Workflows often open a session, such as a design explorer, a notebook, or a remote desktop. The Studio now shows these sessions.

The Workflows tab lists the viewer's running sessions, with the run that opened each, and the recent runs table names each run's session. A session opens inside the Studio when the browser can show it in a frame, and otherwise in its own tab. Every session view has a button that opens it in a new tab.

In chat, a new `list_sessions` tool and the existing `watch_run` give the assistant each session with the markdown that shows it. The markdown is an embed, `![name](/?embed=session&user=<owner>&name=<session>)`, for a session on its own host, and a plain link for a session under the platform's address. When a run launched from chat opens a session, the run watcher adds it to that conversation once it is running, one time only, and records this in the run registry so a restart does not add it again.

Sessions are read from the platform API with the viewer's key, not through the CLI. Older CLI releases drop fields from their JSON output, and the field that ties a session to its run is one the Studio needs.

Framing works only under three conditions:
- The session is served from its own host. A session under the platform's address can't be framed, because the platform's pages refuse it.
- The Studio is open in its own tab, not inside the platform's page.
- The Studio and the session are on the same site.

The reason is the cookie. The platform signs a browser in to each session host with a `SameSite=Lax` cookie, and a frame receives that cookie only under those conditions. A browser that has never opened the session still has to open it once in its own tab, because the first sign-in goes through the platform's login page, which can't load in a frame. The view says so under the frame. The browser decides whether to frame (`web/src/sessionFrame.ts`), since only the browser knows the address the Studio is being viewed at.

A chat embed with no frame tells the chat its height, so the reply shows a short card instead of an empty 480px box.

The user guide, `docs/WORKFLOWS.md`, `docs/ARCHITECTURE.md`, and the README cover the change. Tests cover session normalization, the routes, the embed markdown, the one-time conversation note, and the framing decision. The runs test now stubs `fetch`, so a running run's session lookup never reaches a platform.

## v1.96 (2026-10-10)

### Change log through v1.95 (#403)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.94 and v1.95 releases.

### Agents view names the machine each agent runs on (#404)

The Agents view now names the machine each agent runs on. For a campaign, that is the task's system. For session agents, it is the host the pw code daemon reports. For runners, it is the Studio's own host. When all of a task's agents share a machine, which is the case today, the name appears once beside the task's objective. It moves onto each agent's row only when a task's agents are spread across machines, which becomes possible once session agents run on remote systems. An agent's session line now names that host in place of "this host", and the board's spawn message for a session agent names it too.

The user guide's Agents section and `docs/AGENTS.md` say where the host appears. The session agent and campaign tests check the recorded host.

### Workflow icons from private repositories load with an older pw CLI (#405)

Icons for workflows kept in a private GitLab project still failed to load on deployments whose pw CLI is v7.105. The fix in v1.94 depends on `imageFallbackUrl`, the platform route that reads the thumbnail with the viewer's access. The Studio gets each workflow's details from `pw workflows ls -o json`, and the v7.105 CLI drops that field from its output. That left only the direct GitLab address, which a private project refuses. The v1.94 test passed because the CLI it ran against is a newer canary build that keeps the field.

When the field is missing, the Studio now rebuilds the route from the thumbnail's raw address. That address contains the same repository, ref, and file path the platform builds the route from: `raw.githubusercontent.com/<owner>/<repo>/<ref>/<file>` for GitHub and `<server>/<project>/-/raw/<ref>/<file>` for GitLab. Checked against a live platform listing, the rebuilt route matched the listed one for all 28 workflows that have one, on GitHub and on GitLab. When the CLI does list the route, it is used as before. GitHub thumbnails are still read anonymously first, so a private GitHub repository now also gets the platform route as its fallback.

The icon test now includes a workflow listed the way v7.105 lists it, plus cases for parsing the raw address.

## v1.95 (2026-10-10)

### A compact Library tree: 24px rows in the UI face, rules scoped per tree (#402)

**What was wrong**

The Library tree drew 33px rows in a monospace face, so a large knowledge base showed few entries per screen. That was not its own styling. Three components share the `tree-row` class: the Library explorer, the Agents task tree, and the JSON and YAML viewer. Their rules were global, and the later ones won. The task tree's `6px 8px` padding and the JSON viewer's monospace face and 1.5 line height overrode the explorer's own `3px 8px` rows.

**What changed**
- **Scoped rules:** each tree's rules apply only inside its own container (`.explorer`, `.task-tree`, `.tree-doc`). The task tree and the JSON viewer look as they did.
- **Library rows:** 24px tall, in the UI face (Geist Sans) at 13px, with 4px corners. Folder and file icons use the muted color, and the 14px indent per level stays. In the same pane, the tree shows about 35 entries where it showed 26.
- **Label button:** with a pointer (`@media (hover: hover)`), each row's label button shows when the row is hovered or selected. On touch screens, which have no hover, it stays visible as before.
- **Theme colors:** the selected row uses the text color, and multi-select hover uses the theme's warning tint, in place of the last hard-coded amber.

**How it was verified**
- Screenshots of the tree over a copy of a large knowledge base's folder and file names, before (33px rows) and after (24px rows).
- Server tests (237) and web tests (66) pass.

## v1.94 (2026-10-10)

### Change log through v1.93 (#400)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.93 release.

### Workflow icons from private repositories load through the platform's fallback route (#401)

**What was wrong**

A Workflows tile for a remote workflow showed no icon when its thumbnail sat in a repository the Studio could not read anonymously, such as a private project on a GitLab server. The tile's icon route fetched the thumbnail's raw address directly and without credentials, which returns a sign-in page or fails to connect, for example on a site certificate the host does not trust. Icons stored on the platform, and thumbnails on GitHub, were unaffected.

**What changed**

The platform's workflow listing carries `imageFallbackUrl` for a remote workflow's thumbnail: a platform route (`/api/repositories/thumbnail?...`) that reads the same file with the platform's access. The icon route now tries addresses in order (`iconCandidates`):
- GitHub thumbnails and platform icons go first, as the platform's own web app reads them.
- For any other repository host, the platform's fallback route goes first, then the direct address.

Platform addresses carry the viewer's key, and outside addresses are fetched without it. A response that is not an image, such as a sign-in page, is skipped. Marketplace rows take the fallback the same way.

**How it was verified**
- A new `workflowIcons.test.mjs` covers four cases:
  - a GitLab thumbnail served from the fallback, with the key and without contacting the GitLab host;
  - GitHub read without the key;
  - a platform icon read with the key;
  - a sign-in page refused.

  It also checks the ordering rules.
- Against a live platform, the fallback route returned a deployment's private GitLab thumbnail (600x600 PNG) with the user's credential, where the direct address was unreachable from the Studio's host.
- Server tests (241) and web tests (66) pass.

## v1.93 (2026-10-10)

### Change log through v1.92 (#398)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.92 release.

### Theme colors in place of the dark-mode patches; labels as chips (#399)

After the shared theme (v1.91), 67 `[data-theme='dark']` rules were still patching colors that light-mode rules hard-coded. Some of them had begun to work against the theme: the dark multi-select colors overrode the new warning tints, and the dark shadows were literal blacks. Headings, paths, and labels were also drawn in navy, a color neither the platform nor the other apps use for static text.

**What changed**
- Each light-mode rule that hard-coded a color now uses the theme token that applies in both schemes, and its dark patch is deleted. This removes 51 dark rules and 89 color literals.
- **Headings and titles** use the text color: view titles, chart and list headings, task and fleet headings, the brand name, and empty-state titles. Empty-state titles are now 14px / 500.
- **Paths:** search results, Stats rows, and query cells use the text color and turn the link color on hover. The viewer's path uses the muted color.
- **Tree rows:** folders and files use the text color, and chevrons the muted color.
- **Selections, notices, and banners:**
  - multi-select rows and bars, the index notice, and the approvals box use `--theme-warning-muted`, with warning text mixed toward the text color;
  - the external-endpoint banners use the success and error tints;
  - destructive menu items use `--theme-error`.
- **Search-mode pills:** full text uses the theme's info tint, by meaning uses violet tinted from the theme, and name uses neutral.
- **Labels** become a bordered chip with a 6px dot. An applied label has a success tint and dot, and an inherited one a dashed border and a muted dot. Tree tags are bordered and muted.
- **Bars and shadows:** chart bars use the accent, and menus, panels, and banners use `--theme-shadow-lg`, which the theme sets per scheme.
- **Code and text bodies** use `--theme-muted-panel-bg`.
- **Links** use `--theme-link` site-wide. Conversation rows in the chat rail keep the text color in both schemes.

**Kept on purpose (16 rules):**
- the derived theme defaults;
- the workflow diagram's text;
- the find highlights;
- the 3D canvas gradient;
- code blocks;
- the accent swatches in Settings;
- the tool spec box;
- the chat rail's selected row;
- the package's toggle switch.

**Checked.**
- Screenshots in light and dark of Search with results, Library, Stats, and Chat.
- Server tests (237) and web tests (66) pass.

## v1.92 (2026-10-10)

### Change log through v1.91 (#396)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.91 release.

### Geist type, controls, page frame, and the settings rail, to the shared spec (#397)

The second pass at matching the ACTIVATE platform and the other Parallel Works apps, after the shared theme in v1.91. The values come from a survey of the platform's web app, `@parallelworks/ui`, and the shared theme. Where the Studio's own needs differ, its layouts stay: the phone bottom bar, the Library's tree and viewer, Help's reading width, and the chat.

**Type**
- Geist Sans and Geist Mono, the platform's and the shared chat's typefaces, from `@parallelworks/ui/fonts.css`. This replaces Public Sans and the `@fontsource` packages (`public-sans`, `geist`, `geist-mono`), which are removed.
- `--font-sans` and `--font-mono` carry the families, and the stylesheet uses them throughout.
- Font sizes drop from thirteen values to six: 11, 12, 13, 14, 16 and 20px.

**Controls**
- **Buttons:** 32px with 14px labels at weight 500 and 6px corners. Primary uses the theme accent and its text color. Secondary is bordered on the panel, with a hover fill. A new ghost style and the danger buttons use the theme's error colors. Toolbar buttons in list headings take a compact 28px.
- **Inputs and selects:** 32px, on the theme's input and border colors, with a 3px accent focus ring.
- **Tabs:** 14px, with a 2px bar in the text color under the active tab, as the platform's tabs are.

**Page frame**
- The page header is 48px with a 14px title.
- **Search and Query** lose their gutter and outer cards:
  - their controls sit in a strip under the header;
  - results run edge to edge with 20px row padding and lighter dividers;
  - list headings are slim, sticky rows;
  - hover and multi-select use the theme's hover and warning tints.
- **Agents:** its tabs are a strip under the header, with content at a 20px inset.
- **Stats and Workflows** keep their tiles as cards at a 20px inset.

**Settings rail.** The same groups as the user guide's rail:
- **Studio:** General, Libraries, Workflows.
- **Assistant:** Model access, Assistant tools, Feature previews, Extensions.
- **External access:** on its own.

A group opens when it holds the section being edited, and its header opens or closes it.

**Removed:** the dark-mode patch rules for fields, field focus, tabs, and secondary buttons, which these styles replace.

**Checked.**
- Screenshots in light and dark of Search with results, Query, Stats, Agents, and Settings.
- A settings rail test covers the groups.
- Server tests (237) and web tests (66) pass.

## v1.91 (2026-10-10)

### Change log through v1.90 (#394)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.90 release.

### Colors from the shared Parallel Works theme contract (#395)

The Studio drew its colors from its own `--pw-*` palette, with a dark scheme re-stepped by hand, a partial set of the shared `--theme-*` tokens, and overrides on top of what the shared package derived. Three consequences:
- Its colors only approximated the other Parallel Works apps.
- Dark mode was navy-tinted where the others are neutral.
- Native controls ignored the Studio's dark toggle. Select arrows were drawn black on a dark field, and the shared packages' `dark:` styles never applied, because the Studio set neither the `dark` class nor `color-scheme`.

**Theme** (`accents.ts`)
- The full 45-token contract comes from `@parallelworks/ui/theme`'s `deriveTheme`, the derivation the other apps use. The seed is the page background, and the accent comes from the Accent setting.
- Surface tone is now the background seed:
  - Cool: light `#f3f4f6`, dark `#0d1117`, the platform's own pair.
  - Neutral: `#f4f4f5` / `#111214`.
  - Warm: `#f5f4f1` / `#151312`.
- Nothing derived is overridden afterward. The hand-set link, hover, element, and dark-button values are gone.

**Stylesheet** (`styles.css`)
- Each `--pw-*` name is now an alias of its contract token: text, page ground, panels, borders, muted text, links, inputs, hover, status hues, and the navigation colors. The rest of the stylesheet reads as before.
- The hand-tuned dark token block is replaced by two blocks of derived defaults (navy on cool), used before the script runs. A test keeps them equal to `deriveTheme`'s output.
- Primary buttons, the brand badge, the scope count, and blank workflow icons take the accent with its contract text color. In dark mode that is a light accent with dark text.

**Dark mode for native controls** (`App.tsx`, `index.html`): the `dark` class and `color-scheme` are set alongside `data-theme`, at first paint and on every change. Select arrows, scrollbars, and the code editor now follow the toggle, and the shared packages' dark styles apply.

**Tests**
- `contrast.test.ts` checks eight text pairs at 4.5:1 or better, for every accent (custom included), every surface, and both schemes, on the derived colors. It covers text and muted text on the page ground and panels, a muted label on hover, text on the active navigation row, button text on the accent, and links on panels.
- `themeDefaults.test.ts` checks the stylesheet's fallback blocks, the full contract for each accent and surface, and the aliases.

**Not in this change.** The remaining `[data-theme='dark']` patch rules and literal colors are kept for now. They are pruned page by page in a follow-up, with screenshots, since some cover colors the contract does not define.

**Checked.**
- Screenshots of Search, Query, Stats, Library, Settings, Help, and Chat in light and dark. In dark, Query's select arrows are now light. The chat package follows the same panels and accent as the rest of the app.
- Server tests (237) and web tests (65) pass.

## v1.90 (2026-10-10)

### Change log through v1.89 (#391)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.89 release.

### Parallel Works packages to ui 0.34 and workflow-parser 0.8, with Select chats in the list's top slot (#392)

**What was wrong or missing**

The web app was on `@parallelworks/ui` 0.31.0 and `@parallelworks/workflow-parser` 0.6.1, and the server on `@parallelworks/workflow-parser` 0.1.0, so the browser and the server converted workflow forms and prepared submitted values with different parser releases. The current releases are ui 0.34.0 and workflow-parser 0.8.0.

Select chats was placed in the conversation list by finding the package's New chat button in the DOM, watching it with a MutationObserver, and portaling a row into its group. ui 0.33.0 added `sidebarTop` to `ChatLayout` for a host's own content at the top of the list (parallelworks/foundation#198).

The conversation scrubber has not shown since the move to `@parallelworks/ui` 0.24 (#369). It found the message column by the class `max-w-4xl`, which the new package does not use, and ui 0.32.0 changed that column's class again.

**What changed**

- Versions: web `@parallelworks/ui` 0.31.0 to 0.34.0 and `@parallelworks/workflow-parser` 0.6.1 to 0.8.0; server `@parallelworks/workflow-parser` 0.1.0 to 0.8.0. The parser now depends on js-yaml 5, and js-yaml 4 leaves the lockfile. Neither package marks a breaking change in these ranges, and the functions the Studio calls keep their signatures, so no call site changed. The parser's `logSegments` module, removed after 0.1.0, was not used by the Studio.
- Select chats renders through `sidebarTop`. The portal, the observer, and the `ade-rail-extra` rule are gone. The slot sits above the package's own controls, so the row is now above New chat. It keeps its icon on the collapsed rail and closes the phone drawer when picked, as the package's rows do; before, the drawer stayed open over the selection panel. It takes the package's `hover:chat-tint` like the rows beside it.
- The scrubber finds the thread by the package's `chat-thread` class and that element's first child, which does not depend on the column's width class.
- From the packages, with no Studio change: the chat column widens on a wide window (1022px at a 2200px window, unchanged at 1440px); the latest long reply stays open, so a conversation opens on its last line; the composer keeps send inside its box on a narrow screen. The 0.34 `ConversationLayout`, `drawerBar`, and the new 896px `DRAWER_BELOW_PX` apply only to `sidebarMode="auto"`. The Studio sets inline or drawer itself at its 760px phone breakpoint, so its layout is unchanged.
- workflow-parser 0.8.0 keeps a workflow's `$meta.layout` through form conversion. ui 0.34.0 skips `$meta` when it lists fields (the layout renderer, parallelworks/foundation#202, merged after 0.34.0), so a workflow that declares a layout shows its fields in their listed order until a later ui release.
- docs/HELP.md says Select chats is above New chat. docs/REFERENCE.md describes the folded older replies and the scrubber.

**How it was verified**

- `pnpm test`: server 237 passed, web 54 passed. `pnpm build` passes.
- Parser parity: on 76 workflow definitions exported from the platform, `convertToDynamicForm`, `prepareSubmittableValues` (with each input's default), `workflowHasUserInputs`, `getAllDeps`, and `getStepLabel` return identical output on 0.1.0 and 0.8.0. `dumpYaml` differs on 14 of them: js-yaml 5 leaves a plain scalar that starts with a dash unquoted (`default: -c hostname` where 0.1.0 wrote `'-c hostname'`). All 76 dumps parse back to the original object under js-yaml 5, the `yaml` package, PyYAML, and Go's yaml.v3.
- Headless Chrome against a build of this branch, with a temporary knowledge base and seeded conversations, at 1440, 2200, 1024, and 760px wide:
  - Select chats is above New chat with the same 2px spacing as the package's rows, shows its icon on the collapsed rail, opens the selection panel, and deleted two picked conversations after the confirmation. At 760px it is in the drawer, and picking it closes the drawer.
  - In a conversation with two long replies, the older one is folded and the latest is open, with the thread scrolled to its last line.
  - The scrubber shows eight ticks for an eight-message conversation, and hovering one previews its message.
  - A fixed-position element inside the chat's main area still covers the whole viewport after 0.32 made that area a size container, so the embed viewer's full-window overlay is unaffected.
  - The Workflows tab lists a GitHub workflow (parallelworks/activate-batch), renders its form with the 0.8.0 WASM parser loaded, and Validate reaches the server and the platform CLI.
- Not checked: the platform logins on the test host are expired, so the model list was empty, no reply was streamed, the cluster picker could not list clusters, and Validate stopped at the platform's credential error. Hover tints were not checked, since headless Chrome reports no hover capability and the package's hover classes apply only where hover is available.

### Navigation in groups with a search box, and a user guide rail in groups (#393)

The menu listed nine pages in one column, and the user guide's rail listed fourteen sections. Both read as a wall. They now group the way the ACTIVATE platform's menu does, and the sidebar has a search box.

**Menu** (`App.tsx`, `styles.css`)
- **Groups:** Chat stays a direct link, then two groups:
  - **Knowledge:** Library, Search, Query, Stats, History.
  - **Run:** Workflows, Agents.
- **Group headings:** each heading is a label at 11px / 500 in the muted color, with a 12px chevron that rotates over 200ms as the group's pages slide open or closed. Groups start open, and a closed group is remembered (`ade-nav-closed`). A group that a deployment leaves with one page shows that page on its own.
- **Items:** 32px rows (`6px 12px`) with 14px / 400 muted labels and 6px corners. The active page is filled and set at weight 500. Icons are drawn at 1.5 stroke with round ends.
- **Icon rail:** 64px, showing every page flat. The collapse control is a 16px sidebar icon in place of the `«` text button.
- **Phone:** the bottom bar is unchanged; the group wrappers drop out of its layout.

**Search box.** Under the brand, as on the platform. Enter opens Search with the query and runs it. Cmd+K or Ctrl+K reaches the box from anywhere, or opens Search where the box is hidden (the icon rail and phones). Escape clears it.

**User guide rail** (`HelpView.tsx`)
- `docs/HELP.md` groups its pages under level-one headings: Basics, Organize and analyze, Run work, More.
- The rail shows each group as a row with a rotating chevron, its pages under a thin rule. Only the group being read starts open, so Help opens on the Overview and a few headings.
- The page being read is tinted in the accent.
- A guide without level-one headings, such as a deployment's own `HELP_FILE`, still shows as one flat list.

**Throughout.**
- A 2px accent `:focus-visible` outline.
- Transitions turned off under `prefers-reduced-motion`.
- Thin scrollbars that darken on hover.

**Docs.** The user guide's Getting around section covers the search box, its shortcut, and the groups. `docs/CUSTOMIZATION.md` describes the `#` grouping for custom help files.

**Checked.**
- Screenshots of the menu in light and with a group closed, the search box running a search after Ctrl+K, the Help rail in dark, the icon rail, and the phone bar.
- A new web test for the guide parser covers grouped, flat, and the shipped guide.
- Server tests (237) and web tests (57) pass.

## v1.89 (2026-10-10)

### Change log through v1.88 (#389)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.88 release.

### A short user guide that starts with a quick start, and a README to match (#390)

Readers found the user guide and the README overwhelming. Both had grown one detail at a time, and the guide's 2,265 words listed everything the app does.

**User guide (`docs/HELP.md`, the in-app Help): 2,265 words to 948.**
- The Overview page opens with **Start here**: five steps (ask a question, find a file, browse, add material, run a workflow).
- Each part of the app gets a sentence or two on what it is for, plus the few actions people use there.
- The left rail gains icons for Workflows, Agents, and Using it from other tools.
- "Built on" moves to the README only. "RAG for other tools" becomes the shorter "Using it from other tools".

**Reference (`docs/REFERENCE.md`, new): the details the guide no longer carries, by part of the app.** These include search syntax, keyboard use, model markers, the find bar, query types, label colors, the voice preview, and the two external endpoint models. Nothing was dropped.

**README: 1,543 words to 486.** It has a one-paragraph description and a quick start for ACTIVATE and for your own machine. What it does gets one line per feature, followed by the documentation table, "Built on", and contributing.
- Deployment, container, and model setup move to a new `docs/DEPLOY.md`.
- The architecture diagram moves to `docs/ARCHITECTURE.md`.
- The repository layout and the release procedure move to `CONTRIBUTING.md`.

**Keeping it current.**
- `CONTRIBUTING.md` gains a "The user guide" section: a user-facing change updates `docs/HELP.md` in the same pull request, and the README when it changes what the Studio offers. It keeps the guide short and task-first, puts details in the reference, and names settings exactly as the Settings page labels them.
- The pull request template carries the same reminder as a comment, so it stays out of the release notes generated from these descriptions.

**`studio_docs`** reads the two new documents (`reference`, `deploy`).

**Checked.**
- Every detail removed from the guide is in `REFERENCE.md` or a topic document.
- The new text has none of the phrasings our style rules ban.
- The Help page renders in a browser: the Overview shows Start here, and the rail lists the sections with their icons.
- Server tests (237) and web tests (54) pass.

## v1.88 (2026-10-10)

### Change log through v1.87 (#387)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.87 release.

### Help and README cover the Workflows tab, desktop install, libraries, and session agents (#388)

## Help and README catch up with recent releases

The in-app help (`docs/HELP.md`, also what the assistant reads through `studio_docs`) and the README had fallen behind v1.73 to v1.87.

**`docs/HELP.md`**
- A new **Workflows** section covers:
  - tiles from the account, the marketplace, and GitHub;
  - Validate and Run, presets, and recent runs;
  - approval for GitHub workflows that ask for account-variable access;
  - how administrators choose the set, including **From GitHub** and **Find workflows**;
  - that the assistant sees the same set.
- **Chat:** **Select chats** for bulk deletion.
- **Library:** the library picker, and Settings, "Libraries" with each library's paths.
- **Agents:** where delegation is configured and the three ways agents run.
- **Getting around:** **Install app**, the title bar taking the banner color, and Chrome's chevron moving the banner into the title bar.
- **Built on:** names `@parallelworks/ui` in place of `@parallelworks/ai-chat`.

**`README.md`**
- The integration paragraph mentions the Workflows tab, session agents, and desktop install.
- The Workflows paragraph covers marketplace and GitHub workflows and the approval.
- A new **Agents** section is added.
- A new **Documentation** table lists every document.
- The `web/` layout line lists every view.
- The chat package name is updated.

**Corrections**
- Delegation settings are under Settings, "External access". `docs/AGENTS.md`, the Agents tab's empty-state text, and a comment in `tasks.ts` said "Settings, Delegation", which does not exist.

**`studio_docs`** now reads every document under `docs/`: help, architecture, customization, libraries, workflows, agents, multi-user, and macos. Before, it read only the first three.

Server tests (237) and web tests (54) pass.

## v1.87 (2026-10-10)

### Change log through v1.86 (#384)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.86 release.

### fix(web): the phone chat uses the package's drawer, and the picker names shared providers' owners (#385)

Bumps `@parallelworks/ui` from 0.24.2 to 0.31.0. That release lets a host own the conversation list's drawer (parallelworks/foundation#187), which this view had been building out of CSS.

**Phone conversations list**
- At the phone breakpoint (760px, the same one the bottom bar uses) `ChatLayout` runs with `sidebarMode="drawer"` and `drawerToggle={false}`. The bottom-bar Chat item still toggles it, now through `ChatProvider`'s `drawerOpen` / `onDrawerOpenChange`.
- The package closes the drawer on a pick, New chat, a tap outside or Escape, and returns focus to whatever opened it.
- Removed: the transform rules on the layout's first child, the hidden package toggle, our backdrop element, and the `.chat-rail-backdrop` styles. The show-all filter still rides on the open drawer, raised above the package's drawer layer.

**Activity drawer**
The package dropped the right-hand Activity drawer in 0.27 (reasoning now shows in place in the thread). The DOM observer that mirrored its open state, the width variable, the drag handle and their CSS had nothing left to act on and are removed.

**Shared providers**
The server no longer appends "· shared by <owner>" to the names of providers shared into the account. Since 0.31.0 (parallelworks/foundation#192) the picker names the owner itself, from each model's `provider_owner`, which the gateway already sends; organization providers have none and are not labeled.

Checked in the browser at 420px and 1400px widths: closed by default with the thread at full width, opened from the Chat item, closed by a tap outside, New chat and Escape, and back to the inline column when widened. `pnpm test` passes (server 230, web 54).

### Delegated agents can run as pw code sessions (#386)

Settings, Delegation has a new "Agent execution" choice (`AGENT_EXECUTION`):

| Value | Agents run as |
|---|---|
| `runner` (default, unchanged behavior) | one-shot `pw code -p` runs, or platform workflow runs for a campaign |
| `sessions` | sessions in the local pw code daemon |
| `both` | either, chosen per task; the `delegate` tool gains a `runtime` parameter, defaulting to sessions |

Campaigns on other systems stay on the runner until remote sessions arrive through the platform's agents routes.

**Session agents.** A new `server/src/pwcode.ts` talks to the daemon over its Unix socket, `$XDG_STATE_HOME/pw/code-<hostname>.sock`, or `PW_CODE_SOCKET`. Only the account running the Studio can open that socket. The client checks protocol 2 by strict equality, as the CLI does. If no daemon is running, it starts one pinned to `PW_CONTEXT`.

Each agent is a read-only session created with:
- the task board registered inline, as an agent definition passed in `agentsJson`;
- the board and corpus tools pre-approved, as `mcp__task__*`;
- remote control on.

The board is reached on loopback. The task token travels only in the request body, never in process arguments or a file.

The Studio polls each session (`STUDIO_SESSION_POLL_MS`, default 2 seconds):
- History goes to the agent's live log.
- An approval request makes the agent `input-required`. It is posted to the board and shown in the Agents view under "Waiting for your approval", with Approve and Deny (`POST /api/tasks/:id/agents/:name/approvals/:approvalId`).
- A working agent takes direction mid-turn from a Steer box (`POST .../direction`).
- The end of the turn completes the agent through the existing result contract. Token usage comes from the session state.
- A turn that ends without a reply fails the agent with a reason.

Stopping a task interrupts its sessions. A restart reattaches to working sessions, since they live in the daemon. `GET /api/tasks/runtime` reports the setting and the daemon's version, usability, and remote session policy, and Settings shows that line.

**Runner fixes.**
- Runners no longer write `.agents/settings.local.json`. pw code ignores that file until someone approves it interactively, so the board never reached one-shot workers.
- Campaign runs are no longer sent the board token. The session proxy in front of the Studio's public address refuses it anyway.
- Prompts mention board tools only to agents that have them.
- A delegation depth of 0 was treated as missing and replaced by 2, so "0 stops agents starting their own subtasks" never applied. It is now kept.

**Checked.**
- `sessionAgents.test.mjs`, against a fake daemon on a Unix socket, covers the session request, the board waiting to connect before the prompt, completion and usage, approval, steering and interrupt, a turn with no reply, and the settings values.
- `board.test.mjs` and `tasks.test.mjs` are updated for the runner.
- Against the real daemon (v7.122.0, protocol 2), sessions are created read-only with remote control on. The inline board connects over loopback with 12 tools, and a turn refused for an expired platform login fails the agent with its reason. The success path with a live model is not yet run.
- Server tests (237) and web tests (54) pass.

Docs: new `docs/AGENTS.md`; the Agents tab section of `docs/HELP.md` is updated.

## v1.86 (2026-10-09)

### Change log through v1.85 (#382)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.85 release.

### Settings, Libraries shows each library's index and source roots (#383)

## Settings, Libraries shows where each library points

Each library in Settings, Libraries now shows its index root (the GUFI tree) and its source root under its name. A library whose files are not on this host shows "not on this host" for its source. This covers the knowledge base, libraries pinned by the deployment, and libraries added in Settings.

The paths come from the Settings listing (`GET /api/libraries`, and the reply to adding a library) through a new `settingsLibrary`. The client config that every viewer loads (`/api/config`) still uses `publicLibrary` and carries no paths. The existing test for that stays, and a new test checks that the Settings listing has both roots, with a null source for a library that has no files here.

Layout:
- Paths wrap only after a slash, never inside a directory name, and a selected path copies as written.
- The library column takes 45% of the table; the other headers may wrap to two lines.
- The empty actions column no longer reserves 120px.
- At desktop width each path fits on one line; at 760px they wrap at slashes.

`docs/LIBRARIES.md` notes where the paths appear. Server tests (230) and web tests (54) pass.

## v1.85 (2026-10-09)

### Change log through v1.84 (#380)

Regenerated with `node scripts/release-notes.mjs --changelog` after the v1.83 and v1.84 releases.

### Installed app: the classification banner can take the title bar's place (#381)

## The classification banner can take the title bar's place in the installed app

In the installed app, the title bar sits above the classification banner, so two strips of the same color take about 55px at the top of the window. A Studio with a banner now declares the window controls overlay (`display_override: ["window-controls-overlay"]` in the manifest). When it is on, the page draws in the title bar area and the banner fills it: one strip of about 32px, with the marking between the window controls.

Chrome opens a newly installed app with the standard title bar. A chevron button in the title bar turns the overlay on, and Chrome remembers the choice for later launches. Until a viewer turns it on, nothing changes. A Studio without a banner does not declare the overlay, so it gets no chevron.

With the overlay on, the banner:
- takes the title bar area's height (`env(titlebar-area-height)`);
- drags the window (`app-region: drag`);
- keeps its text centered on the window, with clearance on each side equal to the wider of the two control areas;
- wraps to two lines at a smaller size in a window narrower than about 1000px, so the marking is never truncated.

The banner keeps its text and color, and it is drawn by the page, so it is on screen whenever the app is. The Settings preview of the banner is not affected.

Checked by applying the overlay rules with Mac-like title bar values in headless Chrome, which cannot run the overlay itself: one line at 1440px and 960px, two lines at 720px and 560px, never clipped. In the browser and in the installed app without the overlay, the banner is unchanged (25px, no padding). Server and web tests pass, including a manifest test that only a Studio with a banner declares the overlay.

## v1.84 (2026-10-09)

### GitHub workflow tools: component workflows from a repository (#379)

## GitHub workflow tools

Component workflows kept in a GitHub repository, and never published to the marketplace, can now be offered on the Workflows tab and used by the assistant. One example is a design of experiments or a design explorer that other workflows call as subworkflows.

Settings, Workflows, From GitHub takes a repository, a directory, a workflow file, or a GitHub link (`/tree/<ref>/<path>` is rewritten to `<path>@<ref>`). It lists every workflow file found there: each directory's `workflow.yaml`, or each of its `yamls/<variant>.yaml` files. Each row shows the title and first paragraph of the directory's README, and ticking a row adds it to the set. Tiles take the same title and summary. A tile's icon is the first PNG, JPEG, or WebP in the directory's `thumbnails/`. SVG is skipped, and icon responses now carry a sandboxing Content-Security-Policy.

`pw workflows run` accepts a directory only when it holds a `workflow.yaml`. The Studio therefore resolves each entry to its file and runs that file, ref included. A directory with several variants is refused until one is named.

A workflow can declare `permissions` (account variables, `'*'` for all). The platform runs it only with `--trust`, validations included, and the grant stays with the repository until it is revoked.
- **Workflows tab:** the requested access is shown above Run and Validate, which stay disabled until the viewer ticks the approval.
- **Assistant:** `run_workflow` returns the request without running, and passes `--trust` only when called with `trust: true` after the user approves.

Assistant tools:
- `list_workflows` now includes the curated marketplace and GitHub entries.
- A new tool, `list_github_workflows`, browses a repository.
- `get_workflow`, `workflow_configs`, and `run_workflow` accept every kind of entry. `get_workflow` also reports declared permissions.

CLI errors on the run route are shown without terminal color codes. A CLI too old to run a GitHub workflow file says so: v7.78 cannot, v7.105 can.

GitHub reads use the public API, which allows 60 requests an hour without a token. Each repository's file list is cached for 10 minutes. `GITHUB_TOKEN` is used when set.

Tests: new `githubWorkflows.test.mjs` (browse, resolve, tiles, permissions and approval, old CLI, rate limit) and `githubWorkflowTools.test.mjs` (the chat tools); `workflowEntries.test.mjs` updated for the resolved file entry. Checked against the live `parallelworks/workflows` canary branch: browsing `workflows/` finds 118 workflow files in under a second, the DOE form renders with the approval notice, and a validation without approval is held with the requested access reported.

## v1.83 (2026-10-09)

### Release notes print each pull request's heading once; change log through v1.82 (#377)

Pull request descriptions that open with their own title as a heading (`### Title (#N)`) printed that heading twice in the release notes and `CHANGELOG.md`, once from the script and once from the description; every Studio release since v1.53 carried the duplicate. `scripts/release-notes.mjs` now drops a description's first line when it is a heading repeating the pull request's title, with or without the number. Descriptions that do not open that way are unchanged. The change log is regenerated through v1.82: no duplicate headings remain.

### Installed app title bar matches the classification banner (#378)

With a classification banner configured, an installed Studio window showed its title bar in the page's gray, the banner's color directly below it, and the page under that: a stripe of a third color at the top of the window. The title bar now takes the banner's color whenever the banner is drawn, so the bar and the banner read as one strip.

- `bannerShownColor()` in `ClassificationBanner.tsx` returns the banner color when the banner is on screen (text set, and not inside the platform frame unless `bannerWhenEmbedded`), else null; the banner component uses it too.
- The page sets `theme-color` to that color, falling back to the page ground as before, and remembers it; the pre-paint script in `index.html` applies the remembered color, so a launch is the right color before the configuration loads.
- The manifest's `theme_color` is the banner color when a banner is configured (a valid hex color only), so the first frame of an installed launch matches as well; `background_color`, the splash, stays the page ground.

Checked against a build with a CUI banner (#24612e): manifest `theme_color` #24612e with background #f3f4f6; on load the banner and `theme-color` are both #24612e and the color is remembered; on the next load `theme-color` is #24612e before the configuration arrives. New tests for the manifest with and without a banner and for `bannerShownColor`; all server and web tests pass.

## v1.82 (2026-10-09)

### chore: move to Node 26 (#371)

Moves everything from Node 22 to 26: the image's base images (now pinned by digest, still bookworm), CI, the Node runtime bundled for compute resources (v26.10.0) and the version check that decides whether to use it, `engines`, `@types/node` and the docs.

Node 25+ defines its own `localStorage` global, which is unusable without `--localstorage-file` and hides jsdom's, so the web tests run with `--no-experimental-webstorage`.

Node 26 becomes LTS later this month; until then it is the Current release.

### ci: harden workflows (#372)

Split out of #370; nothing here depends on the container work.

- Read-only token permissions on `ci` and `gufi-macos`.
- Actions pinned to commit SHAs at their latest releases (checkout v7, setup-node v7, pnpm/action-setup v6).
- Checkout no longer leaves credentials in `.git/config`.
- `step-security/harden-runner` audits outbound network traffic on the Linux job.
- 30-minute job timeouts, and a newer push cancels a superseded PR run.
- `gufi-macos`'s free-text `gufi_ref` input reaches the script through `env` instead of `${{ }}` interpolation, which allowed shell injection.

### chore: patch advisories, verify the Node runtime, automate pin updates (#373)

Follow-ups from the #370 review.

**Dependabot alerts.** `pnpm-workspace.yaml` overrides lift the three open advisories:
- `source-map-js` 1.2.1 → 1.2.2 (high, GHSA-68fv-2mgg-jv7q; dev only, via vite)
- `@fastify/busboy` 3.2.1 → 3.2.2 (moderate, GHSA-gxm5-99cw-xjw9; multipart uploads)
- `katex` 0.16.47 → 0.18.11 (low, GHSA-238p-pmpm-9mq7). This one crosses the `^0.16` its dependents ask for, since no 0.16.x has the fix. Inline and display math still render through rehype-katex with no errors.

**Node runtime checksum.** `make_bundle.sh` and both deploy workflows now check the Node tarball against nodejs.org's published sha256 before unpacking it. The bundle script checks its cache on every run too.

**Pins stay current.**
- `.github/dependabot.yml`: weekly, grouped updates for GitHub Actions (SHA pins and their version comments) and npm, with a one-day cooldown matching `minimumReleaseAge`.
- `base-images` workflow: weekly, checks each digest-pinned `From:` in `app.def` and opens a PR when one moves. Job-token pushes do not trigger `pull_request` workflows, so it dispatches `ci` and `container` on the branch itself; `ci.yml` gains `workflow_dispatch` for that. Dispatching `container` works once #370 is merged.

Server and web tests pass locally on Node 26.

### ci: add container build workflow (#370)

The container build workflow is separate from the CI because it takes a long time to build dependencies.

<!-- This description becomes the change note in the release notes and CHANGELOG.md. -->

**What was wrong or missing**

We need a .github/workflow for building the whole container

**What changed**

Added `container.yaml` and added `set -e` in the `build` stage of `deploy/app.def`.

**How it was verified**

Examined the workflow file directly. It needs to be included in a PR (this PR in particular)
to be run for the first time in an actual runner environment.
<!-- Tests added or run, and anything checked by hand (a browser, a deployment). -->

### ci: block unexpected egress, attest images, lint workflows (#374)

Follow-up hardening.

**Egress blocked, not just audited.** harden-runner switches to `egress-policy: block` on the jobs that run third-party code: ci `build-and-test` (package install scripts), container `build-test` (GUFI and llama.cpp build scripts as root), and `zizmor`. The allowlists come from the endpoints the audited runs actually reached. The `push` and `base-images` jobs stay on audit until a run of each shows its full set; neither runs build code.

**Provenance.** The push job attests the exact `studio.sif` it pushed (`actions/attest-build-provenance`). Verify a pulled image with `gh attestation verify studio.sif --repo parallelworks/activate-studio`.

**zizmor** checks the workflows and `dependabot.yml` on every PR and fails on findings, with annotations. It is clean on this branch.

**Container as a required check.** The workflow now starts on every PR and push to main. A `changes` job decides whether the image's inputs changed, failing open to building, so `build-test` either runs or is skipped (which counts as passing) and can be required.

**Smaller.** Dependabot waits 7 days after a release (zizmor's `dependabot-cooldown`; security updates are not delayed). `base-images` no longer keeps its token in `.git/config` and hands it to git only for the push.

After merge, the repo ruleset adds `build-test` and `zizmor` as required checks.

### ci: allow apt's fallback Ubuntu mirrors through the egress block (#375)

The container build on #369 failed installing SingularityCE: `azure.archive.ubuntu.com` timed out, apt fell back to `archive.ubuntu.com` and `security.ubuntu.com` over HTTPS, and harden-runner blocked both since only the Azure mirror was allowlisted. This adds both fallback hosts, on ports 80 and 443, to the `build-test` and `build-and-test` allowlists, so a mirror outage degrades to the fallback instead of failing the job.

### chore(web): use @parallelworks/ui 0.24, with the chat from @parallelworks/ui/ai (#369)

<!-- This description becomes the change note in the release notes and CHANGELOG.md. -->

**What was wrong or missing**

Studio depended on `@parallelworks/ai-chat` 0.5.0 and `@parallelworks/ui` 0.17. The chat package has moved into `@parallelworks/ui` as `@parallelworks/ui/ai`, and ui changed how workflow forms and graphs work in 0.20 and removed unused props and members in 0.21. Both packages also shipped their own Tailwind build, which forced Studio to manage the cascade order between them.

**What changed**

- Swapped `@parallelworks/ai-chat` for `@parallelworks/ui/ai` and upgraded `@parallelworks/ui` to ^0.24.2. ui's prebuilt stylesheet now includes the chat rules, so the separate chat stylesheet and its `pw-ai-chat` layer are gone. ui still emits a top-level `base` layer, so `layers.css` keeps declaring it first.
- `DependencyGraphPreview` now comes from `@parallelworks/ui/graph`, since `/workflow` was renamed.
- ui's workflow form and graph no longer embed the workflow parser. They call a `WorkflowEngine` supplied through `UIProvider`. Studio mounts `UIProvider` at the root with a loader that imports `@parallelworks/workflow-parser` (new dependency, ^0.6.0) on first use, so the parser stays out of the initial bundle. The form and the graph each sit in a `Suspense` boundary while it loads.
- 0.21 removed `ChatNavigation.toAttachments`, which the chat never called; the attachments view is still reached through the navigation target.
- 0.23 added `providerIssues` to the models list, for flagging AI connections whose key is rejected or endpoint is unreachable. The adapter passes it through from the server, defaulting to none; the server does not report these yet, so the existing "last call failed" model label stays.
- Starting values are unchanged: `initializeValues` is called as before, with no blank-default predicate, matching 0.17's default behavior.

**How it was verified**

- `pnpm build`: web type-check and build plus the server build pass; the production bundle carries the parser's WASM as its own asset.
- `pnpm test`: all 217 server and 53 web tests pass (Node 26).
- Ran `pnpm dev` on 0.24.2 and checked in headless Chrome, with the platform endpoints mocked:
  - Chat: renders and the composer enables once a model is listed. A provider issue on the selected model's provider shows the new banner ("API key rejected").
  - Workflow graph (`?embed=dag`, two-job workflow): renders Build → Test.
  - Workflow form: renders with defaults, and a field whose `hidden` expression depends on a toggle appears when the toggle flips, so the parser is evaluating expressions.
  - Found and fixed: under `pnpm dev` the form never rendered. `@parallelworks/workflow-parser` 0.6.1 (which the lockfile now resolves) loads `parseWorkflow.wasm` from beside its module, and Vite's dependency pre-bundling moved the module without the file. `vite.config.ts` now excludes it from pre-bundling. Production builds were unaffected.
  - Large pastes stay as text: ui only turns them into cards when the host supplies an upload handler, which Studio does not.
- Not checked: running a workflow against the platform, or sending a chat message to a real model (no credentials in this environment).

### Workflow forms laid out for reading (#376)

The workflow form on the Workflows tab is the platform's own renderer, and at the Studio's width it read poorly: section headers at 18px bold, louder than the workflow title; labels wrapping in an unaligned column with their help icons pushed to the far edge; an empty multi-select drawn as a field-sized box; Run and Validate as small buttons below the last field, off screen on long forms; and a wide gap between the title and its description.

Scoped to the runner page (`.wf-runner`, `.wf-form`), so no other use of the form library changes:

- **Header:** back link, title (20px, text color), and description grouped and tightened.
- **Section headers:** 13px semibold with a light rule and a small muted chevron.
- **Fields:** a fixed 210px label column aligned to the 32px controls, labels in normal text flow so the required mark and help icon sit right after the text, and 12px between rows.
- **Multi-select:** the trigger matches the other controls; the empty "nothing selected" table becomes a compact dashed line without its header.
- **Action bar:** Run (primary, first) and Validate in a bar pinned to the bottom of the view with a rule above it, the validation or run result beside them.
- **Saved configurations:** the preset selector sits in a light panel on the same label column.
- **Phone width:** labels stack above their controls with no gap.
- **Dark mode:** the required mark uses a lighter red that reads on the dark ground.

Checked in headless Chrome on a marketplace Jupyter form (cluster picker, switches, text fields, a single and a multi-select, two sections) and the ACTIVATE Batch form, in light and dark and at phone width: label-to-control gap 0 at phone width for every field, and with a 700px viewport the form runs to 745px while the action bar stays at 640 to 700. All server and web tests pass.

## v1.81 (2026-10-02)

### Images whose caption failed are described on the next pass (#368)

An uploaded image got no text in the index and never would. The configured vision model refused the caption request (403, in 0.2 s), OCR correctly found no text in a photograph, and the indexer cached the empty result: images cache even when empty, so a picture with nothing to transcribe is not re-sent to the vision model on every pass. That rule could not tell an image with nothing to say from one that was never described, so the empty entry outlived the failure, including after the vision model setting was changed to one that works. The failure itself was invisible: `enrich.py` reports it on stderr, which the server discarded whenever the pass as a whole succeeded.

**Retry.** `caption_image` now returns `None` when the request fails and `''` when captioning is off or the image is too large. A failed caption is not cached, so the next pass asks again; the OCR text, if any, is still indexed for that pass. While a vision model is configured, an empty cached image result is treated as missing and re-extracted: a successful caption is never empty, so such an entry was written by a failed request or before captioning was on. With captioning off, empty entries stand as before. `warm_image_cache.py` follows the same rules.

**Visible.** Lines from the indexer's stderr that report a failure (`caption failed ...`, `extract failed ...`) go to the server log through the sweep's logger, prefixed `index:`, even when the pass succeeds.

**Tested** with the real `enrich.py` against a stand-in gateway in the test process: a refused caption leaves no cache entry and the next pass, with the gateway answering, caches the description; a good entry is reused without a request; an empty entry is retried with a model configured and kept with none. All server and web tests pass.

## v1.80 (2026-10-02)

### Change log through v1.79 (#366)

Regenerates CHANGELOG.md for v1.78 and v1.79.

### Open conversation in the chat rail has no edge bar (#367)

The open conversation in the chat rail was marked with a tinted background, a bold title, and a 3px accent bar on its leading edge (added in v1.68 when the package's own mark was too faint). The bar is removed in both themes; the tint and the bold title remain, so the row is a rounded pill like the rest of the rail. Checked in headless Chrome: no box-shadow on the active row in light or dark, the tint unchanged.

## v1.79 (2026-10-02)

### Workflow form fields look like inputs again (#365)

In a workflow's form, text and number fields rendered as bare text: no border, no background, no padding. The form library draws them with a global `input, textarea` rule in a `base` cascade layer. Since v1.65's layer ordering (`web/src/layers.css`), the Studio ranks that layer below the shared packages' Tailwind resets on purpose, so it cannot restyle inputs elsewhere in the Studio; inside the workflow form the resets therefore won and stripped the fields. Selects and text areas were unaffected because the Studio draws those itself.

The workflow form now gets the Studio's own field look, scoped to `.wf-form`: border, input background, 6px radius, padding, a focus ring, a muted placeholder, and a 32px height shared with the Studio's selects. A combobox input keeps room on the right for its arrow button.

The library's Yes/No switch paints its handle and its unselected half with a fixed white-to-gray gradient and has no dark variant, so in dark mode it showed as a bright white bar; in dark mode those parts now take shades of the panel color, with the handle a step lighter than the label. This also applies to the switches in Settings.

Checked in headless Chrome on a marketplace workflow's form (cluster picker, switches, text fields, a single-select dropdown, a multi-select) in light and dark: every field has a 1px border, the input background, and 8px padding. All tests pass.

## v1.78 (2026-10-02)

### Change log through v1.77 (#363)

Regenerates CHANGELOG.md for v1.75 to v1.77.

### Workflows tab: marketplace and GitHub workflows (#364)

A Studio's workflow collection was limited to workflows saved on its account. An entry can now be any of three kinds, the same forms `pw workflows run` accepts:

| Entry | Runs |
|---|---|
| `name` | a workflow saved on the account, as before |
| `marketplace/<slug>` | the marketplace workflow by slug, with no copy in anyone's account |
| `github.com/<owner>/<repo>[/path][@ref]` | the workflow.yaml in that repository, directory, or named file, with no copy in anyone's account |

**Settings, Workflows** lists the account's workflows as before, the marketplace's workflow items below them (both filtered by the same box), and a From GitHub field. Adding a GitHub entry first reads its workflow.yaml and refuses it with the reason if it cannot. The settings write validates every entry against the three forms (`server/src/workflowEntries.ts`, shared with the routes) and drops anything else, including `..` segments and other hosts.

**Definitions.** Account workflows come from `pw workflows get`, marketplace ones from `pw marketplace get <slug> --yaml` with their name, description, and icon from `pw marketplace ls` (cached five minutes), and GitHub ones from the repository's raw file (workflow.yaml, then workflow.yml). The CLI cannot print a repository's YAML, so the form for a private repository cannot be shown; the run still goes through `pw workflows run`, which reads private repositories through the platform's connected account. A GitHub tile's icon is the repository's thumbnail.png by the platform's convention, and a tile whose icon is missing or fails shows its initial.

**Routes** move from `/api/workflows/:name/...` to `/api/workflows/item/{form,icon,run,install}?w=<entry>`, since entries contain slashes. Install answers 409 for marketplace and GitHub entries, and the tab offers it only for account workflows.

**Verified** against a live platform with one entry of each kind: tiles with icons; forms for all three; a GitHub dry run validated by the platform; the catalog listing 76 account and 99 marketplace workflows. New tests: entry parsing and refusal, raw URLs, tiles, forms (including a missing repository), runs passing the entry to the CLI as written, install refused for non-account entries, and the settings write. 213 server and 53 web tests pass.

## v1.77 (2026-10-02)

### Brand images carry a version in their address (#361)

The brand icon and favicon were always served at the same address (`/api/brand-icon`, `/api/favicon`) with an hour of browser caching, so changing the icon in Settings left browsers showing the old one for up to an hour. Each address now carries `v=`, a short hash of the configured path or URL plus the file's modification time, so a different image, or the same file replaced, is a new address that browsers fetch at once. The installable-app manifest uses the same versioned address. A new test checks that replacing the file changes the address; all tests pass.

### Select and delete chat conversations in bulk (#362)

The chat package's rail deletes conversations one at a time. Until it can select several, the Studio adds a Select chats row to the rail, under New chat and Attachments, which opens a panel in the main pane like the package's Attachments page.

**The panel.** Every conversation with a checkbox, its date, and message count; All (or All shown, when filtered); a title filter; shift-click to set a range; Delete N, which asks for confirmation on the page and then removes them in one request. Clicking a title opens that conversation. If the open conversation is among those deleted, the view returns to a new chat. Done, or any rail navigation, closes the panel.

**Who can delete what.** Only the viewer's own conversations, or ones with no owner. Under shared history the viewer can see others' conversations; those rows are listed but cannot be selected, and the server applies the same rule: `POST /api/chat/conversations/delete` with `{ids}` deletes what the caller owns, removes those transcripts from the knowledge base, reindexes once, and returns `{deleted, skipped}`.

**The rail row.** The package has no slot for extra rail rows, so the row is portaled into the group that holds New chat, styled like its neighbors, with its label class copied from New chat so it collapses with the rail; a mutation observer re-attaches it if the package re-renders that group.

**Conversation history is saved atomically.** The server rewrote `conversations.json` in place, so a crash, or a read, in the middle of a save saw an empty file, and `load()` treats an unreadable file as having no conversations; the next save would have kept it empty. It now writes a temporary file and renames it into place. The new bulk-delete test found this: it read the file during a save, about one run in twenty-five.

**Verified** in headless Chrome against a seeded instance: the row appears in the rail, eight conversations listed, a shift range selects four, the confirmation names the count, deletion leaves the rest in the panel, the rail, and on the server, with no page errors. Two new server tests (the ownership rule with shared history on, and refusal of an empty list); 40 consecutive runs of the bulk-delete test pass. All server and web tests pass.

## v1.76 (2026-10-02)

### Settings lists workflows with the viewer's key when the deployment credential fails (#360)

Settings, Workflows lists the workflows to pick from with the deployment's credential. It fell back to the viewer's own platform key only when the deployment had no credential at all; an expired one is still a non-empty string, so a deployment whose host login had expired showed the expiry error to an administrator whose own key was valid and connected. The catalog now tries the deployment credential and, when it is missing, expired, or rejected, lists the viewer's own account, and says so on the page: until the deployment credential is renewed, other viewers can run a picked workflow only if it is already in their own account, since adding a copy reads the deployment's version. The response carries `source` (`deployment` or `viewer`). One new test; all server and web tests pass.

## v1.75 (2026-10-02)

### Change log through v1.74 (#358)

Regenerates CHANGELOG.md for v1.74 (#356, #357).

### Workflow pages say when the platform credential has expired (#359)

On a deployment whose platform credential had expired, the Workflows tab and its Settings section showed only "internal error": a failed `pw` call raised a plain error, which the sanitizing handler hides. Every platform call behind those pages now goes through one wrapper that turns a CLI failure into a deliberate error. An expired or rejected credential answers 401 with what to do (add a personal platform API key under Settings, Model access, or renew the deployment credential); anything else answers 502 with the CLI's first line, timestamp removed. The run route keeps the raw CLI text, since the workspace retry recognizes a stopped workspace by it. One new test; all server and web tests pass.

## v1.74 (2026-10-02)

### Change log through v1.73 (#355)

Regenerates CHANGELOG.md, which was missing v1.64 to v1.73. The script now dates tags by creatordate, which is the tagger date for an annotated tag and the commit date for a lightweight one; v1.73 is lightweight and was dated "undefined". Existing entries are unchanged.

### The Studio installs as a desktop app (#356)

Chrome and Edge can now install the Studio as an app: its own window, dock or taskbar icon, and launcher entry. When the browser offers installation, an Install app item appears in the rail footer. Firefox does not install web apps on the desktop; Safari on macOS uses File, Add to Dock. Inside the platform's frame the browser never offers it, so the item appears only when the Studio is opened in its own tab.

**Per deployment.** `/manifest.webmanifest` is built by the server from the deployment's name and brand icon, so each deployment installs under its own name. A PNG brand icon is declared at its real size, read from its header; an SVG as `any`. PNGs of the default mark (192, 512, maskable 512, and a 180 touch icon) follow it, since browsers need those sizes to offer installation. The manifest is never cached, so a rename or new icon reaches the next install.

**No `id`.** A manifest id resolves against the origin, not the manifest's folder, so a Studio mounted under a path would take the host's identity. Without one, the start URL is the identity. A test pins this.

**Behind the session proxy.** Browsers fetch a manifest without cookies by default, which the platform's session proxy answers with a sign-in redirect. The link carries `crossorigin="use-credentials"`.

The `theme-color` meta follows the page ground, so an installed window's title bar matches light and dark.

**Verified** in headless Chrome against a local build: the manifest is found and parsed with no errors, the icons serve, and the only installability error reported is the incognito profile headless runs use. The install prompt itself needs a desktop browser to confirm. 3 new server tests; 201 server and 16 web tests pass.

### Layout and type follow the platform's other apps (#357)

The Studio now uses the layout and type of the platform's newer apps.

**Navigation and grounds.** In light mode the app navigation sits on the gray ground and pages on the panel color; in dark mode the navigation is the lighter of the two. The active and hover pills are neutral, and labels are regular weight: the accent marks what is interactive, not where you are. The Settings and Help section lists follow the same pattern. The brand line drops its letter spacing.

**Page header bar.** Search, Query, Stats, Agents, Workflows, and History share one header: the section's name and a one-line note on what it covers (full text on hover, hidden at phone width), ruled off below. The title cards at the top of those pages are gone. History's caveat that snapshots record the file list and not file contents stays on the page as a note.

**Edge to edge.** Chat, the library, Settings, and Help run flush from the navigation, with no inset card frame.

**Type.** Public Sans with tabular figures, replacing Geist; Geist Mono stays for code. `--font-sans` is set so the shared packages' builds pick it up.

**Contrast check.** A new test (`web/test/contrast.test.ts`) checks every text color against the grounds it is drawn on, for all 6 accents, 3 surfaces, and both schemes, at WCAG AA (4.5:1), using the theme package's own `contrastRatio` and `deriveTheme`. It found the light muted gray at 4.26:1 on the gray ground, which the navigation now sits on; it is now `#646e78` (4.72:1 there, 5.19:1 on white). Four places that used the lighter `--pw-muted-2` for readable text (the sync status line, scope counts, History's check line, the Libraries table) now use the muted color; that token remains for icons and disabled controls.

Checked in headless Chrome at desktop and phone width, light and dark, across every view. 201 server and 53 web tests pass.

## v1.73 (2026-10-01)

### Workflows tab: a curated set of platform workflows, run from their own forms (#354)

### Workflows tab (#354)

Studio users asked for platform workflows they can run directly, without going through the chat. The Workflows tab shows a set of ACTIVATE workflows chosen for this Studio as tiles; each opens the workflow's own input form, with Validate (a dry run) and Run.

**Choosing the set.** Settings has a new Workflows section that lists every workflow on the platform account the Studio is deployed under, so a site deployment picks from that site's own workflows. Ticked workflows become tiles, in the order shown, which can be rearranged. A name no longer on the account is flagged. The deploy forms gain an Offered Workflows input (`STUDIO_WORKFLOWS`, comma-separated) for the initial list; a list saved in Settings replaces it.

**Who runs it.** Workflow records are per user, so each viewer runs their own copy under their own credential and allocation. A viewer who lacks a curated workflow is offered "Add to my workflows", which copies the deployment account's definition into theirs with `pw workflows create`. Only workflows in the set can be run from the tab (403 otherwise).

**The form.** It is `DynamicForm` from `@parallelworks/ui` 0.17 with the platform's schema conversion, so conditional fields, groups, defaults, and saved configurations (as presets) behave as on the platform. The Studio supplies the input types that need live data: clusters, partitions of the chosen cluster, and buckets. Slurm accounts and QoS are text inputs, since no public endpoint lists them. Host fields write through Formik as the built-in ones do. A cluster value carries name, user, and type, so the platform's own submission step builds the right `pw://` reference, including for a cluster shared by another user.

**Elsewhere.** The tab and its Settings section appear only when the Studio can reach a platform. `list_workflows` marks the curated workflows `offeredHere` and lists them first, so the assistant prefers them. Recent runs from the tab are listed under the tiles; unfinished ones are refreshed with the viewer's key, since the background watcher's credential cannot see a viewer's own runs. Docs: `docs/WORKFLOWS.md`.

**Verified** against a live platform: tiles and icons, the catalog (76 workflows), a form with a cluster picked in the browser validating as a dry run, and Settings picking and saving. 8 new server tests (tiles, catalog, form, the 403, reference handling, dry run) and a web test for the section's visibility; 198 server and 16 web tests pass.

## v1.72 (2026-09-30)

### Floor brace-expansion at 5.0.12 (#353)

**What was wrong or missing**

Three advisories against the transitive `brace-expansion` 5.0.9 (two high, one medium) surfaced when Dependabot rescanned after #352.

**What changed**

A floor in the `pnpm-workspace.yaml` overrides, `brace-expansion@5: ^5.0.12`, within its major.

**How it was verified**

The lockfile resolves 5.0.12; `pnpm install --frozen-lockfile` passes; 190 server and 15 web tests pass.

## v1.71 (2026-09-30)

### A contributing guide, a security policy, and issue and PR templates (#351)

**What was wrong or missing**

The repository was public and Apache-2.0 licensed but told an outside contributor nothing: not the pnpm workspace rule that fails CI when broken, not that release notes are generated from PR descriptions, and not the two maintainer approvals or the organization's commit-message rule a PR meets at merge.

**What changed**

- `CONTRIBUTING.md`: setup, the workspace, the 24-hour dependency age rule, tests, and what a PR needs (description as change note, squash merge, two approvals, first-time CI approval, no assistant attribution trailers, no deployment-specific names).
- `SECURITY.md`: private vulnerability reporting, now enabled on the repository.
- `.github/PULL_REQUEST_TEMPLATE.md`: what was wrong, what changed, how it was verified.
- Issue forms for bugs and features, and a contact link routing security reports to private advisories.
- README links the guide.

Repository settings changed alongside: merged branches are deleted automatically, and private vulnerability reporting is on.

**How it was verified**

Issue forms parse as YAML; links resolve to files in the repo.

### Close the open security advisories (#352)

**What was wrong or missing**

Dependabot reported 48 open alerts on `main`: 14 high, 30 medium, 4 low.

**What changed**

The smallest upgrades that close every alert:

| Package | From | To | Kind |
|---|---|---|---|
| `@fastify/static` | 8 | 10.1.5 | direct |
| `fastify` | 5.x | 5.12.5 | direct |
| `vite` | 5 | 6.4.3 | direct; also retires the vulnerable `esbuild` it pulled in |
| `vitest` (+ `@vitest/mocker`) | 3 | 4.1.11 | direct |
| `fast-uri` | | ^3.1.6 / ^4.1.4 | override |
| `js-yaml` | | ^4.3.2 | override |
| `dompurify` | | ^3.4.16 | override |

Overrides in `pnpm-workspace.yaml` stay within the major each dependent asks for. Vite 8 and Vitest 5 exist but would also move the React plugin and test setup, which nothing here needs.

One breaking change applied: `@fastify/static` 10 passes `setHeaders` the Fastify reply rather than Node's response, so the cache-header hook uses `reply.header`.

**How it was verified**

On a running server: the HTML shell is `no-store, must-revalidate` and hashed assets `public, max-age=31536000, immutable`, as before; search and library routes answer. 190 server and 15 web tests pass; the web app builds under Vite 6.4.3; headless Chrome renders the chat with no page errors; `pnpm install --frozen-lockfile` passes.

## v1.70 (2026-09-28)

### Conversations rail divider back; a dismissed availability notice stays dismissed (#350)

**Rail divider.** ai-chat 0.4 drew a border on the conversations rail and 0.5 does not; with rail and thread both white in light mode, the middle column stopped reading as a column. The rail takes `border-right` again with the Library rail's token. Checked in both themes.

**Notice dismissal.** The model availability notice returned on every listing after being dismissed, including the five-minute refresh. A dismissal is now stored per browser (`localStorage`) against the exact set of marked models (id plus locked/unavailable), and the notice returns only when that set changes. When nothing is marked the stored dismissal is dropped, so a later recurrence shows. Credential notices are unaffected.

## v1.69 (2026-09-28)

### The Libraries page lays out as a table (#349)

The libraries list used `settings-table`, a class with no styles, so columns ran together and the identifier butted against the name. It now uses `rag-calls-table` like the rest of Settings: the identifier on its own muted line under the name, access in words (Read and write / Read only), capability columns named for what the user gets (Full-text search, Semantic search) with No muted, and the add form under the standard `tool-group` subheading. Checked in a browser.

## v1.68 (2026-09-25)

### The open conversation stands out in the rail (#348)

The open row used the shared pale pill (`#e1f2fb` on a white rail) and was hard to find at a glance. It now gets `color-mix(in oklab, var(--pw-navy) 14%, var(--pw-rail-bg))` plus a 3px `inset` accent bar on the leading edge (dark: `--pw-link` at 22% and a `--pw-link` bar), so the mark holds under any deployment accent. Sidenav, Library tree, and Help keep the shared pill.

Also: in dark mode rail rows rendered in the link blue from the global `[data-theme='dark'] a` rule; rail links now use `--pw-text` like the Library tree. Checked in both themes in headless Chrome.

## v1.67 (2026-09-25)

### The conversations rail matches the Library rail under ai-chat 0.5 (#347)

An audit of the Studio's `.chat-canvas` rules against the ai-chat 0.5 DOM found two more 0.4-era selectors that stopped matching (after the rail highlight in #346):

- **Rail background.** The rail is now `aside.bg-(--theme-muted-panel-bg)`, not `.theme-panel.border-r`, so it lost `--pw-rail-bg`: identical in light, `#2e3032` vs the Library rail's `#232528` in dark.
- **Row size.** Rows and actions are spans inside `li`, `button`, and `a`, not bare links, so they rendered at 14px instead of the Library tree's 13px.

Both rules now match either version. Measured in headless Chrome: rail `#232528` in dark and white in light; rows and actions 13px; the Chats header stays 14px.

The audit's remaining unmatched selectors belong to states the check did not render (activity drawer open, a reply streaming, dark-only rules); their classes (`max-h-20`, `border-l-2`, `transition-[width]`) still ship in 0.5.

## v1.66 (2026-09-25)

### The open conversation is highlighted again in the rail (#346)

ai-chat 0.5 marks the open conversation's row with a bare `bg-[color-mix(in_oklab,var(--theme-panel)_8%,transparent)]` class (an 8% tint) where 0.4 used `theme-muted-panel`. The Studio's accent-pill rule targeted the old class and stopped matching. The rule now also matches `li.group[class*=" bg-[color-mix"]`: every other row carries that class only behind `hover:`, so the leading space selects the open row alone. The old selector is kept so either package version works. Verified in headless Chrome: exactly one row, the open one, draws the pill at weight 600.

## v1.65 (2026-09-25)

### Declare cascade layer order; the banner reason survives truncation (#345)

**Composer and conversation search (regression from ai-chat 0.5 / ui 0.16).** Both packages nest their rules in `pw-ui` / `pw-ai-chat`, but the compiled ui stylesheet also emits a top-level `@layer base { input, textarea { border; padding; background } }`. Declared after the package layers, it outranked their utilities (`border-0`, `pl-8`, `bg-transparent`), so the composer textarea drew a second bordered box and the search input lost the padding that clears its icon. `web/src/layers.css`, imported first, declares `@layer base, pw-ui, pw-ai-chat;` so `base` is lowest. The Studio's own inputs all carry `.field` or a scoped rule and are unaffected. Measured in headless Chrome after the change: textarea border 0 and transparent; search padding-left 32px. (The leak contradicts the ui README's statement that all rules are layered; worth an upstream fix.)

**Banner showed raw JSON.** The probe truncated the provider error to 200 characters before extracting the reason, cutting the nested message in half. It now keeps 2,000, and `providerReason` also accepts a message cut off mid-string (tested with the exact truncated body the banner displayed).

## v1.64 (2026-09-24)

### Say what the provider said, bound silent streams, ai-chat 0.5 (#344)

**Banner.** Every `[unavailable]` mark was explained as a key on an 8-hour lock schedule. Measured against a provider whose models were marked: the provider refused the model names outright, streamed (502) and non-streamed (masked 400), with *Requested model is not available and no compliant same-model variant was found*. The mark was correct; the explanation was wrong. Marked models now carry the provider's own sentence (`providerReason`, which unwraps the gateway's nested error bodies) and the banner quotes it. A lock still gets the unlock step.

**Correction.** #340 claimed live verification that removing the probe's token cap cleared the marks. That check hit the wrong gateway (`Provider owner not found`), and the marks had this other cause. Removing the cap stays, since a probe never needed one.

**Silent streams.** A stream that opened and sent nothing had no limit but the client's abort. Each read now has one (`STUDIO_STREAM_IDLE_MS`, default 300 s), reset by every chunk; some providers buffer a whole answer and take about a minute per call.

**ai-chat 0.5.0** with `@parallelworks/ui` ^0.16 (its peer floor). Attachments paging moved to a cursor contract with `hasMore`; the adapter maps the server offset to it (tested). Headless smoke: chat canvas, composer, and picker render, 19 models listed, no page errors.

Also regenerates CHANGELOG.md; the v1.62 and v1.63 entries never reached main. 189 server and 15 web tests pass.

## v1.63 (2026-09-23)

### Feature previews is its own settings section (#343)

The voice preview switch sat at the foot of **External access**, below the retrieval endpoint and the MCP configuration. A deployment reported the preview missing while its own bundle carried the control; it was findability, not a deploy gap.

Feature previews is now its own section in the settings rail, between Assistant tools and Extensions. The rail is extracted as `settingsSections()`, a pure function with a test, so a new section has to be placed deliberately. The voice text now also states that the Unmute deployment must be reachable from the browser in use, since a closed network needs its own rather than one running elsewhere.

## v1.62 (2026-09-15)

### The model listing answers from a cache and refreshes behind it (#342)

Every load of the model list fetched the gateway catalog and pinged each provider family (about a second on dev, several seconds where the gateway and providers are further away and each user's credential probes on its own), and the client asked three times per mount.

**Server:** the computed listing is cached per viewer and credential (the listing is shaped by the viewer, since shared providers are labeled with their owner). A load inside the 60 s fresh window is answered from cache; an older one is answered from cache and refreshed in the background (deduplicated), so the next load is current; only the first load per credential waits. `?refresh=1` drops the probes and recomputes before answering, and the key test in Settings clears every listing. The last-call-failed decoration is applied after the cache so it stays live.

**Client:** one deduplicated request (`fetchModels` in `api.ts`) serves the picker, the banner, the credential notice, and the fleet page, with a 15 s reuse. Freshness without a reload: refetch when the tab becomes visible and every five minutes while visible; the picker is remounted only when the set of marked models changes, never mid-reply.

Measured on dev: first load 1.2 s, next three under 10 ms, forced refresh 0.4 s. Tests cover cached loads, refresh seeing a changed catalog, and the live decoration. 187 server and 12 web tests pass.

## v1.61 (2026-09-15)

### Neutral provider names in the probe fixtures (#341)

The two new probe cases used a real provider prefix and its model ids as fixture names. Fixtures use neutral names like the rest of the suite.

## v1.60 (2026-09-15)

### A change log and release notes generated from pull request descriptions (#339)

Versions were tagged but never published as GitHub Releases, so the change record existed only as PR titles. Every merged PR here carries a description written as a change note, so the notes for a version are those descriptions in order, and the change log is the same for every version at once.

`scripts/release-notes.mjs` produces both from the tags and the gh CLI (`vX.Y` for one version, `--changelog` for all), mapping squash commits to PRs by the `(#N)` suffix and dropping attribution trailers. `CHANGELOG.md` is its output for all sixty versions to date. The README documents the release steps, and every existing tag is being published as a Release with these notes.

Also makes the run-watch test deterministic: it slept a fixed span and asserted a count band, which flaked on the macOS runner; it now waits for the give-up point and then proves no further polls happen.

### The provider probe no longer marks a healthy model family unavailable (#340)

The provider probe pinged with `max_tokens: 1`. One model family rejects every spelling of a token cap (`max_output_tokens` explicitly, `max_tokens` and `max_completion_tokens` behind the gateway's masked 400; measured earlier on `gpt-5.6-sol`), so the ping failed twice on its own parameter and the probe reported a reachable, unlocked provider as down. Every model of the family showed `[unavailable]` and the banner told the user to unlock a key that was never locked.

The ping now carries no cap. Cost is bounded by reading the streamed response only to its first frame, which is proof the provider answers, then aborting. A body that names a request parameter as the problem is classified as a reachable provider regardless of status, since a provider arguing about the request's shape is up. Locked and unavailable verdicts are unchanged.

Verified live against the cap-rejecting family on the gateway: the verdict is now ok. Tests: parameter rejection reads as up with no second ping; a healthy stream is read to one frame; existing locked, unlock-url, masked-twice, and cache cases still pass. 184 server tests pass.

## v1.59 (2026-09-12)

### A manual macOS job that builds GUFI and exercises its vector stack (#337)

Manual (`workflow_dispatch`) with inputs for the GUFI ref and whether to build the AI dependencies, since that path compiles llama.cpp and takes far longer than the test suite.

It builds GUFI on a clean `macos-15` runner through `indexer/setup_gufi.sh`, indexes a small tree and queries it with `gufi_query`, then exercises the vector stack exactly as the Studio does: load the model, embed a phrase with `lembed`, store vectors in a `vec0` table, and take the nearest by distance. It ends by pointing the built server at that index to confirm binary discovery.

The value is independent verification on a machine nobody has configured, which is the state a new Mac user is in, and it gives us a way to check a GUFI branch on macOS without owning a Mac.

### Libraries: mount several indexes at once, read only beside the knowledge base (#338)

The Studio built and owned its index, which suited a knowledge base and nothing else. It now mounts **libraries**: the knowledge base is the first, writable, and every request that does not name a library means it, so nothing that existed before changes. Any other library is read only: a site's root-built GUFI index or one someone handed over, reached through a local tree with an optional source root.

**Registry** (`server/src/libraries.ts`): libraries come from the deployment (`STUDIO_LIBRARIES`, pinned; also an *Additional Libraries* field on the deploy form) or from Settings > Libraries, where an administrator adds one by path and the Studio probes the tree first (a GUFI index, the Studio's full-text tables, vectors, source readable). Nothing is asked of GUFI and nothing is written into the index.

**Addressing**: a `library` parameter on every corpus and index route, defaulting to the primary. `gufi.ts` functions take an index root (per-root caches), `kb.ts` a source root. Read-only libraries refuse upload, move, rename, delete, labels, and re-index with a 403; a library with no files on this host answers 409 on file routes and the viewer says why. Chat tools read from the library the conversation names.

**Client**: the Library rail header becomes the switcher when more than one library is mounted; the current library rides on every API call from one place (`api.ts`) and on deep links as `&lib=<id>`. `STUDIO_SECTIONS` chooses which sections appear, so a site can run an index viewer with no assistant.

**Verified** against a real second GUFI index on the dev server: probe reported capabilities per library; listing, filename search, and file reads followed the named library; the primary was untouched; mutations on the read-only library were refused (403), no-files library answered 409, unknown answered 404.

Tests: 8 new server tests (registry, probe, addressing, guards, config), 3 new web tests (URL suffix, switcher, deep links). 182 server and 12 web tests pass. Docs: `docs/LIBRARIES.md`, README, ARCHITECTURE, `.env.example`, both deploy workflows.

Not yet: enrichment sidecars for read-only libraries, the Query page's scope, cross-library search, remote libraries through GUFI's client.

## v1.58 (2026-09-10)

### A start-to-finish macOS guide, and a corpus default that works off a server (#336)

**The first run failed for an unexplained reason.** `KB_ROOT` defaults to `/data/knowledge-base`, a server convention that does not exist on a laptop and cannot be created without root. When that path is absent the default is now `knowledge-base/` beside the code, seeded on first start, so `pnpm start` works from a fresh clone. Deployments set `KB_ROOT` explicitly and are unaffected.

**`docs/MACOS.md`** is the path in the order worth doing it: run it first, add GUFI once it works, add a model last. It includes a table of what works in each of the three states, because a Mac user who builds with `GUFI_AI=0` still gets filename and full-text search over extracted document text and only loses the semantic blend, which is a much better story than "unsupported". Troubleshooting covers the failures that actually happen: Apple clang instead of Homebrew LLVM, and the llama.cpp build inside sqlite-lembed.

GUFI binaries are not shipped here and should not be: the guide points at the GUFI project for installation, notes that a site package works, and documents that the Studio finds `gufi_query` on `PATH` or under `/opt/gufi/bin`. `indexer/setup_gufi.sh` remains available for building from source.

174 server tests and 9 web tests pass, and the macOS CI job covers this on a real runner.

## v1.57 (2026-09-10)

### Build GUFI on macOS, test the Studio there, and correct the README (#335)

The README said GUFI does not build on macOS. It does: upstream runs a `macos-15` job in its own CI with `-DDEP_AI=On`, and the difficulty is the toolchain rather than the platform, since libomp is not wired into Apple's clang and GUFI's scripts expect GNU utilities.

**`indexer/setup_gufi.sh`** now handles macOS the way that CI does: Homebrew's LLVM as `CC`/`CXX`, libomp passed through the OpenMP variables, and coreutils/findutils/gnu-sed/grep/diffutils placed ahead on `PATH` for the build only. It also accepts `GUFI_AI=0`, which skips `sqlite-vec` and `sqlite-lembed` (the hard part, since lembed carries llama.cpp) for a fast build that keeps metadata, filename, and full-text search and loses only the semantic blend. `DEP_AI` defaults to On upstream, so a plain `cmake` attempts them; this makes the choice explicit. Installing to a prefix the user owns no longer calls sudo.

**CI** gains a macOS job that installs Node, pnpm, tesseract and poppler, then builds and runs the suite. It deliberately does not build GUFI: that takes far longer than the tests and has upstream coverage. What this job catches is a Linux-only assumption in the server or a tool that only exists under apt. The repo is public, so macOS runner time is free.

**README** now describes both build paths instead of claiming the platform is unsupported.

## v1.56 (2026-09-08)

### The suggestion chips size to their text, not the whole canvas (#334)

`.chat-canvas > * { height: 100% }` forces every direct child to full height for the package's own layout, and the next-message suggestion row is one of those children. It filled the canvas, and with the default `align-items: stretch` each chip inflated with it, drawing giant circles over the conversation.

`height: auto` plus `align-items: flex-start` on the row and `height: auto` on the chips. Measured in a headless browser against the live app: chips 26px tall in a 58px wrapped row, canvas 779px. The conversation scrubber and the persona buttons hit this same rule before; the comment names it so the next component does not.

## v1.55 (2026-09-08)

### The element between the app and the views can shrink, so pages scroll on a phone (#333)

Measured in a headless browser at 390x844 rather than guessed at.

`main.content`, between `.app-body` and the views, is a flex item whose default `min-height: auto` refuses to shrink below its content. In the phone's column layout that let a tall page grow the whole layout past the viewport: Settings measured **2102px inside an 844px app**, which pushed the navigation bar off-screen (`nav@2143`) and left nothing able to scroll, because `.help-content` and the other per-view scrollers never received a bounded height to work against. Chat looked fine only because its content already fits.

`main.content { min-height: 0; min-width: 0 }` fixes it. Re-measured across every view, each now reporting an app that fits the viewport, the bar on the bottom edge, and a working inner scroller:

```
chat      OK  app=844/844 nav@844
settings  OK  app=844/844 nav@844  help-content:735/2033*
help      OK  app=844/844 nav@844
library   OK  app=844/844 nav@844  explorer:286/822*
stats     OK  app=844/844 nav@844
agents    OK  app=844/844 nav@844
search    OK  app=844/844 nav@844
query     OK  app=844/844 nav@844
```

## v1.54 (2026-09-08)

### The mobile shell is pinned to the visual viewport, and a deploy is never cached (#332)

Two faults, and the first hid the second.

**Caching.** Every static file was served `public, max-age=0`: a phone could hold a stale shell after a deploy, while two megabytes of fingerprinted JavaScript were revalidated on every load. `@fastify/static` writes that header itself *after* any `setHeaders` hook, so `cacheControl: false` is required for ours to stand. Now `/assets/*` (content-hashed names) is `public, max-age=31536000, immutable` and the HTML shell is `no-store, must-revalidate`, so a deploy is picked up on the next load with nothing to clear. Verified on the live server.

**Mobile shell.** The layout asked a chain of percentage heights to agree with a viewport that changes size as iOS slides its toolbars; it did not, so the bottom bar left the screen and the thread ran under the browser chrome. `.app` is now `position: fixed; inset: 0; height: 100dvh` under the 760px breakpoint, which is the arrangement iOS keeps correct through that transition. Out of flow, the page has nothing to scroll and the bottom bar cannot be carried away; views scroll inside themselves as before. Confirmed present in the built CSS.

## v1.53 (2026-09-08)

### Settings and help work on a phone, and three chat fixes (#331)

Four things found on a phone.

**Settings and Help on mobile.** Their 220px section rail sat beside a scrolling article; on a phone it took most of the width, and with the page itself no longer scrolling (the viewport fix) the article had nowhere to go, so the page looked frozen. The rail becomes a horizontal strip of section chips above the article, and views scroll inside themselves with `overscroll-behavior: contain`.

**Chat controls overlapping the model picker.** They float over the package's header; on a phone the picker grew under them. Labels drop to icons there and the picker gets `max-width: calc(100vw - 190px)` with truncation (written defensively against the package's class names).

**A retry duplicated the answer.** The client accumulates `content` events and cannot unsay them, so a retry after text had already streamed appended a second reply. A turn that has spoken is no longer retried; the error is surfaced.

**Sol's first turn always failed.** That provider rejects every token-cap spelling (`max_output_tokens` explicitly, `max_tokens` and `max_completion_tokens` behind the masked 400; no cap works). The rejection is now remembered on disk, so only the first turn a deployment ever runs pays to learn it, and that retry keeps streaming, since a rejected parameter is not a generation failure and the unstreamed retry left the reader on a still "Thinking" line for the whole reply.

Voice now opens at top level instead of in an iframe: the deployment is its own platform session on its own domain, so the frame demanded a second sign-in that mobile browsers cannot carry a cookie into. Proxying it from the Studio's origin needs a base path in Unmute's frontend build; noted for next.

## v1.52 (2026-09-08)

### The app fills the visible viewport, not the layout one (#330)

On a phone the navigation bar floated above a band of background and the composer sat near the browser chrome. The dynamic-viewport fix was applied to `.app` alone, while `html, body, #root` were still `height: 100%` of the *layout* viewport, which on iOS is taller than the visible one: the app was laid out in a box bigger than the screen and the page itself scrolled.

Under the existing 760px breakpoint, every ancestor now uses `100dvh` with `overflow: hidden`, so there is no page-level scroll and the bottom bar stays on the bottom edge; views scroll inside their own containers as before. The bar also gets `padding-bottom: max(2px, env(safe-area-inset-bottom))` to clear the home indicator, which requires `viewport-fit=cover` on the viewport meta for the inset to report a real value. Browsers without `dvh` ignore the declarations and keep today's behavior. CSS and one meta tag.

## v1.51 (2026-09-08)

### The model URL is the base, without /v1, and the brain is any API model (#329)

Running the stack on a two-GPU node showed the Unmute backend appending its own `/v1`: a URL given with the suffix became `…/v1/v1/models` and the model never connected (`llm_up: false` with STT and TTS healthy). The workflow input is now **Model base URL** and says the suffix is added.

The description also now matches what the architecture allows: only the speech services need a GPU (about 5.3 GB synthesis, 2.5 GB recognition, splittable across cards), and the conversational model is a configuration value. Point it at a Studio for the corpus, tools and workflows with an API model behind the voice (`studio-voice/<gateway-model-id>`), or at the platform gateway for a plain conversation with that model. Settings text updated to match. Workflow and copy only.

## v1.50 (2026-09-08)

### The Unmute workflow starts on a real host (#328)

Brought up on a two-GPU node (one busy card, one free 6 GB card). Three things the upstream compose assumed were not true there:

- The speech services' start script runs `uvx hf auth login --token $HUGGING_FACE_HUB_TOKEN` unconditionally and dies on an empty token, although the Kyutai STT/TTS weights and stock voices are public (`gated: false`). A Studio start script mounted over the image's makes the login optional.
- GPU reservations by CDI device name failed on a stale CDI spec (`/dev/dri/card2` missing). Services are pinned by NVIDIA-runtime device index instead, with separate `stt_gpu` / `tts_gpu` inputs so the two can be split across cards (STT ~2.5 GB, TTS ~5.3 GB).
- The backend reaches a Studio running on the host through `host.docker.internal` (host-gateway).

The compiled `moshi-server` lives in a named volume so only the first start builds it (about 90 s here with a warm crate cache). Verified: STT healthy on the 6 GB card at 2,516 MiB. Workflow-only.

## v1.49 (2026-09-08)

### Carry the features flags into the client config (#327)

The client assembles its config object field by field, so the `features` block added to `/api/config` in #326 was dropped on the way in and the Voice button could never appear. One line.

## v1.48 (2026-09-08)

### Voice conversations as a feature preview, through Unmute (#326)

Settings gains a **Feature previews** section; its first entry is voice conversations. A deployment switches it on and names the URL of an Unmute deployment (`voiceEnabled`, `voiceUrl`, or `VOICE_ENABLED=1` / `VOICE_URL`), `/api/config` reports `features.voice`, and a **Voice** button appears above the chat that opens Unmute's interface in an overlay (`allow="microphone; autoplay"`, with an open-in-new-tab fallback).

Unmute (Kyutai, MIT) wraps any OpenAI-compatible text model with streaming STT, semantic turn-taking, and TTS. The model it wraps here is the Studio itself: the RAG endpoint now serves `studio-voice[/<gateway-model>]`, the full assistant (tools, grounding, personas) with a spoken-answer style appended to the system prompt: one or two plain sentences, no markdown, lists, or paths, a few words before a slow tool call. It is advertised alongside `studio-agent` in `/v1/models`.

`deploy/workflow-unmute.yaml` stands the stack up on a GPU node from the upstream compose: clones the repo, overlays a compose file that points the backend at the Studio (`KYUTAI_LLM_URL/MODEL/API_KEY`), disables the bundled `llm` service, publishes traefik on the endpoint agent's port, and serves it as a platform session. Unmute needs a 16 GB GPU for STT plus TTS; the Studio's model runs wherever it already does.

Known limit of this slice: the transcript stays with Unmute; recording voice turns as Studio conversations comes when the turns flow through the Studio's own client. Help guide updated; two tests; 153 server and 9 web pass.

## v1.47 (2026-09-07)

### What to type next, as the composer's placeholder and chips, Tab to fill (#325)

When a turn ends, `GET /api/chat/suggestions?conversation=<id>` reads the tool calls stored with the last assistant turn (`suggestNext`) and offers up to three specific follow-ups: follow the run that was launched or show its output, launch a dry run for real, summarize a completed run, explain a failed one, open the file a search cited, run the workflow that was inspected, check on delegated agents, retry a failed tool step; a long plain answer gets "sources" and "save as a note". No extra model call. A reply ending in a question or `NEEDS DECISION:` offers nothing, and so does a conversation where the user spoke last.

`NextUp` attaches to the package's composer textarea from outside (as the slash palette does): the first suggestion becomes the placeholder with "(Tab)" while the box is empty, Tab fills it through the native setter and an input event, the rest are chips above the composer, and typing hides everything. The palette's capture-phase Tab wins when it is open. Four tests on the rules; 151 server and 9 web pass.

## v1.46 (2026-09-07)

### The fleet: standing agents with goals that live in ticks (#324)

The Agents tab gains a **Fleet** page, first among its three, as the operator base for long-running work.

A standing agent (`server/src/fleet.ts`) has a goal, a persona, a model, a placement (local beside the studio, or campaign as a workflow run on a system), triggers, and a budget. It lives in **ticks**: a tick is one bounded delegated task (the existing engine, one agent, depth 0) whose objective is the goal, the event that woke the agent, and the tail of its own journal, so its memory is the journal on disk under the index base rather than a process a rollout would kill. Triggers: a cadence (15m to 1d, or manual), a workflow run ending (fed from the run registry), new or changed files under a watched knowledge-base folder, and a message from a person, answered on a tick that starts at once. A result ending in `NEEDS DECISION:` parks the agent as input-required under a **Needs you** strip. Budgets in ticks and tokens pause an agent that has used them. Pause, resume, retire, tick-now, and message are routes under `/api/fleet`. Every agent reloads at startup; an interrupted tick leaves the agent idle for its next trigger. Token usage per tick comes from the task's agents.

Four starter goals ship as templates (`/api/fleet/templates`) for a demo on a small Slurm system: a nightly benchmark campaign (campaign_runner), a queue and run watcher (watcher), a results reviewer on a watched folder (reviewer), and a daily digest (reporter). The help guide describes the page. Five tests against a fake pw code cover the schedule tick with journal and tokens, the run-ended trigger and inbox, pause, tick budget, decision parking and messages, and reload after a restart. 148 server and 9 web tests pass.

## v1.45 (2026-09-07)

### Default personas, usable unselected, and token usage per agent and per turn (#323)

Five personas ship with the studio for long-running fleet work: `campaign_runner`, `watcher`, `reviewer`, `steward`, `reporter`, each a short set of working rules under `extensions-starter/agents/`. `seedExtensions` now fills in starter files on deployments that seeded before they existed, through a ledger (`.seeded.json`): a starter file is copied only when absent and never seeded before, so an edited default is never overwritten and a deleted one never returns. The library gains **Duplicate**, which opens a copy in the editor as a new file.

The system prompt carries a live persona catalog (rebuilt on every prompt) and a rule for using one without a selection: when a request clearly falls inside one persona's description the assistant reads it and follows its rules for that request, says which one it applied, and never switches mid-request; when delegating, each agent gets the persona that fits its objective.

Token usage is counted where the data exists. `streamTurn` reads a `usage` chunk when the stream carries one (including a usage-only final chunk), the tool loop sums them (`addUsage`), and the total is stored on the message as `tokensUsed`. An agent's pw code envelope is read by `usageOf` for every spelling providers use; the count lives on the agent, sums on the task (`sumUsage`), and shows on the Tasks page row, drill-down, and card. Nothing is shown rather than zeros where the gateway reports nothing.

Five new tests; 143 server and 9 web pass.

## v1.44 (2026-09-07)

### The semantic map caption says what proximity means (#322)

The caption now states that the map places documents by similarity of the embeddings semantic search uses, projected to two dimensions, that the axes carry no meaning of their own, and that color is the cluster. Text only.

## v1.43 (2026-09-05)

### Neutral fixture names and comments (#321)

Test fixtures, tool descriptions, the system prompt's example request, code comments, and the help guide use generic system and host names (vega, juno, example hosts), and the CFD training workflow's account field ships empty instead of a preset. No behavior change; 138 server and 9 web tests pass.

## v1.42 (2026-09-05)

### Runs launched from chat survive a rollout, and the page knows one happened (#320)

A run lives on the platform and outlasts any Studio process; what did not survive a deploy was the knowledge that a conversation was waiting on it, and the process went down mid-answer with no drain.

- **Run registry** (`server/src/runs.ts`): every real `run_workflow` launch is appended to `runs.jsonl` under the index base (preserved across redeploys) with the conversation and user that launched it. The server follows each run to its end (poll every 60 s, unref'd), re-attaches to every unfinished run at startup, gives up after 30 failed checks, and on a terminal state appends a note to the launching conversation via `appendConversationNote`, parented to the last message so it sits on the active branch. `GET /api/runs` serves the registry; the Tasks page lists runs with state and a link to the chat.
- **Drain**: `SIGTERM`/`SIGINT` now wait up to 20 s for in-flight streams (counted in the stream route) before `app.close()`, so a tool loop finishes and a launch gets registered. The deploy replaces the tunnel regardless, so this protects server-side work, not client connections.
- **Rollout notice**: the footer polls `/api/version` every 60 s and on tab focus; when the commit changes under an open page it shows "Updated to vX" with a Reload button. Conversations are saved server-side and survive the reload.

Tool context now carries `conversationId` and `userId` into tools. Three tests on the registry (follow to the end with the conversation note; re-attach from the file after a restart; give up on a run the platform no longer reports); 138 server and 9 web tests pass.

## v1.41 (2026-09-05)

### Run detail shows the job's output, not the workflow's script (#319)

`workflow_run_detail` handed the model the raw run record first. That record embeds the workflow's own script text and filled the 24,000-character output cap before a single line of job output; the errors and the log tail were truncated away. The assistant that had just launched a job could not see its result and went looking for it with shell commands on the cluster.

`summarizeRunDetail` (exported, tested) now produces: run state with start and end times; each job's state, host, and per-step states; the platform's error report; and the tail of every step's output, failed steps first and longest, with the "(error fetching logs …)" placeholders dropped. Measured on a scheduler run: 24,000 characters without the marker or Slurm job id became ~1,500 characters with both. Three tests; 135 pass.

## v1.40 (2026-09-05)

### A scheduler submission with no partition gets one from the platform; help guide updated (#318)

The first end-to-end submission on a scheduler-only site failed at sbatch with `No partition specified or system default partition`, which is what a Slurm system without a default partition says to any job that names none, and which a user asking in plain language should never see. When a workflow declares a `slurm-partitions` input and the request left it empty, `run_workflow` fills it with the system's `up` partition that has the most free nodes (from `pw environments ls`), after the saved-configuration merge so a configured partition always wins, and the tool result says which one was chosen and why. Account and QoS stay the caller's, since they are per-user site values. Three tests cover the choice (busiest-free up partition, never a down one), a caller-named partition left alone, and a direct-ssh submission left without one.

Also: `server/scripts/e2e-hpc.mjs` accepts `E2E_PARTITION` and `E2E_SLURM` (a JSON object merged into the slurm group) for sites that need a partition, account, QoS, or GRES; and `docs/HELP.md` now covers the slash palette, HPC launching with the workspace auto-start, locked models, find in file, the search grammar, and the Agents tab's two pages. 132 server tests and 9 web tests pass.

## v1.39 (2026-09-05)

### A launch starts the user workspace when it is scaled down, then retries (#317)

The platform runs a user's workflows in a per-user workspace that scales down when idle. A launch made while it is down fails with `User workspace not found or is not running` (and `Could not execute workflow` for a while after a start request), and the platform did not start it on demand for a CLI launch: every submission for the account failed that way for about half an hour tonight until `pw workspace start` was run by hand.

`withWorkspace` (new `server/src/workspace.ts`) wraps the assistant's `run_workflow` and the campaign runner's agent submissions: on that failure it requests a start and retries the launch every 15 s until accepted or a four-minute budget is spent, then fails with a message that names the remedy. The launch itself is the readiness check (the status command's wording is not relied on); any other failure is rethrown immediately. The tool result and the campaign board both say when a start was needed.

Six tests: the retry contract (start once, retry until accepted; healthy launch untouched; foreign errors rethrown before and after a start; budget exhaustion message; both wordings), plus `run_workflow` end to end against a fake CLI whose `workflows run` fails until `workspace start` has been called, asserting the exact call sequence.

## v1.38 (2026-09-05)

### A web test suite, and server coverage for the probe, model marks, slash forms, and defaults (#316)

The web had no tests. It now runs vitest with jsdom and Testing Library under the root `pnpm test`, so CI exercises it:

- the composer's slash palette end to end: opens on `/`, filters (`/sea` ranks `search_kb` first), Enter inserts `/search_kb ` through the native setter and an input event, and the Enter keystroke never bubbles to the composer's own handler; Escape closes; ordinary text never opens it
- the live theme hook, and an embed's `theme=` param following a toggle
- the scrubber's message-block selection, the library hash with its `&q=` query (moved into `nav.ts`, one implementation instead of three), and the find-in-file term parser

Server side (node:test, stubbed `fetch`): the provider probe for every verdict it can reach (locked wording, `unlock_url`, the masked failure that only counts when it happens twice, healthy, cached, invalidated); `aiHealth` for its explained 401 and unlock link; the listing's model marking and inbound stripping, now the pure `markImpaired` and `stripAvailabilityMark`; three more slash-command forms; the delegation default and its `DELEGATION_ENABLED=0` kill switch; label normalization.

123 server tests and 9 web tests pass. Lockfile updated with pnpm at the root.

## v1.37 (2026-09-05)

### A slash-command palette on the composer, and a repeatable HPC end-to-end check (#315)

Typing `/` opens a palette above the composer listing everything slash-invocable (meta commands, skills, agents, tools from `/api/extensions` and `/api/chat/tools`), filtered as you type; arrow keys move, Enter or Tab inserts `/name `, Escape closes, mouse works too. The composer is the chat package's, so the palette attaches to its textarea from outside: a capture-phase keydown listener on the textarea sees the keystroke before the package's delegated handler and stops propagation, which is what keeps Enter from sending a half-typed command; acceptance writes through the native value setter and dispatches an input event so the controlled textarea updates. A MutationObserver re-attaches when the composer remounts on a conversation switch.

`server/scripts/e2e-hpc.mjs` runs the HPC chain the assistant uses against a live platform through the same `executeTool` entry point the chat calls: `hpc_environments`, a real `run_workflow` with `scheduler: true`, `watch_run` to a terminal state, `workflow_run_detail`, and passes only when the run completes and a per-run marker comes back from the cluster. First pass today: `activatebatch-00009` on a test cluster, Slurm JobId 27, 33 s end to end.

## v1.36 (2026-09-04)

### A real query grammar, whole-word filenames, and no semantic noise for identifiers (#314)

Quotes were stripped and every word AND-joined, so an exact phrase lost its adjacency and a literal `OR` became a search for the word "or". `parseSearchQuery` now understands the habits people arrive with: `"quoted words"` as an exact fts5 phrase, `OR` between alternatives, `-word` or `NOT word` to exclude (`(a NOT b)` form), `word*` for a prefix (`"word"*`), and whole words otherwise, which the index's unicode61 tokenizer already enforced for full text. Stopwords drop from bare terms only, never from inside a phrase.

The partial-word matches people saw were never full text. They came from the filename substring search (`tin` inside `routine.md`) and from semantic vector hits. A filename term of three characters or fewer now has to start a word (approximated with separator-prefixed LIKE clauses), and semantic hits are omitted when the query is an identifier, an acronym, a quoted phrase, or carries operators, where a "similar" document reads as a wrong answer.

Probed live against the dev index (all eight forms return without an fts5 syntax error); five new tests; 111 pass. The search box placeholder now states the grammar.

## v1.35 (2026-09-04)

### The find bar floats at the top of the scrolling pane (#313)

It sat under the header and scrolled away with it, out of reach exactly when the reader was deep in the file. Now `position: sticky; top: 0` inside `.library-main` (the pane that scrolls), with a light shadow while the document moves beneath it.

## v1.34 (2026-09-04)

### The label overlay survives a rebuilt host, and recovers from the index (#312)

On a filesystem that refuses xattrs (NFS), labels live in `tags-overlay.json` keyed by `<device id>.<inode>`. A rebuilt host mounts the same share under a new device id, so on its first index pass no entry matched, and `overlayPrune`, which treated "not seen" as "deleted", emptied the store. That is how a production deployment lost its labels on 2026-09-03 (host up 20:16, first deploy 20:35, overlay rewritten to an empty store at 20:35).

- Identity tolerates the device id: an entry whose recorded path still resolves to the same inode and creation time is the same file and is re-keyed to the live device id, both in `reapplyTagOverlay` and in the prune.
- A prune that recognizes nothing at all is refused, since a corpus does not vanish between two index passes; the file is copied to `.bak` before any entry is removed.
- The index still carries the label rows written from the overlay, so an empty overlay beside an index with labels is treated as a lost overlay: `recoverOverlayFromIndex()` adopts them at startup (20 s after boot) and on demand via `POST /api/kb/tags/recover`.

Three tests cover the host change (kept and re-keyed), a real deletion (pruned, backup written), and adoption. 106 pass.

## v1.33 (2026-09-04)

### A search result opens on its match, and every file has find (#311)

Clicking a search result opened the file at the top and left the reader to find the match again. The result now opens with the query it matched: the viewer lands on the first occurrence, highlights every occurrence, and shows a find bar with a count, Previous/Next (Enter and Shift+Enter), and Escape to close. The same bar is available on any file from a new Find button in the viewer head.

- Text and Markdown are searched in place, in whichever tab is showing.
- A PDF or office file previews as page images, which cannot be searched, so when opened from a search it lands on the **Indexed text** tab, which holds the very text the index matched; on the Preview tab the bar offers a one-click switch.
- Matches are painted with the CSS Custom Highlight API (`CSS.highlights` + `::highlight()`) rather than by wrapping text nodes in `<mark>`, so React's DOM (Streamdown output, the text bodies) is never rewritten underneath it and re-renders cannot trip over injected marks. Browsers without the API get the landing tab and the count-free bar, no highlight.
- The query travels in the URL hash as `#open=file:<path>&q=<query>`, so a link to a match is shareable and chat citations can carry one later; the `#open=` click handler and the hash parser both accept it.

Search-side quoting and fts5 operators (AND/OR/NOT/NEAR) are stripped from the find terms.

## v1.32 (2026-09-04)

### The empty-chat mark and embeds follow a theme toggle (#310)

Both read `document.documentElement.dataset.theme` once at render, so after a toggle the empty chat kept the other theme's icon until something else re-rendered it, and an embedded DAG or HTML page kept its old theme. `useEffectiveTheme()` subscribes to the attribute App writes (a MutationObserver on `data-theme`), and both consumers read from it: a toggle swaps the mark immediately and changes an embed's `theme=` param, which reloads it in the new theme the same way a fresh open would.

## v1.31 (2026-09-04)

### A rejected or expired credential is an explained state, not an internal error (#309)

When a stored platform token aged out, three surfaces failed the user at once: the picker said "Failed to load models, internal error (ref …)" because the listing route let the 401 escape as a 500; Settings said "Your key ending NA== is not working (auth)", where the suffix was base64 padding; and the health line said "401: Unauthorized". None of it said the one thing that mattered: platform tokens last 24 hours from login, and an API key does not expire.

- The listing answers a rejected credential as a structured 200 (`credential: 'rejected'`) with a message that names the expiry time when the token's payload carries one, or the likely cause when it does not, plus the remedy; the deployment-credential case points at the operator and the personal fallback. The thread banner shows it.
- Settings' headline reads "expired at <time>" or "<platform> rejected your token ending …", with the remedy sentence below; the suffix skips base64 padding.
- The health probe's auth message explains what a 401 means here instead of quoting the status code.

Three tests on the message helper; 103 pass.

## v1.30 (2026-09-04)

### The RAG proxy registers only on an explicit platform; the key input says what it is (#308)

The development Studio's RAG proxy showed up on the production platform's endpoint list. `pw endpoints run` picks its target from `--context`, then `PW_CONTEXT`, then the host CLI's current context; the parent endpoint was launched with `--context`, the spawned child inherited nothing, and the host's current context pointed at the other platform. The proxy now refuses to register unless the target is explicit: `PW_CONTEXT` (passed through as `--context`) or `PW_PLATFORM_HOST` (what the deploy workflow exports), and records why in its status and the log. Three tests cover the guard.

The deploy form's "Gateway API Key" input is a platform API key, not an AI gateway key, and the label invited exactly that confusion; it is now "Platform API key" with a tooltip on where it comes from and what authenticates with it. Workflow-only, live on merge.

## v1.29 (2026-09-04)

### pnpm lockfile for ai-chat 0.4.5, stray npm lockfile removed (#307)

The 0.4.5 upgrade (#306) was installed with npm inside `web/`, which left the workspace `pnpm-lock.yaml` at 0.4.0 and added a `web/package-lock.json` the repo does not use. CI installs with `pnpm install --frozen-lockfile` and failed on the specifier mismatch, and the bundle step's non-frozen install then dirtied the tree. This resolves 0.4.5 in the lockfile and removes the npm artifact; `pnpm install --frozen-lockfile` passes locally.

## v1.28 (2026-09-04)

### ai-chat 0.4.5: the thread keeps a working indicator through tool calls (#306)

The thinking bubble vanished the moment the first tool call arrived and nothing moved again until text did, so a busy turn read as a dead one. Filed upstream as core#19524 and fixed in ai-chat 0.4.5 (core PR 19551): the working indicator now depends only on the stream being open with no text or reasoning showing, not on the absence of tool parts. Verified in the 0.4.5 dist (`showWorkingIndicator = isStreaming && !streamingReasoning && !streamingMessage && !awaitingApproval`).

Two other changes in 0.4.3-0.4.5 meet studio code:

- The picker now renders a host-supplied `name` (core#19534, first half) and falls back to the id only when there is none. The `[locked]` mark therefore rides on the name as well as the id, or it would vanish for every model that has one (all GenAI models do).
- The package now remembers the selected model under its own key (`aiChatSelectedModel`) and validates it against the catalog, which is what the studio's `ModelSelectionGuard` did. The guard is retired; a one-time migration copies a choice remembered under the studio's old key and removes it.
- `variant` left `ChatUIConfig` (one variant now); dropped from our config.

Checked against 0.4.5's dist before upgrading: the scrubber's DOM anchors (`.max-w-4xl`, `.overflow-y-auto`, `group/queued`) and the empty-state greeting `h1` the icon attaches to are unchanged; the only type diff is explicit `| undefined` on optional fields. 97 server tests pass; web typechecks and builds.

## v1.27 (2026-09-01)

### Locked models stay listed, marked [locked] in the dropdown itself (#305)

Hiding (v1.25) read as "no models available" when a whole provider was down, which was worse than the problem it solved.

The mark now rides inside the model id, because the picker renders labels from the id alone and consumes no availability field (core#19534), making the id the one text lane that provably shows. A locked model lists as `<id> [locked]`, the dropdown displays it, and the `[locked]`/`[unavailable]` suffix is stripped from inbound chat requests so selecting a marked model addresses the real one; the call-time classifier then explains the lock. The selection guard refuses to persist a marked id so recovery does not restore a dead selection, and the banner says marked rather than hidden. When core#19534 lands, the id mark can retire in favor of real disabled rendering.

97 tests pass.

## v1.26 (2026-09-01)

### A watchdog restart extracts node when the shell has none (#304)

The deploy step found node in its own environment (module-provided on module-based hosts) and decided the bundled runtime was unneeded, but the watchdog re-runs `run_app.sh` in a bare shell where that node is absent, so every restart died with `nohup: failed to run command 'node'` and the deployment stayed down until the next full deploy. Diagnosed from the host's own logs via a log-tail workflow. The run script now extracts the bundle's node runtime on the spot when node is missing at run time. Workflow-only change: live on the next deploy after merge, no bundle rebuild needed.

## v1.25 (2026-09-01)

### A locked provider's models leave the list instead of being marked (#303)

Marking could not work: the picker renders labels from the id alone and consumes no availability field (core#19534), so a marked model looked normal, stayed selectable, and every call failed. And the banner warned globally, so a user on a healthy model still read an error.

Absence is the one signal the picker cannot ignore. A locked or unresponsive provider's models are filtered out of the listing; the response carries a `hidden` set with `locked` and `unlock_url`, and the banner explains in those terms: hidden because the key is locked, back automatically after unlock. Both re-check paths (Settings' Re-check via `/api/me/model-key/test` and the footer panel via `/api/ai/health?fresh=1`) clear the probe cache so an unlock is seen on the next listing instead of after the five-minute cache expires. A user whose selected model was hidden is moved to a working model by the existing selection guard.

97 tests pass.

## v1.24 (2026-09-01)

### The empty-chat icon at 96px (#302)

56px read as an avatar; at 96px it reads as the deployment's mark anchoring the page. Same pseudo-element, radius scaled with it.

## v1.23 (2026-09-01)

### The icon sits above the greeting, not the canvas top (#301)

The deployment mark was pinned to the top of the empty canvas while the greeting centered itself far below, so the two read as unrelated. It now rides as a `::before` pseudo-element on the package's own greeting `h1`, immediately above "what's on your mind" wherever the package centers it, moving with it across window sizes. The icon URL travels as a CSS variable on the wrapper, so nothing is injected into the package's DOM tree, and no icon configured means no pseudo-element at all.

## v1.22 (2026-09-01)

### The deployment's icon above the empty chat (#300)

A fresh chat page said nothing about whose studio it is until the first question. The deployment's configured icon now sits above the greeting and preset questions, dark-aware with the same pick the sidebar makes, and absent when no icon is configured. The wrapper preserves the chat package's own centering below it.

## v1.21 (2026-09-01)

### The probe streams, because that is the path the gateway does not mask (#299)

Measured against the live locked key: the gateway masks provider errors on the non-streaming path and passes them through with `stream: true`, for the locked-key 401 exactly as previously measured for parameter rejections.

```
stream:false -> An error occurred while generating the response
stream:true  -> API key locked - visit the unlock URL to re-enable your key
```

The probe now streams, so a locked GenAI key yields `kind: locked` (verified live) instead of the inferred `unavailable`. The call-time classifier learned the wording too: "API key locked" without a URL names the 8-hour lock and points at Settings, Model access, instead of suggesting another model. The gateway still drops the provider's `unlock_url` field even on the streaming path; commented on core#19405, where the fix may be as small as giving the non-streaming path the pass-through streaming already has.

97 tests pass.

## v1.20 (2026-09-01)

### A locked provider warns above the thread, where it can be seen (#298)

The model picker belongs to the chat package and renders every label from the model id alone: `ChatModel.name` is consulted only by the search filter and no availability field is consumed, so the locked markings shipped in v1.16 and v1.19 were computed, sent, and invisible. Filed upstream as parallelworks/core#19534 with the exact source location and our real payload.

Until the picker can show per-model state, the warning lives on a surface the studio controls: opening chat checks the models payload, and any model marked locked or unavailable raises the credential banner above the thread with the count, the likely cause, and the Open Settings button that leads to the unlock link and Re-check.

## v1.19 (2026-09-01)

### The probe catches locks the gateway masks (#297)

The provider probe required recognizable credential evidence (unlock_url, 401/403, locked wording) before marking a provider, and the gateway masks a locked GenAI key's 401 into a generic 400 generation error (parallelworks/core#19405), so the exact condition the probe exists for sailed through as healthy. Reproduced and then verified fixed against the live locked key: the hardened probe returns `ok: false, kind: unavailable`.

A one-token ping that fails twice in a row now marks the provider as not responding whatever the wording. The picker label distinguishes the cases: a proven lock reads "(locked; unlock via Settings, Model access)", the masked case reads "(not responding; for GenAI this usually means the key is locked on its 8-hour schedule)". The masked case cannot carry the unlock URL because the gateway discards it; commented on core#19405 with this as further evidence.

96 tests pass.

## v1.18 (2026-09-01)

### The task board card gets the same inset as every other section (#296)

`.card` carries no padding of its own and each section class adds it; the board card never did, so its content sat flush against the border. It now uses the same `16px 20px` as `.ov-list`, and the empty state's last paragraph drops its bottom margin so the card does not end deeper than it starts.

## v1.17 (2026-09-01)

### The task board's type matches every other card (#295)

The empty state had its own heading size and paragraph styling. It now uses the same h3 scale as the other card headings (13px / 650 / navy in light, text ink in dark via the shared dark-toning block) and the shared `view-sub` class for paragraphs, keeping only the ~62ch reading measure.

## v1.16 (2026-09-01)

### Locked providers are found at listing time, not one failed reply at a time (#294)

GenAI models appeared in the picker and were selectable while the key behind them was locked: the gateway lists a registered provider's models from its registry without asking the provider. The picker now asks.

At listing time, providers matching `STUDIO_PROBE_PROVIDERS` (default `genai`) get one probe each: a one-token completion on one model of that provider, which is the only request shape the gateway forwards for a registered provider. A locked provider's models list with `callable: false`, a `locked` marker, the `unlock_url`, and "(locked; unlock via Settings, Model access)" appended to the display name the selector actually renders. Probes cache five minutes per provider and key, the listing never waits more than ~6.5s total, and only a recognizable credential failure (unlock_url, 401/403, locked/unauthorized wording) marks a provider, so a transient generation error cannot grey one out.

A personal provider key that is locked fails earlier, at the provider's own `/models` during listing: that now returns a structured error naming the lock with the unlock link instead of a bare listing failure.

Together with v1.14 (health check shows the unlock link in Settings and the footer) and the chat-error path, a locked key now names its remedy at every surface. 96 tests pass.

## v1.15 (2026-09-01)

### Readable empty state, and the editor stops scrolling past Save (#293)

The task board's empty state was one dense paragraph at full card width with an unstyled heading; it now has a ~62ch reading measure, a type scale matching the other cards, and two short paragraphs (how to start, then what happens and where results land).

The markdown editor grew with content past the bottom of the page, putting the Save button below the fold on any real persona or skill. It now has a 220px floor and a viewport-derived ceiling (`clamp(220px, calc(100vh - 520px), 60vh)`) so the field is never a slit and Save stays on screen, with the resize handle still available.

## v1.14 (2026-09-01)

### A real task board, and the locked-key unlock link at health check (#292)

The Agents tab was one long page where a live fleet and a markdown library fought for the same screen. It is now two pages under one tab: **Tasks**, the delegated-work board, and **Personas and skills**, the library. The Tasks tab shows a live dot while anything is working, chat deep links land on Tasks, and the last-used page is remembered per browser.

The board treats a task as a thing worth watching: cards with a pulsing state dot, a campaign badge naming the system, a progress bar of agents done, and the objective as the title. Opening one shows the agent tree (connector lines, per-agent live-output drill-down) beside the board feed with topics chipped by kind.

Separately, the health check now surfaces the GenAI unlock link. `aiHealth` already probes the provider's own `/models` endpoint when the credential carries a base URL, so the locked-key 401 body was in hand and being truncated into a generic message. The `unlock_url` is now extracted before truncation and carried as its own field; Settings and the footer model panel render it as a clickable link with the plain explanation that the provider locks keys every 8 hours.

95 tests pass.

## v1.13 (2026-09-01)

### Delegation on by default (#291)

Shipping delegation off meant the Agents tab advertised a capability most deployments never saw, and the switch was never the control: the server-side agent ceiling, the depth limit, and read-only workers are enforced regardless of it. `DELEGATION_ENABLED=0` or the Settings toggle turns it back off per deployment. 95 tests pass.

## v1.12 (2026-09-01)

### Campaign mode: agents as platform workflow runs (#290)

The delegation engine gains a second execution target. `local` stays child processes beside the server for quick parallel work. `campaign` submits each agent as a platform workflow run on a named system: `pw code` executes on that system's login node, hours-long under a real timeout, and the worker never depends on the studio process. A server restart reattaches to running campaign agents and resumes watching, rather than declaring them dead.

The runner is one small workflow, `studio-agent-runner`, registered automatically on first use and validated against the platform schema in CI. It writes the agent's prompt and board registration, runs `pw code`, and brackets the final output in markers; the poller tails the run into the same `live.log` the local path streams to, so the Agents tab drill-down is identical for both worker kinds, showing the run identifier, the executing step, and the log as it grows. Stopping a campaign cancels the platform runs, not just the watching.

`delegate` takes `execution` and `resource`, the prompt teaches when campaign is right and to consult placement, and the deploy workflow gains an optional public self URL so remote workers reach the task board over MCP; without it they run boardless and the run log still carries visibility.

Also: the Agents tab's delegated-tasks section now explains itself before any task exists (it was invisible until the first run, with delegation shipped disabled), and dark mode stops painting headings, paths, and DAG labels in the accent.

Five campaign tests drive a fake platform through registration, submission, polling, completion into the corpus, stop-cancels-the-run, and restart-reattach. One of them pins the runner YAML to the real schema fixture. 95 tests pass.

## v1.11 (2026-09-01)

### Delegated agents are visible, navigable, and survive restarts (#289)

First slice of the FY27 direction: pw code agents as first-class helpers the operator can watch and reach.

**Restart durability.** Every state change writes a snapshot beside the board journal, and a fresh process loads history back: finished tasks return with agents, results, and board intact; an agent recorded as working when the process died reads as "interrupted by a server restart" instead of spinning forever. Rehydrated tasks hold no bearer token.

**Live drill-down per agent.** Worker stdout and stderr stream into a per-agent log as they happen, served as a tail (`GET /api/tasks/:id/agents/:name/output`); clicking an agent row opens its objective, current note, and live output, polling while it works. The prompt now instructs each agent to heartbeat via `board_status` before each major step, so the note column shows real progress.

**Navigation.** `delegate` hands the chat a `[View agents working](#view=agents:<id>)` link, chat view-links route in-tab, and the Agents tab opens the linked task directly.

Four durability tests run a real fake-CLI worker through finish, interruption, token hygiene, and the output tail. 90 tests pass.

## v1.10 (2026-08-31)

### A Workflow tab on workflow YAML files in the library (#288)

Opening a workflow.yaml in the library showed the tree and source views, which is the file, not the workflow. A YAML file with a top-level `jobs` map now opens on a Workflow tab hosting the same DAG viewer the chat embeds: the graph, the inputs form, the YAML view, and the schema-error banner from #287 when the file would not run as written. Tree and Source remain as tabs.

The `^jobs:` heuristic only decides whether the tab appears; `/api/kb/dag`'s parse stays the authority, so a YAML that merely mentions jobs elsewhere shows the server's 422 in the tab rather than a wrong graph.

## v1.9 (2026-08-31)

### Validate drafted workflow YAML against the platform's own schema (#287)

A composed workflow rendered a plausible DAG and would not run. Checked against the schema the platform publishes at `/workflow.schema.json`, the composer had two real violations: bare `uses:` references (the schema accepts only the `parallelworks/*` builtins and the `marketplace/`, `workflow/`, `github/` prefixes) and job keys allowing uppercase against a lowercase-only pattern. Both fixed; a bare name becomes `workflow/<name>`, and the composer's output now validates clean against the live schema.

The schema is consulted rather than approximated: fetched from the deployment's own platform host (so the customer platform validates against its own), cached beside the index for offline restarts. It declares draft 2020-12, so this uses Ajv's 2020 build, with `unicodeRegExp` off because the schema's interpolation pattern `^\${{.*}}$` is not a legal unicode-mode regex.

Enforcement points: `compose_workflow` validates its own output and reports errors in the result; a new `validate_workflow` tool checks inline YAML or a corpus file; the prompt requires fixing every error before writing, embedding, or offering to run; and `/api/kb/dag` returns the verdict so the DAG viewer banners a drafted file that will not run. No schema available reports as unvalidated, never as a pass.

A `workflow_authoring` starter skill teaches the reference forms and the loop. Five new tests pin the composer's output to the real schema, carried as a fixture so CI never needs the network. 86 tests pass.

## v1.8 (2026-08-31)

### Dark embeds, honest /v1 switch, unlock links, and DAGs for drafted workflows (#286)

Four fixes from one afternoon of production use.

**Embeds inherit the parent theme.** The DAG iframe is its own document and re-derived the theme for itself, which was not guaranteed to match the page hosting it, so a dark chat held a light graph. The parent passes its theme in the embed URL and the embed obeys it over every local derivation.

**The /v1 switch means off everywhere.** The routes already refused calls, but the platform registration is a separate process holding a session that kept the model listed in the catalog, so the switch looked ignored. Off now takes the registration down, registering while off is refused with the reason, auto-start honors it, and Settings hides the section's fields until the switch is on.

**A locked GenAI key hands over its unlock link.** The provider locks keys every 8 hours and its 401 carries the unlock URL, which is the entire remedy, so the classifier extracts and presents it. Tested against the documented body and the JSON-escaped relayed form.

**Drafted workflows render as DAGs.** `/api/kb/dag?path=` parses a corpus YAML and serves the platform route's exact payload (shared shaping helper), the embed accepts `path=` alongside `workflow=`, and the prompt teaches compose → write_kb_file → embed. Verified against a real corpus workflow file, and a non-workflow file is refused with a 422 naming why.

81 tests pass.

## v1.7 (2026-08-31)

### Say which kind of credential a surface needs, and detect the wrong kind (#285)

A user pasted an AI gateway API key in Settings. Chat worked, Settings said connected, and every workflow surface failed 502 with a message claiming no credential had been added. Two different products both sell "API keys": a platform token or platform API key reaches the platform API; an AI gateway key reaches the model routes and nothing else, and nothing in the interface connected the failure to the key kind.

Three changes:

- `/api/ai/health` now probes the platform API with the stored credential and reports `platformAccess`. Settings shows a plain note when a connected key is chat-only, before the user ever opens a DAG. A provider key with a base URL is reported as no-access without probing, since it is never sent to the platform by design; an unreachable platform reports null rather than pretending to know.
- The Settings intake copy and placeholder name the distinction up front: platform token or API key covers everything, AI gateway or provider key covers chat only.
- The 502 from the workflow routes describes the caller's actual situation instead of telling everyone they had added nothing.

79 tests pass.

## v1.6 (2026-08-31)

### Do not embed a DAG the credential failure just refused (#284)

On a deployment with no platform credential, `list_workflows` failed with an error naming the remedy, and the model pressed on: it pulled an old chat transcript and embedded a DAG for the workflow it had just failed to read. The embed makes the same failing request, so the reply rendered an error box where the answer should be, and the turn read as though the chat had silently died.

The prompt now says to stop on a credential failure and relay the remedy, answer nothing from memory of earlier sessions, and never embed a DAG unless `get_workflow` succeeded for that workflow this conversation.

## v1.5 (2026-08-31)

### Full screen for embedded artifacts (#283)

An interactive HTML artifact out of a simulation campaign is embedded in the reply at 480px, and reading it properly needs more than that box. Every chat embed (HTML artifacts, 3D models, workflow DAGs) now carries a hover control that takes it full screen; Esc or the same control returns it.

The Fullscreen API is asked first. When the studio runs inside the platform's session frame that request is refused unless the platform's iframe grants `allow="fullscreen"`, which is not ours to set, so a refusal falls back to a fixed overlay filling the studio viewport. Same button, best available result.

The iframe node never moves or remounts across the toggle: the artifact inside is stateful and a remount reloads it, so both paths only change classes on the wrapper the iframe already lives in.

## v1.4 (2026-08-29)

### Make the scrubber actually appear (#280)

The rail did not show at all. Two faults, either of which was enough on its own.

The rail is a direct child of `.chat-canvas`, and `.chat-canvas > *` sets `height:100%` for the package's own layout. A child gets that whether it wants it or not, so the rail spanned the whole canvas and painted at the top rather than along the bottom edge. This is the same rule that once put the chat controls mid-page, which I should have remembered when adding another direct child.

The ticks were also built from a single read one animation frame after mount. If the thread had not rendered by then the read found nothing, and with the conversation already in state nothing else changed to trigger a second look, so the rail stayed empty for the rest of the visit. It now rebuilds from a MutationObserver on the canvas and replaces state only when the ticks actually differ, so streaming does not thrash it.

Also added `__adeScrubber()` on window, which reports what each stage of the positional lookup found: whether the canvas, scroll host and list were located, how many children and message blocks were selected, the tick count, and whether the rail is in the DOM. The lookup has several places it can fail and without this the answer to "I can't see it" is guesswork.

### Turn the scrubber vertical, against the thread's edge (#281)

I built the scrubber horizontally along the bottom because the photo I worked from was rotated a quarter turn, and I read the rail as running left to right. It runs top to bottom.

Vertical is also the right axis on its own merits: it is the axis the thread scrolls on, so a tick's position maps directly to how far down the conversation it sits rather than being an arbitrary ordering.

It now sits against the thread's left edge, beside the conversations rail. That offset is measured from the scroll container rather than assumed, because the conversations rail is resizable and collapsible, and it is kept current with a ResizeObserver.

Idle it is a hairline of stubs. Pointing at it fades the rail up and lets the ticks grow out to their real lengths, so a long thread stays navigable without the rail competing with the conversation for attention. Ticks shrink as the message count rises so the rail stays a rail rather than growing past the bottom of the thread, and the overflow is hidden for the extreme case.

### Put the preview card beside the tick it describes (#282)

The card was placed at a percentage of the rail's height, but the ticks are a centred, padded stack, so the nth tick is not n/total of the way down the container. The card sat away from the tick under the cursor and pointed at the wrong part of the conversation, which is what made it look wrong.

It is now placed from the hovered tick's own geometry, and nudged back inside once its measured height is known, so a tick at the very top or bottom does not put half the preview off the thread. It stays to the right of the rail as before.

## v1.3 (2026-08-29)

### A scrubber for navigating long conversations (#279)

A rail of ticks along the bottom of the thread, one per message, height scaled by message length and coloured by role. Hovering a tick previews that message, clicking jumps to it, and the tick you are currently looking at is marked so the rail reads as a position rather than a list. Hidden below six messages, where scrolling already finds everything, and on phone widths where the ticks are too dense to hit.

## Prototype, and coupled on purpose

The chat package exposes no per-message anchor and no handle on its scroll container, so the rendered messages are found positionally. Two things follow from that.

Every lookup fails soft, so a markup change hides the rail rather than breaking the thread.

The DOM is the source of truth for what exists, because the conversation in state is not necessarily what is on screen: branching renders a subset and a pseudo message renders nothing. The message array supplies role and text only when its count agrees with what is rendered, and otherwise the ticks are built from the rendered blocks alone, which is always right about what can be scrolled to.

## Verification

I could not check this in a browser from here, so the fragile part was made a pure function and exercised against the list shape the package produces. The trailing spacer, whitespace-only blocks, and both queued-message shapes are excluded correctly. A streaming block is counted while a reply is in flight and settles when the turn completes, which is visible but harmless.

## Where this should really live

In `packages/ai-chat`. Emitting `data-message-id` on each rendered message would remove all the positional guessing here and would also unblock a conversation outline, jump-to-search-result, and deep links to a turn. The scrubber itself belongs there too, since the package owns the scroll container and already knows message boundaries and roles.

## v1.2 (2026-08-28)

### Bind the sealed key cookie to its subject (#278)

Signing into a second account in the same browser showed that account using the first account's provider model, reported as "connected with your key", without anyone having shared anything.

## Cause

The sealed cookie carried `{k,u}` and no subject, and `hydrateFromCookie` assigned whatever the cookie held to whoever was currently authenticated. A browser keeps one cookie jar per host regardless of who is signed in, so the cookie was effectively a bearer token for the key: any later session on that host picked it up.

The share feature was not involved. The wording came from the personal-key path (`mode !== 'none'`), which is why it read as the second user having a key of their own, and `shareUserKey` is only reachable from one explicit route.

## Fix

The subject is sealed in with the key, and hydration accepts the cookie only for that subject. A cookie written by an earlier build carries no subject, cannot be attributed to anyone, and is refused rather than guessed at, so everyone re-enters a key once. That is the intended cost of closing it.

## Scope

Reachable today mainly through impersonation, which is restricted to one operator, so the practical exposure is small. It does not depend on impersonation though: a shared browser, or one person signing into two accounts, reaches it the same way.

Worth noting this became reachable when the cookie moved from `SameSite=Strict` to `None` in #275, since Strict was suppressing the cookie in the cross-site context rather than the binding being correct. The missing subject was always there.

## Verification

Four tests: a second user in the same browser gets nothing, the rightful owner still recovers their own key from the same cookie, an unattributable cookie grants nothing, and an unrelated jar is ignored. The regression test was run against the unfixed code and fails there, so it is catching the defect rather than describing the fix. 80 tests pass overall.

## v1.1 (2026-08-28)

### Name the locked provider key behind the gateway's masked 400 (#277)

A GenAI-backed model failed with the gateway's generic `An error occurred while generating the response`. The cause was a provider key locked at the provider, which the user can fix, and the reply told them only to pick another model, which never would have.

The gateway returns that one body for both a rejected provider credential and a fault at the provider, and nothing in the response separates them. The message now names the locked key as the usual and actionable cause while saying plainly that it is not certain, and points at model availability in the footer to confirm. It applies only to models carrying a provider prefix, since a session or org model has no personal credential to unlock.

Filed upstream as parallelworks/core#19405. The gateway passes provider detail through on some paths (`Unsupported parameter: max_output_tokens` comes through verbatim) and replaces it on others, so a cause code on the error object would let this be reported exactly instead of hedged. Once that lands this hedge can be replaced with the real reason.

Four more tests: the masked error names the locked key without asserting it, a shared model gets no provider-key advice, and an explicit parameter rejection is still told apart from the masked failure rather than being lumped in with it. 76 tests pass.

## v1.0 (2026-08-28)

### Original-file previews, indexed-text labeling, and file deletion (#2)

Serve files inline with correct MIME (/api/kb/raw) so images render;
convert office documents to PDF on demand via LibreOffice with an
mtime-checked cache (/api/kb/pdf). The viewer now splits Preview
(original rendering: image, PDF, converted office doc) from Indexed text
(the representation stored in the search index, labeled as such).

DELETE /api/kb/file removes a file plus its extract-cache and PDF-cache
entries and re-indexes its subtree; the viewer carries a two-step Delete.
Root-level files get proper index coverage: gufi_dir2index --max-level 0
rebuilds just the root db (also wired into the sweep, which previously
never re-indexed root files).

### Image indexing, platform workflow tools, query builder, pane controls, rename to Studio (#3)

Images join the index: tesseract OCR plus optional vision-model captions
through the gateway's streaming Responses API (ADE_VISION_MODEL), cached
by mtime with a parallel warmer; extraction caches of every kind are now
reused across passes, and stale cache entries are pruned by the sweep.
Root-level files gained real index coverage (gufi_dir2index --max-level 0)
wired into uploads, deletes, and the sweep.

Chat gains the platform command surface: workflow catalog with
descriptions and tags for assembly recommendations, DAG preview,
dry-run-first workflow execution, run monitoring with errors and logs,
pw CLI help discovery, and show_in_viewer so the model can display files,
images, and workflow DAGs in the library pane on request (SSE display
events; custom SVG DAG renderer with layered layout).

Query view in the spirit of the GUFI query-builder webapp: canned
parameterized queries, a structured builder (source, filter rows, group
by, sort, scope) compiled server-side against a whitelist, a guarded
raw-SQL mode, and Superset-style saved queries. Staged -I/-K/-E/-J/-G
aggregation gives global ordering across the per-directory databases in
tens of milliseconds.

Shell: sidebars collapse and resize (main nav and library rail, widths
persisted), drag-and-drop of files and directories onto the tree with
relative-path preservation, sync now refreshes the tree, search view
aligned with the other views, and the app is renamed activate-studio
with a config-driven display name (APP_NAME, default Studio).

### Customizable brand icon; canned queries tab first (#4)

APP_ICON points at an image file served through /api/brand-icon and shown
in place of the letter badge; deployments brand the app with their own
mark (name via APP_NAME). The Query view opens on canned queries.

### Fit the brand header and stop the default-brand flash (#5)

Widen the nav to 240px so longer deployment names fit beside the icon,
and hold the brand slot empty until /api/config arrives so the generic
default never flashes before the configured name and icon.

### Help view, unified library card, footer cleanup (#6)

Add a Help view describing what the system is, what each surface does,
how ingestion and the background sync behave, and how the GUFI index
works, rendered from the deployment's configured name and corpus label.
Library becomes one connected card like Chat (tree, draggable boundary,
viewer) instead of two separate cards. Remove the footer attribution
line.

### Markdown-driven help cards; document OpenAI-compatible model backends (#7)

Help content lives in docs/HELP.md (HELP_FILE overrides per deployment)
with {appName}/{kbLabel} substitution; the view splits it at ## headings
into a card grid with section icons, a config-driven hero, and a live
stats strip on the index card.

README gains a Models section: the chat works with any OpenAI-compatible
backend via OPENAI_BASE_URL/OPENAI_API_KEY (vLLM, llama.cpp, Ollama,
OpenAI), with the ACTIVATE gateway as the zero-config default; platform
workflow tools degrade gracefully without the pw CLI.

### Position as a standalone tool with optional ACTIVATE integration (#8)

README and docs lead with standalone operation (any corpus directory,
any OpenAI-compatible model endpoint) and describe the platform gateway,
workflow tools, and session serving as the optional integration layer.

### Labels on the data, dark mode, corpus overview, platform JWT identity, bulk upload (#9)

Labels: files and directory subtrees carry tags as extended attributes
(user.studio.tags), indexed by GUFI (xattr names and values are BLOBs;
comparisons need CAST). Directory labels inherit down the tree. The
library multi-selects with ctrl or cmd click and applies labels from a
vocabulary popover; the viewer shows own and inherited pills; search
results carry label pills; search_kb annotates results with [labels: ...]
and accepts a tags filter, with the system prompt instructing the
assistant to state provenance labels and never present research-labeled
material as established fact.

Chat gains query_corpus (canned index statistics) and get_labels tools,
and its conversations rail is drag-resizable. The library tree reveals
and scrolls to whatever the chat or search opens, chevrons are legible,
and directory rows are drop targets, so a drop lands in that folder.
Uploads batch 25 files per request with a progress panel (done/failed
counts), sized for thousand-file drops.

Dark mode: a deliberately re-stepped token set on [data-theme=dark],
covering the shell and the ai-chat --theme-* contract, with a THEME
default and a per-user toggle. New Overview view: stat tiles, storage by
file type, largest files, recent changes, labels, and index health.

Platform identity: optional JWT verification against a JWKS endpoint
(jose), header and claim checks configurable, identity flowing into
/api/config; roles can build on this later. Standalone behavior is
unchanged without AUTH_JWKS_URL. Setup and branding documented in
docs/CUSTOMIZATION.md with a committed .env.example; the deploy script
now kills stale instances so exactly one endpoint serves.

### Rename the Overview view to Stats (#10)



### Open the Add popover rightward so the rail never clips it (#11)



### Discoverable labeling, Settings page, theme icon, smooth resizing (#12)

Labeling is reachable everywhere: hover checkboxes on every tree row for
multi-select (with the selection bar), a tag button per row for one
click labeling of a file or directory subtree, a Labels button in the
viewer, label filter chips on Search, a label field in the Query builder
(EXISTS over the indexed xattrs), and an apply_labels chat tool gated to
explicit user requests. The label panel floats at the top right instead
of covering the tree.

Settings view edits runtime configuration stored beside the index and
overriding environment defaults: product name, corpus label, default
theme, background sync interval (the sweep timer re-reads it each
cycle), vision captioning model (injected into incremental indexing),
and chat starter prompts. Theme toggle is a sun/moon icon in the nav
header. Query and Stats get title/subtitle headers like Search.

Fix the chat main pane collapsing to rail width (the rail override
selector also matched the empty state's own flex root; scope to the
direct layout child) and make both drag resizes write to the DOM during
the drag, committing state on release, so they track the pointer without
re-rendering the tree per move.

### Opt-in select mode with bulk delete; supporting nav to the bottom (#13)

Multi-select becomes an explicit Select mode so checkboxes cost no space
in normal browsing; the selection bar carries Labels and a confirmed
bulk Delete (POST /api/kb/delete unlinks all files, then re-indexes each
touched subtree once; directories are refused with a reason). Settings,
Help, and the theme toggle move to the bottom of the sidebar as
supporting items, freeing the header. The Settings vision-model field
offers the connected endpoint's models as suggestions while accepting a
typed id, and the rail header truncates gracefully.

### Dark mode contrast pass, icon select toggle, collapsed-nav pinning (#14)

Fix the genuinely unreadable pieces in dark: nav items and secondary
buttons kept their light-mode ink; the input focus ring glared white;
the DAG viewer had hardcoded light fills (now theme variables). Re-step
the dark palette for better surface separation and text contrast. The
Select toggle becomes an icon button sized like its neighbors, and the
Settings/Help/theme group pins to the bottom in collapsed navigation.

### Align the chat conversations header and model toolbar to one height (#15)



### Instant directory labeling; unify header heights at the library's 52px (#16)

A label change alters no file content, so the former full subtree
reindex (with re-embedding) was wasted minutes; the xattr rows are now
upserted directly into the existing per-directory index dbs, taking
labeling of even the largest subtree from about a minute to under 100 ms,
with the full reindex kept as a fallback for paths that cannot be
updated in place. The label panel shows a spinner while applying. The
chat conversations header and model toolbar drop to 52px to match the
library rail header instead of towering over it.

### Match chat divider lines to the library border token in both themes (#17)



### Title alignment, tree label pills, viewer links, 3D model viewer, code block styling, accent schemes, Geist fonts, chat attachments (#18)

- Search/Query/Stats headers share view-title/view-sub at one padding
- Library tree shows own-label pills (toggle in rail head, auto in select mode); /api/kb/tree carries xattr labels via batched getfattr
- show_in_viewer returns an #open= markdown link instead of auto-navigating; hash deep-links open the Library with the file revealed
- STL/STEP files render in a three.js viewer; STEP meshed server-side by occt-import-js, cached by mtime beside the pdf-preview cache
- Streamdown code blocks restyled: hairline border, 28px header strip, inline actions, Geist Mono
- Accent color presets in Settings applied as CSS variable overrides in both themes
- Geist / Geist Mono bundled via Fontsource as the app font stack
- Chat attachments: uploads land in the library attachments directory, get indexed (small images force OCR/caption), and their extracted content joins the conversation; adapter exposes list/upload/remove/download
- Default starter prompts include tool discovery; Help documents attachments, 3D models, and the underlying technologies

### In-tab viewer links, custom accent, surface tones, new-folder action, wide help cards (#19)

- #open= links in chat replies are intercepted in the capture phase and open the Library viewer in place instead of a new tab
- Accent accepts a custom hex via a color picker; shades derived for both themes
- Surface tone setting re-steps the base neutrals (cool default, neutral gray, warm gray); hardcoded dark literals moved onto variables so tones reach inputs, hovers, and code blocks
- New folder creation from the Add popover via POST /api/kb/mkdir
- Help cards with long lists span the full grid width in two columns

### Assistant tools panel, HPC cluster tools, file-based extensions (tools, skills, agents) (#20)

- Settings split into sections: general, Assistant tools, Extensions
- Tool catalog at /api/chat/tools with full call specs; clicking a tool name in Settings shows the JSON spec for copying into custom tools
- Per-tool enable toggles (disabledTools) filter the specs sent to the model each request
- Custom command tools defined in Settings; run server-side, output returns to the chat
- HPC built-ins: list_clusters (pw cluster ls) and cluster_command (pw ssh), read-only scheduler queries by default, state-changing commands only on explicit user request
- File-based extensions in <index>/extensions: tools/*.json as command tools, skills/*.md loadable via a dynamic use_skill tool, agents/default.md appended to the system prompt per request
- Chat attachments page wired: toAttachments renders AttachmentManager (was a no-op, the sidebar item did nothing)
- docs/CUSTOMIZATION.md documents the tool and extension model

### Align chat dark surfaces with the panel tone; README references and pw CLI auto-auth note (#21)



### Labels, navigation, previews, identity, and index hardening (#22)

Bulk labeling in Search and Query (multi-select, select all, 1,000-item label writes). URL hash navigation with browser history, citation deep-links, attachment click-through. Chat label scoping enforced server-side, humanized thinking stream, persisted reasoning, chat-history awareness. PDF and office previews as server-rendered page images (works inside sandboxed session iframes). workflow.yaml tab on the DAG pane. Platform JWT verification (X-PW-User-Token) with /api/whoami and footer identity/index-health popovers. Sweep poison-pill fix with per-directory error isolation. Inherited-label query support. Raised search/query limits with load-more. Starter saved queries. Settings and Stats restructured. Deployment favicon.

### Inline images and document pages in chat replies (#23)

Chat answers can now show KB images and document pages inline (served from /api/kb/raw and /api/kb/pdf-page), with the library deep-link kept alongside. show_in_viewer returns ready-made markdown for image and document targets; CSS bounds inline media. Verified end to end with an architecture PNG rendered in the chat.

### Accent-color the active conversation in the chat rail (#24)

Selected chat session now uses the accent pill (same treatment as the sidenav active item) instead of the barely-visible muted panel background. Verified light and dark.

### Interactive DAG and 3D viewer windows embedded in chat replies (#25)

Chat replies can now embed the live DagViewer and ModelViewer as same-origin iframe windows via /?embed= URLs rendered from markdown image syntax (ai-chat patch renders only same-origin /?embed= sources as iframes). Verified end to end: interactive DAG with node selection working inside a chat reply.

### Accent user bubble; paint the table-download dropdown (#26)

Own chat bubble in the accent pill. Streamdown's table-download dropdown rendered transparent (its utility classes have no compiled CSS); painted directly with panel tokens plus hover and dark-mode treatment. Verified both themes.

### Chat table styling: token dividers, tinted header, zebra rows (#27)

Markdown tables in chat had 1px black dividers (missing color utility in the compiled CSS). Dividers, header background, and zebra striping now use platform tokens; --color-* Tailwind v4 tokens mapped on the chat canvas. Verified both themes.

### Pan and zoom the DAG canvas (#28)

DAG fits the available space by default (viewBox), wheel zooms around the cursor, drag pans, +/-/Fit controls; node click still selects. Fixes unnavigable wide workflows, especially in the embedded chat window.

### Show what each built-in tool actually calls; drop KB label from Settings (#29)

Tools catalog carries a per-built-in calls description (actual pw CLI / GUFI / xattr invocation), shown above the spec expander. Knowledge base label removed from the Settings form; stays environment-driven.

### Help refreshed into a user guide; unknown file types sniff as text (#30)

Help page rewritten to cover the current feature set with three new cards and fixed bullet indents. Unknown extensions (.bak-*, dotless, logs) now sniff the head and preview as text when the content is text.

### studio_docs tool: assistant answers how-to questions from the app docs (#31)

Built-in tool serving the shipped Help, Architecture, and Customization docs on demand, with a prompt line routing application questions to it. Verified: a bulk-labeling how-to question produced a correct step-by-step answer grounded in the guide.

### Docs-style Help: section nav plus one article at a time (#32)

Help restructured from a card grid into the standard docs layout: left section nav from the HELP.md sections, single article pane with readable measure, verified light and dark.

### Help: name the New folder control and folder drag-drop behavior (#33)

Adding material section now names the Add popover's New folder field and notes dropped folders keep their structure; the assistant answers from this guide, so the gap mattered.

### Right-click context menu on the library tree (#34)

Context menu on tree rows: Open/Labels/Delete for files; New folder inside/Labels/Delete folder for directories, with a new recursive dir-delete endpoint that prunes caches and the index slice. Verified create-then-delete round trip, label panel open, and menu contents.

### Model callability: health check, footer status, classified credential errors (#35)

Live /api/ai/health check, footer 'N models ready' line with detail panel and re-check, and classified gateway credential errors with actionable unlock messaging. Groundwork for per-user provider credentials.

### Per-user model keys for shared platform sessions (#36)

Verified users add their own key in Settings (Your model access); chat, model listing, health, and pw CLI tools run on the caller's key, deployment credential otherwise. Encrypted at rest beside the index (gitignored) or memory-only with TTL; last-four-only to the browser; 403 without verified identity; hidden entirely on standalone deployments. Vault unit-tested (roundtrip, no plaintext, 0600, session TTL, transitions); HTTP surface verified.

### OpenAI-compatible RAG endpoint, platform registration, docs-style Settings (#37)

studio-agent (full pipeline) and studio-rag (context injection) served at /v1 with caller-key passthrough; managed pw endpoints run --openai registration with status and auto-start (verified live: models in the platform catalog, grounded cited answers through the platform gateway); Settings restructured into section nav; dark-mode accent coverage fixed. Docs updated; new modules scrubbed.

### Model dropdowns for vision captioning and RAG default (#38)

Vision model and RAG default-model settings become selects over the live catalog; saved out-of-catalog values stay selectable; studio-* virtual models excluded. Verified options, exclusion, and value retention.

### Taller chat starter prompts field (#39)

Starter prompts textarea grows to 10 rows with a 200px minimum, still vertically resizable.

### Log the credential path per RAG proxy call (#40)

authSource logging (caller bearer / injected deployment / keyless fallback) with a forwarder marker header. Confirmed the platform tunnel forwards caller keys to registered providers.

### Short-term tokens alongside API keys; POST key endpoints; named registration (#41)

Token-kind detection with parsed expiry, expiry enforcement in resolution, POST set/clear endpoints for proxy robustness, configurable registration name. Unit tests: token detection, expiry flag/enforcement, api-key kind, clear.

### Per-caller model resolution, strict personal-key mode, clear registration UI (#42)

Per-caller default underlying model (verified: bogus deployment default falls back to the caller's own model); stored-key resolution for session-authenticated keyless callers; requirePersonalKey mode verified end to end (models blocked with hint, chat credential error, footer key-required, non-AI features untouched); connected banner with catalog ids and model roster.

### Custom model providers, credential UX, right-worded empty state (#43)

Provider picker (platform default / other ACTIVATE host / OpenAI / custom URL) stored with the personal key and used across chat, models, health; non-platform keys never reach pw CLI executions. Patched empty-state copy plus credential-error toast deep-linking to Settings, Your model access. Unit tests for base-URL storage/validation; empty state and deep-link verified.

### Reliable streaming through the platform tunnel; credential UX polish (#44)

Keepalives during silent agent phases, peer-disconnect abort, close-tail grace in proxy and forwarder. Verified through the platform gateway: studio-rag 5s and studio-agent 18s, complete answers with citations and finish_reason delivered. Provider dropdown reworded to OpenAI-compatible; no-key toast restored; form spacing fixed.

### Credential toast sits below the header (#45)

Toast moved to the top of the chat pane per feedback.

### RAG endpoint follows Your model access in the settings nav (#46)

Section order: General, Your model access, RAG endpoint, Assistant tools, Extensions.

### Library rail title matches the Chats header style (#47)

14px semibold normal-case, same treatment as the chat rail's Chats header, both themes.

### Deployment postures: docs, kill switch, workflow dropdown; README diagram (#48)

Postures documented and enforceable: DISABLE_PERSONAL_KEYS env kill switch (unit-tested: set refused, resolution blocked, off by default), workflow deploy dropdown wiring the env per posture, README mermaid architecture diagram, toast button nowrap.

### Provider dropdown names the actual deployment gateway host (#49)

Personal-key status carries gatewayHost; the default provider option shows it concretely.

### Settings and Help sections persist in the URL (#50)

Section slugs in the view hash with refresh restore, verified on both pages.

### Model access page shows connected state and available models (#51)

The Your model access section now opens with a status banner matching the RAG endpoint page: a green connected banner listing the models available to the stored credential when it tests healthy, amber when the key fails its check or when the deployment requires a personal key and none is loaded, and gray otherwise. The stored key is tested automatically when the section loads, with a Re-check button. Storage mode, token expiry, and provider details move inside the banner so each fact appears once; the explainer paragraph now only renders when no key is loaded, with wording that follows the require-personal-key setting.

Server side, all five model-key endpoints return one unified status payload including gatewayHost, so the provider dropdown keeps its host label after a save or remove instead of falling back to "default".

Verified against an isolated instance (temp index, local JWKS, signed test JWT) with Playwright: green banner with 8 model chips for a healthy credential, amber not-connected state after Remove with the host label intact.

### Curate the RAG model surface for agent and chat clients (#52)

Testing in pw code showed studio-rag loses its grounding inside tool-calling harnesses: the host agent's tool specs pass through, so the model works those tools instead of the injected context (server logs confirmed every call ran mode rag; the context was injected and outweighed). In plain chat interfaces the same mode is the fast option, a single grounded call with no tool loop. So each model now has its own advertise toggle on the Settings RAG page (studio-agent on by default, studio-rag off; RAG_ADVERTISE_AGENT_MODEL / RAG_ADVERTISE_RAG_MODEL envs); both stay callable by name and ?all=1 always lists both.

Also in this change: the deployment's own published catalog models are hidden from the Studio's chat model list, since picking them inside the app routes the call out through the platform tunnel back into this same server; ModelSelect gains a type-a-model-id mode so standalone deployments with an empty catalog can still set default and vision models; the RAG settings page drops the redundant underlying-models roster and its registration paragraph now states the credential path accurately (platform callers ride their own credentials, the deployment key covers only keyless clients such as pw code); HELP.md and CUSTOMIZATION.md updated to match.

Verified on the live deployment: /v1/models advertises only studio-agent by default, ?all=1 lists both, the chat model list drops the two circular session entries and keeps 9.

### RAG endpoint observability and Model access rename (#53)

Adds a Recent endpoint calls panel to the Settings RAG page, backed by an in-memory ring of the last 50 /v1 calls: time, model, underlying, credential source, duration, retrieval blocks with the distilled search terms, and outcome. Rejected keyless calls appear too, so a misconfigured client is visible from the app rather than requiring server logs. Question text is never recorded and the page says so.

Also: the advertise toggles now carry the model descriptions (full pipeline vs single fast call and which client type each suits), replacing the duplicated bullet list; the registration banner only shows the catalog ids that are actually advertised; the settings section is renamed to Model access across UI, docs, server messages, and the ai-chat patch.

Verified live: a studio-rag call logged with caller-bearer source, 2.4s, 5 retrieval blocks and terms; a keyless call logged as rejected; page screenshot checked.

### Simplify the README architecture diagram (#54)

The README diagram is the ten-second orientation; the internal server
decomposition it carried is documented in docs/ARCHITECTURE.md. Now
six nodes and five edges: both client types into the server, the
server onto the knowledge base and a model endpoint, the platform
dotted as optional.

### Chat ownership, pinned model listings, help refresh (#55)

Conversations now record the verified username of whoever started them (null on standalone and for pre-existing chats). The rail shows other users' chats with the owner in parentheses and gains an all-chats/my-chats pill; exported transcripts carry the owner in the header and a user-<name> label so Search, Query, and Scope can filter sessions by user.

/v1/models lists a pinned form beside each bare studio model, naming the underlying model resolved from the caller's own credential, so dropdowns say what you get while the bare name stays the stable alias.

HELP.md updated for the RAG modes, advertise toggles, calls log, and required-key deployments.

Verified on an isolated instance with two signed test identities: owner stamped on create and backfilled on first turn, transcript header and user-testuser xattr label present, second user sees the suffix and the filter hides the other user's chat; live deployment confirmed serving pinned listings and owner fields.

### Library refresh without collapse, chat stream keepalives, shared-history toggle (#56)

The Library tree now refreshes softly: a refreshTick prop re-fetches
every open directory in place, replacing the keyed remount that
collapsed the expanded tree whenever sync now or an upload bumped it.
A refresh button in the rail header picks up files that arrived
outside the app, such as chat-session exports.

The app chat stream sends SSE comment keepalives every 3 seconds. A
long model turn planning tool calls emits nothing, and proxies in
front of the session kill silent streams, which left the UI thinking
forever on a 108-second multi-tool answer that the server completed
and recorded. Same failure and same fix as the /v1 path.

New chatSharedHistory setting (default on, CHAT_SHARED_HISTORY env):
off limits each user to their own conversations, enforced server-side
on list, open, rename, and delete; unowned conversations stay visible
to all, and exported transcripts remain in the knowledge base tree
either way. The all-chats/my-chats pill now appears only when another
user's chats are actually in the list.

### Chat rail defaults to the viewer's own conversations (#57)

The rail is personal working space and the library is the shared
corpus, so the default view splits accordingly: the rail starts on my
chats (unowned legacy and standalone chats always show, so this only
hides other users' conversations) with the pill widening to all. The
shared-history setting copy now states the full model: it governs
whether other users' chats are reachable in the chat interface at all,
while exported transcripts stay in the knowledge base tree.

### Chat filter pill shows on any identity-enabled deployment (#58)

Previously it appeared only once another user's chats existed, which
read as missing on a deployment with a single active user. Now it is
present whenever identity verification is on, and standalone
deployments still never see it.

### Parameterize deployment branding and layout; genericize provider copy (#59)

workflow.yaml gains the inputs a real multi-user deployment needs: app
name (APP_NAME), brand icon path (APP_ICON; the icon may arrive after
launch since the server checks the file per request), semicolon-
separated chat starter prompts (SUGGESTED_PROMPTS), a relocatable
index directory (with the embedding model staged into it), starter
directories created inside the knowledge base, and a stable session
name by default (the port suffix is now an opt-in toggle, so the
session URL survives relaunches).

Provider-specific wording in the credential copy is genericized to
'some providers lock keys on a schedule', and the example platform
host placeholder now uses the public host.

### Deploy workflow requires Node 20; Settings and Help headers match rails (#60)

The pull step accepted any system node, and corepack pnpm crashes
under pre-20 runtimes (ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING, hit on
a cluster with an old system node); both source modes now check the
major version and fetch v20 when the system one is older. The
Settings and Help nav headers drop the uppercase micro-label style for
the same 14px semibold header the Chats and Library rails use.

### Scope control layers under side panels; workflow icon in repo (#61)

The scope anchor sat at z-index 30, above ai-chat's side panels (z 10
to 20), so the thinking panel's close control was unclickable behind
it. The anchor now sits at 8 and raises to 60 only while its own menu
is open. deploy/icon.png carries the workflow icon in the repository
so platform imports of the remote workflow can reuse it.

### Deploy build survives a broken corepack pnpm shim (#62)

corepack fetched the latest pnpm on the target cluster and its vm
loader crashed (ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING) even under our
fetched Node 20, and without set -e the step limped past the crash.
The build step now fails fast, validates pnpm by running it rather
than by the shim existing, and falls back to a plain npm global
install of pnpm 10. package.json pins packageManager pnpm@10.33.4 so
corepack resolves a known-good version where it does work.

### Deploy clone hard-resets to origin; pnpm without corepack (#63)

The clone update used pull --ff-only with errors swallowed, so a
force-pushed branch left a stale tree building silently; the checkout
now hard-resets to the origin ref and logs the commit it builds.
corepack is out of the bootstrap entirely: its vm loader crashes on
several pnpm/Node pairings and its broken shims linger across runs
(run 00003 built the web app with a working pnpm, then died through a
leftover corepack shim in the server build). A plain npm global
install of pnpm 10 overwrites any stale shim.

### Model listing advertises bare studio names only (#64)

The per-caller pinned entries (studio-agent/<resolved-model>) read as
duplicate models in catalogs. Bare names stay the stable aliases;
pinning remains available per request and ?all=1 still shows
everything, and the recent-calls log already reports which underlying
model each call resolved to.

### Conversation rail rows match the Library tree scale (#65)

Rail links (conversation rows, New chat, Attachments) render at 13px
like the Library tree rows; the Chats header keeps the 14px shared
rail-title size.

### GUFI source tarball satisfies cmake's git describe (#66)

GUFI's cmake derives its version from git describe, which fails on a
shallow tagless clone ('No names found'), and the tarball stripped
.git entirely, leaving the answer to whichever repository git found
walking up from the extract location. The clone now gets an annotated
deploy-snapshot tag and the tarball keeps .git, so describe resolves
deterministically.

### Label overlay for filesystems without user xattrs (#67)

NFS and EFS refuse user extended attributes (setfattr returns
ENOTSUP), which silently broke labels on a shared-filesystem
deployment: the index upsert worked but a full reindex rebuilt the
xattr rows from a filesystem that has none. Labels now fall back to a
tags-overlay.json beside the index when the filesystem refuses the
write. The overlay merges into tag maps and tree listings (so labels
work even without GUFI), wins over stale index rows, and re-applies
into the index after any incremental or root reindex and at boot,
covering the deploy-time full reindex. Xattr-capable deployments keep
the current behavior with an always-empty overlay.

### Deploy clone self-heals a repo directory without .git (#68)

An interrupted copy or failed clone leaves a repo directory that is
not a repository, and git clone dies on the non-empty destination;
wipe the debris and clone fresh.

### License under Apache 2.0 (#69)

Apache 2.0 over MIT for the explicit patent grant, which matters to
the enterprise and government adopters this tool targets; all
dependencies are permissive (MIT, BSD, Apache) and compatible.
Copyright Parallel Works, Inc.

### GUFI dependency build avoids -march=native (#70)

llama.cpp, vendored via sqlite-lembed for the embedding layer,
defaults GGML_NATIVE on; new CPUs advertise AVX-VNNI and older
assemblers reject the resulting vpdpbusd instructions (binutils 2.35
against a VNNI-capable node). The deploy step injects GGML_NATIVE=Off
for a portable build and re-extracts the GUFI tree clean so a failed
prior build cannot leave stale cmake state.

### Label apply survives a failed reindex fallback (#71)

The label is durable (xattr or overlay) before the reindex fallback
runs, so a deployment without GUFI returned 500 on a labeling call
that had actually succeeded. The fallback is best effort now.

### GUFI installs without root (#72)

GUFI's server and client config paths default under /etc/GUFI, so
make install failed on a node without sudo after a complete build.
Point both at the install prefix.

### Skip GUFI bash completion install on unprivileged deploys (#73)

After the config paths moved into the prefix, make install still tried
to write /etc/bash_completion.d and failed without root, discarding an
otherwise complete build. BASH_COMPLETION=Off skips it.

### Indexer picks a Python 3.10+ interpreter (#74)

The enrichment and embedding scripts use 3.10+ syntax, and clusters
commonly default python3 to 3.9 while shipping a newer one alongside
(the node: 3.9 default, 3.13 installed), which failed indexing
with a TypeError on a union type annotation. The server and
reindex.sh now resolve PYTHON_BIN, then the newest versioned
interpreter that reports 3.10 or later, then plain python3.

### Embedding step resolves gufi_sqlite3 from GUFI_BIN (#75)

embed.py defaulted to a hardcoded /opt/gufi/bin path, so a deployment
with GUFI installed anywhere else failed the embedding stage after
enrichment had already succeeded. The default now follows $GUFI_BIN
and the server passes the resolved path explicitly.

### Apptainer image as a first-class deployment source (#76)

Every deployment failure on a new cluster was an environment mismatch,
not a code defect: an old default python3 beside a newer one, a stale
corepack shim, an assembler rejecting the CPU's newest instructions,
install paths assuming root, a binary sought at a hardcoded prefix.
The image pins Node, Python, GUFI, and the embedding model so the app
stops discovering the host.

app.def carries every fix the from-source path learned: npm instead of
corepack, a tagged GUFI clone for cmake's git describe, GGML_NATIVE
off for a portable llama.cpp, config and completion paths that need no
root, python3.11 for the indexer's syntax, tesseract for OCR. A
start.sh seeds the embedding model into the bind-mounted data
directory and optionally indexes before serving, so nothing writable
lives in the image.

workflow.yaml gains a Container source that stages a .sif (local path
or any URI Apptainer can pull), skips the build and host-index steps,
and launches with the corpus and data directory bind-mounted and the
posture, branding, and gateway environment passed through.

### Key the label overlay by fsid.inode, re-join the index on inode (#77)

Feedback from GUFI's author: an index that stores information the source
tree cannot reproduce is no longer recreatable, and their convention for
such information is a store keyed by fsid.inode that a rebuilt index is
re-joined against. Where extended attributes work the labels live on the
files and the index is already recreatable; the overlay only exists for
filesystems that refuse them, and it was keyed by path, which loses
labels on rename and misapplies them when a new file appears at an old
path.

The overlay is now keyed by fsid.inode with the path demoted to a hint
that the re-join refreshes, so labels follow renames and moves. Re-apply
asks the index where each inode lives now and writes labels back by that
mapping instead of trusting stored paths, and a full pass prunes entries
whose file is gone.

Each entry also records creation time. Filesystems recycle inodes
aggressively: a test deleting a labeled file and writing another in its
place reused the inode immediately on ext4, and the newcomer inherited
labels meant for a different document. A creation-time mismatch now
retires the entry, while ordinary edits keep theirs. Skipped where a
filesystem reports no creation time.

### Semantic search survives directories with nothing embedded (#78)

Vector search attached every index db in one gufi_sqlite3 script, and a
directory with nothing embeddable in it (an empty folder, images only, a
corpus mid-ingest) has no gvec table. The tool abandons the script at
the first missing table, so a single such directory silently truncated
semantic results to whatever had already been emitted, or removed them
entirely when the walk reached it first. The deployment was
returning partial results and a container test with empty starter
directories returned none.

A cheap first pass now asks each db whether it carries a vector table
(reading sqlite_master is always valid) and the search attaches only
those, cached alongside the db list and invalidated with it.

Also in this change, the Apptainer recipe learns what a real container
run taught: the indexer needs bash rather than the slim image's dash,
GUFI's AI tooling needs its dependency libraries copied beside the
binaries, and those need rpaths (both the binaries and the libraries,
since a RUNPATH does not cover transitive dependencies) rather than an
LD_LIBRARY_PATH, which made Python load GUFI's sqlite and broke every
indexer script.

### Stage container images from platform buckets (#79)

A container runtime cannot pull a pw:// bucket path, so the staging
step now recognizes bucket URIs and fetches them with the CLI, the same
way the bundle path does. Keeps images inside the network boundary,
which is the practical option for accredited environments.

### Expand the container to a sandbox where squashfs is unavailable (#80)

A SIF is a squashfs image, and the target login node runs a kernel
without squashfs, without squashfuse, and without the privileges to
load the module, so the runtime refused to mount the image at all.
Where a direct mount fails, the image is now expanded once with
unsquashfs (which needs no kernel support) and run from the sandbox
directory, cached against the image timestamp so reruns skip it.

### Keep the container image and sandbox off shared storage (#81)

Measured on the target cluster: the image is 228 MB compressed and 559
MB expanded, while the index it serves is 27 MB. Both are derived from
a bucket artifact and cost a minute to recreate, so paying shared
filesystem rates for them (and extracting thousands of small files
across NFS, which stalled in testing) buys nothing.

A container scratch directory input, defaulting to node-local /tmp,
now holds the image and its sandbox; the staged image is removed once
expanded. Shared storage keeps only what is actually durable: the
knowledge base and the index.

### Keep bucket transfer progress out of the run transcript (#82)

The CLI draws a carriage-return progress bar, so the platform log
viewer renders the whole transfer as one endless line. Progress now
goes to a file and the step reports the staged size, with the tail of
that file surfaced only on failure.

### Paint the configured theme before the first frame (#83)

Theme, accent, and surface were applied only after React mounted and
the config request returned, so every load flashed stock light-mode
blue before settling, which is most obvious in dark mode. The app now
caches the CSS it applies, and a small inline script restores it
before the bundle loads. The fetched configuration still wins; the
cache only decides what the first frame looks like.

### Parse workflows with the platform's own parser (#84)

@parallelworks/workflow-parser is the parser ACTIVATE itself uses, so
the workflow view now names steps, resolves dependencies, and dumps
YAML through it rather than through logic of ours that would drift as
the schema evolves. Two things improve immediately: dependencies are
transitive (a job that needs a job that needs another now reports
both), and the endpoint returns the input form the platform would
render for the workflow, which is what a run actually asks for.

### Derive theme tokens with the platform, default to neutral gray (#85)

@parallelworks/ui derives the whole --theme-* token set from an accent
and a background, and the shared chat components read those tokens, so
hand-mapping a handful of them was why dark mode kept showing stock
blue wherever our own stylesheet did not reach. Accent and surface are
now emitted together, since the derivation needs both, and the default
accent participates like any other rather than opting out.

The theme entry point carries no React, so it is usable while the rest
of that package targets React 19 and we are on 18.

Two fixes fall out: the style element is moved to the end of head when
written, because the pre-paint script inserts it before the bundle's
stylesheet and equal specificity let the bundle win; and the default
surface is neutral gray. The library context menu now says New folder
rather than New folder inside.

### Seed derived theme tokens from the panel, not the page (#86)

The shared chat components render inside a card, so seeding the
derivation with the page background turned the chat pane grey while
every other view sat on white. Seeding from the panel background
restores the white thread area in light mode and keeps the dark panel
tone in dark mode, and it follows a surface override the same way.

### Upgrade to React 19 (#87)

Both shared packages require React 19: the chat components have been
running against an unmet peer here all along, and the UI package that
carries the platform's components needs it too. React 19 drops the
global JSX namespace, so the three annotations that relied on it now
use ReactElement.

Verified every view renders with no runtime errors: chat, library,
search, query, stats, settings, help.

### Accept a URL for the brand icon and favicon (#88)

APP_ICON and APP_FAVICON took a path on the resource, which means
staging a file next to the corpus and, for a container deployment,
keeping it under a bind mount. Either may now be an https URL, which
the interface loads directly; paths behave exactly as before.

### Render workflows with the platform's dependency graph (#89)

The parser change moved the server to the platform's logic but left the
viewer drawing its own SVG, so a workflow still looked nothing like it
does in ACTIVATE. The DAG tab now renders DependencyGraphPreview from
@parallelworks/ui, which takes the workflow document directly, and an
Inputs tab lists what a run asks for, from the same parser.

Also adopts CopyToClipboard for the two values every caller has to
paste elsewhere: the /v1 endpoint URL and the registered catalog model
ids. The brand icon becomes a setting (URL or path) rather than an
environment variable only, so branding changes without a redeploy.

### Keep the open conversation in the URL; size the workflow graph properly (#90)

A refresh dropped you into an empty chat because the open conversation
lived only in component state. It now lives in the hash, so refreshing
returns to the same session, back and forward move between
conversations, and a link opens the one it names. The view router
leaves that hash alone rather than clearing it.

The workflow graph's chrome sizes itself with full-height children,
which need a parent whose height resolves; our container offered only
a min-height, and our own SVG rules were being applied to the
platform component's internal SVG, which broke its coordinate space.

### compose workflows (#91)

- **Compose workflows into a chain of subworkflows**
- **Keep the workflow graph on the platform's definition view**
- **Give the shared components a border colour**

### Fit the workflow graph inside the chat embed (#92)

The graph carried a minimum height of its own, which inside the chat's
embed frame pushed it past the bottom and clipped the lower jobs; the
frame sets the height there, so the minimum is dropped and the frame
is a little taller. Padding above the canvas keeps the first row of
jobs off the tab strip when the view resets.

### Order the two Tailwind builds so chat styling wins (#93)

Adopting components from the UI package brought a second Tailwind
build into the bundle, and utilities from two builds collide at equal
specificity, so whichever loads last wins. The UI package was landing
after the chat package and its base grid-cols-1 overrode the chat
package's responsive md:grid-cols-2, which is why the starter prompts
went from two columns to a single stacked column.

All three stylesheets are now imported in one place in a deliberate
order: the UI package, then the chat package, then ours.

### Configure deployment models under require-personal-key; keep the endpoint (#94)

Turning on require-personal-key emptied the model dropdowns in
Settings, because they asked the same endpoint chat does and it
refuses without a personal key. The deployment's own choices (vision
model, RAG default) still have to be configurable there, so Settings
asks with config=1 and that listing falls back to the deployment
credential; chat is unchanged.

Image captioning runs during background indexing with no user present,
so it can only use the deployment credential or the host's pw CLI
login. Settings now says so when a vision model is chosen and neither
exists, rather than leaving captioning to fail quietly.

The web endpoint launches with --keep, so the URL survives the
process exiting and a shared link keeps working across a relaunch.

### Declare the compose tool, and show what every tool calls (#95)

compose_workflow had its implementation and its description but no
entry in the tool list, so the model was never offered it and it never
appeared in Settings. It is declared now and grouped with the other
workflow tools.

On that page, what a tool actually runs is the part worth reading, so
it sits under every row instead of appearing only after a click and
below the wire schema. The schema stays behind the name, without the
description repeated inside it.

### Run a turn's tool calls together; stop the stream yanking the view down (#96)

A broad question makes the model ask for many files at once, and we
ran them strictly one after another, so the reply took as long as
their sum: a question about every in-process proposal issued nine
reads in a single turn. They are independent, so they now run
concurrently, capped at six, reported in the order asked, with a
failure isolated to its own call instead of ending the reply.

Scrolling up during a stream snapped back down because the scroll
handler re-armed stick-to-bottom whenever the view was within 150px of
the end, which a single streamed chunk immediately satisfied. The
threshold is now 24px, so only genuinely sitting at the bottom keeps
the view following. The chat package patch is regenerated against the
pristine package and verified to reapply on a clean install.

### Stop the theme flashing before the configured one appears (#97)

Caching the theme for first paint was not enough: the cached style is
inserted before the bundle's stylesheet, and on equal specificity the
later rule wins, so the shared packages' stock values painted first and
were replaced once the app applied the theme itself.

The emitted rules now use doubled selectors, which outrank the plain
:root rules those packages ship regardless of load order. Sampling the
tokens across a load shows one state throughout, with no stock blue at
any point.

### One rail treatment across the views, and a cleaner load (#98)

Chat, Library, Settings, and Help each drew their left rail
differently: the chat rail sat a step off its card while the others lay
flat, and their dividers used a darker token than the chat's, so the
pages read as different designs. They now share one rail background and
one divider colour. Dark mode keeps the two-tone, which is where the
separation needs help; light mode stays flat white like the cards,
where the border alone is enough.

The Library divider also drew its line at the far edge of a five-pixel
drag strip, letting the page background show through beside it, which
read as a thicker seam than the chat's; the strip now carries the
rail's colour.

Loading settles in one step rather than assembling itself: the header
paints the deployment's name and icon from the previous visit instead
of flashing a generic one, and the footer waits for its first read to
finish and appears complete, holding its height meanwhile.

Also: page titles on Settings and Help are heavier and in the text
colour rather than a thin accent line, and six hardcoded light
backgrounds are themed, including the saved-query chip whose label was
unreadable in dark mode.

### Serve brand images through the app, so a platform URL works (#99)

A platform blob URL needs a credential, and the browser cannot send one
to another origin, so pointing the icon at an ACTIVATE blob produced a
401 and no image. Brand images are now always served from our own
endpoint: a configured URL is fetched by the server, without a
credential first and with the deployment's if that is refused, and the
bytes are handed back and cached briefly. Staged files behave as
before.

### Stop re-downloading the embedding model on every run (#100)

The model was cached inside the app tree, which the build deletes and
recreates each run, so the guard never found it and every update
fetched it again. It is cached beside the tree now, next to the GUFI
source and the Node runtime, which survive for the same reason, and
copied in after the rebuild.

### Allow a separate brand image for dark mode (#101)

A mark drawn in dark ink vanishes against a dark background, so the
deployment can now configure a second image (a setting beside the
first, or APP_ICON_DARK). The interface picks by the theme in use, for
the header mark and the browser tab alike, and switching theme swaps
both; a deployment that sets only one image keeps using it for both.

### Hovers follow the accent; folders can be created at the top level (#102)

Choosing a green accent left blue-grey hovers behind, because the
platform derives hover as a neutral tint and several of ours were
hardcoded blues: dark-mode row and sidebar hovers, focus rings, drop
targets. Hover is now mixed from the accent over the background, and
the hardcoded values read the theme, so one choice moves the whole
palette rather than part of it.

Right-clicking the empty space in the Library now opens a menu for the
corpus root, which is the only way to create a top-level folder; it
offers just that, since labelling or deleting the root is not
something to invite.

### Seed an empty knowledge base, and a classification banner when the app stands alone (#103)

A new deployment used to open on an empty tree. The app now creates a
starting layout the first time it finds the knowledge base missing or
empty: papers, software, datasets and reports, each labelled, plus a
README naming the deployment and explaining how material gets in. It
only ever runs on an empty corpus, so it cannot disturb existing files,
and KB_STARTER_DIRS (the workflow's Starter Directories input) chooses
the names or, set empty, asks for the README alone. The workflow no
longer creates those directories itself, so there is one implementation.

The banner is the marking the platform draws above an embedded session,
e.g. the IL5 CUI line. Opened in its own tab the app carried no such
marking, so it now draws its own, with the text and colour set in
Settings or from BANNER_TEXT and BANNER_COLOR at deployment. It is
suppressed inside the platform frame, where the marking is already on
screen, unless a deployment asks for it in both places. Text colour is
computed from the background so a light colour stays readable, and the
banner is remembered with the rest of the branding so it paints on the
first frame rather than appearing a moment late.

### Settings survives a config payload without the banner fields (#104)

The web bundle and the server are briefly skewed during a redeploy: the
new bundle loads against the old server, which sends no bannerColor, and
form.bannerColor.toLowerCase() threw before anything rendered, taking
the whole app down rather than just the field. Every banner field now
tolerates an absent value.

### Banner markings as presets, typed like the platform's banner (#105)

The color is a hex field rather than swatches, and a marking picker fills
both fields at once: Unclassified, CUI, CUI at IL5 High, Secret, Top
Secret, and Top Secret / SCI, with the platform's own values for the
first three. Everything stays editable after picking one.

Type now matches the platform banner (0.8rem, normal weight, 3px
padding), and the ink is chosen by WCAG contrast rather than a lightness
threshold, which was putting white on the orange Top Secret marking.

### Banner text sets in the system stack, like the platform's (#106)

The app body font is Geist, which sets heavier than the platform chrome
at the same weight, so the banner read bolder than the one it is meant to
match. The banner now uses the system stack with antialiased smoothing
and an explicit 400.

### A user can share their model key with everyone on the deployment (#107)

The case: a deployment with no credential of its own, where one person has
a key and wants the app usable by the group without each of them pasting
one. Model access grows a Share button that promotes the caller's own key
into the vault under a reserved subject, and gatewayKey() returns it ahead
of PW_API_KEY and the pw CLI credential, so every path that would
otherwise have no credential picks it up: chat fallback, keyless callers
to the /v1 surface, image captioning during a sweep.

Personal keys still win for whoever has one, and a deployment that
requires personal keys refuses the promotion outright, since a shared key
would never be reached there. A key bound to a custom provider is refused
too, because the deployment-credential paths always call this
deployment's gateway. Anyone verified can stop the sharing, on the
grounds that the person who started it may be gone when it needs to end,
and the UI names who shared it and says plainly that their quota carries
the group. Operators can turn the whole thing off in Settings.

The banner also inherits Geist rather than naming a family: the platform
sets --font-sans to Geist, so inheriting matches it by construction,
where the system stack set wider and Roboto narrower. Its three fields
(marking, text, color) now share one row instead of stacking.

### A refresh returns to the same page inside the platform frame (#108)

Navigation state lives in the hash, which a normal tab keeps in the
address bar across a refresh. In the platform frame it does not: the
parent reloads this app from its bare src, so every refresh landed on the
default chat view. The last hash is now kept in local storage and put
back before the first render, since components read location.hash while
initializing and replaceState fires no event that would correct them
afterwards. The app's own pushState and replaceState navigation records
itself on the way through, hashchange and pagehide cover the rest.

### A shared key satisfies a deployment that requires personal keys (#109)

The posture worth supporting is "everyone brings their own key, but one
person may donate theirs for the rest", which is the shape of a
deployment where the launcher has no key and another user does. Sharing
was refused outright under requirePersonalKey, and the button was hidden
with no explanation, so the feature was invisible exactly where it is
useful.

Promotion is now allowed in that posture, and the three gates that refuse
the deployment credential accept a shared key, since someone put it there
deliberately for these users rather than it being the operator's ambient
credential. The control is always shown when the deployment allows
sharing, disabled with a line saying to add a key first.

The tab icon also stops flickering: the favicon was applied only after
the configuration request returned, so every load showed the bundled
default and then swapped. It is remembered with the rest of the branding
and painted before React renders.

### Indexing survives filesystems with inodes past the 64-bit signed range (#110)

Uploading a PDF to a customer deployment reported failure while leaving the
file on disk. The file was saved; the indexing pass that runs before the
response died in enrich.py with "Python int too large to convert to
SQLite INTEGER". /shared on that cluster issues inode numbers above
9223372036854775807, which SQLite cannot bind as an integer, so every
upload into that corpus failed the same way while the same file uploaded
cleanly elsewhere.

The inode now goes in as text, in words and in gchunks, which is what
every consumer already reads: the search and query joins are
inode=CAST(tinode AS TEXT). Verified by reproducing the overflow, then
running enrich.py and the search join over a fresh index.

Two related faults from the same log: an upload whose indexing fails now
returns the saved files with the indexing error attached, since a file on
disk has been uploaded and the interface should not call it failed; and a
directory that does not exist yet (chat-sessions before the first chat)
lists as empty instead of returning 500.

### Bulk upload shows what it is doing, and the index has one tree again (#111)

Indexing ran inside every upload request, so a drop of five documents held
the request open for the whole pass with the bar at zero: the files were
on disk the entire time and the interface could not say so. Uploads now
send in batches of four with byte-level progress from XHR, naming the file
in flight, and ask for a single indexing pass afterwards through
/api/index/job, polled as its own phase. Nothing about the corpus changes
until that pass finishes, which is the honest thing to show.

Separately, the full rebuild left the index one level too deep.
gufi_dir2index nests its output under the source basename, and reindex.sh
promoted the staging root rather than that directory, so the live tree was
index/gufi/<kb-basename>/... while the server writes its incremental
passes to index/gufi/<subdir>. A deployment that ran both ended up with
two parallel index trees over the same corpus. reindex.sh now promotes the
nested directory, verified by rebuilding a corpus with a subdirectory and
confirming the layout mirrors it with both files enriched.

### Reorganize the corpus by dragging, and select a range with shift (#112)

Files and directories can be moved by dragging a row onto a directory,
which is the missing half of a tree that could already accept uploads
there. A drag carries the whole multi-selection when the dragged row is
part of it. The server refuses a move into a directory's own subtree and
a name that already exists, and reports each refusal per path rather than
failing the batch.

What follows a move: both the source and destination subtrees are
re-indexed before the response, so search and the query builder see the
new location immediately. Labels ride along, xattr labels because they
belong to the inode a rename preserves, overlay labels because the
overlay is inode-keyed and the re-index refreshes the path it stores.
Extraction, PDF page renders, and parsed model caches are keyed by path,
so they are moved rather than dropped; re-extracting a large PDF costs
seconds and a move changes nothing about its content. Verified by
labelling a file, moving it, and confirming the label, the search hit,
and the label filter all followed.

Shift-click selects every row between the last clicked row and this one,
in the order they appear on screen, reading that order from the tree
itself so it matches what the reader sees.

### Bulk moves report progress, and a drag says what it is carrying (#113)

A move of a few hundred files held one request open with a one-line note
for company. Moves now run as a job with the same progress panel uploads
use: the count advances as files land, the panel switches to an indexing
phase while the corpus catches up, and refusals come back per path rather
than as a failed batch. Jobs are a small in-memory registry, read at
/api/jobs/<id>.

Dragging a selection shows a chip with what it is carrying, "12 items" or
the filename for a single row, in place of the browser's ghost of one
row. The selection can also be moved without dragging: "Move to…" in the
selection toolbar lists every directory in the corpus, backed by a new
/api/kb/dirs.

Shift-click now extends from whichever row was last clicked, including a
plain click that opened a file, which is what makes it behave like a file
tree rather than only working after a ctrl-click.

Verified with a 25-file bulk move: the job reported through to done, both
directories ended up correct, and all 25 hits came back at their new
paths.

### The library does what a file browser is expected to do (#114)

Right-click now offers rename, copy path, cut, copy, paste, and add to
chat, with Ctrl-X, Ctrl-C and Ctrl-V for the same three when focus is not
in a text field. Rename opens an inline box with the stem selected and
the extension left alone, and a file open in the viewer follows its new
name.

Rename and copy share the bookkeeping a move already had: derived caches
travel with a rename, both ends are re-indexed before the response, and
labels follow because they belong to the inode. Copy leaves the caches
behind to be rebuilt, since a stale copy of an extraction is worse than
none, and a copy landing beside its original takes "name copy.ext" rather
than refusing. Paths that cannot move or copy come back per path with a
reason.

Add to chat inserts the corpus path into the composer. That box belongs
to the chat package, so the text goes in through the native value setter
and an input event, which is what React notices from outside.

Office documents also index on hosts without python-docx, python-pptx or
openpyxl, which a customer cluster is: docx, pptx and xlsx are zipped XML,
so a standard-library fallback reads them when the library is missing. A
real proposal DOCX yields 10,840 characters of clean text with python-docx
blocked. Until now those files indexed as their filename and nothing else.

### Install the document extractors, and read scanned PDFs (#115)

The cluster had pdftotext and nothing else, so Word, PowerPoint and
Excel files indexed as their filename. Both deployment paths now carry the
libraries: the container installs python-docx, python-pptx and openpyxl
from Debian, and the from-source workflow builds a venv beside the app and
points PYTHON_BIN at it, warning rather than failing when there is no
network. The standard-library fallback stays as insurance.

An empty extraction was being cached, which is why installing a library
would not have helped on its own: the file was skipped on every later
pass. Only images cache an empty result now, where a picture with no text
is a real answer; a document that extracted to nothing is retried, so
material indexes as soon as the tooling to read it exists.

PDF text keeps its layout, which holds columns and table cells apart
instead of running rows together, and a PDF whose text layer is thin (a
signed or faxed form, which is a scan) has its pages rendered and run
through OCR. Measured on a real 12-page proposal: 17,478 characters
plain, 24,206 with layout, and the OCR path returns clean text from
rendered pages when it is reached.

A host without LibreOffice now says so when asked to preview a Word file,
in place of "spawn soffice ENOENT", and the Stats page lists any format
this deployment can only index by filename. The selection toolbar also
lays out for the narrow rail rather than breaking its labels mid-word.

### A remembered location no longer traps the bare URL (#116)

Restoring the last location from local storage meant every visit to the
top-level URL reopened whatever was last open, in any tab, forever. With
that file since deleted, the app opened on an error with no way back to
the top level.

The memory is per tab now, in session storage: a refresh keeps its tab, so
it still returns to the same page inside the platform frame, and a new tab
opens on the default view. A file that will not load also clears itself
from the memory and from the hash, and says it may have been moved,
renamed or deleted, in place of a bare error string.

### Propose labels for unlabelled material, and make uploads cancellable (#117)

Labelling by hand is the step people skip, so the vocabulary stays thin
and the filters stay useless. Right-click a directory and Suggest labels
reads the opening text of each file under it that carries no label of its
own or inherited, reuses the vocabulary already in the corpus wherever it
fits, and proposes new labels only where nothing existing describes the
material. Nothing is applied: the proposals come back as a review list
with a checkbox each, and accepting them runs through the ordinary label
path, grouped so identical label sets go in one call.

The assistant can do the same conversationally through a suggest_labels
tool, which returns proposals and is told to apply them with apply_labels
only once the user agrees. Both share one implementation, which normalizes
labels, drops anything the model invented outside the batch it was given,
and tolerates the JSON arriving inside a code fence. Exercised end to end
against a stub gateway.

Uploads can now be cancelled, and say afterwards how many had already
landed rather than pretending nothing happened. They also send three
requests at a time, with small files batched twelve at a time instead of
four: the cluster writes 5 MB in 0.09 seconds and its filesystem takes
0.06 of that, so an upload through the platform tunnel is waiting on round
trips, not on the server.

The container was also building without the extractors it was supposed to
carry. python-pptx is not packaged in bookworm, so the apt line failed as
a whole and installed none of the three, and the build reported success
anyway. It now installs docx and openpyxl from Debian, python-pptx from
pip at build time, runs with set -e, and imports all three before the
image is sealed.

### Label suggestions go through the call path the chat already uses (#118)

Suggesting labels posted a plain non-streaming completion with
temperature 0, which the providers behind this gateway reject: "An error
occurred while generating the response". It now runs through streamTurn,
the same call the chat makes, and collects the text, so any model that
works for chat works for labelling. Verified against a stub gateway that
answers as a stream, fences and all.

A refusal also says what to do about it now, naming the settings page and
whether a personal credential was in play, in place of a raw gateway 400.

### Labelling uses the model already in the conversation (#119)

Asking the assistant to suggest labels failed with "no model configured
for labelling", pointing at a setting on the RAG endpoint page that has
nothing to do with chat and is easy to miss. The tool now reads with
whichever model is answering the conversation, so there is nothing to
configure. The Library button, which has no conversation behind it, falls
back to the configured default and then to the first model the caller's
credential can reach.

The container recipe also gets the fix that was lost: the earlier edit was
made inside a backgrounded command and the branch rotation checked out
over it, so the image built again with an apt line naming python3-pptx,
which bookworm does not carry. The whole apt line failed, none of the
three extractors installed, and the build reported success. It now takes
docx and openpyxl from Debian, python-pptx from pip at build time, runs
under set -e, and imports all three before the image is sealed.

### Asking for the library opens the library (#120)

Navigating to #view=library reopened whatever document was last open: the
open file was still in state, so the effect that keeps the hash in step
rewrote it straight back to #open=file:..., and the plain library page
could not be reached by URL at all.

An explicit #view=library now clears the open document, and clicking
Library in the navigation while already there does the same, so a second
click closes what is open rather than doing nothing.

### Restore the index layout the server actually reads (#121)

A full rebuild was flattening the index: reindex.sh promoted the staging
root instead of the directory gufi_dir2index nests under the source
basename. The server reads $INDEX_BASE/gufi/<basename of KB_ROOT>, and its
incremental passes write into that same directory, so a deployment that
ran both ended up with a rebuilt tree nothing reads beside a partial tree
that the incremental passes kept alive.

The nesting is restored and verified: a rebuild over a corpus with a
subdirectory puts the tree where the server looks, with both files
enriched.

The symptom that led me here was real but had another cause, already
fixed: enrichment indexed only the corpus root because inodes on that
filesystem overflowed SQLite's signed 64-bit integers.

### Bring the architecture document up to what the code does (#122)

It had drifted far enough to be misleading: it described a single-user
deployment with no identity, no labels, no /v1 surface, no deployment
paths, and a file map missing two thirds of the server. It also stated the
index layout wrongly, which is worth noting because reading it is what
caught a regression I had shipped in the full-rebuild path.

Corrected: the index layout and what INDEX_BASE holds; the inode stored as
text and why; extraction as it now works, including the standard-library
fallbacks, layout-preserving PDF text, OCR for scans, and the rule that
only images cache an empty result. Added sections on labels and their
xattr and overlay stores, identity and the credential vault including
shared keys, the OpenAI-compatible /v1 surface, reorganisation and
background jobs, and the three deployment paths. The need-to-know section
now says plainly what platform identity does and does not separate, rather
than claiming the deployment is single-user.

### Plain language pass over the architecture document (#123)

Eighteen passages carried rhetoric where a fact belonged: "two hard-won
constraints", "the model in the driver's seat", "inspectable rather than
magic", "opens on something with shape", a heading built on an antithesis
("retrieval as tools, not context stuffing"), and several trailing
clauses that editorialised the sentence they followed. Each is now the
plain statement it was dressing up, and the count of "rather than" is
down to the places where it marks a real comparison.

### Design note for multi-user (#124)

docs/MULTI-USER.md collects where the multi-user work starts from and
where it goes: current single-service-user state, the four targets
(POSIX ownership on writes, permission management from the Library,
queries as the caller's uid, need-to-know in the index), GUFI's
permission-permutation external dbs as the mechanism for content
need-to-know with the two open questions, and a
sequencing where each step is useful on its own.

The working assumption is OIDC authentication (through ACTIVATE carrying
the CAC-authenticated user, or standalone against an IdP) with the
authenticated user holding a valid uid and gid on the cluster that holds
the knowledge base, so identity mapping is a lookup rather than a
provisioning scheme. Shared-user deployments without host accounts, the
current shared pattern, stay supported and labelled as what they are.

### Chat can launch model-serving workflows (#125)

A serve_model tool lets the assistant stand up new models when asked:
without launch=true it names the configured engines, shows the workflow
each one runs with its declared inputs and this deployment's presets, and
states exactly what would be submitted; with launch=true it submits the
run through the same pw CLI path run_workflow uses. The launch guard is
explicit in the tool description, so a model is only ever started because
the user asked for one in the conversation.

Engines and their presets are a deployment setting (serveWorkflows, JSON,
SERVE_WORKFLOWS env), defaulting to the vllm-endpoint workflow and the
marketplace Ollama one. A vLLM launch downloads and serves the model and
registers it in the platform catalog; the Ollama path serves an Ollama
plus Open WebUI session on Kubernetes, with models pulled inside it.

Proven against the real chain today: Qwen3.8-27B 4-bit serving from the
new vLLM 0.27 container on a 24 GB A30, registered with pw endpoints
--openai, visible in this Studio's model picker, native tool calling
verified.

### ai-chat 0.1.0 and ui 0.3.1, dist patch retired (#126)

The team's releases cover what we were carrying. The three issues filed
from this app are closed and fixed upstream: scroll position holds during
a stream (18664), the package CSS carries its own Tailwind utilities
(18665), and the composer queues messages while a reply streams (18666),
which arrives as a feature we did not have. Attachment onOpen is part of
the adapter surface now.

The dist patch is deleted. Its remaining customizations moved to the
typed config the package now exposes: the /?embed= image-to-iframe
override through markdownComponents, and the credential empty-state and
composer placeholder wording through strings.

serve_model's ollama engine now defaults to the registered bare-metal
ollama-endpoint workflow; the Kubernetes marketplace variant stays
reachable by configuration. Both engines were proven today as platform
workflow runs on a test cluster: vLLM serving Qwen3.8-27B 4-bit on the A30
(up in 85 seconds), Ollama serving qwen3:4b beside it on the second GPU,
both registered in the model catalog and visible in this deployment's
model picker.

### Fold the external-db design into the multi-user note (#127)

The GUFI external-db design supplies the mechanism the note was waiting for: permission
shards named by permutation inside the GUFI tree with their own file
modes doing enforcement, records keyed fsid.inode.mtime so versioning and
don't-re-extract fall out of the key, three tables per shard (status,
bm25, vec0 with per-chunk model provenance), staging through a spread
tree, and gufi_vt composing hybrid retrieval at query time with
permissions enforced by which shards each thread can open. Both standing
questions are answered: vec0 runs inside the shards in their production
schema, and derived caches become ext db records rather than files.

A multisite section records the direction the multi-user work should
build toward: virtual parquet as the structured-data sibling of the
Studio's enrichment, and cross-site federation as a fan-out of gufi_vt
queries over site-local indexes rather than copied corpora.

### The design note describes the design, not its correspondence (#128)

A public repository is the wrong place for who said what. The multi-user
note now presents the external-db mechanism as the GUFI design it is,
with no named people, organizations, or programs, and no pointers into a
private knowledge base. Technical content unchanged.

### A compute-node mode: the whole stack as one scheduler job (#129)

deploy/workflow-compute.yaml submits a single batch job that runs
everything on a compute node: optionally vLLM on the node's GPUs, the
Studio container beside it with its gateway pointed at that local model,
and the platform session registrations made from the node itself. The
pattern is Jupyter-on-compute for the Studio: the app comes up when the
job runs, points at a persistent corpus and index on the site filesystem,
and ends with the walltime, while session names, corpus, and index
survive for the next launch.

Container mode only, since compute nodes should not build software; the
login node stages both images from a bucket and downloads the model when
the directory is missing, using pip on the host rather than a container
because some login nodes exhaust user namespaces. The job script holds
the allocation while the app answers its health check, registers the web
session and optionally the model itself in the platform catalog, and the
submit step follows the queue and reports the session URL. Scheduler
specifics arrive as inputs (partition, account, qos, walltime, and extra
directives for systems that require an explicit GRES).

### Flatten the multi-user note to plain statements (#130)

Seven passages carried emphasis where a fact belonged: "covers exactly
this need", "falls out of the key", "worth tracking because they land on
our problems", "should not paint them out", "is the shape that follows",
"builds toward it rather than away from it", "the right treatment".
Each now states the mechanism or the constraint without commentary.

### State the current state as current (#131)

The multi-user note opens its status section with "currently runs as one
POSIX user, with the changes planned in this note not yet started", so a
reader cannot mistake the present for the design. The labeling module's
comment also drops its editorial framing for a plain statement of what
the pass does.

### The assistant can write files, and a long command is refused, not cut (#132)

Asking the assistant to generate a geometry produced a cascade of errors:
it had no way to write a file, so it tunnelled STL content through
cluster_command, and cluster_command silently truncated every command at
500 characters, so each attempt failed with a syntax error that pointed
nowhere and the model retried through printf, heredocs, and base64.

cluster_command now refuses a command over 8000 characters with a message
naming the limit, since a command cut mid-token is a different command.
And the tool the model was improvising exists: write_kb_file writes text
content into the corpus under the same path rules as uploads (excluded
and dot-directories refused, traversal refused, 2 MB cap, overwrite only
when stated), re-indexes the touched subtree, and points at
show_in_viewer for display. Verified: write, exists-guard, overwrite,
traversal and exclusion refusals, and the long-command refusal.

### The Activity drawer resizes, and the scope button steps aside (#133)

The package renders the thinking drawer as a fixed 400px right panel, and
its close button sat under our scope button when open. The scope anchor
now moves left of the drawer while it is open, driven by a :has selector
on the drawer's open state, so neither control covers the other.

The drawer is resizable by a drag handle on its left edge, the same
interaction as the conversations rail, with the width kept per browser
(280 to 700px) and applied over the package's fixed width by CSS
variable. The handle exists only while the drawer is open. Drawer content
is sized as a log rather than prose: 13px, with long unbroken tool
arguments wrapping instead of forcing a horizontal scroll.

### Large artifacts default into the site work filesystem (#134)

Models, containers, corpus, and index in the compute-node workflow now
default under ${WORKDIR:-$HOME/pw}: weights and images are tens of GB and
belong on the work filesystem, with home as the fallback where WORKDIR is
unset.

### Cap chat completions (#135)

Chat turns sent no max_tokens, so a reasoning model could generate to its
context limit before answering; at the 24 tokens per second an A30 serves
a 4-bit 27B in eager mode, that is minutes of silence that reads as a
hang. Turns now cap at 8192 completion tokens, CHAT_MAX_TOKENS to change.

### A launch cannot borrow the previous run's success (#136)

The compute-node job script now truncates its logs on start. The run
directory persists across launches, and the submit watcher greps
endpoint.log for the session URL, so a launch whose model server failed
could report the previous launch's URL as its own, which is how a broken
Gemma serve initially reported success.

### A fleet-status tool, and the scope button hugs the thread pane (#137)

hpc_status gives the assistant the deployment's HPC Status Monitor
(github.com/parallelworks/hpc_status) as a read-only tool: fleet summary,
per-system queue health, placement recommendations, allocation burn,
storage quotas and purge exposure, insights, and events, selected by a
view argument mapped onto the monitor's REST endpoints. The tool
description tells the model to consult it before recommending where to
submit or launch anything, which includes serve_model targets, so
scheduling advice can rest on live load and availability. The monitor's
base URL is a deployment setting (HPC_STATUS_URL); unset, the tool says
so instead of failing.

A tool rather than an MCP server on purpose: the assistant's integration
surface is its tool loop, the studio has no MCP client, and one
first-party REST API does not justify building that subsystem. The tool's
shape (view onto endpoint) migrates cleanly if an MCP client arrives for
broader reasons.

The chat scope button now hugs the thread pane's right edge while the
Activity drawer is open, tracking the drawer's resized width, in place of
floating between panes.

### The status tool can deploy the monitor it reads (#138)

hpc_status with launch=true runs the deployment-configured monitor
workflow (hpcStatusWorkflow, HPC_STATUS_WORKFLOW; hpc_status by default), so a user without a running
monitor can ask for one instead of being told to go deploy it. The
not-configured message names the option, launching is gated on the user
having asked, and after the monitor serves, its URL goes into Settings
for the read views.

### Drawer state observed from React (#139)

The scope-button offset and the resize handle keyed on a :has() selector
over the package's utility classes, which did not hold up in the deployed
build. A MutationObserver in ChatView now mirrors the drawer's open state
onto a data attribute on the canvas, and the CSS keys on that.

### A turn cannot end in silence, and Qwen answers instead of thinking (#140)

Reproducing the "no response" against the studio's own stream route
showed the shape: 69 seconds, fifteen tool call and result pairs, done,
and not one text event. The gateway strips reasoning content from
session-model streams, so a Qwen turn's thinking is invisible dead time,
and a turn whose answer lands in the stripped channel finishes with
nothing visible at all.

Two changes. Chat turns now carry per-model chat template kwargs from a
setting whose default disables Qwen thinking (chat_template_kwargs
enable_thinking false, verified honored both directly and through the
gateway), so those models answer immediately. And a finished turn with no
visible text now says what happened, naming reasoning-without-answer or
the finish reason, in place of rendering silence.

The gateway's stripping of reasoning content is the underlying defect and
belongs to the platform; it is being filed against core.

### Small-window models get trimmed history, not a 400 (#141)

The instrumented reproduction closed the case on the silent Qwen turns:
with thinking disabled the loop runs fast, and by the sixth round the
prompt (system context, 24 tool schemas, five rounds of tool results at
up to 24k characters each) exceeds the model's 16k window. The serve
answers 400 Bad Request, the gateway forwards that as an empty 200
stream, and the studio rendered the nothing it received; the empty-turn
notice from the previous fix now names it instead.

The loop now trims to fit: a chatModelWindows setting maps model
substrings to window sizes (default marks qwen at 16384), and before each
turn the oldest oversized tool results are stubbed out until the estimate
fits window minus completion budget. Models without an entry are
untouched.

The gateway swallowing an upstream 400 into an empty success stream is
its own defect, filed alongside the reasoning-stripping issue.

### An empty turn forces the final answer (#142)

With the window trimming in place the loop survived twenty rounds, which
exposed the remaining failure: some models end a tool loop by emitting a
turn with no tool calls and no text, and only the 24-iteration budget
triggered the forced no-tools answer. An empty turn now breaks to that
same forced answer immediately, so the model is made to conclude from
what it gathered instead of the reply ending in the empty-turn notice.

### Window budget covers tool schemas and adapts max_tokens (#143)

The empty replies on small-window models traced to under-counted
prompts: the estimate skipped tool-call arguments and the tool schema
JSON, used an optimistic 3.5 chars per token on JSON-heavy content, and
reserved a fixed 8192-token completion against a 16k window, so vLLM
rejected prompt plus completion with a 400 the gateway forwards as an
empty 200 stream. fitWindow now counts tool_calls arguments and schema
overhead at 3 chars per token, stubs older assistant prose when tool
results alone cannot free enough space, and sizes max_tokens to the room
actually left in the window. The forced final answer is budgeted the
same way.

### Qwen window default raised to the serve's 57k context (#144)

The A30 serve now runs Qwen at max-model-len 57344; measured KV capacity
is 77,414 tokens, so the earlier 16384 was a defensive quarter of the
card. The chatModelWindows default follows the serve, which gives the
tool loop room for long transcripts before any trimming and lets
max_tokens stay at the full completion budget.

### Window map covers gpt-oss at 65536 (#145)

The A30 serve moved from Qwen to gpt-oss-20b: MXFP4 weights leave the
card 248k tokens of KV, CUDA graphs stay enabled, and max-model-len is
65536. The window map gains a gpt-oss entry so long tool transcripts
budget against the real window.

### The closing turn is judged by its own content (#146)

gpt-oss narrates before its tool calls, so the accumulated-content check
let a reply finish on that narration when the closing turn came back
with no tool calls and no text. The guard now looks only at the closing
turn: no tool calls and no text of its own breaks to the forced final
answer.

### Low reasoning effort for gpt-oss and a forced-turn retry (#147)

The forced final answers were reaching vLLM and returning 200 with the
whole completion in the reasoning channel, which the gateway strips on
session-model streams (core#18693), so the studio saw silence. Two
mitigations that do not wait on the gateway fix: the template-kwargs
default runs gpt-oss at low reasoning effort, which reliably emits
final-channel text and is faster per turn, and an empty forced turn gets
one retry with a plain-text instruction before the fallback notice.

### Reliable answers from harmony-template serves, and a deploy watchdog (#148)

Wire captures against the A30 serve pinned four failure modes in the
gpt-oss tool loop. vLLM's incremental parser dies stochastically on some
sampled tool-call headers, surfacing as an SSE error frame in streams
and as a gateway 400 without streaming; malformed generations can leak
raw harmony control tokens as content; a no-tools "answer now" turn
under the deployment system prompt reasons about the missing tools
instead of answering; and a closing turn of brief chatter passed the
empty-turn guard.

The loop now runs gpt-oss models without upstream streaming (tool
activity still streams to the client; the gateway strips the reasoning
stream either way), retries generation failures in either shape, filters
harmony control tokens out of content, treats a sub-200-character
closing turn after tool use as a non-answer, and synthesizes the forced
final turn as plain question, gathered material, answer, under a minimal
system message. Six consecutive knowledge-base questions pass in 12 to
22 seconds each.

The deploy workflow now sweeps stale servers by working directory and
stale endpoint agents by session name on every launch, so a form rerun
always lands clean, and forks a watchdog that restarts the stack when
the app or agent dies.

### The chat keeps a valid, remembered model selection (#149)

Opening a conversation restores the model its last reply used, which can
be a serve that no longer exists (a stopped session model); the picker
shows "Select a model" but the composer still sends, and the request
fails. A guard inside the chat provider now swaps a missing or stale
selection for the remembered last model, falling back to the first
available one, and persists every valid selection for the next visit.
With the selection always valid while models exist, and the composer
already disabled when none exist, a message can no longer be sent
without an active model.

### The watchdog can actually restart what it kills (#150)

The first watchdog shipped broken in three ways that combined into an
outage: its restart command referenced a variable defined only in the
workflow step, so it expanded empty and every restart was "bash: : No
such file or directory"; the pid file was written with an over-escaped
literal so the relaunch cleanup could not kill it; and the agent
liveness check matched the agent by command line, which missed, so the
watchdog declared the stack unhealthy every cycle, killed it, and could
not bring it back.

The restart path is now the script's own absolute path captured from
$0, agent liveness is a recorded pid file instead of a command-line
pattern, the pid file and kill lines use correct quoting, and the
relaunch cleanup sweeps watchdogs by working directory as well as pid
file so a stale one can never fire mid-deploy.

### Portable GUFI resolution, and no exec detail in API errors (#151)

A local run without GUFI_BIN set failed with the raw spawn error for
the hardcoded /opt/gufi path, and that message reached the browser's
network tab. Two problems, two fixes.

GUFI_BIN now resolves like PYTHON_BIN: the env override first, then a
gufi-install directory beside the app, at the repo root, or under the
current directory, then gufi_query on PATH, then /opt/gufi as the
container default. A local checkout with GUFI installed in the work
directory needs no configuration.

Error responses no longer carry internal failure text. The central
handler returns a generic message with a correlation id and logs the
detail server-side; child-process messages hold spawned paths, stderr,
and command lines, and stderr can hold anything the tool printed, which
is not client material. Chat stream errors pass through only the
deliberate gateway messages (credential guidance, upstream HTTP
errors); everything else is logged under the same kind of reference id.

### The sanitizing error handler covers every route (#152)

Verification of the error hygiene change caught a scope gap: a Fastify
error handler set inside one plugin does not apply to routes registered
by the others, so the query route still returned the raw spawn message
in Fastify's default error shape. The handler is now registered at the
app level ahead of every plugin, and the forced-failure test confirms
the previously leaking route answers with the generic reference-id
message while the log holds the detail.

### fix: a missing index blanks the app, and the gateway does not follow the pw context (#153)

## What happened

A fresh clone (`pnpm i && pnpm dev`, no GUFI, no index) rendered a blank white page. The chain:

1. `POST /api/query` was the one KB endpoint with no `gufiAvailable()` guard, so it spawned the absent binary and 500'd with `spawn /opt/gufi/bin/gufi_query ENOENT`.
2. `OverviewView` did `r.json()` on that error body and stored it as a query result, then crashed on `largest?.rows.map(...)`.
3. With no error boundary, React unmounted the entire tree — white page, even though every view except Query/Stats would have worked.

Separately, chat failed with a gateway 401: `GATEWAY_BASE` defaults to a hardcoded `activate.parallel.works`, and `credentialFromCliStore()` scanned identities by that host — ignoring the pw CLI's active context entirely. With a current context on another platform host (e.g. canary), Studio used a stale activate token instead of the credential the CLI itself would use.

## Changes

- `server/src/query.ts`: `/api/query` answers 503 `index not built yet; run indexer/setup_gufi.sh and indexer/reindex.sh` when GUFI is unavailable, matching how search degrades (`routes.ts` returns `{hits: [], error}`).
- `web/src/views/OverviewView.tsx`: only payloads with a `rows` array are treated as results; error bodies are ignored instead of crashing the tree.
- `web/src/views/QueryView.tsx`: surfaces the server's `message` (Fastify error shape) so the Query view shows the friendly text instead of "Service Unavailable".
- `server/src/config.ts`: when no `PW_GATEWAY_URL`/`OPENAI_BASE_URL` is set, the default gateway derives from the CLI credential store's active context (`PW_CONTEXT` env, then `currentIdentity`), falling back to activate.parallel.works. Read once at startup like the rest of the module, so switching contexts needs a server restart.
- `server/src/chat/gateway.ts`: the credential lookup prefers the active context's identity when its server matches the gateway host; several identities can share a server, and the current one is what the CLI would use.

The context-resolution order (`PW_CONTEXT` > `currentIdentity`) and the credential file schema (`identities` keyed by context name with `server`/`token`/`apikey`) mirror `cmd/cli/lib/client.go` in core.

## Verified

- `pnpm build` passes (web tsc + vite, server tsc).
- On a GUFI-less host: app renders, Stats shows placeholder tiles, `/api/query` returns the 503 message.
- With a current context on canary and a stale activate token in the store: `/api/ai/health` went from `401 auth` to `ok` with 13 models.

### Browser-held personal keys, and turns retry without a rejected token cap (#154)

Two commits: personal model keys persist as sealed Secure cookies (HttpOnly, SameSite=Strict) with the server holding them in memory only and the vault scrubbed of personal entries at boot; and chat turns retry without the token-limit parameter when a provider (codex) rejects it, with the discovery held for the rest of the reply. Plus README sections for macOS local runs and the Apptainer container build.

### No Studio RAG model in any Studio's picker (#156)

The picker filter matched only the deployment's own RAG endpoint by
name, so studio-agent published by a different Studio deployment on the
same account still appeared. Session models tagged studio-agent or
studio-rag are now dropped from the chat picker regardless of which
deployment published them; external consumers still see them through
the endpoint's own catalog, which is what the advertising settings
govern.

### docs: add AGENTS.md requiring conventional commits (#155)

Adds an AGENTS.md documenting that this repo uses [Conventional Commits](https://www.conventionalcommits.org/) for commit messages.

### chore: require a 24h release age for dependencies (#157)

## Summary

Sets pnpm's `minimumReleaseAge` to `1440` (24 hours) in `pnpm-workspace.yaml`. New versions of any dependency — direct or transitive — must have been on the registry for a day before pnpm will install them. Most compromised releases are spotted and pulled well inside that window, so the delay costs us very little and removes the "install within minutes of a bad publish" case.

`@parallelworks/*` is excluded, so our own packages remain immediately installable and internal release-and-consume loops don't stall for a day.

## Changes

```yaml
minimumReleaseAge: 1440
minimumReleaseAgeExclude:
  - '@parallelworks/*'
```

## Notes

- `minimumReleaseAge` landed in pnpm 10.16 and glob patterns in `minimumReleaseAgeExclude` in 10.17; this repo pins `pnpm@10.33.4`, so both are supported.
- Existing resolutions in `pnpm-lock.yaml` are unaffected — this only constrains what future resolutions may pick up.
- If a third-party hotfix is ever needed sooner than 24h, add a version-scoped entry (e.g. `some-pkg@1.2.3`) to the exclude list.

### chore: upgrade to pnpm 11 and Node 22 (#159)

## Summary

Moves `packageManager` to `pnpm@11.22.0`. pnpm 11 drops Node 18–21, so everything that pinned Node 20 moves to 22 with it. `engines.node: ">=22"` plus `engineStrict: true` turns a too-old runtime into a clear `ERR_PNPM_UNSUPPORTED_ENGINE` instead of a confusing pnpm failure.

`pnpm-lock.yaml` is unchanged — pnpm 11 reads the existing lockfile as is.

## Changes

- `package.json`: `pnpm@10.33.4` → `pnpm@11.22.0`, plus `engines.node: ">=22"`.
- `pnpm-workspace.yaml`: `engineStrict: true`.
- `deploy/workflow.yaml`: fetches Node v22.23.2 and installs `pnpm@11`. The Node check now compares the major of an existing `node-runtime` rather than trusting its presence — that directory is prepended to `PATH`, so a v20 one left by an earlier run would otherwise shadow a good system node. Same for pnpm: a leftover pnpm 10 is replaced, not kept.
- `deploy/make_bundle.sh`: bundled runtime → v22.23.2.
- `deploy/app.def`: both stages → `node:22-bookworm`, `pnpm@11`.

## New pnpm 11 defaults worth knowing

`minimumReleaseAge` now defaults to 1440, which is what #157 set explicitly — the setting stays for the `@parallelworks/*` exclude and to keep it visible. Also newly on by default: `strictDepBuilds` (our `allowBuilds: esbuild` already covers the only dep that builds), `blockExoticSubdeps`, and `verifyDepsBeforeRun: install`.

That last one has one consequence: `pnpm deploy --prod` records a production-only install in the workspace state, and the next `pnpm run <script>` in that checkout then tries to reinstall — aborting outright without a TTY. The deploy paths all run `pnpm deploy` last, so they're unaffected, but `deploy/make_bundle.sh` left a developer's checkout in that state, so it now restores it with a `pnpm install --frozen-lockfile`.

## Verification

Run locally against pnpm 11.22.0:

- clean `pnpm install --frozen-lockfile` from an empty `node_modules` — passes, lockfile untouched, supply-chain verification passes
- `pnpm build` from clean — web and server both build
- `node server/dist/main.js` — boots and serves `GET /` → 200
- `pnpm dev` — vite and the server both come up
- `pnpm --filter @activate-studio/server --prod deploy --legacy <dir>` — produces a runnable tree, `--legacy` still supported
- the rewritten Node/pnpm selection logic exercised against stale-v20, good-v22, missing-pnpm, and pnpm-10 cases

Not exercised here: the Apptainer build and an actual ACTIVATE deploy — those need the platform.

### chore(deps): update @parallelworks/ui to 0.5.0 and @parallelworks/ai-chat to 0.2.2 (#158)

Updates `@parallelworks/ui` ^0.3.1 → ^0.5.0 and `@parallelworks/ai-chat` 0.1.0 → 0.2.2. No new dependencies; the app builds with a single ui 0.5.0 in the graph.

## Verified

- `pnpm build` passes (web tsc + vite, server tsc); no adapter API changes needed.
- Runtime smoke test on the dev server: app renders, model selector populates from the gateway, and a chat message streams a reply end to end.

### fix(deploy): keep the old app serving until the new build succeeds (#160)

The stop-prior-processes step ran in the setup job, before the clone,
build, and index, so any build failure left the deployment dead instead
of on its previous version; a transient GitHub connection reset from
a customer cluster did exactly that. The step now runs as the first step
of run_app, after every build stage has succeeded, so a failed rollout
leaves the old app serving. Port collisions cannot occur: each launch
picks fresh free ports.

### feat(chat): stream and persist tool calls as message parts (#161)

## What

Tool calls currently show under **Thinking** in the Activity drawer because both ends put them there: the server folds pretty one-liners into `finalReasoning` (`routes.ts`) and the web adapter replays `tool` SSE events into `onReasoning` (`adapter.ts`). This switches them to structured message parts:

- **Server**: `tool` SSE events now carry a stable `id` and an `error` flag; the calls are persisted on the assistant message as `ToolCallPart`-shaped `parts` (short summary as the result); the reasoning stream stays pure model reasoning. `prettyToolArgs` and the reasoning-folding are gone.
- **Web**: the adapter maps `tool` events to `handlers.onPart` `tool_start`/`tool_end` deltas, which `@parallelworks/ai-chat` 0.4.0 renders as tool blocks (running → resolved) inline in the thread, matching the conventions used by other coding agents.
- **Deps**: `@parallelworks/ai-chat` 0.2.2 → 0.4.0, which carries the onPart plumbing and hybrid parts/content rendering (parallelworks/core#18751, released as 0.4.0).

Old conversations keep their tool lines inside stored reasoning and render exactly as before.

### fix(viewer): extracted text stands in when office rendering is missing (#162)

On a host without LibreOffice, opening a DOCX or PPTX ended in an
internal-error reference: the deliberate "soffice is not installed"
message was thrown as a plain Error on one render path, so the error
sanitizer masked it. OfficePreviewUnavailable is now a KbError, which
every route and the app-level handler pass through verbatim, and the
viewer does what its comment always promised: when page rendering is
unavailable it fetches the document's extracted text and shows that,
with the reason in a note above it.

### feat(deploy): office rendering via a LibreOffice container on bare hosts (#163)

Hosts without a system LibreOffice fell back to extracted-text previews
for Word, PowerPoint and Excel files. The deploy workflow gains an
optional LibreOffice Image input: when set and the host has no soffice
but has apptainer or singularity, the image is pulled from the bucket
and a wrapper is installed, and the server renders office pages through
it via a new SOFFICE_BIN override. The container image (app.def) now
carries LibreOffice too, so container-mode deployments render natively.

### fix(deploy): the office-render image is the studio image, not a new artifact (#164)

The LibreOffice input now steers to the Studio's own container image,
which carries LibreOffice as of the previous change, so one managed
artifact serves container-mode deployments and office rendering on bare
hosts alike. The pulled file is named office-render.sif locally to stay
role-descriptive whatever image backs it.

### feat(deploy): office rendering from staged LibreOffice binaries (#165)

The container detour is gone: the office input now takes an
office-runtime.tar.gz, the official LibreOffice Linux binaries repacked
as a relocatable directory, pulled from a bucket and cached once per
host exactly like the Node runtime, with no container runtime required.
The wrapper simply execs the staged soffice, and the SOFFICE_BIN
override in the server is unchanged. Verified by a headless DOCX to PDF
conversion from a bare extracted prefix. The container image keeps
LibreOffice for container-mode deployments, which render natively.

### fix(chat): the credential notice clears when a key is added in Settings (#166)

The chat fetched its model list once, and a deployment requiring
personal keys answered with the credential-required notice; adding a
key in Settings changed nothing the chat could see, so the notice
stayed until a full reload. Saving, clearing, sharing, or unsharing a
key now broadcasts a model-access event; the chat clears the notice,
refetches the list, and remounts its provider so the models and the
empty-state reflect the new access immediately.

### fix(chat): system prompt rides in the first user message for providers that discard the system role (#167)

One provider fixes its own system prompt and discards the one the request
carries, so the assistant briefing (what the corpus is, how the tools
work) never reached the model and questions about the knowledge base
drew blank answers. For models matching a configurable substring list
(default genai), every turn now folds the system prompt into the first
user message, the role that backend honors, applied centrally so the
tool loop and the forced final answer are covered alike.

### feat(chat): prompt-level tool emulation for backends that ignore tools (#168)

One provider's serving path passes the tools field through to
an API that discards it (core#18754), so tool calling was dead there
for those models. For backends on the system-fold list, the studio now
carries its own emulation: tool schemas ride the last user turn inside
an envelope protocol with a per-request nonce, the model's envelope
reply is parsed back into ordinary tool calls, tool results return as
user-role text, and envelope JSON is buffered so it never reaches the
client as chat content. Verified end to end against that platform's gateway:
the previously failing corpus question now runs three tool calls and
answers with citations. Delete this block when the gateway's own
emulation engages on the affected platform.

### feat: each deployment carries a mission statement the assistant honors (#169)

A Studio knew its mechanics but nothing about its purpose, so a model
guessed at what a program acronym stands for. Deployments now carry a
mission statement: one paragraph set on the workflow form, by KB_MISSION
env, or edited live in Settings, spliced into the assistant's system
prompt as operator-authored authority on what the deployment is for.
The base prompt also gains a standing rule against inventing expansions
for acronyms the mission and corpus leave undefined, and the emulation
envelope now names the write tool explicitly so backends stop replying
that they cannot save files they have a tool for.

### fix(settings): the mission field sits full width above the starter prompts (#170)

It had landed inside the icon-fields grid, squeezed to a column and
breaking the section's layout; it now renders as a full-width textarea
directly above the chat starter prompts.

### fix(deploy): the mission export survives apostrophes (#171)

The mission is free text and the generated export wrapped it in single
quotes, so a mission containing an apostrophe broke the launch script's
quoting and the deployment failed to start. The text now passes through
a quoted heredoc and printf %q, which is safe for anything an operator
types.

### feat(deploy): the compute-node workflow carries the mission too (#172)

The mission input and KB_MISSION env reach the self-contained
compute-node deployment the same way they reach the standard one, so a
Studio launched whole on a GPU node knows its purpose as well.

### feat(viewer): sandboxed HTML embeds, model-write nudges, markdown source (#173)

Three viewer improvements from live use. Writing a 3D model or an HTML
page with write_kb_file now returns the ready-made embed markdown, so
the assistant shows the built-in interactive viewer and iterates by
rewriting the file instead of building its own viewer; the system
prompt says so outright. Corpus HTML pages render as a new sandboxed
embed kind and in the library: inline scripts and styles run with no
origin privileges and no network, which makes model-authored parametric
viewers safe to display. Markdown files in the library gain a Source
tab with the raw text for copying, alongside the rendered preview.

### fix(chat): a new chat lands the cursor in the composer (#174)

Clicking new chat left focus wherever it was; the composer textarea is
now focused so typing starts immediately.

### fix(chat): conversation history survives single-turn backends (#175)

One provider discards every message except the last user turn; a probe
with planted facts proved it answers multi-turn requests from account
metadata rather than the conversation, which is why a follow-up like
"can you make it longer" drew a request for clarification. For models
on the fold list, the entire exchange now flattens into that one user
turn: operating instructions, a labeled transcript declared as the
authoritative history, a continue cue, and the tool envelope. The
failing exchange replayed correctly end to end: the follow-up modified
the model file from the prior turn and re-embedded it.

### fix(chat): emulated backends ground answers and survive agent handoffs (#176)

Three hardenings from running the training battery through a platform provider.
Native tool calls arriving on the emulated path are the backend's own
machinery leaking (Gemini Enterprise emits transfer_to_agent handoffs);
they are intercepted and retried with a corrective note instead of
erroring as unknown tools. An unknown tool name now returns a guiding
result rather than a bare error either way. And the envelope
instructions require searching the corpus and citing files for any
knowledge-base question, closing the gap where the model answered a
corpus question from its own memory without citations.

### feat(chat): pre-grounded first turns for single-turn backends (#177)

Persuasion has a ceiling: the backend rationalizes textbook questions
as answerable from memory whatever the instructions say. The first turn
of a conversation with a fold-list model now arrives pre-grounded: the
server runs search_kb on the user's question itself, seeds the result
into the transcript, and surfaces the seeding in the activity feed, so
corpus material is in front of the model before it answers; greetings
skip the seed. The closing cue also carries the authority framing, a
first-reply envelope rule, and the exact citation-link format. A known
residual, twice reproduced: this backend still omits citation links on
questions it perceives as general knowledge even when grounded.

### fix(chat): tool-using turns report their duration again (#178)

The duration row renders only when a message carries reasoning text.
Moving tool calls into message parts left session-model turns with
empty reasoning (the gateway strips the model's own), so the "Thought
for" timing vanished. A turn that ran tools with no reasoning text now
records a one-line summary carrying the whole turn's elapsed time, so
the timing row is back without duplicating the parts display.

### feat(viewer): library previews HTML with source, and turns say Worked for (#179)

Corpus HTML pages now render in the library viewer under the same
sandbox the chat embed uses (inline scripts and styles, no origin
privileges, no network), with a Source tab alongside, matching the
markdown pattern. The turn-duration row reads "Worked for" via the
package's string override instead of "Thought for", which describes a
tool-running turn honestly.

### fix(viewer): the 3D canvas follows the theme (#180)

The renderer draws on a transparent canvas, so dark mode showed the
container's white behind the model. The canvas now carries a themed
radial background, slate in dark mode and pale in light, in both the
library viewer and chat embeds.

### feat(stats): corpus topology treemap and semantic map (#181)

Two views of the corpus from the index land on the Stats page. The
topology treemap lays out the directory tree squarified with area as
bytes, colored by top-level directory, click to zoom with breadcrumbs.
The semantic map mean-pools each file's chunk embeddings (the vectors
semantic search already computes), projects them to 2D with PCA,
clusters with k-means, and renders a canvas scatter where neighbors
read alike whatever folder they live in; hover names the file and click
opens it. The projection is cached beside the index and rebuilds when
any directory db changes, so both views stay one cheap fetch at any
corpus size.

### fix(stats): the corpus maps read at a glance, on a reorganized page (#182)

Feedback from first use, addressed structurally. The two maps leave the
card grid and span the page as full-width bands between the summary
tiles and the smaller cards, at proper heights. The treemap keeps one
hue per top-level directory with shade separating siblings, carries
file counts in its labels, and gains a clickable legend of top-level
directories. The semantic map names its clusters in the corpus's own
vocabulary (majority directory, computed server-side), draws soft
convex hulls behind each cluster, labels every point by filename on
small corpora, and lists the clusters in a legend, so the grouping
explains itself instead of asking the reader to decode colors.

### fix(stats): colliding cluster labels descend the tree until they differ (#183)

A corpus dominated by one directory labeled most clusters identically
(proposals, nine times over). Clusters that collide on a label now
descend a path level and relabel until they differentiate, so the same
map reads proposals/submitted/darpa-gpu and proposals/inprocess and so
on; centroid text trims to the last two segments while the legend keeps
the full path.

### fix(stats): the corpus maps sit side by side at modest, squarish sizes (#184)

Full-width bands overwhelmed the page; the two maps now share a
two-column row at near-square proportions, and their sections use the
same card and heading styles as the rest of the page so the titles
match.

### fix(stats): shorter corpus maps (#185)

The two-up layout reads better with less height: the treemap moves to a
3:2 aspect and the semantic canvas caps lower.

### fix(stats): map colors derive from the accent, panes trim further (#187)

The maps drew from a fixed rainbow that clashed with the deployment's accent scheme; their categorical palette now derives from the active accent as analogous hues with alternating lightness. Both panes also lose a little more height.

### fix(stats): the semantic canvas tracks its pane and both maps match height (#188)

The canvas measured its width once at first draw, so an early or stale
measurement left it smaller than its pane and window resizes never
redrew it; a resize observer now redraws at the pane's true width. Both
maps sit at the same fixed height so the two-up row reads as one band.

### fix(stats): the maps row keeps the page's uniform spacing (#189)

The stats page spaces its children with a flex gap and the maps row
carried its own top margin on top of that, doubling the gap above the
topology card; the redundant margins are gone.

### fix(stats): file names on the semantic map are hover-only (#190)

Per-point filename labels crowded the map even on small corpora; points
now identify themselves on hover while the cluster names stay drawn at
the centroids.

### feat(deploy): the compute form launches the CFD training studio as-is (#191)

The all-in-one compute workflow becomes the training package. A starter
knowledge base (the curated CFD corpus: cases, tutorials, manuals,
papers, geometry, branding) lives in the bucket and seeds an empty
knowledge base on first launch, never touching existing content. The
form defaults carry the proven configuration end to end, the Gemma 4
serve with its parsers and template, the CFD mission, prompts, labels,
icon, and the H100 scheduler settings, so a trainee picks a resource
and account and runs.

### fix(deploy): compute submission survives partial inputs and stale output (#192)

A CLI launch passing a partial scheduler group arrives with the unset
fields empty rather than form defaults, which submitted without a
partition, walltime, or GRES and the scheduler rejected it. The proven
training values now back every scheduler field in the submit step
itself. The job also writes a per-submission output file (with a
stable symlink), because the watcher previously read a prior job's
leftover output and mistook its state for the new run's.

### docs(deploy): the compute workflow gets real documentation (#193)

deploy/COMPUTE.md documents the compute-node workflow end to end: the
trainee quick start (three fields, everything else defaulted), what
each input group controls, operating notes including artifact refresh
and the stored-yaml push that the customer platform requires, and the
failure modes seen in practice with their meanings. The yaml header and
repo README point to it.

### fix(deploy): the compute form opens compact (#194)

Every input group starts collapsed except the scheduler, whose account
and QOS are the fields a first launch actually needs; the rest expand
on demand.

### feat(deploy): one workflow for login-node and scheduler placement (#195)

The Studio workflow gains a "Submit as a Scheduler Job" toggle that runs
the whole stack, optional vLLM serve included, as one batch job in the
resource's scheduler dialect, Slurm or PBS, following the marketplace
script submitter's header mechanics. The form shows the matching dialect
group only, with typed Slurm account, partition, and QOS inputs, and a
site configurations preset carries the CFD training package. The e2e
and cleanup session methods work in both placements. Existing records
without the toggle keep the login-node behavior unchanged;
workflow-compute.yaml is superseded and kept for records that still
reference it. COMPUTE.md documents the unified workflow.

### feat(deploy): generator for the turnkey CFD training workflow (#196)

make_cfd_training_yaml.py derives a training copy of the unified
workflow with the site preset baked in as plain form defaults and the
form opening compact; the output backs the cfd-studio platform record.
COMPUTE.md records the two pre-fill paths, the site partition shapes,
and the standalone model-serving workflows.

### feat(deploy): read shared images and corpora in place (#197)

A container image or starter tarball given as a path on the resource is
now used where it sits instead of copied into the per-user cache, so one
read-only copy serves a class of trainees rather than each launch
pulling 8 GB of images; bucket URIs still cache per user. The training
generator gains --shared-dir to point images, model, and corpus at that
copy.

### docs(deploy): what a class of trainees needs (#198)

COMPUTE.md gains a "Running a class" section: the shared read-only
staging directory and what it saves per launch, what stays per user, and
the exclusive-node capacity ceiling with the two ways around it.

### refactor(deploy): keep site specifics out of the repository (#199)

The training generator now reads a site preset file (resource names,
paths, accounts, model choice) that lives with the operator's notes
instead of a configurations block in the workflow, and COMPUTE.md
describes the mechanisms without naming a system, host, partition, or
filesystem path. The superseded standalone compute workflow is deleted;
records that still run it hold their own stored copy.

### feat(deploy): commit the CFD studio training workflow (#200)

deploy/workflow-cfd-studio.yaml is a generated turnkey copy carrying the
site's paths and scheduler settings as form defaults, so it can be added
straight from the repository as a remote workflow instead of each person
building a record by hand. The generator stamps its provenance and now
fills the dark-mode icon that ships in the starter corpus.

### feat(deploy): workflow thumbnails in the repository (#201)

deploy/thumbnails carries the Parallel Works mark for the general Studio
workflow and the CFD deployment's light and dark icons, all square 512px
PNGs, so a workflow record can point at a file in the repository instead
of a platform blob nobody can trace back.

### refactor(deploy): make the compute job script readable (#202)

The job script was assembled from placeholder tokens and rewritten by an
inline python pass, which made the part that actually runs on the node
the hardest part of the workflow to read. Launch values now go to a
%q-quoted job.env beside it, so the script is plain bash referring to
named variables, with one generated line pointing at those values. Free
text reaches the file through quoted heredocs, which also fixes a real
break when a name or mission contained an apostrophe, and the write step
syntax-checks what it produced.

### fix(deploy): the generated training workflow reads like the source (#203)

Parsing and re-dumping the workflow discarded every comment and rewrote
each run block as one escaped string, so the committed copy was the least
readable file in the repository. The generator now edits the source text
and touches only the lines whose values change, leaving comments, block
scalars, and the shell exactly as written.

### docs(deploy): where to look when a launch misbehaves (#204)

COMPUTE.md points at job.sh and job.env in the run directory, which are
what the node actually ran.

### feat(deploy): the training workflow launches on defaults alone (#205)

Account and QOS carry the site's values, so every field in the generated
training workflow now has a default and the only thing a trainee supplies
is the cluster. Anyone on a different allocation changes those two fields
as before.

### feat(web): tree previews for JSON and YAML, and consistent tabs (#206)

JSON and YAML files open as a collapsible tree beside a Source tab, with
multi-document YAML and parse failures both handled and long strings
shown as blocks. The tab bar is now built from the representations a file
actually has, so a text file with an unfamiliar extension gets the same
chrome as everything else instead of a bare rendering, and each tab names
what it shows: Tree, Rendered, Page, Source, Text, or Indexed text.

### feat: a timeline of what the corpus held (#207)

Each index pass now records a census snapshot beside the index, one row
per file with size, mtime, and inode, compressed to about 20 bytes per
file so thirty of them cost a few megabytes. The Stats page draws them as
a timeline and lists what changed between any two passes: added, removed,
changed, and moved, with moves recognised by inode so a reorganised
corpus does not read as wholesale churn. Snapshots describe the corpus
and do not hold file contents, so this answers what changed rather than
restoring anything.

### feat: a History view for travelling through the corpus (#208)

The timeline moves out of Stats into its own view: a scrubber down the
right edge selects a moment, the panel shows the knowledge base as the
index found it then with directories and files browsable at that point,
and the side panel lists what arrived in that pass. Older snapshots
recede behind the panel so the direction of travel is visible, and the
arrow keys move through time. A new level endpoint serves one directory
of a snapshot at a time so a large census is never shipped whole.

### fix(web): the History view reads like the rest of the app (#209)

It now opens with the same header card and explanatory line the other
views use, carries stat tiles for snapshots kept, files and size at the
selected point, and changes in that pass, and puts its two panes and the
scrubber in labelled cards. The scrubber lists passes newest first.

### fix(chat): survive a hardened backend refusing the emulated tool format (#210)

The emulation prompt told the model to disregard its own identity and
adopt a gateway protocol, which is what a hardened assistant is built to
refuse, and the user got the refusal instead of an answer. It now reads
as a reply format for a workspace that runs the lookups, with no identity
talk. The parser accepts the paraphrases these models actually produce, a
rewritten nonce among them, and validates the tool name against the tools
offered rather than trusting the nonce alone. A refusal that yields no
lookup retries once with a plainer request.

### fix(chat): shape the emulated prompt the way these backends answer (#211)

Measured against both Gemini Enterprise models rather than guessed: the
old prompt, which explained at length that the model should act as a
tool-calling agent, produced a usable lookup in 0 of 3 attempts on each,
answering instead that it has no filesystem access. Moving the required
output to the end of the message and asking it to pick an item from a
list, with no talk of identity or capability, gives 6 of 6 and 5 of 6.
Arguing the point makes it worse: a paragraph explaining that no
permissions are needed scored 0 of 3 on one model, because raising access
invites the objection, so that paragraph is gone.

One builder now assembles the emulated turn, replacing the flattener and
the instruction block, and it asks for prose with citations once lookup
results are present, which both models now do. Refusal detection also
covers the common "I do not have direct access" shape, scoped to the
opening of a reply so a caveat inside a real answer is not retried.

### fix(deploy): a shared copy falls back to the bucket behind it (#212)

Baking one site's paths in as defaults broke the workflow everywhere
else: a launch on another system reported the image unreadable and
stopped. Image, vLLM image, and starter bundle now accept several sources
separated by semicolons and use the first that works, so the shared copy
is tried first and the bucket picks it up on a system that has no such
directory. The training generator emits both, keeping whatever the site
preset already named as the fallback.

### feat(deploy): the training form reads as two steps (#213)

Someone opening this for the first time met seven groups of equal weight
and had to work out which mattered. The two they must touch now say so,
Step 1 for the cluster and Step 2 for the account and QOS, and the rest
are labelled Advanced, so the depth is there without reading as more
required work.

### fix(deploy): deleting the session releases the node (#214)

The job registered its endpoint with --keep and then held the allocation
open by polling its own health, so deleting the session left the Slurm
job running until the walltime expired. One left overnight burned nearly
seven hours of a whole node for a session nobody was using.

The Studio now runs under `pw endpoints run`, which owns the process tree
and tears it down when the endpoint goes, so deleting the session ends
the job. The model endpoint drops --keep for the same reason, a model
session outliving its job points at nothing, and the cleanup trap stops
its agent. The walltime stays as the backstop for a session nobody
deletes.

### fix(deploy): the corpus is actually indexed in container mode (#215)

The image tests INDEX_ON_START=1 exactly, and the workflow sent "true",
so the check failed silently and every container deployment served an
unindexed corpus: no GUFI tree, no vector search, and search quietly on
the grep fallback while the app looked healthy. The workflow now sends
what the image checks for, and the image accepts the obvious spellings
from the next build onward.

### fix(web): History matches the rest of the app (#216)

It read as a foreign page: a shadowed card nested inside another card,
uppercase letter-spaced labels no other view uses, and paths set in
monospace that wrapped raggedly down the change list. It now uses one
flat card like Query and Stats, the same heading weight and colour, the
app's own type, and truncating single-line rows. The receding stack of
older snapshots is gone; it was costing more in inconsistency than the
metaphor earned.

### fix(chat): name the model this deployment serves (#217)

A model served beside the Studio answers /v1/models in the plain OpenAI
shape with no provider fields, so the picker filed it under "unknown
provider" even though it was the only model there and worked fine. It is
now labelled by the engine serving it, "Served here (vLLM)" when the
gateway is on this machine and the engine name otherwise, and the window
vLLM reports is passed through so the picker can show it.

### feat(deploy): per-user session names, and no sessions left behind (#218)

The session name is also the URL subdomain, so a class all launching the
same workflow would have collided on it. Shell variables are now expanded
in the name and the training default is cfd-studio-${USER}, which gives
each person their own session, URL, and model endpoint.

The remaining --keep is gone too. A session that outlives its server is a
URL resolving to nothing, and it tells anyone reading the session list
that a dead deployment is running. The watchdog already restarts the app
and re-registers, which is what --keep was covering for.

### fix(deploy): the launcher gets its values, and the model endpoint is per-user (#219)

Running the Studio under the endpoint agent needed the invocation in a
script, but that script runs as a fresh shell: the first attempt left
$RUNTIME unexpanded and exec'd nothing, and pasting the values in instead
broke the starter prompts, whose JSON quoting does not survive a second
shell. The launcher now sources the same %q-quoted env file the job
script uses, with the runtime and gateway appended once both are known.

The model endpoint name is also expanded and slugified now, and the
training preset uses gemma-${USER}, so a class does not collide on it the
way they would have on the session name.

### fix: registering the RAG endpoint no longer ends the session (#220)

The Studio spawned the pw CLI to register itself as a platform model, but
a container deployment has no CLI inside it, and a spawn that cannot find
its command emits an unhandled 'error' event, so one click on that button
killed the server and took the session and its Slurm job with it. The
spawn failure is now handled and reported in the endpoint status, saying
what is missing and how to supply it, and the compute workflow binds the
CLI in so the feature works there rather than merely failing politely.

Loading is addressed too, since the app can take several seconds to
arrive over a slow link and a blank page reads as a broken deployment.
index.html now paints a starting indicator before the bundle lands, and
the 3D viewer loads on demand rather than in the entry chunk, which drops
it from 2.4 MB to 1.9 MB for everyone who never opens a model.

### fix: the CLI in the container, both model catalogs, and staying up (#221)

Fixes from the training session.

**The container never had the pw CLI.** The generated training yaml predates the bind that puts it there, so every platform tool (workflows, clusters, status) failed whichever model was driving, and registering the RAG endpoint spawned a binary that was not present. Three call sites also still hardcoded bare `pw` instead of the bound path, and a missing CLI was reported as `spawn pw ENOENT`, which reads as a command that ran and failed. The training yaml is regenerated so it carries the bind.

**The served model replaced the gateway instead of joining it,** so a deployment serving its own model could not see the platform providers at all. It is now a second target: both catalogs are listed together, a turn is routed to whichever owns the chosen model, and either side being unavailable leaves the other intact.

**Staying up.** The endpoint agent owns the process tree, so any exit removed the endpoint, ended the job, and killed the model serving beside it on the node. An unhandled rejection now logs instead of exiting, and the launcher restarts the container up to five times rather than letting one crash cost the allocation.

Also carries the 3D viewer fix: geometry goes to the browser as binary rather than JSON numbers. A 57 MB STL measured a 452 MB reply and 1.05 GB resident before, and at 76 MB `JSON.stringify` exceeded V8's maximum string length and threw. Now 41.2 MB and 251 MB, and the 76 MB case returns in 0.3s.

Verified: merged catalog lists a platform provider, a session model, and the local serve together; a turn naming the local model is routed to the local serve; a real model called `list_workflows` through the restored CLI and answered from the result.

### fix(deploy): the Studio image resolves from the bucket first (#222)

The shared containers directory on the training system cannot be written by the account that launches, so a stale image there cannot be refreshed and pins every trainee to it. The Studio image is also the one artifact that changes with each fix, so it now takes the bucket first with the shared copy behind it: a one-time pull per user, cached in their work directory.

The vLLM image and the model keep the shared copy first, since they are 8 GB and 59 GB and rarely change.

### fix(deploy): Refresh Software actually replaces a cached bucket image (#223)

The bucket cache is keyed by destination path alone, so once a copy existed it was reused whatever the source now held. A bucket-hosted image could never be updated for anyone who had launched before, which is the wrong default for the artifact that changes with every fix. Refresh Software now discards the cached copy before resolving.

### fix(deploy): Refresh Software is matched case-insensitively (#224)

The form sends a real boolean, but a CLI launch filling inputs by hand can send the string `True`, and a case-sensitive test turned that into a silent no-op: the launch reported success and kept the cached image. Caught deploying the new Studio image.

### fix: one entry per model, and endpoints removed when the walltime kills the job (#226)

**Duplicate model.** A model served here and also registered on the platform arrived from both catalogs and was offered twice. The direct entry is kept, since it answers without the gateway hop, and the gateway's copy is dropped. The match is on the endpoint name this deployment registered, passed in from the launcher, so an identically named model belonging to another session is left alone.

**Endpoints outliving their job.** The agent deletes its own session on a clean exit, but a walltime kill never lets it, leaving stopped records pointing at a node that had stopped serving. Cleanup now deletes both sessions by name, the trap covers TERM and INT rather than EXIT alone (EXIT does not fire when the shell is killed by a signal, which is how a job ends at its walltime), and the batch script asks Slurm for SIGTERM a minute early so cleanup has time to run.

### fix(deploy): pass the registered endpoint name to the Studio (#227)

Without it the duplicate-model match falls back to comparing model ids, which would also drop an identically named model served by a different session.

### fix(web): History takes its colour and type from the app's tokens (#228)

The view reached for `--border`, `--text`, `--muted` and `--hover`. This app defines the first two only inside `.chat-canvas` and the last two nowhere, so those declarations resolved to nothing: the pane rules fell back to the text colour instead of a light grey, and the scrubber ticks inherited rather than reading as muted. A green, an amber and a red each a shade off the ones the rest of the app uses, a 15px card heading where every other card uses 13px, and fractional 12.5px text finished the job.

Everything now comes from the platform tokens, so dark mode follows for free. Amber gained the dark step it never had: its only previous use was a 3px rule, where the light value still reads, and as text on the dark panel it does not.

### fix(web): History is assembled from the Stats page's own furniture (#229)

Matching the other pages by token was not enough: the view still carried its own split-pane card, its own row style, and its own heading sizes, so it read as a different product even with the right palette. The body is now built from the same pieces as Stats (card ov-list sections, 13px h3 headings, ov-row rows with the monospace path left and muted meta right), with the timeline as its own card in a right-hand rail. The bespoke CSS shrinks to the page grid, the change tallies, and the timeline ticks.

### feat: History shows what it costs to keep, and lets you clean it up (#230)

Snapshots were invisible as storage: the census files accumulated to the retention cap with nothing in the interface saying what they cost or any way to remove one. The listing now carries each snapshot's own size on disk and the total, shown in the stats tile and at the top of the timeline. Each pass gets a hover-revealed delete, the same pattern as removing a saved query, and a prune action keeps the last ten when the rail grows past that.

Deleting is safe by construction: a snapshot is a census, not content, so removing the newest one of a live corpus just means the next visit captures a fresh point. The id is numeric and validated, so the delete cannot be steered outside the history directory.

Verified against a running server: listing carries disk and storedBytes, deleting a middle snapshot removes exactly it, a traversal-shaped id gets 400, prune keeps the newest N and reports the count removed.

### feat(chat): a model that cannot be called says so (#231)

A provider's catalog listing succeeds on the registration alone, so a model whose key has expired still lists as available and the failure arrives at reply time as a raw gateway relay (`gateway chat 400: {json}`), which reads as a Studio fault.

Call outcomes are now remembered per model: an invoke-time failure marks the model, the next success clears it, and nothing is probed. The picker shows the mark on the model's name, and the reply-time error states what is known instead of relaying JSON: the model cannot be called right now, an expired platform provider key fails exactly this way when the model sits behind one, and other models are unaffected.

### feat(history): quiet days read as verified, and capture rides the sweep (#232)

A timeline with no new points was ambiguous: the corpus may be quiet, or nothing may be checking it. Two changes separate those.

Capture moves to the sweep, so an index change becomes a snapshot within one sweep interval with nobody watching; before this, a week of churn collapsed into a single point at the next visit to the History tab.

A sweep or visit that finds the index unchanged records a check entry, a timestamp rather than a census, at most one per calendar day. The timeline shows them above the snapshots as "checked, no change" ticks, so silence becomes distinguishable from inactivity at the cost of a line of JSON per day.

Verified on a running server: an unchanged pass records exactly one check, a same-day repeat does not duplicate it, and a changed index still captures a snapshot.

### feat: a phone layout, a test suite, and CI that says pass or fail (#234)

**Mobile**: one breakpoint at 760px, layout only. The navigation rail becomes an icon bar along the bottom edge, side-by-side panes stack, and the grids whose auto-fit floor (420px) overflows a 390px phone go single column. The library's resizable rail stacks above the listing with its desktop drag width overridden. Nothing changes above the breakpoint.

**Tests**: thirteen cases over the logic that has actually failed or would hurt most if it did: the 3D wire format round-trips exactly and stays aligned, history diffs tell a move from churn by inode, resolveKb refuses traversal (and the suite documents that absolute paths are root-relative), and the provider emulation shape is pinned.

**CI**: GitHub Actions builds the server, runs the tests, and typechecks and builds the web app on every push and PR. The check on the PR and GitHub's failure email are the notification; the README carries the badge.

### fix(web): the phone layout reaches the chat view (#235)

The first breakpoint never touched the landing view. Chat's layout comes from the ai-chat package with the conversations rail pinned at 260px by our own rule, resized only by a mouse drag handle, so a 390px phone showed mostly rail with the thread crushed beside it, and the Activity drawer could take another 400px.

Below 760px the rail is now a slide-over: hidden by default so the thread gets the whole width, opened by re-tapping the Chat item in the bottom bar, closed by picking a conversation. The drag handles are gone at this width, the Activity drawer stays closed, and the fixed-width label panel clamps to the viewport.

### fix(web): the bottom bar stays visible and inside the viewport on phones (#236)

The status footer rides inside the nav on desktop, so the bottom bar inherited it as a squeezed row item that overflowed the right edge of the viewport (the clipped username and model count in the report) and fattened the bar until the browser toolbar buried the icons. At phone width the footer is gone; its content lives on the Stats page one tap away. The app also pins to 100dvh so a dynamic browser toolbar shrinks the layout instead of covering the navigation, and the body refuses horizontal scroll.

### feat: the knowledge base as an MCP server (#237)

The RAG proxy publishes this corpus as a model, which locks callers to this deployment's serving loop and prompt. `POST /api/mcp` inverts that: an MCP client (pw code, or any MCP-capable tool) brings its own model and calls the corpus tools directly.

Stateless streamable-HTTP JSON-RPC, the shape an HTTP transport registration expects, behind the same bearer auth as every other /api route. Only the corpus-reading tools are exposed (search, read, list, query, labels, docs); writes and platform actions stay with the chat assistant.

Five tests cover the handshake, the exposed-set boundary, a real tool call, refusal of unexposed tools, and notification semantics.

### feat(web): a More sheet for the phone bar, system theme, and the Stats overflow (#238)

The bottom bar was carrying every destination plus the status footer, which overflowed the viewport and buried the icons. The bar now keeps the four main destinations (Chat, Library, Search, Stats) and a More button opens a sheet with the rest, Settings and Help included, plus the status footer.

Theme follows the device when the user has not chosen: stored choice, then prefers-color-scheme, then the deployment default, updating live. The system signal only ever adds dark, so a dark-branded deployment keeps its look.

Stats bars rode off the viewport edge because grid children refuse to shrink below content width without min-width: 0.

### feat: an MCP access section in Settings, and the bar holds the bottom (#239)

Settings gains an MCP access section beside the OpenAI endpoint and platform registration: what the endpoint is, a switch to serve or stop serving it (honored server-side with a 403, tested through the settings route), and copy-ready registration for pw code plus the generic mcpServers JSON, built from this deployment's own URL, with the Authorization header included exactly when the deployment enforces sign-in.

Also the remaining phone-footer case: desktop gives .view height:100% to fill the row beside the rail; in the phone's column layout that same 100% plus the bar overflowed the viewport, so scrollable pages carried the bar off-screen.

### feat: External access explains and toggles both surfaces, MCP first (#240)

The section formerly titled RAG endpoint is now External access, opening with a plain comparison of the two ways outside tools use this knowledge base: knowledge tools over MCP (the client brings its own model, our tools ground it) and the grounded model at /v1 (this deployment answers as a model with retrieval built in). MCP leads; the /v1 surface remains the right shape for OpenAI-only clients and the platform catalog.

Both are now real switches: ragProxyEnabled gates /v1 with a 403 while the call log stays readable, independent of mcpEnabled, with a test proving one can be off while the other answers.

### fix: the collapse button works, the drawer closes by tap, shared providers say so (#241)

The conversations rail's width, resize separator, and collapse animation belong to the chat package; our CSS pinned the width with !important over its inline style, which made the collapse button appear dead everywhere and left the phone slide-over showing a collapsed, empty shell. The override and our redundant drag handle are gone; desktop collapse and resize work as the package designed them. On the phone the package toggle is hidden and the slide-over closes by backdrop tap or the Chat item; the my-chats filter chip shows inside the open drawer.

A provider shared into the account lists with the same display name as one's own registration and looked like a duplicate; the picker now labels it 'shared by <owner>'.

### fix(web): sidebar resizing is back (#242)

The rail's first child is the package's resize separator when expanded, and our header-height alignment rule targeted div:first-child, clamping the separator's grab strip to a 52px sliver at the top, which read as resizing having disappeared. The rule now names the header row it always meant.

### fix(chat): the ownership filter refreshes the list instead of remounting the app (#243)

Toggling my-chats/all-chats bumped the provider epoch, which tore down the open thread, aborted any running stream, and rebuilt every piece of chat state just to refilter the rail; on a phone it read as the app breaking. The filter is applied client-side in the adapter's listing, so a FilterReloader inside the provider re-runs loadConversations on a filter-change event and nothing else moves. The epoch remount stays for credential changes.

### fix(chat): pass custom tool arguments as positional parameters (#233)

## Problem

Custom tools (Settings-defined and `extensions/tools/*.json`) were executed by concatenating the model-supplied `args` string onto the configured command:

```ts
const cmd = extra ? `${custom.command} ${extra}` : custom.command
execFile('bash', ['-lc', cmd], ...)
```

Because the result is handed to `bash -lc`, every shell metacharacter in `args` is parsed as syntax. An argument of `x; touch /tmp/marker` runs `touch` as a second command with the server's user and environment. A tool wrapper cannot defend against this by validating its own arguments, since the escape happens in the shell before the wrapper is invoked.

The exposure is largest for deployments where the assistant reasons over retrieved documents, because those documents are untrusted input that can attempt to steer an argument.

## Fix

Arguments are passed as positional parameters instead of being spliced into the command text:

```ts
execFile('bash', ['-lc', `${custom.command} "$@"`, custom.name, ...splitToolArgs(extra)], ...)
```

`splitToolArgs` is quote-aware so multi-word arguments still work, and does nothing else: no expansion, substitution, or metacharacter handling. Everything the model writes reaches the command as data.

| `args` | before | after |
|---|---|---|
| `--limit 5` | two arguments | two arguments |
| `--path "two words"` | two arguments | two arguments |
| `a; touch /tmp/marker` | runs `touch` | three literal arguments |
| `$(id)` | command substitution | one literal argument |

## Compatibility

Commands that read their arguments as `"$1"`, `"$@"`, or via `getopts` are unaffected. A command that relied on `args` supplying shell operators would change behaviour, which is the intent.

The bundled starter tools take no arguments and are unchanged.

## Verification

`splitToolArgs` checked against empty input, flags, quoted multi-word values, embedded `;`, `&&`, `$(...)`, empty quotes, and repeated whitespace. The `execFile` shape was confirmed to pass a `;`-bearing argument through as literal text with no second command run. `pnpm --filter @activate-studio/server build` passes.

Docs updated in `docs/CUSTOMIZATION.md` so tool authors know to expect `"$1"`.

### feat(history): one snapshot a day by default, with the cadence in Settings (#244)

Capturing on every index pass turned the timeline into a churn log and burned the thirty-point retention window inside a day. Snapshots are now taken at most once per `historyIntervalSec` (default 86400), so a point summarizes everything that changed since the previous one; 0 restores a point per pass. An unchanged index still leaves the proof-of-life marker either way.

Also a Studio icon that is not the generic company mark (corpus lines with a spark, legible at 28px in the phone bar), and the proposal dashboard's favicon moved onto the PW palette.

### feat(settings): platform switches, pw code first, and the two surfaces compared as a list (#245)

External access used raw checkboxes where @parallelworks/ui ships SwitchToggle, and its introduction packed both capabilities into one paragraph. The two surfaces are now a comparison list, one entry each, and the enable controls are the platform switches.

The MCP registration example leads with `pw code mcp add`, which signs its platform requests with the existing CLI login so the command carries no token; the generic settings JSON covers other clients, with the bearer header included exactly when the deployment enforces sign-in.

### feat: the Studio as an MCP client, so outside tools join the assistant (#246)

`/api/mcp` publishes this corpus to other agents; this is the inverse. A deployment can attach MCP servers somebody else runs (a CRM, a ticketing system, an internal service) and their tools join the assistant's own, so one turn can cross both.

Remote HTTP servers only (a stdio server would mean spawning processes named in settings). Listings cached for a minute; an unreachable server contributes no tools and never breaks the assistant's own; remote tools are namespaced under the server name.

Two properties stated in the UI because they are security decisions: attaching a server sends tool arguments to a third party, and a remote server's tool descriptions are text the model reads while choosing what to call, so they are untrusted input.

Tested against a live JSON-RPC fixture: handshake, session-id carriage, namespacing, schema preservation, call routing, status reporting, and unreachable-server tolerance.

### fix: the DAG viewer runs the bound CLI, and the Status Monitor finds itself (#247)

Two failures reported from a bundle-mode deployment.

**DAG 500**: both workflow routes still called bare `pw`. Three other call sites moved to the bound path earlier; these were missed, so a deployment whose CLI is not on PATH lost workflow listing and DAG rendering with an error naming neither.

**hpc_status**: required an operator to paste the monitor URL into Settings, so on a fresh deployment the tool reported itself unconfigured while a monitor ran in the same account. It now discovers one by listing sessions, preferring names that read like a monitor, and probing each for the fleet endpoint, taking the first that answers with JSON. Probing is what makes guessing safe: a platform login redirect answers 200 with HTML and is skipped. Configured URLs still win, and requests now carry the deployment credential.

### feat(indexer): extract documents to Markdown with downmark (Office + PDF, OCR) (#225)

## What

Document extraction moves to [downmark](https://github.com/giraffesyo/downmark) (`@giraffesyo/downmark` 0.10.0 — native binary per platform via optionalDependencies, wasm fallback). Office documents (`.docx`, `.pptx`, `.xlsx`, `.doc`) and PDFs extract to **Markdown**; scanned PDF pages are read through tesseract with downmark marking the pages OCR filled in. The Python Office readers are gone.

- `server/src/preextract.ts` — converts stale/missing entries into `$INDEX_BASE/extract/<rel>.txt` as a **forked child** before each `enrich.py` pass, and as a CLI from `reindex.sh`. PDFs are routed only where the native binary resolves (`binaryPath()`), with OCR budgeted to 30 pages / 2 min per page / 5 min per document and enabled only when tesseract is present. Conversion `warnings` are logged per file and counted. Entries refresh once per downmark version.
- `enrich.py` — reuses the cache; fallbacks: stdlib OOXML pass for Office, `pdftotext` (+ pypdf) for PDFs on wasm-only hosts. The thin-text OCR rule now also applies to *cached* PDFs (downmark OCRs only pages with no text at all, so a typed header over a scanned body needs it), and documents carrying downmark's OCR marker are not re-OCR'd.
- Deploy: no pip/venv/binary staging anywhere — `pnpm install` picks the platform package (verified it lands in the `pnpm deploy --legacy` layout); `app.def`'s proof step checks pdftotext, tesseract, soffice, and the downmark wasm; the workflow's Python venv step is removed; `requirements.txt` keeps only optional `pypdf`. `pnpm-workspace.yaml` exempts `@giraffesyo/*` from the 24h release-age gate, as it does `@parallelworks/*`.
- Viewer renders extracted Office text with Streamdown; extractor report lists downmark (covers `.pdf` when native).

## Why

- Extraction no longer depends on the host: no pip, no venv, no per-host binary staging — the npm install carries everything, and the lockfile pins the binary.
- Materially better output: pptx tables/charts/notes, docx headings/tables/equations, xlsx as GFM tables, PDF ligatures folded (giraffesyo/pdf#9), OCR'd pages marked so consumers can weigh OCR text differently.
- Markdown is the better shape for the chat context, FTS snippets and the viewer.

## Tests

`testdata/corpus` — 13 synthetic files (~75 KiB, no third-party licence, regenerated deterministically by `make-corpus.py`) covering every extracted format, plus a pure scan, a scanned body under a typed header, and a file whose extension lies. `pnpm test` runs the pipeline as deployed (preextract → enrich.py) and asserts on the text reaching the fts5 tables. Assertions are properties, not golden files, so converter formatting drift does not fail the build while dropped content does. `.github/workflows/ci.yml` runs it on Node 22 with tesseract and poppler — the repo's first CI workflow, easy to drop if you would rather add it separately.

Verified the suite actually catches regressions: reverting the OCR policy from `thin` to textless fails exactly one test, with "the scanned body under the typed header was lost: is the OCR policy still \"thin\"?".

## Verified

- `pnpm build` (server + web) clean; `workflow.yaml` parses.
- Native pass converts 6/6 (Office + text PDF + scanned PDF via OCR, `<!-- downmark: page N includes OCR text -->` marker present, 0 ligature codepoints); rerun reuses 6/6; `DOWNMARK_FORCE_WASM=1` skips PDFs (4/4 Office only).
- `enrich.py` over a fake GUFI tree indexes the Markdown from cache with 0 re-extractions; with no cache and no Python libs, its stdlib/pdftotext fallbacks still index everything; `ocr_if_thin` appends OCR for a thin cached PDF and respects the downmark marker.
- Fork path works under tsx (dev) and from `dist/` (prod); the platform binary resolves inside the `pnpm deploy --legacy` layout.
- Unreadable input is logged, counted as failed, exit 0.

## Notes

- Wasm-only hosts (unlisted platforms) lose nothing: PDFs fall to `enrich.py`'s pdftotext + OCR path, Office to the wasm.
- OCR runs under downmark's `thin` policy (0.10.0, giraffesyo/pdf#35): a scanned body under a typed header is OCR'd per page inside the converter. `enrich.py`'s thin-text re-check on cached PDFs remains only as a self-heal for entries written while tesseract was absent.

### fix: workflow listing and DAGs use the platform token, not the host CLI (#248)

The DAG viewer's 500 survived the CLI-path fix because the cause was not the path: the pw CLI on that login node has no context configured at all, which is the normal state of a host nobody has run `pw auth` on, and both workflow routes depended on it.

They now call the platform REST API with the deployment's own token, which the Studio already holds for the model gateway and which serves both the listing and each workflow's yaml. The CLI remains the fallback, and when neither works the error names the missing credential instead of answering 500 with a reference code.

### feat: a persona library in the corpus, and an Agents tab to grow it (#249)

A user with a folder of agent-persona markdown files had nowhere to put them. Two thirds of what they wanted already existed and was invisible: skills are task instructions the assistant loads itself by description, and `agents/default.md` is a standing persona. What was missing: only one persona could exist, none could be chosen per conversation, the library was deployment-local, and the only way to add one was dropping a file on the host.

Personas now merge from the corpus (`KB_ROOT/.agents`) and the deployment, corpus-first so a team shares one library that versions with the material it describes, deployment second so a name can be overridden. A turn names its persona and chat gains a Persona control; naming none keeps the standing default.

The Agents tab is where they are read and written, beside the Library rather than in Settings. Tests cover the merge, override precedence, frontmatter stripping, fallback, and that a crafted name cannot escape the persona directories.

### fix(chat): drop the stale rail width; feat: personas labeled, searchable, editable (#250)

**Layout regression.** Handing the rail's width back to the package left our copy of that width behind; the filter chip sized itself to it, so after any resize it floated at a width nothing else used. That state, its drag handler and its dead handle ref are gone.

**Personas** are labeled `agent-persona` on write and at startup, and a shared one is indexed immediately rather than at the next sweep. The prompt is told what the label means, including that retrieving a persona is reading a document about a stance, not adopting it, so one surfacing in search never changes behavior in that turn.

**Editing** reads a persona back into the same form that writes it.

### fix: workflow routes use the caller's platform token where the deployment has none (#251)

A bring-your-own-key deployment holds no credential of its own, so the platform-API path could not authenticate and the DAG viewer still failed there. The routes now try the requesting user's platform token first and fall back to the deployment's, the rule the chat tools already follow. A custom provider key carrying its own baseUrl is skipped rather than sent to the platform, and the error names Settings, Model access as the fix.

### fix(agents): the Persona control was invisible, and rows now go somewhere (#252)

The Persona control rendered with no positioning while the Scope button beside it is absolutely positioned over the package's header, so it landed somewhere in the canvas and could not be found. It now sits beside Scope, steps left of the Activity drawer the same way, and drops to its icon on a phone. It renders when the library is empty too, saying where to add one.

Agents rows used the shared row style (pointer cursor) while doing nothing on click. A row now opens that persona or skill in the form that writes it, skills included, with the corpus file reachable through a separate 'in library' link. Both sections say where their contents take effect.

### fix(chat): Scope and Persona share one control row (#253)

They were two absolutely-positioned elements with a hand-tuned offset between them, which is how the persona control landed somewhere nobody could find. One flex row holds both, so they cannot drift apart or overlap, and the Activity drawer shifts the pair rather than each separately.

### fix(chat): the control row is not full height (#254)

`.chat-canvas > *` forces `height:100%` on direct children, so the new row filled the canvas and `align-items:center` put Scope and Persona halfway down the page. The anchors it replaced each carried an explicit `height:auto` for this reason; the row now does too.

### feat(library): personas are browsable and rendered, and the editor fits a long one (#255)

The Library hid every dot entry, so `.agents` could not be browsed even though its files index and search like any other corpus material. Dot entries are hidden because they are machinery; this one is material a user writes and only lives under a dot to stay out of the way, so it is shown now. The viewer already renders Markdown.

Agent files run long, so the editor opens at 45vh rather than a 220px box, stays resizable, and offers to open the rendered file for reading.

### fix(agents): descriptions wrap instead of pushing the page sideways (#256)

`.ov-row-meta` carries `white-space: nowrap`, which suits the file size it was built for and not a description sentence. A long one ran past the card and put scrollbars on both axes. Descriptions now wrap and grid children are allowed to shrink, scoped to this view so listings elsewhere keep their single-line meta column.

### feat: libraries of sixty stay usable, in the prompt and in the interface (#257)

Every skill's name and description ship inside the `use_skill` tool description on every request: ~146 characters each, so sixty skills is ~2,200 tokens per turn whether or not a skill is used. Past twenty-five the descriptions are clipped to the identifying part, bounding the cost while keeping selection possible; the tool says so, and every skill stays selectable. Test asserts the description stays under 5,000 characters at sixty skills.

The interface gets the same problem visibly: the Agents tab filters both lists once there are more than eight definitions and shows how many of how many match, and the chat Persona menu gains the filter the Scope menu already had plus a scroll bound.

### feat(agents): page through long persona and skill lists (#258)

Filtering finds a definition you can name; paging is for browsing one you cannot. Each list shows twelve at a time with a count line saying which twelve of how many, and the pager appears only past twelve so a small library is unchanged. Filtering resets to the first page.

### fix(chat): the thread survives navigating away and back (#259)

Views stay mounted and hidden with `display:none`. The chat thread measures the container it scrolls in, and `display:none` makes that measurement zero; the package does not recover it on show, so returning to Chat left the thread blank with only the composer drawn (the part that does not depend on measured height).

Chat is now hidden without leaving the layout, so its geometry stays valid, with visibility and pointer-events keeping it out of the way. Other views keep `display:none`. A view change also dispatches a resize, for anything that re-measures only on that signal.

### fix(map): cluster labels stop piling into an unreadable stack (#260)

Every cluster drew its label at its centroid with nothing preventing overlap, and the text carried two path segments per part. Labels are now drawn largest cluster first and one that would cover an already-drawn label is skipped; the legend below names every cluster in full, so a dropped label costs nothing. The text is the last path segment of at most two parts, capped at thirty characters.

### feat(personas): custom icons, uploaded or an emoji (#261)

A picker of sixty text rows is a wall; a mark gives each persona a shape you recognise before you read it. The icon field takes an uploaded image or an emoji, and with neither the name's initials stand in.

An uploaded image is written into the corpus under `.agents/icons`, so it versions and syncs with the definition it belongs to rather than living in a store of its own, and is served through the ordinary corpus route. The extension comes from an allowlist and the filename from the persona, so an upload cannot name its own path or land as something executable, and one persona keeps one icon.

Icons appear in the Agents list, the chat picker, and on the control once a persona is active.

### fix(chat): the persona picker drops deployment jargon (#262)

'Deployment default' names the thing from the operator's side. Someone choosing a persona mid-conversation is picking how the assistant answers, so the entry is now 'Default', described as how this workspace normally answers.

### fix(agents): the icon upload button says why, instead of doing nothing (#263)

The file input was disabled until a persona name existed, so on a new persona the button looked pressable and did nothing. It is always enabled now and explains the one precondition when it applies, since the image file is named after the persona. A failed upload reports its status, and the input resets so choosing the same file twice fires again.

Verified the server side independently: posting a PNG to `/api/extensions/persona-icon` returns `{ok:true, icon:'.agents/icons/<name>.png'}`.

### fix(agents): the icon attaches on upload, and the button is honest before it (#264)

Two faults in one flow. The upload wrote the image into the corpus but only filled a form field, so an image landed with nothing referencing it until someone pressed Save, which reads exactly like the upload going nowhere. When the persona exists the icon is now written into its frontmatter as part of the upload and the list refreshes; a persona still being written carries the path in its form until saved, and the message says which happened.

The button now looks unavailable before a name exists rather than explaining itself after a click: greyed, not pressable, precondition in the label.

### feat: missions, the first slice of coordinated multi-agent runs (#265)

A mission is an objective, a set of agents from the persona library, a board recording what happened, and a results directory in the corpus. Agents are `pw code` one-shot runs: headless, bounded, daemon-independent. An agent's result may request subtasks, so a fleet grows itself, with the ceiling (default 10, cap 24) and depth limit (default 2, cap 3) enforced server-side; a refused subtask is recorded on the board with the reason.

Results are corpus files under `missions/<id>/` that index on arrival; board messages point at them. Chat gains `start_mission` and `mission_status`; the Agents tab gains the fleet view: per-agent status with lineage, the board timeline, and a stop.

Workers do not write the board directly in this slice (that needs mission-scoped tokens on /api/mcp; named next step). Tested with a stub CLI whose agents request subtasks: lineage, both limits, refusals on the board, results on disk, board ordering.

### feat(missions): a coordination plane workers can actually use (#266)

Workers reach the mission board over the Studio's own MCP endpoint with six tools: post, read, status, claim, complete, offer. Each mission mints a scoped bearer authorizing exactly that board for that mission's lifetime, written into each worker's workspace as the CLI's local MCP settings, so a spawned agent is connected without a second process and never holds a platform credential.

The claim is what makes many workers on one queue safe: atomic by construction on the server's single thread, with a losing claimant told who holds the task rather than duplicating work.

Board content is text other models wrote, so `board_read` names it as data and forbids following instructions in it. The auth hook accepts a mission bearer only for /api/mcp and the MCP layer re-checks before exposing any board tool.

### HPC coordination: launch and follow work on connected systems (#267)

Asking to run a model on a named HPC system should not require the user to know the site's scheduler settings. It did.

## What changed

The platform already holds the knowledge. A workflow ships named input configurations, one per site, carrying settings known to work there; `pw environments` reports every schedulable partition with state, free nodes, walltime ceiling and required fields. Three new tools expose that to the assistant:

- `workflow_configs` lists a workflow's site configurations, summarised by the settings that decide a submission (partition, QoS, walltime, scheduler directives, account, model).
- `hpc_environments` lists systems, or one system's partitions with its ceilings and required fields, for sites no configuration covers.
- `watch_run` follows a run, reports per-job state and which system it landed on, and returns as soon as something changes so progress can be reported as it happens.

`run_workflow` now takes a configuration, resolves a named system into whichever input the workflow declares as `compute-clusters`, and merges caller inputs over the configuration instead of letting `--inputs` replace it wholesale.

The site account is the one required value the builtin configurations omit, since it is per user. A rejection for it is now explained, with an account the workflow's other configurations use, rather than returned as a raw validation line.

Two fixes found along the way: `list_clusters` dropped `schedulerType`, without which a caller cannot tell whether to write `#SBATCH` or `#PBS`; and CLI JSON is now parsed by taking the first balanced value, because the CLI appends an upgrade notice that a bare `JSON.parse` chokes on.

## Also: missions are tasks

"Mission" was a CrewAI crew under a name nobody else uses, and it put the fan-out decision in the user's sentence, which is the one place the field does not put it. Fan-out is now implicit and off by default, bounded by `delegationEnabled`, `delegationMaxAgents` and `delegationMaxDepth`. Agent states are A2A's own, which adds `input-required`, a state the old set could not express.

## Verification

50 server tests pass, 10 of them new and covering configuration selection, resource resolution, merge precedence, the missing-account path, and run following. The three read-only tools were also exercised against the live platform across 18 connected systems.

### Follow-ups: run status wording, and the CLI in container deployments (#268)

Both found by deploying #267 and following the run with the tool it added.

`watch_run` printed a job the run has not reached yet as `undefined`, which reads as a fault rather than as its turn not coming. It now reads `not started`.

The container launch path binds no `pw` CLI, so every platform tool in the assistant fails there with a spawn error for a command that does not exist. The compute path already binds the host binary in; the container path now does the same, alongside the platform host added in #267. Bundle deployments were unaffected because they run on the host and inherit its PATH, which is why this went unnoticed.

Verified: the workflow YAML parses, and the 10 HPC tests still pass.

### Chat: /tools lists the commands instead of reaching the model as text (#269)

A user typed `/tools` in the chat and got an answer about the string. The listing answered to `help`, `commands` and `skills`, but not to the name people actually reach for.

It now answers to `tools`, `agents`, `personas` and `list` as well. An unknown bare `/word` was passing through for the same reason, so anyone guessing at the command set got a reply about their guess rather than the set; it now says there is no such command and shows what does exist. Anything with content after the first segment is still left alone, so a pasted path such as `/shared/corpus/notes.md` reaches the model as a path rather than being read as a command.

Five tests added covering the alias set, tool invocation by name, the unknown-command path, pasted paths, and ordinary messages containing a slash.

### Drop tool attribution from the repo (#270)

`AGENTS.md` still carried its old filename as its heading, and the MCP module named one particular client and its registration command in comments where the point is the protocol, not the product. Neither reference does any work, so both are gone.

What stays is deliberate: the conventions file the knowledge base actually ships, which the server reads by name, and the agent-state directory in the indexer skip list. Those are real paths. Removing them would stop the corpus conventions binding chat output and would start indexing agent state into the corpus.

55 tests pass.

### Hide the show-all-chats toggle when shared history is off (#271)

The setting's own description says turning it off "removes that option entirely", and it did not.

The button was gated on whether auth is enabled, never on the setting, so it stayed on the chat page offering a scope the server would refuse: with shared history off, conversations owned by someone else are already 404 to everyone but their owner. The control could only fail, and the only way to find that out was to click it.

`/api/me/model-key` now reports `sharedHistory`, and the button is gated on it. A browser that stored the all-chats scope before the setting was turned off is moved back to its own, since it would otherwise sit on a scope it can no longer have and see an unexplained empty rail.

55 tests pass.

### Retry without the token cap when a provider rejects it, and stop blaming the key (#272)

`/tools` on a codex-backed model failed with `Unsupported parameter: max_output_tokens`.

## The bug

Measured directly against the gateway, the failing combination is streaming plus a token cap. Streaming alone works, the cap alone works, and together the gateway translates our `max_tokens` into the provider's own spelling, which that provider refuses. A retry without the cap returns tool calls normally.

A retry for exactly this already existed, but it matched only the gateway's masked wording, "an error occurred while generating the response". The gateway now passes the provider's own complaint through instead, so nothing matched and the 400 reached the user. It now also matches an explicit rejection naming a token-cap parameter, and remembers the model for the process lifetime so only the first turn pays a failed request.

## The message was worse than the bug

Any gateway error on a model whose name carries a provider prefix was reported as a probably-expired key. So a 400 about a request parameter told the user to go renew a credential that was working fine, which is a worse outcome than the raw error.

Auth failures are 401 and 403. A 400 naming a parameter is the request being wrong for that provider. The classification now splits on status, and the logic is extracted as a pure function with six tests covering the parameter case, both auth codes, shared models that have no personal key to renew, and the unclassified fallback.

61 tests pass.

### Pin the RAG endpoint subdomain so its URL survives a restart (#273)

The platform assigns a random subdomain unless one is given, so the registered model endpoint moved every time the Studio restarted. Today it went from `capable-cat.activate.pw` to `proven-burro.activate.pw` across a single restart, and anything configured against the old URL, a `pw code` model or an MCP client, then pointed at a session that no longer existed and failed with "Session not found".

The endpoint already has a stable name, so it is now the subdomain too. The URL becomes `https://<app>-rag.<sessions domain>/` and stays there.

61 tests pass.

### Keep the selected model across a refresh, and the credential vault across a redeploy (#274)

Two pieces of state that should have survived and did not.

## The selected model reset on refresh

Picking a model and reloading came back on a different one. The guard that restores the remembered model only acted when the current selection was missing or no longer served, and the chat provider sets its own valid selection as soon as the model catalog arrives, so the restore never ran. Worse, the effect that remembers the choice then wrote that default over the model the user had picked, so the stored value was destroyed by the same reload that ignored it.

The remembered choice now wins once, on the first pass after the catalog loads and before anything is written back. After that the selection is the user's own and is persisted, and a selection naming a model that is no longer served still falls back rather than letting a send fail.

## Personal keys had to be re-entered after every redeploy

With no `index_base` configured the index base is the work directory's `app/index`, and that is where the per-user credential vault and the secret that decrypts it live. The bundle refresh runs `rm -rf app`, so a redeploy deleted both, along with the search index.

The refresh now moves `app/index` aside and restores it over the freshly unpacked tree, so the bundle replaces code and leaves state alone. Anything new the bundle ships that the old base lacks is kept.

Deployments that set an explicit `index_base` were never affected, which is why this went unnoticed. Note that keys added as "session only" are held in memory with a TTL by design and are still lost on restart.

### Send the sealed key cookie in the context the app actually runs in (#275)

A user who entered their key was asked for it again after a restart, even though the browser still held the durable copy.

## Cause

The key cookie was `SameSite=Strict`. A deployment is reached through a sessions domain that differs from the platform's own, so the app runs as a cross-site context, and a Strict cookie is stored and then never offered back. The server's in-memory entry was all that carried the key, and that dies with the process.

I measured the session proxy first rather than assuming: `Set-Cookie` survives it intact and `Cookie` is forwarded to the app. The transport was never the problem, the policy was.

## Fix

`SameSite=None`, which is safe here because the cookie grants nothing on its own. It is `HttpOnly`, its value is encrypted with a server secret the browser never sees, and rehydration runs only for a request already carrying a verified platform JWT, so a cross-site request without that token cannot use it.

It is written twice under two names, because neither form covers both ways the app is opened:

- **Partitioned** is keyed to the embedding site under CHIPS, which is what browsers that block third-party cookies require when the app runs inside the platform.
- **Unpartitioned** is what arrives when the app is opened directly at its own URL, where the partitioned copy belongs to a different partition and stays behind.

Both carry the same sealed value and hydration takes whichever arrives, so a key entered in one context still works in the other.

Six tests cover the attributes, that both copies are written, chunk ordering, that no header expires a cookie another header in the same response sets (cookie identity ignores SameSite, so a naive migration would have deleted the value it just wrote), and that clearing expires both copies. 67 tests pass overall.

### Report which build is running, so a deploy can be confirmed (#276)

A redeploy that silently failed and one that worked look identical from the interface, and the only way to tell them apart was to go and read the run log.

The version comes from a release tag. A deployment has no git checkout, so the bundle build stamps version, commit and build time into `build-info.json` and that is the authority there. Running from the repository the tag is read from git instead, and an untagged checkout reads as `dev` rather than borrowing the package version, so a stale deploy can never look like a release.

`GET /api/version` answers it in one request, which is what a deploy check actually wants. The sidebar footer shows the version beside the identity, model and index lines and opens a panel with the commit, the build time, and when the server process started, so a page left open across a restart is visibly talking to a different process.

Versions are `vX.Y`, tagged in the repository. This PR is tagged `v1.0` once merged.

Five tests cover the resolution order: the stamp wins over the checkout, an incomplete or unparseable stamp falls through rather than being trusted or throwing, and an untagged checkout reports `dev`. 72 tests pass overall.

