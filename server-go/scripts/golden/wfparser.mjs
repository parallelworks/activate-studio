// Golden values for internal/workflows/wfparser: what the workflow-parser
// package the Node server uses gives for each workflow YAML listed in
// internal/workflows/wfparser/testdata/cases.json (parsed with the npm yaml
// package, as the server parses workflow YAML): dumpYaml of the document,
// convertToDynamicForm of its inputs, workflowHasUserInputs, getStepLabel
// and getAllDeps for its jobs, and prepareSubmittableValues for each set of
// values the case lists.
// Usage: node wfparser.mjs <cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'

const [, , casesFileArg, repoArg] = process.argv
const casesFile = path.resolve(casesFileArg)
const repo = path.resolve(repoArg)
const cases = JSON.parse(fs.readFileSync(casesFile, 'utf8'))
const require = createRequire(path.join(repo, 'server', 'package.json'))
const { parse } = await import(require.resolve('yaml'))
// The package is ESM-only, with no require entry: import its "." export.
const wpDir = path.join(repo, 'server', 'node_modules', '@parallelworks', 'workflow-parser')
const wpPkg = JSON.parse(fs.readFileSync(path.join(wpDir, 'package.json'), 'utf8'))
const entry = wpPkg.exports?.['.']?.import ?? wpPkg.exports?.['.']?.default ?? wpPkg.exports?.['.'] ?? wpPkg.module
const wp = await import(pathToFileURL(path.join(wpDir, typeof entry === 'string' ? entry : entry.default)).href)
const attempt = fn => { try { return { out: fn() } } catch (e) { return { error: String(e?.message ?? e) } } }
const json = v => v === undefined ? undefined : JSON.parse(JSON.stringify(v))

const out = cases.docs.map(c => {
  const text = fs.readFileSync(path.resolve(path.dirname(casesFile), c.file), 'utf8')
  const doc = parse(text)
  const jobs = doc?.jobs ?? {}
  const inputs = doc?.on?.execute?.inputs
  return {
    file: c.file,
    dump: attempt(() => wp.dumpYaml(doc ?? {})),
    hasInputs: wp.workflowHasUserInputs(doc),
    form: attempt(() => json(inputs ? wp.convertToDynamicForm(inputs) : null) ?? null),
    labels: Object.fromEntries(Object.entries(jobs).map(([id, def]) =>
      [id, attempt(() => (def?.steps ?? []).map((s, i) => wp.getStepLabel(s, i)))])),
    deps: Object.fromEntries(Object.keys(jobs).map(id => [id, attempt(() => [...wp.getAllDeps(id, jobs)])])),
    submit: (c.submit ?? []).map(s => attempt(() => json(wp.prepareSubmittableValues(doc, s.values, s.user)) ?? null)),
  }
})
const values = (cases.values ?? []).map(v => attempt(() => wp.dumpYaml(v)))
console.log(JSON.stringify({ docs: out, values }, null, 1))
