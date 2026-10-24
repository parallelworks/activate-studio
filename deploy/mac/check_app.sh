#!/usr/bin/env bash
# Check a built "ACTIVATE Studio.app".
#
# On any host: the bundle's layout and Info.plist, the scripts' syntax
# (and shellcheck where installed), that every native file is an arm64
# Mach-O with an intact signature whose libraries resolve inside the app or
# the system, that nothing built for Linux slipped in, and the size.
#
# On a Mac, --run also runs what was bundled: Node, Python, the app's
# executable, and GUFI indexing and querying a small folder, embedding with
# the model, tesseract reading text in pictures and a scanned PDF, the
# indexer scripts, and the server itself from the app's own run loop on a
# spare port, all with no DYLD_* variables set. --launch then
# exercises the launcher and its LaunchAgent with a temporary home
# directory, and the launcher without one, as on a managed Mac where
# ~/Library/LaunchAgents is read-only. On a Mac older than the app's LSMinimumSystemVersion they are
# skipped. Signed by sign_app.sh, all of it runs under the hardened runtime.
#
# --dmg FILE checks the disk image made of the app (make_dmg.sh): the app
# and the install note, and no link to /Applications. With --launch it also
# installs from the mounted image with launcher.sh --install, as the app's
# window does when opened there, and runs the installed copy.
#
# Usage: deploy/mac/check_app.sh "path/to/ACTIVATE Studio.app" [--run] [--launch] [--dmg FILE]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP="$(cd "${1:?usage: check_app.sh APP [--run] [--launch] [--dmg FILE]}" && pwd -P)"
shift
RUN=0 LAUNCH=0 DMG=
while [ $# -gt 0 ]; do
  case "$1" in
    --run) RUN=1 ;;
    --launch) RUN=1; LAUNCH=1 ;;
    --dmg) DMG="$(cd "$(dirname "${2:?--dmg needs a file}")" && pwd -P)/$(basename "$2")"; shift ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
C="$APP/Contents"
R="$C/Resources"
G="$R/gufi"
O="$R/ocr"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

echo "Bundle"
check "Info.plist parses, names the bundle, its executable and its copyright" python3 - "$C/Info.plist" <<'PY'
import os, plistlib, sys
info = plistlib.load(open(sys.argv[1], 'rb'))
assert info['CFBundleIdentifier'] == 'com.parallelworks.activate-studio'
assert info['CFBundlePackageType'] == 'APPL'
exe = os.path.join(os.path.dirname(sys.argv[1]), 'MacOS', info['CFBundleExecutable'])
assert os.access(exe, os.X_OK), exe
for k in ('CFBundleShortVersionString', 'CFBundleVersion', 'LSMinimumSystemVersion'):
    assert info[k], k
assert 'Parallel Works' in info['NSHumanReadableCopyright'], 'NSHumanReadableCopyright'
PY
for f in \
  "MacOS/ACTIVATE Studio" Resources/launcher.sh Resources/studio-run.sh Resources/update-signing.pub \
  Resources/node/bin/node Resources/python/bin/python3 \
  Resources/models/minilm384.gguf Resources/models/minilm-l6/model.onnx Resources/models/minilm-l6/tokenizer.json \
  Resources/app/build-info.json Resources/app/package.json \
  Resources/app/server/dist/main.js Resources/app/server/node_modules/fastify/package.json \
  Resources/app/web/dist/index.html Resources/app/indexer/enrich.py Resources/app/indexer/embed.py \
  Resources/app/indexer/embed_onnx.py Resources/app/indexer/embed_query.py Resources/app/indexer/semantic_map.py \
  Resources/app/indexer/reindex.sh Resources/app/docs/HELP.md Resources/app/CHANGELOG.md Resources/app/deploy/gufi-index.yaml \
  Resources/gufi/bin/gufi_query Resources/gufi/bin/gufi_dir2index Resources/gufi/bin/gufi_sqlite3 \
  Resources/gufi/lib/gufi_vt.dylib Resources/gufi/lib/vec0.dylib Resources/gufi/lib/lembed0.dylib \
  Resources/gufi/lib/libomp.dylib Resources/gufi/lib/libpcre2-8.0.dylib Resources/gufi/licenses/GUFI-LICENSE.txt; do
  check "$f" test -s "$C/$f"
done
if [ -d "$O" ]; then
  for f in Resources/ocr/bin/tesseract Resources/ocr/lib/libleptonica.6.dylib \
    Resources/ocr/share/tessdata/eng.traineddata Resources/ocr/share/tessdata/configs/tsv \
    Resources/ocr/licenses/tesseract-LICENSE.txt Resources/ocr/licenses/leptonica-LICENSE.txt; do
    check "$f" test -s "$C/$f"
  done
elif [ "$(uname -s)" = Darwin ]; then
  bad "no OCR in the app (Resources/ocr), which build_app.sh adds on a Mac"
else
  echo "  note  no OCR in the app (Resources/ocr): built off a Mac without OCR_TARBALL"
fi
check "update-signing.pub is an Ed25519 public key" python3 - "$R/update-signing.pub" <<'PY'
import base64, sys
pem = open(sys.argv[1]).read().strip().splitlines()
assert pem[0] == '-----BEGIN PUBLIC KEY-----' and pem[-1] == '-----END PUBLIC KEY-----'
der = base64.b64decode(''.join(pem[1:-1]))
assert len(der) == 44 and der[:12] == bytes.fromhex('302a300506032b6570032100')
PY
check "the app has its icon (an .icns with a 1024-pixel image)" python3 - "$C/Info.plist" <<'PY'
import os, plistlib, struct, sys
info = plistlib.load(open(sys.argv[1], 'rb'))
path = os.path.join(os.path.dirname(sys.argv[1]), 'Resources', info['CFBundleIconFile'] + '.icns')
data = open(path, 'rb').read()
assert data[:4] == b'icns' and struct.unpack('>I', data[4:8])[0] == len(data)
types, i = set(), 8
while i < len(data):
    t, n = data[i:i + 4], struct.unpack('>I', data[i + 4:i + 8])[0]
    types.add(t)
    i += n
assert b'ic10' in types, types
PY
check "no link in the bundle leads nowhere" sh -c 'find "$1" -type l | while IFS= read -r l; do [ -e "$l" ] || exit 1; done' sh "$APP"
if command -v codesign >/dev/null 2>&1; then
  # The bundle's seal over every file in it: ad hoc from build_app.sh, or
  # with a certificate from sign_app.sh.
  if out="$(codesign --verify --deep --strict "$APP" 2>&1)"; then
    ok "the bundle's signature seals the app (codesign --verify --deep --strict)"
  else
    bad "the bundle's signature does not verify:"; printf '%s\n' "$out" | head -n 20 | sed 's/^/        /'
  fi
fi
check "the model is the full file (over 10 MB)" test "$(( $(wc -c <"$R/models/minilm384.gguf") ))" -gt 10000000
check "the ONNX model is the pinned file" sh -c "python3 -c 'import hashlib, json, sys; d = json.load(open(sys.argv[1])); sys.exit(0 if hashlib.sha256(open(sys.argv[2], \"rb\").read()).hexdigest() == d[\"model\"][\"files\"][\"model.onnx\"][\"sha256\"] else 1)' '$R/app/indexer/embed_deps.json' '$R/models/minilm-l6/model.onnx'"
check "onnxruntime, tokenizers and numpy are in the app's Python" sh -c "ls -d '$R'/python/lib/python3.*/site-packages/onnxruntime/__init__.py '$R'/python/lib/python3.*/site-packages/tokenizers/__init__.py '$R'/python/lib/python3.*/site-packages/numpy/__init__.py"
check "pypdf is in the app's Python, for PDFs downmark cannot read" sh -c "ls -d '$R'/python/lib/python3.*/site-packages/pypdf/__init__.py '$R'/python/lib/python3.*/site-packages/pypdf-*.dist-info/licenses"
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
NATIVE=("$R/node/bin/node" "$R"/python/bin/python3.* "$R"/python/lib/*.dylib "$R"/python/lib/python3.*/lib-dynload/*.so "$G"/bin/* "$G"/lib/*.dylib
  "$O"/bin/* "$O"/lib/*.dylib)
# The Python packages' native libraries (onnxruntime, numpy and others), read
# NUL-separated since the app's path has a space in it.
while IFS= read -r -d '' f; do NATIVE+=("$f"); done < <(
  find "$R"/python/lib/python3.*/site-packages -type f \( -name '*.so' -o -name '*.dylib' \) -print0)
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
# Signed by sign_app.sh, with the hardened runtime: every Mach-O file, as
# file(1) finds them, is signed the same way, as notarization requires and
# library validation assumes.
if command -v codesign >/dev/null 2>&1 && codesign -dv "$APP" 2>&1 | grep -q '^CodeDirectory .*flags=0x[0-9a-f]*([^)]*runtime'; then
  team="$(codesign -dvv "$APP" 2>&1 | sed -n 's/^TeamIdentifier=//p')"
  unlike="$(find "$APP" -type f -print0 | xargs -0 file -N -F $'\t' | awk -F '\t' '$2 ~ /Mach-O/ { print $1 }' |
    while IFS= read -r f; do
      i="$(codesign -dvv "$f" 2>&1 || true)"
      if ! grep -q '^CodeDirectory .*flags=0x[0-9a-f]*([^)]*runtime' <<<"$i" || [ "$(sed -n 's/^TeamIdentifier=//p' <<<"$i")" != "$team" ]; then
        printf '%s\n' "${f#"$APP"/}"
      fi
    done)"
  if [ -z "$unlike" ]; then
    ok "every Mach-O file has the hardened runtime and the bundle's team ($team)"
  else
    bad "Mach-O files not signed like the bundle:"; printf '%s\n' "$unlike" | sed 's/^/        /'
  fi
fi

echo "Size"
echo "  $(du -sh "$APP" | cut -f1)  total"
for d in node python gufi ocr app/server/node_modules app/web models; do
  [ -e "$R/$d" ] || continue
  echo "  $(du -sh "$R/$d" | cut -f1)  $d"
done

if [ "$RUN" = 1 ]; then
  if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
    echo "--run needs a Mac with Apple silicon; skipped"
  elif ! python3 -c 'import sys; v = lambda s: [int(x) for x in s.split(".")]; sys.exit(0 if v(sys.argv[1]) <= v(sys.argv[2]) else 1)' \
    "$MIN" "$(sw_vers -productVersion)"; then
    echo "--run: the app needs macOS $MIN and this Mac has $(sw_vers -productVersion); skipped"
  else
    T="$(mktemp -d)"
    trap 'rm -rf "$T"' EXIT
    # The environment launchd gives an agent, with nothing from this shell,
    # and no Python bytecode caches, as studio-run.sh runs the server.
    clean() { env -i HOME="$T/home" PATH=/usr/bin:/bin:/usr/sbin:/sbin PYTHONDONTWRITEBYTECODE=1 "$@"; }
    mkdir -p "$T/home" "$T/corpus/notes"
    printf 'the index is a tree of per-directory databases\n' >"$T/corpus/notes/design.md"
    printf 'a recipe for lemon cake\n' >"$T/corpus/cake.txt"
    # A picture with text and a scanned PDF (one page that is only an
    # image), for OCR with the app's tesseract, found as studio-run.sh
    # finds it: first on the PATH, with TESSDATA_PREFIX naming its data.
    OCR_RUN=0
    OCR_ENV=(PATH="$O/bin:/usr/bin:/bin:/usr/sbin:/sbin" TESSDATA_PREFIX="$O/share/tessdata")
    if [ -d "$O" ] && [ -f "$HERE/testdata/ocr-sample.png" ]; then
      OCR_RUN=1
      mkdir -p "$T/corpus/scans"
      cp "$HERE/testdata/ocr-sample.png" "$HERE/testdata/ocr-sample.pdf" "$T/corpus/scans/"
    fi

    echo "Running"
    check "node runs" clean "$R/node/bin/node" -e 'process.exit(process.versions.node.startsWith("26.") ? 0 : 1)'
    check "python runs, with sqlite3 and pypdf" clean "$R/python/bin/python3" -c 'import sqlite3, json, zipfile, urllib.request, pypdf'
    check "the .xls and .ppt readers (xlrd, olefile) import in the app's Python" clean "$R/python/bin/python3" -B -c 'import xlrd, olefile'
    check "and with onnxruntime, tokenizers and numpy" clean "$R/python/bin/python3" -c 'import onnxruntime, tokenizers, numpy'
    # The window, started only far enough to hand --help to the launcher.
    check "the app's executable starts" sh -c "env -i HOME='$T/home' PATH=/usr/bin:/bin '$C/MacOS/ACTIVATE Studio' --help | grep -q -- --uninstall"
    if command -v codesign >/dev/null 2>&1; then
      check "codesign accepts every GUFI file" sh -c "for f in '$G'/bin/* '$G'/lib/*.dylib; do file -b \"\$f\" | grep -q Mach-O || continue; codesign --verify \"\$f\" || exit 1; done"
    fi
    # Through /bin/sh, which drops DYLD_* variables, as gufi_vt and scripts start the tools.
    check "gufi_dir2index indexes a folder" clean /bin/sh -c "'$G/bin/gufi_dir2index' -x '$T/corpus' '$T/idx'"
    check "gufi_query reads the index" clean /bin/sh -c "'$G/bin/gufi_query' -d ' ' -E 'SELECT name FROM vrpentries;' '$T/idx/corpus' | grep -q cake.txt"
    check "gufi_sqlite3 embeds with the bundled model" clean /bin/sh -c "printf '%s\n' \"INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$R/models/minilm384.gguf');\" \"SELECT length(lembed('m', 'hello'));\" | '$G/bin/gufi_sqlite3' | grep -q 1536"
    # dyld's lines are "dyld[pid]: /path", or on macOS 15 "dyld[pid]: <uuid>
    # /path" with "delayed: <name>" lines for libraries it defers.
    loaded="$(DYLD_PRINT_LIBRARIES=1 "$G/bin/gufi_sqlite3" :memory: 'SELECT 1;' 2>&1 >/dev/null |
      sed -n 's/^dyld\[[0-9]*\]: \(<[^>]*> \)\{0,1\}\(\/.*\)/\2/p' || true)"
    outside="$(printf '%s\n' "$loaded" | grep -v -e "^$G/" -e '^/usr/lib/' -e '^/System/' | grep . || true)"
    if [ -z "$loaded" ]; then
      bad "dyld listed no libraries for gufi_sqlite3"
    elif [ -n "$outside" ]; then
      bad "gufi_sqlite3 loads libraries from outside the app:"; printf '%s\n' "$outside" | sed 's/^/        /'
    else
      ok "gufi_sqlite3 loads only the app's libraries and the system's"
    fi
    check "the indexer extracts text" clean env "${OCR_ENV[@]}" "$R/python/bin/python3" "$R/app/indexer/enrich.py" \
      --kb-root "$T/corpus" --index "$T/idx/corpus" --extract-cache "$T/extract"
    if [ "$OCR_RUN" = 1 ]; then
      check "and reads the text in a picture with the app's tesseract" grep -qi heliotrope "$T/extract/scans/ocr-sample.png.txt"
    fi
    # The output goes to a file first: grep -q on the pipe would stop
    # embed.py at its first line, before it writes anything.
    check "the indexer embeds with the ONNX model" clean /bin/sh -c "'$R/python/bin/python3' '$R/app/indexer/embed.py' --index '$T/idx/corpus' --model '$R/models/minilm384.gguf' --gufi-sqlite3 '$G/bin/gufi_sqlite3' >'$T/embed-onnx.out' && grep -q 'model: minilm-l6-mean-int8 (onnx)' '$T/embed-onnx.out' && grep -q '0 errors' '$T/embed-onnx.out'"
    check "and names it in each folder's index" clean "$R/python/bin/python3" -c "import sqlite3, sys; sys.exit(0 if sqlite3.connect('$T/idx/corpus/db.db').execute('SELECT model FROM gvecmodel').fetchone()[0] == 'minilm-l6-mean-int8' else 1)"
    check "the semantic map is made from those vectors with the app's numpy" clean /bin/sh -c "'$R/python/bin/python3' '$R/app/indexer/semantic_map.py' --index '$T/idx/corpus' --out '$T/semantic-map' >'$T/map.out' && '$R/python/bin/python3' -c 'import json, sys; d = json.loads(open(sys.argv[1]).read().strip().splitlines()[-1]); sys.exit(0 if \"available\" in d else 1)' '$T/map.out'"
    check "the older model still embeds through gufi_sqlite3" clean /bin/sh -c "'$R/python/bin/python3' '$R/app/indexer/embed.py' --index '$T/idx/corpus' --backend lembed --model '$R/models/minilm384.gguf' --gufi-sqlite3 '$G/bin/gufi_sqlite3' >'$T/embed-lembed.out' && grep -q 'model: minilm384 (lembed)' '$T/embed-lembed.out' && grep -q '0 errors' '$T/embed-lembed.out'"
    check "the query embedder answers with 384 values" clean /bin/sh -c "printf '%s\n' '{\"id\": 1, \"texts\": [\"lemon dessert\"]}' | '$R/python/bin/python3' '$R/app/indexer/embed_query.py' --onnx '$R/models/minilm-l6' | '$R/python/bin/python3' -c 'import base64, json, sys; l = [json.loads(x) for x in sys.stdin]; sys.exit(0 if l[0][\"ready\"] and len(base64.b64decode(l[1][\"vectors\"][0])) == 1536 else 1)'"
    # A PDF out of the box: downmark's binary first, the bundled pypdf for one
    # it leaves (no poppler on a Mac).
    if [ -f "$HERE/../../testdata/corpus/docs/report.pdf" ]; then
      mkdir -p "$T/pdfs/docs" && cp "$HERE/../../testdata/corpus/docs/report.pdf" "$T/pdfs/docs/"
      check "downmark's binary reads a PDF" clean /bin/sh -c 'b="$(find "$1" -path "*downmark-darwin-arm64/bin/downmark" -type f | head -1)"; "$b" "$2" | grep -q BOUNDARY-LAYER-4417' sh "$R/app/server/node_modules" "$T/pdfs/docs/report.pdf"
      check "the bundled pypdf reads it too" clean "$R/python/bin/python3" -c "import sys; from pypdf import PdfReader; sys.exit(0 if 'BOUNDARY-LAYER-4417' in ''.join(p.extract_text() or '' for p in PdfReader('$T/pdfs/docs/report.pdf').pages) else 1)"
    fi
    # OCR: the app's tesseract on each image format leptonica reads, then on
    # a scanned PDF, whose page downmark hands it as a PNG (no poppler).
    if [ "$OCR_RUN" = 1 ]; then
      check "tesseract runs, with the English model" clean env "${OCR_ENV[@]}" /bin/sh -c 'tesseract --list-langs 2>&1 | grep -qx eng'
      for img in "$HERE"/testdata/ocr-sample.png "$HERE"/testdata/ocr-sample.jpg "$HERE"/testdata/ocr-sample.tif \
        "$HERE"/testdata/ocr-sample.webp "$HERE"/testdata/ocr-sample.gif; do
        check "tesseract reads the text in $(basename "$img")" \
          clean env "${OCR_ENV[@]}" /bin/sh -c 'tesseract "$1" stdout 2>/dev/null | grep -qi heliotrope' sh "$img"
      done
      loaded="$(DYLD_PRINT_LIBRARIES=1 "$O/bin/tesseract" --version 2>&1 >/dev/null |
        sed -n 's/^dyld\[[0-9]*\]: \(<[^>]*> \)\{0,1\}\(\/.*\)/\2/p' || true)"
      outside="$(printf '%s\n' "$loaded" | grep -v -e "^$O/" -e '^/usr/lib/' -e '^/System/' | grep . || true)"
      if [ -z "$loaded" ]; then
        bad "dyld listed no libraries for tesseract"
      elif [ -n "$outside" ]; then
        bad "tesseract loads libraries from outside the app:"; printf '%s\n' "$outside" | sed 's/^/        /'
      else
        ok "tesseract loads only the app's library and the system's"
      fi
      check "downmark reads a scanned PDF through the app's tesseract" clean env "${OCR_ENV[@]}" /bin/sh -c \
        'b="$(find "$1" -path "*downmark-darwin-arm64/bin/downmark" -type f | head -1)"; "$b" -ocr tesseract -ocr-policy thin "$2" | grep -qi heliotrope' \
        sh "$R/app/server/node_modules" "$HERE/testdata/ocr-sample.pdf"
    fi

    # A network that inspects HTTPS with its own certificate authority: a
    # throwaway authority trusted by macOS (in the System keychain, where an
    # administrator or the Mac's management puts one) and a local HTTPS
    # server with a certificate from it. Node on its own does not trust it;
    # the app's server, which adds the system's trusted certificates at
    # start (netTrust.ts), does, and its update check below goes there.
    # Needs sudo without a password, as on a CI runner; skipped otherwise.
    TLS_RUN=0 TLS_SHA1= tls_pid=
    # A keychain command run as root, given SECS seconds: one that waits for
    # an authorization prompt no one can answer is killed instead of holding
    # the job. Its status, or 124 when it was killed.
    as_root_bounded() {
      local secs="$1" p n=0; shift
      # The command writes its own process id (the shell execs it), so a
      # stop reaches it rather than only sudo.
      rm -f "$T/tls/root.pid"
      sudo -n /bin/sh -c 'echo $$ >"$1"; shift; exec "$@"' sh "$T/tls/root.pid" "$@" >"$T/tls/last.log" 2>&1 &
      p=$!
      while kill -0 "$p" 2>/dev/null; do
        if [ "$n" -ge $((secs * 4)) ]; then
          [ -s "$T/tls/root.pid" ] && sudo -n kill -KILL "$(cat "$T/tls/root.pid")" 2>/dev/null
          wait "$p" 2>/dev/null
          return 124
        fi
        sleep 0.25; n=$((n + 1))
      done
      wait "$p"
    }
    # Deleting the certificate from the System keychain takes the authority
    # away; removing its trust setting (remove-trusted-cert -d) waits for an
    # authorization prompt on a runner, and a setting for a certificate that
    # is gone trusts nothing.
    tls_cleanup() {
      [ -n "$tls_pid" ] && kill "$tls_pid" 2>/dev/null
      tls_pid=
      if [ -n "$TLS_SHA1" ]; then
        as_root_bounded 30 security delete-certificate -Z "$TLS_SHA1" /Library/Keychains/System.keychain || true
        TLS_SHA1=
      fi
    }
    trap 'tls_cleanup; rm -rf "$T"' EXIT
    echo "Outbound HTTPS through a certificate authority macOS trusts"
    if ! sudo -n true 2>/dev/null; then
      echo "  (sudo asks for a password here, so no authority can be added to the System keychain; skipped)"
    else
      mkdir -p "$T/tls"
      printf '[req]\ndistinguished_name = dn\n[dn]\n[v3_ca]\nbasicConstraints = critical,CA:TRUE\nkeyUsage = critical,keyCertSign,cRLSign\nsubjectKeyIdentifier = hash\n' >"$T/tls/ca.cnf"
      printf 'subjectAltName = DNS:localhost,IP:127.0.0.1\nbasicConstraints = CA:FALSE\nextendedKeyUsage = serverAuth\n' >"$T/tls/server.ext"
      if (cd "$T/tls" &&
        openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.pem -days 2 -subj '/CN=Studio Check Authority' -config ca.cnf -extensions v3_ca &&
        openssl req -newkey rsa:2048 -nodes -keyout server.key -out server.csr -subj /CN=localhost -config ca.cnf &&
        openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -out server.pem -days 2 -extfile server.ext) >/dev/null 2>&1; then
        TLS_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
        # Answers every request with an empty list of releases.
        clean "$R/node/bin/node" -e "require('https').createServer({ key: require('fs').readFileSync(process.argv[1]), cert: require('fs').readFileSync(process.argv[2]) }, (q, r) => { r.setHeader('content-type', 'application/json'); r.end('[]') }).listen(Number(process.argv[3]), '127.0.0.1')" \
          "$T/tls/server.key" "$T/tls/server.pem" "$TLS_PORT" &
        tls_pid=$!
        for _ in $(seq 1 20); do curl -ks -m 1 "https://127.0.0.1:$TLS_PORT/" >/dev/null 2>&1 && break; sleep 0.25; done
        FETCH="fetch('https://localhost:$TLS_PORT/').then(r => process.exit(r.ok ? 0 : 1), e => { console.error(e.cause?.code ?? e.message); process.exit(1) })"
        check "Node on its own does not trust a certificate from that authority" sh -c "! env -i HOME='$T/home' PATH=/usr/bin:/bin '$R/node/bin/node' -e \"\$1\"" _ "$FETCH"
        TLS_SHA1="$(openssl x509 -noout -fingerprint -sha1 -in "$T/tls/ca.pem" | sed 's/.*=//; s/://g')"
        if as_root_bounded 60 security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain "$T/tls/ca.pem"; then
          TLS_RUN=1
          check "once macOS trusts it, the app's Node with the system's certificates added does" \
            env -i HOME="$T/home" PATH=/usr/bin:/bin "$R/node/bin/node" --input-type=module -e \
            "import { trustSystemCertificates } from '$R/app/server/dist/netTrust.js'; trustSystemCertificates(); $FETCH"
        else
          # Not added (refused, or held for a prompt and stopped): nothing to take out again.
          echo "  (security add-trusted-cert did not add it here, so the check is skipped: $(tr '\n' ' ' <"$T/tls/last.log" | cut -c1-160))"
          as_root_bounded 30 security delete-certificate -Z "$TLS_SHA1" /Library/Keychains/System.keychain || true
          TLS_SHA1=
        fi
      else
        bad "openssl could not make a throwaway certificate authority"
      fi
    fi

    # The server from the app's own run loop, as the LaunchAgent runs it.
    PORT_FREE="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
    mkdir -p "$T/data"
    printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT_FREE" "$T/corpus" >"$T/data/studio.env"
    # Its update check asks the HTTPS server above, behind the authority
    # only macOS trusts.
    [ "$TLS_RUN" = 1 ] && printf "STUDIO_UPDATE_URL='https://localhost:%s'\n" "$TLS_PORT" >>"$T/data/studio.env"
    # Started directly, not through clean(), so $! is the run loop itself
    # rather than a subshell that TERM would end without reaching it.
    env -i HOME="$T/home" PATH=/usr/bin:/bin:/usr/sbin:/sbin /bin/bash "$R/studio-run.sh" "$T/data" &
    loop=$!
    up=0
    for _ in $(seq 1 90); do
      if curl -fsS -m 2 "http://127.0.0.1:$PORT_FREE/api/version" >"$T/version.json" 2>/dev/null; then up=1; break; fi
      sleep 1
    done
    if [ "$up" = 1 ]; then ok "the server answers on 127.0.0.1:$PORT_FREE"; else bad "the server did not start:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
    check "it reports this build" grep -q "$(sed -n 's/.*"builtAt": *"\([^"]*\)".*/\1/p' "$R/app/build-info.json")" "$T/version.json"
    check "its log says it trusts the system's certificates" grep -q "trusts the system's certificates" "$T/data/logs/studio.log"
    if [ "$TLS_RUN" = 1 ]; then
      curl -fsS -m 30 -X POST "http://127.0.0.1:$PORT_FREE/api/updates/check" >"$T/updcheck.json" 2>/dev/null || true
      if python3 -c 'import json, sys; d = json.load(open(sys.argv[1])); sys.exit(0 if d.get("state") == "idle" and not d.get("error") and not d.get("offline") and d.get("checkedAt") else 1)' "$T/updcheck.json" 2>/dev/null; then
        ok "and its update check reaches a server behind that authority"
      else
        bad "its update check did not reach the server behind that authority:"
        sed 's/^/        /' "$T/updcheck.json" 2>/dev/null || true
        grep -i -E "outbound|update check|certificate" "$T/data/logs/studio.log" | tail -n 5 | sed 's/^/        /'
      fi
    fi
    check "the model is linked into its index" test -e "$T/data/index/models/minilm384.gguf"
    check "and the ONNX model too" test -e "$T/data/index/models/minilm-l6/model.onnx"
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
    # By meaning: no word in common, so only the vectors can find it, with
    # the query embedded by the worker the server starts.
    meaning=0
    for _ in $(seq 1 60); do
      if curl -fsS -m 30 "http://127.0.0.1:$PORT_FREE/api/search?q=which%20dessert%20uses%20citrus%20fruit" 2>/dev/null |
        python3 -c 'import json, sys; d = json.load(sys.stdin); h = d.get("results") or d.get("hits") or []; sys.exit(0 if any(x.get("name") == "cake.txt" and x.get("mode") == "vector" for x in h) else 1)'; then meaning=1; break; fi
      sleep 1
    done
    if [ "$meaning" = 1 ]; then ok "and search by meaning finds it with the ONNX model"; else bad "search by meaning did not find it:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
    # OCR from the run loop's PATH and TESSDATA_PREFIX: the readers report
    # names the app's tesseract, and search finds the picture and the
    # scanned PDF by words only OCR could read.
    if [ "$OCR_RUN" = 1 ]; then
      check "the server reports the app's tesseract" sh -c "curl -fsS -m 30 'http://127.0.0.1:$PORT_FREE/api/index/extractors' |
        python3 -c 'import json, sys; t = [e for e in json.load(sys.stdin)[\"extractors\"] if e[\"name\"] == \"tesseract\"]; sys.exit(0 if t and t[0][\"available\"] and t[0][\"note\"].startswith(\"Bundled\") else 1)'"
      ocr_found=0
      for _ in $(seq 1 120); do
        if curl -fsS -m 10 "http://127.0.0.1:$PORT_FREE/api/search?q=heliotrope" 2>/dev/null |
          python3 -c 'import json, sys; d = json.load(sys.stdin); n = {x.get("name") for x in d.get("results") or d.get("hits") or []}; sys.exit(0 if {"ocr-sample.png", "ocr-sample.pdf"} <= n else 1)'; then ocr_found=1; break; fi
        sleep 1
      done
      if [ "$ocr_found" = 1 ]; then ok "and search finds a picture and a scanned PDF by the text OCR read"; else bad "search did not find both OCR samples:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
    fi
    # A query across two libraries goes through gufi_vt, with node and the
    # wrapper inside "ACTIVATE Studio.app" and a second index whose path has
    # a space and a single quote, all quoted for gufi_vt.
    second="$T/second idx's"
    if clean /bin/sh -c "'$G/bin/gufi_dir2index' -x '$T/corpus' \"\$1\"" sh "$second" >/dev/null 2>&1 &&
      curl -fsS -m 30 -X POST -H 'content-type: application/json' \
        -d "$(python3 -c 'import json, sys; print(json.dumps({"id": "second", "label": "Second", "indexRoot": sys.argv[1]}))' "$second/corpus")" \
        "http://127.0.0.1:$PORT_FREE/api/libraries" >/dev/null; then
      curl -fsS -m 60 -X POST -H 'content-type: application/json' -d '{"library": "all", "canned": "largest", "n": 20}' \
        "http://127.0.0.1:$PORT_FREE/api/query" >"$T/query.json" 2>"$T/query.err" || true
      if python3 -c 'import json, sys; d = json.load(open(sys.argv[1])); libs = set(d.get("rowLibraries") or []); sys.exit(0 if d.get("engine") == "gufi_vt" and {"kb", "second"} <= libs and not d.get("failed") else 1)' "$T/query.json" 2>/dev/null; then
        ok "a query across two libraries runs through gufi_vt"
      else
        bad "a query across two libraries did not run through gufi_vt:"
        python3 -c 'import json, sys; d = json.load(open(sys.argv[1])); print("engine", d.get("engine"), "libraries", sorted(set(d.get("rowLibraries") or [])), "failed", d.get("failed"))' "$T/query.json" 2>&1 | sed 's/^/        /'
        sed 's/^/        /' "$T/query.err" || true
        { grep -i -E 'gufi_vt|fell back' "$T/data/logs/studio.log" || true; } | tail -n 5 | sed 's/^/        /'
        # The server's own probe by hand, and the steps before it.
        {
          clean "$G/bin/gufi_sqlite3" :memory: 'SELECT 1;'; echo "select 1: exit $?"
          clean "$G/bin/gufi_sqlite3" :memory: "SELECT group_concat(name) FROM pragma_module_list WHERE name LIKE 'gufi%';"; echo "gufi modules: exit $?"
          clean env PATH="$G/bin:/usr/bin:/bin" "$G/bin/gufi_sqlite3" :memory: \
            "CREATE VIRTUAL TABLE temp.l USING gufi_vt(index='$T/idx/corpus', E=\"SELECT name FROM vrpentries;\"); SELECT count(*) FROM temp.l;"; echo "local location: exit $?"
          clean "$G/bin/gufi_sqlite3" :memory: \
            "CREATE VIRTUAL TABLE temp.p USING gufi_vt(index='/', E=\"SELECT name FROM vrpentries;\", remote_cmd='/usr/bin/true'); SELECT count(*) FROM temp.p;"; echo "remote probe: exit $?"
        } >"$T/vtdiag.txt" 2>&1 || true
        sed 's/^/        /' "$T/vtdiag.txt"
      fi
    else
      bad "could not add a second library to query across"
    fi
    kill -TERM "$loop" 2>/dev/null || true
    # The loop waits for the server's own graceful stop, which launchd
    # allows 30 seconds (ExitTimeOut).
    stopped=0
    for _ in $(seq 1 70); do if ! kill -0 "$loop" 2>/dev/null; then stopped=1; break; fi; sleep 0.5; done
    if [ "$stopped" = 1 ] && ! curl -fsS -m 2 "http://127.0.0.1:$PORT_FREE/api/version" >/dev/null 2>&1; then
      ok "TERM stops the run loop and the server"
    else
      bad "the run loop or server kept running after TERM"
      # So the launcher checks below do not reach a leftover server.
      for p in $(lsof -ti "tcp:$PORT_FREE" -sTCP:LISTEN 2>/dev/null) $loop; do kill -KILL "$p" 2>/dev/null || true; done
    fi
    # The throwaway authority leaves the System keychain as soon as it is done with.
    if [ "$TLS_RUN" = 1 ]; then
      tls_cleanup
      check "the throwaway authority is out of the System keychain again" sh -c \
        "! security find-certificate -c 'Studio Check Authority' /Library/Keychains/System.keychain"
    fi

    if [ "$LAUNCH" = 1 ]; then
      echo "Launcher"
      L="$R/launcher.sh"
      H="$T/home"
      LABEL=com.parallelworks.activate-studio
      # A port of its own, so nothing left from the checks above answers.
      PORT_FREE="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
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
      # The uninstall plan the app's Uninstall dialog shows, read with the
      # app's own Python: a library index in a chosen folder is listed, the
      # documents are kept, and a dry run removes nothing.
      LIBIDX="$T/chosen-index"
      mkdir -p "$LIBIDX/tree" "$H/Library/Application Support/ACTIVATE Studio/index"
      printf '{"index":"%s"}\n' "$LIBIDX/tree" >"$LIBIDX/.studio-index.json"
      printf '[{"id":"lib","indexRoot":"%s","build":{"indexDir":"%s","managed":false}}]\n' "$LIBIDX/tree" "$LIBIDX" \
        >"$H/Library/Application Support/ACTIVATE Studio/index/libraries.json"
      PLAN="$(env HOME="$H" "$L" --uninstall --dry-run --purge 2>&1 || true)"
      check "the uninstall plan lists the app, the data and a library index" sh -c "printf '%s\n' \"\$1\" | grep -q '^app	' && printf '%s\n' \"\$1\" | grep -q '^data	' && printf '%s\n' \"\$1\" | grep -q '^library-index	$LIBIDX\$'" _ "$PLAN"
      check "and keeps the documents" sh -c "printf '%s\n' \"\$1\" | grep -q '^keep-documents	$T/corpus\$'" _ "$PLAN"
      check "a dry run removes nothing" test -f "$LIBIDX/.studio-index.json"

      # A managed Mac where ~/Library/LaunchAgents is read-only: the app
      # starts the server itself (a run loop in a session of its own, named
      # in server.pid), with no administrator rights.
      echo "Launcher, with ~/Library/LaunchAgents read-only"
      H2="$T/home-locked"
      D2="$H2/Library/Application Support/ACTIVATE Studio"
      PORT2="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
      PORT3="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
      mkdir -p "$H2/Library/LaunchAgents" "$D2"
      chmod 555 "$H2/Library/LaunchAgents"
      printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT2" "$T/corpus" >"$D2/studio.env"
      locked() { env HOME="$H2" STUDIO_NO_BROWSER=1 "$L" "$@"; }
      loop_pid() { head -n 1 "$D2/server.pid" 2>/dev/null || true; }
      check "opening the app starts the server without a LaunchAgent" locked
      check "no LaunchAgent was written" test ! -e "$H2/Library/LaunchAgents/$LABEL.plist"
      P2="$(loop_pid)"
      check "the server answers after the launcher has exited" curl -fsS -m 5 "http://127.0.0.1:$PORT2/api/version"
      own_group() { [ -n "$P2" ] && [ "$(ps -o pgid= -p "$P2" | tr -d ' ')" = "$P2" ]; }
      check "its run loop has a process group of its own" own_group
      same_loop() { locked >/dev/null && [ "$(loop_pid)" = "$P2" ]; }
      check "opening it again uses the same server" same_loop
      # The server's own restart (here for a new port, as Settings, Server
      # does) comes back from the same run loop.
      restarted() {
        curl -fsS -m 10 -X POST -H 'content-type: application/json' -d "{\"port\": $PORT3}" \
          "http://127.0.0.1:$PORT2/api/setup/port" | grep -q '"restarting":true' || return 1
        for _ in $(seq 1 90); do curl -fsS -m 2 "http://127.0.0.1:$PORT3/api/version" >/dev/null 2>&1 && break; sleep 1; done
        curl -fsS -m 2 "http://127.0.0.1:$PORT3/api/version" >/dev/null && [ "$(loop_pid)" = "$P2" ] && kill -0 "$P2"
      }
      check "a restart the server asks for brings it back, from the same run loop" restarted
      check "--stop stops it" locked --stop
      check "and the server is gone" sh -c "! curl -fsS -m 2 http://127.0.0.1:$PORT3/api/version"
      check "and its run loop" sh -c "! kill -0 '$P2' 2>/dev/null"
      check "opening the app again starts it again" locked
      PLAN2="$(locked --uninstall --dry-run 2>&1 || true)"
      check "the uninstall plan lists that server" sh -c 'printf "%s\n" "$1" | grep -qxF "server	$2/server.pid"' _ "$PLAN2" "$D2"
      check "and no LaunchAgent" sh -c '! printf "%s\n" "$1" | grep -q LaunchAgents' _ "$PLAN2"
      locked --stop >/dev/null 2>&1 || true
      chmod 755 "$H2/Library/LaunchAgents"
    fi

    # The disk image: the app and the install note, no /Applications link;
    # mounted where macOS mounts a downloaded image (/Volumes), so the app on
    # it is the copy the window offers to install.
    if [ -n "$DMG" ]; then
      echo "Disk image"
      M=
      if [ ! -f "$DMG" ]; then
        bad "no disk image at $DMG"
      elif plist="$(hdiutil attach -nobrowse -readonly -noautoopen -plist "$DMG" 2>/dev/null)"; then
        M="$(printf '%s' "$plist" | python3 -c 'import plistlib, sys; print(next(e["mount-point"] for e in plistlib.loads(sys.stdin.buffer.read())["system-entities"] if e.get("mount-point")))' 2>/dev/null || true)"
      fi
      if [ -z "$M" ]; then
        [ -f "$DMG" ] && bad "the disk image did not mount"
      else
        NAME="$(basename "$APP" .app)"
        IMG_APP="$M/$NAME.app"
        check "it holds the app" test -f "$IMG_APP/Contents/Info.plist"
        check "and the install note" grep -q "choose Install" "$M/Installing $NAME.txt"
        check "and no link to /Applications" sh -c '! [ -e "$1/Applications" ] && ! [ -L "$1/Applications" ]' _ "$M"
        check "it is mounted under /Volumes, where the app offers to install itself" sh -c 'case "$1" in /Volumes/*) exit 0 ;; esac; exit 1' _ "$M"
        if [ "$LAUNCH" = 1 ]; then
          # A home of its own, as a person who has never installed the app.
          H3="$T/home-install"
          D3="$H3/Library/Application Support/ACTIVATE Studio"
          COPY="$H3/Applications/$NAME.app"
          PORT4="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
          mkdir -p "$D3"
          printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT4" "$T/corpus" >"$D3/studio.env"
          fresh() { env HOME="$H3" PATH=/usr/bin:/bin:/usr/sbin:/sbin "$@"; }
          check "opened from the image in a terminal, it says to install it and starts nothing" \
            sh -c '! env HOME="$1" PATH=/usr/bin:/bin:/usr/sbin:/sbin STUDIO_NO_BROWSER=1 "$2" 2>"$3" && grep -q -- --install "$3" && ! curl -fsS -m 2 "http://127.0.0.1:$4/api/version"' \
            _ "$H3" "$IMG_APP/Contents/Resources/launcher.sh" "$T/not-installed.txt" "$PORT4"
          check "--install puts it in ~/Applications, which it creates" fresh "$IMG_APP/Contents/Resources/launcher.sh" --install
          check "the installed copy's signature verifies (codesign --verify --deep --strict)" codesign --verify --deep --strict "$COPY"
          check "and it carries no quarantine mark" sh -c '! xattr -lr "$1" 2>/dev/null | grep -q com.apple.quarantine' _ "$COPY"
          check "the installed copy starts the server" fresh env STUDIO_NO_BROWSER=1 "$COPY/Contents/Resources/launcher.sh"
          check "and it answers" curl -fsS -m 5 "http://127.0.0.1:$PORT4/api/version"
          # Installing again over the running copy: its server is stopped,
          # the copy replaced whole (a file added to the old one is gone),
          # and the new copy starts it again.
          touch "$COPY/Contents/old-copy-marker"
          check "--install again replaces the installed copy" fresh "$IMG_APP/Contents/Resources/launcher.sh" --install
          check "and stopped the server running from it" sh -c 'for _ in $(seq 1 30); do curl -fsS -m 2 "http://127.0.0.1:$1/api/version" >/dev/null 2>&1 || exit 0; sleep 1; done; exit 1' _ "$PORT4"
          check "with the old copy gone, and no staging copies left" sh -c '! [ -e "$1/Contents/old-copy-marker" ] && ! ls -a "$2" | grep -q "^\.$3\.app\."' _ "$COPY" "$H3/Applications" "$NAME"
          check "the replaced copy's signature verifies" codesign --verify --deep --strict "$COPY"
          check "opening it starts the server again" fresh env STUDIO_NO_BROWSER=1 "$COPY/Contents/Resources/launcher.sh"
          check "--stop stops it" fresh "$COPY/Contents/Resources/launcher.sh" --stop
          launchctl enable "gui/$(id -u)/com.parallelworks.activate-studio" 2>/dev/null || true
          # A copy macOS marked as downloaded (quarantined): the installed
          # copy has the mark removed, so macOS does not translocate it.
          QSRC="$T/quarantined/$NAME.app"
          mkdir -p "$T/quarantined"
          ditto "$IMG_APP" "$QSRC"
          xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");check_app;" "$QSRC"
          xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");check_app;" "$QSRC/Contents/MacOS/$NAME"
          check "a quarantined copy installs without the mark" sh -c 'env HOME="$1" PATH=/usr/bin:/bin:/usr/sbin:/sbin "$2" --install "$3" >/dev/null && ! xattr -lr "$3/$4.app" 2>/dev/null | grep -q com.apple.quarantine && codesign --verify --deep --strict "$3/$4.app"' \
            _ "$H3" "$QSRC/Contents/Resources/launcher.sh" "$T/elsewhere" "$NAME"
        fi
        hdiutil detach "$M" >/dev/null 2>&1 || hdiutil detach -force "$M" >/dev/null 2>&1 || true
      fi
    fi

    # Nothing written into the app while it ran: a file added or changed
    # there breaks the signature's seal.
    if command -v codesign >/dev/null 2>&1; then
      if out="$(codesign --verify --deep --strict --verbose=4 "$APP" 2>&1)"; then
        ok "running it left the app as it was signed"
      else
        bad "running it changed the app:"; printf '%s\n' "$out" | head -n 20 | sed 's/^/        /'
      fi
    fi
  fi
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "All checks passed"
