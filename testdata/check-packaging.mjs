/**
 * Assert the deployed server tree can still convert documents.
 *
 * The container and the bundle both ship what `pnpm deploy --prod --legacy`
 * produces, not the workspace. Document conversion depends on two things
 * surviving that step: the platform package's native binary (as a file that
 * is still executable), and the wasm the fallback path and the image's own
 * proof step look for. Neither is exercised by importing the library in a
 * dev checkout, and both are the kind of thing a packaging change breaks
 * quietly — the app keeps starting, and documents index as their filename.
 *
 *     node testdata/check-packaging.mjs <deployed-server-dir>
 */
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'

const target = process.argv[2]
if (!target) {
  console.error('usage: check-packaging.mjs <deployed-server-dir>')
  process.exit(2)
}

const root = path.resolve(target)
const failures = []
const ok = (msg) => console.log(`  ok  ${msg}`)
const fail = (msg) => { failures.push(msg); console.error(`FAIL  ${msg}`) }

// The path deploy/app.def's proof step checks before sealing the image.
// Keep the two in step: if this moves, that must move with it.
const wasm = path.join(root, 'node_modules', '@giraffesyo', 'downmark', 'dist', 'downmark.wasm')
if (fs.existsSync(wasm)) ok(`wasm present (${(fs.statSync(wasm).size / 1e6).toFixed(1)} MB): ${path.relative(root, wasm)}`)
else fail(`wasm missing at ${wasm} — deploy/app.def's proof step checks this exact path`)

const preextract = path.join(root, 'dist', 'preextract.js')
if (fs.existsSync(preextract)) ok('preextract.js present (reindex.sh runs this)')
else fail(`preextract.js missing at ${preextract} — reindex.sh would fall back to the stdlib reader`)

const { binaryPath } = await import(path.join(root, 'node_modules', '@giraffesyo', 'downmark', 'dist', 'index.js'))
const bin = binaryPath()
if (!bin) {
  // Only a platform with no published package may legitimately land here.
  fail(`no native binary resolved for ${process.platform}/${process.arch}: PDFs and OCR would fall back to enrich.py`)
} else if (!fs.existsSync(bin)) {
  fail(`binaryPath() points at ${bin}, which does not exist`)
} else {
  ok(`native binary resolved: ${path.relative(root, bin)}`)
  // npm preserves modes, but a copy, a container layer or an archive
  // round-trip can drop the executable bit; spawn then fails at run time.
  const mode = fs.statSync(bin).mode
  if (mode & 0o111) ok('native binary is executable')
  else fail(`native binary is not executable (mode ${(mode & 0o777).toString(8)})`)

  try {
    const version = execFileSync(bin, ['-version'], { encoding: 'utf8', timeout: 30_000 }).trim()
    ok(`native binary runs: ${version}`)
  } catch (err) {
    fail(`native binary will not execute: ${String(err.message).slice(0, 200)}`)
  }
}

if (failures.length) {
  console.error(`\n${failures.length} packaging check(s) failed for ${root}`)
  process.exit(1)
}
console.log(`\npackaging ok for ${root}`)
