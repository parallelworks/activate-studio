// A user's own library on a cluster: added through the index workflow,
// made ready from the summary the run prints, and browsed and searched on
// the cluster. pw, the remote session, and gufi_query are fakes here: the
// remote loop runs in a local bash whose HOME holds a fake GUFI install.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ul-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'ul-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ul-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'

// The remote side: a home with a GUFI install whose gufi_query answers
// the shapes of query the Studio sends.
const home = path.join(dir, 'home')
const bin = path.join(home, '.activate-studio', 'gufi', 'abc123', 'bin')
const index = path.join(home, '.activate-studio', 'indexes', 'proj-1', 'proj')
fs.mkdirSync(bin, { recursive: true }); fs.mkdirSync(index, { recursive: true })
fs.writeFileSync(path.join(bin, 'gufi_query'), `#!/bin/sh
args="$*"
for last; do :; done
R=$(printf '\\036')
case "$args" in
  *"--max-level 0"*) printf 'notes.txt%sf%s17%s1700000000\\n' "$R" "$R" "$R" ;;
  *"--max-level 1"*) printf 'runs%s1700000001\\n' "$R" ;;
  *"INSERT INTO tn"*) printf 'n%s1%s%s/runs/case-a/input.yaml%sinput.yaml%s30%s1700000002%s\\n' "$R" "$R" "$last" "$R" "$R" "$R" "$R" ;;
  *) printf '%s/runs/case-a/input.yaml%sinput.yaml%s30%s1700000002\\n' "$last" "$R" "$R" "$R" ;;
esac
`, { mode: 0o755 })

const pwLog = path.join(dir, 'pw.log')
const fakePw = path.join(dir, 'pw')
fs.writeFileSync(fakePw, `#!/bin/sh
echo "$@" >> ${pwLog}
case "$1 $2" in
  "clusters ls") echo '[{"name":"cluster-a","user":"jdoe"}]' ;;
  "workflows get") echo 'Error: workflow not found' >&2; exit 1 ;;
  "workflows create") echo '{"name":"ok"}' ;;
  "workflows run") echo '{"run":{"slug":"studio-gufi-index-x-00001"}}' ;;
  "workflows runs")
    if [ "$3" = "view" ]; then echo '{"status":"completed"}'; else
      printf 'step output\\nSTUDIO_INDEX_BEGIN\\n{"source":"~/proj","index":"~/.activate-studio/indexes/proj-1/proj","mode":"build","builtAt":"2026-10-11T00:00:00Z","gufiCommit":"abc123","directories":2,"files":3}\\nSTUDIO_INDEX_END\\n'; fi ;;
esac
`, { mode: 0o755 })
process.env.PW_CLI = fakePw

const R = await import('../dist/remoteExec.js')
R.setRemoteSpawn((site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, HOME: home } }))
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
test.after(() => R.closeRemoteChannels())

test('the index summary is read from between its markers', () => {
  assert.deepEqual(U.manifestFrom('x\nSTUDIO_INDEX_BEGIN\n{"index":"/a"}\nSTUDIO_INDEX_END\n'), { index: '/a' })
  assert.equal(U.manifestFrom('no markers'), null)
})

test('adding from a cluster registers the workflow once and starts the build as the owner', async () => {
  const d = await U.addFromCluster(undefined, { site: 'cluster-a', sourceDir: '~/proj', label: 'Project' })
  assert.equal(d.state, 'building'); assert.equal(d.resource, 'pw://jdoe/cluster-a'); assert.equal(d.run.slug, 'studio-gufi-index-x-00001')
  const log = fs.readFileSync(pwLog, 'utf8')
  assert.match(log, /workflows create --yaml \S+gufi-index\.yaml --display-name Studio library index studio-gufi-index-[0-9a-f]{8}/)
  const run = log.split('\n').find(l => l.startsWith('workflows run'))
  const inputs = JSON.parse(run.slice(run.indexOf('{'), run.lastIndexOf('}') + 1))
  assert.deepEqual(inputs, { resource: 'pw://jdoe/cluster-a', cluster: { scheduler: false }, index: { source_dir: '~/proj', index_dir: '', mode: 'build', threads: 4 } })
  await assert.rejects(U.addFromCluster(undefined, { site: 'cluster-a', sourceDir: '~/a; rm -rf ~' }), /directory/)
})

test('a finished run makes the library ready and the viewer can search and browse it', async () => {
  const [d] = await U.listUserLibraries(undefined)
  assert.equal(d.state, 'ready', d.error ?? '')
  assert.equal(d.indexRoot, index); assert.equal(d.sourceRoot, path.join(home, 'proj')); assert.equal(d.gufiBin, bin)
  assert.equal(d.directories, 2); assert.equal(d.files, 3)
  const lib = L.listLibraries('kb', null).find(l => l.id === d.id)
  assert.ok(lib, 'it joins the library list')
  assert.equal(L.listLibraries('kb').some(l => l.id === d.id), false, 'not in the deployment\'s own listing')
  assert.equal(lib.site, 'cluster-a'); assert.equal(lib.writable, false); assert.match(lib.indexRoot, /^remote:/)
  assert.equal(L.publicLibrary(lib).source, true)
  const tree = await U.remoteTree(lib.indexRoot, '')
  assert.deepEqual(tree.map(e => [e.type, e.name, e.size]), [['dir', 'runs', 0], ['file', 'notes.txt', 17]])
  const hits = await G.searchNames('input', 5, lib.indexRoot)
  assert.deepEqual(hits.map(h => h.path), ['runs/case-a/input.yaml'], 'paths are relative to the remote index')
  await assert.rejects(U.remoteTree(lib.indexRoot, '../../etc'), /invalid path/)
})

test('removing forgets the library', () => {
  const [d] = U.loadUserLibraries('single')
  U.removeUserLibrary(undefined, d.id)
  assert.equal(L.listLibraries('kb', null).some(l => l.id === d.id), false)
})
