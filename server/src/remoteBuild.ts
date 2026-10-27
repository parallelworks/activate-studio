import fs from 'node:fs'
import path from 'node:path'
import { PROJECT_ROOT } from './config.js'

/**
 * Building a GUFI index on a remote system over the session remoteExec.ts
 * keeps (plain ssh or pw ssh): the scripts that check the folders, start
 * the build there, and report how far it has got. userLibraries.ts runs
 * them and keeps the library's state.
 *
 * The build runs on the remote system as a process of its own, detached
 * from the session (setsid where there is one, nohup always), so it goes
 * on when the session or the Studio stops, and the Studio follows it again
 * when it next looks. Its state is a folder of small files,
 * ~/.activate-studio/builds/<build id>: the phase it is in, its exit status
 * and error, its process id, and its logs. Like the index workflow
 * (deploy/gufi-index.yaml), it writes the new index beside the old one and
 * swaps it in when complete, and leaves .studio-index.json in the index
 * folder with what it built. Where the system has Python 3.10 or later
 * with SQLite's full-text module, the Studio's enrich.py is copied there
 * and run, so the index carries the files' text; where a GUFI with the
 * embedding extension and the Studio's model are there too, embed.py runs
 * as well.
 *
 * Every value reaches the scripts as a positional argument, never as text
 * inside them.
 */

/** Checks the source and index folders, and what the build can run there.
 *  $1 is the source folder, $2 the index folder, both absolute. Prints
 *  key=value lines. */
export const BUILD_CHECK = `near() { p=$1; while [ ! -e "$p" ] && [ "$p" != / ]; do p=$(dirname "$p"); done; printf '%s' "$p"; }
if [ -d "$1" ]; then
  if [ -r "$1" ] && [ -x "$1" ]; then echo source=ok; else echo source=unreadable; fi
  echo "source_real=$(cd "$1" 2>/dev/null && pwd -P)"
elif [ -e "$1" ]; then echo source=notdir
else a=$(near "$1"); if [ -d "$a" ] && [ -w "$a" ]; then echo source=create; else echo source=nocreate; fi; echo "source_at=$a"; fi
if [ -d "$2" ]; then
  if [ -f "$2/.studio-index.json" ]; then echo index=studio; echo "index_source=$(sed -n 's/^ *"source": *"\\(.*\\)", *$/\\1/p' "$2/.studio-index.json" | head -n 1)"
  elif [ -n "$(ls -A "$2" 2>/dev/null)" ]; then echo index=foreign
  elif [ -w "$2" ]; then echo index=empty
  else echo index=readonly; fi
  echo "index_real=$(cd "$2" 2>/dev/null && pwd -P)"
elif [ -e "$2" ]; then echo index=notdir
else a=$(near "$2"); if [ -d "$a" ] && [ -w "$a" ]; then echo index=create; else echo index=nocreate; fi; echo "index_at=$a"; fi
for p in python3.13 python3.12 python3.11 python3.10 python3; do
  c=$(command -v "$p" 2>/dev/null) || continue
  if "$c" -c 'import sqlite3, sys; sqlite3.connect(":memory:").execute("CREATE VIRTUAL TABLE t USING fts5(x)"); sys.exit(0 if sys.version_info >= (3, 10) else 1)' 2>/dev/null; then
    echo "python=$c"; echo "python_version=$("$c" -c 'import sys; print("%d.%d" % sys.version_info[:2])')"; break
  fi
done
m="$HOME/.activate-studio/models/minilm384.gguf"; [ -f "$m" ] && echo "model=$m"
exit 0`

/** The build itself, written to the state folder and run detached.
 *  $1 state folder, $2 GUFI bin, $3 source, $4 index folder, $5 threads,
 *  $6 python (or empty), $7 embedding model (or empty), $8 1 to make the
 *  source folder when it is missing, $9 embed.py's options for the level of
 *  search by meaning (meaningArgs), $10 enrich.py's for the depth
 *  (textArgs); both words with no spaces or quotes. */
export const BUILD_RUN = `s=$1 bin=$2 src=$3 idx=$4 threads=$5 py=$6 model=$7 create=$8 meaning=\${9:-} text=\${10:-}
uname -n > "$s/host" 2>/dev/null
echo $$ > "$s/pid"
phase() { printf '%s\\n' "$1" > "$s/phase"; }
fail() { printf '%s\\n' "$1" > "$s/error"; exit 1; }
trap 'echo $? > "$s/exit"' EXIT
export PATH="$bin:$PATH"
[ "$(uname -s)" = Darwin ] && export DYLD_LIBRARY_PATH="\${bin%/*}/lib"
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
base=\${src##*/}; tree="$idx/$base"; stage="$idx/.build"
# rm -rf, opening the folders first where one keeps it out: an index of a
# folder its owner may not write has index folders of that mode.
rmtree() { [ -e "$1" ] || return 0; rm -rf "$1" 2>/dev/null && return 0; chmod u+rwx "$(dirname "$1")" 2>/dev/null; chmod -R u+rwx "$1" 2>/dev/null; rm -rf "$1"; }
rmtree "$stage"
phase index
dir2index() { "$bin/gufi_dir2index" -n "$threads" -x "$src" "$stage" > "$s/dir2index.log" 2>&1 || fail "gufi_dir2index failed: $(tail -n 3 "$s/dir2index.log" | tr '\\n' ' ')"; }
dir2index
# gufi_dir2index gives each index folder the mode of its source folder
# before the folders inside it make theirs, so for a user other than root
# a source folder its owner may not write (0555, say) has what is inside it
# left out, with a "mkdir ... failure" line for each. Those index folders
# are made first and the build is run again; what the index records about
# the source is unchanged.
if grep -q '^mkdir .*/db\\.db failure: ' "$s/dir2index.log"; then
  rmtree "$stage"
  find "$src" -type d ! -perm -700 -print0 2>/dev/null | while IFS= read -r -d '' d; do
    find "$d" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null | while IFS= read -r -d '' c; do
      if [ -r "$c" ] && [ -x "$c" ]; then mkdir -p "$stage/$base\${c#"$src"}"; fi
    done
  done
  dir2index
fi
[ -d "$stage/$base" ] || fail "gufi_dir2index wrote no tree at $stage/$base."
# The index folders opened for this user, so the text pass can write in them
# and the tree can be moved; their group and other bits stay the source's.
[ "$(id -u)" = 0 ] || find "$stage/$base" -type d ! -perm -700 -exec chmod u+rwx {} + 2>/dev/null
rmtree "$idx/.old"
if [ -d "$tree" ]; then mv "$tree" "$idx/.old"; fi
mv "$stage/$base" "$tree" || fail "The new index could not be moved into place."
rmtree "$stage"; rmtree "$idx/.old"
fulltext=false vectors=false
if [ -n "$py" ] && [ -f "$s/enrich.py" ]; then
  phase extract
  # shellcheck disable=SC2086
  if "$py" "$s/enrich.py" --kb-root "$src" --index "$tree" --extract-cache "$idx/.extract" $text > "$s/enrich.log" 2>&1; then fulltext=true; fi
  if [ "$fulltext" = true ] && [ -n "$model" ] && [ -x "$bin/gufi_sqlite3" ] && [ -f "$s/embed.py" ]; then
    phase embed
    # shellcheck disable=SC2086
    "$py" "$s/embed.py" --index "$tree" --model "$model" --gufi-sqlite3 "$bin/gufi_sqlite3" $meaning > "$s/embed.log" 2>&1 && vectors=true
  fi
  # The Studio searches the text with gufi_query, whose SQLite must read it.
  if [ "$fulltext" = true ]; then "$bin/gufi_query" -n 1 --max-level 0 -E 'SELECT count(*) FROM words;' "$tree" >/dev/null 2>&1 || fulltext=false; fi
fi
phase count
c=$("$bin/gufi_query" -n "$threads" -d '|' -a 1 -I 'CREATE TABLE t (d INT64, f INT64);' -K 'CREATE TABLE agg (d INT64, f INT64);' \\
  -E 'INSERT INTO t SELECT count(*), sum(totfiles) FROM vrsummary;' -J 'INSERT INTO agg SELECT * FROM t;' -G 'SELECT sum(d), sum(f) FROM agg;' "$tree" 2>/dev/null | tail -n 1)
dirs=\${c%%|*}; files=\${c##*|}
case "$dirs" in ''|*[!0-9]*) dirs=0 ;; esac
case "$files" in ''|*[!0-9]*) files=0 ;; esac
json() { printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\\\/\\\\\\\\/g' -e 's/"/\\\\"/g')"; }
{
  printf '{\\n  "version": 1,\\n  "source": %s,\\n  "index": %s,\\n  "indexDir": %s,\\n' "$(json "$src")" "$(json "$tree")" "$(json "$idx")"
  printf '  "builtAt": %s,\\n  "gufiBin": %s,\\n' "$(json "$(date -u +%Y-%m-%dT%H:%M:%SZ)")" "$(json "$bin")"
  printf '  "directories": %s,\\n  "files": %s,\\n  "fullText": %s,\\n  "vectors": %s\\n}\\n' "$dirs" "$files" "$fulltext" "$vectors"
} > "$idx/.studio-index.json.tmp" && mv "$idx/.studio-index.json.tmp" "$idx/.studio-index.json"
phase done
exit 0`

/** Writes the build's files to its state folder and starts it detached.
 *  $1 build id, $2 the run script, $3 enrich.py, $4 embed.py (either may
 *  be empty), then BUILD_RUN's arguments from its second. */
export const BUILD_START = `s="$HOME/.activate-studio/builds/$1"
mkdir -p "$s" || exit 3
printf '%s\\n' "$2" > "$s/run.sh"
rm -f "$s/enrich.py" "$s/embed.py" "$s/exit" "$s/error" "$s/phase" "$s/pid" "$s/host"
[ -n "$3" ] && printf '%s\\n' "$3" > "$s/enrich.py"
[ -n "$4" ] && printf '%s\\n' "$4" > "$s/embed.py"
shift 4
if command -v setsid >/dev/null 2>&1; then
  setsid nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
else
  nohup bash "$s/run.sh" "$s" "$@" </dev/null >"$s/log" 2>&1 &
fi
echo started`

/** How far a build has got. $1 build id, $2 index folder.
 *
 *  Whether the run is still alive is read before its exit file. The run
 *  writes exit as it stops, so a run already stopped has written it, and
 *  one that stops between the two reads counts as running now and as
 *  finished at the next look. Read the other way round, a build that ended
 *  between the reads was taken for one that stopped without finishing.
 *  The process id means something only on the machine that started the
 *  run: where a host name reaches several login nodes, another node's
 *  session cannot tell, and the build counts as running until its exit
 *  file says how it ended. */
export const BUILD_STATUS = `s="$HOME/.activate-studio/builds/$1"
[ -d "$s" ] || { echo state=missing; exit 0; }
pid=$(cat "$s/pid" 2>/dev/null)
host=$(cat "$s/host" 2>/dev/null)
alive=1
if [ -n "$pid" ] && { [ -z "$host" ] || [ "$host" = "$(uname -n)" ]; } && ! kill -0 "$pid" 2>/dev/null; then alive=0; fi
echo "phase=$(cat "$s/phase" 2>/dev/null)"
if [ -f "$s/exit" ]; then
  echo "exit=$(cat "$s/exit")"
  [ -f "$s/error" ] && echo "error=$(head -c 400 "$s/error" | tr '\\n' ' ')"
  if [ -f "$2/.studio-index.json" ]; then echo __MANIFEST__; cat "$2/.studio-index.json"; fi
  exit 0
fi
if [ "$alive" = 0 ]; then echo state=gone; echo "log=$(tail -n 3 "$s/log" 2>/dev/null | tr '\\n' ' ')"; exit 0; fi
[ "$(cat "$s/phase" 2>/dev/null)" = index ] && echo "dirs=$(find "$2/.build" -name db.db 2>/dev/null | wc -l | tr -d ' ')"
exit 0`

/** key=value lines as a record; later keys win. */
export function keyValues(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf('=')
    if (i > 0 && /^[a-z_]+$/.test(line.slice(0, i))) out[line.slice(0, i)] = line.slice(i + 1).trim()
  }
  return out
}

export interface BuildStatus {
  /** missing: no such build; gone: it stopped without finishing; running; finished. */
  state: 'missing' | 'gone' | 'running' | 'finished'
  phase: string
  exit: number | null
  error: string | null
  /** Folders indexed so far, while gufi_dir2index runs. */
  dirs: number | null
  manifest: Record<string, unknown> | null
}

export function parseStatus(text: string): BuildStatus {
  const [head, manifestText] = text.split('\n__MANIFEST__\n')
  const kv = keyValues(head)
  let manifest: Record<string, unknown> | null = null
  if (manifestText) { try { manifest = JSON.parse(manifestText) as Record<string, unknown> } catch { /* written halfway */ } }
  const exit = kv.exit !== undefined && /^\d+$/.test(kv.exit) ? Number(kv.exit) : null
  const state = kv.state === 'missing' ? 'missing' : kv.state === 'gone' ? 'gone' : exit !== null ? 'finished' : 'running'
  return {
    state, phase: kv.phase ?? '', exit,
    error: kv.error || (kv.state === 'gone' ? `the build stopped without finishing${kv.log ? `: ${kv.log}` : ''}` : null),
    dirs: kv.dirs !== undefined && /^\d+$/.test(kv.dirs) ? Number(kv.dirs) : null,
    manifest,
  }
}

/** The Studio's indexer scripts, copied to the remote system for a build. */
export function indexerScript(name: 'enrich.py' | 'embed.py'): string {
  return fs.readFileSync(path.join(PROJECT_ROOT, 'indexer', name), 'utf8')
}

/** What a phase reads as, for the progress line. */
export const PHASE_LABEL: Record<string, string> = {
  prepare: 'Preparing', index: 'Indexing folders', extract: 'Extracting text', embed: 'Computing search by meaning', count: 'Counting', done: 'Done',
}
