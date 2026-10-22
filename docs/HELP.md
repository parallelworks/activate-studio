### Quick start

Files collect in many folders, drives, and systems. Finding one means remembering where it was saved, and answering a question from a set of documents means opening them one at a time.

{appName} indexes your folders with GUFI. **Search** finds files by name, by the words inside them (PDFs and office documents, and text in images where OCR is available), and by meaning. **Query** answers questions about the files themselves, such as the largest, the newest, or the totals by type. **Chat** answers questions from the files and links each source it used.

Files stay where they are. {appName} keeps its index in a folder of its own, reads each file in place, and follows the file system's permissions. Indexing, search, and query run where {appName} runs and need no network connection; Chat sends text only to the model endpoint you configure, which can be on the same computer. The same index works for a folder on a laptop and for a cluster's file systems.

1. **Choose a folder.** On a computer of its own, setup asks which folder to index; Settings, Libraries changes it later.
2. **Search.** Type a word or phrase in **Search**, then click a result to open the file at the match.
3. **Ask.** Open **Chat** and ask in plain language. The answer links the files it used.
4. **Query.** Open **Query** and pick a canned query, or build one, to list files by size, age, type, or label.

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
- Settings, Libraries lists every library {appName} searches, with where it is, its state, and what it can search. **Add a library** adds a GUFI index on this machine or, where offered, one on another machine over ssh or one built on one of your clusters. On a computer of its own, the main folder's row changes the folder and rebuilds its index.

## Search

One box finds exact words (inside PDFs, office documents, and images too), related meaning, and file names. Each result says which kind of match it was.

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

The knowledge base at a glance: size, file types, labels, and recent changes. Click any figure to see the files behind it.

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
- The **status panel** at the right shows what is running (agent tasks, standing agents, workflow runs, open sessions), your libraries and the last index sync, the model in use, and connections that need attention. The panel button at the top right of each page opens and closes it, and {appName} remembers. Click a card's header for its detail; click a run or session to open it beside the chat or on the Workflows page. While a run or session is open beside the chat, the panel steps aside. Phones do not show it.

## Setup and the sidebar

On a computer of its own, with nobody signing in, {appName} opens with a short setup, one step at a time, and any step can be skipped:

- **Your Studio**: the name, an icon for light mode and an optional one for dark mode (PNG, JPEG, SVG or WebP, up to 1 MB; the browser tab uses it too), and the accent color.
- **AI features**: on, with the OpenAI-compatible endpoint, its key (empty for a model server that takes none) and the default model; **Test** lists the models the endpoint offers or says what failed. Off, Chat and Agents leave the sidebar, the footer drops its model line, and nothing is sent to a model; search, the library, query and statistics work as before.
- **Sidebar**: which pages it shows.
- **Your folder**: the folder to index. Choosing another folder restarts {appName} on it when it runs as the macOS app; elsewhere, set `KB_ROOT` and restart. **Build the index** indexes the whole folder and shows its progress.
- **Other libraries**: GUFI indexes that already exist, on this computer (typed or picked from its folders), on another machine over ssh, or, when the pw CLI here is signed in, on one of your running clusters through ACTIVATE.

Settings, General, **Run setup again** opens the setup later. Everything in it is also in Settings: the name, icon, accent and AI features in General, the endpoint in Model access, and libraries in Libraries.

Settings, General, **Sidebar** has a switch for each page. A page switched off leaves the navigation, and a link to it opens the first page still shown. Settings and Help always show, and the switches apply to everyone using this {appName}.

## Using it from other tools

Other tools can use this knowledge base. pw code and other MCP clients can search it directly, and any OpenAI-compatible client can use it as a model that answers from it. Settings, "External access" has the addresses and the exact commands.

## Updates

When a newer version is available, a one-line notice at the top of every page says so; **What's new** in it lists the changes in each newer version, and a click on one reads it. In the macOS app, **Update now** downloads the release, checks its signature, installs it and restarts, and the page reloads on the new version. A copy run from a clone of the repository shows the commands that update it under **What's new**. **Later** hides the notice until a newer version comes out; the version at the bottom of the navigation still says **update available**.

Settings, General, **Updates** has the same, and **Roll back**, which returns to the version installed before. The app checks a minute after it starts and every six hours unless **Check for updates automatically** is off; with **Install updates automatically when idle** on, it installs a new version once no chat reply or indexing has run for ten minutes. The first time {appName} opens on a new version, a notice says **Updated to** that version and lists its changes. A hosted deployment is updated by its operator and shows none of these.

**What's new**, at the top of Help, lists every version's changes, newest first. The version at the bottom of the navigation and Settings, Updates open it too.

## The index

{appName} is built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases, one per folder, holding each file's details, its text, and search vectors. Access follows the file system's permissions, and a change re-indexes only the folder it touched.

Knowledge base for this deployment: **{kbLabel}**
