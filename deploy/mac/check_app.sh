#!/usr/bin/env bash
# Check a built "ACTIVATE Studio.app".
#
# On any host: the bundle's layout and Info.plist, the scripts' syntax
# (and shellcheck where installed), that every native file is an arm64
# Mach-O with an intact signature whose libraries resolve inside the app or
# the system, that nothing built for Linux slipped in, and the size.
#
# On a Mac, --run also runs what was bundled: Node, Python, and GUFI
# indexing and querying a small folder, embedding with the model, the
# indexer scripts, and the server itself from the app's own run loop on a
# spare port, all with no DYLD_* variables set. --launch then exercises the
# launcher and its LaunchAgent with a temporary home directory.
#
# Usage: deploy/mac/check_app.sh "path/to/ACTIVATE Studio.app" [--run] [--launch]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP="$(cd "${1:?usage: check_app.sh APP [--run] [--launch]}" && pwd -P)"
shift
RUN=0 LAUNCH=0
for a in "$@"; do
  case "$a" in --run) RUN=1 ;; --launch) RUN=1; LAUNCH=1 ;; *) echo "unknown option $a" >&2; exit 2 ;; esac
done
C="$APP/Contents"
R="$C/Resources"
G="$R/gufi"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

echo "Bundle"
check "Info.plist parses, names the bundle and its executable" python3 - "$C/Info.plist" <<'PY'
import os, plistlib, sys
info = plistlib.load(open(sys.argv[1], 'rb'))
assert info['CFBundleIdentifier'] == 'com.parallelworks.activate-studio'
assert info['CFBundlePackageType'] == 'APPL'
exe = os.path.join(os.path.dirname(sys.argv[1]), 'MacOS', info['CFBundleExecutable'])
assert os.access(exe, os.X_OK), exe
for k in ('CFBundleShortVersionString', 'CFBundleVersion', 'LSMinimumSystemVersion'):
    assert info[k], k
PY
for f in \
  "MacOS/ACTIVATE Studio" Resources/launcher.sh Resources/studio-run.sh Resources/update-signing.pub \
  Resources/node/bin/node Resources/python/bin/python3 \
  Resources/models/minilm384.gguf Resources/app/build-info.json Resources/app/package.json \
  Resources/app/server/dist/main.js Resources/app/server/node_modules/fastify/package.json \
  Resources/app/web/dist/index.html Resources/app/indexer/enrich.py Resources/app/indexer/embed.py \
  Resources/app/indexer/reindex.sh Resources/app/docs/HELP.md Resources/app/deploy/gufi-index.yaml \
  Resources/gufi/bin/gufi_query Resources/gufi/bin/gufi_dir2index Resources/gufi/bin/gufi_sqlite3 \
  Resources/gufi/lib/gufi_vt.dylib Resources/gufi/lib/vec0.dylib Resources/gufi/lib/lembed0.dylib \
  Resources/gufi/lib/libomp.dylib Resources/gufi/lib/libpcre2-8.0.dylib Resources/gufi/licenses/GUFI-LICENSE.txt; do
  check "$f" test -s "$C/$f"
done
check "update-signing.pub is an Ed25519 public key" python3 - "$R/update-signing.pub" <<'PY'
import base64, sys
pem = open(sys.argv[1]).read().strip().splitlines()
assert pem[0] == '-----BEGIN PUBLIC KEY-----' and pem[-1] == '-----END PUBLIC KEY-----'
der = base64.b64decode(''.join(pem[1:-1]))
assert len(der) == 44 and der[:12] == bytes.fromhex('302a300506032b6570032100')
PY
check "the model is the full file (over 10 MB)" test "$(( $(wc -c <"$R/models/minilm384.gguf") ))" -gt 10000000
check "the document converter's macOS arm64 binary is there" \
  sh -c "find '$R/app/server/node_modules' -path '*downmark-darwin-arm64/bin/downmark' -type f | grep -q ."
check "and no Linux build of it" sh -c "! find '$R/app/server/node_modules' -name 'downmark-linux-*' | grep -q ."

echo "Scripts"
# The executable is the launcher script, or the native window when built on a Mac.
scripts=("$R/launcher.sh" "$R/studio-run.sh" "$HERE/install.sh")
if head -c 2 "$C/MacOS/ACTIVATE Studio" | grep -q '#!'; then scripts+=("$C/MacOS/ACTIVATE Studio"); else
  check "the executable is the native window, for arm64" sh -c "file -b '$C/MacOS/ACTIVATE Studio' | grep -q 'Mach-O.*arm64'"
fi
for s in "${scripts[@]}"; do
  check "bash -n $(basename "$s")" bash -n "$s"
  if command -v shellcheck >/dev/null 2>&1; then
    check "shellcheck $(basename "$s")" shellcheck -s bash "$s"
  fi
done
command -v shellcheck >/dev/null 2>&1 || echo "  (shellcheck is not installed; skipped)"

echo "Native code"
if command -v file >/dev/null 2>&1; then
  check "node is a Mach-O arm64 executable" sh -c "file -b '$R/node/bin/node' | grep -q 'Mach-O 64-bit.*arm64'"
fi
MIN="$(python3 -c 'import plistlib,sys; print(plistlib.load(open(sys.argv[1],"rb"))["LSMinimumSystemVersion"])' "$C/Info.plist")"
NATIVE=("$R/node/bin/node" "$R"/python/bin/python3.* "$R"/python/lib/*.dylib "$R"/python/lib/python3.*/lib-dynload/*.so "$G"/bin/* "$G"/lib/*.dylib)
if out="$(python3 "$HERE/macho_check.py" --max-minos "$MIN" "${NATIVE[@]}" 2>&1)"; then
  ok "arm64, signed, runs on macOS $MIN, libraries resolve without DYLD_LIBRARY_PATH"
else
  bad "Mach-O check:"
  printf '%s\n' "$out" | sed 's/^/        /'
fi
elf="$(python3 - "$APP" <<'PY'
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
if [ -z "$elf" ]; then ok "no Linux (ELF) binaries"; else bad "Linux binaries in the app:"; printf '        %s\n' "$elf"; fi

echo "Size"
echo "  $(du -sh "$APP" | cut -f1)  total"
for d in node python gufi app/server/node_modules app/web models; do
  echo "  $(du -sh "$R/$d" | cut -f1)  $d"
done

if [ "$RUN" = 1 ]; then
  if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
    echo "--run needs a Mac with Apple silicon; skipped"
  else
    T="$(mktemp -d)"
    trap 'rm -rf "$T"' EXIT
    # The environment launchd gives an agent, with nothing from this shell.
    clean() { env -i HOME="$T/home" PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"; }
    mkdir -p "$T/home" "$T/corpus/notes"
    printf 'the index is a tree of per-directory databases\n' >"$T/corpus/notes/design.md"
    printf 'a recipe for lemon cake\n' >"$T/corpus/cake.txt"

    echo "Running"
    check "node runs" clean "$R/node/bin/node" -e 'process.exit(process.versions.node.startsWith("26.") ? 0 : 1)'
    check "python runs, with sqlite3" clean "$R/python/bin/python3" -c 'import sqlite3, json, zipfile, urllib.request'
    if command -v codesign >/dev/null 2>&1; then
      check "codesign accepts every GUFI file" sh -c "for f in '$G'/bin/* '$G'/lib/*.dylib; do file -b \"\$f\" | grep -q Mach-O || continue; codesign --verify \"\$f\" || exit 1; done"
    fi
    # Through /bin/sh, which drops DYLD_* variables, as gufi_vt and scripts start the tools.
    check "gufi_dir2index indexes a folder" clean /bin/sh -c "'$G/bin/gufi_dir2index' -x '$T/corpus' '$T/idx'"
    check "gufi_query reads the index" clean /bin/sh -c "'$G/bin/gufi_query' -d ' ' -E 'SELECT name FROM vrpentries;' '$T/idx/corpus' | grep -q cake.txt"
    check "gufi_sqlite3 embeds with the bundled model" clean /bin/sh -c "printf '%s\n' \"INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$R/models/minilm384.gguf');\" \"SELECT length(lembed('m', 'hello'));\" | '$G/bin/gufi_sqlite3' | grep -q 1536"
    loaded="$(DYLD_PRINT_LIBRARIES=1 "$G/bin/gufi_sqlite3" :memory: 'SELECT 1;' 2>&1 >/dev/null || true)"
    if printf '%s\n' "$loaded" | grep -E '/opt/homebrew|/usr/local|/Users/runner' >/dev/null; then
      bad "gufi_sqlite3 loads libraries from outside the app:"; printf '%s\n' "$loaded" | grep -E '/opt/homebrew|/usr/local|/Users/runner' | sed 's/^/        /'
    else
      ok "gufi_sqlite3 loads only the app's libraries and the system's"
    fi
    check "the indexer extracts text" clean "$R/python/bin/python3" "$R/app/indexer/enrich.py" \
      --kb-root "$T/corpus" --index "$T/idx/corpus" --extract-cache "$T/extract"
    check "the indexer embeds through gufi_sqlite3" clean "$R/python/bin/python3" "$R/app/indexer/embed.py" \
      --index "$T/idx/corpus" --model "$R/models/minilm384.gguf" --gufi-sqlite3 "$G/bin/gufi_sqlite3"

    # The server from the app's own run loop, as the LaunchAgent runs it.
    PORT_FREE="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
    mkdir -p "$T/data"
    printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT_FREE" "$T/corpus" >"$T/data/studio.env"
    clean /bin/bash "$R/studio-run.sh" "$T/data" &
    loop=$!
    up=0
    for _ in $(seq 1 90); do
      if curl -fsS -m 2 "http://127.0.0.1:$PORT_FREE/api/version" >"$T/version.json" 2>/dev/null; then up=1; break; fi
      sleep 1
    done
    if [ "$up" = 1 ]; then ok "the server answers on 127.0.0.1:$PORT_FREE"; else bad "the server did not start:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
    check "it reports this build" grep -q "$(sed -n 's/.*"builtAt": *"\([^"]*\)".*/\1/p' "$R/app/build-info.json")" "$T/version.json"
    check "the model is linked into its index" test -e "$T/data/index/models/minilm384.gguf"
    # A full rebuild runs indexer/reindex.sh (bash, then Python) with the
    # GUFI the server found; search then reads it through gufi_query.
    check "the server starts a full index" curl -fsS -m 10 -X POST -H 'content-type: application/json' -d '{}' \
      "http://127.0.0.1:$PORT_FREE/api/reindex"
    found=0
    for _ in $(seq 1 120); do
      if curl -fsS -m 10 "http://127.0.0.1:$PORT_FREE/api/search?q=lemon" 2>/dev/null | grep -q cake.txt; then found=1; break; fi
      sleep 1
    done
    if [ "$found" = 1 ]; then ok "and search finds a file by its text"; else bad "search found nothing after the index:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
    kill -TERM "$loop" 2>/dev/null || true
    stopped=0
    for _ in $(seq 1 40); do if ! kill -0 "$loop" 2>/dev/null; then stopped=1; break; fi; sleep 0.5; done
    if [ "$stopped" = 1 ] && ! curl -fsS -m 2 "http://127.0.0.1:$PORT_FREE/api/version" >/dev/null 2>&1; then
      ok "TERM stops the run loop and the server"
    else
      bad "the run loop or server kept running after TERM"
    fi

    if [ "$LAUNCH" = 1 ]; then
      echo "Launcher"
      L="$R/launcher.sh"
      H="$T/home"
      LABEL=com.parallelworks.activate-studio
      mkdir -p "$H/Library/Application Support/ACTIVATE Studio"
      printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT_FREE" "$T/corpus" >"$H/Library/Application Support/ACTIVATE Studio/studio.env"
      check "opening the app starts the agent and the server" env HOME="$H" STUDIO_NO_BROWSER=1 "$L"
      check "the LaunchAgent is loaded" launchctl print "gui/$(id -u)/$LABEL"
      check "its plist parses" plutil -lint "$H/Library/LaunchAgents/$LABEL.plist"
      check "opening it again finds it running" env HOME="$H" STUDIO_NO_BROWSER=1 "$L"
      check "--stop stops it" env HOME="$H" "$L" --stop
      sleep 2
      check "and the server is gone" sh -c "! curl -fsS -m 2 http://127.0.0.1:$PORT_FREE/api/version"
      launchctl enable "gui/$(id -u)/$LABEL" 2>/dev/null || true
    fi
  fi
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "All checks passed"
