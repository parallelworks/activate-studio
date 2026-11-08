s=$1 bin=$2 src=$3 idx=$4 threads=$5 tools=$6 model=$7 create=$8 meaning=${9:-} text=${10:-} onnx=${11:-} reads=${12:-} embeds=${13:-} depth=${14:-}
uname -n > "$s/host" 2>/dev/null
echo $$ > "$s/pid"
phase() { printf '%s\n' "$1" > "$s/phase"; }
fail() { printf '%s\n' "$1" > "$s/error"; exit 1; }
trap 'echo $? > "$s/exit"' EXIT
export PATH="$bin:$PATH"
[ "$(uname -s)" = Darwin ] && export DYLD_LIBRARY_PATH="${bin%/*}/lib"
phase prepare
if [ ! -d "$src" ]; then
  [ "$create" = 1 ] || fail "$src is not a folder."
  mkdir -p "$src" || fail "$src could not be created."
fi
src=$(cd "$src" && pwd -P) || fail "$src cannot be read."
[ "$src" != / ] || fail "Indexing / is not supported; choose a folder."
# The index folder is written to either way; the check said it can be made.
mkdir -p "$idx" || fail "$idx could not be created."
idx=$(cd "$idx" && pwd -P)
base=${src##*/}; tree="$idx/$base"; stage="$idx/.build"
# rm -rf, opening the folders first where one keeps it out: an index of a
# folder its owner may not write has index folders of that mode.
rmtree() { [ -e "$1" ] || return 0; rm -rf "$1" 2>/dev/null && return 0; chmod u+rwx "$(dirname "$1")" 2>/dev/null; chmod -R u+rwx "$1" 2>/dev/null; rm -rf "$1"; }
rmtree "$stage"
phase index
dir2index() { "$bin/gufi_dir2index" -n "$threads" -x "$src" "$stage" > "$s/dir2index.log" 2>&1 || fail "gufi_dir2index failed: $(tail -n 3 "$s/dir2index.log" | tr '\n' ' ')"; }
dir2index
# gufi_dir2index gives each index folder the mode of its source folder
# before the folders inside it make theirs, so for a user other than root
# a source folder its owner may not write (0555, say) has what is inside it
# left out, with a "mkdir ... failure" line for each. Those index folders
# are made first and the build is run again; what the index records about
# the source is unchanged.
if grep -q '^mkdir .*/db\.db failure: ' "$s/dir2index.log"; then
  rmtree "$stage"
  find "$src" -type d ! -perm -700 -print0 2>/dev/null | while IFS= read -r -d '' d; do
    find "$d" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | while IFS= read -r -d '' c; do
      if [ -r "$c" ] && [ -x "$c" ]; then mkdir -p "$stage/$base${c#"$src"}"; fi
    done
  done
  dir2index
fi
[ -d "$stage/$base" ] || fail "gufi_dir2index wrote no tree at $stage/$base."
# The index folders opened for this user, so the text pass can write in them
# and the tree can be moved; their group and other bits stay the source's.
[ "$(id -u)" = 0 ] || find "$stage/$base" -type d ! -perm -700 -exec chmod u+rwx {} + 2>/dev/null
new="$stage/$base"
# The Studio's helper, which runs the text pass and the embedder, kept in
# ~/.activate-studio/go/<version>, and ONNX Runtime with the embedding
# model, kept in ~/.activate-studio/embed/<version>: each taken into use
# once, from what the Studio sends while the build runs (it keeps
# <version>.sending touched as it goes, and removes it when done), after
# the helper has checked every file against the SHA256SUMS sent last.
# Several builds may take one into use at once; the first to finish keeps
# its copy.
g="$HOME/.activate-studio/go/$tools"
helper="$g/studio-remote"
# kitready DIR CHECKER LOG: DIR whole, from DIR.files.
kitready() {
  [ -f "$1/ok" ] && return 0
  while [ -f "$1.sending" ] && [ -n "$(find "$1.sending" -mmin -10 2>/dev/null)" ]; do sleep 1; done
  t="$1.tmp-$$"; rmtree "$t"
  if [ -f "$1.files/SHA256SUMS" ] && mkdir -p "$t" && cp "$1.files"/* "$t"/ 2>"$3" && { [ ! -f "$t/studio-remote" ] || chmod 755 "$t/studio-remote"; } &&
    "$2" verify "$t" $(awk '{ print $2 "=" $1 }' "$t/SHA256SUMS") >> "$3" 2>&1; then
    : > "$t/ok"
    [ -e "$1" ] || mv "$t" "$1"
    [ -f "$1/ok" ] && rmtree "$1.files"
  fi
  rmtree "$t"
  [ -f "$1/ok" ]
}
toolsready() { kitready "$g" "$g.tmp-$$/studio-remote" "$s/tools.log"; }
e="$HOME/.activate-studio/embed/$onnx"
onnxready() { kitready "$e" "$helper" "$s/onnx.log"; }
fulltext=false vectors=false textwhy= vecwhy=
rm -f "$s/text.json" "$s/embed.json"
if [ "$reads" = 1 ]; then
  if [ -z "$tools" ]; then textwhy="the Studio has no helper for this system ($(uname -s) $(uname -m))"
  elif ! toolsready; then textwhy="the Studio's helper could not be installed: $(tail -n 1 "$s/tools.log" 2>/dev/null)"; fi
fi
if [ "$reads" = 1 ] && [ -z "$textwhy" ]; then
  phase extract
  # shellcheck disable=SC2086
  if "$helper" text --kb-root "$src" --index "$new" --extract-cache "$idx/.extract" --report "$s/text.json" $text > "$s/text.log" 2>&1; then fulltext=true; else textwhy="the text pass stopped: $(tail -n 1 "$s/text.log")"; fi
  if [ "$fulltext" = true ] && [ -x "$bin/gufi_sqlite3" ] && [ "$embeds" = 1 ]; then
    phase embed
    onnxdir=
    if [ -n "$onnx" ] && onnxready; then onnxdir=$e; fi
    if [ -n "$onnxdir" ] || [ -n "$model" ]; then
      # The ONNX model when its kit is there, else the GGUF one.
      # shellcheck disable=SC2086
      STUDIO_ORT_LIB=${onnxdir:+$onnxdir/libonnxruntime.so} "$helper" embed --index "$new" ${model:+--model "$model"} ${onnxdir:+--onnx "$onnxdir"} --gufi-sqlite3 "$bin/gufi_sqlite3" --cache "$idx/.embed-cache.db" --prune --report "$s/embed.json" $meaning > "$s/embed.log" 2>&1 && vectors=true
      [ "$vectors" = true ] || vecwhy="the embedding pass stopped: $(tail -n 1 "$s/embed.log")"
    else
      vecwhy="the embedding model could not be installed: $(tail -n 1 "$s/onnx.log" 2>/dev/null)"
    fi
  fi
  # The Studio searches the text with gufi_query, whose SQLite must read it.
  if [ "$fulltext" = true ]; then "$bin/gufi_query" -n 1 --max-level 0 -E 'SELECT count(*) FROM words;' "$new" >/dev/null 2>&1 || { fulltext=false; textwhy="gufi_query on this system cannot read the text tables"; }; fi
fi
# The index answers whole until here: the new one, text and vectors in it,
# takes its place in one move.
rmtree "$idx/.old"
if [ -d "$tree" ]; then mv "$tree" "$idx/.old"; fi
mv "$new" "$tree" || fail "The new index could not be moved into place."
rmtree "$stage"; rmtree "$idx/.old"
case "$depth" in meaning-*) if [ "$vectors" != true ] && [ -z "$vecwhy" ]; then
  if [ "$fulltext" != true ]; then vecwhy="there is no text to embed"
  elif [ ! -x "$bin/gufi_sqlite3" ]; then vecwhy="there is no gufi_sqlite3 in $bin"
  else vecwhy="there is no embedding model for this system"; fi
fi ;; esac
phase count
c=$("$bin/gufi_query" -n "$threads" -d '|' -a 1 -I 'CREATE TABLE t (d INT64, f INT64);' -K 'CREATE TABLE agg (d INT64, f INT64);' \
  -E 'INSERT INTO t SELECT count(*), sum(totfiles) FROM vrsummary;' -J 'INSERT INTO agg SELECT * FROM t;' -G 'SELECT sum(d), sum(f) FROM agg;' "$tree" 2>/dev/null | tail -n 1)
dirs=${c%%|*}; files=${c##*|}
case "$dirs" in ''|*[!0-9]*) dirs=0 ;; esac
case "$files" in ''|*[!0-9]*) files=0 ;; esac
# What the index itself holds, whatever the passes reported. A folder
# without the table answers nothing.
sumq() { "$bin/gufi_query" -n "$threads" -E "$1" "$tree" 2>/dev/null | awk '{ n += $1 } END { print n + 0 }'; }
pdfwords=$(sumq "SELECT count(*) FROM words WHERE lower(fname) LIKE '%.pdf';")
pdfvectors=$(sumq "SELECT count(DISTINCT g.fname) FROM gchunks g JOIN gvec_rowids v ON v.rowid = g.cid WHERE lower(g.fname) LIKE '%.pdf';")
vectorrows=$(sumq 'SELECT count(*) FROM gvec_rowids;')
report() { if [ -s "$1" ]; then cat "$1"; else printf null; fi; }
json() { printf '"%s"' "$(printf '%s' "$1" | tr '\n\t' '  ' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"; }
{
  printf '{\n  "version": 1,\n  "source": %s,\n  "index": %s,\n  "indexDir": %s,\n' "$(json "$src")" "$(json "$tree")" "$(json "$idx")"
  printf '  "builtAt": %s,\n  "gufiBin": %s,\n' "$(json "$(date -u +%Y-%m-%dT%H:%M:%SZ)")" "$(json "$bin")"
  printf '  "directories": %s,\n  "files": %s,\n  "fullText": %s,\n  "vectors": %s,\n' "$dirs" "$files" "$fulltext" "$vectors"
  printf '  "depth": %s,\n  "pdfWords": %s,\n  "pdfVectors": %s,\n  "vectorRows": %s,\n' "$(json "$depth")" "$pdfwords" "$pdfvectors" "$vectorrows"
  printf '  "textWhy": %s,\n  "vectorsWhy": %s,\n  "text": %s,\n  "embed": %s\n}\n' "$(json "$textwhy")" "$(json "$vecwhy")" "$(report "$s/text.json")" "$(report "$s/embed.json")"
} > "$idx/.studio-index.json.tmp" && mv "$idx/.studio-index.json.tmp" "$idx/.studio-index.json"
phase done
exit 0