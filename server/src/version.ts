import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { PROJECT_ROOT } from './config.js'

/**
 * What build is running. The question this answers is "did the deploy
 * actually land", which is otherwise guesswork: a bundle that failed to
 * refresh and one that succeeded look identical from the interface.
 *
 * A deployed Studio has no git checkout, so the build stamps itself into
 * build-info.json and that is the authority. Running from the repository
 * there is no stamp, so the tag is read from git and the answer stays
 * truthful for whoever is developing.
 */
export interface BuildInfo {
  /** Release tag, e.g. v1.0. "dev" when nothing has been tagged yet. */
  version: string
  /** Short commit the build came from, when known. */
  commit: string | null
  /** When the bundle was built, ISO, when known. */
  builtAt: string | null
  /** When this process started, so a stale server is visible as such. */
  startedAt: string
  /** Where the answer came from, so an unexpected value can be traced. */
  source: 'bundle' | 'git' | 'package'
}

const STAMP_FILE = path.join(PROJECT_ROOT, 'build-info.json')
const startedAt = new Date().toISOString()

function fromStamp(): BuildInfo | null {
  try {
    const raw = JSON.parse(fs.readFileSync(STAMP_FILE, 'utf8')) as Partial<BuildInfo>
    if (!raw.version) return null
    return {
      version: String(raw.version),
      commit: raw.commit ? String(raw.commit) : null,
      builtAt: raw.builtAt ? String(raw.builtAt) : null,
      startedAt,
      source: 'bundle',
    }
  } catch { return null }
}

function git(args: string[]): string | null {
  try {
    return execFileSync('git', args, { cwd: PROJECT_ROOT, timeout: 3000, stdio: ['ignore', 'pipe', 'ignore'] })
      .toString().trim() || null
  } catch { return null }
}

function fromGit(): BuildInfo | null {
  const commit = git(['rev-parse', '--short', 'HEAD'])
  if (!commit) return null
  // Exact tag if this commit is one, otherwise the last tag with a distance
  // suffix, which reads as "past v1.0" rather than claiming to be it.
  const described = git(['describe', '--tags', '--always', '--dirty'])
  return {
    version: described && /^v/.test(described) ? described : 'dev',
    commit,
    builtAt: git(['log', '-1', '--format=%cI']),
    startedAt,
    source: 'git',
  }
}

function fromPackage(): BuildInfo {
  let version = '0.0.0'
  try {
    version = String(JSON.parse(fs.readFileSync(path.join(PROJECT_ROOT, 'package.json'), 'utf8')).version ?? version)
  } catch { /* keep the placeholder */ }
  return { version: `v${version}`, commit: null, builtAt: null, startedAt, source: 'package' }
}

// Resolved once: the answer cannot change without the process restarting,
// and shelling out to git on every request would be silly.
let cached: BuildInfo | null = null

export function buildInfo(): BuildInfo {
  if (!cached) cached = fromStamp() ?? fromGit() ?? fromPackage()
  return cached
}
