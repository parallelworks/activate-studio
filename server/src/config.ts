import path from 'node:path'
import fs from 'node:fs'
import os from 'node:os'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

// fileURLToPath, not URL.pathname: the macOS app lives under "ACTIVATE Studio.app",
// and a pathname keeps the space percent-encoded.
export const PROJECT_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
export const KB_ROOT = process.env.KB_ROOT ?? ((): string => {
  // Deployments always set KB_ROOT; the fallback is for someone running from
  // a clone on their own machine, where /data is a Linux server convention
  // that does not exist and cannot be created without root. A directory
  // beside the code works everywhere and is seeded on first start.
  const server = '/data/knowledge-base'
  if (fs.existsSync(server)) return server
  return path.join(PROJECT_ROOT, 'knowledge-base')
})()
export const INDEX_BASE = process.env.INDEX_BASE ?? path.join(PROJECT_ROOT, 'index')
// gufi_dir2index mirrors the source tree under INDEX_BASE/gufi/<basename of KB_ROOT>
export const GUFI_INDEX = path.join(INDEX_BASE, 'gufi', path.basename(KB_ROOT))
export const EXTRACT_CACHE = path.join(INDEX_BASE, 'extract')
/** Where findGufiBin looks besides the environment, for tests to replace. */
export interface GufiSearch {
  projectRoot: string
  cwd: string
  /** The directory holding gufi_query on PATH, or null. */
  onPath: () => string | null
  /** The usual install locations, in order. */
  prefixes: string[]
}

function gufiOnPath(): string | null {
  try {
    const found = execFileSync('sh', ['-c', 'command -v gufi_query'], { encoding: 'utf8' }).trim()
    return found ? path.dirname(found) : null
  } catch { return null /* not on PATH */ }
}

/** The container image's /opt/gufi, then GUFI's macOS package, which
 *  installs to /usr/local for all users or ~/usr/local for one. */
export const GUFI_PREFIXES = ['/opt/gufi/bin', '/usr/local/bin', path.join(os.homedir(), 'usr', 'local', 'bin')]

/** GUFI binaries: GUFI_BIN first; then a GUFI bundled with the app (the
 *  macOS app's launcher sets STUDIO_GUFI_PREFIX); installs near the app
 *  (the deploy workflows place gufi-install beside the app directory, and
 *  a source checkout may carry one at the repo root or the current
 *  directory); PATH; then the usual prefixes. Each candidate counts only
 *  where gufi_query exists; with none, /opt/gufi/bin as before. */
export function findGufiBin(env: NodeJS.ProcessEnv = process.env, search: Partial<GufiSearch> = {}): string {
  if (env.GUFI_BIN) return env.GUFI_BIN
  const projectRoot = search.projectRoot ?? PROJECT_ROOT
  const cwd = search.cwd ?? process.cwd()
  const has = (dir: string) => fs.existsSync(path.join(dir, 'gufi_query'))
  const first = [
    ...(env.STUDIO_GUFI_PREFIX ? [path.join(env.STUDIO_GUFI_PREFIX, 'bin')] : []),
    path.join(projectRoot, 'gufi-install', 'bin'),
    path.join(projectRoot, '..', 'gufi-install', 'bin'),
    path.join(cwd, 'gufi-install', 'bin'),
  ]
  for (const c of first) if (has(c)) return c
  const found = (search.onPath ?? gufiOnPath)()
  if (found) return found
  for (const c of search.prefixes ?? GUFI_PREFIXES) if (has(c)) return c
  return '/opt/gufi/bin'
}

export const GUFI_BIN = findGufiBin()

/** GUFI's libraries, beside its bin directory. */
export const gufiLibDir = (bin: string) => path.join(path.dirname(bin), 'lib')
export const GUFI_LIB = gufiLibDir(GUFI_BIN)

/** gufi_vt as a module another SQLite can load (gufi_sqlite3 carries it
 *  already): gufi_vt.so on Linux, gufi_vt.dylib on macOS. */
export const gufiVtModule = (lib: string, platform: NodeJS.Platform = process.platform) =>
  path.join(lib, platform === 'darwin' ? 'gufi_vt.dylib' : 'gufi_vt.so')
export const GUFI_VT_MODULE = gufiVtModule(GUFI_LIB)

/** The variable the platform's dynamic loader searches first. */
export const libraryPathVar = (platform: NodeJS.Platform = process.platform) =>
  platform === 'darwin' ? 'DYLD_LIBRARY_PATH' : 'LD_LIBRARY_PATH'

/**
 * The environment for a program that runs GUFI. Every GUFI tool gets
 * GUFI_BIN, GUFI_BIN first on PATH (gufi_vt runs gufi_query by name), and
 * GUFI's lib directory first on the loader's path, which GUFI's macOS
 * package needs (DYLD_LIBRARY_PATH) and a Linux install already has
 * through its rpath, so there it changes nothing. A system lib directory
 * is never added, since the loader searches it anyway.
 *
 * The indexer scripts (Python) get GUFI_BIN alone, `libraries: false`: a
 * loader path that reaches GUFI's own sqlite breaks Python's sqlite3
 * module (deploy/app.def). They reach GUFI only through gufi_sqlite3,
 * which the macOS app's copy runs without help (deploy/mac/macho_relink.py)
 * and a Linux install through its rpath.
 */
export function gufiEnvFor(
  bin: string,
  base: NodeJS.ProcessEnv,
  extra: Record<string, string> = {},
  opts: { libraries?: boolean; platform?: NodeJS.Platform } = {},
): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...base, ...extra, GUFI_BIN: bin }
  if (opts.libraries === false) return env
  env.PATH = [bin, base.PATH].filter(Boolean).join(path.delimiter)
  const lib = gufiLibDir(bin)
  const system = ['/lib', '/usr/lib'].includes(lib)
  if (!system && fs.existsSync(lib)) {
    const v = libraryPathVar(opts.platform)
    env[v] = [lib, base[v]].filter(Boolean).join(path.delimiter)
  }
  return env
}

/** gufiEnvFor this server's GUFI and environment: for GUFI's tools. */
export const gufiEnv = (extra: Record<string, string> = {}) => gufiEnvFor(GUFI_BIN, process.env, extra)
/** The same for the indexer scripts: GUFI_BIN, and no loader path. */
export const indexerEnv = (extra: Record<string, string> = {}) =>
  gufiEnvFor(GUFI_BIN, process.env, extra, { libraries: false })

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

/** The pw CLI's credential store: PW_CREDENTIALS_DIR, as the CLI reads it,
 *  else ~/.config/pw. */
export function pwCredentialsFile(): string {
  const dir = process.env.PW_CREDENTIALS_DIR || path.join(os.homedir(), '.config', 'pw')
  return path.join(dir, 'credentials')
}

/** The gateway on a platform host. */
export const gatewayFor = (server: string) => `https://${server}/api/openai/v1`

/** The pw CLI's active context (PW_CONTEXT, then currentIdentity in its
 *  credential store) names the platform host the user is actually working
 *  against, so it decides the default gateway; env overrides still win.
 *  This is the value at startup; access.ts moves it when another context
 *  is chosen in Model access. */
function cliContextGateway(): string | null {
  try {
    const data = JSON.parse(fs.readFileSync(pwCredentialsFile(), 'utf8'))
    const name = process.env.PW_CONTEXT || data.currentIdentity
    const server = data.identities?.[name]?.server
    return server ? gatewayFor(server) : null
  } catch {
    return null
  }
}

/** True when the environment names the gateway, which no context choice overrides. */
export const GATEWAY_FROM_ENV = !!(process.env.PW_GATEWAY_URL ?? process.env.OPENAI_BASE_URL)
/** The model gateway, and through its host, the platform. A live binding:
 *  importers read the current value at each use, so choosing another pw CLI
 *  context moves every caller at once. */
export let GATEWAY_BASE = (process.env.PW_GATEWAY_URL ?? process.env.OPENAI_BASE_URL ?? cliContextGateway() ?? 'https://activate.parallel.works/api/openai/v1').replace(/\/$/, '')
/** Point the gateway at another base; a gateway set in the environment stays. */
export function setGatewayBase(url: string): void {
  if (GATEWAY_FROM_ENV || !url) return
  GATEWAY_BASE = url.replace(/\/$/, '')
}
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
export const EXTRACTED_SUFFIXES = new Set(['.pdf', '.docx', '.pptx', '.xlsx', '.doc'])
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
