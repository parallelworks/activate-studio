// A restart must not amnesia a campaign the operator was watching. The
// snapshot written beside the board is what a fresh process loads, and an
// agent recorded as working when the process died has to say it was
// interrupted rather than spin forever.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = idx

// A worker that reports progress on stderr, then finishes with the
// contract on stdout, like the real CLI shape.
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
process.stderr.write('working on it\\n')
process.stdout.write(JSON.stringify({ result: 'all done', subtasks: [] }))
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const first = await import('../dist/tasks.js')

const waitDone = async (mod, id) => {
  for (let i = 0; i < 100; i++) {
    const t = mod.getTask(id)
    if (t && t.state !== 'working') return t
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error('task never finished')
}

test('a finished task survives a restart with its outcome intact', async () => {
  const m = first.createTask({ objective: 'write the report', model: 'test-model', agents: [{ objective: 'do it' }] })
  await waitDone(first, m.id)

  // A fresh import against the same index directory is a new process.
  const second = await import(`../dist/tasks.js?restart=${Date.now()}`)
  const listed = second.listTasks().find(t => t.id === m.id)
  assert.ok(listed, 'the task is listed after the restart')
  assert.equal(listed.state, 'completed')
  const re = second.getTask(m.id)
  assert.equal(re.nodes.size, 1, 'its agents came back too')
  assert.ok([...re.nodes.values()][0].resultPath, 'with the result path pointing into the corpus')
  assert.ok(re.board.length > 0, 'and the board history')
})

test('an agent that was mid-flight reads as interrupted, not as working', async () => {
  // A worker that never finishes stands in for the process dying mid-run.
  fs.writeFileSync(path.join(bin, 'pw'), '#!/bin/bash\nsleep 60\n', { mode: 0o755 })
  const m = first.createTask({ objective: 'long haul', model: 'test-model', agents: [{ objective: 'never ends' }] })
  await new Promise(r => setTimeout(r, 300))

  const third = await import(`../dist/tasks.js?restart2=${Date.now()}`)
  const re = third.getTask(m.id)
  assert.ok(re, 'the interrupted task is still visible')
  assert.equal(re.state, 'canceled')
  const agent = [...re.nodes.values()][0]
  assert.equal(agent.state, 'canceled')
  assert.match(agent.note, /interrupted by a server restart/)
  first.stopTask(m.id)
})

test('a rehydrated task holds no live token', async () => {
  const fourth = await import(`../dist/tasks.js?restart3=${Date.now()}`)
  for (const row of fourth.listTasks()) {
    const t = fourth.getTask(row.id)
    assert.equal(t.token, '', `${row.id} must not resurrect a bearer`)
    assert.equal(fourth.taskByToken(''), undefined, 'and the empty token resolves to nothing')
  }
})

test('the live output tail is readable while and after an agent runs', async () => {
  fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
process.stderr.write('step one\\n')
process.stdout.write(JSON.stringify({ result: 'ok' }))
`, { mode: 0o755 })
  const m = first.createTask({ objective: 'tail me', model: 'test-model', agents: [{ objective: 'talk' }] })
  await waitDone(first, m.id)
  const name = [...first.getTask(m.id).nodes.keys()][0]
  const tail = first.agentOutput(m.id, name)
  assert.match(tail, /step one/, 'stderr narration is captured')
  assert.match(tail, /"result"/, 'and the final contract lands in the same log')
  assert.equal(first.agentOutput(m.id, 'nope'), null, 'an unknown agent is null, not empty')
})
