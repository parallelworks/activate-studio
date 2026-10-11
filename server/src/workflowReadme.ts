import type { FastifyInstance } from 'fastify'
import { GATEWAY_BASE } from './config.js'
import { KbError } from './kb.js'
import { parseWorkflowEntry, type WorkflowEntry } from './workflowEntries.js'
import { firstJson, platformKeyFor, workflowsCli } from './workflowsTab.js'

/**
 * A workflow's README, shown beside its form: it usually says how the
 * workflow works and what each input changes.
 *
 * Every kind of entry resolves to a repository, a ref, and a file. An
 * account workflow that came from a repository names them in its record's
 * `remote` block (copies of marketplace workflows included); a marketplace
 * entry names them per version; a GitHub entry is its own repository, with
 * the README in the workflow's directory. The file is read through the
 * platform's repository route with the viewer's key, which reaches private
 * GitHub repositories and projects on registered GitLab servers alike. A
 * workflow defined only on the platform has no README, and the page shows
 * its description.
 */

export interface ReadmeSource {
  /** The platform's form: OWNER/REPO for GitHub, HOST/GROUP/PROJECT for GitLab. */
  repo: string
  ref: string
  path: string
  github: boolean
}

const README_MAX = 100 * 1024

/** A repository address in the platform's form, or null when it is not one. */
export function repoKey(url: string): { key: string; github: boolean } | null {
  let s = String(url ?? '').trim().replace(/\.git$/, '').replace(/\/+$/, '')
  let host = 'github.com'
  const m = /^https?:\/\/([^/]+)\/(.+)$/.exec(s)
  if (m) { host = m[1].toLowerCase(); s = m[2] }
  if (!/^[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)+$/.test(s)) return null
  const github = host === 'github.com'
  if (github && s.split('/').length !== 2) return null
  return { key: github ? s : `${host}/${s}`, github }
}

/** Where a repository-backed record keeps its README, from its remote block. */
export function sourceFromRemote(remote: { repo?: string; branch?: string; readme?: string } | null | undefined): ReadmeSource | null {
  if (!remote?.repo || !remote.readme) return null
  const rk = repoKey(remote.repo)
  const path = String(remote.readme).replace(/^\/+/, '')
  if (!rk || !path || path.includes('..')) return null
  return { repo: rk.key, ref: remote.branch || 'main', path, github: rk.github }
}

/** A GitHub entry's README: the one in its workflow's directory. */
export function githubReadmeSource(e: Extract<WorkflowEntry, { kind: 'github' }>): ReadmeSource {
  let dir = e.path
  if (/\.ya?ml$/.test(dir)) dir = dir.split('/').slice(0, -1).join('/')
  dir = dir.replace(/(^|\/)yamls$/, '')
  return { repo: `${e.owner}/${e.repo}`, ref: e.ref ?? 'HEAD', path: `${dir ? `${dir}/` : ''}README.md`, github: true }
}

/** A file's page on the repository's own site, for links that are not images. */
export function blobUrl(src: ReadmeSource, path: string): string {
  return src.github
    ? `https://github.com/${src.repo}/blob/${src.ref}/${path}`
    : `https://${src.repo}/-/blob/${src.ref}/${path}`
}

const IMAGE = /\.(png|jpe?g|gif|svg|webp|ico|bmp)$/i
const ABSOLUTE = /^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i

/** A relative URL resolved against the README's directory, as a path in the repository. */
export function resolveInRepo(readmePath: string, rel: string): string | null {
  const bare = rel.replace(/[?#].*$/, '')
  if (!bare) return null
  const parts = bare.startsWith('/') ? [] : readmePath.split('/').slice(0, -1)
  for (const seg of bare.replace(/^\/+/, '').split('/')) {
    if (!seg || seg === '.') continue
    if (seg === '..') { if (!parts.length) return null; parts.pop() } else parts.push(seg)
  }
  return parts.join('/') || null
}

/**
 * Relative links and images rewritten to work outside the repository:
 * images through the Studio's asset route (which reads them with the
 * viewer's key), other files to their page on the repository's site.
 * Code spans and fenced blocks are left as written.
 */
export function rewriteReadme(markdown: string, src: ReadmeSource, entry: string): string {
  const fix = (url: string, image: boolean): string => {
    if (ABSOLUTE.test(url)) return url
    const p = resolveInRepo(src.path, url)
    if (!p) return url
    return image || IMAGE.test(p)
      ? `/api/workflows/item/asset?w=${encodeURIComponent(entry)}&path=${encodeURIComponent(p)}`
      : blobUrl(src, p)
  }
  return markdown.split(/(^```[\s\S]*?^```|^~~~[\s\S]*?^~~~|`+[^`\n]+`+)/m).map((chunk, i) => {
    if (i % 2 === 1) return chunk
    return chunk
      .replace(/(!?)(\[[^\]]*\]\()([^)\s]+)(\s*(?:"[^"]*"\s*)?\))/g, (_m, bang, open, url, close) => `${bang}${open}${fix(url, bang === '!')}${close}`)
      .replace(/^(\[[^\]]+\]:\s+)(\S+)(.*)$/gm, (_m, a, url, b) => `${a}${fix(url, false)}${b}`)
      .replace(/(<img\b[^>]*?\ssrc\s*=\s*["'])([^"']+)(["'])/gi, (_m, a, url, b) => `${a}${fix(url, true)}${b}`)
      .replace(/(<a\b[^>]*?\shref\s*=\s*["'])([^"']+)(["'])/gi, (_m, a, url, b) => `${a}${fix(url, false)}${b}`)
  }).join('')
}

const platform = (): string | null => { try { return new URL(GATEWAY_BASE).origin } catch { return null } }

async function platformGet(path: string, key: string, timeoutMs = 20_000): Promise<Response | null> {
  const host = platform()
  if (!host) return null
  return fetch(`${host}${path}`, { headers: { Authorization: `Bearer ${key}` }, signal: AbortSignal.timeout(timeoutMs) }).catch(() => null)
}

/** Where an entry's README lives, or null when it has none. Throws when
 *  the platform could not say, so a passing failure is not remembered as
 *  "no README". */
export async function readmeSource(e: WorkflowEntry, key: string): Promise<ReadmeSource | null> {
  if (e.kind === 'github') return githubReadmeSource(e)
  if (e.kind === 'account') {
    const res = await platformGet(`/api/workflows/${encodeURIComponent(e.name)}`, key)
    if (!res?.ok) throw new Error(`workflow record unavailable (${res?.status ?? 'no answer'})`)
    const doc = await res.json().catch(() => null) as { remote?: { repo?: string; branch?: string; readme?: string } } | null
    return sourceFromRemote(doc?.remote)
  }
  const doc = firstJson<any>(await workflowsCli(['marketplace', 'get', e.slug, '-o', 'json'], key, 60_000))
  const versions = (doc?.versions ?? {}) as Record<string, { repo?: string; branch?: string; readme?: string }>
  const order: string[] = Array.isArray(doc?.versionOrder) ? doc.versionOrder : Object.keys(versions)
  const tag = doc?.versionTags?.latest
  return sourceFromRemote(versions[tag] ?? versions[order[order.length - 1]])
}

/** The README's text, read through the platform; GitHub falls back to its raw host. */
export async function readmeText(src: ReadmeSource, key: string): Promise<string | null> {
  const q = new URLSearchParams({ repo: src.repo, ref: src.ref, path: src.path })
  const res = await platformGet(`/api/repositories/readme?${q}`, key)
  if (res?.ok) {
    const body = await res.json().catch(() => null) as { markdown?: string } | null
    if (typeof body?.markdown === 'string') return body.markdown.slice(0, README_MAX)
  }
  if (src.github) {
    const raw = await fetch(`https://raw.githubusercontent.com/${src.repo}/${src.ref}/${src.path}`, { signal: AbortSignal.timeout(15_000) }).catch(() => null)
    if (raw?.ok) return (await raw.text()).slice(0, README_MAX)
  }
  return null
}

const readmeCache = new Map<string, { at: number; value: { markdown: string | null; web: string | null } }>()
const assetCache = new Map<string, { at: number; type: string; body: Buffer }>()
const TTL_MS = 300_000
const ASSET_CSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

export async function workflowReadmeRoutes(app: FastifyInstance): Promise<void> {
  const entryAndKey = (req: { query: unknown; user?: { id?: string } }) => {
    const e = parseWorkflowEntry(String((req.query as { w?: string }).w ?? ''))
    if (!e) throw new KbError(400, 'invalid workflow: use a name, marketplace/<slug>, or github.com/<owner>/<repo>[/path][@ref]')
    const { key } = platformKeyFor(req.user?.id)
    if (!key) throw new KbError(409, 'No platform credential: add your ACTIVATE API key under Settings, Model access.')
    return { e, key }
  }

  // The README as markdown with its links made to work here, and the
  // file's page on its repository; markdown is null when there is none.
  app.get('/api/workflows/item/readme', async req => {
    const { e, key } = entryAndKey(req)
    const ck = `${key.slice(-12)}|${e.entry}`
    const hit = readmeCache.get(ck)
    if (hit && Date.now() - hit.at < TTL_MS) return hit.value
    let src: ReadmeSource | null
    try { src = await readmeSource(e, key) } catch { return { markdown: null, web: null } }
    const text = src ? await readmeText(src, key) : null
    const value = { markdown: text && src ? rewriteReadme(text, src, e.entry) : null, web: src && text ? blobUrl(src, src.path) : null }
    // Only a definite answer is kept: a README, or a workflow with none. A
    // read that failed is asked again next time.
    if (!src || text !== null) readmeCache.set(ck, { at: Date.now(), value })
    return value
  })

  // An image a README shows, read from the workflow's repository with the
  // viewer's key. Only image files, and only inside the repository.
  app.get('/api/workflows/item/asset', async (req, reply) => {
    const { e, key } = entryAndKey(req)
    const path = String((req.query as { path?: string }).path ?? '').replace(/^\/+/, '')
    if (!path || path.split('/').includes('..') || !IMAGE.test(path)) throw new KbError(400, 'not an image path in the repository')
    const ck = `${key.slice(-12)}|${e.entry}|${path}`
    const hit = assetCache.get(ck)
    const send = (type: string, body: Buffer) => reply.type(type).header('Cache-Control', 'private, max-age=3600').header('Content-Security-Policy', ASSET_CSP).send(body)
    if (hit && Date.now() - hit.at < 3_600_000) return send(hit.type, hit.body)
    const src = await readmeSource(e, key).catch(() => null)
    if (!src) throw new KbError(404, 'this workflow has no repository')
    const q = new URLSearchParams({ repo: src.repo, ref: src.ref, path })
    let res = await platformGet(`/api/repositories/thumbnail?${q}`, key)
    if (!res?.ok && src.github) res = await fetch(`https://raw.githubusercontent.com/${src.repo}/${src.ref}/${path}`, { signal: AbortSignal.timeout(15_000) }).catch(() => null)
    const type = res?.headers.get('content-type') ?? ''
    if (!res?.ok || !/^image\//.test(type)) throw new KbError(404, 'image unavailable')
    const body = Buffer.from(await res.arrayBuffer())
    assetCache.set(ck, { at: Date.now(), type, body })
    return send(type, body)
  })
}

/** Tests reset the caches between cases. */
export function resetWorkflowReadmeForTests(): void { readmeCache.clear(); assetCache.clear() }
