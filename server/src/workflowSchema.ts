import fs from 'node:fs'
import path from 'node:path'
// The platform schema declares draft 2020-12, which the default Ajv build
// does not know; the 2020 build is the same package one entry point over.
import AjvImport from 'ajv/dist/2020.js'
import { type ValidateFunction } from 'ajv'
// ESM/CJS interop: under nodenext the CJS package's type is the module
// namespace, and the constructor sits on .default at runtime. Typed by
// hand because the namespace type has no construct signature.
type AjvCtor = new (opts?: Record<string, unknown>) => { compile: (schema: object) => ValidateFunction }
type AjvError = NonNullable<ValidateFunction['errors']>[number]
const Ajv = ((AjvImport as unknown as { default?: unknown }).default ?? AjvImport) as AjvCtor
import { GATEWAY_BASE, INDEX_BASE } from './config.js'

/**
 * Validation of workflow YAML against the platform's own schema, published
 * at <platform>/workflow.schema.json. The point is executable output: a
 * composed or hand-drafted workflow that merely looks plausible renders a
 * broken DAG and fails at submission, and the schema is the authority on
 * the difference, so it is consulted rather than approximated.
 *
 * The schema is fetched from the deployment's own platform host, which
 * keeps an air-gapped enclave consistent with the platform it actually
 * submits to, and cached beside the index so a restart with the platform
 * unreachable still validates against the last known schema.
 */

const CACHE_FILE = path.join(INDEX_BASE, 'workflow.schema.json')
const REFRESH_MS = 24 * 3600 * 1000

let compiled: ValidateFunction | null = null
let fetchedAt = 0
let schemaDoc: Record<string, any> | null = null
const branchCache = new Map<string, ValidateFunction>()
const AJV_OPTS = { strict: false, allErrors: true, allowUnionTypes: true, unicodeRegExp: false }

function schemaUrl(): string | null {
  try { return `${new URL(GATEWAY_BASE).origin}/workflow.schema.json` } catch { return null }
}

async function loadSchema(): Promise<Record<string, unknown> | null> {
  const url = schemaUrl()
  if (url) {
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(8000) })
      if (res.ok) {
        const doc = await res.json() as Record<string, unknown>
        try { fs.writeFileSync(CACHE_FILE, JSON.stringify(doc)) } catch { /* cache is best-effort */ }
        return doc
      }
    } catch { /* fall through to the disk cache */ }
  }
  try { return JSON.parse(fs.readFileSync(CACHE_FILE, 'utf8')) } catch { return null }
}

async function validator(): Promise<ValidateFunction | null> {
  if (compiled && Date.now() - fetchedAt < REFRESH_MS) return compiled
  const doc = await loadSchema()
  if (!doc) return compiled
  try {
    // strict:false because the platform schema uses if/then step variants
    // and metadata keywords ajv's strict mode complains about; the schema
    // is theirs to shape.
    // unicodeRegExp off because the schema's own interpolation pattern
    // (^\${{.*}}$) is not a legal unicode-mode regex: {{ reads as an
    // incomplete quantifier under the u flag the 2020 dialect defaults to.
    const ajv = new Ajv(AJV_OPTS)
    compiled = ajv.compile(doc)
    fetchedAt = Date.now()
    schemaDoc = doc as Record<string, any>
    branchCache.clear()
  } catch { /* an uncompilable schema leaves the previous one in place */ }
  return compiled
}

export interface SchemaVerdict {
  /** null when no schema could be obtained, which is not a pass. */
  ok: boolean | null
  errors: string[]
  source: string | null
}

/** Validate a parsed workflow document. Errors are formatted for a model
 *  or a person to act on: the path into the document, then the problem. */
export async function validateWorkflowDoc(doc: unknown): Promise<SchemaVerdict> {
  const v = await validator()
  const source = schemaUrl()
  if (!v) return { ok: null, errors: ['The platform workflow schema could not be fetched or found in the cache; the document was not validated.'], source }
  const ok = v(doc)
  if (ok) return { ok: true, errors: [], source }
  const all = v.errors ?? []
  // Errors under the inputs come from a oneOf over every input type, so ajv
  // lists what each type would have needed. Those are replaced by checking
  // each input against the type it declares, which names the field and rule.
  const INPUTS = '/on/execute/inputs'
  const inputs = (doc as any)?.on?.execute?.inputs ?? (doc as any)?.[true as any]?.execute?.inputs
  const precise = inputs && typeof inputs === 'object' && all.some(e => e.instancePath.startsWith(INPUTS)) ? inputErrors(inputs, INPUTS) : null
  const kept = precise?.length ? all.filter(e => !e.instancePath.startsWith(INPUTS)).map(formatError) : all.map(formatError)
  const errors = [...new Set([...kept, ...(precise ?? [])])].slice(0, 20)
  return { ok: false, errors, source }
}

function formatError(e: AjvError): string {
  const where = e.instancePath || '(document root)'
  const extra = e.params && 'additionalProperty' in e.params ? ` ("${(e.params as { additionalProperty: string }).additionalProperty}")` : ''
  const pattern = (e.params as { pattern?: string } | undefined)?.pattern
  const message = e.keyword === 'pattern' && pattern === '^\\${{.*}}$' ? 'must be an expression, written ${{ ... }}' : (e.message ?? 'invalid')
  return `${where}: ${message}${extra}`
}

/** The schema's alternatives for one workflow input, one per input type. */
function inputBranches(): any[] {
  const pp = schemaDoc?.$defs?.inputs?.patternProperties
  const first = pp && Object.values(pp)[0] as { oneOf?: any[] } | undefined
  return Array.isArray(first?.oneOf) ? first.oneOf : []
}

const typesOf = (branch: any): string[] => {
  const t = branch?.properties?.type
  return t?.const !== undefined ? [String(t.const)] : Array.isArray(t?.enum) ? t.enum.map(String) : []
}

/** A validator for one alternative (or the inputs map itself), with the schema's $defs in scope. */
function branchValidator(key: string, schema: object): ValidateFunction | null {
  const hit = branchCache.get(key)
  if (hit) return hit
  try {
    const v = new Ajv(AJV_OPTS).compile({ $schema: schemaDoc?.$schema, $defs: { ...schemaDoc?.$defs, __branch: schema }, $ref: '#/$defs/__branch' })
    branchCache.set(key, v)
    return v
  } catch { return null }
}

/** The properties of a branch that hold nested inputs (a group's items, a step's options). */
const nestedKeys = (branch: any): string[] =>
  Object.entries(branch?.properties ?? {}).filter(([, p]: [string, any]) => p?.$ref === '#/$defs/inputs').map(([k]) => k)

/**
 * A value that may take one of several forms (a width that is a number or a
 * percentage) fails each form; one line per place says what any of them
 * would accept.
 */
function collapse(errs: AjvError[]): AjvError[] {
  const choice = new Set(errs.filter(e => e.keyword === 'oneOf' || e.keyword === 'anyOf').map(e => e.instancePath))
  const out: AjvError[] = []
  for (const at of choice) {
    const parts = errs.filter(e => e.instancePath === at && e.keyword !== 'oneOf' && e.keyword !== 'anyOf').map(e => formatError(e).replace(/^[^:]*: /, ''))
    out.push({ instancePath: at, keyword: 'choice', message: parts.length ? `must satisfy one of: ${[...new Set(parts)].join('; ')}` : 'matches none of the allowed forms', params: {}, schemaPath: '' } as unknown as AjvError)
  }
  return [...errs.filter(e => !choice.has(e.instancePath)), ...out]
}

/** Errors for a form layout: each node against the node type it declares, then its children. */
function layoutErrors(node: unknown, path: string): string[] {
  const alts: any[] = schemaDoc?.$defs?.formLayout?.oneOf ?? []
  if (!alts.length) return []
  if (!node || typeof node !== 'object' || Array.isArray(node)) return [`${path}: must be a layout node (field, stack, grid, or section)`]
  const type = String((node as any).type ?? '')
  const i = alts.findIndex(b => typesOf(b).includes(type))
  if (i < 0) return [`${path}/type: "${type}" is not a layout node type (${alts.flatMap(typesOf).join(', ')})`]
  const kids = (node as any).children
  const v = branchValidator(`layout:${i}`, alts[i])
  const errs = v && !v(Array.isArray(kids) ? { ...node, children: [] } : node) ? collapse(v.errors ?? []) : []
  const out = errs.map(e => formatError({ ...e, instancePath: `${path}${e.instancePath}` }))
  if (Array.isArray(kids)) kids.forEach((k, j) => out.push(...layoutErrors(k, `${path}/children/${j}`)))
  return out
}

/** Errors for an inputs map: each input against the alternative for its declared type, then its nested inputs. */
function inputErrors(inputs: Record<string, any>, at: string): string[] {
  const out: string[] = []
  const branches = inputBranches()
  if (!branches.length) return out
  const meta = inputs.$meta
  if (meta !== undefined) {
    // $meta without its layout, then the layout node by node.
    const v = branchValidator('$meta', { type: 'object', properties: { $meta: schemaDoc?.$defs?.inputs?.properties?.$meta ?? {} } })
    const { layout, ...rest } = (meta && typeof meta === 'object' ? meta : {}) as Record<string, unknown>
    if (v && !v({ $meta: meta && typeof meta === 'object' ? rest : meta })) out.push(...collapse(v.errors ?? []).map(e => formatError({ ...e, instancePath: `${at}${e.instancePath}` })))
    if (layout !== undefined) out.push(...layoutErrors(layout, `${at}/$meta/layout`))
  }
  for (const [name, node] of Object.entries(inputs)) {
    if (name === '$meta') continue
    const path = `${at}/${name}`
    if (!node || typeof node !== 'object') { out.push(`${path}: must be an object describing the input`); continue }
    const type = String((node as any).type ?? '')
    const candidates = branches.map((b, i) => ({ b, i })).filter(({ b }) => typesOf(b).includes(type))
    if (!candidates.length) { out.push(`${path}/type: "${type}" is not an input type the platform knows`); continue }
    // Validate the input itself with its nested inputs left out; they are checked one by one below.
    let best: { errs: AjvError[]; nested: string[] } | null = null
    for (const { b, i } of candidates) {
      const nested = nestedKeys(b)
      const shallow = { ...node } as Record<string, unknown>
      for (const k of nested) if (k in shallow) shallow[k] = {}
      const v = branchValidator(`branch:${i}`, b)
      if (!v) continue
      const errs = v(shallow) ? [] : (v.errors ?? [])
      if (!best || errs.length < best.errs.length) best = { errs, nested }
      if (!errs.length) break
    }
    if (!best) continue
    out.push(...collapse(best.errs).map(e => formatError({ ...e, instancePath: `${path}${e.instancePath}` })))
    for (const k of best.nested) {
      const child = (node as any)[k]
      if (child && typeof child === 'object') out.push(...inputErrors(child, `${path}/${k}`))
    }
  }
  return out
}
