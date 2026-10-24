// Libraries on the same remote host are searched in one remote gufi_query
// with their index roots stacked, so a search across them is one round trip;
// a library on another host gets its own. Each hit goes back to its own
// library, and one host failing names its libraries and leaves the rest.
// Fake cluster libraries stand in, through the remote session with a local
// bash and a fake gufi_query that logs each run.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'stack-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'stack-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'stack-kb-'))
process.env.INDEX_BASE = IX
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'
process.env.GUFI_BIN = path.join(dir, 'no-local-gufi')
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))

const home = path.join(dir, 'home')
const bin = path.join(home, 'gufi', 'bin')
const log = path.join(dir, 'runs.log')
fs.mkdirSync(bin, { recursive: true })
for (const n of ['a', 'b', 'c']) fs.mkdirSync(path.join(home, 'idx', n), { recursive: true })
// One line per run with its index roots; a name row for each root given,
// except on the broken host, whose gufi_query fails.
fs.writeFileSync(path.join(bin, 'gufi_query'), `#!/bin/sh
R=$(printf '\\036')
row() { printf '%s' "$1"; shift; for c in "$@"; do printf '%s%s' "$R" "$c"; done; printf '\\n'; }
roots=""
for a in "$@"; do case "$a" in "$HOME"/idx/*) roots="$roots $(basename "$a")" ;; esac; done
echo "$roots" >> ${log}
case "$roots" in *c*) echo "Error: cannot open database" >&2; exit 1 ;; esac
case "$*" in
  *"SELECT 'n', "*)
    for a in "$@"; do case "$a" in "$HOME"/idx/*) b=$(basename "$a"); row n 2 "$a/notes/$b-plan.md" "$b-plan.md" 7 1700000000 "" ;; esac; done ;;
esac
`, { mode: 0o755 })

const owner = 'single'
const hash = createHash('sha256').update(owner).digest('hex').slice(0, 16)
fs.mkdirSync(path.join(IX, 'users', hash), { recursive: true })
fs.writeFileSync(path.join(IX, 'users', hash, 'owner'), owner)
const def = (id, label, site, n) => ({ id, label, site, resource: `pw://u/${site}`, sourceDir: '~', sourceRoot: path.join(home, 'src', n), indexRoot: path.join(home, 'idx', n), gufiBin: bin, state: 'ready', run: null, error: null, builtAt: null, directories: 1, files: 1, scheduler: null })
// a and b share a host; c is on another, which fails.
fs.writeFileSync(path.join(IX, 'users', hash, 'libraries.json'), JSON.stringify([def('u-a', 'Library A', 'cluster-1', 'a'), def('u-b', 'Library B', 'cluster-1', 'b'), def('u-c', 'Library C', 'cluster-2', 'c')]))

const R = await import('../dist/remoteExec.js')
R.setRemoteSpawn((site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, HOME: home } }))
const S = await import('../dist/libraryScope.js')
test.after(() => R.closeRemoteChannels())

test('libraries on one host share one remote gufi_query; another host has its own; hits keep their library', async () => {
  fs.rmSync(log, { force: true })
  const libs = S.scopeLibraries('all', null, 'kb').filter(l => l.site)
  assert.deepEqual(libs.map(l => l.id), ['u-a', 'u-b', 'u-c'])
  const omitted = []
  const hits = await S.searchScope(libs, 'plan', 10, { semantic: false, omitted })
  const runs = fs.readFileSync(log, 'utf8').split('\n').filter(Boolean).map(l => l.trim()).sort()
  // The search's one run per host (the remote session may first ask its
  // thread count, which is not gufi_query).
  assert.deepEqual(runs, ['a b', 'c'])
  assert.deepEqual(hits.map(h => [h.library.id, h.path]).sort(), [['u-a', 'notes/a-plan.md'], ['u-b', 'notes/b-plan.md']])
  assert.deepEqual(omitted.map(o => o.id), ['u-c'], 'the failing host names its library')
})
