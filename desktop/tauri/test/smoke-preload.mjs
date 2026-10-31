// Loaded into the Studio's server (node --import) by the shell's smoke test
// only. It starts one long-lived child process, as an indexer would, and
// records both PIDs, so the test can check that nothing outlives the
// server; and it exits the server with the code written to the trigger
// file, so the test can drive the shell's supervision.
import { spawn } from 'node:child_process'
import fs from 'node:fs'

const pids = process.env.STUDIO_SMOKE_PIDS
const trigger = process.env.STUDIO_SMOKE_TRIGGER

const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1 << 30)'], { stdio: 'ignore', windowsHide: true })
child.unref()
if (pids) fs.appendFileSync(pids, JSON.stringify({ server: process.pid, child: child.pid, at: Date.now() }) + '\n')

if (trigger) {
  setInterval(() => {
    let code
    try { code = Number(fs.readFileSync(trigger, 'utf8').trim()) } catch { return }
    try { fs.rmSync(trigger) } catch { /* gone */ }
    process.exit(Number.isInteger(code) ? code : 1)
  }, 100).unref()
}
