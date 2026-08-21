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
/** GUFI binaries: env override first, then installs near the app (the
 *  deploy workflows place gufi-install beside the app directory, and a
 *  source checkout may carry one at the repo root or the current
 *  directory), then PATH, then the container image's /opt/gufi. */
export const GUFI_BIN = (() => {
  if (process.env.GUFI_BIN) return process.env.GUFI_BIN
  const nearby = [
    path.join(PROJECT_ROOT, 'gufi-install', 'bin'),
    path.join(PROJECT_ROOT, '..', 'gufi-install', 'bin'),
    path.join(process.cwd(), 'gufi-install', 'bin'),
  ]
  for (const c of nearby) {
    if (fs.existsSync(path.join(c, 'gufi_query'))) return c
  }
  try {
    const found = execFileSync('sh', ['-c', 'command -v gufi_query'], { encoding: 'utf8' }).trim()
    if (found) return path.dirname(found)
  } catch { /* not on PATH */ }
  return '/opt/gufi/bin'
})()

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

/**
 * Where the pw CLI lives. A container deployment has one only if the
 * binary was bound in, and it is bound at an absolute path rather than
 * placed on PATH, so every caller has to be told where to look. Bare "pw"
 * remains the default for a deployment running directly on a host.
 */
export const PW_CLI = process.env.PW_CLI || 'pw'

/**
 * A model served beside this deployment, on the same node: an OpenAI-
 * compatible base URL reachable over the loopback interface.
 *
 * This is deliberately separate from the gateway rather than a
 * replacement for it. Pointing the whole deployment at the local serve
 * makes that model the only one anybody can pick, which hides the
 * platform's provider models; listing both means a private model on the
 * node and the platform providers appear in one picker and either can be
 * called in the same conversation.
 */
export const SIDECAR_BASE = (process.env.SIDECAR_MODEL_URL ?? '').replace(/\/$/, '')
export const SIDECAR_KEY = process.env.SIDECAR_MODEL_KEY || 'local'

/**
 * The platform endpoint this deployment registered its own model under,
 * when it registered one. Knowing the name lets the picker drop the
 * gateway's copy of that model precisely, rather than by matching model
 * names and risking the removal of an identically named model served by
 * somebody else's session.
 */
export const SIDECAR_ENDPOINT = process.env.SIDECAR_ENDPOINT || ''

/** True when a failure is the CLI being absent rather than a command that
 *  ran and failed, which the assistant should report differently. */
export function isMissingCli(err: unknown): boolean {
  return (err as NodeJS.ErrnoException)?.code === 'ENOENT'
}

export const NO_CLI_MESSAGE =
  'The pw CLI is not available to this deployment, so platform actions (workflows, clusters, status) cannot run from here. '
  + 'Bind the CLI and its credentials into the container, or set PW_CLI to its path.'

export function gufiAvailable(): boolean {
  try {
    return fs.existsSync(path.join(GUFI_BIN, 'gufi_query')) && fs.existsSync(GUFI_INDEX)
  } catch {
    return false
  }
}
