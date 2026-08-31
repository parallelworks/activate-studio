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
    const ajv = new Ajv({ strict: false, allErrors: true, allowUnionTypes: true, unicodeRegExp: false })
    compiled = ajv.compile(doc)
    fetchedAt = Date.now()
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
  const errors = (v.errors ?? []).slice(0, 20).map(e => {
    const where = e.instancePath || '(document root)'
    const extra = e.params && 'additionalProperty' in e.params ? ` ("${(e.params as { additionalProperty: string }).additionalProperty}")` : ''
    return `${where}: ${e.message ?? 'invalid'}${extra}`
  })
  return { ok: false, errors, source }
}
