# GUFI for macOS 14 and later

The macOS app carries [GUFI](https://github.com/mar-file-system/GUFI), built from source by this repository for macOS 14 and later on Apple silicon. This page covers the published build, how to reproduce it, what the build does, and how it is checked and published. GUFI's own macOS package declares macOS 27 as the minimum for every file and loads two libraries from a Homebrew install, which is why the app does not use it.

## The published build

The release [`gufi-1713537-macos14`](https://github.com/parallelworks/activate-studio/releases/tag/gufi-1713537-macos14) is a prerelease of this repository, separate from the app's releases. It holds `gufi-macos-arm64.tar.gz`, its `.sha256`, and `gufi-macos-arm64.manifest.json`, which records what was built:

| | |
|---|---|
| GUFI | commit `1713537a` (GUFI's `macos-pkg-test` tag) |
| Runs on | macOS 14.0 and later, Apple silicon |
| Bundled by GUFI | SQLite 3.53.4, sqlite3-pcre, sqlite-vec 0.1.10-alpha.4, sqlite-lembed 0.0.1-alpha.8, llama.cpp, jemalloc |
| Built here from release sources | LLVM OpenMP 23.1.4, PCRE2 10.49 |
| Built with | Homebrew clang 23.1, macOS SDK 15.5, on macOS 15.7 |

The archive unpacks to `gufi/` with `bin` (every GUFI tool), `lib` (`gufi_vt.dylib`, `vec0.dylib`, `lembed0.dylib`, the llama.cpp and ggml libraries, `libomp.dylib`, `libpcre2-8.0.dylib`), `licenses`, `README.txt` and `manifest.json`. The tools load their libraries from `lib` wherever the tree is, with no `DYLD_LIBRARY_PATH`.

```
curl -fsSLO https://github.com/parallelworks/activate-studio/releases/download/gufi-1713537-macos14/gufi-macos-arm64.tar.gz
curl -fsSLO https://github.com/parallelworks/activate-studio/releases/download/gufi-1713537-macos14/gufi-macos-arm64.tar.gz.sha256
shasum -a 256 -c gufi-macos-arm64.tar.gz.sha256
tar -xzf gufi-macos-arm64.tar.gz
export PATH="$PWD/gufi/bin:$PATH"     # gufi_vt starts gufi_query by name
gufi_dir2index -x ~/Documents/notes /tmp/idx
gufi_query -d ' ' -E 'SELECT name FROM vrpentries;' /tmp/idx/notes
```

`gufi_sqlite3` has gufi_vt, sqlite-vec and sqlite-lembed built in. A file downloaded with curl carries no quarantine flag; one downloaded with a browser needs `xattr -dr com.apple.quarantine gufi` before macOS runs it, since the files are signed ad hoc, not by an identified developer.

## Building it

On a Mac with Apple silicon, the Command Line Tools (`xcode-select --install`) and Homebrew:

```
git clone https://github.com/parallelworks/activate-studio
cd activate-studio
deploy/mac/build_gufi.sh
deploy/mac/check_gufi.sh deploy/mac/out/gufi-macos-arm64.tar.gz
```

The build takes about six minutes on a GitHub `macos-15` runner and writes the archive, its `.sha256` and the manifest to `deploy/mac/out`. It installs its build tools with Homebrew unless `SKIP_BREW=1`. Other settings:

| Variable | Default | |
|---|---|---|
| `GUFI_REF` | `1713537a…` | GUFI branch, tag or commit to clone |
| `GUFI_SRC` | | an existing GUFI checkout, built as it is |
| `MACOSX_DEPLOYMENT_TARGET` | `14.0` | the oldest macOS the tree runs on |
| `OUT_DIR`, `CACHE_DIR` | `deploy/mac/out`, `deploy/mac/.cache` | outputs, and downloads kept between builds |
| `JOBS` | the number of CPUs | parallel build jobs |

Homebrew must not have `jemalloc` installed: GUFI links one that pkg-config finds instead of building its own, and Homebrew's targets a newer macOS. The build stops if it finds one.

## What the build does

1. *Build tools:* GUFI's `contrib/CI/macos.sh` list from Homebrew (CMake, pkgconf, LLVM, the autotools, gettext, and the GNU coreutils, findutils, sed, patch, grep, diffutils and flock, first on `PATH` for the build). Nothing the tree loads comes from Homebrew.
2. *The deployment target* goes into the environment as `MACOSX_DEPLOYMENT_TARGET`, which clang and CMake both read, so it reaches the nested configure and CMake runs GUFI uses for its bundled dependencies. A flag given to GUFI's own CMake does not reach those.
3. *LLVM OpenMP and PCRE2* are built from their release sources, pinned by SHA-256, for the same target: Homebrew's bottles of both need a newer macOS. PCRE2 is built shared with JIT, so every GUFI file links the one copy in the tree.
4. *A compiler wrapper.* The llama.cpp that sqlite-lembed vendors uses `errno`, `EXIT_FAILURE`, `std::fill` and `std::hardware_destructive_interference_size` without including their headers, which libc++ in LLVM 23 no longer pulls in through others. GUFI's dependency build sets its own flags for that project, so `CXXFLAGS` does not reach it; the C++ compiler is a wrapper that includes the common standard headers in every compile.
5. *GUFI* with `-DDEP_AI=On` and OpenMP pointed at the libomp from step 3, then `cmake --install --component Local` under a `DESTDIR`, since the install also writes an example config to `/etc/GUFI`.
6. *The tree:* `bin` and `lib` from the install, plus libomp and PCRE2. `libllama-common` (which links OpenSSL and cJSON, not in the tree) and `libpresidio_plugin`, which nothing loads, are left out, with static libraries, build metadata and links to files that were not installed.
7. *Checks on the build:* no file links a library from Homebrew or `/usr/local`, and the linker reported no object built for a newer macOS than the target.
8. *Relinking* (`deploy/mac/macho_relink.py`): every library reference and rpath that names the build machine is rewritten relative to the file (`@loader_path`), so the tree runs from wherever it is unpacked. Each file is then signed ad hoc, since Apple silicon runs only signed code and the rewrite invalidates the old signature.
9. *Licenses* for GUFI, sqlite-vec, sqlite-lembed (which states its license in `sqlite-dist.toml` rather than a file), llama.cpp, jemalloc, LLVM OpenMP and PCRE2 go into `licenses/`. SQLite and sqlite3-pcre are in the public domain.
10. *README.txt and manifest.json*, a static check of the tree, and the archive.

## How it is checked

`deploy/mac/check_gufi.sh TREE|ARCHIVE` checks on any host that the expected tools, libraries, licenses and manifest are there, and that every Mach-O file is arm64, signed, needs no newer macOS than the manifest says, and loads its libraries from the tree or the system without `DYLD_LIBRARY_PATH`, never a system copy of a library the tree carries. On a Mac it also indexes a folder with `gufi_dir2index`, queries it with `gufi_query` and through `gufi_vt`, embeds text with sqlite-lembed and a 384-dimension model, and ranks with sqlite-vec, all through `/bin/sh` with no `DYLD_*` variables, as gufi_vt and scripts start the tools. It then confirms from dyld's own list that only the tree's libraries and the system's were loaded.

## CI and publishing

The `gufi-macos` workflow builds on a `macos-15` runner, runs `check_gufi.sh` there, and runs it again on clean runners that did not build it, `macos-14` by default, which is the oldest macOS the build targets. Run by hand from the Actions tab, it takes a GUFI ref, a deployment target and the test runners, and keeps the archive as a workflow artifact for 30 days. Pushing a tag named `gufi-*` builds with the defaults and attaches the archive to that tag's release, as a prerelease that is never marked latest, so the app's installer and update check keep finding the app's own releases.

To move the app to a newer GUFI: run the workflow with the new `gufi_ref` and check its archive in an app build (the `mac-app` workflow's `gufi_run` input); change `GUFI_REF` in `build_gufi.sh` to that commit; push a tag such as `gufi-<commit>-macos14`; and set `GUFI_RELEASE` in `.github/workflows/mac-app.yml` to the tag.
