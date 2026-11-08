// Golden values for internal/state: stateFormat.ts on the fixture in
// internal/state/testdata/fixture.json (files under INDEX_BASE; "@config"
// is the launcher's studio.env). Usage: node state.mjs <fixture.json> <repo root>
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const [, , fixtureArg, repoArg] = process.argv
const fixture = JSON.parse(fs.readFileSync(fixtureArg, 'utf8'))
const base = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-state-'))
const ix = path.join(base, 'ix')
const conf = path.join(base, 'data', 'studio.env')
process.env.INDEX_BASE = ix
process.env.KB_ROOT = path.join(base, 'kb')
process.env.STUDIO_CONFIG_FILE = conf
delete process.env.STUDIO_DATA_DIR
for (const [rel, body] of Object.entries(fixture.files)) {
  const file = rel === '@config' ? conf : path.join(ix, rel)
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body)
}
const S = await import(path.join(path.resolve(repoArg), 'server/dist/stateFormat.js'))
const out = {}
out.files = S.stateFiles(ix)
out.formatBefore = S.stateFormat(ix)
out.first = S.reconcileState('v1', () => {}, ix)
out.marker = (({ format, writtenBy }) => ({ format, writtenBy }))(JSON.parse(fs.readFileSync(path.join(ix, S.MARKER), 'utf8')))
out.again = S.reconcileState('v1', () => {}, ix)
const snap = S.takeSnapshot({ from: 'v1', to: 'v2', reason: 'update' }, ix)
out.snapshot = { from: snap.from, to: snap.to, reason: snap.reason, format: snap.format, files: snap.files, idShape: /^\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-\d{3}Z-\d+-[0-9a-z]{4}$/.test(snap.id) }
out.snapshotFiles = fs.readdirSync(path.join(S.snapshotsDir(), snap.id, 'files'), { recursive: true }).map(String).sort()
// A newer version migrated: format 2, new settings, a new file, one gone.
S.writeMarker(2, 'v2', ix)
fs.writeFileSync(path.join(ix, 'settings.json'), JSON.stringify({ appName: 'New', updates: { check: false, channel: 'preview' }, extra: [1, { a: 'b' }] }))
fs.writeFileSync(path.join(ix, 'users', 'u1', 'new.json'), '{}')
fs.rmSync(path.join(ix, 'tasks', 't.json'))
out.formatNewer = S.stateFormat(ix)
const logs = []
out.downgrade = S.reconcileState('v1', m => logs.push(m.replace(/taken \S+/, 'taken <at>')), ix)
out.log = logs
out.after = S.stateFiles(ix)
out.settings = fs.readFileSync(path.join(ix, 'settings.json'), 'utf8')
out.config = fs.readFileSync(conf, 'utf8')
out.markerAfter = JSON.parse(fs.readFileSync(path.join(ix, S.MARKER), 'utf8')).format
out.snapshots = S.listSnapshots().map(s => ({ from: s.from, to: s.to, reason: s.reason.replace(/of \S+$/, 'of <at>'), format: s.format, files: s.files.length }))
// A format no snapshot can read.
fs.rmSync(S.snapshotsDir(), { recursive: true, force: true })
S.writeMarker(3, 'v3', ix)
out.unreadable = S.reconcileState('v1', () => {}, ix)
fs.rmSync(base, { recursive: true, force: true })
console.log(JSON.stringify(out, null, 1))
