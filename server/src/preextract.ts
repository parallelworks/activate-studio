/**
 * Office documents to Markdown for the extract cache, ahead of enrich.py.
 *
 * Word, PowerPoint and Excel files (plus legacy .doc) convert through
 * downmark (the pure-Go document converter compiled to WebAssembly, shipped
 * as an npm dependency) into `$INDEX_BASE/extract/<rel>.txt`. enrich.py
 * already reuses any cache entry at least as new as its source, so it picks
 * these up as-is and indexes them; a file downmark cannot read gets no entry
 * and falls through to enrich.py's standard-library OOXML pass, the only
 * reader left there for these formats. PDFs stay with pdftotext and OCR in
 * enrich.py; poppler is present for page previews regardless and downmark
 * has no OCR.
 *
 * Markdown rather than flat text because the downstream consumers are the
 * chat model, full-text snippets, and the viewer: headings, tables and slide
 * boundaries survive, and the viewer renders them.
 *
 * Runs as a child process (see runPreExtract) or as a CLI from reindex.sh:
 *   node server/dist/preextract.js --kb-root DIR --extract-cache DIR [--subdir REL] [--no-recurse]
 * The wasm is single-threaded and blocks its thread per document, so the
 * server never converts on its own event loop.
 */
import { fork } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

export const OFFICE_MARKDOWN_SUFFIXES = new Set(['.docx', '.pptx', '.xlsx', '.doc'])

// Same cap as enrich.py's MAX_TEXT: the index holds the head of a huge
// workbook, full-text search covers the rest of nothing.
const MAX_TEXT = 500_000
// downmark refuses OOXML archives above this anyway; skip the read.
const MAX_INPUT_BYTES = 64 * 1024 * 1024
// Cache entries written by an older converter (or by enrich.py's flat-text
// readers before this existed) are refreshed once per downmark version; the
// marker is written after a full-scope pass completes.
const VERSION_MARKER = '.downmark-version'

export interface PreExtractOptions {
  kbRoot: string
  cacheRoot: string
  subdir?: string
  noRecurse?: boolean
  excludeDirs?: Set<string>
  log?: (msg: string) => void
}

export interface PreExtractSummary {
  scanned: number
  converted: number
  reused: number
  /** Converted, but downmark reported content it could not carry over. */
  warned: number
  failed: number
  ms: number
}

const DEFAULT_EXCLUDE = new Set([
  '.git', 'node_modules', '.venv', 'venv', '__pycache__', 'dist', 'build',
  '.cache', '.pytest_cache', 'screenshots', '.ipynb_checkpoints',
])

async function readMarker(cacheRoot: string): Promise<string> {
  try { return (await fsp.readFile(path.join(cacheRoot, VERSION_MARKER), 'utf8')).trim() } catch { return '' }
}

/** Convert every Office document under the scope whose cache entry is
 *  missing or stale. Never throws for a single document; failures are
 *  counted and logged so enrich.py's readers take over for those files. */
export async function preExtract(opts: PreExtractOptions): Promise<PreExtractSummary> {
  const t0 = Date.now()
  const log = opts.log ?? (() => {})
  const exclude = opts.excludeDirs ?? DEFAULT_EXCLUDE
  const { convert, version } = await import('@giraffesyo/downmark')
  const ver = await version()
  const refreshAll = (await readMarker(opts.cacheRoot)) !== ver
  const summary: PreExtractSummary = { scanned: 0, converted: 0, reused: 0, warned: 0, failed: 0, ms: 0 }

  const sub = (opts.subdir ?? '').replace(/^\/+|\/+$/g, '')
  const start = sub ? path.join(opts.kbRoot, sub) : opts.kbRoot
  if (!path.resolve(start).startsWith(path.resolve(opts.kbRoot))) throw new Error('subdir escapes the knowledge base')

  const walk = async (dir: string, rel: string): Promise<void> => {
    let entries
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
    entries.sort((a, b) => a.name.localeCompare(b.name))
    for (const e of entries) {
      const abs = path.join(dir, e.name)
      const childRel = rel ? `${rel}/${e.name}` : e.name
      if (e.isDirectory()) {
        if (!opts.noRecurse && !exclude.has(e.name) && !e.name.startsWith('.')) await walk(abs, childRel)
        continue
      }
      if (!e.isFile() || !OFFICE_MARKDOWN_SUFFIXES.has(path.extname(e.name).toLowerCase())) continue
      summary.scanned++
      let st: fs.Stats
      try { st = await fsp.stat(abs) } catch { continue }
      const cacheFile = path.join(opts.cacheRoot, childRel + '.txt')
      if (!refreshAll) {
        try {
          const cst = await fsp.stat(cacheFile)
          if (cst.mtimeMs >= st.mtimeMs && cst.size > 0) { summary.reused++; continue }
        } catch { /* no entry yet */ }
      }
      if (st.size > MAX_INPUT_BYTES) { summary.failed++; log(`preextract: ${childRel}: ${st.size} bytes exceeds the converter's input limit`); continue }
      try {
        const data = await fsp.readFile(abs)
        const { markdown, warnings } = await convert(data, { filename: e.name })
        const text = markdown.slice(0, MAX_TEXT)
        if (!text.trim()) { summary.failed++; log(`preextract: ${childRel}: no text`); continue }
        // A conversion can succeed and still lose content; say what, so a
        // document that indexes short is explained in the log rather than
        // discovered by a reader. The Markdown is still the best text we have.
        if (warnings.length) {
          summary.warned++
          for (const w of warnings.slice(0, 5)) log(`preextract: ${childRel}: ${w.code}${w.location ? ` (${w.location})` : ''}: ${w.message}`)
          if (warnings.length > 5) log(`preextract: ${childRel}: ${warnings.length - 5} more warnings`)
        }
        await fsp.mkdir(path.dirname(cacheFile), { recursive: true })
        const tmp = `${cacheFile}.${process.pid}.tmp`
        await fsp.writeFile(tmp, text)
        await fsp.rename(tmp, cacheFile)
        summary.converted++
      } catch (err) {
        summary.failed++
        log(`preextract: ${childRel}: ${String((err as Error).message ?? err).slice(0, 200)}`)
      }
    }
  }
  await walk(start, sub)

  // Only a pass over the whole tree proves every entry is current.
  if (refreshAll && !sub && !opts.noRecurse) {
    await fsp.mkdir(opts.cacheRoot, { recursive: true })
    await fsp.writeFile(path.join(opts.cacheRoot, VERSION_MARKER), ver + '\n')
  }
  summary.ms = Date.now() - t0
  return summary
}

/** Run preExtract in a child process so a long conversion never blocks the
 *  server. Resolves to the summary, or null when the child failed; callers
 *  continue either way because enrich.py covers the same files. */
export function runPreExtract(opts: Omit<PreExtractOptions, 'log' | 'excludeDirs'>, log: (msg: string) => void = () => {}): Promise<PreExtractSummary | null> {
  const self = fileURLToPath(import.meta.url)
  const script = path.join(path.dirname(self), 'preextract' + path.extname(self))
  const args = ['--kb-root', opts.kbRoot, '--extract-cache', opts.cacheRoot]
  if (opts.subdir) args.push('--subdir', opts.subdir)
  if (opts.noRecurse) args.push('--no-recurse')
  return new Promise(resolve => {
    // fork inherits execArgv, so the tsx loader used in development carries
    // over; in production the script is plain JS under dist/.
    const child = fork(script, args, { stdio: ['ignore', 'pipe', 'pipe', 'ipc'], timeout: 30 * 60_000 })
    let out = ''
    let err = ''
    child.stdout?.on('data', d => { out += d })
    child.stderr?.on('data', d => { err += d })
    child.on('error', e => { log(`preextract: ${e.message}`); resolve(null) })
    child.on('exit', code => {
      for (const line of err.split('\n')) if (line.trim()) log(line.trim())
      if (code !== 0) { log(`preextract: exited ${code}`); resolve(null); return }
      try { resolve(JSON.parse(out.trim().split('\n').pop() ?? '')) } catch { resolve(null) }
    })
  })
}

function parseArgs(argv: string[]): PreExtractOptions {
  const opts: PreExtractOptions = { kbRoot: '', cacheRoot: '' }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--kb-root') opts.kbRoot = argv[++i] ?? ''
    else if (a === '--extract-cache') opts.cacheRoot = argv[++i] ?? ''
    else if (a === '--subdir') opts.subdir = argv[++i] ?? ''
    else if (a === '--no-recurse') opts.noRecurse = true
    else throw new Error(`unknown argument: ${a}`)
  }
  if (!opts.kbRoot || !opts.cacheRoot) throw new Error('usage: preextract --kb-root DIR --extract-cache DIR [--subdir REL] [--no-recurse]')
  return opts
}

const invokedDirectly = process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href
if (invokedDirectly) {
  let opts: PreExtractOptions
  try { opts = parseArgs(process.argv.slice(2)) } catch (e) { console.error(String((e as Error).message)); process.exit(2) }
  preExtract({ ...opts!, log: m => console.error(m) })
    .then(s => { console.log(JSON.stringify(s)); process.exit(0) })
    .catch(e => { console.error(`preextract: ${String((e as Error).message ?? e)}`); process.exit(1) })
}
