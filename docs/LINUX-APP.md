# The Linux app (draft)

ACTIVATE Studio for Linux x86_64 is the macOS app's contents without Apple's parts: one directory tree (Node, Python with its packages, GUFI with its AI pieces and models, tesseract, the built server and web app) that runs from any path, a window around it, and two ways to install it. Nothing here is published yet: the `linux-app` workflow builds the archive and the packages, and they are workflow artifacts.

| | Per-user tarball | .deb and .rpm packages |
|---|---|---|
| Who installs | you, with no administrator rights | an administrator, with apt or dnf |
| Where | `~/.local/share/activate-studio` | `/opt/activate-studio` |
| Updates | the Studio updates itself (see "Updates") | apt or dnf only |
| Several users | each installs their own copy | one copy, each user has their own server and index |
| Remove | `activate-studio --uninstall` | apt or dnf |

Both come from the same tree, so they behave the same. Your index, settings and logs are in your home folder either way (`~/.local/share/activate-studio-data`), so moving from one to the other keeps them.

## Requirements

A 64-bit Intel or AMD computer with glibc 2.34 or newer: Ubuntu 22.04 and 24.04, Debian 12, RHEL 9 and its rebuilds (Rocky Linux 9, AlmaLinux 9), or newer. RHEL 8 and Ubuntu 20.04 are too old. About 700 MB of disk for the program, plus the index.

For the Studio's own window, WebKitGTK and GTK 3:

| System | Package | Window shell used |
|---|---|---|
| Ubuntu 22.04, 24.04, Debian 12, Fedora | `libwebkit2gtk-4.1-0` | WebKitGTK 4.1 |
| RHEL 9, Rocky 9, AlmaLinux 9 | `webkit2gtk3` | WebKitGTK 4.0 |
| Debian 11, Ubuntu 22.04 without 4.1 | `libwebkit2gtk-4.0-37` | WebKitGTK 4.0 |

The `.deb` and `.rpm` declare these as dependencies. For the tarball, they are installed by the system's package manager (the install script installs nothing outside your home folder). Without WebKitGTK, or without a desktop session, the Studio runs in the background and opens in the default browser instead; everything works there except the system folder chooser and the window's own handling of downloads.

## Install

Per user, no administrator rights:

```
tar -xzf activate-studio-linux-x86_64.tar.gz
activate-studio/install.sh
```

The script copies the tree to `~/.local/share/activate-studio/<version>`, points `~/.local/share/activate-studio/current` at it, writes the command `~/.local/bin/activate-studio`, a menu entry and icons under `~/.local/share`, and tells you if WebKitGTK is missing. `--move` moves the unpacked folder instead of copying it. Running it again reinstalls the same version. Open ACTIVATE Studio from the applications menu, or run `activate-studio`.

For everyone on the computer:

```
sudo apt install ./activate-studio_<version>_amd64.deb      # Ubuntu, Debian
sudo dnf install ./activate-studio-<version>.x86_64.rpm     # RHEL 9, Rocky, Alma
```

The `.deb` carries the WebKitGTK 4.1 window shell and depends on `libwebkit2gtk-4.1-0`, which Ubuntu 22.04 and 24.04 both have. The `.rpm` carries the 4.0 shell and depends on `webkit2gtk3`. Neither runs a script when it is installed. The package adds `/usr/bin/activate-studio` and a menu entry.

## Use

`activate-studio` opens the window: the Studio on your own documents folder (`~/Documents/ACTIVATE Studio` until you choose another, in setup or Settings, Libraries, where **Choose another folder…** opens the system folder chooser). The window starts the server, which listens on 127.0.0.1 only, and stops it, and everything it started, when the window closes. Opening the program a second time brings the first window forward. Two users on one computer each get their own server, on their own port.

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

Packages are updated by apt or dnf only; the Studio's own update check reports that and does not offer to install. A per-user install updates itself from the `linux` release channel, which is not published yet; until it is, install a newer tarball over the old one with `install.sh`, which keeps your data. A new version is placed beside the old one and `current` moves to it, so the window and the browser mode both start the new version at their next restart.

## Uninstall

Per user: `activate-studio --uninstall`. It lists what it removes (the program, the command, the menu entry and icons, the window's cache), keeps your index, settings and logs, and never touches your documents. `--purge` removes the index, settings and logs too, and `--dry-run` lists what would go and what stays, one `kind<TAB>path` line each. Packages: `sudo apt remove activate-studio` or `sudo dnf remove activate-studio`. Data in your home folder stays until you delete `~/.local/share/activate-studio-data`.

## Install layout and markers

For the updater (`server/src/updates.ts`) and the scripts, these are fixed. `studio-run.sh` and the server run from the tree whichever way it was installed; the tree is the folder that holds `studio-run.sh` and `manifest.json`, and is found from the server's own location (`<tree>/app/server/dist/main.js`).

Per-user install, with `R` = `${XDG_DATA_HOME:-~/.local/share}/activate-studio`:

| Path | Meaning |
|---|---|
| `R/install.json` | the marker: `{"layout": 1, "kind": "user", "installer": "install.sh", "current": "current", "version", "installedAt"}`. Present means the install is self-updating if `R` is writable. |
| `R/<version>/` | a whole tree, as unpacked from the archive (its name is the manifest's `version`, made safe for a file name) |
| `R/current` | a relative symlink to the running version's folder; replaced atomically (`mv -T`) |
| `R/previous` | optional symlink an updater may keep to the version before, for rollback |
| `~/.local/bin/activate-studio` | a script that sets `STUDIO_TREE=R/current` and runs `R/current/bin/activate-studio` |
| `~/.local/share/applications/activate-studio.desktop`, `~/.local/share/icons/hicolor/*/apps/activate-studio.png` | menu entry and icons, written by `install.sh` |

An update unpacks the new archive to `R/.<version>.new`, checks it, renames it to `R/<version>`, swaps `current`, and exits the server with status 76. Both the window shell and `studio-run.sh` start the server again through the path `R/current/studio-run.sh` (the shell keeps the logical path, from `STUDIO_TREE`), so the new copy runs. Old versions are the updater's to prune; `uninstall.sh` removes `R` whole.

Package install: the tree is `/opt/activate-studio`, owned by root, and `/opt/activate-studio/PACKAGE-MANAGED` exists (its content is `deb` or `rpm`). That file means "updated by apt or dnf": the updater must report this and offer no install, whatever the permissions. A tree with neither `R/install.json` beside it nor `PACKAGE-MANAGED` in it is an archive unpacked and run in place; it has no update path.

The window shell's contract with `studio-run.sh`: `studio-run.sh --once DATA_DIR` starts the server once and exits with its exit status; the shell reads 75 (a setting changed) and 76 (updated) as restart requests. `STUDIO_RUN_PORT` is the port to use unless `studio.env` names one.

## What is in the tree

```
activate-studio/
  bin/activate-studio           the launcher
  bin/activate-studio-wk41      the window shell for WebKitGTK 4.1
  bin/activate-studio-wk40      the window shell for WebKitGTK 4.0
  install.sh, uninstall.sh      (not in packages)
  node/  python/  app/  gufi/  ocr/  models/
  share/                        icons, menu entry template
  studio-run.sh  manifest.json  update-signing.pub  LICENSE  NOTICE
```

| Path | Contents |
|---|---|
| `node/` | Node 26 for Linux x64, with `lib/libatomic.so.1` beside it (Node's run path finds it) |
| `python/` | python-build-standalone 3.12 with pypdf, xlrd, olefile, onnxruntime, tokenizers and numpy |
| `app/` | the built server with its production `node_modules` (including the document converter's Linux binary), the built web app, the indexer, `docs/`, `CHANGELOG.md`, `build-info.json` |
| `gufi/` | GUFI 0.8.0 with sqlite-vec and sqlite-lembed, PCRE2 and `libgomp.so.1` |
| `ocr/` | tesseract and leptonica with the English model |
| `models/` | the GGUF and ONNX embedding models |
| `studio-run.sh` | the run loop: starts the server with the payload's own programs, restarts it on exit 75 |
| `manifest.json` | what it was built from and the system libraries it needs |

The window shell is a small Go program (Wails v2, `desktop/wails`) that links to the computer's GTK and WebKitGTK. It starts the server through `studio-run.sh --once`, waits for `/api/version`, and shows the Studio through a reverse proxy it serves on 127.0.0.1, which gives the page the same bridge the macOS window gives (folder chooser, following the server to another port, relaunch after an update, downloads into `~/Downloads`, links that leave the Studio opening in the browser). It starts the server again after exit 75 or 76, shows an error with **Try Again** after any other exit, and ends the server and everything it started when the window closes, on TERM, and when the shell itself is killed (a guard process does that).

## Known limits

- RHEL 9's WebKitGTK cannot construct `Intl.NumberFormat`; the Studio's page carries a small guard (`web/index.html`) that stands in for it. Only the pages the build's check opens were tried under that engine.
- Not tested: Wayland, HiDPI, a physical GPU or the 3D map, SELinux enforcing, a real desktop session, microphone permission in the window, aarch64.
- The window shell is not code signed and the packages are not signed.
- Ubuntu 24.04 restricts unprivileged user namespaces; WebKitGTK's sandbox (bubblewrap) works there through Ubuntu's own profile for `/usr/bin/bwrap`.

## How it is built and tested

`.github/workflows/linux-app.yml` builds GUFI and tesseract in a Rocky Linux 9 container (glibc 2.34), the two window shells (4.1 on Ubuntu 22.04, 4.0 on Rocky 9), puts the tree together (`deploy/linux/build_app.sh`, `desktop/linux/add_desktop.sh`), and builds the packages with nfpm (`desktop/linux/build_packages.sh`). `deploy/linux/check_app.sh` runs the payload in clean containers of Ubuntu 22.04, 24.04, Rocky 9 and AlmaLinux 9. `desktop/linux/test_install.sh` installs each package type in a clean container, launches it under Xvfb, checks that the Studio draws and a search finds a document, quits, checks that nothing is left running, uninstalls, and checks that nothing is left in the home folder but the data. `deploy/linux/elf_check.py` fails the build if a file in the tree needs a newer glibc than RHEL 9's.

The sizes: the archive is about 264 MiB, each package about 246 MiB, the tree 700 MB.
