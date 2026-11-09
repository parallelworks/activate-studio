// Golden values for internal/settings: effectiveSettings() of the Node
// server for each case in internal/settings/testdata/cases.json.
// Usage: node settings.mjs <cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const [, , casesFileArg, repoArg, part] = process.argv
const casesFile = path.resolve(casesFileArg)
const repo = path.resolve(repoArg)
const cases = JSON.parse(fs.readFileSync(casesFile, 'utf8'))
if (!part) {
  // One process for the cases without sign-in, one with: auth.js reads
  // AUTH_JWKS_URL once, when it is imported.
  const run = (p, extra) => JSON.parse(execFileSync(process.execPath, [process.argv[1], casesFile, repo, p], { env: { PATH: process.env.PATH, HOME: process.env.HOME, ...extra }, encoding: 'utf8' }))
  const off = run('off', {})
  const on = run('on', { AUTH_JWKS_URL: 'http://127.0.0.1:9/never-fetched' })
  console.log(JSON.stringify(cases.map(c => ({ name: c.name, effective: (c.auth ? on : off)[c.name] })), null, 1))
} else {
  const base = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-settings-'))
  process.env.KB_ROOT = path.join(base, 'kb')
  process.env.INDEX_BASE = path.join(base, 'ix')
  fs.mkdirSync(process.env.INDEX_BASE, { recursive: true })
  const { effectiveSettings } = await import(path.join(repo, 'server/dist/settings.js'))
  const keep = { ...process.env }
  const out = {}
  const file = path.join(process.env.INDEX_BASE, 'settings.json')
  let n = 0
  for (const c of cases) {
    if (!!c.auth !== (part === 'on')) continue
    for (const k of Object.keys(process.env)) if (!(k in keep)) delete process.env[k]
    Object.assign(process.env, keep, c.env)
    if (c.saved === null) fs.rmSync(file, { force: true })
    else { fs.writeFileSync(`${file}.${++n}`, JSON.stringify(c.saved)); fs.renameSync(`${file}.${n}`, file) }
    out[c.name] = effectiveSettings()
  }
  fs.rmSync(base, { recursive: true, force: true })
  console.log(JSON.stringify(out))
}
