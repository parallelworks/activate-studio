// Building a GUFI index on a remote system over the session: the check
// (GUFI, the folders, text extraction), the build started there as a
// process of its own and followed until the library is ready, browsing and
// searching it, the details panel, a rebuild, and a rebuild that fails.
// The remote systems are stand-ins: each ssh or pw ssh session runs in a
// local bash whose HOME is that system's, so the build really runs, with
// this machine's GUFI and Python. Nothing reaches a platform or a host, and
// downloads fail, here and there, as on a network with no way out. Skips
// without GUFI. Search by meaning there with the Studio's ONNX model runs
// where STUDIO_TEST_ONNX_DIR names a folder with model.onnx and
// tokenizer.json and STUDIO_TEST_WHEELS one that fetch_embed.py --download
// filled for this Python; the GGUF fallback where STUDIO_TEST_GGUF names
// that model.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawn, spawnSync } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const skip = !fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'rbuild-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'rbuild-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'rbuild-ix-'))
// ssh's sockets (socketDir in remoteExec.ts) go in a folder of this test's
// own, not the user's runtime folder.
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
// Stands in for this computer's pw sign-in.
process.env.PW_API_KEY = 'cli-key'
delete process.env.AUTH_JWKS_URL
// No downloads: fetch_embed.py, here and on the stand-in systems, finds no way out.
const NO_NETWORK = { https_proxy: 'http://127.0.0.1:9', HTTPS_PROXY: 'http://127.0.0.1:9', http_proxy: 'http://127.0.0.1:9', HTTP_PROXY: 'http://127.0.0.1:9' }
Object.assign(process.env, NO_NETWORK)
delete process.env.no_proxy; delete process.env.NO_PROXY
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const exe = (f, body) => { write(f, body); fs.chmodSync(f, 0o755) }
exe(path.join(dir, 'pw'), '#!/bin/sh\nexit 0\n')
process.env.PW_CLI = path.join(dir, 'pw')

// Two systems: one over ssh, one a cluster over pw ssh. GUFI is found
// where the Studio looks (this machine's), and on the ssh one also
// through the bin directory given.
const homes = { 'me@linux-box': path.join(dir, 'box'), 'cluster-a': path.join(dir, 'cluster') }
const project = path.join(homes['me@linux-box'], 'project')
write(path.join(project, 'readme.md'), '# Project\nThe wind tunnel campaign.\n')
write(path.join(project, 'runs', 'case-1', 'input.yaml'), 'mach: 0.8\n')
write(path.join(project, 'runs', 'case-1', 'out.dat'), Buffer.alloc(2048, 7))
write(path.join(homes['cluster-a'], 'work', 'notes.txt'), 'cluster notes\n')

const R = await import('../dist/remoteExec.js')
const sessions = []
R.setRemoteSpawn((site, key, command) => {
  const name = R.parseSshSite(site)?.destination ?? site
  sessions.push({ site, key })
  if (!homes[name]) return spawn('/bin/sh', ['-c', 'echo "ssh: Could not resolve hostname" >&2; exit 255'], { stdio: ['pipe', 'pipe', 'pipe'] })
  return spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: homes[name], PATH: '/usr/bin:/bin', ...NO_NETWORK } })
})
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const D = await import('../dist/libraryDetails.js')
const RB = await import('../dist/remoteBuild.js')
U.setBuildPollMs(50)
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.register(D.libraryDetailsRoutes)
await app.ready()
test.after(async () => { R.closeRemoteChannels(); await app.close() })

const post = async (url, body) => { const r = await app.inject({ method: 'POST', url, payload: body }); return { status: r.statusCode, body: r.json() } }
const listing = async () => (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json()
const waitFor = async (id, states = ['ready', 'failed']) => {
  for (let i = 0; i < 400; i++) {
    const l = (await listing()).libraries.find(x => x.id === id)
    if (l && states.includes(l.state)) return l
    await new Promise(r => setTimeout(r, 50))
  }
  throw new Error(`${id} did not finish`)
}
const box = { destination: 'me@linux-box:2222' }

test('a build status reads as running, finished, gone, or missing', () => {
  assert.deepEqual(RB.parseStatus('phase=index\ndirs=12\n'), { state: 'running', phase: 'index', exit: null, error: null, dirs: 12, manifest: null })
  const done = RB.parseStatus('phase=done\nexit=0\n__MANIFEST__\n{"index":"/h/i/p","files":3}\n')
  assert.equal(done.state, 'finished'); assert.equal(done.exit, 0); assert.equal(done.manifest.files, 3)
  assert.equal(RB.parseStatus('phase=prepare\nexit=1\nerror=/x is not a folder.\n').error, '/x is not a folder.')
  assert.match(RB.parseStatus('phase=index\nstate=gone\nlog=Killed\n').error, /stopped without finishing: Killed/)
  assert.equal(RB.parseStatus('state=missing\n').state, 'missing')
})

// The status script as a session runs it, on a state folder made here.
// `prelude` is shell put before it, to place an event at a chosen moment.
const statusOf = (home, id, indexDir, prelude = '') => RB.parseStatus(spawnSync('sh', ['-c', `${prelude}\n${RB.BUILD_STATUS}`, '_', id, indexDir],
  { env: { HOME: home, PATH: '/usr/bin:/bin' }, encoding: 'utf8' }).stdout)
const stateDir = (home, id, files) => {
  const s = path.join(home, '.activate-studio', 'builds', id)
  for (const [f, v] of Object.entries(files)) write(path.join(s, f), v)
  return s
}
/** The process id of a process that has ended. */
const deadPid = () => spawnSync('true').pid

test('a build that ends just as its status is read is finished, not stopped', () => {
  const home = fs.mkdtempSync(path.join(dir, 'status-'))
  const idx = path.join(home, 'idx')
  write(path.join(idx, '.studio-index.json'), JSON.stringify({ index: path.join(idx, 'p'), files: 1 }))
  const s = stateDir(home, 'b1', { phase: 'count\n', pid: `${deadPid()}\n`, host: `${os.hostname()}\n` })
  // The run writes its exit status and stops at the moment the script asks
  // whether it is alive: the interleaving a fast build meets by chance.
  const ends = `kill() { [ "$1" = -0 ] && printf '0\\n' > '${s}/exit'; command kill "$@"; }`
  const st = statusOf(home, 'b1', idx, ends)
  assert.equal(st.state, 'finished', JSON.stringify(st))
  assert.equal(st.exit, 0)
  assert.equal(st.manifest.files, 1)
})

test('a build stopped without its exit status is gone on its own machine, and running as seen from another', () => {
  const home = fs.mkdtempSync(path.join(dir, 'status-'))
  const idx = path.join(home, 'idx')
  stateDir(home, 'b2', { phase: 'extract\n', pid: `${deadPid()}\n`, host: `${os.hostname()}\n`, log: 'Killed\n' })
  const gone = statusOf(home, 'b2', idx)
  assert.equal(gone.state, 'gone')
  assert.match(gone.error, /stopped without finishing: Killed/)
  // Another login node behind the same host name has no such process to ask.
  stateDir(home, 'b3', { phase: 'extract\n', pid: `${deadPid()}\n`, host: 'another-login-node\n' })
  assert.equal(statusOf(home, 'b3', idx).state, 'running')
  // A build started before the host was recorded is judged by its process.
  stateDir(home, 'b4', { phase: 'index\n', pid: `${deadPid()}\n` })
  assert.equal(statusOf(home, 'b4', idx).state, 'gone')
})

test('the listing offers building over both sessions, and no workflow on a computer of its own', async () => {
  const r = await listing()
  assert.equal(r.ssh, true)
  assert.equal(r.pw, true)
  assert.equal(r.workflow, false)
})

test('the check reaches the system, finds GUFI, and lists the folders it would make', { skip }, async () => {
  const r = (await post('/api/user-libraries/ssh/build/test', { ...box, sourceRoot: '~/inbox' })).body
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.deepEqual(r.steps.map(s => [s.name, s.ok]), [['ssh', true], ['gufi', true], ['source', true], ['index', true], ['extract', true]])
  assert.match(r.steps[0].detail, /^Connected to me@linux-box on port 2222/)
  const home = homes['me@linux-box']
  assert.equal(r.sourceRoot, `${home}/inbox`)
  assert.match(r.indexDir, new RegExp(`^${home}/\\.activate-studio/indexes/inbox-[0-9a-f]{8}$`))
  // The source folder is made once confirmed; the default index folder is the Studio's own.
  assert.deepEqual(r.create, [`${home}/inbox`])
  assert.match(r.steps[4].detail, /Text is extracted there with Python 3\.\d+/)
  assert.equal(fs.existsSync(`${home}/inbox`), false, 'a check makes nothing')
  // The session is the user's own ssh, on the port written after the host.
  assert.ok(sessions.some(s => s.site === 'ssh://me@linux-box:2222' && s.key === ''))
})

test('the check says plainly when GUFI is not there, and refuses unusable folders', { skip }, async () => {
  const g = (await post('/api/user-libraries/ssh/build/test', { ...box, sourceRoot: '~/project', gufiBin: '/nowhere/bin' })).body
  assert.equal(g.ok, false)
  assert.match(g.steps.find(s => s.name === 'gufi').detail, /^GUFI is not installed on me@linux-box: there is no gufi_dir2index and gufi_query in \/nowhere\/bin\. Install GUFI there/)
  const inside = (await post('/api/user-libraries/ssh/build/test', { ...box, sourceRoot: '~/project', indexRoot: '~/project/idx' })).body
  assert.match(inside.steps.find(s => !s.ok).detail, /inside the folder being indexed/)
  write(path.join(homes['me@linux-box'], 'taken', 'file.txt'), 'x')
  const taken = (await post('/api/user-libraries/ssh/build/test', { ...box, sourceRoot: '~/project', indexRoot: '~/taken' })).body
  assert.match(taken.steps.find(s => !s.ok).detail, /holds files the Studio did not write/)
  const host = (await post('/api/user-libraries/ssh/build/test', { destination: 'me@elsewhere', sourceRoot: '~/x' })).body
  assert.deepEqual(host.steps.map(s => s.name), ['ssh'])
  assert.match(host.steps[0].detail, /does not resolve/)
})

let boxId
test('a build over ssh runs there detached, is followed, and makes a searchable library', { skip }, async () => {
  const refused = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/project', indexRoot: '~/indexes/project' })
  assert.equal(refused.status, 409)
  assert.match(refused.body.error, /do not exist yet: .*\/indexes\/project\. Confirm to create them/)
  // Without an index folder, the Studio's own under ~/.activate-studio: nothing to confirm.
  const r = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/project', label: 'Project' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.state, 'building')
  assert.equal(r.body.built, true)
  assert.equal(r.body.existing, false)
  assert.equal(r.body.port, 2222)
  assert.ok(r.body.job, 'a job follows it')
  boxId = r.body.id
  const l = await waitFor(boxId)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.directories, 3)
  assert.equal(l.files, 3)
  assert.equal(l.fullText, true)
  assert.equal(l.progress, null)
  assert.equal(l.sourceRoot, project)
  assert.match(l.indexRoot, /\.activate-studio\/indexes\/project-[0-9a-f]{8}\/project$/)
  const state = fs.readdirSync(path.join(homes['me@linux-box'], '.activate-studio', 'builds'))
  assert.equal(state.length, 1)
  assert.equal(fs.readFileSync(path.join(homes['me@linux-box'], '.activate-studio', 'builds', state[0], 'exit'), 'utf8').trim(), '0')

  const lib = L.listLibraries('kb', null).find(x => x.id === boxId)
  assert.ok(lib, 'it joins the library list')
  assert.equal(lib.writable, false)
  assert.equal(lib.caps.fullText, true)
  const tree = await U.remoteTree(lib.indexRoot, '')
  assert.deepEqual(tree.map(e => e.name), ['runs', 'readme.md'])
  assert.deepEqual((await G.searchNames('input', 5, lib.indexRoot)).map(h => h.path), ['runs/case-1/input.yaml'])
  assert.deepEqual((await G.searchFts('tunnel', 5, lib.indexRoot)).map(h => h.path), ['readme.md'])
})

test('the details panel describes a folder on the remote system from its index', { skip }, async () => {
  const r = await app.inject({ method: 'GET', url: `/api/kb/details?path=runs&library=${boxId}` })
  const d = r.json()
  assert.equal(r.statusCode, 200, JSON.stringify(d))
  assert.equal(d.type, 'dir')
  assert.equal(d.files, 2)
  assert.equal(d.dirs, 1)
  assert.equal(d.size, 2048 + 10)
  assert.equal(d.types[0].ext, 'dat')
  assert.equal(d.library.kind, 'Built on me@linux-box over ssh')
  assert.match(d.index, /^me@linux-box:\/.*\/project$/)
  const f = (await app.inject({ method: 'GET', url: `/api/kb/details?path=runs/case-1/out.dat&library=${boxId}` })).json()
  assert.equal(f.type, 'file')
  assert.equal(f.size, 2048)

  // The whole index is counted when asked; until then the build's counts.
  const def = (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json().libraries.find(l => l.id === boxId)
  const top = (await app.inject({ method: 'GET', url: `/api/kb/details?path=&library=${boxId}` })).json()
  assert.deepEqual([top.counted, top.files, top.dirs, top.size, top.types], [false, def.files, def.directories - 1, 0, []])
  const counted = (await app.inject({ method: 'GET', url: `/api/kb/details?path=&library=${boxId}&count=1` })).json()
  assert.equal(counted.counted, true)
  assert.ok(counted.size >= 2048 + 10)
  assert.equal(counted.types[0].ext, 'dat')
  const again = (await app.inject({ method: 'GET', url: `/api/kb/details?path=&library=${boxId}` })).json()
  assert.deepEqual(again, counted, 'once counted, it is kept')
})

test('a rebuild replaces the index when complete; one that fails keeps the old index', { skip }, async () => {
  write(path.join(project, 'later.md'), '# later\n')
  const r = await post(`/api/user-libraries/${boxId}/refresh`, {})
  assert.equal(r.status, 200)
  assert.equal(r.body.state, 'updating')
  const l = await waitFor(boxId)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.files, 4)

  fs.renameSync(project, `${project}-moved`)
  await post(`/api/user-libraries/${boxId}/refresh`, {})
  const failed = await waitFor(boxId, ['ready'])
  assert.match(failed.error, /project is not a folder\./)
  assert.equal(failed.files, 4, 'the index it had stays')
  fs.renameSync(`${project}-moved`, project)
})

test('over pw ssh: a system from the dropdown, built with the platform key', { skip }, async () => {
  const t = (await post('/api/user-libraries/pw/build/test', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/work' })).body
  assert.equal(t.ok, true, JSON.stringify(t.steps))
  assert.deepEqual(t.create, [path.join(homes['cluster-a'], 'indexes', 'work')])
  const r = await post('/api/user-libraries/pw/build', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/work', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.transport, 'pw')
  assert.equal(r.body.label, 'work')
  const l = await waitFor(r.body.id)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.files, 1)
  assert.equal(l.indexRoot, path.join(homes['cluster-a'], 'indexes', 'work', 'work'))
  assert.ok(sessions.some(s => s.site === 'cluster-a' && s.key === 'cli-key'))
  // The same index folder cannot be taken twice.
  const again = await post('/api/user-libraries/pw/build', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/work', create: true })
  assert.equal(again.status, 409)
  assert.equal((await post('/api/user-libraries/pw/build/test', { cluster: '-oProxyCommand=x', sourceRoot: '~/w' })).status, 400)
})

test('a build there starts at Meaning on documents, and the check says what the first one sends', { skip }, async () => {
  const t = (await post('/api/user-libraries/pw/build/test', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/plain' })).body
  const plat = spawnSync('python3', ['-c', 'import sys; print("cp%d%d" % sys.version_info[:2])'], { encoding: 'utf8' }).stdout.trim()
  const pinned = Object.keys(JSON.parse(fs.readFileSync(new URL('../../indexer/embed_deps.json', import.meta.url), 'utf8')).wheels).some(k => k.endsWith(plat))
  assert.match(t.steps.find(s => s.name === 'extract').detail, pinned
    ? /At a Meaning depth, embeddings are made with the Studio's model: the first build sends it with its packages for this Python \(about \d+ MB, once\)/
    : /There are no packages of the Studio's embedding model for/)
  const r = await post('/api/user-libraries/pw/build', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/plain', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.depth, 'meaning-documents')
  const stored = JSON.parse(fs.readFileSync(path.join(process.env.INDEX_BASE, 'indexing.json'), 'utf8')).libraries[r.body.id]
  assert.equal(stored.depth, 'meaning-documents', 'stored with it, so a later default leaves it as built')
  const l = await waitFor(r.body.id)
  assert.equal(l.state, 'ready', l.error ?? '')
  // Nothing could be sent or downloaded, and there is no GGUF model there: no vectors, and the build stands.
  assert.equal(l.vectors, false)
  // At Text + OCR, nothing for search by meaning is sent.
  const o = await post('/api/user-libraries/pw/build', { cluster: 'cluster-a', sourceRoot: '~/work', indexRoot: '~/indexes/ocr', depth: 'ocr', create: true })
  assert.equal(o.status, 200, JSON.stringify(o.body))
  assert.equal((await waitFor(o.body.id)).state, 'ready')
  const state = fs.readdirSync(path.join(homes['cluster-a'], '.activate-studio', 'builds')).find(b => b.startsWith(o.body.id))
  for (const f of ['embed.py', 'embed_onnx.py', 'fetch_embed.py']) {
    assert.equal(fs.existsSync(path.join(homes['cluster-a'], '.activate-studio', 'builds', state, f)), false, `${f} is not sent`)
  }
})

test('a build at the Names depth reads nothing from inside the files there', { skip }, async () => {
  const r = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/project', indexRoot: '~/names-only', label: 'Names only', depth: 'names', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.depth, 'names')
  const l = await waitFor(r.body.id)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.files, 4, 'the project, with the file the rebuild test added')
  assert.equal(l.fullText, false, 'no text tables')
  const state = fs.readdirSync(path.join(homes['me@linux-box'], '.activate-studio', 'builds')).find(b => b.startsWith(r.body.id))
  assert.equal(fs.existsSync(path.join(homes['me@linux-box'], '.activate-studio', 'builds', state, 'enrich.py')), false, 'enrich.py is not sent')
  assert.equal((await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/project', indexRoot: '~/other', depth: 'deepest', create: true })).status, 400)
})

test('a read-only folder there (0555 folders, 0444 files) is indexed whole, and rebuilt', { skip: skip || (process.getuid?.() === 0 && 'root writes in any folder') }, async () => {
  // gufi_dir2index gives an index folder its source folder's mode before
  // the folders inside it make theirs; the build there makes those first
  // and runs it again (indexModes.ts says why).
  const archive = path.join(homes['me@linux-box'], 'archive')
  write(path.join(archive, 'top.md'), 'the top file about wombatword\n')
  write(path.join(archive, 'reports', 'deep', 'inner', 'q2.md'), 'second quarter numbatword\n')
  write(path.join(archive, 'scans', 'page.md'), 'a scanned page\n')
  const readOnly = d => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) readOnly(p); else fs.chmodSync(p, 0o444) }
    fs.chmodSync(d, 0o555)
  }
  readOnly(archive)
  test.after(() => spawnSync('chmod', ['-R', 'u+rwx', archive]))
  const r = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/archive', indexRoot: '~/ro-index', label: 'Archive', depth: 'text', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  let l = await waitFor(r.body.id)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.deepEqual([l.directories, l.files, l.fullText], [5, 3, true])
  const lib = L.listLibraries('kb', null).find(x => x.id === r.body.id)
  assert.deepEqual((await G.searchNames('q2', 5, lib.indexRoot)).map(h => h.path), ['reports/deep/inner/q2.md'])
  assert.deepEqual((await G.searchFts('numbatword', 5, lib.indexRoot)).map(h => h.path), ['reports/deep/inner/q2.md'])
  // Its index folders keep the source's group and other bits and are the build's to write.
  assert.equal(fs.statSync(path.join(l.indexRoot, 'reports', 'deep')).mode & 0o777, 0o755)
  // GUFI's record of the source keeps its modes.
  const modes = spawnSync(path.join(gufiBin, 'gufi_query'), ['-d', '|', '-E', 'SELECT mode FROM vrsummary;', l.indexRoot], { encoding: 'utf8' }).stdout.trim().split('\n')
  assert.equal(modes.length, 5)
  for (const m of modes) assert.equal(Number(m) & 0o7777, 0o555)
  // A rebuild moves the old tree aside and removes it.
  await post(`/api/user-libraries/${r.body.id}/refresh`, {})
  l = await waitFor(r.body.id)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.directories, 5)
  assert.equal(fs.existsSync(path.join(homes['me@linux-box'], 'ro-index', '.old')), false)
})

// Search by meaning on another system, with the Studio's ONNX model: sent
// there by the first build, installed once with no network, found by the
// one-pass walk; and the GGUF model's vectors made where it could not be,
// embedded again with it at the next build.
const onnxDir = process.env.STUDIO_TEST_ONNX_DIR || ''
const wheelsDir = process.env.STUDIO_TEST_WHEELS || ''
const ggufFile = process.env.STUDIO_TEST_GGUF || ''
const skipOnnx = skip || (!onnxDir && 'STUDIO_TEST_ONNX_DIR is not set') || (!wheelsDir && 'STUDIO_TEST_WHEELS is not set')
const papers = path.join(homes['me@linux-box'], 'papers')
const embedRoot = path.join(homes['me@linux-box'], '.activate-studio', 'embed')
const vectorModels = root => [...new Set(spawnSync(path.join(gufiBin, 'gufi_query'), ['-d', '|', '-E', 'SELECT model FROM gvecmodel;', root], { encoding: 'utf8' }).stdout.trim().split('\n').filter(Boolean))]
let papersId = null
const RE = await import('../dist/remoteEmbed.js')
const S = await import('../dist/libraryScope.js')
// The query worker the walk's search by meaning starts would keep the test running.
test.after(async () => (await import('../dist/embedWorker.js')).stopQueryWorker())
/** The folder and the Studio's own model, as the app keeps it, from here on. */
const withModel = () => {
  const link = path.join(process.env.INDEX_BASE, 'models', 'minilm-l6')
  if (fs.existsSync(link)) return
  write(path.join(papers, 'turbine.md'), '# Blade cooling\nThe turbine blades are cooled by air bled from the compressor, which keeps the metal below its melting point.\n')
  write(path.join(papers, 'notes', 'garden.md'), '# Tomatoes\nWater the tomato plants in the morning and stake them once they are a foot tall.\n')
  fs.mkdirSync(path.dirname(link), { recursive: true })
  fs.symlinkSync(path.resolve(onnxDir), link)
}

test('without the packages, here or there, a build there embeds with the GGUF model where that is there', { skip: skipOnnx || (!ggufFile && 'STUDIO_TEST_GGUF is not set') }, async () => {
  const gguf = path.join(homes['me@linux-box'], '.activate-studio', 'models', 'minilm384.gguf')
  fs.mkdirSync(path.dirname(gguf), { recursive: true })
  fs.copyFileSync(ggufFile, gguf)
  withModel()
  try {
    const before = RE.embedFilesSent()
    const r = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/papers', indexRoot: '~/papers-index', label: 'Papers', create: true })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    papersId = r.body.id
    const l = await waitFor(papersId)
    assert.equal(l.state, 'ready', l.error ?? '')
    assert.equal(l.vectors, true)
    assert.deepEqual(vectorModels(l.indexRoot), ['minilm384'])
    // The model's own files went, from the Studio's folder; the wheels could be had nowhere.
    assert.equal(RE.embedFilesSent() - before, 2)
    assert.equal(fs.readdirSync(embedRoot).some(n => !n.includes('.')), false, 'nothing installed')
  } finally { fs.rmSync(gguf) }
})

test('the first build there sends the Studio\'s model and packages, installs them once with no network, and the one-pass walk finds its vectors', { skip: skipOnnx, timeout: 300_000 }, async () => {
  withModel()
  // The wheels, as a bundle or a download here keeps them; until now the
  // downloads here found no way out.
  fs.rmSync(path.join(process.env.INDEX_BASE, 'embed-wheels'), { recursive: true, force: true })
  fs.symlinkSync(path.resolve(wheelsDir), path.join(process.env.INDEX_BASE, 'embed-wheels'))
  const before = RE.embedFilesSent()
  let id = papersId
  if (id) {
    const r = await post(`/api/user-libraries/${id}/refresh`, {})
    assert.equal(r.status, 200, JSON.stringify(r.body))
  } else {
    const r = await post('/api/user-libraries/ssh/build', { ...box, sourceRoot: '~/papers', indexRoot: '~/papers-index', label: 'Papers', create: true })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    id = papersId = r.body.id
  }
  const l = await waitFor(id)
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.vectors, true)
  // GGUF vectors from before are embedded again, all of them with the ONNX model.
  assert.deepEqual(vectorModels(l.indexRoot), ['minilm-l6-mean-int8'])
  // Sent what was not there yet: three wheels, and the model's files unless the GGUF test sent them.
  const sent = RE.embedFilesSent() - before
  assert.ok(sent === 3 || sent === 5, `${sent} files sent`)
  const versions = fs.readdirSync(embedRoot)
  const v = versions.find(n => !n.includes('.'))
  assert.ok(v && fs.existsSync(path.join(embedRoot, v, 'ok')), versions.join(' '))
  assert.deepEqual(versions.filter(n => n !== v), [], 'the sent files and the marker are gone once installed')
  assert.ok(fs.existsSync(path.join(embedRoot, v, 'deps', 'onnxruntime')))
  assert.ok(fs.existsSync(path.join(embedRoot, v, 'model', 'model.onnx')))

  // The one-pass walk over the remote index: a question in other words
  // finds the passage by meaning.
  const lib = L.listLibraries('kb', null).find(x => x.id === id)
  const hits = await S.searchScope([lib], 'how is the engine kept from overheating', 10, { semantic: true })
  const hit = hits.find(h => h.path === 'turbine.md')
  assert.ok(hit, JSON.stringify(hits))
  assert.ok((hit.kinds ?? [hit.mode]).includes('vector'), JSON.stringify(hit))

  // The next build sends nothing and uses what is installed.
  const again = RE.embedFilesSent()
  await post(`/api/user-libraries/${id}/refresh`, {})
  const l2 = await waitFor(id)
  assert.equal(l2.state, 'ready', l2.error ?? '')
  assert.equal(RE.embedFilesSent(), again)
  assert.deepEqual(vectorModels(l2.indexRoot), ['minilm-l6-mean-int8'])
})
