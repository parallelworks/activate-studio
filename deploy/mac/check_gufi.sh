#!/usr/bin/env bash
# Check GUFI built for the macOS app: a tree from deploy/mac/build_gufi.sh,
# or its archive.
#
# On any host: the layout (the tools and libraries the Studio runs, the
# licenses, the manifest), and that every Mach-O file is arm64, signed,
# runs on the macOS release the manifest names (or --max-minos), and loads
# its libraries from the tree or the system without DYLD_LIBRARY_PATH,
# never a system copy of a library the tree carries (macho_check.py).
#
# On a Mac with Apple silicon, unless --static, also what the Studio does
# with it, with no DYLD_* variables and through /bin/sh as gufi_vt and the
# indexer start the tools: index a folder, query it with gufi_query and
# through gufi_vt, embed text with sqlite-lembed and the model, and search
# with sqlite-vec; and that only the tree's libraries and the system's load.
#
# Usage: deploy/mac/check_gufi.sh [--static] [--max-minos VERSION] TREE|ARCHIVE
#   GUFI_TEST_MODEL  the embedding model (default: the pinned model,
#                    fetched into CACHE_DIR)
#   CACHE_DIR        downloads, kept between runs (default deploy/mac/.cache)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
MODEL_COMMIT=7a7bac37782986fe1d4f213de771a8a3d9170b35    # as build_app.sh
MODEL_SHA256=71f1d177171468fb5f186c07019e303015aea17af275a67767760bba7be8d2e6
USAGE="usage: check_gufi.sh [--static] [--max-minos VERSION] TREE|ARCHIVE"

die() { echo "check_gufi: $*" >&2; exit 1; }
STATIC=0 MAX='' ARG=''
while [ $# -gt 0 ]; do
  case "$1" in
    --static) STATIC=1; shift ;;
    --max-minos) MAX="${2:?$USAGE}"; shift 2 ;;
    -*) echo "$USAGE" >&2; exit 2 ;;
    *) [ -z "$ARG" ] || { echo "$USAGE" >&2; exit 2; }; ARG="$1"; shift ;;
  esac
done
[ -n "$ARG" ] || { echo "$USAGE" >&2; exit 2; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'
}

# Resolved, since dyld reports the paths it loads that way (on macOS the
# temporary directory is reached through a /var symlink).
T="$(cd "$(mktemp -d)" && pwd -P)"
trap 'rm -rf "$T"' EXIT
if [ -d "$ARG" ]; then
  G="$(cd "$ARG" && pwd -P)"
elif [ -f "$ARG" ]; then
  if [ -f "$ARG.sha256" ]; then
    want="$(awk '{print $1; exit}' "$ARG.sha256")"
    got="$(sha256_of "$ARG")"
    [ "$want" = "$got" ] || die "$ARG has SHA-256 $got; its .sha256 says $want"
  fi
  tar -xzf "$ARG" -C "$T"
  G="$T/gufi"
  [ -d "$G" ] || die "$ARG holds no gufi/ directory"
else
  die "$ARG does not exist"
fi
echo "Checking $G"

failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
# check WHAT CMD...: runs CMD quietly, and shows the end of its output when it fails.
check() {
  local what="$1" out
  shift
  if out="$("$@" 2>&1)"; then ok "$what"; else
    bad "$what"
    [ -z "$out" ] || printf '%s\n' "$out" | tail -n 15 | sed 's/^/        /'
  fi
}

echo "Layout"
for f in bin/gufi_query bin/gufi_dir2index bin/gufi_sqlite3 \
  lib/gufi_vt.dylib lib/vec0.dylib lib/lembed0.dylib lib/libsqlite3-pcre.dylib lib/libomp.dylib lib/libpcre2-8.0.dylib \
  licenses/GUFI-LICENSE.txt licenses/sqlite-vec-LICENSE-MIT.txt licenses/sqlite-lembed-LICENSE.txt \
  licenses/llama.cpp-LICENSE.txt licenses/jemalloc-COPYING.txt licenses/libomp-LICENSE.txt licenses/pcre2-LICENCE.md \
  README.txt manifest.json; do
  check "$f" test -s "$G/$f"
done
TARGET="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["deploymentTarget"])' "$G/manifest.json" 2>/dev/null || true)"
MAX="${MAX:-$TARGET}"
if [ -n "$TARGET" ]; then ok "the manifest says it was built for macOS $TARGET"; else bad "the manifest names no deployment target"; fi

echo "Native code"
NATIVE=("$G"/bin/* "$G"/lib/*.dylib)
FLOOR=()
[ -z "$MAX" ] || FLOOR=(--max-minos "$MAX")
if out="$(python3 "$HERE/macho_check.py" ${FLOOR[@]+"${FLOOR[@]}"} --bundled "$G/lib" "${NATIVE[@]}" 2>&1)"; then
  ok "arm64, signed, runs on macOS ${MAX:-(any)}, loads its libraries from the tree or the system"
else
  bad "Mach-O check:"
  printf '%s\n' "$out" | sed 's/^/        /'
fi
elf="$(python3 - "$G" <<'PY'
import os, sys
for d, _dirs, files in os.walk(sys.argv[1]):
    for f in files:
        p = os.path.join(d, f)
        if not os.path.islink(p):
            with open(p, 'rb') as fh:
                if fh.read(4) == b'\x7fELF':
                    print(p)
PY
)"
if [ -z "$elf" ]; then ok "no Linux (ELF) binaries"; else bad "Linux binaries:"; printf '        %s\n' "$elf"; fi

if [ "$STATIC" = 1 ]; then
  :
elif [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
  echo "(running the tools needs a Mac with Apple silicon; skipped)"
else
  echo "Running on macOS $(sw_vers -productVersion)"
  if [ -n "${GUFI_TEST_MODEL:-}" ]; then
    MODEL="$GUFI_TEST_MODEL"
  else
    MODEL="$CACHE_DIR/minilm384-$MODEL_COMMIT.gguf"
    if [ ! -f "$MODEL" ] || [ "$(sha256_of "$MODEL")" != "$MODEL_SHA256" ]; then
      mkdir -p "$CACHE_DIR"
      curl -fsSL -o "$MODEL.part" \
        "https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/$MODEL_COMMIT/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf"
      mv "$MODEL.part" "$MODEL"
    fi
    [ "$(sha256_of "$MODEL")" = "$MODEL_SHA256" ] || die "$MODEL has SHA-256 $(sha256_of "$MODEL"), expected $MODEL_SHA256"
  fi
  case "$MODEL" in *"'"*) die "the model's path cannot contain a single quote" ;; esac
  for f in "${NATIVE[@]}"; do
    if [ -f "$f" ] && [ ! -L "$f" ] && file -b "$f" | grep -q Mach-O; then
      codesign --verify "$f" 2>/dev/null || bad "codesign rejects $f"
    fi
  done

  # The environment launchd gives an agent, plus the tree's tools on PATH
  # (gufi_vt runs gufi_query by name), and nothing from this shell.
  clean() { env -i HOME="$T/home" PATH="$G/bin:/usr/bin:/bin:/usr/sbin:/sbin" "$@"; }
  mkdir -p "$T/home" "$T/corpus/notes"
  printf 'the index is a tree of per-directory databases\n' >"$T/corpus/notes/design.md"
  printf 'a recipe for lemon cake\n' >"$T/corpus/cake.txt"
  # gufi_sqlite3 runs each line of its input as one statement, and takes no
  # dot commands.
  cat >"$T/vt.sql" <<SQL
CREATE VIRTUAL TABLE temp.v USING gufi_vt(index='$T/idx/corpus', threads=1, E="SELECT name FROM vrpentries;");
SELECT name FROM temp.v;
SQL
  cat >"$T/embed.sql" <<SQL
INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$MODEL');
SELECT length(lembed('m', 'hello'));
SQL
  # The Studio's pattern: load the model, embed the question with lembed,
  # store document vectors in a vec0 table, and take the nearest.
  cat >"$T/search.sql" <<SQL
INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$MODEL');
CREATE VIRTUAL TABLE v USING vec0(id INTEGER PRIMARY KEY, fp384 float[384]);
INSERT INTO v(id, fp384) VALUES (1, lembed('m', 'a tree of per-directory databases holding file metadata')), (2, lembed('m', 'a recipe for lemon cake'));
CREATE TABLE q AS SELECT lembed('m', 'how is the file index laid out') AS e;
SELECT id FROM v WHERE fp384 MATCH (SELECT e FROM q) ORDER BY distance LIMIT 2;
SQL

  # Through /bin/sh, which drops DYLD_* variables, as gufi_vt and scripts start the tools.
  # shellcheck disable=SC2016  # $0 and $1 are expanded by that shell
  check "gufi_dir2index indexes a folder" clean /bin/sh -c 'gufi_dir2index -x "$0" "$1"' "$T/corpus" "$T/idx"
  # shellcheck disable=SC2016
  check "gufi_query reads the index" clean /bin/sh -c \
    'gufi_query -d " " -E "SELECT name FROM vrpentries;" "$0" | grep cake.txt' "$T/idx/corpus"
  # shellcheck disable=SC2016
  check "gufi_vt runs gufi_query through the shell" clean /bin/sh -c 'gufi_sqlite3 <"$0" | grep cake.txt' "$T/vt.sql"
  # shellcheck disable=SC2016
  check "sqlite-lembed embeds with the model (384 floats)" clean /bin/sh -c \
    'gufi_sqlite3 <"$0" | grep -x 1536' "$T/embed.sql"
  # shellcheck disable=SC2016
  check "sqlite-vec puts the related text first" clean /bin/sh -c \
    'gufi_sqlite3 <"$0" | head -n 1 | grep -x 1' "$T/search.sql"
  loaded="$(env -i HOME="$T/home" DYLD_PRINT_LIBRARIES=1 "$G/bin/gufi_sqlite3" <"$T/search.sql" 2>&1 >/dev/null |
    sed -n 's/^dyld\[[0-9]*\]: //p' || true)"
  outside="$(printf '%s\n' "$loaded" | grep -v -e "^$G/" -e '^/usr/lib/' -e '^/System/' | grep . || true)"
  if [ -z "$loaded" ]; then
    bad "dyld listed no libraries for gufi_sqlite3"
  elif [ -n "$outside" ]; then
    bad "gufi_sqlite3 loads libraries from outside the tree:"
    printf '%s\n' "$outside" | sed 's/^/        /'
  else
    ok "gufi_sqlite3 loads only the tree's libraries and the system's ($(printf '%s\n' "$loaded" | grep -c "^$G/") from the tree)"
  fi
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "All checks passed"
