// First-run setup on a single-user install: the status the wizard reads,
// the folder browser, saving a new primary folder or port through the
// launcher, a whole-folder index build through GUFI, and the record that
// setup is done.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const HOME = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-home-'))
const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-ix-'))
const OTHER = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-other-'))
process.env.HOME = HOME
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
const GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
process.env.GUFI_BIN = GUFI
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'STUDIO_APP_BUNDLE', 'PW_PLATFORM_HOST', 'PORT', 'HOST']) delete process.env[k]

fs.mkdirSync(path.join(HOME, 'beta'))
fs.mkdirSync(path.join(HOME, 'Alpha'))
fs.mkdirSync(path.join(HOME, '.hidden'))
fs.writeFileSync(path.join(HOME, 'file.txt'), 'not a folder')

fs.writeFileSync(path.join(KB, 'README.md'), '# top level readme\n')
fs.mkdirSync(path.join(KB, 'notes'))
fs.writeFileSync(path.join(KB, 'notes', 'alpha.md'), 'The quick brown fox met a zebracorn in the field.\n')
fs.mkdirSync(path.join(KB, 'docs', 'sub'), { recursive: true })
fs.writeFileSync(path.join(KB, 'docs', 'sub', 'beta.txt'), 'nested text file\n')

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { setupRoutes } = await import('../dist/setup.js')
const L = await import('../dist/lifecycle.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(setupRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('status: a single-user install without setupDone is a first run', async () => {
  const r = await j('GET', '/api/setup/status')
  assert.equal(r.status, 200)
  assert.equal(r.body.firstRun, true)
  assert.equal(r.body.standalone, true)
  assert.equal(r.body.canRestart, false)
  assert.equal(r.body.primary.path, KB)
  assert.equal(r.body.primary.writable, true)
  assert.equal(r.body.primary.indexed, false)
  assert.equal(r.body.gufi.bin, GUFI)
  assert.equal(r.body.embedModel.found, false)
  assert.equal(r.body.libraries[0].id, 'kb')
  // A Studio the platform deploys is configured there.
  process.env.PW_PLATFORM_HOST = 'platform.test'
  assert.equal((await j('GET', '/api/setup/status')).body.firstRun, false)
  delete process.env.PW_PLATFORM_HOST
})

test('browse lists folders only, sorted, without dot-folders, and refuses what is not there', async () => {
  const r = await j('GET', '/api/setup/browse')
  assert.equal(r.status, 200)
  assert.equal(r.body.path, HOME)
  assert.equal(r.body.parent, path.dirname(HOME))
  assert.equal(r.body.writable, true)
  assert.deepEqual(r.body.dirs, [{ name: 'Alpha', path: path.join(HOME, 'Alpha') }, { name: 'beta', path: path.join(HOME, 'beta') }])
  const tilde = await j('GET', `/api/setup/browse?path=${encodeURIComponent('~/Alpha')}`)
  assert.equal(tilde.body.path, path.join(HOME, 'Alpha'))
  assert.deepEqual(tilde.body.dirs, [])
  assert.equal((await j('GET', `/api/setup/browse?path=${encodeURIComponent(path.join(HOME, 'nope'))}`)).status, 404)
  assert.equal((await j('GET', `/api/setup/browse?path=${encodeURIComponent(path.join(HOME, 'file.txt'))}`)).status, 400)
  assert.equal((await j('GET', '/api/setup/browse?path=/')).body.parent, null)
})

test('primary folder: validated, and saved only where a launcher can carry it', async () => {
  assert.equal((await j('POST', '/api/setup/primary-folder', {})).status, 400)
  assert.equal((await j('POST', '/api/setup/primary-folder', { path: path.join(HOME, 'nope') })).status, 400)
  assert.equal((await j('POST', '/api/setup/primary-folder', { path: path.join(HOME, 'file.txt') })).status, 400)
  // A folder that holds the index would index the index.
  const holder = await j('POST', '/api/setup/primary-folder', { path: path.dirname(IX) })
  assert.equal(holder.status, 400)
  assert.match(holder.body.error, /own index/)
  const same = await j('POST', '/api/setup/primary-folder', { path: KB })
  assert.deepEqual([same.body.saved, same.body.restarting], [true, false])

  const none = await j('POST', '/api/setup/primary-folder', { path: OTHER })
  assert.equal(none.status, 200)
  assert.equal(none.body.saved, false)
  assert.equal(none.body.restarting, false)
  assert.match(none.body.note, new RegExp(`KB_ROOT=${OTHER}`))

  const conf = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'setup-conf-')), 'studio.env')
  process.env.STUDIO_CONFIG_FILE = conf
  const noRestart = await j('POST', '/api/setup/primary-folder', { path: '~/beta' })
  assert.deepEqual([noRestart.body.saved, noRestart.body.restarting], [true, false])
  assert.match(noRestart.body.note, /Restart/)
  assert.equal(L.readLauncherSettings().KB_ROOT, path.join(HOME, 'beta'))

  process.env.STUDIO_RESTARTABLE = '1'
  let code = null
  L.setStopper(c => { code = c })
  assert.equal((await j('GET', '/api/setup/status')).body.canRestart, true)
  // The macOS app names its bundle, for Settings, Server's uninstall line.
  assert.equal((await j('GET', '/api/setup/status')).body.macApp, null)
  process.env.STUDIO_APP_BUNDLE = '/Applications/ACTIVATE Studio.app'
  assert.equal((await j('GET', '/api/setup/status')).body.macApp, '/Applications/ACTIVATE Studio.app')
  delete process.env.STUDIO_APP_BUNDLE
  const restart = await j('POST', '/api/setup/primary-folder', { path: OTHER })
  assert.deepEqual(restart.body, { saved: true, restarting: true })
  assert.equal(L.readLauncherSettings().KB_ROOT, OTHER)
  await new Promise(r => setTimeout(r, 400))
  assert.equal(code, L.RESTART_EXIT_CODE)
  delete process.env.STUDIO_CONFIG_FILE
  delete process.env.STUDIO_RESTARTABLE
})

test('port: validated, saved for the launcher, refused when taken, and applied by a restart', async () => {
  const net = await import('node:net')
  const listen = (port, host = '127.0.0.1') => new Promise((resolve, reject) => {
    const srv = net.createServer().once('error', reject)
    srv.listen({ port, host }, () => resolve(srv))
  })
  // A port nothing listens on, and one something does.
  const probe = await listen(0)
  const free = probe.address().port
  await new Promise(r => probe.close(r))
  const taken = await listen(0)
  const busy = taken.address().port
  // A program on every address holds the port at 127.0.0.1 too, which a
  // bind alone does not show on macOS.
  const { portFree } = await import('../dist/setup.js')
  const wide = await listen(0, '0.0.0.0')
  assert.equal(await portFree(wide.address().port, '127.0.0.1'), false)
  assert.equal(await portFree(busy, '0.0.0.0'), false)
  assert.equal(await portFree(free, '127.0.0.1'), true)
  await new Promise(r => wide.close(r))

  const status = (await j('GET', '/api/setup/status')).body
  assert.equal(status.port, 4080)
  assert.equal(status.portNext, null)
  for (const port of [undefined, 'x', 80, 70000, 4180.5]) {
    assert.equal((await j('POST', '/api/setup/port', { port })).status, 400, String(port))
  }
  assert.match((await j('POST', '/api/setup/port', { port: 4080 })).body.note, /already uses/)
  // Without a launcher the variable to set, for the Studio and its MCP endpoint alike.
  const none = await j('POST', '/api/setup/port', { port: free })
  assert.deepEqual([none.body.saved, none.body.restarting], [false, false])
  assert.match(none.body.note, new RegExp(`PORT=${free}`))

  const conf = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'setup-port-')), 'studio.env')
  process.env.STUDIO_CONFIG_FILE = conf
  try {
    const inUse = await j('POST', '/api/setup/port', { port: busy })
    assert.equal(inUse.status, 409)
    assert.match(inUse.body.error, new RegExp(`port ${busy} is in use`))
    assert.equal(L.readLauncherSettings().PORT, undefined)
    const saved = await j('POST', '/api/setup/port', { port: free })
    assert.deepEqual([saved.body.saved, saved.body.restarting], [true, false])
    assert.match(saved.body.note, /Restart/)
    assert.equal(L.readLauncherSettings().PORT, String(free))
    assert.equal((await j('GET', '/api/setup/status')).body.portNext, free)
    // Choosing the port in use again takes the saved one back.
    await j('POST', '/api/setup/port', { port: 4080 })
    assert.equal(L.readLauncherSettings().PORT, '4080')
    assert.equal((await j('GET', '/api/setup/status')).body.portNext, null)

    process.env.STUDIO_RESTARTABLE = '1'
    let code = null
    L.setStopper(c => { code = c })
    const restart = await j('POST', '/api/setup/port', { port: free })
    assert.deepEqual(restart.body, { saved: true, restarting: true, port: free })
    assert.equal(L.readLauncherSettings().PORT, String(free))
    await new Promise(r => setTimeout(r, 400))
    assert.equal(code, L.RESTART_EXIT_CODE)
  } finally {
    delete process.env.STUDIO_CONFIG_FILE
    delete process.env.STUDIO_RESTARTABLE
    await new Promise(r => taken.close(r))
  }
})

const noGufi = !fs.existsSync(path.join(GUFI, 'gufi_dir2index')) && `no GUFI at ${GUFI}`

test('the setup index covers the whole folder and makes it searchable', { skip: noGufi, timeout: 120_000 }, async () => {
  // Left over from another folder: a cache entry, and an index tree for a
  // directory that is not here.
  fs.writeFileSync(path.join(IX, 'indexed-root'), '/somewhere/else\n')
  fs.mkdirSync(path.join(IX, 'extract'), { recursive: true })
  fs.writeFileSync(path.join(IX, 'extract', 'stale.pdf.txt'), 'old text')
  const gone = path.join(IX, 'gufi', path.basename(KB), 'gone')
  fs.mkdirSync(gone, { recursive: true })

  const start = await j('POST', '/api/setup/index')
  assert.equal(start.status, 200)
  // A second request while it runs follows the same job.
  assert.equal((await j('POST', '/api/setup/index')).body.id, start.body.id)
  let job = start.body
  const until = Date.now() + 100_000
  while (job.state !== 'done' && job.state !== 'error' && Date.now() < until) {
    await new Promise(r => setTimeout(r, 200))
    job = (await j('GET', `/api/index/job/${start.body.id}`)).body
  }
  assert.equal(job.state, 'done', job.error ?? '')
  assert.equal(job.kind, 'build')
  assert.deepEqual(job.progress.failed, [])
  assert.equal(job.progress.phase, 'done')
  // Each phase with its own count: four folders named, their text read,
  // and no embeddings without the model.
  const phase = id => job.phases.find(p => p.id === id)
  assert.equal(phase('names').state, 'done')
  assert.equal(phase('names').total, 4)
  assert.equal(phase('names').done, 4)
  assert.equal(phase('text').state, 'done')
  assert.equal(phase('text').done, phase('text').total)
  assert.equal(phase('meaning').state, 'skipped')
  assert.ok(job.searchableAt, 'the names were put in place')
  assert.ok(job.ms >= 0)
  // The status every view reads says the same, and keeps the timings.
  const st = (await j('GET', '/api/index/status')).body
  assert.equal(st.running, false)
  assert.equal(st.job, null)
  assert.equal(st.indexed, true)
  assert.equal(st.lastBuild.id, job.id)
  assert.equal(st.lastBuild.state, 'done')
  assert.equal(st.incomplete, false)

  // The other folder's cache went aside with its index, not into this one's.
  assert.equal(fs.existsSync(path.join(IX, 'extract', 'stale.pdf.txt')), false)
  assert.equal(fs.existsSync(gone), false)
  assert.equal(fs.readFileSync(path.join(IX, 'indexed-root'), 'utf8').trim(), KB)
  const sweep = JSON.parse(fs.readFileSync(path.join(IX, 'sweep-state.json'), 'utf8'))
  assert.deepEqual(Object.keys(sweep).sort(), ['.', 'docs', 'docs/sub', 'notes'])

  const status = await j('GET', '/api/setup/status?files=1')
  assert.equal(status.body.primary.indexed, true)
  assert.equal(status.body.primary.files, 3)
  assert.equal(status.body.job, null)

  const hits = await j('GET', '/api/search?q=zebracorn')
  assert.equal(hits.status, 200)
  assert.ok(hits.body.hits.some(h => h.path === 'notes/alpha.md'), JSON.stringify(hits.body))
  const nested = await j('GET', '/api/search?q=beta.txt')
  assert.ok(nested.body.hits.some(h => h.path === 'docs/sub/beta.txt'), JSON.stringify(nested.body))
})

test('a new computer of its own starts with AI off, Workflows hidden, its own mission and ten questions', async () => {
  const { effectiveSettings, updateSettings, settingsRoutes, PERSONAL_PROMPTS, PERSONAL_MISSION, PERSONAL_SECTIONS } = await import('../dist/settings.js')
  const { default: Fastify } = await import('fastify')
  const sapp = Fastify()
  await sapp.register(settingsRoutes)
  await sapp.ready()
  const put = async body => { const r = await sapp.inject({ method: 'PUT', url: '/api/settings', payload: body }); return { status: r.statusCode, body: r.json() } }
  const saved = process.env.AI_ENABLED
  delete process.env.AI_ENABLED
  try {
    let e = effectiveSettings()
    assert.equal(e.aiEnabled, false)
    assert.deepEqual(e.sections, PERSONAL_SECTIONS)
    assert.ok(!e.sections.includes('workflows'))
    assert.equal(PERSONAL_PROMPTS.length, 10)
    assert.deepEqual(e.suggestedPrompts, PERSONAL_PROMPTS)
    assert.equal(e.kbMission, PERSONAL_MISSION)
    // Saved by an earlier version: a blank mission and a deployment's three questions are no choice.
    await updateSettings({ kbMission: '  ', suggestedPrompts: ['What tools can you use and what can I ask you to do?', 'What is in this knowledge base?', 'Which workflows are available to run?'] })
    e = effectiveSettings()
    assert.equal(e.kbMission, PERSONAL_MISSION)
    assert.deepEqual(e.suggestedPrompts, PERSONAL_PROMPTS)
    // Saving the defaults as they are keeps them defaults; a real choice is kept.
    let r = await put({ suggestedPrompts: PERSONAL_PROMPTS, kbMission: PERSONAL_MISSION })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    let file = JSON.parse(fs.readFileSync(path.join(IX, 'settings.json'), 'utf8'))
    assert.equal(file.suggestedPrompts, undefined)
    assert.equal(file.kbMission, undefined)
    r = await put({ suggestedPrompts: ['Only this one'], kbMission: 'My lab notes.' })
    file = JSON.parse(fs.readFileSync(path.join(IX, 'settings.json'), 'utf8'))
    assert.deepEqual(file.suggestedPrompts, ['Only this one'])
    assert.equal(file.kbMission, 'My lab notes.')
    await updateSettings({ suggestedPrompts: undefined, kbMission: undefined })
    await sapp.close()
  } finally { if (saved !== undefined) process.env.AI_ENABLED = saved }
})

test('complete records setupDone, keeps what setup showed, and the next status is not a first run', async () => {
  const saved = process.env.AI_ENABLED
  delete process.env.AI_ENABLED
  try {
    const r = await j('POST', '/api/setup/complete')
    assert.deepEqual(r.body, { setupDone: true })
    const file = JSON.parse(fs.readFileSync(path.join(IX, 'settings.json'), 'utf8'))
    assert.equal(file.setupDone, true)
    // Still the defaults when setup ended, so saved as they were shown.
    assert.equal(file.aiEnabled, false)
    assert.ok(Array.isArray(file.sections) && !file.sections.includes('workflows'))
    assert.equal((await j('GET', '/api/setup/status')).body.firstRun, false)
  } finally { if (saved !== undefined) process.env.AI_ENABLED = saved }
})
