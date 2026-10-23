# ACTIVATE Studio

[![ci](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml/badge.svg)](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml)

A web workspace over a folder of documents. It indexes the folder, including the text inside PDFs, office files, and images, and puts a chat assistant over it that answers from those files and links its sources, alongside search, browsing, and structured queries. It runs anywhere Node and GUFI run, with any OpenAI-compatible model. On the Parallel Works ACTIVATE platform it can also run workflows and agents on connected HPC and cloud systems.

## Quick start

**On a Mac (Apple silicon, macOS 14 or later):** `curl -fsSL https://github.com/parallelworks/activate-studio/releases/download/macos/install.sh | bash` installs the ACTIVATE Studio app in `~/Applications`, which runs the Studio on your own folder with nothing else to install and no administrator rights, including on a managed Mac that does not allow LaunchAgents. The disk image on the [macOS release](https://github.com/parallelworks/activate-studio/releases/tag/macos) page does the same: double-click the app in it and choose Install. To remove it, choose **Uninstall ACTIVATE Studio…** in the app menu: it lists what it removes (the app, and by default its indexes, settings and logs) and never your documents. See [`docs/MACOS.md`](docs/MACOS.md).

**On ACTIVATE:** run the workflow in [`deploy/workflow.yaml`](deploy/workflow.yaml) on a connected resource and set its "Knowledge Base Directory" to your folder. It builds the index and opens the Studio as a session. The platform's models work with no setup.

**On your own machine** (Linux or macOS, with Node 26 and pnpm):

```
git clone https://github.com/parallelworks/activate-studio && cd activate-studio
pnpm install && pnpm build
indexer/setup_gufi.sh                      # once: builds the GUFI index engine
export KB_ROOT=/path/to/your/folder
export OPENAI_BASE_URL=https://api.openai.com/v1 OPENAI_API_KEY=<your key>
indexer/reindex.sh                         # indexes the folder (or later: Settings, Libraries, Build index)
pnpm start                                 # then open http://localhost:4080
```

Without `KB_ROOT`, it opens a small sample folder. Any OpenAI-compatible endpoint works in place of OpenAI's. A clone on a Mac is covered in [`docs/MACOS.md`](docs/MACOS.md) too.

## What it does

- **Chat** answers from your files and links each source.
- **Search** finds file names, the words inside files, and, where it is on, meaning: passages that say the same thing in other words.
- **Library** browses and views documents, images, PDFs, office files, and 3D models, and describes the folder you select: its size, contents, and labels.
- **Query** answers structured questions about the files, such as the largest or newest, or runs read-only SQL.
- **Workflows**, on ACTIVATE, runs chosen platform workflows from their own forms, and opens the sessions they start.
- **Agents** take the parts of a larger request in parallel; you can watch them, approve what they ask to do, and steer them.
- **Other tools**, such as pw code, can search the same knowledge base over MCP or an OpenAI-compatible endpoint.

### Index depth

Each library the Studio builds has an index depth, set on a scale in Settings, Libraries, in setup, and when the library is added. Each stop adds to the one before and takes longer to index:

| Depth | What the index holds |
|---|---|
| Names | Names, sizes, dates and owners of every file and folder. |
| Text | The words inside files: text, code, PDFs and Office documents. |
| Text + OCR | Text in images and scanned PDF pages, read by tesseract. Libraries other than the main folder start here. |
| Meaning on documents | Search by meaning over the first four passages of each PDF, Office, text, Markdown and image file. The main folder's default. |
| Meaning on all files | Search by meaning over up to twelve passages of every file with text. |

Many folders (code, data, simulation output) need no more than Text. Settings, Indexing also sets the speed of search by meaning: **Standard** embeds the first 256 tokens of each passage, and **Faster** the first 128, in well under half the time with slightly lower ranking. Embeddings come from all-MiniLM-L6-v2 through ONNX Runtime (int8), which the macOS app and the container carry; a clone gets it from `indexer/setup_gufi.sh`, or `python3 indexer/fetch_embed.py --deps indexer/.deps --model index/models/minilm-l6`. An index connected as it is stays as it was built.

The in-app Help is the user guide: [`docs/HELP.md`](docs/HELP.md), with the details in [`docs/REFERENCE.md`](docs/REFERENCE.md). Its What's new page shows [`CHANGELOG.md`](CHANGELOG.md).

## Documentation

| Document | Covers |
|---|---|
| [`docs/HELP.md`](docs/HELP.md) | the user guide, shown in the app and read by the assistant |
| [`docs/REFERENCE.md`](docs/REFERENCE.md) | the details behind the user guide, part by part |
| [`docs/DEPLOY.md`](docs/DEPLOY.md) | running it on ACTIVATE, in a container, or as a plain server, and connecting models |
| [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) | settings, branding, identity, and the assistant's tools |
| [`docs/LIBRARIES.md`](docs/LIBRARIES.md) | several indexes at once, building an index of a folder here or on a remote system over ssh or pw ssh, the Library's Details card, and which parts of the app appear |
| [`docs/WORKFLOWS.md`](docs/WORKFLOWS.md) | the Workflows tab |
| [`docs/AGENTS.md`](docs/AGENTS.md) | delegated agents |
| [`docs/MULTI-USER.md`](docs/MULTI-USER.md) | several people on one deployment |
| [`docs/MACOS.md`](docs/MACOS.md) | the macOS app, and running from a clone on macOS |
| [`docs/GUFI-MACOS.md`](docs/GUFI-MACOS.md) | GUFI built for macOS 14 and later: the published archive, building and checking it |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | how the index, retrieval, and server work |

## Built on

- [GUFI](https://github.com/mar-file-system/GUFI) (Los Alamos National Laboratory): the metadata, full-text, and vector index.
- [sqlite-vec](https://github.com/asg017/sqlite-vec) and [sqlite-lembed](https://github.com/asg017/sqlite-lembed): vector storage, and embedding with a local model where ONNX Runtime is not installed.
- [ONNX Runtime](https://onnxruntime.ai/), [tokenizers](https://github.com/huggingface/tokenizers) and [all-MiniLM-L6-v2](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2): embeddings for search by meaning.
- [@parallelworks/ui](https://www.npmjs.com/package/@parallelworks/ui): the chat interface, the platform's workflow forms and run graph, the log viewer and code editor, and the controls throughout (buttons, fields, menus, dialogs, tabs, tables).
- [Streamdown](https://github.com/vercel/streamdown), [three.js](https://threejs.org/), [occt-import-js](https://github.com/kovacsv/occt-import-js), [Tesseract](https://github.com/tesseract-ocr/tesseract), [Fastify](https://fastify.dev/), [React](https://react.dev/), and [Vite](https://vite.dev/).

## Contributing

Contributions are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) covers setup, the repository layout, tests, pull requests, and releases. Report security issues through [`SECURITY.md`](SECURITY.md).
