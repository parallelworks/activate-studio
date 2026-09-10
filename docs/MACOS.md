# Running on a Mac

The Studio runs from a clone on macOS. This page is the whole path, from
nothing installed to a working index, in the order it is worth doing:
run it first, add search once you know it works, add a model last.

Everything here is tested on every pull request by a `macos-15` job in
CI, which builds the server and web app and runs the full suite.

## 1. Prerequisites

```
brew install node pnpm
```

Node 22 or newer. Nothing else is needed to start.

## 2. Run it

```
git clone https://github.com/parallelworks/activate-studio
cd activate-studio
pnpm install
pnpm build
pnpm start                       # http://localhost:4080
```

With no `KB_ROOT` set, the corpus is `knowledge-base/` beside the code
and is created with a few starter folders on first start. Point it at
your own material instead with `KB_ROOT=/path/to/corpus pnpm start`, and
put anything you want to keep across restarts in a `.env` at the repo
root.

At this point the file tree, the viewers, document previews, grep
search, and the assistant all work. Indexed search and the query page
answer with a note that no index exists yet, which is step 3.

## 3. Add GUFI, for indexed search

The index comes from [GUFI](https://github.com/mar-file-system/GUFI),
LANL's Grand Unified File Index. Install it from GUFI itself if your
site or a package provides it; the Studio finds `gufi_query` on `PATH`
or under `/opt/gufi/bin`. To build it from source here:

```
indexer/setup_gufi.sh            # Homebrew toolchain, GUFI, embedding model
KB_ROOT=/path/to/corpus indexer/reindex.sh
```

The script installs what GUFI's own macOS CI uses, which is the part
worth knowing about: Homebrew's LLVM rather than Apple's clang, since
libomp is not wired into the system toolchain, plus the GNU utilities
GUFI's scripts expect, placed ahead on `PATH` for the build only.

*The fast build:* GUFI's AI dependencies, `sqlite-vec` and
`sqlite-lembed` with llama.cpp inside it, are what semantic search needs
and are also the hard part of the build on a Mac. They are on by
default. To skip them:

```
GUFI_AI=0 indexer/setup_gufi.sh
```

That build is quick and keeps metadata, filename, and full-text search,
including text extracted from Office documents and PDFs. Only the
semantic blend is missing, and it can be added later by rebuilding.

What you get in each state:

| | Browse, view, grep, chat | Filename and full-text search, query page, statistics | Semantic search |
|---|---|---|---|
| No GUFI | yes | no | no |
| GUFI, `GUFI_AI=0` | yes | yes | no |
| GUFI with AI deps | yes | yes | yes |

*Document extraction:* Word, PowerPoint, Excel and PDF files extract to
Markdown through a bundled converter, so they need nothing extra.
`brew install tesseract poppler` adds OCR for scanned pages, and
`brew install --cask libreoffice` gives visual previews of Office files.

## 4. Point it at a model

The assistant talks to any OpenAI-compatible endpoint:

```
OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama pnpm start
```

Ollama, llama.cpp's server, vLLM, and hosted APIs all work. The endpoint
must serve `/models` and streaming `/chat/completions`, and it must
support tool calling for the assistant to search the corpus and read
files. On the ACTIVATE platform the gateway is used automatically and
none of this is needed.

## Troubleshooting

*`gufi_query: command not found` after installing:* the Studio looks on
`PATH` and in `/opt/gufi/bin`. Set `GUFI_PREFIX` when building to choose
somewhere else, and add its `bin` to `PATH`.

*The GUFI build fails on OpenMP or `omp.h`:* Apple's clang is being used
instead of Homebrew's LLVM. `indexer/setup_gufi.sh` sets `CC` and `CXX`
for you; if you are building GUFI by hand, do the same.

*The GUFI build fails inside llama.cpp or sqlite-lembed:* that is the AI
dependency path. Build with `GUFI_AI=0` to get everything except
semantic search, and treat the vector build as a separate exercise.

*Searches return nothing after adding files:* the index updates when the
Studio writes a file itself. For material copied in from the outside,
re-run `indexer/reindex.sh`, or use the Library's upload so the subtree
is re-indexed for you.

*Apple silicon versus Intel:* both work. Homebrew installs to different
prefixes on each, which is why the scripts ask `brew --prefix` rather
than hardcoding paths.
