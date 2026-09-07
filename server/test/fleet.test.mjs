// Standing agents: ticks as delegated tasks, triggers, budgets, journals,
// and reload after a restart, against a fake pw code.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
process.env.STUDIO_FLEET_TICK_MS = '25'
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'bin-'))
const calls = path.join(bin, 'calls.log')
// The fake agent: reads its prompt, answers with the result contract and
// a usage block; a prompt mentioning "decide" ends with a decision request.
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const a = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(a.slice(0, 3)) + '\\n')
if (a[0] === 'code') {
  const prompt = a[a.indexOf('-p') + 1] || ''
  const decision = /decide/i.test(prompt) ? ' NEEDS DECISION: which system should the next round use?' : ''
  const result = 'Tick done. Checked the queue; nothing failed.' + decision
  process.stdout.write(JSON.stringify({ result, usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 } }))
  process.exit(0)
}
process.stdout.write('{}')
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')
const fleet = await import('../dist/fleet.js')
const sleep = ms => new Promise(r => setTimeout(r, ms))
const until = async (fn, ms = 4000) => { const t0 = Date.now(); while (!fn() && Date.now() - t0 < ms) await sleep(20); return fn() }

test('a new agent ticks on the schedule, journals the result, counts tokens, and waits for the next tick', async () => {
  fleet.startFleet()
  const a = fleet.createAgent({ name: 'Queue watcher', goal: 'Watch the queue and report.', persona: 'watcher', model: 'm', every: '15m', onRunEnd: true })
  assert.equal(a.state, 'idle')
  assert.ok(await until(() => fleet.getAgent(a.id).ticks === 1 && fleet.getAgent(a.id).state === 'idle'), 'ticked and returned to idle')
  const b = fleet.getAgent(a.id)
  assert.equal(b.tokens, 120)
  assert.ok(b.nextTickAt && Date.parse(b.nextTickAt) > Date.now() + 10 * 60_000, 'next tick about fifteen minutes out')
  const kinds = fleet.journalTail(a.id).map(e => e.kind)
  assert.deepEqual(kinds.slice(0, 4), ['created', 'tick', 'result', 'state'])
  assert.match(fleet.journalTail(a.id).find(e => e.kind === 'result').text, /Tick done/)
  assert.ok(fs.existsSync(path.join(IX, 'fleet', a.id, 'agent.json')))
})
test('a run ending wakes the agents that asked for it, and a working agent gets it in its inbox', async () => {
  const a = fleet.listAgents()[0]
  const before = a.ticks
  fleet.notifyRunEnded({ slug: 'activatebatch-00012', workflow: 'activatebatch', resource: 'a30gpuserver', state: 'completed' })
  assert.ok(await until(() => fleet.getAgent(a.id).ticks === before + 1 && fleet.getAgent(a.id).state === 'idle'))
  const ev = fleet.journalTail(a.id).filter(e => e.kind === 'event').pop()
  assert.match(ev.text, /activatebatch-00012 .* ended: completed/)
  const tick = fleet.journalTail(a.id).filter(e => e.kind === 'tick').pop()
  assert.match(tick.text, /activatebatch-00012/, 'the event is what the tick was told')
})
test('a paused agent does not tick; a tick budget pauses it; a decision request parks it until answered', async () => {
  const p = fleet.createAgent({ name: 'Paused', goal: 'Nothing yet.', model: 'm', every: '15m' })
  fleet.pauseAgent(p.id)
  await sleep(120)
  assert.equal(fleet.getAgent(p.id).ticks, 0)
  const b = fleet.createAgent({ name: 'Budgeted', goal: 'One round only.', model: 'm', every: '15m', maxTicks: 1 })
  assert.ok(await until(() => fleet.getAgent(b.id).state === 'paused'))
  assert.match(fleet.getAgent(b.id).note, /tick budget of 1 used/)
  const d = fleet.createAgent({ name: 'Decider', goal: 'Please decide the next system.', model: 'm', every: '15m' })
  assert.ok(await until(() => fleet.getAgent(d.id).state === 'input-required'))
  assert.match(fleet.getAgent(d.id).note, /which system/)
  fleet.messageAgent(d.id, 'Use the debug partition on a30gpuserver.')
  assert.ok(await until(() => fleet.getAgent(d.id).ticks === 2))
  assert.match(fleet.journalTail(d.id).filter(e => e.kind === 'message').pop().text, /debug partition/)
})
test('after a restart the fleet is reloaded from disk and an interrupted tick becomes idle', async () => {
  const w = fleet.createAgent({ name: 'Restarter', goal: 'Persist.', model: 'm', every: 'manual' })
  const rec = JSON.parse(fs.readFileSync(path.join(IX, 'fleet', w.id, 'agent.json'), 'utf8'))
  rec.state = 'working'; rec.currentTaskId = 'gone'
  fs.writeFileSync(path.join(IX, 'fleet', w.id, 'agent.json'), JSON.stringify(rec))
  fleet.resetFleetForTests()
  const fresh = await import('../dist/fleet.js?v=2')
  const n = fresh.startFleet()
  assert.ok(n >= 4, `reloaded ${n} agents`)
  const back = fresh.getAgent(w.id)
  assert.equal(back.state, 'idle')
  assert.match(back.note, /interrupted by a restart/)
  fresh.resetFleetForTests()
})
