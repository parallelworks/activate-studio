import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'bin-'))
const calls = path.join(bin, 'calls.log')
const WF = { name: 'hellobatch', yaml: { on: { execute: { inputs: {
  resource: { type: 'compute-clusters' }, scheduler: { type: 'boolean', default: false },
  slurm: { type: 'group', items: { partition: { type: 'slurm-partitions' }, time: { type: 'string', default: '00:30:00' } } },
} } }, jobs: { j: {} } }, configurations: [] }
const ENVS = [
  { clusterName: 'vega', clusterUser: 'me', schedulerType: 'slurm', partitionName: 'gpu', partitionState: 'up', availNodes: 2, totalNodes: 8, maxTime: '1-00:00:00' },
  { clusterName: 'vega', clusterUser: 'me', schedulerType: 'slurm', partitionName: 'cpu', partitionState: 'up', availNodes: 20, totalNodes: 24, maxTime: '7-00:00:00' },
  { clusterName: 'vega', clusterUser: 'me', schedulerType: 'slurm', partitionName: 'maint', partitionState: 'down', availNodes: 99, totalNodes: 99, maxTime: '1:00:00' },
]
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const a = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(a) + '\\n')
const sub = a.slice(0, 2).join(' ')
if (a[0] === 'environments') process.stdout.write(JSON.stringify(${JSON.stringify(ENVS)}))
else if (sub === 'workflows get') process.stdout.write(JSON.stringify(${JSON.stringify(WF)}))
else if (sub === 'workflows run') process.stdout.write(JSON.stringify({ run: { slug: 'hellobatch-00004', status: 'running' } }))
else process.stdout.write('{}')
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')
const { executeTool } = await import('../dist/chat/tools.js')
const sent = () => fs.readFileSync(calls, 'utf8').trim().split('\n').map(l => JSON.parse(l)).filter(a => a[0] === 'workflows' && a[1] === 'run').pop()
const inputsOf = a => JSON.parse(a[a.indexOf('-i') + 1])

test('a scheduler submission with no partition gets the busiest-free up partition, and says so', async () => {
  const r = await executeTool('run_workflow', JSON.stringify({ name: 'hellobatch', resource: 'vega', dry_run: false, inputs: JSON.stringify({ scheduler: true, slurm: { time: '00:05:00' } }) }))
  assert.equal(inputsOf(sent()).slurm.partition, 'cpu', 'the up partition with the most free nodes, never the down one')
  assert.equal(inputsOf(sent()).resource, 'pw://me/vega')
  assert.match(r.result, /Partition cpu was chosen for vega \(20\/24 nodes free/)
})
test('a partition the caller named is left alone', async () => {
  const r = await executeTool('run_workflow', JSON.stringify({ name: 'hellobatch', resource: 'vega', dry_run: false, inputs: JSON.stringify({ scheduler: true, slurm: { partition: 'gpu' } }) }))
  assert.equal(inputsOf(sent()).slurm.partition, 'gpu')
  assert.doesNotMatch(r.result, /was chosen/)
})
test('a direct-ssh submission (scheduler false) is not given a partition', async () => {
  await executeTool('run_workflow', JSON.stringify({ name: 'hellobatch', resource: 'vega', dry_run: false, inputs: JSON.stringify({ scheduler: false }) }))
  assert.equal(inputsOf(sent()).slurm, undefined)
})
