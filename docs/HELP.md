{appName} is a workspace over a knowledge base: the documents, images, spreadsheets, and code a team accumulates, indexed so both people and AI can find and use them. A chat assistant grounded in the corpus sits beside direct tools for browsing, searching, querying, labeling, and adding material. When connected to the Parallel Works ACTIVATE platform, the assistant can also reason over, preview, run, and monitor the account's workflows.

The intent is an interaction layer for AI-driven engineering: one place where the knowledge base, the retrieval layer, the models, and the execution layer meet. Every substantive answer is built by searching and reading the actual corpus, with each citation a link that opens the source in the Library viewer.

## Chat

Ask in plain language; the assistant searches, reads the files that matter, and cites clickable paths. It remembers past sessions: every conversation is exported into the corpus and searchable, so "what did we discuss about X" works.

- Shows things inline, not just as links: images, document pages, an interactive workflow DAG window, and an interactive 3D model window can all appear inside the reply.
- **Scope** (top right of the thread) limits retrieval for the conversation to selected labels; the restriction is enforced server-side.
- Recommends workflows from the account catalog; validates runs with a dry run and launches a real run only when you explicitly ask; lists and inspects runs, including errors and log tails.
- Attach files or images with the paperclip; they are filed into the library, indexed, and their content (including text read from images) is part of the conversation. Clicking an attachment tile later opens the file in the Library.
- Slash commands invoke extensions directly: /skill_name applies a skill's instructions, /tool_name runs a tool, /agent_name adopts an agent file for one message. /help lists everything available.
- The thinking line above a reply expands to show the reasoning and each tool call as it happens, and stays with the message afterward.

## Library

The file tree beside a viewer. Markdown and code render directly; images show the original; PDFs and office documents render as page images; STL and STEP models open in an interactive 3D viewer.

- The **Indexed text** tab shows exactly what the search index holds for a file; for images that is OCR text plus a model-written description.
- **Delete** removes a file from the corpus and the index together.
- Panes resize at the boundary and collapse from the header.

## Search

One box, three retrieval modes at once, each result labeled by the mode that found it: **full text** (exact words, including inside DOCX, PDF, PPTX, and text on images), **semantic** (meaning-based), and **filename**.

- Label chips under the box filter results to material carrying those labels.
- Hover a result for its checkbox; **Select all** plus **Labels…** applies labels to the whole result set in one action.
- **Load more matches** extends a search to up to 1,000 results.

## Query

Structured questions about the corpus itself, answered from the file index in tens of milliseconds.

- **Canned**: largest, newest, oldest, recently changed, totals by extension, biggest directories.
- **Builder**: filters (including labels), grouping, sorting, and subtree scope, no SQL needed. **Reset** clears the form and results.
- **SQL**: raw read-only SELECT over the index tables.
- **Saved queries**: name one, rerun it in a click; a few starter examples ship with a fresh deployment.
- Result rows with a path column select the same way search results do, for bulk labeling.

## Labels

Labels organize the corpus without moving files, and they follow inheritance: labeling a directory covers everything under it, now and later, without touching the files.

- Apply from the tree (select mode or a row's tag button), the viewer's **Labels** button, search or query multi-select, or by asking the assistant.
- Filter by label in Search, the Query builder, the chat Scope control, and the assistant's own retrieval.
- A file's own labels show green in the viewer; inherited ones gray.

## Adding material

Everything added becomes searchable in about a second.

- Drag files or whole folders onto the tree, or use **Add**; the destination folder is created on demand.
- Add by URL: web pages are reduced to text with the source recorded; PDFs saved as-is.
- Files that arrive outside the interface are picked up by the background sync within minutes, or immediately with **sync now**.

## Stats

Corpus health at a glance, and every element is a shortcut: storage rows and label pills open a prefilled query listing the matching files, largest and recently-changed rows open in the viewer, and the activity card tracks conversations, exported transcripts, and attachments.

## Getting around

The address bar tracks what you are looking at: open documents and views live in the URL, so refresh restores your place, browser back and forward walk your path, and a copied link drops someone else exactly where you were. The footer shows the platform-verified identity and index health; click either for details.

## The index

Built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases mirroring the directory structure, holding metadata, extracted text, and embedding vectors. Access control is inherited from the filesystem, and additions re-index only the touched folder.

Corpus root for this deployment: **{kbLabel}**

## Built on

Open technologies, each doing the job it was built for.

- [GUFI](https://github.com/mar-file-system/GUFI), the Grand Unified File Index from Los Alamos National Laboratory: the metadata, full-text, and vector index.
- [sqlite-vec](https://github.com/asg017/sqlite-vec) and [sqlite-lembed](https://github.com/asg017/sqlite-lembed): embedding storage and on-index embedding with a local GGUF model.
- [@parallelworks/ai-chat](https://www.npmjs.com/package/@parallelworks/ai-chat): the chat interface components, driven by a custom adapter against any OpenAI-compatible endpoint.
- [Streamdown](https://github.com/vercel/streamdown): streaming markdown rendering in chat and the viewer.
- [three.js](https://threejs.org/) and [occt-import-js](https://github.com/kovacsv/occt-import-js) (Open CASCADE compiled to WebAssembly): the 3D model viewer and STEP conversion.
- [Tesseract](https://github.com/tesseract-ocr/tesseract): OCR for text inside images.
- [poppler](https://poppler.freedesktop.org/): PDF page rendering for previews.
- [Fastify](https://fastify.dev/), [React](https://react.dev/), and [Vite](https://vite.dev/): the server and the interface.
