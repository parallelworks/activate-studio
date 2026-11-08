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

The automatic install never goes to an older version and never installs a release with `requiresRelaunch`. **Roll back** stays the one-step return to the copy kept beside the app.

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

A release with `requiresRelaunch: true` replaces the whole app. The updater verifies and swaps it in as usual (the previous copy kept for Roll back, `updates/pending.json` written), then writes `updates/reopen` and `updates/relaunch.json` (`from`, `to`, `at`) and does not restart the server into the new copy: its status is `relaunch` and the server keeps running from the moved copy until the app reopens. The page asks the window to reopen at once when the person started the install, and offers **Reopen now** otherwise.

- macOS: the window reopens the app bundle. The launcher of the new copy finds `relaunch.json`, removes it, and starts the background server again from its own copy.
- Linux: the window shell treats `relaunch.json` as a changed window program: it stops the server, starts the install's `bin/activate-studio`, and quits.
- The old run loop, if its server stops in the meantime and the copy at its path has no `node/bin/node`, stops instead of restarting.
- The new version removes `relaunch.json` when it starts.

## Publishing a Preview release

1. Build each platform as usual, and sign each manifest with `STUDIO_RELEASE_CHANNEL=preview` (and `STUDIO_STATE_FORMAT` and `STUDIO_REQUIRES_RELAUNCH=1` where they apply).
2. Publish the GitHub release as a prerelease, not marked latest.
3. Run `deploy/mark_preview.sh vX.Y`, which checks that every attached manifest says `preview` and sets the prerelease flag if it is missing; `--check` only checks.

Promoting a Preview to Stable is a new release with a new version, signed without the channel or with `stable`. A published manifest cannot be edited, since the signature covers its bytes.

## The Go version

The Go server is a rewrite with no Node. It ships to Preview first and must keep every install able to move between it and Node versions:

- **Bridge release.** The first Go release must be preceded by a Stable Node release that carries this contract (channels, the state marker and snapshots, `requiresRelaunch`). A Node version from before it would restart its server into the Go copy, and its run loop would roll the update back. Sign the first Go release with `requiresRelaunch: true`, `channel: preview` and its `stateFormat`.
- **Assets and signing.** The same asset names, manifest fields and release key; the install's `update-signing.pub` stays where the Node versions look for it (`Contents/Resources/` in the bundle, the payload's top directory on Linux), so a Go install can go back to a Node release.
- **Paths.** The same bundle identifier, app path and data folder on macOS; the same payload path and `bin/activate-studio` launcher on Linux; `app/build-info.json` with the version (under `Contents/Resources/` in the bundle) and `update-signing.pub`, which the updater checks in a copy with no Node, and on Linux the payload's `manifest.json`; the same `INDEX_BASE`, `updates` folder, `studio.env`, and the `pending.json`, `rolled-back.json`, `swap.json` and `reopen` files.
- **Updater.** The rules above, identically: channels and the `prerelease` alias, the checks on every download, newer-only automatic installs, the version picker with `inspect`, Move to Stable, snapshots before every change, the marker rules, and `requiresRelaunch`, which it also writes when a later release changes the app's shape again.
- **First start.** The Go launcher stops the server a Node copy left running (the LaunchAgent `com.parallelworks.activate-studio` on macOS, the window shell's server on Linux) and installs its own, and it removes `relaunch.json`. It watches `pending.json` and puts the previous copy back when it keeps failing to start, as the Node run loop does.
- **State.** It raises `stateFormat` only with a migration and a snapshot first. Going back to a Node release then puts that snapshot back.
