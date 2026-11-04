# The Linux app payload (draft)

This page describes the Linux x86_64 payload of ACTIVATE Studio, which the `linux-app` workflow builds. It is the contents of the macOS app without a window or a package: a directory tree that runs from any path. There is no installer and no window yet, and nothing is published unless a run of the workflow is started by hand with a release; the archive is otherwise a workflow artifact. The update channel is described under Updates below.

## What it holds

`activate-studio-linux-x86_64.tar.gz` unpacks to `activate-studio/`:

| Path | Contents |
|---|---|
| `node/` | Node 26 for Linux x64, with `lib/libatomic.so.1` beside it (Node's run path finds it) |
| `python/` | python-build-standalone 3.12 with pypdf, xlrd, olefile, onnxruntime, tokenizers and numpy |
| `app/` | the built server with its production `node_modules` (including the document converter's Linux binary), the built web app, the indexer, `docs/`, `CHANGELOG.md`, `build-info.json` |
| `gufi/` | GUFI 0.8.0 with sqlite-vec and sqlite-lembed, PCRE2 and `libgomp.so.1`, built by `deploy/linux/build_gufi.sh` |
| `ocr/` | tesseract and leptonica with the English model, built by `deploy/linux/build_ocr.sh` |
| `models/` | the GGUF and ONNX embedding models |
| `studio-run.sh` | the run loop: starts the server with the payload's own programs, restarts it on exit 75 |
| `studio-recover.sh` | finishes or undoes an update cut off between its two moves (see Updates) |
| `manifest.json` | what it was built from and the system libraries it needs |

Run it with `activate-studio/studio-run.sh DATA_DIR`. `DATA_DIR` holds `studio.env` (the settings, such as `PORT=4180` and `KB_ROOT`), `index/` and `logs/`; the server listens on 127.0.0.1 only.

## How it is built

`.github/workflows/linux-app.yml` builds GUFI and tesseract in a Rocky Linux 9 container (glibc 2.34), puts the payload together on an Ubuntu runner (`deploy/linux/build_app.sh`), and runs it in clean Ubuntu 22.04, Ubuntu 24.04 and Rocky Linux 9 containers as a plain user (`deploy/linux/check_app.sh`). `deploy/linux/elf_check.py` reads every ELF file in the tree and fails the build if one needs a glibc, libstdc++ or libgcc symbol newer than RHEL 9 has, or a library that is neither in the tree nor one every desktop has. The versions of the sources the macOS and Linux builds share are in `deploy/common/pins.sh`.

## What was measured

The archive is about 255 MiB (the tree 687 MB: Node 144 MB, Python 196 MB, GUFI 182 MB, tesseract 12 MB, the server's `node_modules` 69 MB, the web app 36 MB, the models 47 MB), against 503 MB for the macOS app, whose GUFI is 55 MB and Python 142 MB. In CI, GUFI builds in about 8 to 9 minutes, tesseract in 2 to 3, and the payload is assembled in under a minute after the web and server build. The newest symbol versions anywhere in the tree are glibc 2.34, libstdc++ 3.4.29 (GCC 11) and CXXABI 1.3.13, so it needs RHEL 9's or Ubuntu 22.04's C libraries or newer, and does not run on RHEL 8 or Ubuntu 20.04.

The system libraries the tree needs are the C library (libc, libm, libdl, libpthread, librt, libutil, ld-linux), libstdc++, libgcc_s and zlib: packages `libc6`, `libstdc++6`, `libgcc-s1` and `zlib1g` on Ubuntu, `glibc`, `libstdc++`, `libgcc` and `zlib` on RHEL 9. Every desktop has them, and the clean images the tests start from needed no other package. Node's libatomic and GCC's libgomp, which a RHEL 9 install may lack, are in the tree.

## Updates

Two install layouts come from the one payload, and they update differently. The server tells them apart by the file `install-kind` in the payload's top directory (beside `studio-run.sh`), which the installer or the package writes.

| `install-kind` | Written by | Where the payload is | Updates |
|---|---|---|---|
| `user` | the per-user installer | a directory the user can write, such as `~/.local/lib/activate-studio` | the Studio replaces itself from the project's releases |
| `deb` | the .deb package | `/opt/activate-studio`, root-owned | never by itself: the Studio shows that a version is available and says to update with `apt` |
| `rpm` | the .rpm package | `/opt/activate-studio`, root-owned | never by itself: the same, with `dnf` |

The file holds one line: the kind, then optionally the package name when it is not `activate-studio` (`deb activate-studio`). The archive that updates download does not contain the file; an update writes `user` into the new payload before it is put in place, so a per-user install stays one. A package must write its kind and must not leave a payload that can replace itself: a `deb` or `rpm` copy never installs an update, whoever runs it and whatever it can write, because the package database would no longer match the files.

With no `install-kind` file (a tarball unpacked by hand), the path decides. A payload whose directory the user can replace (the directory holding it is writable) is taken as a per-user install and updates itself. One the user cannot write to is taken as managed by someone else: if dpkg lists `studio-run.sh` under `activate-studio`, or `rpm -qf` names a package for it, the Studio treats it as that package; otherwise it says it cannot update itself here and suggests the releases page or a per-user install. An unreadable or unknown `install-kind` counts as no file.

### What the installer and the packages must do

- The payload's top directory has a stable path that holds no version, and it is a real directory, not a link: `studio-run.sh` finds its own place by the physical path, and an update replaces the directory at that path. A per-user install is `~/.local/lib/activate-studio` (the archive unpacks to `activate-studio/`, so `tar -xzf ... -C ~/.local/lib`). Launchers and `.desktop` files name `studio-run.sh` or a program in that directory by that path.
- The directory holding the payload must be writable by the user, because an update unpacks `.activate-studio.new` beside it and keeps the old payload as `.activate-studio.previous` for a rollback. Nothing else in the tree is written by the Studio, so a package can install it read-only.
- A window shell inside the payload is named in the payload's `manifest.json` as `"shell": "path/inside/the/payload"`. When an update changes that file, the Studio tells the person to reopen the window; the run loop alone is restarted by the update. A payload with no `shell` entry has no window to reopen.
- A package that installs into `/opt/activate-studio` writes `install-kind` as `deb` or `rpm` and is named `activate-studio` (or writes the name after the kind). The commands the Studio shows are `sudo apt update` then `sudo apt install --only-upgrade activate-studio` for a `deb`, and `sudo dnf upgrade --refresh activate-studio` for an `rpm`.
- `STUDIO_APP_BUNDLE` stays unset for Linux. The server finds the payload from its own place (`<payload>/app/server/dist`), or from `STUDIO_PAYLOAD_ROOT` where that is set.

### The linux channel

The channel is built like the macOS one, so one release flow serves both. A release that gets a Linux build has three more assets: `activate-studio-linux-x86_64.tar.gz`, `activate-studio-linux-x86_64.manifest.json` and `activate-studio-linux-x86_64.manifest.json.sig`. The same files are in a fixed release tagged `linux`, a prerelease that is never latest and that the update check skips because its tag is not a version; it is the address an installer and the docs use. The `linux-app` workflow does this only when started by hand with the `release` input (a published tag such as `v1.150`), after it has built that tag and run it on all five distributions; a run on a push or without the input publishes nothing. It signs the manifest with `deploy/linux/sign_release.sh`, with the key the macOS app uses, whose public half is `update-signing.pub` in the payload.

Releases are frequent and most do not get a Linux build. An install updates to the newest release on its channel (stable, or pre-release if chosen in Settings) that carries the three assets, and is told of no other. A package install is told of a version on the same rule, which is a statement that a Linux build exists, not that the package repository has it yet.

### How a per-user install updates

The check runs on the same schedule as the macOS app's. For a release with the assets the server:

1. downloads the manifest and its signature and verifies the Ed25519 signature with `update-signing.pub` from its own payload; the manifest names the version, the asset, its SHA-256 and size, and `minGlibc`;
2. checks that the manifest's version is the release's and newer than the running one, and that the C library on this computer is at least `minGlibc`;
3. downloads the archive into the data directory's `updates` folder, stopping at the size the manifest gives and comparing the SHA-256 as it arrives;
4. unpacks it into `.activate-studio.new` beside the payload, after checking that every entry is inside `activate-studio/`; checks that `studio-run.sh`, Node, the server, the update key and `manifest.json` are there and that the version inside is the manifest's;
5. moves the running payload to `.activate-studio.previous` and the new one into its place (a failure of the second move puts the first back), writes `updates/pending.json`, and exits with status 76, on which `studio-run.sh` runs the new payload's own `studio-run.sh` in place of itself.

A download that fails any check leaves the payload as it was and removes what it fetched; automatic installation does not try that version again until the Studio restarts, and **Update now** tries it again. After an update the run loop watches `pending.json`: three stops within two minutes put `.activate-studio.previous` back and say so in `updates/rolled-back.json`, which Settings shows once; a server that stays up for a minute clears the mark. Settings, Updates keeps **Roll back** while `.activate-studio.previous` exists. Automatic installation when idle works as on macOS.

The swap is two renames in one directory, so the payload's path is missing for the moment between them; the running Studio is unaffected, since its files stay open in the moved tree. A process that dies in that moment (a kill, the computer losing power) would leave only `.activate-studio.previous` and the staged `.activate-studio.new`, so the swap is recoverable. Before the first rename the server writes `updates/swap.json` (the versions, and whether the window program changed), and removes it after `pending.json` is written. `studio-recover.sh`, in the payload's top directory, reads it:

- With the payload missing, the swap file naming a version, and `.activate-studio.new` complete (`studio-run.sh` and Node executable, the server, `update-signing.pub` and `manifest.json` present, and `app/build-info.json` at the version the swap file names), it moves `.new` into place and writes `pending.json`, so the three-stop rollback applies as after any update.
- With the payload missing and any other state (no swap file, a partial or wrong-version `.new`, a cut-off rollback, which takes two renames too), it moves `.activate-studio.previous` back and writes `rolled-back.json` saying the update was interrupted. A staged `.new` that no swap file vouches for is never promoted. The server removes the leftovers at its next start.
- With the payload there and a swap file left over, it removes the file, writing `pending.json` first when the payload is the version the file names.

`studio-run.sh` reads the script in at its start and runs it before every start of the server, so a run loop whose server died in the gap recovers and then runs the copy now at its path (its own loop, when the swap was finished). A launcher that finds `studio-run.sh` missing runs `studio-recover.sh TREE DATA_DIR` from whichever of `.activate-studio.new` or `.activate-studio.previous` holds a copy; the script uses its arguments, not its own location. The update that moves a payload without this check to one with it is done by the old server, which writes no swap file; a cut-off there is repaired by putting `.previous` back, and the update is offered again.

An atomic exchange (`renameat2` with `RENAME_EXCHANGE`) or a symlink flip would remove the gap, and neither was used. Node has no binding for the exchange, so it would need a helper process, and not every filesystem a home directory is on (NFS among them) supports it, so the two-rename path and its recovery would still be needed. A symlink flip needs a versioned directory behind a link, which this layout rules out above (a stable real directory), and converting an existing install to it has the same gap once. The Studio is the only writer.

### What a package install shows

A `deb` copy shows, under the **Version … is available** notice and in Settings, Updates: "This copy was installed from a .deb package, so it does not update itself" with `sudo apt update` and `sudo apt install --only-upgrade activate-studio` to copy, and says to quit and reopen ACTIVATE Studio afterward. An `rpm` copy says the same with `sudo dnf upgrade --refresh activate-studio`. There is no **Update now**, no automatic install and no rollback, and the install and rollback requests are refused with the same sentence.
