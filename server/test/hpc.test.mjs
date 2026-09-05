// Launching work on a connected HPC system without the user knowing
// scheduler syntax: site configurations chosen by name, resource targeting
// resolved from the workflow's own schema, and a run followed to its jobs.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
const calls = path.join(bin, 'calls.log')

// A stand-in for the pw CLI. It answers the four subcommands these tools
// use and records every invocation, so the test can assert on what was
// actually submitted. Its JSON replies carry the CLI's upgrade notice
// trailing the payload, which is the shape that broke a bare JSON.parse.
const WORKFLOW = {
  name: 'mp.ollama.latest',
  yaml: {
    on: {
      execute: {
        inputs: {
          resource: { type: 'compute-clusters', default: null },
          cluster: {
            type: 'group',
            items: {
              scheduler: { type: 'boolean', default: false },
              slurm: {
                type: 'group',
                items: {
                  partition: { type: 'slurm-partitions', default: null },
                  qos: { type: 'slurm-qos', default: null },
                  account: { type: 'slurm-accounts', default: null },
                  time: { type: 'string', default: '00:30:00' },
                },
              },
            },
          },
          service: { type: 'group', items: { models: { type: 'string', default: 'a-model' } } },
        },
      },
    },
    jobs: { preprocessing: {}, session_runner: { needs: ['preprocessing'] } },
  },
  configurations: [
    {
      name: 'Vega', builtin: true,
      inputs: { cluster: { scheduler: true, slurm: { partition: 'standard', qos: 'standard', time: '01:00:00' } } },
    },
    {
      name: 'gemma-uncensored', builtin: false,
      inputs: {
        resource: 'pw://someone/vega',
        cluster: { scheduler: true, slurm: { partition: 'standard', qos: 'standard', account: 'acct123', time: '01:00:00' } },
        service: { models: 'hf.co/some/abliterated-GGUF:Q4_K_M' },
      },
    },
  ],
}

const ENVS = [
  {
    clusterName: 'vega', clusterDisplayName: 'Vega (Site A)', clusterUser: 'someone',
    schedulerType: 'slurm', partitionName: 'AIML', partitionState: 'up',
    availNodes: 9, totalNodes: 16, maxTime: '7-00:00:00',
    defaults: { numNodes: 1, walltime: '7-00:00:00' },
    fields: [{ key: 'walltime', required: true }, { key: 'account', required: true }, { key: 'qos', required: true }],
  },
  {
    clusterName: 'juno', clusterDisplayName: 'Juno (Site B)', clusterUser: 'someone',
    schedulerType: 'pbs', partitionName: 'standard', partitionState: 'up',
    availNodes: 40, totalNodes: 100, maxTime: '1-00:00:00',
    defaults: {}, fields: [],
  },
]

const RUN_VIEW = {
  slug: 'mp-ollama-00028', workflowName: 'mp.ollama.latest', status: 'running',
  inputs: { service: { endpoint_name: 'gemma-heretic', models: 'hf.co/some/abliterated-GGUF:Q4_K_M' } },
  executedJobs: {
    preprocessing: { status: 'completed', ssh: { remoteHost: 'vega.example.org' }, steps: [{ name: 'Checkout', status: 'completed' }] },
    session_runner: { status: 'running', ssh: { remoteHost: 'vega.example.org' }, steps: [{ name: 'Start service', status: 'running' }] },
    cleanup: { status: 'skipped', steps: [{ name: 'Never ran', status: 'skipped' }] },
  },
}

const NOTICE = '\n\nA new version of the PW CLI is available (current: v7.91.0, latest: v7.94.0).\n'
const fake = `#!/usr/bin/env node
const fs = require('fs')
const args = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(args) + '\\n')
const notice = ${JSON.stringify(NOTICE)}
const sub = args.slice(0, 3).join(' ')
if (args[0] === 'environments') {
  process.stdout.write(JSON.stringify(${JSON.stringify(ENVS)}) + notice)
} else if (sub.startsWith('workflows get')) {
  process.stdout.write(JSON.stringify(${JSON.stringify(WORKFLOW)}) + notice)
} else if (sub.startsWith('workflows runs view')) {
  if (args[3] !== 'mp-ollama-00028' && args[3] !== 'abc123') {
    process.stderr.write('[ERROR] Workflow run not found')
    process.exit(1)
  }
  process.stdout.write(JSON.stringify(${JSON.stringify(RUN_VIEW)}) + notice)
} else if (sub.startsWith('workflows runs list')) {
  process.stdout.write(JSON.stringify({ runs: [{ id: 'abc123', slug: 'mp-ollama-00028' }] }) + notice)
} else if (sub.startsWith('workflows run')) {
  // Reject when no account reached the submission, as the platform does.
  const i = args.indexOf('-i')
  const sent = i >= 0 ? JSON.parse(args[i + 1]) : {}
  if (!sent?.cluster?.slurm?.account) {
    process.stderr.write('[ERROR] Missing required fields: SLURM account')
    process.exit(1)
  }
  process.stdout.write(JSON.stringify({ run: { slug: 'mp-ollama-00028', status: 'validated' } }) + notice)
} else {
  process.stdout.write('{}')
}
`
fs.writeFileSync(path.join(bin, 'pw'), fake, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const { executeTool } = await import('../dist/chat/tools.js')
const call = (name, args) => executeTool(name, JSON.stringify(args))
const lastRun = () => fs.readFileSync(calls, 'utf8').trim().split('\n')
  .map(l => JSON.parse(l)).filter(a => a[0] === 'workflows' && a[1] === 'run').pop()

test('a system listing survives the notice the CLI appends to JSON', async () => {
  const r = await call('hpc_environments', {})
  assert.match(r.result, /vega \(Vega \(Site A\)\), slurm/)
  assert.match(r.result, /juno \(Juno \(Site B\)\), pbs/)
})

test('partitions report their ceiling, free nodes and required fields', async () => {
  const r = await call('hpc_environments', { cluster: 'vega' })
  assert.match(r.result, /AIML: up, 9\/16 nodes free, max walltime 7-00:00:00/)
  assert.match(r.result, /required walltime, account, qos/)
})

test('an unknown system names the ones that do have partitions', async () => {
  const r = await call('hpc_environments', { cluster: 'nosuch' })
  assert.match(r.result, /vega/)
  assert.match(r.result, /wheat/)
})

test('configurations are summarised by the settings that decide a submission', async () => {
  const r = await call('workflow_configs', { name: 'mp.ollama.latest' })
  assert.match(r.result, /Vega \(builtin\): partition=standard, qos=standard/)
  // The account is the one required value the builtin configurations omit,
  // so it has to be visible where it does exist.
  assert.match(r.result, /account=acct123/)
  assert.match(r.result, /resource=pw:\/\/someone\/vega/)
})

test('a saved configuration launches without the caller composing inputs', async () => {
  await call('run_workflow', { name: 'mp.ollama.latest', config: 'gemma-uncensored', dry_run: false })
  const args = lastRun()
  assert.ok(args.includes('--saved-inputs'), 'the configuration is passed to the CLI')
  assert.equal(args[args.indexOf('--saved-inputs') + 1], 'gemma-uncensored')
  assert.ok(!args.includes('--dry-run'), 'a requested run is not silently downgraded to validation')
})

test('a named system is resolved into the workflow\'s own compute-clusters input', async () => {
  await call('run_workflow', {
    name: 'mp.ollama.latest', config: 'gemma-uncensored', resource: 'vega', dry_run: false,
  })
  const args = lastRun()
  const sent = JSON.parse(args[args.indexOf('-i') + 1])
  // Resolved to the owning account rather than passed through bare.
  assert.equal(sent.resource, 'pw://someone/vega')
  // The configuration is merged in, because --inputs would otherwise
  // replace it wholesale and drop the site's scheduler settings.
  assert.equal(sent.cluster.slurm.partition, 'standard')
  assert.equal(sent.cluster.slurm.account, 'acct123')
})

test('caller inputs win over the configuration they are merged onto', async () => {
  await call('run_workflow', {
    name: 'mp.ollama.latest', config: 'gemma-uncensored', dry_run: false,
    inputs: JSON.stringify({ cluster: { slurm: { time: '04:00:00' } } }),
  })
  const sent = JSON.parse(lastRun()[lastRun().indexOf('-i') + 1])
  assert.equal(sent.cluster.slurm.time, '04:00:00')
  assert.equal(sent.cluster.slurm.qos, 'standard', 'sibling settings survive the override')
})

test('a missing site account is explained with the account that would fix it', async () => {
  const r = await call('run_workflow', {
    name: 'mp.ollama.latest', config: 'Makau', resource: 'vega', dry_run: false,
  })
  assert.match(r.result, /SLURM account/)
  assert.match(r.result, /acct123/)
  assert.doesNotMatch(r.result, /\[ERROR\]/, 'the raw CLI line is not what the model reads')
})

test('a run reports where it landed and which step is live', async () => {
  const r = await call('watch_run', { run: 'mp-ollama-00028', wait: 0 })
  assert.match(r.result, /session_runner: running on vega\.example\.org \(at "Start service"\)/)
  // A skipped job has no live step, and naming one would be a fiction.
  assert.doesNotMatch(r.result, /cleanup: skipped.*\(at/)
  assert.match(r.result, /Endpoint "gemma-heretic"/)
})

test('a run identifier that does not exist is reported, not thrown', async () => {
  const r = await call('watch_run', { run: 'nope', wait: 0 })
  assert.match(r.result, /No run named nope/)
})
