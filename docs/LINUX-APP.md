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

## What the update check does on Linux

The updater (`server/src/updates.ts`) installs only into a macOS app bundle: it needs `STUDIO_APP_BUNDLE`, and reads `Contents/Resources` and the macOS asset names. `studio-run.sh` does not set it, so the server runs the check, which reads the project's releases, and reports `mode: clone`, whether a newer release exists, and that it cannot install it. That is the only change from a clone's behavior; it does not install anything.
