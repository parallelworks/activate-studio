import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = IX
const { usageOf } = await import('../dist/tasks.js')
const { seedExtensions, personas } = await import('../dist/extensions.js')
const { personaCatalog } = await import('../dist/chat/context.js')
const { streamTurn } = await import('../dist/chat/gateway.js')

test('usage is read from every spelling pw code and providers use, and is null when absent', () => {
  assert.deepEqual(usageOf({ usage: { prompt_tokens: 120, completion_tokens: 30, total_tokens: 150 }, total_cost_usd: 0.004 }), { input: 120, output: 30, total: 150, cost: 0.004 })
  assert.deepEqual(usageOf({ input_tokens: 10, output_tokens: 5 }), { input: 10, output: 5, total: 15, cost: null })
  assert.deepEqual(usageOf({ result: 'done' }), null)
  assert.deepEqual(usageOf('plain text'), null)
})

test('the starter set seeds once, fills in later additions, and never resurrects a deletion', () => {
  const ext = path.join(IX, 'extensions')
  seedExtensions()
  assert.ok(fs.existsSync(path.join(ext, 'agents', 'campaign_runner.md')), 'default personas arrive on first seed')
  const names = personas().map(p => p.name)
  for (const n of ['campaign_runner', 'watcher', 'reviewer', 'steward', 'reporter']) assert.ok(names.includes(n), n)
  fs.unlinkSync(path.join(ext, 'agents', 'reporter.md'))
  seedExtensions()
  assert.equal(fs.existsSync(path.join(ext, 'agents', 'reporter.md')), false, 'a deleted default stays deleted')
  fs.writeFileSync(path.join(ext, 'agents', 'watcher.md'), '---\nname: watcher\ndescription: mine\n---\nmy rules\n')
  seedExtensions()
  assert.match(fs.readFileSync(path.join(ext, 'agents', 'watcher.md'), 'utf8'), /my rules/, 'an edited default is never overwritten')
  const ledger = JSON.parse(fs.readFileSync(path.join(ext, '.seeded.json'), 'utf8'))
  assert.ok(ledger.includes('agents/reporter.md'))
})

test('the prompt carries a live persona catalog', () => {
  const c = personaCatalog()
  assert.match(c, /^Persona catalog: /)
  assert.match(c, /campaign_runner \(Expands a study/)
  assert.doesNotMatch(c, /reporter/, 'the deleted one is gone from the catalog too')
})

test('a stream that ends with a usage chunk yields usage on the turn', async () => {
  const sse = [
    'data: {"choices":[{"delta":{"content":"ok"}}]}',
    'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}',
    'data: {"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":2,"total_tokens":42}}',
    'data: [DONE]', '',
  ].join('\n\n')
  globalThis.fetch = async () => new Response(sse, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  const t = await streamTurn({ model: 'm', messages: [], stream: true }, null, { onContent: () => {} }, undefined, 'k')
  assert.equal(t.content, 'ok')
  assert.deepEqual(t.usage, { prompt_tokens: 40, completion_tokens: 2, total_tokens: 42 })
})

test('usage adds across turns and a task sums its agents', async () => {
  const { addUsage } = await import('../dist/chat/routes.js')
  assert.deepEqual(addUsage({}, { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 }), { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 })
  assert.deepEqual(addUsage({ prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 }, { prompt_tokens: 1, completion_tokens: 1 }), { prompt_tokens: 11, completion_tokens: 6, total_tokens: 17 })
  const { sumUsage } = await import('../dist/tasks.js')
  const nodes = new Map([['a', { usage: { input: 1, output: 2, total: 3, cost: 0.1 } }], ['b', { usage: null }], ['c', { usage: { input: 10, output: 20, total: 30, cost: null } }]])
  assert.deepEqual(sumUsage({ nodes }), { input: 11, output: 22, total: 33, cost: 0.1 })
  assert.equal(sumUsage({ nodes: new Map([['x', {}]]) }), null)
})
