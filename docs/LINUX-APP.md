# The Linux app (draft)

ACTIVATE Studio for Linux x86_64 is the macOS app's contents without Apple's parts: one directory tree (the Go server, GUFI, tesseract, the embedding model and the built web app, with no Node and no Python) that runs from any path, a window around it, and two ways to install it. Nothing here is published yet: the `linux-app` workflow builds the archive and the packages, and they are workflow artifacts.

| | Per-user tarball | .deb and .rpm packages |
|---|---|---|
| Who installs | you, with no administrator rights | an administrator, with apt or dnf |
| Where | `~/.local/lib/activate-studio` | `/opt/activate-studio` |
| Updates | the Studio updates itself (see "Updates") | apt or dnf only |
| Several users | each installs their own copy | one copy, each user has their own server and index |
| Remove | `activate-studio --uninstall` | apt or dnf |

Both come from the same tree, so they behave the same, and the update contract below (`install-kind`, a stable payload path, the `shell` entry) is what tells them apart. Your index, settings and logs are in your home folder either way (`~/.local/share/activate-studio-data`), so moving from one to the other keeps them.

## Requirements

A 64-bit Intel or AMD computer with glibc 2.28 or newer. Each release's checks run in containers of RHEL, Rocky Linux, AlmaLinux and Oracle Linux 8 and 9, CentOS Stream 9, Ubuntu 20.04, 22.04 and 24.04, Debian 11, 12 and 13, Fedora 43 and Amazon Linux 2023. RHEL 7 is too old. About 700 MB of disk for the program, plus the index.

For the Studio's own window, WebKitGTK and GTK 3:

| System | Package | Window shell used |
|---|---|---|
| Ubuntu 22.04, 24.04, Debian 12 and 13, Fedora | `libwebkit2gtk-4.1-0` | WebKitGTK 4.1 |
| RHEL, Rocky, AlmaLinux, Oracle Linux 8 and 9, CentOS Stream 9 | `webkit2gtk3` | WebKitGTK 4.0 |
| Debian 11, Ubuntu 20.04, Ubuntu 22.04 without 4.1 | `libwebkit2gtk-4.0-37` | WebKitGTK 4.0 |

The `.deb` and `.rpm` declare these as dependencies. For the tarball, they are installed by the system's package manager (the install script installs nothing outside your home folder). Without WebKitGTK, or without a desktop session, the Studio runs in the background and opens in the default browser instead; everything works there except the system folder chooser and the window's own handling of downloads.

## Dropping files and folders

WebKitGTK gives a page nothing of a drop from a file manager, and its file chooser cannot pick a folder, so the window handles both: it takes the drop, or opens the system folder chooser for **Choose a folder**, walks what it got, and gives the page each file's place and size under a random id. When the page's turn to send comes, the window streams those files from disk to the ordinary upload, so a large folder does not pass through the web view's memory. The page never names a path, and an id works for one send. Names that start with a dot are left out, and links are not followed. An empty folder in a drop is not created. Drops are taken on the Library page.

The window's context menu offers copy and paste over text and fields. Elsewhere it is off, as Back and Reload mean nothing in an app window.

Ctrl+= and Ctrl+- zoom the window in and out, in steps from 50% to 300%, and Ctrl+0 returns it to actual size. The zoom is WebKitGTK's own zoom level on the web view, as in a browser (`desktop/wails/zoom.go`), and is kept in `run/window-zoom` in the data folder for the next launch.

## The window's title bar

The window draws its own title bar (a GTK header bar), which takes the color the page names in its `theme-color` tag, as the macOS window's does: the classification banner's when one is shown, so the bar and the banner read as one strip, otherwise the page's ground in its light or dark theme, following changes while the window is open. The title and the window controls switch to light or dark to read on it. A bar the window manager draws cannot take the page's color, and `GTK_CSD=1` is not used for this because GTK ignores it where no compositor runs, as in many remote desktop sessions. The last color is kept in `run/bar-color` in the data folder, so the next launch opens with it. Dialogs such as the folder chooser keep the desktop's theme.

## Install

Per user, no administrator rights:

```
tar -xzf activate-studio-linux-x86_64.tar.gz
activate-studio/install.sh
```

The script copies the tree to `~/.local/lib/activate-studio` (a real directory, the same path for every version), writes `install-kind` (`user`) in it, the command `~/.local/bin/activate-studio`, a menu entry and icons under `~/.local/share`, and tells you if WebKitGTK is missing. `--move` moves the unpacked folder instead of copying it. Running it again replaces the install with the one unpacked, and keeps your data. Open ACTIVATE Studio from the applications menu, or run `activate-studio`.

For everyone on the computer:

```
sudo apt install ./activate-studio_<version>_amd64.deb      # Ubuntu, Debian
sudo dnf install ./activate-studio-<version>.x86_64.rpm     # RHEL 8 or 9, Rocky, Alma, Oracle
```

The `.deb` carries the WebKitGTK 4.1 window shell and depends on `libwebkit2gtk-4.1-0`, which Ubuntu 22.04 and 24.04 both have. The `.rpm` carries the 4.0 shell and depends on `webkit2gtk3`. Neither runs a script when it is installed. The package adds `/usr/bin/activate-studio` and a menu entry.

## Use

`activate-studio` opens the window: the Studio on your own documents folder (`~/Documents/ACTIVATE Studio` until you choose another, in setup or Settings, Indexing, Libraries, where **Choose another folder…** opens the system folder chooser). The window starts the server, which listens on 127.0.0.1 only, and stops it, and everything it started, when the window closes. Opening the program a second time brings the first window forward. Two users on one computer each get their own server, on their own port.

| Command | Does |
|---|---|
| `activate-studio` | opens the window, or the browser when there is no WebKitGTK |
| `activate-studio --browser` | runs the server in the background and opens the default browser |
| `activate-studio --stop` | closes the window and stops the server, or stops the background server |
| `activate-studio --uninstall [--purge] [--dry-run]` | removes a per-user install |
| `activate-studio --version` | the version, and which window shell would be used |

`STUDIO_NO_BROWSER=1` starts the background server and opens nothing. In the browser mode the server keeps running after the browser closes, until `--stop`.

Settings are read from `studio.env` in the data folder (`~/.local/share/activate-studio-data`, or `STUDIO_DATA_DIR`) before every start: `PORT`, `KB_ROOT`, and the others the Studio's Settings pages write. Logs are in `logs/studio.log` there. Without `PORT` the window uses the port it used last, or 4180, or a free one.

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
- The directory holding the payload must be writable by the user, because an update unpacks `.activate-studio.new` beside it and keeps the old payload as `.activate-studio.previous` until the new one has started cleanly. Nothing else in the tree is written by the Studio, so a package can install it read-only.
- A window shell inside the payload is named in the payload's `manifest.json` as `"shell": "path/inside/the/payload"` (here `bin/activate-studio-wk41`; the manifest also lists `shells` by name). When an update changes that file, the Studio tells the person to reopen the window; the run loop alone is restarted by the update. A payload with no `shell` entry has no window to reopen.
- A package that installs into `/opt/activate-studio` writes `install-kind` as `deb` or `rpm` and is named `activate-studio` (or writes the name after the kind). The commands the Studio shows are `sudo apt update` then `sudo apt install --only-upgrade activate-studio` for a `deb`, and `sudo dnf upgrade --refresh activate-studio` for an `rpm`.
- `STUDIO_APP_BUNDLE` stays unset for Linux. The server finds the payload from its own place (`<payload>/app/server/dist`), or from `STUDIO_PAYLOAD_ROOT` where that is set.

### What the window does about updates

The window shell supervises the server itself, through `studio-run.sh --once`, and reads the same contract as the run loop: exit 75 (a setting changed) and exit 76 (an update replaced the payload) start the server again, from the install's own path, which now holds the new payload, and the page reloads. After an update (`updates/pending.json` exists) a server that stops three times within two minutes makes the shell run `studio-run.sh --rollback`, which puts `.activate-studio.previous` back as the run loop does, and the server starts again from it. When the page asks to reopen (Reopen now, after an update that changed the window's program), the shell compares its own program with the file of the same name in the install; if they differ it stops the server, starts the install's launcher, which waits for the old window to end, and quits; otherwise only the server restarts.

### The linux channel

The channel is built like the macOS one, so one release flow serves both. A release that gets a Linux build has three more assets: `activate-studio-linux-x86_64.tar.gz`, `activate-studio-linux-x86_64.manifest.json` and `activate-studio-linux-x86_64.manifest.json.sig`. The same files are in a fixed release tagged `linux`, a prerelease that is never latest and that the update check skips because its tag is not a version; it is the address an installer and the docs use. The `linux-app` workflow does this only when started by hand with the `release` input (a published tag such as `v1.150`), after it has built that tag and run it on all five distributions; a run on a push or without the input publishes nothing. It signs the manifest with `deploy/linux/sign_release.sh`, with the key the macOS app uses, whose public half is `update-signing.pub` in the payload.

Releases are frequent and most do not get a Linux build. An install updates to the newest release on its channel (Stable, or Preview if chosen in Settings) that carries the three assets, and is told of no other. Channels, installing an older version, the state snapshot taken before every change of version, and releases that ask for the app to be reopened follow [`UPDATES.md`](UPDATES.md). A package install is told of a version on the same rule, which is a statement that a Linux build exists, not that the package repository has it yet.

### How a per-user install updates

The check runs on the same schedule as the macOS app's. For a release with the assets the server:

1. downloads the manifest and its signature and verifies the Ed25519 signature with `update-signing.pub` from its own payload; the manifest names the version, the asset, its SHA-256 and size, and `minGlibc`;
2. checks that the manifest's version is the release's and newer than the running one, and that the C library on this computer is at least `minGlibc`;
3. downloads the archive into the data directory's `updates` folder, stopping at the size the manifest gives and comparing the SHA-256 as it arrives;
4. unpacks it into `.activate-studio.new` beside the payload, after checking that every entry is inside `activate-studio/`; checks that `studio-run.sh`, Node, the server, the update key and `manifest.json` are there and that the version inside is the manifest's;
5. moves the running payload to `.activate-studio.previous` and the new one into its place (a failure of the second move puts the first back), writes `updates/pending.json`, and exits with status 76, on which `studio-run.sh` runs the new payload's own `studio-run.sh` in place of itself.

A download that fails any check leaves the payload as it was and removes what it fetched; automatic installation does not try that version again until the Studio restarts, and **Update now** tries it again. After an update the run loop watches `pending.json`: three stops within two minutes put `.activate-studio.previous` back and say so in `updates/rolled-back.json`, which Settings shows once; a server that stays up for a minute clears the mark, and once no process runs from `.activate-studio.previous` (the old window does until it is reopened), the Go server removes it. **Roll back** in Settings, Updates downloads the version before again and installs it (docs/UPDATES.md, "The copy kept beside the app, and Roll back"). Automatic installation when idle works as on macOS.

The swap is two renames in one directory, so the payload's path is missing for the moment between them; the running Studio is unaffected, since its files stay open in the moved tree. A process that dies in that moment (a kill, the computer losing power) would leave only `.activate-studio.previous` and the staged `.activate-studio.new`, so the swap is recoverable. Before the first rename the server writes `updates/swap.json` (the versions, and whether the window program changed), and removes it after `pending.json` is written. `studio-recover.sh`, in the payload's top directory, reads it:

- With the payload missing, the swap file naming a version, and `.activate-studio.new` complete (`studio-run.sh` and Node executable, the server, `update-signing.pub` and `manifest.json` present, and `app/build-info.json` at the version the swap file names), it moves `.new` into place and writes `pending.json`, so the three-stop rollback applies as after any update.
- With the payload missing and any other state (no swap file, a partial or wrong-version `.new`, a cut-off rollback, which takes two renames too), it moves `.activate-studio.previous` back and writes `rolled-back.json` saying the update was interrupted. A staged `.new` that no swap file vouches for is never promoted. The server removes the leftovers at its next start.
- With the payload there and a swap file left over, it removes the file, writing `pending.json` first when the payload is the version the file names.

`studio-run.sh` reads the script in at its start and runs it before every start of the server, so a run loop whose server died in the gap recovers and then runs the copy now at its path (its own loop, when the swap was finished). Entry points outside the directory do the same before they start anything: `studio-recover.sh TREE DATA_DIR` is run from whichever of `.activate-studio.new` or `.activate-studio.previous` holds a copy (the script uses its arguments, not its own location), and with no script in either, `.activate-studio.previous` is moved back. The command `install.sh` writes at `~/.local/bin/activate-studio`, which the menu entry runs, does this when `studio-run.sh` is missing at the install's path, and the window shell does it before each start of the server, which also covers a window left open across the cut-off update. The update that moves a payload without this check to one with it is done by the old server, which writes no swap file; a cut-off there is repaired by putting `.previous` back, and the update is offered again.

An atomic exchange (`renameat2` with `RENAME_EXCHANGE`) or a symlink flip would remove the gap, and neither was used. Node has no binding for the exchange, so it would need a helper process, and not every filesystem a home directory is on (NFS among them) supports it, so the two-rename path and its recovery would still be needed. A symlink flip needs a versioned directory behind a link, which this layout rules out above (a stable real directory), and converting an existing install to it has the same gap once. The Studio is the only writer.

### What a package install shows

A `deb` copy shows, under the **Version … is available** notice and in Settings, Updates: "This copy was installed from a .deb package, so it does not update itself" with `sudo apt update` and `sudo apt install --only-upgrade activate-studio` to copy, and says to quit and reopen ACTIVATE Studio afterward. An `rpm` copy says the same with `sudo dnf upgrade --refresh activate-studio`. There is no **Update now**, no automatic install and no rollback, and the install and rollback requests are refused with the same sentence.

## Uninstall

Per user: `activate-studio --uninstall`. It lists what it removes (the program and any copy an update left beside it, the command, the menu entry and icons, the window's cache), keeps your index, settings and logs, and never touches your documents. `--purge` removes the index, settings and logs too, and `--dry-run` lists what would go and what stays, one `kind<TAB>path` line each. Packages: `sudo apt remove activate-studio` or `sudo dnf remove activate-studio`. Data in your home folder stays until you delete `~/.local/share/activate-studio-data`.

## What is in the tree

```
activate-studio/
  bin/activate-studio           the launcher
  bin/activate-studio-wk41      the window shell for WebKitGTK 4.1
  bin/activate-studio-wk40      the window shell for WebKitGTK 4.0
  install.sh, uninstall.sh      (not in packages)
  install-kind                  written by install.sh or the package
  node/  app/  gufi/  ocr/  models/  licenses/
  share/                        icons, menu entry template
  studio-run.sh  studio-recover.sh  manifest.json  update-signing.pub  LICENSE  NOTICE
```

| Path | Contents |
|---|---|
| `node/` | stand-ins only: `node/bin/node` and `app/server/dist/main.js` are what the v1.187 and v1.188 updaters check for before they install, marked by `node/PLACEHOLDER` |
| `app/` | `bin/` (`studio-server`, `semantic-map`), `lib/libonnxruntime.so.1`, `occt/` (the STEP preview's modules), the built web app, `docs/`, `CHANGELOG.md`, `build-info.json` |
| `gufi/` | GUFI 0.8.1, the lean build (sqlite-vec, no llama.cpp), with PCRE2 and `libgomp.so.1` |
| `ocr/` | tesseract and leptonica with the English model |
| `models/` | the ONNX embedding model (MiniLM) and its tokenizer |
| `licenses/` | the licenses of ONNX Runtime and the STEP modules (LGPL) |
| `studio-run.sh` | the run loop: starts the server with the payload's own programs, restarts it on exit 75 |
| `studio-recover.sh` | finishes or undoes an update cut off between its two moves (see Updates) |
| `manifest.json` | what it was built from and the system libraries it needs |

The window shell is a small Go program (Wails v2, `desktop/wails`) that links to the computer's GTK and WebKitGTK. It starts the server through `studio-run.sh --once`, waits for `/api/version`, and shows the Studio through a reverse proxy it serves on 127.0.0.1, which gives the page the same bridge the macOS window gives (folder chooser, following the server to another port, relaunch after an update, downloads into `~/Downloads`, links that leave the Studio opening in the browser). It starts the server again after exit 75 or 76, shows an error with **Try Again** after any other exit, and ends the server and everything it started when the window closes, on TERM, and when the shell itself is killed (a guard process does that).

This is the Go version, the default of `deploy/linux/build_app.sh` (`STUDIO_SERVER=go-only`, docs/UPDATES.md "The Go version"). The payload of v1.188 and earlier (`STUDIO_SERVER=node`) has instead Node 26 in `node/` with `libatomic.so.1`, python-build-standalone 3.12 in `python/` with pypdf, xlrd, olefile, onnxruntime, tokenizers and numpy, the Node server with its production `node_modules` and the indexer in `app/`, GUFI with sqlite-lembed, and the GGUF model. The window shell reads the tree at each start of the server: `"server": "go"` in `manifest.json` (or `STUDIO_GO_SERVER=1`) starts the Go server through `studio-run.sh`, and a rollback to a Node payload under a running window starts Node.

## What was measured

The archive is about 116 MB (the tree 274 MB: the Go programs 44 MB, the `studio-remote` helpers for Linux x86_64 and aarch64 and macOS arm64 73 MB (about 29 MB compressed), ONNX Runtime 28 MB, the models 23 MB, GUFI 22 MB, the window shells 22 MB, the web app 36 MB, the STEP modules 15 MB, tesseract 13 MB), against 278 MB (757 MB unpacked) for the Node payload of v1.188, and 307 MB for the macOS app (an archive of 128 MB, an image of 144 MB), which carries the same three helpers. In CI, GUFI builds in about 8 to 9 minutes, tesseract in 2 to 3, and the payload is assembled in under a minute after the web and server build. The build limits the tree to symbol versions of glibc 2.28, libstdc++ 3.4.25 (GCC 8) and CXXABI 1.3.11, so it needs RHEL 8's C libraries or newer. The WebKitGTK 4.1 shell is the exception: it is built on Ubuntu 22.04 and needs glibc 2.35, and it is used only where WebKitGTK 4.1 is installed.

The system libraries the tree needs are the C library (libc, libm, libdl, libpthread, librt, libutil, ld-linux), libstdc++, libgcc_s and zlib: packages `libc6`, `libstdc++6`, `libgcc-s1` and `zlib1g` on Ubuntu, `glibc`, `libstdc++`, `libgcc` and `zlib` on RHEL. Every desktop has them, and the clean images the tests start from needed no other package (the Rocky 8 images leave out findutils, which the checks themselves use and the Studio does not). Node's libatomic and GCC's libgomp, which a RHEL install may lack, are in the tree.

## Known limits

- RHEL 9's WebKitGTK cannot construct `Intl.NumberFormat`; the Studio's page carries a small guard (`web/index.html`) that stands in for it. Only the pages the build's check opens were tried under that engine.
- Not tested: Wayland, HiDPI, a physical GPU or the 3D map, SELinux enforcing, a real desktop session, microphone permission in the window, aarch64.
- The window shell is not code signed and the packages are not signed.
- On Fedora 43 the window crashed at launch twice in about ten test launches (in `gtk_main`, in a container on a remote desktop), and opened on the next try. The cause is not known.
- Ubuntu 24.04 restricts unprivileged user namespaces; WebKitGTK's sandbox (bubblewrap) works there through Ubuntu's own profile for `/usr/bin/bwrap`.

## How it is built and tested

`.github/workflows/linux-app.yml` builds the lean GUFI and tesseract in a Rocky Linux 8 container (glibc 2.28, with GCC 13 from `gcc-toolset-13` and Python 3.11, as GCC 8 and Python 3.6 are too old for them), the two window shells (4.1 on Ubuntu 22.04, 4.0 on Rocky 8), puts the Go tree together (`deploy/linux/build_app.sh`, `desktop/linux/add_desktop.sh`), and builds the packages with nfpm (`desktop/linux/build_packages.sh`). `deploy/linux/check_app.sh` runs the payload in clean containers of Ubuntu 22.04, 24.04, Rocky 8 and 9 and AlmaLinux 8 and 9, with the image's `python3` added for the checks. `deploy/linux/test_go_bridge_e2e.sh` installs the last release (Node), updates it to the new payload through that release's own updater on the Stable and the Preview channel, and rolls back; `deploy/app-go.def` builds the payload into the Go container image. The earlier payloads (`node`, and `go` with Node behind it) are built only when a run asks for them (the `legacy` input), and do not fail it. `desktop/linux/test_install.sh` installs each package type in a clean container, launches it under Xvfb, checks that the Studio draws and a search finds a document, quits, checks that nothing is left running, uninstalls, and checks that nothing is left in the home folder but the data. `deploy/linux/elf_check.py` fails the build if a file in the tree needs a newer glibc than RHEL 8's.

The sizes of the Node payload of v1.188 were an archive of about 264 MiB, each package about 246 MiB, and a tree of 700 MB; the Go payload's are above.
