// Golden values for internal/remote: remoteExec.ts's pure functions on the
// cases in internal/remote/testdata/cases.json.
// Usage: node remote.mjs <cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const [, , casesFile, repoArg] = process.argv
const repo = path.resolve(repoArg)
const cases = JSON.parse(fs.readFileSync(casesFile, 'utf8'))
const base = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-remote-'))
process.env.INDEX_BASE = path.join(base, 'ix')
process.env.KB_ROOT = path.join(base, 'kb')
const R = await import(path.join(repo, 'server/dist/remoteExec.js'))
const tryIt = f => { try { return { ok: f() } } catch (e) { return { error: e.message, status: e.status ?? null } } }
const out = {
  options: cases.options.map(t => tryIt(() => R.parseSshOptions(t))),
  sites: cases.sites.map(c => tryIt(() => R.sshSite(c.destination, c.port, c.options))),
  parsed: cases.parsed.map(s => tryIt(() => R.parseSshSite(s))),
  names: cases.parsed.map(s => R.siteName(s)),
  shells: cases.parsed.map(s => tryIt(() => R.shellFor(s))),
  tags: cases.sites.map(c => R.optionsTag(c.options)),
  jumps: cases.sites.map(c => R.jumpOf(c.options)),
  words: cases.words.map(w => R.shellWord(w)),
  direct: R.directCommand(cases.words),
  quiet: cases.stderr.map(s => R.quietStderr(s)),
  bootstrap: { ssh: R.bootstrapCommand('ssh://me@box'), pw: R.bootstrapCommand('cluster') },
}
fs.rmSync(base, { recursive: true, force: true })
console.log(JSON.stringify(out, null, 1))
