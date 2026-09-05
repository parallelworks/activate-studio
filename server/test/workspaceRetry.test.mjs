import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { withWorkspace, isWorkspaceDown } = await import('../dist/workspace.js')

const DOWN = new Error('2026-09-05T04:37:10Z [ERROR] User workspace not found or is not running')
const runner = (log = []) => ({ cli: async args => { log.push(args.join(' ')); return '' }, sleep: async () => {}, budgetMs: 5_000, retryEveryMs: 1, log: m => log.push(`log: ${m}`) })

test('a launch that fails because the workspace is down starts it and retries until accepted', async () => {
  const log = []
  let attempts = 0
  const launch = async () => { attempts++; if (attempts < 3) throw DOWN; return 'run-00042' }
  const r = await withWorkspace(runner(log), launch)
  assert.equal(r.value, 'run-00042')
  assert.equal(r.started, true)
  assert.equal(attempts, 3)
  assert.equal(log.filter(l => l === 'workspace start').length, 1, 'exactly one start request')
})
test('a healthy launch never touches the workspace', async () => {
  const log = []
  const r = await withWorkspace(runner(log), async () => 'ok')
  assert.deepEqual([r.value, r.started, log], ['ok', false, []])
})
test('any other failure is rethrown at once, before and after a start', async () => {
  await assert.rejects(withWorkspace(runner(), async () => { throw new Error('Missing required fields: SLURM account') }), /Missing required fields/)
  let n = 0
  await assert.rejects(withWorkspace(runner(), async () => { n++; if (n === 1) throw DOWN; throw new Error('boom') }), /boom/)
})
test('when the workspace never comes up the error says what to do', async () => {
  const r = { ...runner(), budgetMs: 3, retryEveryMs: 1, sleep: ms => new Promise(res => setTimeout(res, ms)) }
  await assert.rejects(withWorkspace(r, async () => { throw DOWN }), /still not running after a start request .*pw workspace start/)
})
test('both wordings the platform uses are recognized', () => {
  assert.equal(isWorkspaceDown('User workspace not found or is not running'), true)
  assert.equal(isWorkspaceDown('[ERROR] Could not execute workflow'), true)
  assert.equal(isWorkspaceDown('Workflow run not found'), false)
})
