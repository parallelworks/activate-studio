// The GUFI commands a request runs, with how each ended and its error
// output: the records Search, Query and the chat's tool blocks show, the
// expected "no such table" lines told apart, the commands a failed request
// ran, a remote command's stderr tail, the optional command log, and the
// chat's tool events, whose commands go to the person who asked and never
// to the model. The tests that run GUFI need it (GUFI_BIN, else
// /opt/gufi/bin) and skip without it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && fs.existsSync(path.join(gufiBin, 'gufi_query'))

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gc-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.STUDIO_DATA_DIR = path.join(dir, 'data')
process.env.STUDIO_GUFI_LOG = '1'
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deploy-key'
process.env.PW_CLI = '/nonexistent/pw'
// Platform identity on, so viewers are told apart; nothing is fetched from this URL.
process.env.AUTH_JWKS_URL = 'https://keys.example.invalid/jwks'
test.after(() => { fs.rmSync(dir, { recursive: true, force: true }); fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }) })

// A small tree, indexed without text: every directory lacks a words table.
const write = (f, s) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, s) }
write(path.join(dir, 'kb', 'notes', 'plan.txt'), 'the storage plan')
write(path.join(dir, 'kb', 'runs', 'a', 'out.log'), 'run a')
write(path.join(dir, 'kb', 'big.dat'), 'x'.repeat(4000))
if (haveGufi) {
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
}

// The model: a query_corpus call, then an answer from its result.
const modelCalls = []
let callSeq = 0
const sseOf = chunks => new Response(chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') + 'data: [DONE]\n\n', { headers: { 'content-type': 'text/event-stream' } })
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  if (u.pathname === '/api/openai/v1/chat/completions') {
    const body = JSON.parse(init.body)
    modelCalls.push(body)
    const last = body.messages.at(-1)
    if (!body.tools || last.role === 'tool') {
      return sseOf([{ model: body.model, choices: [{ index: 0, delta: { content: `The largest files are listed. ${'More words for a full answer. '.repeat(8)}` } }] }, { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }])
    }
    return sseOf([
      { model: body.model, choices: [{ index: 0, delta: { tool_calls: [{ index: 0, id: `call_${++callSeq}`, type: 'function', function: { name: 'query_corpus', arguments: JSON.stringify({ canned: 'largest', n: 3 }) } }] } }] },
      { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
    ])
  }
  return new Response('{}', { status: 404 })
}

const T = await import('../dist/gufiTrace.js')
const L = await import('../dist/gufiLog.js')
const G = await import('../dist/gufi.js')
const R = await import('../dist/remoteExec.js')
const S = await import('../dist/settings.js')
R.setRemoteSpawn((site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'] }))
test.after(() => R.closeRemoteChannels())

const logged = () => (fs.existsSync(L.GUFI_LOG) ? fs.readFileSync(L.GUFI_LOG, 'utf8').trim().split('\n').filter(Boolean).map(l => JSON.parse(l)) : [])

test('error output is split into the expected "no such table" lines and the rest, kept as it came', () => {
  const s = T.splitStderr([
    'Error: no such table: words: /ix/a/db.db',
    'Error: no such table: gvec: /ix/b/db.db: "SELECT 1 FROM gvec;"',
    'Error: no such table: ext_bm25_by_file: /ix/c/db.db',
    'Error: no such table: vrpentries: /ix/d/db.db',
    'Error: no such column: zzz: /ix/e/db.db',
    '',
  ].join('\n'))
  assert.equal(s.expected, 3)
  assert.equal(s.example, 'Error: no such table: words: /ix/a/db.db')
  assert.equal(s.other, 'Error: no such table: vrpentries: /ix/d/db.db\nError: no such column: zzz: /ix/e/db.db', 'a missing index table is not expected')
})

test('a note records how the command ended, and a check is logged but not shown', async () => {
  const t0 = Date.now() - 25
  const { commands } = await T.traced('query', async () => {
    T.noteGufi(['gufi_query', '-E', 'SELECT 1;', '/ix'], { t0, code: 0, stderr: 'Error: no such table: words: /ix/a/db.db\nError: something else\n' })
    T.noteGufi(['gufi_query', '/ix'], { t0, signal: 'SIGKILL', stderr: '' })
    T.noteGufi(['gufi_query', '/ix'], { t0, error: 'the ssh session ended (255)', site: 'cluster-a' })
    T.noteGufi(['gufi_query', '--version'], { t0, code: 0, stderr: '', show: false })
  })
  assert.equal(commands.length, 3)
  const [ok, killed, lost] = commands
  assert.equal(ok.command, "gufi_query -E 'SELECT 1;' /ix")
  assert.equal(ok.exit, 0)
  assert.equal(ok.at, new Date(t0).toISOString())
  assert.ok(ok.ms >= 25)
  assert.equal(ok.stderr, 'Error: something else')
  assert.equal(ok.expected, 1)
  assert.equal(killed.exit, undefined)
  assert.equal(killed.failed, 'ended by SIGKILL')
  assert.equal(lost.failed, 'the ssh session ended (255)')
  assert.equal(lost.stderr, undefined, 'no error output was kept')
  assert.equal(lost.site, 'cluster-a')
  const log = logged().slice(-4)
  assert.deepEqual(log.map(e => e.source), ['query', 'query', 'query', 'query'], 'the check is logged under its request')
  assert.equal(log[3].command, 'gufi_query --version')
  assert.equal(log[2].stderr, null, 'null where the error output is not kept')
})

test('execFile\'s outcome: an exit status, a signal, or why it did not run', () => {
  assert.deepEqual(T.execRun(null, 'warn\n', 5), { t0: 5, code: 0, signal: null, stderr: 'warn\n' })
  assert.equal(T.execRun(Object.assign(new Error('Command failed'), { code: 3 }), '', 5).code, 3)
  assert.equal(T.execRun(Object.assign(new Error('killed'), { code: null, signal: 'SIGTERM', killed: true }), '', 5).signal, 'SIGTERM')
  const missing = T.execRun(Object.assign(new Error('spawn /x/gufi_query ENOENT'), { code: 'ENOENT' }), undefined, 5)
  assert.equal(missing.code, null)
  assert.equal(missing.error, 'spawn /x/gufi_query ENOENT')
  assert.equal(missing.stderr, undefined)
})

test('outside a request the log names the source, and nothing is collected', async () => {
  const before = logged().length
  await T.gufiSource('stats', async () => T.noteGufi(['gufi_query', '/a'], { t0: Date.now(), code: 0, stderr: '' }))
  T.noteGufi(['gufi_query', '/b'], { t0: Date.now(), code: 0, stderr: '', source: 'labels' })
  T.noteGufi(['gufi_query', '/c'], { t0: Date.now(), code: 0, stderr: '' })
  // A trace already running keeps its own source.
  await T.traced('tool', () => T.gufiSource('stats', async () => T.noteGufi(['gufi_query', '/d'], { t0: Date.now(), code: 0 })))
  assert.deepEqual(logged().slice(before).map(e => [e.source, e.command]), [['stats', 'gufi_query /a'], ['labels', 'gufi_query /b'], ['other', 'gufi_query /c'], ['tool', 'gufi_query /d']])
})

test('with the log off nothing is written, and the setting turns it on', async () => {
  await S.updateSettings({ gufiLog: false })
  const before = logged().length
  T.noteGufi(['gufi_query', '/off'], { t0: Date.now(), code: 0 })
  assert.equal(logged().length, before)
  await S.updateSettings({ gufiLog: true })
  T.noteGufi(['gufi_query', '/on'], { t0: Date.now(), code: 0 })
  assert.equal(logged().at(-1).command, 'gufi_query /on')
  assert.equal(L.GUFI_LOG, path.join(dir, 'data', 'logs', 'gufi-commands.log'), 'beside the index log')
})

test('a request that fails still has the commands it ran', async () => {
  const err = await T.traced('query', async () => {
    T.noteGufi(['gufi_query', '-E', 'SELECT zzz;', '/ix'], { t0: Date.now(), code: 0, stderr: 'Error: no such column: zzz: /ix/db.db' })
    throw new Error('no such column: zzz')
  }).catch(e => e)
  assert.equal(T.commandsOf(err)?.[0].stderr, 'Error: no such column: zzz: /ix/db.db')
  assert.ok(!Object.keys(err).includes('gufi'), 'not serialized with the error')
})

test('a remote command keeps the end of its error output, and says so', async () => {
  // 300 expected lines and one other: more than the 4000 bytes the session sends back.
  const script = 'for i in $(seq 1 300); do echo "Error: no such table: words: /idx/d$i/db.db" >&2; done; echo "Error: out of memory" >&2; exit 3'
  const { commands } = await T.traced('search', async () => {
    const r = await T.remoteGufi('cluster-a', 'k', ['bash', '-c', script])
    assert.equal(r.code, 3)
    assert.equal(r.stderrCut, true)
  })
  const [c] = commands
  assert.equal(c.site, 'cluster-a')
  assert.equal(c.exit, 3)
  assert.equal(c.stderr, 'Error: out of memory')
  assert.equal(c.stderrCut, true)
  assert.ok(c.expected > 50 && c.expected < 300, `${c.expected} expected lines in the tail`)
})

test('a local gufi_query: its exit, and the expected lines of a directory without text', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const before = logged().length
  const { result, commands } = await T.traced('search', () => G.queryPerDir('SELECT 1 FROM words LIMIT 1;'))
  assert.deepEqual(result, [])
  const [c] = commands
  assert.match(c.command, /gufi_query -n \d+ -d \$'\\x1e' -E 'SELECT 1 FROM words LIMIT 1;' \S+\/ix\/gufi\/kb$/)
  assert.equal(c.exit, 0)
  assert.equal(c.stderr, '')
  assert.ok(c.expected >= 4, 'one line per directory')
  assert.match(c.expectedExample, /no such table: words/)
  const entry = logged().slice(before).find(e => e.command === c.command)
  assert.equal(entry.source, 'search')
  assert.equal(entry.expected, c.expected)
})

// The chat: a tool's commands ride its result event and its stored call,
// for the viewer who asked.
const { default: Fastify } = await import('fastify')
const { chatRoutes } = await import('../dist/chat/routes.js')
const { conversationRoutes } = await import('../dist/conversations.js')
const app = Fastify()
app.addHook('onRequest', async req => {
  const who = String(req.headers['x-test-user'] ?? '')
  if (who) req.user = { id: `u-${who}`, username: who }
})
await app.register(chatRoutes)
await app.register(conversationRoutes)
await app.ready()
test.after(() => app.close())

const parseSSE = text => text.split('\n\n').filter(b => /^data:/m.test(b)).map(b => {
  let event = 'message', data = ''
  for (const line of b.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data += line.slice(5).trim()
  }
  return { event, data: JSON.parse(data) }
})

test('a chat tool\'s commands go to the person who asked, never to the model', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const headers = who => ({ 'x-test-user': who })
  const c = (await app.inject({ method: 'POST', url: '/api/chat/conversations', headers: headers('alex'), payload: { title: 't' } })).json().id
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', headers: headers('alex'), payload: { model: 'test-model', conversationId: c, userMessageId: 'u1', messages: [{ role: 'user', content: 'Which files are largest?' }] } })
  const result = parseSSE(res.payload).find(e => e.event === 'tool' && e.data.phase === 'result')
  assert.equal(result.data.name, 'query_corpus')
  assert.match(result.data.summary, /^3 rows in \d+ ms$/, 'the summary is the summary alone')
  assert.equal(result.data.gufi.length, 1)
  assert.match(result.data.gufi[0].command, /gufi_query .*ORDER BY size DESC LIMIT 3;/)
  assert.equal(result.data.gufi[0].exit, 0)
  // The model's tool result is the rows, without the command.
  const toolMsg = modelCalls.at(-1).messages.find(m => m.role === 'tool')
  assert.match(toolMsg.content, /big\.dat/)
  assert.doesNotMatch(toolMsg.content, /gufi_query/)
  // Stored with the call, shown to alex; sam, who can open it, sees the call without them.
  const part = who => app.inject({ method: 'GET', url: `/api/chat/conversations/${c}`, headers: headers(who) })
    .then(r => r.json().messages.flatMap(m => m.parts ?? []).find(p => p.kind === 'tool_call'))
  const mine = await part('alex')
  assert.equal(mine.gufi[0].command, result.data.gufi[0].command)
  assert.equal(mine.gufiViewer, undefined, 'the viewer id stays on the server')
  assert.equal(mine.result, result.data.summary)
  const theirs = await part('sam')
  assert.equal(theirs.name, 'query_corpus')
  assert.equal(theirs.gufi, undefined)
  assert.ok(logged().some(e => e.source === 'tool' && e.command === mine.gufi[0].command), 'logged as a tool\'s')
})
