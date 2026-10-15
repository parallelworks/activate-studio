// Only the tool groups a reply needs go to the model: the knowledge base
// tools always, workflows, agents, and editing when the question or the
// conversation calls for them, and more_tools to add the rest.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'tg-kb-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'tg-ix-'))
const { initialToolGroups, selectToolSpecs, toolGroupOf, TOOL_SPECS, MORE_TOOLS, executeTool } = await import('../dist/chat/tools.js')
const names = specs => specs.map(t => t.function.name)

test('a question about documents gets the knowledge base tools and more_tools, not the platform', () => {
  const groups = initialToolGroups('Which proposals mention cybersecurity? Summarize them.')
  assert.equal(groups.size, 0)
  const sent = names(selectToolSpecs(TOOL_SPECS, groups))
  for (const n of ['search_kb', 'read_kb_file', 'list_kb_dir', MORE_TOOLS]) assert.ok(sent.includes(n), n)
  for (const n of ['run_workflow', 'start_agent', 'write_kb_file']) assert.ok(!sent.includes(n), n)
  assert.ok(sent.length < TOOL_SPECS.length / 2)
})

test('the question, or tools used earlier in the conversation, bring their groups', () => {
  assert.ok(initialToolGroups('Launch the DOE workflow on the cluster').has('workflows'))
  assert.ok(initialToolGroups('What applications does my ACTIVATE account offer?').has('workflows'))
  assert.ok(initialToolGroups('Delegate this to two agents').has('agents'))
  assert.ok(initialToolGroups('Label these files as draft').has('editing'))
  assert.ok(initialToolGroups('And what about the second one?', ['watch_run']).has('workflows'))
  const all = names(selectToolSpecs(TOOL_SPECS, new Set(['workflows', 'agents', 'editing'])))
  assert.ok(!all.includes(MORE_TOOLS), 'nothing left to add')
  assert.equal(toolGroupOf('run_workflow'), 'workflows')
  assert.equal(toolGroupOf('search_kb'), null)
})

test('read_kb_file reads several files in one call, each under its own heading', async () => {
  fs.writeFileSync(path.join(kb, 'a.md'), 'alpha body')
  fs.writeFileSync(path.join(kb, 'b.md'), 'bravo body')
  const out = await executeTool('read_kb_file', JSON.stringify({ paths: ['a.md', 'b.md', 'missing.md'] }), {})
  assert.match(out.result, /### a\.md\nalpha body/)
  assert.match(out.result, /### b\.md\nbravo body/)
  assert.match(out.result, /### missing\.md\nnot read/)
  assert.equal(out.summary, '3 files')
})
