# ACTIVATE Studio

[![ci](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml/badge.svg)](https://github.com/parallelworks/activate-studio/actions/workflows/ci.yml)

A web workspace over a folder of documents. It indexes the folder, including the text inside PDFs, office files, and images, and puts a chat assistant over it that answers from those files and links its sources, alongside search, browsing, and structured queries. It runs anywhere Node and GUFI run, with any OpenAI-compatible model. On the Parallel Works ACTIVATE platform it can also run workflows and agents on connected HPC and cloud systems.

## Quick start

**On ACTIVATE:** run the workflow in [`deploy/workflow.yaml`](deploy/workflow.yaml) on a connected resource and set its "Knowledge Base Directory" to your folder. It builds the index and opens the Studio as a session. The platform's models work with no setup.

**On your own machine** (Linux or macOS, with Node 26 and pnpm):

```
git clone https://github.com/parallelworks/activate-studio && cd activate-studio
pnpm install && pnpm build
indexer/setup_gufi.sh                      # once: builds the GUFI index engine
export KB_ROOT=/path/to/your/folder
export OPENAI_BASE_URL=https://api.openai.com/v1 OPENAI_API_KEY=<your key>
indexer/reindex.sh                         # indexes the folder
pnpm start                                 # then open http://localhost:4080
```

Without `KB_ROOT`, it opens a small sample folder. Any OpenAI-compatible endpoint works in place of OpenAI's. On a Mac, see [`docs/MACOS.md`](docs/MACOS.md).

## What it does

- **Chat** answers from your files and links each source.
- **Search** finds exact words, related meaning, and file names in one box.
- **Library** browses and views documents, images, PDFs, office files, and 3D models.
- **Query** answers structured questions about the files, such as the largest or newest, or runs read-only SQL.
- **Workflows**, on ACTIVATE, runs chosen platform workflows from their own forms.
- **Agents** take the parts of a larger request in parallel; you can watch them, approve what they ask to do, and steer them.
- **Other tools**, such as pw code, can search the same knowledge base over MCP or an OpenAI-compatible endpoint.

The in-app Help is the user guide: [`docs/HELP.md`](docs/HELP.md), with the details in [`docs/REFERENCE.md`](docs/REFERENCE.md).

## Documentation

| Document | Covers |
|---|---|
| [`docs/HELP.md`](docs/HELP.md) | the user guide, shown in the app and read by the assistant |
| [`docs/REFERENCE.md`](docs/REFERENCE.md) | the details behind the user guide, part by part |
| [`docs/DEPLOY.md`](docs/DEPLOY.md) | running it on ACTIVATE, in a container, or as a plain server, and connecting models |
| [`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) | settings, branding, identity, and the assistant's tools |
| [`docs/LIBRARIES.md`](docs/LIBRARIES.md) | several indexes at once, and which parts of the app appear |
| [`docs/WORKFLOWS.md`](docs/WORKFLOWS.md) | the Workflows tab |
| [`docs/AGENTS.md`](docs/AGENTS.md) | delegated agents |
| [`docs/MULTI-USER.md`](docs/MULTI-USER.md) | several people on one deployment |
| [`docs/MACOS.md`](docs/MACOS.md) | running on macOS |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | how the index, retrieval, and server work |

## Built on

- [GUFI](https://github.com/mar-file-system/GUFI) (Los Alamos National Laboratory): the metadata, full-text, and vector index.
- [sqlite-vec](https://github.com/asg017/sqlite-vec) and [sqlite-lembed](https://github.com/asg017/sqlite-lembed): vector storage and embedding with a local model.
- [@parallelworks/ui](https://www.npmjs.com/package/@parallelworks/ui): the chat interface and the platform's workflow forms.
- [Streamdown](https://github.com/vercel/streamdown), [three.js](https://threejs.org/), [occt-import-js](https://github.com/kovacsv/occt-import-js), [Tesseract](https://github.com/tesseract-ocr/tesseract), [Fastify](https://fastify.dev/), [React](https://react.dev/), and [Vite](https://vite.dev/).

## Contributing

Contributions are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) covers setup, the repository layout, tests, pull requests, and releases. Report security issues through [`SECURITY.md`](SECURITY.md).
