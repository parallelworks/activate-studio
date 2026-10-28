### Quick start

Files collect in many folders, drives, and systems. Finding one means remembering where it was saved, and answering a question from a set of documents means opening them one at a time.

{appName} indexes your folders with GUFI. **Search** finds files by name, by the words inside them (PDFs and office documents, and text in images and scanned pages where OCR is available, as it is in the macOS app), and by meaning. **Query** answers questions about the files themselves, such as the largest, the newest, or the totals by type. **Chat** answers questions from the files and links each source it used.

Files stay where they are. {appName} keeps its index in a folder of its own, reads each file in place, and follows the file system's permissions. Indexing, search, and query run where {appName} runs and need no network connection; Chat sends text only to the model endpoint you configure, which can be on the same computer. The same index works for a folder on a laptop and for a cluster's file systems.

1. **Choose a folder.** On a computer of its own, setup asks which folder to index; Settings, Libraries changes it later.
2. **Search.** Type a word or phrase in **Search**, then click a result to open the file at the match.
3. **Ask.** Open **Chat** and ask in plain language. The answer links the files it used.
4. **Query.** Open **Query** and pick a preset, or build one, to list files by size, age, type, owner, or label.

On a computer of its own, setup can be run again at any time, starting from what is set now: **Run setup again** in Settings, General, or **Run Setup…** in the macOS app's menu.

The macOS app needs no administrator rights. Opened from its disk image, it offers to install itself in Applications in your home folder (`~/Applications`), where it updates itself. An administrator can put it in `/Applications` instead; that copy updates itself only for users who can write there. On a Mac that does not allow the app's background item (a LaunchAgent), the app starts its server itself when it opens, instead of at login.

# Basics

## Chat

Ask in plain language. The assistant searches the knowledge base, reads the files that matter, and links each source; a source link opens the file in the Library, in the library it came from. Past conversations are kept and are searchable themselves.

- **Attach** files or images with **+**, then **Add files**; they are added to the library and indexed.
- **+**, then **Tools**, switches kinds of tools on or off for your chats: the knowledge base, platform workflows, platform changes, agents, editing the library, extensions, and each attached MCP server. Switch off workflows to keep a chat from launching anything, or editing to keep it read only. A dot on **+** means something is off; the choice is kept in this browser. A Studio with no platform connected (no ACTIVATE credential and no pw CLI sign-in) offers only the library tools, and the platform switches are not shown.
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

- **Right-click** a file or folder to label it, add a folder, or delete it. In a read-only library the actions that would change it are shown disabled, with the reason on hover; labels still apply.
- **Find**, in the viewer's header, searches the open file.
- **Indexed text** shows what search sees for a file, including text read from images.
- A PDF opens on its **Pages**, and an Office file on its **Slides**, **Document** or **Sheets**. Where LibreOffice is not installed, as in the macOS app, a Word, PowerPoint or Excel file (or an OpenDocument one) shows the text and pictures read from it, slide by slide or sheet by sheet, without its layout; a 97-2003 file (.doc, .ppt, .xls) opens on its indexed text.
- If several libraries are available, the picker at the top of the tree switches between them. **All libraries**, the default, shows them as one tree, a folder each; Stats then covers all of them, and files can still be added and changed inside the knowledge base's folder. Each tab remembers its own choice. Search and Query have pickers of their own, where any of the libraries can be ticked.
- With the status panel open, the **Details** card describes what you last clicked in the tree, or the library itself: its size, files and folders, the latest change, the file types taking the most space, its labels and those inside it, and which library and index it comes from. For a library on a remote system it shows the file and folder counts from its connection test; **Count size and types** reads the whole index there, which can take minutes on a large one.
- Settings, Libraries lists every library {appName} searches, with where it is, its state, and what it can search. **Add a library** asks where (this computer, or a remote system reached over SSH or over ACTIVATE with pw ssh, where your systems on the platform are listed, running ones first) and how: build an index of a folder, or connect a GUFI index that already exists. A build checks GUFI and the folders first, asks before making a folder that does not exist, and shows its progress in the list. A folder on this computer is kept in step as it changes. On a computer of its own, the main folder's row changes the folder, builds or updates its index, stops a build, deletes the index (never the files), and shows the index log.

## Search

One box searches four ways, and each result says how it was found: **name** (any part of a file's name), **folder** (a folder it is in has the search in its name), **words** (whole words inside files: PDFs, office documents, notes, code, and text in images), and **meaning** (passages that say the same thing in other words, so a plain question finds a report that never uses its words). Hover the label to see every way a result was found. The line under the box says which libraries are searched by meaning; **How search works** explains each kind.

What a search can find in a library depends on its **index depth**, a scale of five stops, each adding to the one before and taking longer to index: **Names**; **Text** (the words inside files); **Text + OCR** (text in images and scanned pages); **Meaning on documents** (search by meaning over the first few passages of PDF, Office, text, Markdown and image files); and **Meaning on all files** (every file with text, about three times the indexing time of Meaning on documents). The main folder and the libraries {appName} builds, on this computer or on a remote system, start at Meaning on documents. On a remote system, the first build sends {appName}'s embedding model and its packages for the Python there (about 70 MB, once, kept in `~/.activate-studio/embed`); the connection test says whether it can. Libraries added by an earlier version keep the depth they had. Change any of them in Settings, Libraries, in the Depth column. Search by meaning is the slowest part of indexing, so it runs last, in the background: a new library is searchable by name once its folders are indexed, then by its words, and the line above the results says when its meaning is still being computed. For a folder of more than about 500,000 files, Text + OCR is faster, and meaning can be added later. For the main folder and a library built on this computer, a change is applied in the background at once, over the files already indexed: only what the new depth adds runs (the text, OCR, or search by meaning), and a lower depth runs nothing. With the background sync paused it waits for **sync now**. A library built on a remote system takes a new depth at its next **Rebuild**. A search with quotes or operators, one short word, or a code such as ABC-12 matches words only.

**Searching** above the box picks the libraries: tick any of them, or **All libraries**. Search and Query each keep their own choice in this browser, and each result says which library it is in when more than one is ticked.

**Refine** splits the box in two. **Name or path** takes words, which match any part of a name or a folder, and patterns such as `*.docx` or `reports/*.pdf`, with OR and -word. **Text or meaning** takes the words inside files in full-text syntax (`AND`, `OR`, `NOT`, `"phrase"`, `prefix*`), and the same words without the operators are searched by meaning. By default matches of either kind are listed, each to its own limit, so many name matches never push out the text; with **Search text and meaning only in files whose name or path matches** on, only files that match both are listed, such as the .docx files that mention a budget. The address carries a search, so it can be bookmarked or shared.

When a search leaves a library out, or searches it only in part, a line above the results names it and says why: it is being built, its text has not been read yet, its index is missing, it did not answer within 20 seconds, it could not be reached over ssh, or macOS has not allowed {appName} to read its folder. Each name opens the library's row in Settings, Libraries. To wait for a library that did not answer in time, tick it alone in **Searching**. Query shows the same line, and the assistant's search says the same in its answer.

- Use "quotes" for a phrase, OR between alternatives, -word to leave a word out, and word* for words that start the same way.
- Results found more than one way come first, then the best of each kind: a file that uses the words more, in less text; the exact name, then names that start with the search; the passage nearest in meaning. Each library's index is read once per search for all of them.
- Label chips under the box narrow the results.
- Tick results to label them together.
- **Export CSV** saves the results listed (with **Load more** for up to 1,000) as a CSV file: library, path, name, how each was found, size in bytes, modified time in UTC, labels and the snippet. **Copy** puts the same rows on the clipboard as tab-separated text, which a spreadsheet pastes into columns. In the macOS app the file goes to Downloads.
- **GUFI commands**, under the results, shows the commands that found them, ready to copy and run in a terminal.

## Adding material

Drag files or folders onto the tree, or use **Add**, which also takes a web address. New material is searchable in about a second. Files copied in outside {appName} are picked up within minutes, or right away with **sync now** at the bottom of the navigation.

# Organize and analyze

## Labels

Labels organize files without moving them. Label a folder and everything in it carries the label, including files added later. Apply labels from the tree, the viewer, search results, or by asking the assistant; filter by them in Search, Query, and the chat's Scope.

In the knowledge base the labels are saved on the files. In any other library, including a read-only folder and an index on another machine, {appName} saves them itself, so the files are never changed; the Labels panel says so. They show in the tree, Details, Search and Query like any other, and are removed with the library.

## Query

Structured questions about the files themselves (largest, oldest, totals by type or owner) answered in milliseconds, in the libraries ticked under **Querying**. Pick a preset under **Start from** and change it, add conditions (all must match, or any), or write read-only SQL, and save the ones you reuse. Sizes take units (`10 MB`), and dates take a day (`2026-01-01`) or a time ago (`7 days`). **GUFI commands** under the form shows what ran, ready to copy, and the code button beside a saved query, or **Show SQL** for the form on screen, shows the SQL it runs and its command.

**Export CSV** above the rows saves them as a CSV file, sizes in bytes, and **Copy** puts them on the clipboard for a spreadsheet. Both take the rows the query returned, up to its limit (2,000 at most); for more, narrow the query or run the command under **GUFI commands** in a terminal.

## Stats

The knowledge base at a glance: size, file types, labels, and recent changes. Click any figure to see the files behind it. While the index is being built the page follows it, phase by phase; **Refresh** reads everything again, and **Index runs** lists recent builds and syncs with how long each phase took.

Each map shows one library, picked beside its title and remembered in this browser: the corpus topology starts on the main folder, the semantic map on the largest library that has one. **Corpus topology** shows the library's folders by size, read from its index, on this computer or on another system, and zooming in shows the folders inside them. The **semantic map** places files by what they say, in clusters, with a sample of up to 3,000 files; click a cluster for all of its files. Each cluster is named for the words that set it apart (or for its folders, where its files have no text), and the folder name shows when you hover it. Both maps zoom (wheel, pinch, + and -), move (drag or the arrow keys) and open full screen, and the semantic map turns in **3D**, where zooming magnifies toward the pointer and turning goes about the middle of the view. **Show links** draws a line from each cluster to the two that read most alike (on by default in 3D), and hovering a file draws lines to its five nearest files by meaning, with a ring where one is not on the map yet. Each legend shows its eight largest entries and **+ N more**; hovering an entry shows it on the map, and clicking it opens it. The map is remade in the background after the library's search by meaning changes. A library has a semantic map when it has search by meaning: for one the Studio builds, a depth of **Meaning on documents** or **Meaning on all files**, which the map offers to set. An index connected as it is has one only where it holds the Studio's own vectors. For a library on another system, the Studio fetches the vectors of up to 50,000 of its files over the library's own connection and draws the map here; the map says when it shows a sample, which model the vectors come from and when they were fetched, and **Refresh** fetches them again. It is also checked for changes at most hourly while you look at it, and after a build there. Nearest files are searched for on that system. A library there with no vectors says so; for one the Studio builds there, set its depth to **Meaning on documents** and Rebuild it.

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
- **AI features**: optional, and off on a new install until turned on here. On, with the OpenAI-compatible endpoint (a site AI portal, a provider, or a model server), its key (empty for a model server that takes none) and the default model; **Test** lists the models the endpoint offers or says what failed, and whether the model calls tools. An ACTIVATE account works too, set up in Settings, Model access, as is a portal that needs the Advanced options there. Off, Chat and Agents leave the sidebar, the footer drops its model line, and nothing is sent to a model; search, the library, query and statistics work as before. AI features can be turned back on at any time in Settings, General, AI features.
- **Sidebar**: which pages it shows; Workflows starts hidden on a new install. Workflows runs ACTIVATE workflows and needs an ACTIVATE credential; for workflow YAML files of your own, without ACTIVATE, use [Meridian](https://github.com/parallelworks/meridian), the open-source workflow engine.

On a new install the AI features page starts with a mission written for one person's own files and ten suggested questions; the empty chat shows four of them at a time. A blank mission, or questions left as they came, keep following the defaults.
- **Your folder**: the folder to index. In the macOS app, **Choose another folder…** opens the Mac's folder chooser; elsewhere the folders show in the page. Choosing another folder restarts {appName} on it in the macOS app, then offers to build its index; elsewhere, set `KB_ROOT` and restart. **Index depth** sets how far its index goes, from names only to meaning on all files (see Search). **Build the index** runs in the background: go on with setup, or finish it, while it runs, and the steps after show how far it has got. Indexes can be added or changed at any time in Settings, Libraries.
- **Other libraries**: GUFI indexes that already exist, on this computer (typed or picked from its folders), on another machine over ssh, or, when the pw CLI here is signed in, on one of your running clusters through ACTIVATE. A machine whose login runs only `gufi_query` (no bash, or a restricted shell) works too: give the index directory as an absolute path and leave the GUFI bin directory empty; you can browse, search and query it, but not open its files or build an index there. A machine behind a jump host works too: give its Host alias from `~/.ssh/config` (with ProxyJump there), or put `-J you@jumphost` in **SSH options**, which also takes a port, an identity file and `-o` settings such as `ConnectTimeout=20`. The connection test says which jump host it went through. {appName} keeps the ssh connection to each host open for ten minutes after it is last used, and opens it as Search or Query opens, so searches and queries a few minutes apart do not log in again. Libraries that reach the same account on the same host with the same SSH options share that connection and are searched in one pass, whether the host was given as `you@host` or as an alias; a library with SSH options of its own gets its own connection, so they always apply. Settings, Libraries shows each one as the `user@host` ssh reaches.

**Run setup again**, in Settings, General, and **Run Setup…** in the macOS app's menu open the setup later. Each step shows what is set now and changes nothing until you change it; leaving partway keeps what was already saved.

Settings, General has a tab for each part: **Appearance** (theme, accent, surface tone), **Branding** (name, icon, classification banner), **Sidebar**, **AI features** (the switch, the mission and context the assistant works from, the suggested questions under an empty chat, and image captioning), **Indexing** (search across libraries, background sync, history snapshots), **Updates**, and **Server** (the port). Settings, Model access has the model endpoint, and Settings, Libraries the folder and other libraries.

Settings, General, **Sidebar** has a switch for each page. A page switched off leaves the navigation, and a link to it opens the first page still shown. Settings and Help always show, and the switches apply to everyone using this {appName}.

On a computer of its own, the suggested questions and the mission start out written for one person's own files; change them under Settings, General, AI features.

## Model access

Settings, **Model access** shows where {appName} gets its models, with the models available or what failed; **Re-check** asks again. **What each part uses** lists the credential behind models, the Workflows page and the assistant's platform tools, libraries on ACTIVATE systems, and ChatGPT and Copilot subscriptions. All but models also run the pw CLI. When {appName} cannot find the CLI, the ACTIVATE (pw CLI) tab says so and takes its location (`command -v pw` in Terminal prints it).

- **ACTIVATE (pw CLI)**: the ACTIVATE platforms the pw CLI on this computer is signed in to, one row per context. **Use** picks one; with **Follow the pw CLI's current context** ticked, {appName} uses whichever `pw context use` last chose. **Sign in** signs the CLI in to another platform: it shows a code, you approve it in a browser on any device, and {appName} uses the new sign-in.
- **ACTIVATE API key**: a token or API key from an ACTIVATE account, which covers models and the platform, or an AI gateway key, which covers models only. The host is the platform's own, such as `platform.example.com`. An address with a path, such as `https://portal.example.org/v1`, is an API rather than a platform host: the tab says so, and **Use as an endpoint** moves it and the key to the next tab.
- **OpenAI-compatible endpoint**: any service that speaks the OpenAI API, such as a site's AI portal, Ollama, LM Studio or vLLM on this computer, the OpenAI API, or GitHub Models with a token that has the Models permission. The base URL usually ends in `/v1`. **Advanced** is for portals that differ: the header the key goes in (`Authorization: Bearer` unless set to `api-key`, `x-api-key` or none), extra headers, model names for an endpoint that does not list its models, and a query string such as `api-version=...`. Header values are stored like the key and never shown again. An endpoint covers models only; platform tools keep using the pw CLI.
- **Subscriptions through ACTIVATE** connects a ChatGPT plan, with a code you enter at OpenAI, or GitHub Copilot, once your GitHub account is linked in ACTIVATE. The platform keeps the connection, and its models appear with the others.

**Test** checks that the endpoint answers, then sends the model one small request with a tool. Chat searches your libraries through tool calls, so a model that answers without calling the tool cannot search them; Test says so, and you can pick another model or turn tool calling on where the model is served. Saving works either way.

On a shared deployment the same page holds your own key or endpoint; without one, requests use the deployment's credential (**ACTIVATE (deployment)**).

## Working offline

With no network, {appName} keeps working from its libraries: browsing, search, query, statistics and adding material. Chat keeps working with a model server on this computer, set under Model access, OpenAI-compatible endpoint. Anything that needs the network says so in one line instead of waiting: the model banner and the footer show **offline**, the Workflows page says the platform cannot be reached, and the update check shows **Offline** and tries again later.

## Networks that inspect HTTPS, and proxies

{appName} trusts the certificate authorities this computer trusts as well as its own list, so a network that inspects HTTPS works once its authority is installed on the computer; on a Mac, in Keychain Access or by the Mac's management. Behind a proxy it uses `HTTPS_PROXY` and `NO_PROXY`, and the macOS app takes the proxy from System Settings when none is set, except one given by an automatic configuration (PAC) file. A certificate or proxy problem is said in one line, for example on Settings, Updates, instead of being shown as offline.

## Using it from other tools

Other tools can use this knowledge base. pw code and other MCP clients can search it directly, and any OpenAI-compatible client can use it as a model that answers from it. Settings, "External access" has the addresses and the exact commands.

## Updates

When a newer version is available, a one-line notice at the top of every page says so; **What's new** in it lists the changes in each newer version, and a click on one reads it. In the macOS app, **Update now** downloads the release, checks its signature, installs it and restarts, and the page reloads on the new version. A copy run from a clone of the repository shows the commands that update it under **What's new**. **Later** hides the notice for a day, or until a newer version comes out; the version at the bottom of the navigation still says **update available**, and a click on it opens the **Build** panel with **Update now**. With nothing newer known, the same line offers **check for updates**, and the Build panel shows when the last check ran and **Check now**. In the macOS app, **ACTIVATE Studio, Check for Updates…** in the menu bar opens Settings, Updates and checks.

Settings, General, **Updates** has the same, and **Roll back**, which returns to the version installed before. The app checks a minute after it starts, then every 15 minutes, and when its window opens after a longer gap (time the Mac was asleep counts), unless **Check for updates automatically** is off; with **Install updates automatically when idle** on, it installs a new version once no chat reply or indexing has run for ten minutes. The first time {appName} opens on a new version, a notice says **Updated to** that version and lists its changes. When an update also changed the macOS app's own window (the title bar, the Dock icon), the app reopens on its own after an update you started; after one installed while idle, the notice and Settings, Updates offer **Reopen now**. A hosted deployment is updated by its operator and shows none of these.

To remove the macOS app, choose **Uninstall ACTIVATE Studio…** in its app menu. It lists everything it removes, with your indexes, settings and logs included unless you untick them, and never removes your documents.

**What's new**, at the top of Help, lists every version's changes, newest first. The version at the bottom of the navigation and Settings, Updates open it too.

## The index

{appName} is built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases, one per folder, holding each file's details, its text, and search vectors. Access follows the file system's permissions, and a change re-indexes only the folder it touched.

A build runs in four phases, each with its own count: **names** (every file and folder, searchable by name as soon as this phase ends), **documents** (PDFs and Office files converted to text), **text** (images, other formats, and the full-text tables), and **meaning** (embeddings for search by meaning, at the two meaning stops). At the Names depth only the first phase runs. The index line at the bottom of the navigation, Settings, Libraries, and Stats all show the same progress. Click the index line for the phases, **Stop**, and **View log**, the index log as plain text. Text and embeddings are kept between builds, so updating an index, or choosing a folder that was indexed before, redoes only what changed. A build that a shutdown or a quit cuts off is taken up again at the next start, and everything it had read is reused.

On macOS, a folder in Desktop, Documents or Downloads, in iCloud Drive, or on a removable or network volume can be read only once macOS allows it, in System Settings, Privacy & Security, Files and Folders (or Full Disk Access). Until then {appName} indexes the rest, names the folders it could not read in search and Query, and keeps what it had indexed of them.

Settings, General, **Indexing** sets how many threads each phase uses (one per processor core by default), the main folder's index depth, how documents are read (the PDF reader for files the converter cannot read, LibreOffice for previews and for Office files the built-in readers cannot read, and, from Text + OCR up, image descriptions by the vision model chosen under AI features), the speed of search by meaning (**Standard**, or **Faster**: the first 128 tokens of each passage rather than 256, in well under half the time, with slightly lower ranking), and where the index is kept. Libraries built of a folder on this computer follow the same threads and readers, and each has its own depth, in Settings, Libraries. It also lists which file types this computer can read and which index by name only: older Office files (`.ppt`, `.xls`) and OpenDocument are read with nothing to install, and for each tool still missing (OCR, poppler, LibreOffice) it says what the tool adds and gives the command that installs it here, with **Check again** and **Update the index now** once it is found.

Knowledge base for this deployment: **{kbLabel}**
