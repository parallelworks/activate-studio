# Reference

The details behind the user guide ([`HELP.md`](HELP.md)), by part of the app. The assistant reads this too.

## Chat

- Replies can show things inline: images, document pages, a workflow's steps as an interactive diagram, and an interactive 3D model.
- **Scope** limits the assistant's retrieval for the conversation to the chosen labels; the limit is enforced on the server.
- The assistant recommends workflows from the account's catalog, checks a run with a dry run first, and launches it only when asked. It lists and inspects runs, including their errors and the end of their logs.
- When it launches work on an HPC system it reads the site's partitions, walltime limits, and saved configurations from the platform, and follows the run to the end. If the platform's per-user workspace has shut down while idle, a launch starts it and tries again.
- An attachment is filed into the library and indexed, and its content, including text read from images, is part of the conversation. Clicking its tile later opens the file in the Library.
- The **/** list filters as you type; the arrow keys move, Enter or Tab inserts, and Escape closes. `/skill_name` applies a skill, `/tool_name` runs a tool, `/agent_name` adopts an agent file for one message, and `/help` lists them all.
- The thinking line above a reply expands to show the reasoning and each tool call as it happens, and stays with the message afterward.
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

## Workflows

The full description, including where tiles come from, approvals for GitHub workflows, how a run belongs to the person running it, and the input types the forms support, is in [`WORKFLOWS.md`](WORKFLOWS.md).

## Agents

Fleet agents each have a goal, a persona, a budget, and triggers: a schedule, a workflow run ending, or files changing under a folder. An agent wakes, does one bounded round of work with the assistant's tools, writes the outcome to its journal, and waits under **Needs you** when it needs a person; a message in its box is answered on its next round. Delegated tasks, the ways agents run, approvals, and steering are in [`AGENTS.md`](AGENTS.md).

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

## Using it from other tools

The deployment serves an OpenAI-compatible endpoint with two models:

| Model | Does | Suits |
|---|---|---|
| `studio-agent` | runs the full assistant and returns a finished answer with citations | tool-calling clients such as pw code |
| `studio-rag` | adds retrieved, cited context to one fast model call | plain chat clients and scripted questions |

Settings, "External access" chooses which of the two appear in model lists (`studio-agent` by default); both can be called by name either way. `studio-agent/<model id>` pins the underlying model for a request. The same section can publish the endpoint into the platform's chat and provider catalog, shows the registration state and the exact model ids, and logs recent calls with the model used, the credential, the retrieval terms, and the timing. The MCP endpoint and the command to add it to pw code are there too. Inside the Studio's own chat the published models are hidden, since the chat already has the same grounding.
