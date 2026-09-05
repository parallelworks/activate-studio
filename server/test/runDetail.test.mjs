import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { summarizeRunDetail } = await import('../dist/chat/tools.js')

const VIEW = JSON.stringify({ slug: 'hellobatch-00007', workflowName: 'hellobatch', status: 'completed', createdAt: '2026-09-05T12:50:00Z', completedAt: '2026-09-05T12:51:30Z',
  executedJobs: {
    preprocessing: { status: 'completed', ssh: { remoteHost: 'vega.example.org' }, steps: [{ name: 'Create command script', status: 'completed' }] },
    job_runner: { status: 'completed', ssh: { remoteHost: 'vega.example.org' }, steps: [{ name: 'Submit batch job', status: 'completed' }, { name: 'Show job output', status: 'completed' }, { name: 'Notify', status: 'skipped' }] },
  },
  yaml: { jobs: { job_runner: { steps: [{ run: 'cat > commands.sh << EOF\n' + 'x'.repeat(30_000) + '\nEOF' }] } } } })
const LOGS = `=== job: preprocessing > step: Create command script (completed, 1s) ===
(error fetching logs: API error 400)
echo "$(date) Creating command script..."
=== job: job_runner > step: Submit batch job (completed, 60s) ===
Submitted batch job 5251
=== job: job_runner > step: Show job output (completed, 0s) ===
  Hostname:    xfer01
studio-e2e-mtodkzdu
JobId=5251 JobName=hellobatch-00007
=== job: job_runner > step: Notify (skipped, 0s) ===
`

test('the job output reaches the reader, and the workflow script text does not', () => {
  const { text, status } = summarizeRunDetail(VIEW, '', LOGS)
  assert.equal(status, 'completed')
  assert.match(text, /^Run hellobatch-00007 of hellobatch: completed, started 2026-09-05T12:50:00Z, ended 2026-09-05T12:51:30Z/)
  assert.match(text, /job_runner: completed on vega\.example\.org \[Submit batch job: completed; Show job output: completed; Notify: skipped\]/)
  assert.match(text, /studio-e2e-mtodkzdu/)
  assert.match(text, /JobId=5251/)
  assert.doesNotMatch(text, /xxxxxxxx/, 'the embedded script is not dumped')
  assert.doesNotMatch(text, /error fetching logs/)
  assert.ok(text.length < 2000, `compact: ${text.length} chars`)
})
test('a failed step comes first with a longer tail, and errors are shown', () => {
  const view = JSON.stringify({ slug: 'r-1', workflowName: 'w', status: 'error', executedJobs: { j: { status: 'error', steps: [{ name: 'Submit', status: 'error' }] } } })
  const logs = `=== job: j > step: Prep (completed, 1s) ===\nok\n=== job: j > step: Submit (error, 0s) ===\nsbatch: error: Job rejected: No GRES specified\n`
  const { text, status } = summarizeRunDetail(view, 'Errors: sbatch failed', logs)
  assert.equal(status, 'error')
  assert.match(text, /Errors:\nErrors: sbatch failed/)
  assert.ok(text.indexOf('--- j > Submit (error) ---') < text.indexOf('--- j > Prep (completed) ---'), 'failed step first')
  assert.match(text, /No GRES specified/)
})
test('an unparseable view still yields something useful', () => {
  const { text, status } = summarizeRunDetail('not json at all', '', '')
  assert.equal(status, 'unknown'); assert.match(text, /Run detail:\nnot json at all/)
})
