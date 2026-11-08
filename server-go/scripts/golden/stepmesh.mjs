// Golden model-cache files for internal/stepmesh: what Node's model.ts
// writes to INDEX_BASE/model-cache for each STEP file, as its byte length
// and SHA-256. Each file is meshed in its own Node process, as the Go
// server meshes each in a fresh instance. The files are occt-import-js's
// own test files. Usage:
//   node stepmesh.mjs <repo root> <file under occt-import-js/test/testfiles>...
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const [, , repoArg, ...files] = process.argv
const root = path.resolve(repoArg)
const testfiles = path.join(root, 'server/node_modules/occt-import-js/test/testfiles')

if (process.env.STEPMESH_ONE) {
  const kb = path.dirname(process.env.STEPMESH_ONE)
  process.env.KB_ROOT = kb
  process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'golden-step-'))
  const { modelFile } = await import(path.join(root, 'server/dist/model.js'))
  const out = await modelFile(path.basename(process.env.STEPMESH_ONE))
  const b = fs.readFileSync(out)
  process.stdout.write(JSON.stringify({ bytes: b.length, sha256: crypto.createHash('sha256').update(b).digest('hex') }))
  fs.rmSync(process.env.INDEX_BASE, { recursive: true, force: true })
} else {
  const out = {}
  for (const f of files) {
    const res = execFileSync(process.execPath, [process.argv[1], repoArg], {
      env: { ...process.env, STEPMESH_ONE: path.join(testfiles, f) }, encoding: 'utf8',
    })
    out[f] = JSON.parse(res)
  }
  process.stdout.write(JSON.stringify(out, null, 1) + '\n')
}
