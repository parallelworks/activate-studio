// Golden values for internal/workflows' schema validator: the errors ajv
// gives, as workflowSchema.ts formats them, for each document in
// internal/workflows/testdata/schema-cases.json against the platform
// schema fixture beside it.
// Usage: node wfschema.mjs <testdata dir> <repo root>  (prints JSON)
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'

const [, , dirArg, repoArg] = process.argv
const dir = path.resolve(dirArg)
const require = createRequire(path.join(path.resolve(repoArg), 'server', 'package.json'))
const Ajv = require('ajv/dist/2020.js').default ?? require('ajv/dist/2020.js')
const schema = JSON.parse(fs.readFileSync(path.join(dir, 'workflow.schema.json'), 'utf8'))
const v = new Ajv({ strict: false, allErrors: true, allowUnionTypes: true, unicodeRegExp: false }).compile(schema)
const cases = JSON.parse(fs.readFileSync(path.join(dir, 'schema-cases.json'), 'utf8'))
console.log(JSON.stringify(cases.map(doc => {
  if (v(doc)) return { ok: true, errors: [] }
  return { ok: false, errors: (v.errors ?? []).slice(0, 20).map(e => {
    const where = e.instancePath || '(document root)'
    const extra = e.params && 'additionalProperty' in e.params ? ` ("${e.params.additionalProperty}")` : ''
    return `${where}: ${e.message ?? 'invalid'}${extra}`
  }) }
}), null, 1))
