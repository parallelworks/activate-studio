// Golden values for internal/userlibs: the remote build scripts of
// remoteBuild.ts, copied verbatim into internal/userlibs/scripts, and the
// pure functions of userLibraries.ts, remoteBuild.ts and remoteEmbed.ts on
// the cases in internal/userlibs/testdata/cases.json.
// Usage: node userlibs.mjs <cases.json> <repo root>             (prints JSON)
//        node userlibs.mjs --scripts <dir> <repo root>          (writes the scripts)
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const [, , first, second, repoArg] = process.argv
const writeScripts = first === '--scripts'
const repo = path.resolve(writeScripts ? repoArg : second)
const base = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-userlibs-'))
process.env.INDEX_BASE = path.join(base, 'ix')
process.env.KB_ROOT = path.join(base, 'kb')
delete process.env.AUTH_JWKS_URL
const B = await import(path.join(repo, 'server/dist/remoteBuild.js'))
const scripts = { build_check: B.BUILD_CHECK, build_run: B.BUILD_RUN, build_start: B.BUILD_START, build_status: B.BUILD_STATUS, embed_probe: B.EMBED_PROBE }
if (writeScripts) {
  fs.mkdirSync(second, { recursive: true })
  for (const [name, text] of Object.entries(scripts)) fs.writeFileSync(path.join(second, `${name}.sh`), text)
  fs.rmSync(base, { recursive: true, force: true })
  process.exit(0)
}
const U = await import(path.join(repo, 'server/dist/userLibraries.js'))
const E = await import(path.join(repo, 'server/dist/remoteEmbed.js'))
const cases = JSON.parse(fs.readFileSync(first, 'utf8'))
const tryIt = f => { try { return { ok: f() } } catch (e) { return { error: e.message, status: e.status ?? null } } }
const out = {
  scripts,
  phases: B.PHASE_LABEL,
  keyValues: cases.keyValues.map(t => B.keyValues(t)),
  statuses: cases.statuses.map(t => B.parseStatus(t)),
  vectorsMade: cases.manifests.map(m => B.vectorsMade(m)),
  gaps: cases.manifests.map(m => B.buildGaps(m)),
  dests: cases.dests.map(a => tryIt(() => U.sshDest(a))),
  sshSpecs: cases.sshSpecs.map(a => tryIt(() => U.sshSpec(a))),
  pwSpecs: cases.pwSpecs.map(a => tryIt(() => U.pwSpec(a))),
  advice: cases.advice.map(a => U.sshAdvice(a.message, { destination: a.destination, port: a.port })),
  indexDirs: cases.indexDirs.map(([home, source]) => U.defaultIndexDir(home, source)),
  manifestFrom: cases.logs.map(l => U.manifestFrom(l)),
  public: cases.defs.map(d => U.publicUserLibrary(d)),
  embed: cases.platforms.map(p => ({ files: E.embedFiles(p), version: E.embedVersion(p), megabytes: E.embedMegabytes(p) })),
  readers: { files: E.readerFiles(), version: E.readersVersion() },
}
fs.rmSync(base, { recursive: true, force: true })
console.log(JSON.stringify(out, null, 1))
