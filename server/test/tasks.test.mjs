// Missions: headless agents with server-enforced limits. The pw binary is
// a stub whose first-generation agents request subtasks, so the sub-spawn
// path, the ceiling, and the depth limit are all exercised for real.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = idx

// The stub: a top-level task (objective contains "root") answers with two
// subtasks; everything else answers plainly. Runs fast, no daemon.
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
fs.writeFileSync(path.join(bin, 'pw'), `#!/bin/bash
prompt=""
while [ $# -gt 0 ]; do [ "$1" = "-p" ] && prompt="$2"; shift; done
if [[ "$prompt" == *root-task* ]]; then
  echo '{"result": "root analysis done", "subtasks": [{"objective": "leaf one"}, {"objective": "leaf two"}]}'
else
  echo '{"result": "leaf work done", "subtasks": [{"objective": "grandchild that must be refused"}]}'
fi
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const { createTask, getTask } = await import('../dist/tasks.js')

const settle = async (m, tries = 60) => {
  for (let i = 0; i < tries; i++) {
    if (m.state !== 'working') return
    await new Promise(r => setTimeout(r, 100))
  }
}

test('agents spawn, sub-spawn within limits, and results land in the corpus', async () => {
  const m = createTask({
    objective: 'test coordination', model: 'stub:model',
    agents: [{ persona: '', objective: 'root-task investigate' }],
    maxAgents: 10, maxDepth: 1,
  })
  await settle(m)
  assert.equal(m.state, 'completed')
  const agents = [...m.nodes.values()]
  // 1 root + 2 leaves; the grandchildren the leaves requested exceed
  // depth 1 and must have been refused, not spawned.
  assert.equal(agents.length, 3, `expected 3 agents, saw ${agents.map(a => a.name).join(', ')}`)
  assert.ok(agents.every(a => a.state === 'completed'))
  const leaves = agents.filter(a => a.depth === 1)
  assert.equal(leaves.length, 2)
  assert.ok(leaves.every(a => a.parent === agents[0].name), 'lineage is recorded')
  const refusals = m.board.filter(b => b.topic === 'refused')
  assert.equal(refusals.length, 2, 'both grandchild requests were refused on the board')
  // results are corpus files the board points at
  for (const a of agents) {
    assert.ok(a.resultPath && fs.existsSync(path.join(kb, a.resultPath)), `${a.name} result on disk`)
  }
  // the board is ordered and complete
  const seqs = m.board.map(b => b.seq)
  assert.deepEqual(seqs, [...seqs].sort((x, y) => x - y))
})

test('the agent ceiling refuses subtasks past it', async () => {
  const m = createTask({
    objective: 'ceiling test', model: 'stub:model',
    agents: [{ objective: 'root-task expand' }],
    maxAgents: 2, maxDepth: 3,
  })
  await settle(m)
  assert.ok(m.nodes.size <= 2, `ceiling held: ${m.nodes.size} agents`)
  assert.ok(m.board.some(b => b.topic === 'refused' && /ceiling/.test(b.body)), 'the refusal names the ceiling')
})
