{appName} is a workspace over a knowledge base: the documents, images, spreadsheets, and code a team accumulates, indexed so both people and AI can find and use them. A chat assistant grounded in the corpus sits beside direct tools for browsing, searching, querying, and adding material. When connected to the Parallel Works ACTIVATE platform, the assistant can also reason over, preview, run, and monitor the account's workflows.

The intent is an interaction layer for AI-driven engineering: one place where the knowledge base, the retrieval layer, the models, and the execution layer meet. Every substantive answer is built by searching and reading the actual corpus, with file paths cited so claims can be checked.

## Chat

Ask in plain language; the assistant searches, reads the files that matter, and cites its paths. It also knows the platform.

- Recommends workflows from the account catalog, with descriptions and tags, including how they compose.
- Renders a workflow DAG in the Library when you ask to see one.
- Validates runs with a dry run; launches a real run only when you explicitly ask.
- Lists and inspects runs, including errors and log tails.
- "Show me &lt;file&gt;" returns a link that opens it in the Library viewer.
- Attach files or images with the paperclip; they are filed into the library, indexed, and their content (including text read from images) is part of the conversation.
- Slash commands invoke extensions directly: /skill_name applies a skill's instructions to your request, /tool_name runs a tool, /agent_name adopts an agent file for one message. /help lists everything available.

## Library

The file tree beside a viewer. Markdown and code render directly; images and PDFs show the original; office documents get a PDF preview; STL and STEP models open in an interactive 3D viewer.

- The **Indexed text** tab shows exactly what the search index holds for a file.
- For images that is OCR text plus a model-written description.
- **Delete** removes a file from the corpus and the index together.
- Panes resize at the boundary and collapse from the header.

## Search

One box, three retrieval modes at once, each result labeled by the mode that found it.

- **Full text**: exact words, including inside DOCX, PDF, PPTX, and text on images.
- **Semantic**: meaning-based, so related phrasing matches.
- **Filename**: name fragments.

## Query

Structured questions about the corpus itself, answered from the file index in tens of milliseconds.

- **Canned**: largest, newest, oldest, recently changed, totals by extension, biggest directories.
- **Builder**: filters, grouping, sorting, and subtree scope, no SQL needed.
- **SQL**: raw read-only SELECT over the index tables.
- **Saved queries**: name one, rerun it in a click.

## Adding material

Everything added becomes searchable in about a second.

- Drag files or whole folders onto the tree, or use **Add**; the destination folder is created on demand.
- Add by URL: web pages are reduced to text with the source recorded; PDFs saved as-is.
- Files that arrive outside the interface are picked up by the background sync within minutes, or immediately with **sync now**.

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
- [Fastify](https://fastify.dev/), [React](https://react.dev/), and [Vite](https://vite.dev/): the server and the interface.
