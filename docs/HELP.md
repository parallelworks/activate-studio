{appName} is a workspace over a knowledge base: a team's documents, images, spreadsheets, and code, indexed so people and the assistant can find and use them. Ask the assistant a question and it answers from those files, with a link to each source.

### Start here

1. **Ask a question.** Open **Chat** and ask in plain language, for example "what did we decide about the cooling design?". The answer links the files it used; click one to open it.
2. **Find a file.** Type a word or phrase in **Search**. Click a result to open the file at the match.
3. **Browse.** **Library** shows the folders. Click a file to view it: documents, images, PDFs, office files, and 3D models open in place.
4. **Add material.** Drag files or folders onto the Library tree. They are searchable within seconds.
5. **Run a workflow** (on the ACTIVATE platform). Open **Workflows**, choose a tile, fill in the form, and press **Run**.

# Basics

## Chat

Ask in plain language. The assistant searches the knowledge base, reads the files that matter, and links each source. Past conversations are kept and are searchable themselves.

- **Attach** files or images with the paperclip; they are added to the library and indexed.
- **Scope**, at the top right, limits answers to material with the labels you choose.
- Type **/** to pick a skill, a tool, or a persona to use.
- Ask it to run work on a connected HPC system; it follows the run and explains a failure if there is one.
- **Select chats**, above New chat, deletes old conversations together.
- In a shared session, add your own model key under Settings, "Model access", to chat as yourself.

## Library

The folder tree beside a viewer. Markdown, code, images, PDFs, office documents, and STL and STEP models open in place.

- **Right-click** a file or folder to label it, add a folder, or delete it.
- **Find**, in the viewer's header, searches the open file.
- **Indexed text** shows what search sees for a file, including text read from images.
- If several libraries are available, the picker at the top of the tree switches between them.

## Search

One box finds exact words (inside PDFs, office documents, and images too), related meaning, and file names. Each result says which kind of match it was.

- Use "quotes" for a phrase, OR between alternatives, -word to leave a word out, and word* for words that start the same way.
- Label chips under the box narrow the results.
- Tick results to label them together.

## Adding material

Drag files or folders onto the tree, or use **Add**, which also takes a web address. New material is searchable in about a second. Files copied in outside {appName} are picked up within minutes, or right away with **sync now** at the bottom of the navigation.

# Organize and analyze

## Labels

Labels organize files without moving them. Label a folder and everything in it carries the label, including files added later. Apply labels from the tree, the viewer, search results, or by asking the assistant; filter by them in Search, Query, and the chat's Scope.

## Query

Structured questions about the files themselves (largest, newest, totals by type) answered in milliseconds. Start from a canned query, use the builder, or write read-only SQL, and save the ones you reuse.

## Stats

The knowledge base at a glance: size, file types, labels, and recent changes. Click any figure to see the files behind it.

# Run work

## Workflows

On the ACTIVATE platform, the Workflows tab shows the workflows chosen for {appName}. Open a tile, fill in the form, and press **Run**; **Validate** checks the inputs without running anything. Runs use your own account.

- Saved configurations appear as presets at the top of the form.
- A workflow from GitHub that asks for access to your account variables waits until you tick the approval.
- Administrators choose the set under Settings, "Workflows".
- Apps that runs open, such as a design explorer or a notebook, are listed under **Sessions**. Open one to use it here, or press **New tab**. A session started from chat also appears in that conversation.
- If a session shows as empty or refused, open it once in its own tab to sign in to it, then press **Reload**.

## Agents

When a request splits into parts, the assistant can hand each part to an agent working in parallel. The Agents tab has three pages:

- **Tasks**: each delegated task, its agents, the machine they run on, and their output. Approve or deny what an agent asks to do, or give a working agent direction.
- **Fleet**: standing agents that wake on a schedule or when something changes, and ask you when they need a decision.
- **Personas and skills**: the files that shape how the assistant behaves.

Delegation is turned on under Settings, "External access".

# More

## Getting around

The address bar follows what you are looking at, so back and forward work and a copied link opens the same place for someone else.

- **Search**, at the top of the navigation, searches the knowledge base; Ctrl+K (⌘K on a Mac) jumps to it from anywhere.
- The navigation groups its pages under **Knowledge** and **Run**; click a group's name to fold it away, and {appName} remembers.
- **Install app**, in the navigation, installs {appName} as a desktop app in its own window. If a classification banner is shown, the chevron in Chrome's title bar moves the banner into the title bar.
- The bottom of the navigation shows who you are signed in as, whether the models are reachable, and the state of the index; click any of them for details.

## Using it from other tools

Other tools can use this knowledge base. pw code and other MCP clients can search it directly, and any OpenAI-compatible client can use it as a model that answers from it. Settings, "External access" has the addresses and the exact commands.

## The index

{appName} is built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases, one per folder, holding each file's details, its text, and search vectors. Access follows the file system's permissions, and a change re-indexes only the folder it touched.

Knowledge base for this deployment: **{kbLabel}**
