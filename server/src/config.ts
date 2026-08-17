import path from 'node:path'
import fs from 'node:fs'
import os from 'node:os'
import { execFileSync } from 'node:child_process'

export const KB_ROOT = process.env.KB_ROOT ?? '/data/knowledge-base'
export const PROJECT_ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..', '..')
export const INDEX_BASE = process.env.INDEX_BASE ?? path.join(PROJECT_ROOT, 'index')
// gufi_dir2index mirrors the source tree under INDEX_BASE/gufi/<basename of KB_ROOT>
export const GUFI_INDEX = path.join(INDEX_BASE, 'gufi', path.basename(KB_ROOT))
export const EXTRACT_CACHE = path.join(INDEX_BASE, 'extract')
export const GUFI_BIN = process.env.GUFI_BIN ?? '/opt/gufi/bin'

export const PORT = Number(process.env.PORT ?? 4080)
export const HOST = process.env.HOST ?? '0.0.0.0'

// Any OpenAI-compatible backend works; the ACTIVATE gateway is the default.
/** Indexer scripts use 3.10+ syntax; some clusters still default python3
 *  to 3.9, so prefer PYTHON_BIN, then the newest versioned interpreter on
 *  PATH, then plain python3. */
export const PYTHON_BIN = (() => {
  const explicit = process.env.PYTHON_BIN
  if (explicit) return explicit
  for (const cand of ['python3.13', 'python3.12', 'python3.11', 'python3.10', 'python3']) {
    try {
      const v = execFileSync(cand, ['-c', 'import sys; print(sys.version_info >= (3, 10))'], { encoding: 'utf8' }).trim()
      if (v === 'True') return cand
    } catch { /* try the next one */ }
  }
  return 'python3'
})()

/** The pw CLI's active context (PW_CONTEXT, then currentIdentity in its
 *  credential store) names the platform host the user is actually working
 *  against, so it decides the default gateway; env overrides still win.
 *  Read once at startup, like the rest of this module. */
function cliContextGateway(): string | null {
  try {
    const file = path.join(os.homedir(), '.config', 'pw', 'credentials')
    const data = JSON.parse(fs.readFileSync(file, 'utf8'))
    const name = process.env.PW_CONTEXT || data.currentIdentity
    const server = data.identities?.[name]?.server
    return server ? `https://${server}/api/openai/v1` : null
  } catch {
    return null
  }
}

export const GATEWAY_BASE = (process.env.PW_GATEWAY_URL ?? process.env.OPENAI_BASE_URL ?? cliContextGateway() ?? 'https://activate.parallel.works/api/openai/v1').replace(/\/$/, '')
export const PW_API_KEY = process.env.PW_API_KEY ?? process.env.OPENAI_API_KEY ?? ''

// Directory names never entered when browsing, indexing, or searching.
export const EXCLUDE_DIRS = new Set([
  '.git', 'node_modules', '.venv', 'venv', '__pycache__', 'dist', 'build',
  '.cache', '.pytest_cache', 'screenshots', '.ipynb_checkpoints',
])
// File suffixes treated as opaque binaries (no preview, no extraction).
export const EXCLUDE_FILE_SUFFIXES = ['.sif', '.pyc', '.so', '.o', '.zip', '.tar', '.gz', '.tgz']

export const TEXT_SUFFIXES = new Set([
  '.md', '.txt', '.py', '.sh', '.yaml', '.yml', '.json', '.csv', '.tsv', '.xml',
  '.html', '.css', '.js', '.ts', '.tsx', '.svg', '.toml', '.cfg', '.ini', '.def', '.mjs', '.sql',
  '.log', '.conf', '.env', '.service',
])
export const EXTRACTED_SUFFIXES = new Set(['.pdf', '.docx', '.pptx', '.xlsx'])
export const IMAGE_SUFFIXES = new Set(['.png', '.jpg', '.jpeg', '.gif', '.webp'])
export const MODEL_SUFFIXES = new Set(['.stl', '.step', '.stp'])

export const MAX_PREVIEW_BYTES = 512 * 1024
export const MAX_TOOL_ITERATIONS = 24

export function gufiAvailable(): boolean {
  try {
    return fs.existsSync(path.join(GUFI_BIN, 'gufi_query')) && fs.existsSync(GUFI_INDEX)
  } catch {
    return false
  }
}
