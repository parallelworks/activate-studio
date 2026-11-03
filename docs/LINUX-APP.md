# The Linux app payload (draft)

This page describes the Linux x86_64 payload of ACTIVATE Studio, which the `linux-app` workflow builds. It is the contents of the macOS app without a window or a package: a directory tree that runs from any path. There is no installer, no window and no update channel yet, and nothing is published; the archive is a workflow artifact.

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
| `manifest.json` | what it was built from and the system libraries it needs |

Run it with `activate-studio/studio-run.sh DATA_DIR`. `DATA_DIR` holds `studio.env` (the settings, such as `PORT=4180` and `KB_ROOT`), `index/` and `logs/`; the server listens on 127.0.0.1 only.

## How it is built

`.github/workflows/linux-app.yml` builds GUFI and tesseract in a Rocky Linux 9 container (glibc 2.34), puts the payload together on an Ubuntu runner (`deploy/linux/build_app.sh`), and runs it in clean Ubuntu 22.04, Ubuntu 24.04 and Rocky Linux 9 containers as a plain user (`deploy/linux/check_app.sh`). `deploy/linux/elf_check.py` reads every ELF file in the tree and fails the build if one needs a glibc, libstdc++ or libgcc symbol newer than RHEL 9 has, or a library that is neither in the tree nor one every desktop has. The versions of the sources the macOS and Linux builds share are in `deploy/common/pins.sh`.

## What was measured

The archive is about 255 MiB (the tree 687 MB: Node 144 MB, Python 196 MB, GUFI 182 MB, tesseract 12 MB, the server's `node_modules` 69 MB, the web app 36 MB, the models 47 MB), against 503 MB for the macOS app, whose GUFI is 55 MB and Python 142 MB. In CI, GUFI builds in about 8 to 9 minutes, tesseract in 2 to 3, and the payload is assembled in under a minute after the web and server build. The newest symbol versions anywhere in the tree are glibc 2.34, libstdc++ 3.4.29 (GCC 11) and CXXABI 1.3.13, so it needs RHEL 9's or Ubuntu 22.04's C libraries or newer, and does not run on RHEL 8 or Ubuntu 20.04.

The system libraries the tree needs are the C library (libc, libm, libdl, libpthread, librt, libutil, ld-linux), libstdc++, libgcc_s and zlib: packages `libc6`, `libstdc++6`, `libgcc-s1` and `zlib1g` on Ubuntu, `glibc`, `libstdc++`, `libgcc` and `zlib` on RHEL 9. Every desktop has them, and the clean images the tests start from needed no other package. Node's libatomic and GCC's libgomp, which a RHEL 9 install may lack, are in the tree.

## What the update check does on Linux

The updater (`server/src/updates.ts`) installs only into a macOS app bundle: it needs `STUDIO_APP_BUNDLE` to name a writable `.app`, reads `Contents/Resources/update-signing.pub`, takes the `activate-studio-macos-arm64` assets and restarts through a LaunchAgent. `studio-run.sh` does not set `STUDIO_APP_BUNDLE`, so the server runs in its clone mode: it checks the project's releases on its schedule and reports whether a newer one exists (`available`), but `canInstall` is false, the install request is refused with 409, and the reason it gives is the clone's ("runs from a clone of the repository ... git pull && pnpm install && pnpm build"), which is wrong advice for this payload. The check also counts a release as newer when it has no Linux asset.
