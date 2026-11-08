// Golden values for internal/jsonv: JSON.stringify(JSON.parse(s)) and
// JSON.stringify of the npm yaml package's parse(s), for each case in
// internal/jsonv/testdata/cases.json, as the Node server reads platform
// JSON and workflow YAML.
// Usage: node jsonv.mjs <cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'

const [, , casesFileArg, repoArg] = process.argv
const cases = JSON.parse(fs.readFileSync(path.resolve(casesFileArg), 'utf8'))
const require = createRequire(path.join(path.resolve(repoArg), 'server', 'package.json'))
const { parse } = await import(require.resolve('yaml'))
const attempt = fn => { try { return { out: fn() } } catch (e) { return { error: true } } }
console.log(JSON.stringify({
  json: cases.json.map(s => attempt(() => JSON.stringify(JSON.parse(s)))),
  yaml: cases.yaml.map(s => attempt(() => JSON.stringify(parse(s) ?? null))),
}, null, 1))
