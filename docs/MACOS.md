# Running on a Mac

There are two ways to run the Studio on a Mac: the app, which carries
everything it needs, and a clone of this repository. The app is the short
path; the clone is for working on the Studio itself.

## The app (Apple silicon)

*Install:* in Terminal,

```
curl -fsSL https://github.com/parallelworks/activate-studio/releases/latest/download/install.sh | bash
```

This puts `ACTIVATE Studio.app` in `~/Applications`, without
administrator rights, and opens it. The alternative is the disk image,
`activate-studio-macos-arm64.dmg` on the
[releases](https://github.com/parallelworks/activate-studio/releases)
page: open it and drag the app to Applications.

*First open:* the app starts the Studio's server in the background and
shows the Studio in its own window, with its own Dock icon and menus; no
browser is involved. Links to other sites open in the default browser,
downloads go to Downloads, and quitting leaves the server running so the
next open is immediate (the app menu's Stop Background Server stops it).
macOS reports a new background item, and may ask once whether the Studio
can use the Documents folder. An app built on Linux has no native window
(it is compiled on a Mac): it opens the Studio in an app window in Google
Chrome or Microsoft Edge, or a tab in the default browser, where Safari's
File, Add to Dock makes it an app.

*What runs:* the server listens on 127.0.0.1, port 4180, so nothing
outside the Mac can reach it. A LaunchAgent keeps it running and starts it
at login; it restarts on its own when a setting needs a restart, and after
a crash. The app carries Node, GUFI, Python for the indexer, and the
embedding model, so nothing else needs installing. No Parallel Works
account is needed; where the `pw` CLI is installed and signed in, the
platform features work too.

*Where things live:*

| | |
|---|---|
| Documents | `~/Documents/ACTIVATE Studio`, until another folder is chosen |
| Index and logs | `~/Library/Application Support/ACTIVATE Studio/index` and `logs` |
| Settings | `~/Library/Application Support/ACTIVATE Studio/studio.env`: `KEY='value'` lines read before each start, such as `PORT='4180'` or a model endpoint (`OPENAI_BASE_URL`, `OPENAI_API_KEY`) |

*Stop or uninstall:*

```
"$HOME/Applications/ACTIVATE Studio.app/Contents/MacOS/ACTIVATE Studio" --stop
"$HOME/Applications/ACTIVATE Studio.app/Contents/MacOS/ACTIVATE Studio" --uninstall
```

`--stop` stops the server until the app is next opened. `--uninstall`
removes the background server and the app, and keeps the index, logs and
settings unless `--purge` is added. Neither touches the documents.

*Updates:* the app checks this repository's releases a minute after it
starts and every six hours after that; Settings, General shows the running
version, the newest release with its notes, and Update now. It takes the
newest release that carries the signed app; while a newer one is still
waiting for its macOS build, it checks again after 30 minutes. The download
goes to `~/Library/Application Support/ACTIVATE Studio/updates` and is
installed only if the release's manifest is signed by the release key
(the app carries its public half, `Contents/Resources/update-signing.pub`),
names this app's archive and a newer version than the one running, and
matches the download's size and SHA-256. The new copy then takes the app's place, the old one is
kept beside it as `.ACTIVATE Studio.app.previous` (hidden in Finder), and
the server restarts on the new version while the window reloads. If the
update changed the window itself, Settings asks for the app to be
reopened. Settings can also turn the checks off, follow prereleases, or
install updates automatically, which happens once an update is verified
and the Studio has had no chat answer or indexing running for ten
minutes. An app in a folder its user cannot write to, such as
`/Applications` for a standard account, reports updates without
installing them. A clone of the repository is told the newest version and
the command that updates it; a Studio that signs users in, or that the
platform deploys, does not check.

*Rolling back:* while the previous copy is there, Settings, General offers
Roll back, which puts it back and restarts. The same happens without
asking when a new version stops three times within two minutes of being
installed; Settings then says so, and that version is not installed
automatically again.

*Publishing an update:* a release carries the
`activate-studio-macos-arm64.tar.gz` that `build_app.sh` writes, and the
`.manifest.json` and `.manifest.json.sig` that
`deploy/mac/sign_release.sh activate-studio-macos-arm64.tar.gz` writes
beside it. The script reads the private key from
`STUDIO_UPDATE_SIGNING_KEY` (the PEM text, as the repository secret of
that name holds it) or from the file `STUDIO_UPDATE_SIGNING_KEY_FILE`,
refuses a key that does not match `update-signing.pub`, takes the version
from the app in the archive, and needs OpenSSL 3 (`brew install openssl@3`
on a Mac).

*Opening an unsigned app:* the app is not yet signed by Apple. The curl
installer does not meet this, since a download made with curl carries no
quarantine flag. An app downloaded with a browser, including from the disk
image, is blocked the first time: on macOS 15 and later, open System
Settings, Privacy & Security, and click Open Anyway; on earlier versions,
Control-click the app and choose Open. Clearing the flag in Terminal also
works:

```
xattr -dr com.apple.quarantine "/Applications/ACTIVATE Studio.app"
```

*Requirements:* a Mac with Apple silicon, and the macOS release the app
declares as its minimum, which its bundled GUFI sets: macOS 14 with GUFI
built by `deploy/mac/build_gufi.sh`, macOS 27 with GUFI's own package (see
[GUFI in the app](#gufi-in-the-app)).

*Building the app:* `deploy/mac/build_app.sh`, on macOS or Linux, writes
the app, `activate-studio-macos-arm64.tar.gz` with its `.sha256`,
`install.sh`, and on a Mac the disk image to `deploy/mac/out`. It checks
the app with `deploy/mac/check_app.sh`, which on a Mac also runs it with
`--run`. `install.sh` installs from a local archive too:
`bash install.sh path/to/activate-studio-macos-arm64.tar.gz`. The app's
icon is `deploy/mac/AppIcon-1024.png`, drawn from `deploy/mac/AppIcon.svg`
on Apple's icon grid; the disk image's volume shows it too. The web icons
come from `web/icons/`.

## From a clone

This is the whole path from nothing installed to a working index, in the
order it is worth doing: run it first, add search once you know it works,
add a model last. Everything here is tested on every pull request by a
`macos-15` job in CI, which builds the server and web app and runs the
full suite.

### 1. Prerequisites

```
brew install node pnpm
```

Node 26 or newer. Nothing else is needed to start.

### 2. Run it

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

### 3. Add GUFI, for indexed search

The index comes from [GUFI](https://github.com/mar-file-system/GUFI),
LANL's Grand Unified File Index. Install it from GUFI itself if your
site or a package provides it; the Studio uses `GUFI_BIN` when set, and
otherwise finds `gufi_query` on `PATH` or under `/opt/gufi/bin`,
`/usr/local/bin` or `~/usr/local/bin`, which is where GUFI's macOS
package installs for all users or for one. That package's tools need
`<prefix>/lib` on `DYLD_LIBRARY_PATH`, which the Studio sets for the tools
it runs, and they load Homebrew's `libomp` and `pcre2`
(`brew install llvm pcre2`). To build GUFI from source here instead:

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

### 4. Point it at a model

The assistant talks to any OpenAI-compatible endpoint:

```
OPENAI_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama pnpm start
```

Ollama, llama.cpp's server, vLLM, and hosted APIs all work. The endpoint
must serve `/models` and streaming `/chat/completions`, and it must
support tool calling for the assistant to search the corpus and read
files. On the ACTIVATE platform the gateway is used automatically and
none of this is needed.

### Troubleshooting

*`gufi_query: command not found` after installing:* the Studio looks on
`PATH` and in `/opt/gufi/bin`, `/usr/local/bin` and `~/usr/local/bin`.
Set `GUFI_PREFIX` when building to choose somewhere else, and add its
`bin` to `PATH` or set `GUFI_BIN` to it.

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

## GUFI in the app

The app carries GUFI in `Contents/Resources/gufi` (`bin`, `lib`,
`licenses`), relinked so that the tools load their libraries from that
`lib` without `DYLD_LIBRARY_PATH`. `deploy/mac/build_app.sh` takes it
from one of two sources:

| Source | Set with | Oldest macOS |
|---|---|---|
| An archive from `deploy/mac/build_gufi.sh`; the app's releases use the published one | `GUFI_TARBALL=path/to/gufi-macos-arm64.tar.gz` | its deployment target, 14 by default |
| GUFI's own macOS package, with Homebrew's `libomp` and `pcre2` bottles (when no archive is given) | `GUFI_PKG`, or the pinned download | 27 |

The app's `LSMinimumSystemVersion` is the newest macOS any bundled binary
declares, and GUFI is the one that sets it (the bundled Node needs 13.5).
GUFI's package is built without a deployment target, so its binaries need
the macOS release that built them. [`GUFI-MACOS.md`](GUFI-MACOS.md) covers
the published archive, how to build and check it, and how a new one is
published.
