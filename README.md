# ACTIVATE Studio

[![ci](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml/badge.svg)](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml)

A web workspace over a folder of documents. It indexes the folder, including the text inside PDFs, office files, and images, and puts a chat assistant over it that answers from those files and links its sources, alongside search, browsing, and structured queries. It runs anywhere Node and GUFI run, with any OpenAI-compatible model. On the Parallel Works ACTIVATE platform it can also run workflows and agents on connected HPC and cloud systems.

## Quick start

**On a Mac (Apple silicon, macOS 14 or later):** `curl -fsSL https://github.com/parallelworks/activate-studio/releases/download/macos/install.sh | bash` installs the ACTIVATE Studio app in `~/Applications`, which runs the Studio on your own folder with nothing else to install and no administrator rights, including on a managed Mac that does not allow LaunchAgents. The disk image on the [macOS release](https://github.com/parallelworks/activate-studio/releases/tag/macos) page does the same: double-click the app in it and choose Install. To remove it, choose **Uninstall ACTIVATE Studio…** in the app menu: it lists what it removes (the app, and by default its indexes, settings and logs) and never your documents. See [`docs/MACOS.md`](docs/MACOS.md).

**On Linux (x86_64, glibc 2.28 or newer, so RHEL 8 and 9 and their rebuilds, Ubuntu 20.04 and later, Debian 11 and later):** a per-user install with no administrator rights (`activate-studio/install.sh` from the unpacked archive) or an administrator's `.deb` (Ubuntu, Debian) or `.rpm` (RHEL, Rocky, Alma) puts the same Studio in its own window, where WebKitGTK is installed, and in your browser where it is not. See [`docs/LINUX-APP.md`](docs/LINUX-APP.md).

The macOS and Linux apps are built as the Go version: the Go server (`server-go`) with GUFI, tesseract and the embedding model, and no Node or Python. Installed copies of v1.188 and earlier move to it through their normal update ([`docs/UPDATES.md`](docs/UPDATES.md), "The Go version"). So is the container image on ghcr.io, built from the release's Linux app with `deploy/app-go.def`.

**On Windows:** the Linux app runs inside WSL2 (Windows Subsystem for Linux), in its own window through WSLg or in a Windows browser, and indexes Windows folders through `/mnt/c`. There is no native Windows build, and this setup has been tested only in containers so far. A browser can also open a Studio that runs on a Linux machine. See [`docs/WINDOWS.md`](docs/WINDOWS.md).

**On ACTIVATE:** run the workflow in [`deploy/workflow.yaml`](deploy/workflow.yaml) on a connected resource and set its "Knowledge Base Directory" to your folder. It builds the index and opens the Studio as a session. The platform's models work with no setup.

**On your own machine** (Linux or macOS, with git, Node 26 and pnpm; on Ubuntu or Debian `indexer/setup_gufi.sh` installs the build tools and libraries it needs with apt, so it asks for sudo unless run as root; on macOS it uses Homebrew):

```
git clone https://github.com/parallelworks/activate-studio && cd activate-studio
pnpm install && pnpm build
indexer/setup_gufi.sh                      # once: builds the GUFI index engine (release 0.8.1)
export KB_ROOT=/path/to/your/folder
export OPENAI_BASE_URL=https://api.openai.com/v1 OPENAI_API_KEY=<your key>
indexer/reindex.sh                         # indexes the folder (or later: Settings, Indexing, Libraries, Build index)
pnpm start                                 # then open http://localhost:4080
```

Without `KB_ROOT`, it opens a small sample folder. Any OpenAI-compatible endpoint works in place of OpenAI's, including a site's AI portal; Settings, Model access takes the headers, model names and query string a portal may need, and its Test says whether the model calls tools, which chat needs to search your files. A clone on a Mac is covered in [`docs/MACOS.md`](docs/MACOS.md) too.

## What it does

- **Chat** answers from your files and links each source. Type @ to hand it a file from the library, set how long the model reasons with Effort, ask it to save a report or table as a PDF, Word or Excel file, and have it move, rename, copy or delete library files (a delete waits for your approval).
- **Search** finds file names, the words inside files, and, where it is on, meaning: passages that say the same thing in other words.
- **Library** browses and views documents, CSV tables, images, PDFs, office files, and 3D models, takes files and whole folders with their structure, and describes the folder you select: its size, contents, and labels. A library can name a second host over ssh that has its files, for an index whose own host does not. Libraries can be exported as a library list file that others import into their own Studio, in setup or in Settings, so an administrator shares one setup, including an index built as root, and each person connects as themselves and sees what their own permissions allow.
- **Query** answers structured questions about the files, such as the largest or newest, or runs read-only SQL. Query and search results export to CSV.
- **Stats** maps each library, here or on another system: its folders by size, and its files by what they say.
- **Workflows**, on ACTIVATE, runs chosen platform workflows from their own forms, and opens the sessions they start.
- **Code**, on ACTIVATE, lists your pw code sessions across the platform's machines, shows each one's conversation, and opens it in ACTIVATE.
- **Agents** take the parts of a larger request in parallel; you can watch them, approve what they ask to do, and steer them.
- **Sign-in**, on a Studio several people share: through the ACTIVATE session proxy, or through ACTIVATE or any other OpenID Connect provider ([`docs/DEPLOY.md`](docs/DEPLOY.md)).
- **Other tools**, such as pw code, can search and read the same knowledge base over MCP (search, the text the index holds, and the original files) or an OpenAI-compatible endpoint.

### Index depth

Each library the Studio builds has an index depth, set on a scale in Settings, Indexing, Libraries, in setup, and when the library is added. Each stop adds to the one before and takes longer to index:

| Depth | What the index holds |
|---|---|
| Names | Names, sizes, dates and owners of every file and folder. |
| Text | The words inside files: text, code, PDFs and Office documents. |
| Text + OCR | Text in images and scanned PDF pages, read by tesseract. |
| Meaning on documents | Search by meaning over the first four passages of each PDF, Office, text, Markdown and image file. The default for the main folder and the libraries the Studio builds, here or on a remote system, computed last, after names and text are searchable. |
| Meaning on all files | Search by meaning over up to twelve passages of every file with text. |

Many folders (code, data, simulation output) need no more than Text. Settings, Indexing, Building also sets the speed of search by meaning: **Standard** embeds the first 256 tokens of each passage, and **Faster** the first 128, in well under half the time with slightly lower ranking. Embeddings come from all-MiniLM-L6-v2 through ONNX Runtime (int8), which the macOS app and the container carry; a clone gets it from `indexer/setup_gufi.sh`, or `python3 indexer/fetch_embed.py --deps indexer/.deps --model index/models/minilm-l6`. A build on a remote system embeds with the same model, which the first build there sends with its packages for the Python there. An index connected as it is stays as it was built.

The in-app Help is the user guide: [`docs/HELP.md`](docs/HELP.md), with the details in [`docs/REFERENCE.md`](docs/REFERENCE.md). Its What's new page shows [`CHANGELOG.md`](CHANGELOG.md).

## Documentation

| Document | Covers |
|---|---|
| [`docs/HELP.md`](docs/HELP.md) | the user guide, shown in the app and read by the assistant |
| [`docs/REFERENCE.md`](docs/REFERENCE.md) | the details behind the user guide, part by part |
| [`docs/DEPLOY.md`](docs/DEPLOY.md) | running it on ACTIVATE, in a container, or as a plain server, and connecting models |
| [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) | settings, branding, identity, and the assistant's tools |
| [`docs/LIBRARIES.md`](docs/LIBRARIES.md) | several indexes at once, building an index of a folder here or on a remote system over ssh or pw ssh, library lists to export and import, the Library's Details card, and which parts of the app appear |
| [`docs/WORKFLOWS.md`](docs/WORKFLOWS.md) | the Workflows tab |
| [`docs/AGENTS.md`](docs/AGENTS.md) | delegated agents |
| [`docs/ACCESS-CONTROL.md`](docs/ACCESS-CONTROL.md) | who can find what: GUFI and the file system's permissions, deploying for user and group access, and an administrator's index |
| [`docs/MULTI-USER.md`](docs/MULTI-USER.md) | several people on one deployment |
| [`docs/MACOS.md`](docs/MACOS.md) | the macOS app, and running from a clone on macOS |
| [`docs/UPDATES.md`](docs/UPDATES.md) | update channels (Stable and Preview), installing another version, the state snapshot and format, and what the Go version must keep |
| [`docs/LINUX-APP.md`](docs/LINUX-APP.md) | the Linux app: per-user install, `.deb` and `.rpm`, requirements, update and uninstall, the install layout |
| [`docs/GUFI-MACOS.md`](docs/GUFI-MACOS.md) | GUFI built for macOS 14 and later: the published archive, building and checking it |
| [`docs/WINDOWS.md`](docs/WINDOWS.md) | the Linux app on Windows through WSL2: install, window or browser, Windows folders under `/mnt/c`, labels and sync there |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | how the index, retrieval, and server work |

## Built on

- [GUFI](https://github.com/mar-file-system/GUFI) (Los Alamos National Laboratory): the metadata, full-text, and vector index.
- [sqlite-vec](https://github.com/asg017/sqlite-vec) and [sqlite-lembed](https://github.com/asg017/sqlite-lembed): vector storage, and embedding with a local model where ONNX Runtime is not installed.
- [ONNX Runtime](https://onnxruntime.ai/), [tokenizers](https://github.com/huggingface/tokenizers) and [all-MiniLM-L6-v2](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2): embeddings for search by meaning.
- [@parallelworks/ui](https://www.npmjs.com/package/@parallelworks/ui): the chat interface, the platform's workflow forms and run graph, the log viewer and code editor, and the controls throughout (buttons, fields, menus, dialogs, tabs, tables).
- [Streamdown](https://github.com/vercel/streamdown), [three.js](https://threejs.org/), [occt-import-js](https://github.com/kovacsv/occt-import-js), [Tesseract](https://github.com/tesseract-ocr/tesseract), [Fastify](https://fastify.dev/), [React](https://react.dev/), and [Vite](https://vite.dev/).

## Contributing

Contributions are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) covers setup, the repository layout, tests, pull requests, and releases. Report security issues through [`SECURITY.md`](SECURITY.md).
