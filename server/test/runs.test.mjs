import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = IX
process.env.STUDIO_RUN_POLL_MS = '15'
// A conversation that launched the run, written before the modules load.
fs.writeFileSync(path.join(IX, 'conversations.json'), JSON.stringify([{ id: 'c1', title: 'hpc', owner: 'me', createdAt: 'x', updatedAt: 'x', activeBranchId: null,
  messages: [{ id: 'u1', role: 'user', content: 'run it', timestamp: 'x' }, { id: 'a1', role: 'assistant', content: 'launched', parentId: 'u1', timestamp: 'x' }] }]))
const runs = await import('../dist/runs.js')
const sleep = ms => new Promise(r => setTimeout(r, ms))

test('a launched run is followed to its end, recorded, and reported into its conversation', async () => {
  let views = 0
  runs.setRunsCli(async args => { assert.deepEqual(args.slice(0, 3), ['workflows', 'runs', 'view']); views++; return JSON.stringify({ slug: args[3], status: views < 3 ? 'running' : 'completed' }) + '\n\nA new version of the PW CLI is available.\n' })
  runs.recordRun({ slug: 'hellobatch-00007', workflow: 'hellobatch', resource: 'vega', conversationId: 'c1', owner: 'me' })
  assert.equal(runs.listRuns()[0].state, 'running')
  for (let i = 0; i < 40 && runs.listRuns()[0].state !== 'completed'; i++) await sleep(15)
  const r = runs.listRuns()[0]
  assert.equal(r.state, 'completed'); assert.ok(r.endedAt)
  const lines = fs.readFileSync(path.join(IX, 'runs.jsonl'), 'utf8').trim().split('\n').map(l => JSON.parse(l))
  assert.deepEqual(lines.map(l => l.t), ['launch', 'end'])
  await sleep(30)
  const conv = JSON.parse(fs.readFileSync(path.join(IX, 'conversations.json'), 'utf8'))[0]
  const note = conv.messages[conv.messages.length - 1]
  assert.equal(note.role, 'assistant')
  assert.equal(note.parentId, 'a1', 'parented to the last message so it sits on the active branch')
  assert.match(note.content, /Run hellobatch-00007 of hellobatch on vega finished: completed/)
  assert.match(note.content, /workflow_run_detail hellobatch-00007/)
})
test('after a restart, runs that had not ended are watched again from the file', async () => {
  runs.resetRunsForTests()
  fs.writeFileSync(path.join(IX, 'runs.jsonl'), JSON.stringify({ t: 'launch', slug: 'x-1', workflow: 'w', launchedAt: '2026-09-05T00:00:00Z' }) + '\n'
    + JSON.stringify({ t: 'launch', slug: 'x-2', workflow: 'w', launchedAt: '2026-09-05T00:00:01Z' }) + '\n'
    + JSON.stringify({ t: 'end', slug: 'x-2', state: 'completed', endedAt: '2026-09-05T00:01:00Z' }) + '\n')
  const fresh = await import('../dist/runs.js?v=2')
  const asked = []
  fresh.setRunsCli(async args => { asked.push(args[3]); return JSON.stringify({ status: 'error' }) })
  assert.equal(fresh.watchRegisteredRuns(), 1, 'only the run without an end event')
  for (let i = 0; i < 40 && fresh.listRuns().find(r => r.slug === 'x-1').state !== 'error'; i++) await sleep(15)
  assert.deepEqual([...new Set(asked)], ['x-1'])
  assert.equal(fresh.listRuns().find(r => r.slug === 'x-1').state, 'error')
  assert.equal(fresh.listRuns().find(r => r.slug === 'x-2').state, 'completed')
})
test('a run the platform keeps failing to report is dropped from watching, not polled forever', async () => {
  const fresh = await import('../dist/runs.js?v=3')
  fresh.resetRunsForTests()
  let n = 0
  fresh.setRunsCli(async () => { n++; throw new Error('Workflow run not found') })
  fresh.recordRun({ slug: 'gone-1', workflow: 'w' })
  await sleep(15 * 40)
  assert.ok(n >= 30 && n <= 34, `stopped after 30 failures (saw ${n})`)
})
