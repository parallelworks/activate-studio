### Quick start

Files collect in many folders, drives, and systems. Finding one means remembering where it was saved, and answering a question from a set of documents means opening them one at a time.

{appName} indexes your folders with GUFI. **Search** finds files by name, by the words inside them (PDFs and office documents, and text in images where OCR is available), and by meaning. **Query** answers questions about the files themselves, such as the largest, the newest, or the totals by type. **Chat** answers questions from the files and links each source it used.

Files stay where they are. {appName} keeps its index in a folder of its own, reads each file in place, and follows the file system's permissions. Indexing, search, and query run where {appName} runs and need no network connection; Chat sends text only to the model endpoint you configure, which can be on the same computer. The same index works for a folder on a laptop and for a cluster's file systems.

1. **Choose a folder.** On a computer of its own, setup asks which folder to index; Settings, Libraries changes it later.
2. **Search.** Type a word or phrase in **Search**, then click a result to open the file at the match.
3. **Ask.** Open **Chat** and ask in plain language. The answer links the files it used.
4. **Query.** Open **Query** and pick a canned query, or build one, to list files by size, age, type, or label.

On a computer of its own, setup can be run again at any time, starting from what is set now: **Run setup again** in Settings, General, or **Run Setup…** in the macOS app's menu.

# Basics

## Chat

Ask in plain language. The assistant searches the knowledge base, reads the files that matter, and links each source. Past conversations are kept and are searchable themselves.

- **Attach** files or images with **+**, then **Add files**; they are added to the library and indexed.
- **+**, then **Tools**, switches kinds of tools on or off for your chats: the knowledge base, platform workflows, platform changes, agents, editing the library, extensions, and each attached MCP server. Switch off workflows to keep a chat from launching anything, or editing to keep it read only. A dot on **+** means something is off; the choice is kept in this browser.
- **Scope**, at the top right, chooses which library the assistant searches in this chat, or all of them, and limits answers to material with the labels you choose.
- Type **/** to pick a skill, a tool, or a persona to use.
- Ask it to run work on a connected HPC system; it follows the run and explains a failure if there is one. A side view beside the chat shows the run: its form as submitted, the live graph of its jobs, and the web session it opens, such as a study viewer. Click a step in the graph to read its summary, its log as it grows, and the script it ran. **Beside chat** on a session card adds the session as a tab there, **Full screen** shows it over the whole Studio (Close or Escape returns), and **Pop out** opens a tab in a window beside the Studio.
- Ask to see running sessions by a word or a run ("show me the modo sessions"); each comes back as a card that opens it.
- Ask what storage you have, or ask it to start, resume, stop, or destroy a cloud cluster or cancel a workflow run. Each of these changes asks first: the chat shows the exact call and waits. **Yes, allow this call** runs it, **No, deny this call** does not, and when stopping or destroying a cluster you can choose the other instead. A request left unanswered for 30 minutes expires. Only the person who asked can answer, and the change runs with their own platform key.
- **Select chats**, above New chat, deletes old conversations together.
- In a shared session, add your own model key under Settings, "Model access", to chat as yourself.
- A reply with no answer says why: the model reasoned without answering, ran tools without answering, or returned an empty reply, which some providers do when they decline a request. A provider that is rate limiting is retried twice after short waits. Either way the reply ends with **Retry with** links to the other models; nothing is sent to another model until you click one.
- Send a message while a reply is running to steer it: the reply takes it in at its next step and shows "You added" where it did. A message sent while the reply is writing its final answer goes out after it, as before.
- A message whose reply was cut off, for example when the Studio restarted, says so and offers **Retry**, which sends it again.
- While a reply streams, its thinking and tool calls stay folded to one line each; click one to open it.

## Library

The folder tree beside a viewer. Markdown, code, images, PDFs, office documents, and STL and STEP models open in place.

- **Right-click** a file or folder to label it, add a folder, or delete it.
- **Find**, in the viewer's header, searches the open file.
- **Indexed text** shows what search sees for a file, including text read from images.
- If several libraries are available, the picker at the top of the tree switches between them. **All libraries**, the default, shows them as one tree, a folder each; Search, Query, and Stats then cover all of them, and files can still be added and changed inside the knowledge base's folder. Each tab remembers its own choice.
- With the status panel open, the **Details** card describes what you last clicked in the tree, or the library itself: its size, files and folders, the latest change, the file types taking the most space, its labels and those inside it, and which library and index it comes from.
- Settings, Libraries lists every library {appName} searches, with where it is, its state, and what it can search. **Add a library** asks where (this computer, or a remote system reached over SSH or over ACTIVATE with pw ssh, where your systems on the platform are listed, running ones first) and how: build an index of a folder, or connect a GUFI index that already exists. A build checks GUFI and the folders first, asks before making a folder that does not exist, and shows its progress in the list. A folder on this computer is kept in step as it changes. On a computer of its own, the main folder's row changes the folder, builds or updates its index, stops a build, deletes the index (never the files), and shows the index log.

## Search

One box searches three ways, and each result says which one matched: **name** (any part of a file or folder name), **words** (whole words inside files: PDFs, office documents, notes, code, and text in images), and **meaning** (passages that say the same thing in other words, so a plain question finds a report that never uses its words). The line under the box says which libraries are searched by meaning; **How search works** explains each kind.

Search by meaning is a choice per library, because it is the slowest part of indexing and many folders (code, data, simulation output) gain little from it: **Off** (names and words only, fastest), **Documents** (documents and images: PDF, Office, text, Markdown and image descriptions, the first few passages of each), or **Full** (every file with text, about three times the indexing time of Documents). The main folder starts at Documents and other libraries at Off; change them in Settings, Libraries, in the Meaning column. A change applies at the library's next build or sync. A search with quotes or operators, one short word, or a code such as ABC-12 matches words only.

- Use "quotes" for a phrase, OR between alternatives, -word to leave a word out, and word* for words that start the same way.
- Label chips under the box narrow the results.
- Tick results to label them together.
- **GUFI commands**, under the results, shows the commands that found them, ready to copy and run in a terminal.

## Adding material

Drag files or folders onto the tree, or use **Add**, which also takes a web address. New material is searchable in about a second. Files copied in outside {appName} are picked up within minutes, or right away with **sync now** at the bottom of the navigation.

# Organize and analyze

## Labels

Labels organize files without moving them. Label a folder and everything in it carries the label, including files added later. Apply labels from the tree, the viewer, search results, or by asking the assistant; filter by them in Search, Query, and the chat's Scope.

## Query

Structured questions about the files themselves (largest, newest, totals by type) answered in milliseconds. Start from a canned query, use the builder, or write read-only SQL, and save the ones you reuse. **GUFI commands** under the form shows what ran, ready to copy, and the code button beside a saved query shows the SQL it runs and its command.

## Stats

The knowledge base at a glance: size, file types, labels, and recent changes. Click any figure to see the files behind it. While the index is being built the page follows it, phase by phase; **Refresh** reads everything again, and **Index runs** lists recent builds and syncs with how long each phase took.

# Run work

## Workflows

On the ACTIVATE platform, the Workflows tab shows the workflows chosen for {appName}. Open a tile, fill in the form, and press **Run**; **Validate** checks the inputs without running anything. Runs use your own account.

- Saved configurations appear as presets at the top of the form.
- Under the title, the form says where the workflow comes from: stored in your account (local), or read from a repository (remote), with its branch and file.
- A form shows the sections and columns its workflow defines.
- A workflow kept in a repository shows its README beside the form, with how it works and what its inputs change. **Hide README** puts it away.
- A workflow from GitHub that asks for access to your account variables waits until you tick the approval.
- Administrators choose the set under Settings, "Workflows".
- Apps that runs open, such as a design explorer or a notebook, are listed under **Sessions**. Click one to show it in a side view on the page, as in chat, or press **Window** or **New tab**. Click a run's id to show its form, graph, and session there; a run started from a form opens there too. When the Studio is open inside ACTIVATE, the session opens on its ACTIVATE page in a new tab. A session started from chat also appears in that conversation.

## Agents

When a request splits into parts, the assistant can hand each part to an agent working in parallel. The Agents tab has three pages:

- **Tasks**: each delegated task, its agents, the machine they run on, and their output. Approve or deny what an agent asks to do, or give a working agent direction.
- **Fleet**: standing agents that wake on a schedule or when something changes, and ask you when they need a decision.
- **Personas and skills**: the files that shape how the assistant behaves.

Delegation is turned on under Settings, "External access".

You can also ask the assistant to run a pw code agent on one of your clusters, for example "start an agent on my cluster in ~/project to fix the failing tests, and keep going until they pass". It starts a pw code session there with your key and gives it the goal, so the agent keeps working until the goal is met or a turn limit is reached. Ask the assistant how the agent is doing, give it direction, or pause, resume, or clear its goal. You can open the same session in a terminal with `pw code attach <cluster>/<session>`.

# More

## Getting around

The address bar follows what you are looking at, so back and forward work and a copied link opens the same place for someone else.

- **Search**, at the top of the navigation, searches the knowledge base; Ctrl+K (⌘K on a Mac) jumps to it from anywhere.
- The navigation groups its pages under **Knowledge** and **Run**; click a group's name to fold it away, and {appName} remembers.
- **Install app**, in the navigation, installs {appName} as a desktop app in its own window. If a classification banner is shown, the chevron in Chrome's title bar moves the banner into the title bar. The loading screen draws the banner as it was last shown, so the title bar has no gap while the app starts.
- The bottom of the navigation shows who you are signed in as, whether the models are reachable, and the state of the index; click any of them for details.
- **About**, at the foot of Help's list and in the version's panel at the bottom of the navigation, shows the version and license. ACTIVATE Studio is built and maintained by Parallel Works.
- The **status panel** at the right shows what is running (agent tasks, standing agents, workflow runs, open sessions), your libraries and the last index sync, the model in use (while AI features are on), and **Connections**, when there are any: the ACTIVATE platform, attached MCP servers, and the machines that hold remote libraries, each with its state. The panel button at the top right of each page opens and closes it, and {appName} remembers. Click a card's header for its detail; click a run or session to open it beside the chat or on the Workflows page. While a run or session is open beside the chat, the panel steps aside. Phones do not show it.

## Setup and the sidebar

On a computer of its own, with nobody signing in, {appName} opens with a short setup, one step at a time. **Skip this step** moves past a step without changing it, and **Finish later** closes setup at any step; everything in it is also in Settings.

- **Your Studio**: the name, an icon for light mode and an optional one for dark mode (PNG, JPEG, SVG or WebP, up to 1 MB; the browser tab uses it too).
- **Appearance**: Light, Dark or System (which follows the computer's own setting), and the accent color. Both apply as you pick them.
- **AI features**: optional. On, with the OpenAI-compatible endpoint, its key (empty for a model server that takes none) and the default model; **Test** lists the models the endpoint offers or says what failed. An ACTIVATE account works too, set up in Settings, Model access. Off, Chat and Agents leave the sidebar, the footer drops its model line, and nothing is sent to a model; search, the library, query and statistics work as before. AI features can be turned back on at any time in Settings, General, AI features.
- **Sidebar**: which pages it shows. Workflows needs an ACTIVATE platform login; for workflow YAML files of your own, see [Meridian](https://github.com/parallelworks/meridian), the open-source workflow engine.
- **Your folder**: the folder to index. In the macOS app, **Choose another folder…** opens the Mac's folder chooser; elsewhere the folders show in the page. Choosing another folder restarts {appName} on it in the macOS app, then offers to build its index; elsewhere, set `KB_ROOT` and restart. **Search by meaning** sets how much of the folder it covers (see Search). **Build the index** runs in the background: go on with setup, or finish it, while it runs, and the steps after show how far it has got. Indexes can be added or changed at any time in Settings, Libraries.
- **Other libraries**: GUFI indexes that already exist, on this computer (typed or picked from its folders), on another machine over ssh, or, when the pw CLI here is signed in, on one of your running clusters through ACTIVATE.

**Run setup again**, in Settings, General, and **Run Setup…** in the macOS app's menu open the setup later. Each step shows what is set now and changes nothing until you change it; leaving partway keeps what was already saved.

Settings, General has a tab for each part: **Appearance** (theme, accent, surface tone), **Branding** (name, icon, classification banner), **Sidebar**, **AI features** (the switch, the mission and context the assistant works from, the suggested questions under an empty chat, and image captioning), **Indexing** (search across libraries, background sync, history snapshots), **Updates**, and **Server** (the port). Settings, Model access has the model endpoint, and Settings, Libraries the folder and other libraries.

Settings, General, **Sidebar** has a switch for each page. A page switched off leaves the navigation, and a link to it opens the first page still shown. Settings and Help always show, and the switches apply to everyone using this {appName}.

On a computer of its own, the suggested questions and the mission start out written for one person's own files; change them under Settings, General, AI features.

## Model access

Settings, **Model access** shows where {appName} gets its models, with the models available or what failed; **Re-check** asks again. **What each part uses** lists the credential behind models, the Workflows page and the assistant's platform tools, libraries on ACTIVATE systems, and ChatGPT and Copilot subscriptions. All but models also run the pw CLI. When {appName} cannot find the CLI, the pw CLI tab says so and takes its location (`command -v pw` in Terminal prints it).

- **pw CLI**: the platforms the pw CLI on this computer is signed in to, one row per context. **Use** picks one; with **Follow the pw CLI's current context** ticked, {appName} uses whichever `pw context use` last chose. **Sign in** signs the CLI in to another platform: it shows a code, you approve it in a browser on any device, and {appName} uses the new sign-in.
- **API key**: an ACTIVATE platform token or API key, which covers models and the platform, or an AI gateway key, which covers models only.
- **Endpoint**: any OpenAI-compatible endpoint, such as Ollama, LM Studio or vLLM on this computer, the OpenAI API, or GitHub Models with a token that has the Models permission. An endpoint covers models only; platform tools keep using the pw CLI.
- **Subscriptions through ACTIVATE** connects a ChatGPT plan, with a code you enter at OpenAI, or GitHub Copilot, once your GitHub account is linked in ACTIVATE. The platform keeps the connection, and its models appear with the others.

On a shared deployment the same page holds your own key or endpoint; without one, requests use the deployment's credential.

## Working offline

With no network, {appName} keeps working from its libraries: browsing, search, query, statistics and adding material. Chat keeps working with a model server on this computer, set under Model access, Endpoint. Anything that needs the network says so in one line instead of waiting: the model banner and the footer show **offline**, the Workflows page says the platform cannot be reached, and the update check shows **Offline** and tries again later.

## Using it from other tools

Other tools can use this knowledge base. pw code and other MCP clients can search it directly, and any OpenAI-compatible client can use it as a model that answers from it. Settings, "External access" has the addresses and the exact commands.

## Updates

When a newer version is available, a one-line notice at the top of every page says so; **What's new** in it lists the changes in each newer version, and a click on one reads it. In the macOS app, **Update now** downloads the release, checks its signature, installs it and restarts, and the page reloads on the new version. A copy run from a clone of the repository shows the commands that update it under **What's new**. **Later** hides the notice until a newer version comes out; the version at the bottom of the navigation still says **update available**.

Settings, General, **Updates** has the same, and **Roll back**, which returns to the version installed before. The app checks a minute after it starts and every six hours unless **Check for updates automatically** is off; with **Install updates automatically when idle** on, it installs a new version once no chat reply or indexing has run for ten minutes. The first time {appName} opens on a new version, a notice says **Updated to** that version and lists its changes. When an update also changed the macOS app's own window (the title bar, the Dock icon), the app reopens on its own after an update you started; after one installed while idle, the notice and Settings, Updates offer **Reopen now**. A hosted deployment is updated by its operator and shows none of these.

To remove the macOS app, choose **Uninstall ACTIVATE Studio…** in its app menu. It lists everything it removes, with your indexes, settings and logs included unless you untick them, and never removes your documents.

**What's new**, at the top of Help, lists every version's changes, newest first. The version at the bottom of the navigation and Settings, Updates open it too.

## The index

{appName} is built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases, one per folder, holding each file's details, its text, and search vectors. Access follows the file system's permissions, and a change re-indexes only the folder it touched.

A build runs in four phases, each with its own count: **names** (every file and folder, searchable by name as soon as this phase ends), **documents** (PDFs and Office files converted to text), **text** (images, other formats, and the full-text tables), and **meaning** (embeddings for search by meaning, at the library's level; skipped when it is Off). The index line at the bottom of the navigation, Settings, Libraries, and Stats all show the same progress. Click the index line for the phases, **Stop**, and **View log**, the index log as plain text. Text and embeddings are kept between builds, so updating an index, or choosing a folder that was indexed before, redoes only what changed.

Settings, General, **Indexing** sets how many threads each phase uses (one per processor core by default), which readers run (document text, the PDF reader for files the converter cannot read, LibreOffice for older Office files, OCR, and image descriptions by the vision model chosen under AI features), the main folder's level of search by meaning, and where the index is kept. Libraries built of a folder on this computer follow the same threads and readers, and each has its own level of search by meaning, in Settings, Libraries. It also lists which file types this computer can read and which index by name only.

Knowledge base for this deployment: **{kbLabel}**
