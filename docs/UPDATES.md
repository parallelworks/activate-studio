# Updates: channels, versions and state

This is the contract between a release and the updater that installs it, for the macOS app and the per-user Linux app. The Node server implements it in `server/src/updates.ts` and `server/src/stateFormat.ts`; the Go server implements the same rules. [`MACOS.md`](MACOS.md) and [`LINUX-APP.md`](LINUX-APP.md) describe the platform parts: the bundle and payload layout, the swap, the run loop and the rollback after a failed start.

## Channels

There are two channels, Stable and Preview. Settings, General, Updates stores the choice as `updates.channel`, `stable` or `preview`, in `settings.json`. The value `prerelease`, which versions before channels wrote, is read as `preview`. Stable takes stable releases only; Preview takes both, so a Preview install moves on to a Stable release that is newer than its version.

A release's channel comes from two places:

| GitHub prerelease flag | `channel` in the signed manifest | Channel |
|---|---|---|
| no | absent or `stable` | Stable |
| yes | absent, `stable` or `preview` | Preview |
| no | `preview` | refused at install |

The list of releases (the GitHub API) shows only the flag, so the updater lists releases by the flag and checks the signed field when it downloads the manifest. A manifest that says `preview` on a release published as stable is refused, and a Stable install refuses any Preview manifest. Any other `channel` value is refused.

Versions before channels go by the flag alone and skip prereleases unless set to follow them, which is why a Preview release must always be a GitHub prerelease. Such a version set to follow prereleases takes Preview releases, as it did before.

## The manifest

The signed manifest (`activate-studio-<platform>.manifest.json`, with its Ed25519 signature in `.manifest.json.sig`, signed by the release key whose public half every install carries as `update-signing.pub`) holds:

| Field | Meaning |
|---|---|
| `version`, `asset`, `sha256`, `size` | the release, the archive's name, its SHA-256 and size in bytes |
| `minMacos` or `minGlibc` | the oldest system the build runs on |
| `builtAt` | when it was built |
| `channel` | optional; `stable` or `preview` |
| `stateFormat` | optional; the newest state format the version reads, a whole number; absent is 1 |
| `requiresRelaunch` | optional; `true` when the version replaces the whole app, window included |

Unknown fields are ignored, so releases with the new fields install on versions from before them. `deploy/mac/sign_release.sh` and `deploy/linux/sign_release.sh` write the optional fields from `STUDIO_RELEASE_CHANNEL`, `STUDIO_STATE_FORMAT` and `STUDIO_REQUIRES_RELAUNCH=1`; without them the manifest is as before.

## Which versions install

A release is installable when it is on the channel and carries the platform's archive, manifest and signature. Every download is checked the same way, whatever the direction: the manifest's signature with the install's own key, its `asset`, its version against the release's tag, the channel, the system minimum, and then the archive against `size` and `sha256` as it arrives, and the unpacked copy's own version.

**Update now** and the automatic install go to the newest installable release, and only when it is newer than the running version. An older version is installed only when a person picks it:

- **Install a version** in Settings lists the installable releases on the channel, newest first, and installs the one chosen, newer or older (`POST /api/updates/install` with `{ "version": "v1.145" }`). Before it asks, the page reads what the change would do (`POST /api/updates/inspect`, which verifies the manifest's signature and answers whether the version is older, the state formats, and the snapshot it would need).
- **Move to Stable** appears when the channel is Stable and the running version is newer than every Stable release (it is a Preview). It installs the newest Stable through the same path.

The Go version lists, inspects and installs only Go releases, those after v1.188 (`updates.LastNodeRelease`; the first Go Preview is v1.188.1): a Node release is not in **Install a version**, `install` and `inspect` refuse one as not installable, and **Move to Stable** from a Go Preview goes to v1.189 or later, never to v1.188. Releases carry no mark of their server in the signed manifest, so the version is the mark.

The automatic install never goes to an older version and never installs a release with `requiresRelaunch`.

## The copy kept beside the app, and Roll back

An update moves the running copy to `.activate-studio.previous` (Linux) or `.ACTIVATE Studio.app.previous` (macOS) and keeps it only until the new version's first clean start: `updates/pending.json` cleared after a minute up, and no process left running from the copy (the window of the old copy runs from it until it is reopened; the Go server looks in `/proc` on Linux and asks `lsof` on macOS, and keeps the copy when it cannot tell). Then it removes the copy, with the files in the data folder that only the Node version made and that its run loop makes again (below), and writes the version the copy held to `updates/previous.json`. Until the clean start the run loop's rollback after three failed starts puts the copy back, offline too.

**Roll back** in Settings, Updates goes to that version by downloading it again, when it is a Go release: the same path as **Install a version** (the manifest's signature, the size and SHA-256 of the download, the swap with the running copy kept beside it and `pending.json` written, a state snapshot first, and the snapshot the version needs put back when it cannot read the state as it is), reported as a rollback in `updates/rolled-back.json`. The status gives `previous: { version, size }`, the size of that download, which the confirmation shows. Offline, the rollback fails before anything changes and says that the version is still installed. The version must be a signed release on the channel. When the copy held a Node release (the move from v1.188 or earlier), `previous` is null, **Roll back** is not shown, and `POST /api/updates/rollback` answers 409 naming the version as one that runs the Node server. The move from Node to Go is not undone by choice; until the first clean start the run loop's rollback still puts the Node copy back when the Go version keeps failing to start.

What only the Node or Python versions used in the data folder (`INDEX_BASE`, `<data>` on Linux `~/.local/share/activate-studio-data`, on macOS `~/Library/Application Support/ACTIVATE Studio`) and in `~/.cache/activate-studio`, checked against `server/src`, `indexer/` and the run loops of v1.188 and against `server-go`:

| Path | Node or Python | Go | With the copy |
| --- | --- | --- | --- |
| `models/minilm384.gguf` | a link into the app's GGUF model, made by the run loop at each start, for sqlite-lembed | reads it only with the full GUFI, which the Go build does not carry | removed when the link leads nowhere |
| `models/minilm-l6` | a link into the app's ONNX model | the same link | kept |
| `extract`, `embed-cache.db`, `pdf-preview`, `pdf-pages`, `model-cache`, `semantic-map`, `.soffice`, `embed-wheels`, `workflow.schema.json` | caches | the same names and formats | kept |
| `~/.cache/activate-studio`, `<data>/webview` | the Linux window's web view (cache, storage) | the same window | kept |
| Python bytecode | none: the run loops set `PYTHONDONTWRITEBYTECODE=1`, and the indexer runs from the app | none | nothing to remove |

Never touched: the libraries and their indexes, conversations, keys and the credential vault, settings, `studio.env`, logs and the snapshots, which keep their own rule. Nothing is removed before the first clean start, so the run loop's rollback finds the Node copy and its files as they were. On macOS the LaunchAgent (`~/Library/LaunchAgents/com.parallelworks.activate-studio.plist`) names the app's `Resources/studio-run.sh` by its fixed path, which the Go app keeps, and the Go app's `launcher.sh` is the same script and rewrites the agent when its contents differ; the Swift window's own files outside the bundle (its preferences and WebKit data under the bundle identifier) are left in place, since the Wails window shares them.

## State

The state is what a newer version might migrate: everything at the top of `INDEX_BASE` except caches and locks (`settings.json`, `conversations.json`, the credential vault `user-credentials.json` and `.credentials-secret`, `libraries.json`, `access.json` and the like), the directories `users` (without `users/*/maps`), `tasks`, `fleet`, `extensions`, `branding` and `ssh`, the JSON files directly inside each `libraries/<id>`, and the launcher's `studio.env`. Indexes (`gufi`, a library's index), caches (`extract`, `embed-cache.db`, `semantic-map`, `model-cache`, `pdf-*`), models, logs and agent workspaces are not state: they carry their own format or are rebuilt.

`INDEX_BASE/state-format.json` holds `{ "format": N, "writtenBy": "<version>", "at": "<time>" }`. State with no marker is format 1. The Node server reads and writes format 1.

Before every change of version (an update, an older version, a rollback) the updater copies the state to `<data>/updates/state-snapshots/<id>/` (`files/`, and `snapshot.json` with `from`, `to`, `reason`, `format`, `at` and the list of files), mode 0700. If the copy fails, nothing is changed. It keeps the three newest snapshots and the newest of each format, so the snapshot taken before a first move to a newer format survives later updates.

The rules every version follows:

1. At start, before anything reads the state: no marker, write one with the version's format. A marker at or below the version's format: run, migrating if needed. A marker above it: put back the newest snapshot whose format the version reads; with none, say so in the log and leave the state alone.
2. A version that migrates the state to a higher format takes a snapshot first (in the old format), then migrates, then writes the marker. It never changes the format without that snapshot.
3. Installing a version whose `stateFormat` is below the marker needs a snapshot in a format it reads. With one, the person is told which (its date and the versions it was taken between) and the install puts it back after the swap; with none, the install is refused.
4. Putting a snapshot back first saves what is there as a snapshot of its own, removes state files the snapshot does not hold, copies the snapshot's files in, keeps the current `updates` policy in `settings.json` (it chose the move), and writes the marker with the snapshot's format.

Changes made after a snapshot (conversations from a Preview, say) are not in the version it is put back for. They stay in the snapshot taken when it was replaced.

## Relaunch

A release with `requiresRelaunch: true` replaces the whole app. The updater verifies and swaps it in as usual (the previous copy kept until the first clean start, `updates/pending.json` written), then writes `updates/reopen` and `updates/relaunch.json` (`from`, `to`, `at`) and does not restart the server into the new copy: its status is `relaunch` and the server keeps running from the moved copy until the app reopens. The page asks the window to reopen at once when the person started the install, and offers **Reopen now** otherwise.

- macOS: the window reopens the app bundle. The launcher of the new copy finds `relaunch.json`, removes it, and starts the background server again from its own copy.
- Linux: the window shell treats `relaunch.json` as a changed window program: it stops the server, starts the install's `bin/activate-studio`, and quits.
- The old run loop, if its server stops in the meantime and the copy at its path has no `node/bin/node`, stops instead of restarting.
- The new version removes `relaunch.json` when it starts.

## Publishing a Preview release

1. Build each platform as usual, and sign each manifest with `STUDIO_RELEASE_CHANNEL=preview` (and `STUDIO_STATE_FORMAT` and `STUDIO_REQUIRES_RELAUNCH=1` where they apply).
2. Publish the GitHub release as a prerelease, not marked latest.
3. Run `deploy/mark_preview.sh vX.Y`, which checks that every attached manifest says `preview` and sets the prerelease flag if it is missing; `--check` only checks.

`deploy/release_preview.sh [COMMIT] [VERSION]` does this for a go-port commit (default `origin/go-port`): it checks that the commit builds the Go version, picks the next `vMAJOR.MINOR.N` after the newest Stable release (`v1.188.1` after `v1.188`; the Node updaters of v1.180 to v1.188 and the Go one compare MAJOR, MINOR and PATCH as numbers, so it orders after `v1.188` and before `v1.189`), refuses without the signing and notary secrets, pushes an annotated tag, creates the release with `--prerelease --latest=false` and checks that "latest" did not move, and dispatches `linux-app` and `mac-app` with `release=VERSION` and `channel=preview`. Those runs sign the manifests with `STUDIO_RELEASE_CHANNEL=preview`, sign and notarize the macOS app, and leave the fixed `linux` and `macos` releases (the addresses new installs come from) at the newest Stable build. `DRY_RUN=1` prints the steps after the checks; `WAIT=1` waits for both runs and then runs `deploy/mark_preview.sh VERSION --check`. A Preview install moves on to the next Stable release when it is published, since that version is newer. Publishing a release also starts `container.yaml`, which pushes an image tagged with the version and leaves `:latest` alone for a prerelease.

Promoting a Preview to Stable is a new release with a new version, signed without the channel or with `stable`. A published manifest cannot be edited, since the signature covers its bytes.

## The Go version

The Go version runs the Go server (`app/bin/studio-server`) with no Node and no Python, here or on the other systems it builds libraries on: there it sends its helper (`app/bin/remote`) and, for search by meaning, ONNX Runtime from the onnxruntime wheel pinned in `indexer/embed_deps.json`, which it downloads and checks in Go. It ships to Preview first, as an ordinary update, and every install can move between it and the Node versions.

No bridge release is needed. The updaters of the releases already installed accept a Go build as it is. The macOS app and its updater exist from v1.141 (no release before it carries a macOS app); its bundle check is the same from v1.141 to v1.188: `Info.plist` with the bundle identifier and version, the executable, `Resources/studio-run.sh`, `Resources/node/bin/node` and `Resources/app/server/dist/main.js`. The Linux updater of v1.187 and v1.188 (the only Linux releases) checks for an executable `node/bin/node`, `app/server/dist/main.js`, `update-signing.pub` and `manifest.json`. Neither checks a signature on the bundle beyond the manifest's. The updaters of v1.141 to v1.144 take the newest release whether or not it carries the app yet, so they install v1.189 once its macOS build is attached. The Go build carries stand-ins at those paths, marked by `node/PLACEHOLDER`.

After the swap the old server restarts its run loop, and the new copy's `studio-run.sh`, which reads `"server": "go"` in `manifest.json` and starts the Go server, takes over in one of three ways:

- With a LaunchAgent (every macOS release before v1.170, and later ones where the agent loads), `launchctl kickstart -k` restarts the agent, whose `studio-run.sh` path is now the new copy's.
- Without one (`STUDIO_SUPERVISOR=detached`, set by `launcher.sh` from v1.170) and on Linux, the server exits 76 and the old run loop executes the new copy's `studio-run.sh`.
- When kickstart fails, the server exits 75 and the old run loop starts what it takes for Node: the new copy's `node/bin/node` with `app/server/dist/main.js`. On macOS that stand-in runs `studio-run.sh --serve`, which starts the Go server once under the old loop; the old loop keeps restarting it and applies the three-stop rule. Before v1.170 this is the only way a failed kickstart becomes an update; from v1.170 a run loop without an agent runs detached and uses exit 76. The Linux stand-in exits 1, since the Linux updater always uses exit 76.

`requiresRelaunch` is not needed for this move. A person on a release before v1.141 has no macOS app to update; they install the current app from the macos release (the disk image or `install.sh`), which keeps the data folder.

The updater of the old release writes `updates/reopen` because the window program changed (Swift to Wails). The Go server reports `reopen: true`, so Settings, Updates shows "The update also changed the app's window" with **Reopen now**, and the footer shows a "Reopen to finish updating" line with the same button. A Swift window from v1.156 quits and opens the new app on that click; an older one has no such request, and the button gives way to "Quit and reopen ACTIVATE Studio to finish." A person who never reopens keeps a working Studio: the Swift window loads the page from the Go server, its menu actions run the new copy's `launcher.sh`, the copy of the old app is kept until that window quits, and the next start opens the Wails window.

What the bridge jobs prove, each from the release's own assets with a throwaway key on 127.0.0.1 (`deploy/mac/test_go_bridge_e2e.sh` and `deploy/linux/test_go_bridge_e2e.sh`): the old release is installed and started, given settings, a label, a second library and a conversation, and indexed; a Go build whose server exits at once is installed by the old updater and put back by the run loop after three failed starts, with the data intact; the Go build is then installed by the old updater, runs the Go server with the data intact, offers no Roll back to the Node release and refuses one, and removes the old copy after the first clean start. `mac-app.yml` runs v1.169 with its own `launcher.sh` and LaunchAgent and v1.188 detached on every check run, and on the `bridge` input `all` also v1.141 with its LaunchAgent, v1.169 with no agent (exit 75 through the stand-in), and v1.170, v1.180 and v1.188 both with their LaunchAgent and detached. A GitHub macOS runner bootstraps, kickstarts and boots out an agent in `gui/501` (its console user), so the LaunchAgent cases run as on a Mac. `linux-app.yml` runs v1.187 and v1.188 on Stable and Preview.

- **Assets and signing.** The same asset names, manifest fields and release key; `update-signing.pub` stays where the Node versions look for it (`Contents/Resources/` in the bundle, the payload's top directory on Linux), so a Go install can go back to a Node release.
- **Paths.** The same bundle identifier, app path and data folder on macOS; the same payload path and `bin/activate-studio` launcher on Linux; `app/build-info.json` with the version; the same `INDEX_BASE`, `updates` folder, `studio.env`, and the `pending.json`, `rolled-back.json`, `swap.json` and `reopen` files.
- **Updater.** The rules above, identically: channels and the `prerelease` alias, the checks on every download, newer-only automatic installs, the version picker with `inspect`, Move to Stable, snapshots before every change (without agent workspaces, `tasks/work`, or the index's history), the marker rules, and `requiresRelaunch`. While the Go server runs Node behind it (`STUDIO_SERVER=go` builds), it asks Node whether an index is running before it installs.
- **The window.** On Linux the window shell decides the server from the payload at each start (`STUDIO_GO_SERVER`: `1` on, `0` off, unset: on when `manifest.json` says `"server": "go"`; a payload with `node/PLACEHOLDER` always runs Go), so a rollback under a running window starts Node again. An update that changes the window program sets `reopen`; the page reopens the window when the person started the install there, and offers **Reopen now** otherwise. On macOS the Go build's window is the Wails one (`desktop/wails`, see [MACOS.md](MACOS.md)); like the Swift window it runs `launcher.sh`, which restarts a server whose `builtAt` differs from its own copy, so the first start of the new window takes over cleanly from the old LaunchAgent server.
- **Rolling back.** The Go version never goes back to a Node release by choice: **Install a version**, **Move to Stable** and **Roll back** offer only releases after v1.188 (above). Until the first clean start the run loop puts the Node copy back when the Go version keeps failing to start, and the state snapshot taken before the move is there for it.
- **State.** It raises `stateFormat` only with a migration and a snapshot first.

The Go version is the default build: `deploy/linux/build_app.sh` and `deploy/mac/build_app.sh` build it unless `STUDIO_SERVER` says otherwise (`node` for the payload of v1.188 and earlier, `go` for the Go server with Node behind it), and it is what the release jobs of `linux-app.yml` and `mac-app.yml` build, check, sign and attach. On macOS the release signs and notarizes it with the Developer ID certificate; `deploy/mac/sign_app.sh` signs every Mach-O file in it, the Go server with `allow-unsigned-executable-memory` (`deploy/mac/entitlements/server.plist`, for the STEP preview's WebAssembly) and the Wails window with `app.plist`. The earlier builds stay buildable through the workflows' `legacy` input and do not fail a run.

What the Go build carries, by platform (`STUDIO_SERVER=go-only`, the default, in `deploy/linux/build_app.sh` and `deploy/mac/build_app.sh`; `deploy/app-go.def` for the container):

| Part | Where | Notes |
| --- | --- | --- |
| `studio-server`, `semantic-map` | `app/bin/` | built without cgo; `studio-server` also runs `extract`, `embed`, `embed-query` and `routes` |
| `studio-remote` for Linux x86_64 and aarch64 and macOS arm64 | `app/bin/remote/<platform>/` | built without cgo (`server-go/scripts/build_remote.sh`), about 25 MB each (33 MB for the three, compressed); sent to the systems libraries are built on; `app/indexer/embed_deps.json` pins what goes with it |
| ONNX Runtime 1.31.0 | `app/lib/` | Microsoft's release archive at a pinned SHA-256; found beside `app/bin` and named in `STUDIO_ORT_LIB` |
| MiniLM model and tokenizer | `models/minilm-l6/` | `indexer/fetch_embed.py --model`, pinned; no GGUF model |
| STEP preview modules | `app/occt/` | `server-go/scripts/occt/build.sh`; separate LGPL files, named in `STUDIO_OCCT_WASM` |
| GUFI | `gufi/` | the lean build (sqlite-vec, no llama.cpp): `build_gufi.sh` with `GUFI_LEMBED=0` |
| tesseract | `ocr/` | as before |
| poppler | not bundled | the Go PDF renderer reads PDFs without it; the container image installs it |
