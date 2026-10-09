// The assistant's workflow tools on GitHub and marketplace entries: listed
// with the curated set, browsed by repository, read with their declared
// permissions, and run only with trust once the user has approved.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gt-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'gt-ix-'))
const DOE = 'github.com/example-org/components/workflows/doe/yamls/general.yaml@canary'
process.env.STUDIO_WORKFLOWS = `activatebatch,marketplace/jupyter,${DOE}`
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'gt-bin-'))
const calls = path.join(bin, 'calls.log')
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const a = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(a) + '\\n')
if (a[0] === 'workflows' && a[1] === 'ls') { process.stdout.write(JSON.stringify([{ name: 'activatebatch', displayName: 'Batch', description: 'Runs commands', tags: [] }, { name: 'other', displayName: 'Other' }])); process.exit(0) }
if (a[0] === 'workflows' && a[1] === 'run') { process.stdout.write(JSON.stringify({ run: { slug: 'general-00002', status: 'running' } })); process.exit(0) }
process.stdout.write('{}')
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const YAML = "permissions:\n  - '*'\non:\n  execute:\n    inputs:\n      n_cases:\n        type: number\n        default: 8\njobs:\n  sample:\n    steps:\n      - run: echo hi\n  report:\n    needs: [sample]\n    steps:\n      - run: echo done\n"
const RAW = 'https://raw.githubusercontent.com/example-org/components/canary/'
globalThis.fetch = async (url) => {
  const u = String(url)
  if (u.startsWith('https://api.github.com/repos/example-org/components/git/trees/canary')) {
    return new Response(JSON.stringify({ tree: ['workflows/doe/README.md', 'workflows/doe/yamls/general.yaml'].map(p => ({ path: p, type: 'blob' })) }), { status: 200 })
  }
  if (u === `${RAW}workflows/doe/README.md`) return new Response('# DOE: Design of Experiments\n\nSpreads designs over bounds.\n', { status: 200 })
  if (u === `${RAW}workflows/doe/yamls/general.yaml`) return new Response(YAML, { status: 200 })
  return new Response('nope', { status: 404 })
}

const { executeTool, TOOL_SPECS } = await import('../dist/chat/tools.js')
const runs = () => fs.existsSync(calls) ? fs.readFileSync(calls, 'utf8').trim().split('\n').map(l => JSON.parse(l)).filter(a => a[1] === 'run') : []

test('list_github_workflows is a tool and lists a repository directory', async () => {
  assert.ok(TOOL_SPECS.some(t => t.function.name === 'list_github_workflows'))
  const r = await executeTool('list_github_workflows', JSON.stringify({ repo: 'github.com/example-org/components/workflows@canary' }))
  assert.deepEqual(JSON.parse(r.result), [{ name: DOE, title: 'DOE: Design of Experiments', summary: 'Spreads designs over bounds.' }])
  const bad = await executeTool('list_github_workflows', JSON.stringify({ repo: 'marketplace/jupyter' }))
  assert.equal(bad.summary, 'error')
})

test('list_workflows puts the curated set first, GitHub and marketplace entries included', async () => {
  const r = await executeTool('list_workflows', '{}')
  const rows = JSON.parse(r.result)
  assert.deepEqual(rows.filter(w => w.offeredHere).map(w => [w.name, w.displayName]), [
    ['activatebatch', 'Batch'], ['marketplace/jupyter', 'jupyter'], [DOE, 'DOE: Design of Experiments'],
  ])
  assert.equal(rows.at(-1).name, 'other')
})

test('get_workflow reads a GitHub workflow and shows the access it asks for', async () => {
  const r = await executeTool('get_workflow', JSON.stringify({ name: DOE }))
  const doc = JSON.parse(r.result)
  assert.deepEqual(doc.dag.jobs, ['sample', 'report'])
  assert.deepEqual(doc.dag.edges, ['sample -> report'])
  assert.deepEqual(doc.permissions, ['*'])
})

test('run_workflow asks for approval before granting access, then runs the file with --trust', async () => {
  const before = runs().length
  const held = await executeTool('run_workflow', JSON.stringify({ name: DOE, dry_run: false, inputs: '{"n_cases":4}' }))
  assert.match(held.summary, /approval/)
  assert.match(held.result, /Not run\..*account variables: \*/s)
  assert.equal(runs().length, before, 'nothing reaches the platform before approval')

  const r = await executeTool('run_workflow', JSON.stringify({ name: DOE, dry_run: false, inputs: '{"n_cases":4}', trust: true }))
  assert.equal(r.summary, 'run submitted')
  const run = runs().at(-1)
  assert.ok(run.includes('--trust'))
  assert.ok(run.includes(DOE), 'the @ref survives')
})
