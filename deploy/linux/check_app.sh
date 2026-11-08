#!/usr/bin/env bash
# Check an unpacked Linux payload (the activate-studio/ tree build_app.sh
# makes).
#
# --static (on any Linux host): the tree's layout, the scripts' syntax (and
# the shellcheck tool, where installed), that every ELF file is x86-64 and needs no
# glibc, libstdc++ or libgcc symbol newer than the oldest target has, that
# every library each file needs is beside it or one every desktop has, that
# nothing built for macOS slipped in, and the size.
#
# --run (as a plain user, on the distribution under test): runs what was
# bundled with a clean environment and nothing but the base system: Node,
# Python and its packages, GUFI indexing and querying a small folder,
# embedding with the model, tesseract reading text in pictures, the document
# converter on a PDF, a scanned PDF and an Office document, the indexer
# scripts, and the server itself from the payload's run loop
# (studio-run.sh) on a spare port: a full index, search by words, by
# meaning, by the text OCR read and by a converted document's text, a Query
# across two libraries through gufi_vt, and the update check against a
# local stand-in for the release server. Nothing here needs curl or the
# system's Python: the payload's own is used.
#
# Usage: deploy/linux/check_app.sh PAYLOAD_DIR [--static] [--run]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
APP="$(cd "${1:?usage: check_app.sh PAYLOAD_DIR [--static] [--run]}" && pwd -P)"
shift
STATIC=0 RUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --static) STATIC=1 ;;
    --run) RUN=1 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
[ "$STATIC$RUN" != 00 ] || STATIC=1
G="$APP/gufi"
O="$APP/ocr"
PY="$APP/python/bin/python3"
# Which server the tree runs (build_app.sh, STUDIO_SERVER): from its
# manifest, node/PLACEHOLDER marking the Go server alone.
SERVER="${STUDIO_SERVER:-}"
if [ -z "$SERVER" ]; then
  SERVER=node
  grep -q '"server": *"go"' "$APP/manifest.json" 2>/dev/null && SERVER=go
  [ -e "$APP/node/PLACEHOLDER" ] && SERVER=go-only
fi
# The checks' own Python: the payload's, or, in a tree without one, the
# system's, run isolated.
[ -x "$PY" ] || PY="$(command -v python3)"
MAX_GLIBC="${MAX_GLIBC:-2.28}"
MAX_GLIBCXX="${MAX_GLIBCXX:-3.4.25}"
MAX_CXXABI="${MAX_CXXABI:-1.3.11}"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }

if [ "$STATIC" = 1 ]; then
  echo "Layout"
  LAYOUT="studio-run.sh studio-recover.sh update-signing.pub manifest.json LICENSE NOTICE share/activate-studio.png
    node/bin/node models/minilm-l6/model.onnx models/minilm-l6/tokenizer.json
    app/build-info.json app/package.json app/server/dist/main.js
    app/web/dist/index.html app/docs/HELP.md app/CHANGELOG.md app/deploy/gufi-index.yaml
    gufi/bin/gufi_query gufi/bin/gufi_dir2index gufi/bin/gufi_sqlite3 gufi/lib/gufi_vt.so gufi/lib/vec0.so
    gufi/lib/libgomp.so.1 gufi/lib/libpcre2-8.so.0 gufi/licenses/GUFI-LICENSE.txt gufi/manifest.json
    ocr/bin/tesseract ocr/lib/libleptonica.so.6 ocr/share/tessdata/eng.traineddata ocr/share/tessdata/configs/tsv
    ocr/licenses/tesseract-LICENSE.txt ocr/licenses/leptonica-LICENSE.txt ocr/manifest.json"
  [ "$SERVER" = go-only ] || LAYOUT="$LAYOUT node/lib/libatomic.so.1 node/LICENSE python/bin/python3 models/minilm384.gguf
    app/server/node_modules/fastify/package.json app/indexer/enrich.py app/indexer/embed.py app/indexer/embed_onnx.py
    app/indexer/embed_query.py app/indexer/semantic_map.py app/indexer/reindex.sh gufi/lib/lembed0.so"
  [ "$SERVER" = node ] || LAYOUT="$LAYOUT app/bin/studio-server app/bin/semantic-map app/bin/studio-extract
    onnxruntime/lib/libonnxruntime.so.1 onnxruntime/licenses/onnxruntime-LICENSE.txt"
  [ "$SERVER" != go-only ] || LAYOUT="$LAYOUT node/PLACEHOLDER"
  for f in $LAYOUT; do
    check "$f" test -s "$APP/$f"
  done
  if [ "$SERVER" != node ]; then
    check "the manifest says the Go server runs" grep -q '"server": *"go"' "$APP/manifest.json"
    check "studio-server is a static program" sh -c "! readelf -d '$APP/app/bin/studio-server' 2>/dev/null | grep -q NEEDED"
  fi
  if [ "$SERVER" = go-only ]; then
    check "node/bin/node is the stand-in, executable, as older updaters check" sh -c "[ -x '$APP/node/bin/node' ] && head -c 2 '$APP/node/bin/node' | grep -q '#!'"
    check "no Node, Python or node_modules" sh -c "[ ! -d '$APP/python' ] && [ ! -d '$APP/app/server/node_modules' ] && ! head -c 4 '$APP/node/bin/node' | grep -q ELF"
  fi
  check "update-signing.pub is an Ed25519 public key" "$PY" -I - "$APP/update-signing.pub" <<'PY'
import base64, sys
pem = open(sys.argv[1]).read().strip().splitlines()
assert pem[0] == '-----BEGIN PUBLIC KEY-----' and pem[-1] == '-----END PUBLIC KEY-----'
der = base64.b64decode(''.join(pem[1:-1]))
assert len(der) == 44 and der[:12] == bytes.fromhex('302a300506032b6570032100')
PY
  check "the icon is a 1024-pixel PNG" "$PY" -I -c 'import struct, sys; d = open(sys.argv[1], "rb").read(24); sys.exit(0 if d[:8] == b"\x89PNG\r\n\x1a\n" and struct.unpack(">II", d[16:24]) == (1024, 1024) else 1)' "$APP/share/activate-studio.png"
  check "no link in the tree leads nowhere" sh -c 'find "$1" -type l | while IFS= read -r l; do [ -e "$l" ] || exit 1; done' sh "$APP"
  check "the ONNX model is the pinned file" "$PY" -I -c 'import hashlib, json, sys; d = json.load(open(sys.argv[1])); sys.exit(0 if hashlib.sha256(open(sys.argv[2], "rb").read()).hexdigest() == d["model"]["files"]["model.onnx"]["sha256"] else 1)' "$ROOT/indexer/embed_deps.json" "$APP/models/minilm-l6/model.onnx"
  if [ "$SERVER" != go-only ]; then
    check "the model is the full file (over 10 MB)" test "$(( $(wc -c <"$APP/models/minilm384.gguf") ))" -gt 10000000
    check "onnxruntime, tokenizers and numpy are in the payload's Python" sh -c "ls -d '$APP'/python/lib/python3.*/site-packages/onnxruntime/__init__.py '$APP'/python/lib/python3.*/site-packages/tokenizers/__init__.py '$APP'/python/lib/python3.*/site-packages/numpy/__init__.py"
    check "pypdf is in the payload's Python, for PDFs downmark cannot read" sh -c "ls -d '$APP'/python/lib/python3.*/site-packages/pypdf/__init__.py"
    check "the document converter's Linux x64 binary is there" \
      sh -c "find '$APP/app/server/node_modules' -path '*downmark-linux-x64/bin/downmark' -type f | grep -q ."
    check "and no other platform's" sh -c "! find '$APP/app/server/node_modules' \\( -name 'downmark-darwin-*' -o -name 'downmark-win32-*' -o -name 'downmark-linux-arm64' \\) | grep -q ."
  fi

  echo "Scripts"
  for s in "$APP/studio-run.sh" "$APP/studio-recover.sh" "$HERE/build_app.sh" "$HERE/build_gufi.sh" "$HERE/build_ocr.sh" "$HERE/check_app.sh"; do
    check "bash -n $(basename "$s")" bash -n "$s"
    if command -v shellcheck >/dev/null 2>&1; then
      check "shellcheck $(basename "$s")" shellcheck -x -s bash -S warning "$s"
    fi
  done
  command -v shellcheck >/dev/null 2>&1 || echo "  (shellcheck is not installed; skipped)"

  echo "Native code"
  macho="$("$PY" -I - "$APP" <<'PY'
import os, sys
magics = {bytes.fromhex(x) for x in ('feedface', 'feedfacf', 'cefaedfe', 'cffaedfe', 'cafebabe')}
for d, _dirs, files in os.walk(sys.argv[1]):
    for f in files:
        p = os.path.join(d, f)
        if os.path.islink(p) or not os.path.isfile(p):
            continue
        with open(p, 'rb') as fh:
            head = fh.read(4)
        # cafebabe is also a Java class file's magic; only a native library or program counts.
        if head in magics and not p.endswith(('.class', '.jar')):
            print(p)
PY
)"
  if [ -z "$macho" ]; then ok "no macOS (Mach-O) binaries"; else bad "macOS binaries in the payload:"; printf '        %s\n' "$macho"; fi
  if out="$("$PY" -I "$HERE/elf_check.py" --root "$APP" --skip bin/activate-studio-wk --max-glibc "$MAX_GLIBC" --max-glibcxx "$MAX_GLIBCXX" --max-cxxabi "$MAX_CXXABI" "$APP" 2>&1)"; then
    ok "x86-64, libraries beside the files that load them, no symbol newer than glibc $MAX_GLIBC, libstdc++ $MAX_GLIBCXX"
    printf '%s\n' "$out" | sed 's/^/        /'
  else
    bad "ELF check:"
    printf '%s\n' "$out" | sed 's/^/        /'
  fi

  # The window shells and installer (desktop/linux), when the tree has them:
  # a shell links to the computer's own GTK and WebKitGTK, so only its CPU and
  # the newest glibc it needs are checked (a 4.1 shell is built on Ubuntu
  # 22.04, so glibc 2.35; the 4.0 shell on RHEL 8, glibc 2.28).
  if [ -d "$APP/bin" ]; then
    echo "Desktop"
    for f in bin/activate-studio bin/activate-studio-wk41 bin/activate-studio-wk40 install.sh uninstall.sh \
      share/applications/activate-studio.desktop share/icons/hicolor/48x48/apps/activate-studio.png \
      share/icons/hicolor/512x512/apps/activate-studio.png; do
      check "$f" test -s "$APP/$f"
    done
    for s in "$APP/bin/activate-studio" "$APP/install.sh" "$APP/uninstall.sh"; do check "bash -n $(basename "$s")" bash -n "$s"; done
    check "the 4.1 shell is x86-64 and needs no glibc newer than 2.35" "$PY" -I "$HERE/elf_check.py" --root "$APP" --no-libs --max-glibc 2.35 "$APP/bin/activate-studio-wk41"
    check "the 4.0 shell is x86-64 and needs no glibc newer than $MAX_GLIBC" "$PY" -I "$HERE/elf_check.py" --root "$APP" --no-libs --max-glibc "$MAX_GLIBC" "$APP/bin/activate-studio-wk40"
    check "the menu entry takes its command from the installer (@BIN@)" grep -q '^Exec=@BIN@' "$APP/share/applications/activate-studio.desktop"
  fi

  echo "Size"
  echo "  $(du -sh "$APP" | cut -f1)  total"
  for d in node python gufi ocr onnxruntime app/bin app/server/node_modules app/web models; do
    [ -e "$APP/$d" ] || continue
    echo "  $(du -sh "$APP/$d" | cut -f1)  $d"
  done
fi

if [ "$RUN" = 1 ]; then
  [ "$(uname -m)" = x86_64 ] || { echo "--run needs x86_64"; exit 2; }
  T="$(mktemp -d)"
  SERVER_LOOP='' FAKE_PID=''
  stop_all() {
    if [ -n "$SERVER_LOOP" ] && kill -0 "$SERVER_LOOP" 2>/dev/null; then kill -TERM "$SERVER_LOOP" 2>/dev/null || true; fi
    if [ -n "$FAKE_PID" ]; then kill "$FAKE_PID" 2>/dev/null || true; fi
    rm -rf "$T"
  }
  trap stop_all EXIT
  # A desktop session's environment, little of it: nothing from this shell,
  # and no Python bytecode caches, as studio-run.sh runs the server.
  clean() { env -i HOME="$T/home" PATH=/usr/bin:/bin PYTHONDONTWRITEBYTECODE=1 "$@"; }
  mkdir -p "$T/home" "$T/corpus/notes"
  printf 'the index is a tree of per-directory databases\n' >"$T/corpus/notes/design.md"
  printf 'a recipe for lemon cake\n' >"$T/corpus/cake.txt"
  # The repository's own corpus: an Office document and a PDF to convert, a
  # picture and a scanned PDF for OCR.
  for d in docs notes images scans; do mkdir -p "$T/corpus/$d"; done
  cp "$ROOT"/testdata/corpus/docs/handbook.docx "$ROOT"/testdata/corpus/docs/report.pdf "$T/corpus/docs/"
  cp "$HERE"/../mac/testdata/ocr-sample.png "$HERE"/../mac/testdata/ocr-sample.pdf "$T/corpus/scans/"
  OCR_ENV=(PATH="$O/bin:/usr/bin:/bin" TESSDATA_PREFIX="$O/share/tessdata")
  DOWNMARK=
  [ "$SERVER" = go-only ] || DOWNMARK="$(find "$APP/app/server/node_modules" -path '*downmark-linux-x64/bin/downmark' -type f | head -n 1)"

  echo "System"
  echo "  $(. /etc/os-release && echo "$PRETTY_NAME"), glibc $(ldd --version 2>&1 | awk 'NR == 1 {print $NF}'), $(id -un) (uid $(id -u))"
  # What the loader cannot find for any file in the tree: the packages the
  # base system lacks.
  # (Not the window shells, which need the desktop's GTK and WebKitGTK; the
  # install tests cover them.)
  unresolved="$(find "$APP" -type f \( -name '*.so' -o -name '*.so.*' -o -path '*/bin/*' \) -not -name 'activate-studio-wk4*' | while read -r f; do
    head -c 4 "$f" | grep -q ELF || continue
    ldd "$f" 2>/dev/null | grep 'not found' | sed "s|^|$(basename "$f"): |" || true
  done)"
  if [ -z "$unresolved" ]; then ok "the loader finds every library every file in the tree needs"; else bad "libraries the system lacks:"; printf '%s\n' "$unresolved" | sort -u | sed 's/^/        /'; fi

  echo "Running"
  if [ "$SERVER" != go-only ]; then
    check "node runs" clean "$APP/node/bin/node" -e 'process.exit(process.versions.node.startsWith("26.") ? 0 : 1)'
    check "python runs, with sqlite3 and pypdf" clean "$PY" -c 'import sqlite3, json, zipfile, urllib.request, pypdf'
    check "the .xls and .ppt readers (xlrd, olefile) import in the payload's Python" clean "$PY" -B -c 'import xlrd, olefile'
    check "and with onnxruntime, tokenizers and numpy" clean "$PY" -c 'import onnxruntime, tokenizers, numpy'
  fi
  if [ "$SERVER" != node ]; then
    check "studio-server runs" clean "$APP/app/bin/studio-server" routes
  fi
  # Through /bin/sh, as gufi_vt and scripts start the tools.
  check "gufi_dir2index indexes a folder" clean /bin/sh -c "'$G/bin/gufi_dir2index' -x '$T/corpus' '$T/idx'"
  check "gufi_query reads the index" clean /bin/sh -c "'$G/bin/gufi_query' -d ' ' -E 'SELECT name FROM vrpentries;' '$T/idx/corpus' | grep -q cake.txt"
  if [ "$SERVER" = go-only ]; then
    # No sqlite-lembed in the Go version: the vectors come from the Go
    # server's ONNX embedder, and GUFI stores and searches them (sqlite-vec).
    cat >"$T/search.sql" <<SQL
CREATE VIRTUAL TABLE v USING vec0(id INTEGER PRIMARY KEY, fp3 float[3]);
INSERT INTO v(id, fp3) VALUES (1, '[0.9, 0.1, 0.0]'), (2, '[0.0, 0.2, 0.98]');
SELECT id FROM v WHERE fp3 MATCH '[1, 0, 0]' ORDER BY distance LIMIT 2;
SQL
  else
    check "gufi_sqlite3 embeds with the bundled model" clean /bin/sh -c "printf '%s\n' \"INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$APP/models/minilm384.gguf');\" \"SELECT length(lembed('m', 'hello'));\" | '$G/bin/gufi_sqlite3' | grep -q 1536"
    # The Studio's pattern: load the model, embed the question with lembed,
    # store document vectors in a vec0 table, and take the nearest.
    cat >"$T/search.sql" <<SQL
INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$APP/models/minilm384.gguf');
CREATE VIRTUAL TABLE v USING vec0(id INTEGER PRIMARY KEY, fp384 float[384]);
INSERT INTO v(id, fp384) VALUES (1, lembed('m', 'a tree of per-directory databases holding file metadata')), (2, lembed('m', 'a recipe for lemon cake'));
CREATE TABLE q AS SELECT lembed('m', 'how is the file index laid out') AS e;
SELECT id FROM v WHERE fp384 MATCH (SELECT e FROM q) ORDER BY distance LIMIT 2;
SQL
  fi
  # shellcheck disable=SC2016  # $0 is expanded by that shell
  check "sqlite-vec puts the related text first" clean /bin/sh -c "'$G/bin/gufi_sqlite3' <\"\$0\" | head -n 1 | grep -x 1" "$T/search.sql"
  cat >"$T/vt.sql" <<SQL
CREATE VIRTUAL TABLE temp.v USING gufi_vt(index='$T/idx/corpus', threads=1, E="SELECT name FROM vrpentries;");
SELECT name FROM temp.v;
SQL
  # shellcheck disable=SC2016
  check "gufi_vt runs gufi_query through the shell" clean /bin/sh -c "PATH='$G/bin':\$PATH '$G/bin/gufi_sqlite3' <\"\$0\" | grep cake.txt" "$T/vt.sql"
  # The libraries gufi_sqlite3 loads, with the extensions it loads at run
  # time, from /proc after it has run a statement that uses them.
  loaded="$(clean /bin/sh -c "{ cat '$T/search.sql'; sleep 2; } | '$G/bin/gufi_sqlite3' >/dev/null 2>&1 & pid=\$!; sleep 1; awk '{ \$1 = \$2 = \$3 = \$4 = \$5 = \"\"; sub(/^ +/, \"\"); print }' /proc/\$pid/maps | grep '[.]so' | sort -u; wait \$pid" || true)"
  outside="$(printf '%s\n' "$loaded" | grep -v -e "^$G/" -e '^/usr/lib' -e '^/lib' | grep . || true)"
  if [ -z "$loaded" ]; then
    bad "could not list the libraries gufi_sqlite3 loaded"
  elif [ -n "$outside" ]; then
    bad "gufi_sqlite3 loads libraries from outside the payload:"; printf '%s\n' "$outside" | sed 's/^/        /'
  else
    ok "gufi_sqlite3 loads the payload's libraries and the system's only ($(printf '%s\n' "$loaded" | grep -c "^$G/") from the payload)"
  fi
  check "tesseract runs, with the English model" clean env "${OCR_ENV[@]}" /bin/sh -c 'tesseract --list-langs 2>&1 | grep -qx eng'
  for img in "$HERE"/../mac/testdata/ocr-sample.png "$HERE"/../mac/testdata/ocr-sample.jpg "$HERE"/../mac/testdata/ocr-sample.tif \
    "$HERE"/../mac/testdata/ocr-sample.webp "$HERE"/../mac/testdata/ocr-sample.gif; do
    check "tesseract reads the text in $(basename "$img")" \
      clean env "${OCR_ENV[@]}" /bin/sh -c 'tesseract "$1" stdout 2>/dev/null | grep -qi heliotrope' sh "$img"
  done
  if [ "$SERVER" != go-only ]; then
    check "downmark's binary reads a PDF" clean /bin/sh -c '"$1" "$2" | grep -q BOUNDARY-LAYER-4417' sh "$DOWNMARK" "$T/corpus/docs/report.pdf"
    check "and an Office document" clean /bin/sh -c '"$1" "$2" | grep -q VELOCITY-INLET-7734' sh "$DOWNMARK" "$T/corpus/docs/handbook.docx"
    check "and a scanned PDF through the payload's tesseract" clean env "${OCR_ENV[@]}" /bin/sh -c \
      '"$1" -ocr tesseract -ocr-policy thin "$2" | grep -qi heliotrope' sh "$DOWNMARK" "$HERE/../mac/testdata/ocr-sample.pdf"
    check "the bundled pypdf reads the PDF too" clean "$PY" -c "import sys; from pypdf import PdfReader; sys.exit(0 if 'BOUNDARY-LAYER-4417' in ''.join(p.extract_text() or '' for p in PdfReader('$T/corpus/docs/report.pdf').pages) else 1)"
    check "the indexer extracts text" clean env "${OCR_ENV[@]}" "$PY" "$APP/app/indexer/enrich.py" \
      --kb-root "$T/corpus" --index "$T/idx/corpus" --extract-cache "$T/extract"
    check "and reads the text in a picture with the payload's tesseract" grep -qi heliotrope "$T/extract/scans/ocr-sample.png.txt"
    check "the indexer embeds with the ONNX model" clean /bin/sh -c "'$PY' '$APP/app/indexer/embed.py' --index '$T/idx/corpus' --model '$APP/models/minilm384.gguf' --gufi-sqlite3 '$G/bin/gufi_sqlite3' >'$T/embed-onnx.out' && grep -q 'model: minilm-l6-mean-int8 (onnx)' '$T/embed-onnx.out' && grep -q '0 errors' '$T/embed-onnx.out'"
    check "the semantic map is made from those vectors with the payload's numpy" clean /bin/sh -c "'$PY' '$APP/app/indexer/semantic_map.py' --index '$T/idx/corpus' --out '$T/semantic-map' >'$T/map.out' && '$PY' -c 'import json, sys; d = json.loads(open(sys.argv[1]).read().strip().splitlines()[-1]); sys.exit(0 if \"available\" in d else 1)' '$T/map.out'"
    check "the older model still embeds through gufi_sqlite3" clean /bin/sh -c "'$PY' '$APP/app/indexer/embed.py' --index '$T/idx/corpus' --backend lembed --model '$APP/models/minilm384.gguf' --gufi-sqlite3 '$G/bin/gufi_sqlite3' >'$T/embed-lembed.out' && grep -q 'model: minilm384 (lembed)' '$T/embed-lembed.out' && grep -q '0 errors' '$T/embed-lembed.out'"
  fi

  # A stand-in for the release server the update check asks (GitHub's API):
  # one release, newer than any this build can be, with no Linux asset.
  free_port() { "$PY" -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
  FAKE_PORT="$(free_port)"
  mkdir -p "$T/fake"
  cat >"$T/fake/server.py" <<'PY'
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps([{
            'tag_name': 'v99.0', 'prerelease': False, 'draft': False, 'published_at': '2099-01-01T00:00:00Z',
            'body': '- A stand-in release for the update check.', 'html_url': 'https://example.invalid/releases/v99.0',
            'assets': [{'name': 'activate-studio-macos-arm64.tar.gz', 'size': 1, 'browser_download_url': 'https://example.invalid/mac.tar.gz'}],
        }]).encode()
        self.send_response(200)
        self.send_header('content-type', 'application/json')
        self.send_header('content-length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a):
        pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
  "$PY" -I "$T/fake/server.py" "$FAKE_PORT" &
  FAKE_PID=$!

  # The server from the payload's run loop, as a person's session runs it.
  PORT="$(free_port)"
  mkdir -p "$T/data"
  printf "PORT='%s'\nKB_ROOT='%s'\nSTUDIO_UPDATE_URL='http://127.0.0.1:%s'\n" "$PORT" "$T/corpus" "$FAKE_PORT" >"$T/data/studio.env"
  api() { "$PY" -I "$HERE/api.py" "$@"; }
  # jq() { "$PY" -I -c 'PROGRAM'; }: JSON from stdin, a Python expression on d.
  assert_json() { "$PY" -I -c 'import json, sys; d = json.load(sys.stdin); sys.exit(0 if eval(sys.argv[1]) else 1)' "$1"; }
  # Started directly, not through clean(), so $! is the run loop itself.
  env -i HOME="$T/home" PATH=/usr/bin:/bin /bin/bash "$APP/studio-run.sh" "$T/data" &
  SERVER_LOOP=$!
  up=0
  for _ in $(seq 1 90); do
    if api -m 2 GET "http://127.0.0.1:$PORT/api/version" >"$T/version.json" 2>/dev/null; then up=1; break; fi
    sleep 1
  done
  if [ "$up" = 1 ]; then ok "the server answers on 127.0.0.1:$PORT"; else bad "the server did not start:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'; fi
  check "it reports this build" grep -q "$(sed -n 's/.*"builtAt": *"\([^"]*\)".*/\1/p' "$APP/app/build-info.json")" "$T/version.json"
  [ "$SERVER" = go-only ] || check "the model is linked into its index" test -e "$T/data/index/models/minilm384.gguf"
  check "and the ONNX model too" test -e "$T/data/index/models/minilm-l6/model.onnx"
  check "the server starts a full index" api -m 10 POST "http://127.0.0.1:$PORT/api/reindex" '{}'
  poll() {   # poll SECONDS URL PYTHON-EXPRESSION-ON-d: until the answer satisfies it
    local secs="$1" url="$2" expr="$3" _
    for _ in $(seq 1 "$secs"); do
      if api -m 30 GET "$url" 2>/dev/null | assert_json "$expr" 2>/dev/null; then return 0; fi
      sleep 1
    done
    return 1
  }
  hits='(d.get("results") or d.get("hits") or [])'
  if poll 120 "http://127.0.0.1:$PORT/api/search?q=lemon" "any(x.get('name') == 'cake.txt' for x in $hits)"; then
    ok "search finds a file by its text"
  else
    bad "search found nothing after the index:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'
  fi
  if poll 60 "http://127.0.0.1:$PORT/api/search?q=which%20dessert%20uses%20citrus%20fruit" "any(x.get('name') == 'cake.txt' and x.get('mode') == 'vector' for x in $hits)"; then
    ok "search by meaning finds it with the ONNX model"
  else
    bad "search by meaning did not find it:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'
  fi
  check "the server reports the payload's tesseract" sh -c "'$PY' -I '$HERE/api.py' GET 'http://127.0.0.1:$PORT/api/index/extractors' |
    '$PY' -I -c 'import json, sys; t = [e for e in json.load(sys.stdin)[\"extractors\"] if e[\"name\"] == \"tesseract\"]; sys.exit(0 if t and t[0][\"available\"] and t[0][\"note\"].startswith(\"Bundled\") else 1)'"
  if poll 120 "http://127.0.0.1:$PORT/api/search?q=heliotrope" "{'ocr-sample.png', 'ocr-sample.pdf'} <= {x.get('name') for x in $hits}"; then
    ocr_found=1; ok "search finds a picture and a scanned PDF by the text OCR read"
  else
    ocr_found=0; bad "search did not find both OCR samples:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'
  fi
  if poll 60 "http://127.0.0.1:$PORT/api/search?q=VELOCITY-INLET-7734" "any(x.get('name') == 'handbook.docx' for x in $hits)"; then
    ok "search finds text converted from an Office document (downmark)"
  else
    bad "search did not find the converted .docx:"; tail -n 30 "$T/data/logs/studio.log" | sed 's/^/        /'
  fi
  # A query across two libraries goes through gufi_vt, with node and the
  # wrapper inside the payload and a second index whose path has a space
  # and a single quote.
  second="$T/second idx's"
  if clean /bin/sh -c "'$G/bin/gufi_dir2index' -x '$T/corpus' \"\$1\"" sh "$second" >/dev/null 2>&1 &&
    api -m 30 POST "http://127.0.0.1:$PORT/api/libraries" \
      "$("$PY" -I -c 'import json, sys; print(json.dumps({"id": "second", "label": "Second", "indexRoot": sys.argv[1]}))' "$second/corpus")" >/dev/null; then
    api -m 60 POST "http://127.0.0.1:$PORT/api/query" '{"library": "all", "canned": "largest", "n": 20}' >"$T/query.json" 2>"$T/query.err" || true
    if assert_json 'd.get("engine") == "gufi_vt" and {"kb", "second"} <= set(d.get("rowLibraries") or []) and not d.get("failed")' <"$T/query.json" 2>/dev/null; then
      ok "a query across two libraries runs through gufi_vt"
    else
      bad "a query across two libraries did not run through gufi_vt:"
      "$PY" -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print("engine", d.get("engine"), "libraries", sorted(set(d.get("rowLibraries") or [])), "failed", d.get("failed"))' "$T/query.json" 2>&1 | sed 's/^/        /'
      { grep -i -E 'gufi_vt|fell back' "$T/data/logs/studio.log" || true; } | tail -n 5 | sed 's/^/        /'
    fi
    if [ "$ocr_found" = 1 ]; then
      api -m 60 POST "http://127.0.0.1:$PORT/api/query" \
        '{"library": "all", "builder": {"source": "files", "filters": [{"field": "text", "op": "match", "value": "heliotrope"}], "groupBy": "none", "sort": "size", "dir": "desc", "limit": 20}}' \
        >"$T/qwords.json" 2>/dev/null || true
      if assert_json 'd.get("engine") == "gufi_vt" and any("ocr-sample" in str(r) for r in d.get("rows") or []) and not d.get("failed")' <"$T/qwords.json" 2>/dev/null; then
        ok "and a query with a words condition runs through gufi_vt too"
      else
        bad "a query with a words condition did not run through gufi_vt"
      fi
    fi
  else
    bad "could not add a second library"
  fi
  # The update check. This payload is unpacked in a directory its user can
  # write and has no install-kind file, so it is taken as a per-user install
  # (mode app) that updates itself. The stand-in release server above offers
  # one release with a macOS build only, which is not an update for this
  # payload: the check reaches the server, reports nothing to install, and
  # an install request is refused. test_update_e2e.sh installs an update.
  api -m 30 POST "http://127.0.0.1:$PORT/api/updates/check" >"$T/updcheck.json" 2>/dev/null || true
  if assert_json 'd.get("state") == "idle" and not d.get("error") and not d.get("offline") and d.get("checkedAt")' <"$T/updcheck.json" 2>/dev/null; then
    ok "the update check reaches the release server"
  else
    bad "the update check did not complete:"; sed 's/^/        /' "$T/updcheck.json" 2>/dev/null || true
  fi
  check "a release with only a macOS build is not an update (mode app, nothing available, canInstall false)" \
    assert_json 'd["mode"] == "app" and d["available"] is False and d["latest"] is None and d["canInstall"] is False' <"$T/updcheck.json"
  echo "  note  update status: $("$PY" -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print({k: d.get(k) for k in ("mode", "available", "canInstall", "reason", "state")})' "$T/updcheck.json" 2>/dev/null)"
  check "installing is refused (409), not attempted" sh -c "! '$PY' -I '$HERE/api.py' POST 'http://127.0.0.1:$PORT/api/updates/install' '{}' >/dev/null 2>&1"
  api -m 5 GET "http://127.0.0.1:$PORT/api/index/extractors" >/dev/null 2>&1 || true

  # Stopping it: a TERM to the run loop ends the server and the loop.
  kill -TERM "$SERVER_LOOP"
  stopped=0
  for _ in $(seq 1 30); do kill -0 "$SERVER_LOOP" 2>/dev/null || { stopped=1; break; }; sleep 1; done
  if [ "$stopped" = 1 ] && ! api -m 2 GET "http://127.0.0.1:$PORT/api/version" >/dev/null 2>&1; then
    ok "TERM stops the run loop and the server"
  else
    bad "the run loop did not stop"
  fi
  SERVER_LOOP=
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "All checks passed"
