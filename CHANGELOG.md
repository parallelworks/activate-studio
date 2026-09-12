# Change log

Every version, newest first. Each entry is the description of the pull request that made the change, which is written as a change note when the change is made.

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

`server/scripts/e2e-hpc.mjs` runs the HPC chain the assistant uses against a live platform through the same `executeTool` entry point the chat calls: `hpc_environments`, a real `run_workflow` with `scheduler: true`, `watch_run` to a terminal state, `workflow_run_detail`, and passes only when the run completes and a per-run marker comes back from the cluster. First pass today: `activatebatch-00009` on `a30gpuserver`, Slurm JobId 27, 33 s end to end.

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

Live /api/ai/health check, footer 'N models ready' line with detail panel and re-check, and classified gateway credential errors with actionable unlock messaging. Groundwork for per-user genai.mil credentials.

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
workflow runs on a30gpuserver: vLLM serving Qwen3.8-27B 4-bit on the A30
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
workflow (hpcStatusWorkflow, HPC_STATUS_WORKFLOW; hpcmp_status on the
HPCMP platform, hpc_status generally), so a user without a running
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

### fix(chat): system prompt rides in the first user message for GenAI.mil (#167)

GenAI.mil fixes its own system prompt and discards the one the request
carries, so the assistant briefing (what the corpus is, how the tools
work) never reached the model and questions about the knowledge base
drew blank answers. For models matching a configurable substring list
(default genai), every turn now folds the system prompt into the first
user message, the role that backend honors, applied centrally so the
tool loop and the forced final answer are covered alike.

### feat(chat): prompt-level tool emulation for backends that ignore tools (#168)

The GenAI.mil serving path on the HSP passes the tools field through to
an API that discards it (core#18754), so tool calling was dead there
for those models. For backends on the system-fold list, the studio now
carries its own emulation: tool schemas ride the last user turn inside
an envelope protocol with a per-request nonce, the model's envelope
reply is parsed back into ordinary tool calls, tool results return as
user-role text, and envelope JSON is buffered so it never reaches the
client as chat content. Verified end to end against the HSP gateway:
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

GenAI.mil discards every message except the last user turn; a probe
with planted facts proved it answers multi-turn requests from account
metadata rather than the conversation, which is why a follow-up like
"can you make it longer" drew a request for clarification. For models
on the fold list, the entire exchange now flattens into that one user
turn: operating instructions, a labeled transcript declared as the
authoritative history, a continue cue, and the tool envelope. The
failing exchange replayed correctly end to end: the follow-up modified
the model file from the prior turn and re-embedded it.

### fix(chat): emulated backends ground answers and survive agent handoffs (#176)

Three hardenings from running the training battery through GenAI.mil.
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

Verified: merged catalog lists GenAI.mil, a session model, and the local serve together; a turn naming the local model is routed to the local serve; a real model called `list_workflows` through the restored CLI and answered from the result.

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

**Tests**: thirteen cases over the logic that has actually failed or would hurt most if it did: the 3D wire format round-trips exactly and stays aligned, history diffs tell a move from churn by inode, resolveKb refuses traversal (and the suite documents that absolute paths are root-relative), and the GenAI.mil emulation shape is pinned.

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

