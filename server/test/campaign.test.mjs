// Campaign mode: agents as platform workflow runs. The fake CLI plays the
// platform: it serves the runner workflow check, accepts the submission,
// reports the run through view/logs, and the engine must carry the agent
// from submission to a corpus result without ever owning a child process.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = idx
process.env.STUDIO_CAMPAIGN_POLL_MS = '150'
// The schema fixture stands in for the platform, as in workflowSchema.test.
process.env.PW_GATEWAY_URL = 'http://127.0.0.1:1/api/openai/v1'
const here = path.dirname(fileURLToPath(import.meta.url))
fs.copyFileSync(path.join(here, 'fixtures', 'workflow.schema.json'), path.join(idx, 'workflow.schema.json'))

// State the fake CLI mutates across calls, on disk because each call is a
// fresh process.
const state = path.join(idx, 'fake-state')
fs.mkdirSync(state, { recursive: true })

const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
const CONTRACT = JSON.stringify({ result: 'campaign findings', subtasks: [] })
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const path = require('path')
const S = ${JSON.stringify(state)}
const a = process.argv.slice(2)
fs.appendFileSync(path.join(S, 'calls.log'), JSON.stringify(a) + '\\n')
const sub = a.slice(0, 3).join(' ')
if (sub.startsWith('workflows get')) {
  // First call: the runner does not exist yet; after create, it does.
  if (!fs.existsSync(path.join(S, 'runner-created'))) { process.stderr.write('[ERROR] workflow not found'); process.exit(1) }
  process.stdout.write(JSON.stringify({ name: 'studio-agent-runner' }))
} else if (sub.startsWith('workflows create')) {
  fs.writeFileSync(path.join(S, 'runner-created'), '1')
  process.stdout.write('{}')
} else if (sub.startsWith('workflows runs view')) {
  const t = Number(fs.readFileSync(path.join(S, 'ticks'), 'utf8')) + 1
  fs.writeFileSync(path.join(S, 'ticks'), String(t))
  const status = t < 10 ? 'running' : 'completed'
  process.stdout.write(JSON.stringify({ slug: 'agent-run-00001', status,
    executedJobs: { agent: { status, steps: [{ name: 'Run agent', status: t < 3 ? 'running' : 'completed' }] } } }))
} else if (sub.startsWith('workflows runs logs')) {
  const t = Number(fs.readFileSync(path.join(S, 'ticks'), 'utf8'))
  process.stdout.write(t < 10
    ? 'setting up the agent...'
    : 'setting up the agent...\\nSTUDIO_RESULT_BEGIN\\n' + ${JSON.stringify(CONTRACT)} + '\\nSTUDIO_RESULT_END\\ntrailing stderr tail')
} else if (sub.startsWith('workflows runs cancel')) {
  fs.writeFileSync(path.join(S, 'canceled'), '1')
  process.stdout.write('{}')
} else if (sub.startsWith('workflows run')) {
  // Ordered after every 'workflows runs *' branch: 'workflows runs view'
  // startsWith 'workflows run' too, and the collision sent polls here.
  fs.writeFileSync(path.join(S, 'ticks'), '0')
  process.stdout.write(JSON.stringify({ run: { slug: 'agent-run-00001', status: 'running' } }))
} else { process.stdout.write('{}') }
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const tasksMod = await import('../dist/tasks.js')

const waitFor = async (cond, ms = 15000) => {
  const start = Date.now()
  while (Date.now() - start < ms) {
    if (cond()) return
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error('condition never met')
}

test('the runner workflow itself passes the platform schema', async () => {
  const { parse } = await import('yaml')
  const { validateWorkflowDoc } = await import('../dist/workflowSchema.js')
  const doc = parse(tasksMod.runnerWorkflowYaml())
  const v = await validateWorkflowDoc(doc)
  assert.equal(v.ok, true, `the runner must be submittable: ${v.errors.join('; ')}`)
})

test('a campaign agent goes from submission to a corpus result with no child process', async () => {
  const m = tasksMod.createTask({
    objective: 'campaign objective', model: 'test-model',
    agents: [{ objective: 'study the thing' }],
    execution: 'campaign', resource: 'pw://user/testcluster',
  })
  assert.equal(m.execution, 'campaign')
  const agent = () => [...tasksMod.getTask(m.id).nodes.values()][0]

  await waitFor(() => agent().runSlug === 'agent-run-00001')
  await waitFor(() => agent().note.includes('running'), 10000)
  assert.match(agent().note, /agent-run-00001/, 'the note names the run while it works')

  await waitFor(() => agent().state === 'completed')
  assert.equal(tasksMod.getTask(m.id).state, 'completed')
  const a = agent()
  assert.ok(a.resultPath, 'the result landed in the corpus')
  const body = fs.readFileSync(path.join(kb, a.resultPath), 'utf8')
  assert.match(body, /campaign findings/, 'through the same completion path as local agents')
  assert.equal(tasksMod.getTask(m.id).procs.size, 0, 'no child process ever existed for it')

  const tail = tasksMod.agentOutput(m.id, a.name)
  assert.match(tail, /setting up the agent/, 'the run log serves the same drill-down as local output')

  const runnerYaml = fs.readFileSync(path.join(idx, 'tasks', 'agent-runner.yaml'), 'utf8')
  assert.match(runnerYaml, /STUDIO_RESULT_BEGIN/, 'the runner was written and registered on first use')
})

test('campaign requires a resource, since agents must run somewhere', () => {
  assert.throws(() => tasksMod.createTask({
    objective: 'x', model: 'm', agents: [{ objective: 'y' }], execution: 'campaign',
  }), /needs a resource/)
})

test('stopping a campaign cancels the platform run, not just the poller', async () => {
  fs.rmSync(path.join(state, 'ticks'), { force: true })
  const m = tasksMod.createTask({
    objective: 'to be stopped', model: 'test-model',
    agents: [{ objective: 'long haul' }],
    execution: 'campaign', resource: 'pw://user/testcluster',
  })
  await waitFor(() => [...tasksMod.getTask(m.id).nodes.values()][0].runSlug)
  tasksMod.stopTask(m.id)
  await waitFor(() => fs.existsSync(path.join(state, 'canceled')))
  const a = [...tasksMod.getTask(m.id).nodes.values()][0]
  assert.equal(a.state, 'canceled')
})

test('a restart reattaches to a running campaign instead of declaring it dead', async () => {
  fs.rmSync(path.join(state, 'canceled'), { force: true })
  fs.rmSync(path.join(state, 'ticks'), { force: true })
  const m = tasksMod.createTask({
    objective: 'survives restarts', model: 'test-model',
    agents: [{ objective: 'keep going' }],
    execution: 'campaign', resource: 'pw://user/testcluster',
  })
  await waitFor(() => [...tasksMod.getTask(m.id).nodes.values()][0].runSlug)

  // A fresh import is the new process; the platform run is still going.
  const second = await import(`../dist/tasks.js?restart=${Date.now()}`)
  const re = second.getTask(m.id)
  assert.equal(re.state, 'working', 'the campaign is still alive after the restart')
  const a = [...re.nodes.values()][0]
  assert.equal(a.state, 'working')
  await waitFor(() => [...second.getTask(m.id).nodes.values()][0].state === 'completed')
  assert.ok([...second.getTask(m.id).nodes.values()][0].resultPath, 'and it finishes under the new process')
  tasksMod.stopTask(m.id)
})
