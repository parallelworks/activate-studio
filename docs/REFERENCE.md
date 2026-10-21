# Reference

The details behind the user guide ([`HELP.md`](HELP.md)), by part of the app. The assistant reads this too.

## Chat

- Replies can show things inline: images, document pages, a workflow's steps as an interactive diagram, and an interactive 3D model.
- **Scope** sets the library the conversation searches (one library, or all of them) and limits its retrieval to the chosen labels; both are enforced on the server.
- The assistant recommends workflows from the account's catalog, checks a run with a dry run first, and launches it only when asked. It lists and inspects runs, including their errors and the end of their logs.
- When it launches work on an HPC system it reads the site's partitions, walltime limits, and saved configurations from the platform, and follows the run to the end. If the platform's per-user workspace has shut down while idle, a launch starts it and tries again.
- An attachment is filed into the library and indexed, and its content, including text read from images, is part of the conversation. Clicking its tile later opens the file in the Library.
- The **/** list filters as you type; the arrow keys move, Enter or Tab inserts, and Escape closes. `/skill_name` applies a skill, `/tool_name` runs a tool, `/agent_name` adopts an agent file for one message, and `/help` lists them all.
- The thinking line above a reply expands to show the reasoning and each tool call as it happens, and stays with the message afterward. While the reply streams, the thinking stays one line until clicked, and each tool call is one row, its result summary beside its name, so nothing opens or closes on its own as the answer arrives.
- A user message with no reply under it, in a conversation that nothing is answering, shows that no reply was saved, with **Retry**: the Studio restarted mid-turn, or the turn failed or was stopped before any text. A turn that is stopped or fails keeps the text and the tool calls it had, so their errors stay readable.
- When a platform tool's command is refused for its credential, the assistant is told whose credential failed (the user's key, or the deployment's when the user has none) and that the fix is a current key under Settings, "Model access", not a change to the command.
- A reply may spend up to 50 model rounds calling tools (Settings, Assistant tools, "Most tool rounds per reply", 5 to 200; `STUDIO_MAX_TOOL_ROUNDS` sets the deployment default). At the limit the assistant answers from what it gathered and says it stopped, and asking it to continue picks up from there. Each reply logs its timing (rounds, tool calls, model time, tool time, time to the first model answer, and per round the time and the characters sent and written) as `reply timing` in the server log.
- A reply sends the model only the tools it needs. The knowledge base tools, skills, and custom and remote tools always go; the workflow and HPC tools, the agent tools, and the file-writing and labeling tools go when the question names their subject or the conversation used them, and the model can add a group itself with `more_tools`. The system prompt's workflow catalog and HPC guidance go with the workflow and agent tools. On a question about every in-process proposal, this cut a reply from about 42 to about 30 seconds with the same model and the same answer.
- The reader's **+**, **Tools** switches (`GET /api/chat/tool-options`: knowledge base, each tool group, extensions, each attached MCP server) arrive with each request as `tools`. A group switched off is not sent, not offered by `more_tools`, and not added by the question's wording; a call to a tool switched off does not run, and the model is told why. A request without `tools` gets every tool, as before.
- A message sent while a reply is running goes to that reply (`POST /api/chat/steer`), which takes it in as a user message before its next model call and notes it in the reply; once taken it leaves the chat's queue. One the reply does not reach, or one with attachments, goes out from the queue afterwards.
- `run_workflow` takes the workflow, the system name (`resource`), and inputs by plain name; it places each input where the workflow declares it, and fills an empty scheduler account, QoS, or partition from the user's most recent completed run on that system (the same workflow's runs first), saying which run the values came from. A dry run says that nothing ran and how to launch.
- `read_kb_file` reads up to 25 files in one call (`paths`), each under its own heading, so a question over many documents takes fewer model rounds.
- A long reply folds behind **Show more**, except the latest one, so opening a conversation ends on the last line of its answer.
- In a conversation of six or more messages, a column of ticks along the thread's left edge marks each message. Pointing at a tick previews the message; clicking it scrolls there.
- A model whose provider has locked or rejected its key shows **[locked]** or **[unavailable]** in the model list, with a notice above the conversation. Settings, "Model access" has the unlock link and **Re-check**.
- Under Settings, "Model access", a key you add is checked and the page shows which models it reaches. A deployment can require a personal key; until one is added, chat and models wait while browsing, search, and adding material stay open.
- **Select chats** selects conversations by click, or a range with Shift-click, and deletes the selection after a confirmation.

## Library

- **Indexed text** shows exactly what the index holds for a file; for an image that is the text read from it plus a description written by a model.
- **Find** highlights every match, stays in view while you scroll, and steps through matches with Enter, Shift+Enter, or its buttons. A file opened from a search result opens on its first match with the query already in the find bar; a PDF or office document opened that way opens on its indexed text, since page images cannot be searched.
- The right-click menu has Open, Labels…, and Delete for a file, and New folder inside…, Labels…, and Delete folder (with its contents, after a confirmation) for a folder. Delete removes the material and its index entries together.
- Panes resize at their edges and collapse from their headers.
- A read-only library says so beside its name in the picker. Settings, "Libraries" lists each library with its index and source paths, and adds one by path ([`LIBRARIES.md`](LIBRARIES.md)).

## Search

- Words match whole words: TIN matches the word TIN, not "routine". "Quotes" match a phrase, OR separates alternatives, -word or NOT word leaves a word out, and word* matches words that start that way.
- Results by meaning are left out for identifiers, acronyms, quoted phrases, and searches using these operators, where a "similar" document would look like a wrong answer.
- A result opens on its first match, with the query in the viewer's find bar.
- Hover a result to tick it; **Select all** and **Labels…** label the whole result set at once.
- **Load more matches** extends a search to 1,000 results.
- Across all libraries, search runs each library in parallel and merges the results. Settings, General, Search across libraries switches it to `gufi_vt`, as Query uses: name matches, and full-text matches where every search word is plain letters and digits, then run as one `gufi_vt` statement per kind, shown in the GUFI commands panel, while a word with punctuation, a library `gufi_vt` cannot reach, and results by meaning still run per library. Both ways return the same matches. `gufi_vt` answers its libraries one after another, so the parallel default is faster. The environment variable `STUDIO_SEARCH_ENGINE=gufi_vt` sets the default for a deployment.

## Workflows

The full description, including where tiles come from, approvals for GitHub workflows, how a run belongs to the person running it, and the input types the forms support, is in [`WORKFLOWS.md`](WORKFLOWS.md).

## Agents

Fleet agents each have a goal, a persona, a budget, and triggers: a schedule, a workflow run ending, or files changing under a folder. An agent wakes, does one bounded round of work with the assistant's tools, writes the outcome to its journal, and waits under **Needs you** when it needs a person; a message in its box is answered on its next round. Delegated tasks, the ways agents run, approvals, and steering are in [`AGENTS.md`](AGENTS.md).

## Status panel

While open and the tab is visible, the panel reads `GET /api/tasks`, `/api/fleet`, `/api/runs`, `/api/index/status`, and `/api/settings/mcp-servers?cached=1` every 10 seconds, all answered from the Studio's own state, and `GET /api/sessions` and `/api/user-libraries`, which can reach the platform, once a minute. Model health and index counts come from the navigation footer's own reads. Closed, it reads nothing. `?cached=1` returns each attached MCP server's last known state without contacting it, with `checkedAt` null for a server not contacted since the server started or the settings changed; without the flag the route checks each server, at most once a minute, as Settings does. Agent rows follow the Agents section, and run and session rows need Workflows and a platform credential.

## Query

- **Canned** queries: largest, newest, oldest, recently changed, totals by extension, and biggest folders.
- **Builder**: filters (labels included), grouping, sorting, and a folder to search under, with no SQL. **Reset** clears the form and the results.
- **SQL**: any read-only SELECT over the index tables.
- **Saved queries** rerun in one click; a new deployment comes with a few examples.
- Result rows with a path can be ticked and labeled, as search results can.

## Labels

A file's own labels show green in the viewer, and labels it inherits from a folder show gray.

## Adding material

- **Add** has a **New folder** field: type a path, nested if you like, to create a folder and add into it.
- A web page added by address is reduced to its text, with its address recorded; a PDF is saved as it is.
- A dropped folder keeps its structure, and a missing destination folder is created.

## Feature previews

Settings, "Feature previews" turns on capabilities still in preview. **Voice conversations** add a Voice button above the chat for a spoken conversation with the assistant, which can use its tools while you talk. It runs on a separate Unmute deployment (the `unmute` workflow).

## Setup

Setup is for a single-user install, where nobody signs in. With sign-in on, `GET /api/setup/status` answers only `{firstRun: false, standalone: false, canRestart: false}` and the other setup routes refuse with 403.

- `GET /api/setup/status` (`?files=1` adds the indexed file count): `firstRun` (sign-in off, setup not done, and not deployed by the platform, which sets `PW_PLATFORM_HOST`), `standalone`, `canRestart` (a launcher with `STUDIO_CONFIG_FILE` and `STUDIO_RESTARTABLE=1`), `launcher`, `primary` (`path`, `exists`, `writable`, `indexed`, `files`), `gufi` (`found`, `bin`, `semantic`), `embedModel` (`path`, `found`), `libraries`, and `job`, a setup index build still running.
- `GET /api/setup/browse?path=`: the folders in a folder, sorted, without dot-folders, with `parent` and `writable`. `~` is the home folder, and a relative path is taken from it. A path that does not exist is a 404.
- `POST /api/setup/primary-folder {path}`: the folder must exist, be writable, and neither contain the Studio's index nor sit inside it. With a launcher it saves `KB_ROOT` to the launcher's settings file and restarts: `{saved: true, restarting: true}`. A settings file without a restart answers `{saved: true, restarting: false, note}`; no launcher answers `{saved: false, restarting: false, note}`, naming the `KB_ROOT` to set.
- `POST /api/setup/index`: one job over the whole primary folder, its top-level files and then each top-level folder, through the same passes and lock as other indexing. `GET /api/index/job/<id>` follows it; its `progress` carries `phase` (`files`, `directories`, `finishing`, `done`), `done`, `total`, `current` and `failed`. A request while a build runs returns that build. A build for a different folder than the last one clears the old folder's extraction caches and index tree first, and afterwards the sweep records the folder as indexed, so it looks only for later changes.
- `POST /api/setup/complete`: saves `setupDone`.

The sidebar switches save the `sections` setting (`PUT /api/settings {sections}`). An empty list shows every page, including pages a later version adds.

## Using it from other tools

The deployment serves an OpenAI-compatible endpoint with two models:

| Model | Does | Suits |
|---|---|---|
| `studio-agent` | runs the full assistant and returns a finished answer with citations | tool-calling clients such as pw code |
| `studio-rag` | adds retrieved, cited context to one fast model call | plain chat clients and scripted questions |

Settings, "External access" chooses which of the two appear in model lists (`studio-agent` by default); both can be called by name either way. `studio-agent/<model id>` pins the underlying model for a request. The same section can publish the endpoint into the platform's chat and provider catalog, shows the registration state and the exact model ids, and logs recent calls with the model used, the credential, the retrieval terms, and the timing. The MCP endpoint and the command to add it to pw code are there too. Inside the Studio's own chat the published models are hidden, since the chat already has the same grounding.
