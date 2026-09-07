import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { suggestNext } = await import('../dist/chat/suggest.js')
const conv = (messages) => ({ id: 'c', title: null, createdAt: 'x', updatedAt: 'x', activeBranchId: null, messages })
const asst = (content, parts = []) => ({ id: 'a1', role: 'assistant', content, timestamp: 'x', parts })
const call = (name, args, result, status = 'ok') => ({ kind: 'tool_call', id: 't', name, args: JSON.stringify(args), status, result })

test('after a real launch: follow the run, or show its output', () => {
  const r = suggestNext(conv([{ id: 'u', role: 'user', content: 'run it', timestamp: 'x' }, asst('Launched.', [call('run_workflow', { name: 'activatebatch', dry_run: false }, '{"run":{"slug":"activatebatch-00013"}}')])]))
  assert.equal(r.after, 'a1')
  assert.deepEqual(r.suggestions.map(s => s.text), ['Follow run activatebatch-00013 until it finishes and tell me the outcome', 'Show me the output of run activatebatch-00013'])
})
test('a dry run suggests the real launch; a completed watch suggests summarizing; a failure suggests the fix', () => {
  assert.equal(suggestNext(conv([asst('Validated.', [call('run_workflow', { name: 'hellobatch', dry_run: true }, 'ok')])])).suggestions[0].text, 'Launch hellobatch for real now')
  assert.match(suggestNext(conv([asst('Done.', [call('watch_run', { run: 'x-00001' }, 'x-00001: completed')])])).suggestions[0].text, /Summarize the results of x-00001/)
  assert.match(suggestNext(conv([asst('It failed.', [call('workflow_run_detail', { run: 'x-00002' }, 'Run x-00002 of w: error')])])).suggestions[0].text, /Explain why x-00002 failed/)
})
test('a reply that asks the user something offers nothing', () => {
  assert.deepEqual(suggestNext(conv([asst('Which partition should I use?', [call('hpc_environments', { cluster: 'vega' }, '...')])])).suggestions, [])
  assert.deepEqual(suggestNext(conv([asst('Status.\nNEEDS DECISION: proceed?', [])])).suggestions, [])
})
test('a search that cited a file suggests opening it; a long plain answer suggests sources; nothing when the user spoke last', () => {
  const cited = asst('See [proposals/x.md](#open=file:proposals/x.md) for detail.', [call('search_kb', { query: 'x' }, 'hits')])
  assert.equal(suggestNext(conv([cited])).suggestions[0].text, 'Open proposals/x.md and summarize it')
  const long = asst('a'.repeat(500), [])
  assert.match(suggestNext(conv([long])).suggestions[0].text, /sources behind that/)
  assert.deepEqual(suggestNext(conv([cited, { id: 'u2', role: 'user', content: 'ok', timestamp: 'x' }])).suggestions, [])
  assert.equal(suggestNext(conv([])).after, null)
})
