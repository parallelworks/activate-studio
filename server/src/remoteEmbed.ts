import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { INDEX_BASE, PROJECT_ROOT, PYTHON_BIN } from './config.js'
import { ONNX_DIR } from './embedWorker.js'
import { remoteRun, remoteRunOnce, siteName } from './remoteExec.js'

/**
 * The Studio's ONNX embedding model for a build on another system.
 *
 * Search embeds the query with the ONNX model (all-MiniLM-L6-v2, int8, mean
 * pooling) and the one-pass walk matches only vectors made with it, so a
 * build on another system makes its vectors with the same model: there,
 * onnxruntime, tokenizers and numpy for the Python the build runs, and
 * model.onnx and tokenizer.json, as pinned in indexer/embed_deps.json. They
 * are kept in ~/.activate-studio/embed/<version>, one folder per platform
 * and set of pins (embedVersion), and installed once by fetch_embed.py,
 * with no network, from files kept in <version>.files until then.
 *
 * The first build that needs them sends those files from here while it
 * runs (sendEmbed): the wheels from the bundle's folder (app/indexer/wheels,
 * which the platform bundle carries for Linux x86_64) or the Studio's own
 * (INDEX_BASE/embed-wheels, where fetch_embed.py --download puts them when
 * this computer reaches the package index), and the model from the
 * Studio's own. A file that cannot be had here, fetch_embed.py there
 * downloads where that system reaches the network. Without them the build
 * embeds with the GGUF model where that is there (remoteBuild.ts).
 */

export const PINS_FILE = path.join(PROJECT_ROOT, 'indexer', 'embed_deps.json')

interface Pin { url: string; sha256: string; size: number }
interface Pins { wheels: Record<string, Record<string, Pin>>; model: { id: string; files: Record<string, Pin> } }
let pinned: Pins | null = null
const pins = (): Pins => (pinned ??= JSON.parse(fs.readFileSync(PINS_FILE, 'utf8')) as Pins)

/** A file a build there needs: its name as fetch_embed.py's cache keeps it
 *  (the first 16 characters of its SHA-256, then the name it is published
 *  under), its digest and size, and for the model's files, the name it has
 *  in the Studio's own model folder. */
export interface EmbedFile { name: string; sha256: string; size: number; local?: string }

const cacheName = (p: Pin) => `${p.sha256.slice(0, 16)}-${p.url.slice(p.url.lastIndexOf('/') + 1)}`

/** A platform as fetch_embed.py names it: linux-x86_64-cp312. */
export const PLATFORM = /^[a-z0-9]+-[a-z0-9_]+-cp\d{2,3}$/

/** The files a build on this platform needs, the wheels and then the
 *  model's; null when no wheels are pinned for it (a Python older than
 *  3.11, say). */
export function embedFiles(platform: string): EmbedFile[] | null {
  const wheels = PLATFORM.test(platform) ? pins().wheels[platform] : undefined
  if (!wheels) return null
  return [
    ...Object.values(wheels).map(p => ({ name: cacheName(p), sha256: p.sha256, size: p.size })),
    ...Object.entries(pins().model.files).map(([local, p]) => ({ name: cacheName(p), sha256: p.sha256, size: p.size, local })),
  ]
}

/** The folder name of a platform's model and packages there: the platform
 *  and a digest of their pins, so new pins install beside the old. */
export function embedVersion(platform: string): string | null {
  const wheels = PLATFORM.test(platform) ? pins().wheels[platform] : undefined
  if (!wheels) return null
  return `${platform}-${createHash('sha256').update(JSON.stringify({ wheels, model: pins().model })).digest('hex').slice(0, 12)}`
}

/** Megabytes a first build sends to a system of this platform. */
export function embedMegabytes(platform: string): number {
  return Math.round((embedFiles(platform) ?? []).reduce((n, f) => n + f.size, 0) / 1e6)
}

/** Folders here that may hold the files, as fetch_embed.py's cache names
 *  them: the bundle's, then the Studio's own. */
export const wheelDirs = () => [path.join(PROJECT_ROOT, 'indexer', 'wheels'), path.join(INDEX_BASE, 'embed-wheels')]

/** Files whose digest was checked, by path, while their size and time stand. */
const checked = new Map<string, string>()
async function holds(p: string, f: EmbedFile): Promise<boolean> {
  let st: fs.Stats
  try { st = await fsp.stat(p) } catch { return false }
  if (!st.isFile() || st.size !== f.size) return false
  const stamp = `${f.sha256}:${st.size}:${st.mtimeMs}`
  if (checked.get(p) === stamp) return true
  const h = createHash('sha256')
  for await (const chunk of fs.createReadStream(p)) h.update(chunk as Buffer)
  if (h.digest('hex') !== f.sha256) return false
  checked.set(p, stamp)
  return true
}

/** Where each file is here, checked against its pinned digest; null for
 *  one that is not here. */
export async function findLocal(files: EmbedFile[]): Promise<(string | null)[]> {
  return Promise.all(files.map(async f => {
    for (const d of wheelDirs()) if (await holds(path.join(d, f.name), f)) return path.join(d, f.name)
    if (f.local && await holds(path.join(ONNX_DIR, f.local), f)) return path.join(ONNX_DIR, f.local)
    return null
  }))
}

/** What is missing here downloaded into the Studio's own folder by
 *  fetch_embed.py --download; why not, or null. */
function download(platform: string): Promise<string | null> {
  return new Promise(resolve => {
    const child = spawn(PYTHON_BIN, [path.join(PROJECT_ROOT, 'indexer', 'fetch_embed.py'), '--download', '--platform', platform,
      '--cache', path.join(INDEX_BASE, 'embed-wheels')], { stdio: ['ignore', 'ignore', 'pipe'] })
    let err = ''
    const timer = setTimeout(() => child.kill(), 15 * 60_000)
    child.stderr.on('data', d => { err = (err + String(d)).slice(-1000) })
    child.on('error', e => { clearTimeout(timer); resolve(e.message) })
    child.on('close', code => { clearTimeout(timer); resolve(code === 0 ? null : err.trim().split('\n').pop() || `exit ${code}`) })
  })
}

/** The files there for a version, by name and size. $1 the version. */
const LIST = `d="$HOME/.activate-studio/embed/$1.files"; [ -d "$d" ] || exit 0
for f in "$d"/*; do [ -f "$f" ] && printf '%s %s\\n' "$(wc -c < "$f" | tr -d ' ')" "\${f##*/}"; done; exit 0`
/** One file from standard input, written whole or not at all, and the
 *  marker touched. $1 the version, $2 the file's name. */
const PUT = `d="$HOME/.activate-studio/embed/$1.files"; mkdir -p "$d" && cat > "$d/.$2.part" && mv -f "$d/.$2.part" "$d/$2" && touch "$HOME/.activate-studio/embed/$1.sending"`
const TOUCH = 'touch "$HOME/.activate-studio/embed/$1.sending"'
const DONE = 'rm -f "$HOME/.activate-studio/embed/$1.sending"'

/** Files sent, for tests. */
let sentCount = 0
export const embedFilesSent = () => sentCount

const sending = new Map<string, Promise<void>>()

/**
 * Sends a build there what it needs to embed with the ONNX model and does
 * not have, while the build runs: BUILD_START marked the version as being
 * sent, and the build waits for that mark to go before its meaning phase.
 * The mark is touched as the files go and removed at the end, whatever
 * happened, so the build goes on with what arrived. One sending per system
 * and version at a time; a second build waits for the first and sends
 * what is still missing.
 */
export function sendEmbed(site: string, key: string, platform: string, version: string, log: (m: string) => void = m => console.warn(m)): Promise<void> {
  const k = `${key.slice(-16)}|${site}|${version}`
  const run = (sending.get(k) ?? Promise.resolve()).catch(() => {}).then(() => send(site, key, platform, version, log))
  const held = run.finally(() => { if (sending.get(k) === held) sending.delete(k) })
  sending.set(k, held)
  return held
}

async function send(site: string, key: string, platform: string, version: string, log: (m: string) => void): Promise<void> {
  const files = embedFiles(platform)
  if (!files) return
  const beat = setInterval(() => { void remoteRun(site, key, ['sh', '-c', TOUCH, '_', version], 30_000).catch(() => {}) }, 60_000)
  beat.unref?.()
  try {
    let local = await findLocal(files)
    if (local.some(p => !p)) {
      const why = await download(platform)
      if (why) log(`search by meaning on ${siteName(site)}: the ONNX packages for ${platform} could not be downloaded here (${why}); the build there downloads them itself where it can`)
      local = await findLocal(files)
    }
    const there = new Map<string, number>()
    const listed = await remoteRun(site, key, ['sh', '-c', LIST, '_', version], 60_000)
    for (const line of listed.stdout.split('\n')) {
      const m = /^(\d+) (.+)$/.exec(line.trim())
      if (m) there.set(m[2], Number(m[1]))
    }
    for (const [i, f] of files.entries()) {
      const p = local[i]
      if (!p || there.get(f.name) === f.size) continue
      const r = await remoteRunOnce(site, key, ['sh', '-c', PUT, '_', version, f.name], 30 * 60_000, p)
      if (r.code !== 0) throw new Error(`${f.name} could not be written there: ${r.stderr.trim().split('\n').pop() || `exit ${r.code}`}`)
      sentCount++
    }
  } catch (e) {
    log(`search by meaning on ${siteName(site)}: sending the embedding model failed: ${(e as Error).message}`)
  } finally {
    clearInterval(beat)
    await remoteRun(site, key, ['sh', '-c', DONE, '_', version], 30_000).catch(() => {})
  }
}
