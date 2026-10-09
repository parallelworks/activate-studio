import { parse as parseYaml } from 'yaml'
import { KbError } from './kb.js'
import type { WorkflowEntry } from './workflowEntries.js'

/**
 * Workflows defined in GitHub repositories.
 *
 * `pw workflows run` takes a repository, a directory holding workflow.yaml,
 * or a named file. Repositories of components keep their definitions as
 * <dir>/yamls/<variant>.yaml instead (parallelworks/workflows does), so a
 * directory entry is resolved here to the files the CLI can run. One tree
 * request per repository and ref, cached, keeps within GitHub's limit for
 * unauthenticated clients; a token in GITHUB_TOKEN or GH_TOKEN is used when
 * the host has one.
 */

type GithubEntry = Extract<WorkflowEntry, { kind: 'github' }>

export interface GithubWorkflow {
  entry: string
  title: string
  description: string
}

const TREE_TTL = 10 * 60_000
const treeCache = new Map<string, { at: number; paths: Set<string> }>()
const readmeCache = new Map<string, { at: number; title: string; description: string }>()

const ghHeaders = (): Record<string, string> => {
  const token = process.env.GITHUB_TOKEN || process.env.GH_TOKEN
  return { Accept: 'application/vnd.github+json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }
}

const refOf = (e: GithubEntry) => e.ref ?? 'HEAD'
const rawUrl = (e: GithubEntry, path: string) => `https://raw.githubusercontent.com/${e.owner}/${e.repo}/${refOf(e)}/${path}`
const entryFor = (e: GithubEntry, path: string) =>
  `github.com/${e.owner}/${e.repo}${path ? `/${path}` : ''}${e.ref ? `@${e.ref}` : ''}`

/** Every file path in the repository at the entry's ref. */
async function treeOf(e: GithubEntry): Promise<Set<string>> {
  const key = `${e.owner}/${e.repo}@${refOf(e)}`
  const hit = treeCache.get(key)
  if (hit && Date.now() - hit.at < TREE_TTL) return hit.paths
  const res = await fetch(`https://api.github.com/repos/${e.owner}/${e.repo}/git/trees/${encodeURIComponent(refOf(e))}?recursive=1`,
    { headers: ghHeaders(), signal: AbortSignal.timeout(20_000) }).catch(() => null)
  if (!res) throw new KbError(502, `Could not reach GitHub for ${key}.`)
  if (res.status === 404) throw new KbError(404, `No public repository or ref ${key} on GitHub.`)
  if (res.status === 403 || res.status === 429) throw new KbError(503, 'GitHub is rate limiting this host; try again in a few minutes, or set GITHUB_TOKEN.')
  if (!res.ok) throw new KbError(502, `GitHub answered ${res.status} for ${key}.`)
  const body = await res.json() as { tree?: { path: string; type: string }[] }
  const paths = new Set((body.tree ?? []).filter(t => t.type === 'blob').map(t => t.path))
  treeCache.set(key, { at: Date.now(), paths })
  return paths
}

const isYaml = (p: string) => /\.ya?ml$/.test(p)

/** The definition files a directory provides: its workflow.yaml, or each yamls/<variant>.yaml. */
function filesIn(paths: Set<string>, dir: string): string[] {
  const pre = dir ? `${dir}/` : ''
  for (const name of ['workflow.yaml', 'workflow.yml']) if (paths.has(pre + name)) return [pre + name]
  return [...paths].filter(p => p.startsWith(`${pre}yamls/`) && isYaml(p) && !p.slice(`${pre}yamls/`.length).includes('/')).sort()
}

/**
 * The runnable entries behind a GitHub entry: itself when it names a file,
 * otherwise the directory's workflow.yaml or each of its yamls/ variants.
 */
export async function resolveGithubEntry(e: GithubEntry): Promise<string[]> {
  if (isYaml(e.path)) return [e.entry]
  const files = filesIn(await treeOf(e), e.path)
  if (!files.length) throw new KbError(404, `No workflow.yaml or yamls/*.yaml at ${e.entry}.`)
  return files.map(f => entryFor(e, f))
}

/** A directory's README title and first paragraph, for listings. */
async function readmeOf(e: GithubEntry, dir: string): Promise<{ title: string; description: string }> {
  const key = `${e.owner}/${e.repo}@${refOf(e)}:${dir}`
  const hit = readmeCache.get(key)
  if (hit && Date.now() - hit.at < TREE_TTL) return hit
  let title = dir.split('/').pop() || e.repo
  let description = ''
  const res = await fetch(rawUrl(e, `${dir ? `${dir}/` : ''}README.md`), { signal: AbortSignal.timeout(15_000) }).catch(() => null)
  if (res?.ok) {
    const lines = (await res.text()).split('\n')
    const h1 = lines.find(l => /^#\s+/.test(l))
    if (h1) title = h1.replace(/^#\s+/, '').trim()
    const para: string[] = []
    for (const l of lines.slice(h1 ? lines.indexOf(h1) + 1 : 0)) {
      if (!l.trim()) { if (para.length) break; continue }
      if (/^(#|!\[|<|\||```)/.test(l.trim())) { if (para.length) break; continue }
      para.push(l.trim())
    }
    // A paragraph that introduces a list ends in a colon; it stands alone here.
    const text = para.join(' ').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/[*_`]/g, '').replace(/:$/, '.')
    // Long first paragraphs end at a sentence, or failing that a word.
    description = text.length <= 320 ? text
      : text.slice(0, 320).match(/^.*[.!?](?=\s)/s)?.[0] ?? `${text.slice(0, 320).replace(/\s+\S*$/, '')}…`
  }
  const out = { at: Date.now(), title, description }
  readmeCache.set(key, out)
  return out
}

/**
 * The workflows in a repository, or under one directory of it: every
 * directory with a workflow.yaml or yamls/ variants, one entry per file,
 * titled from its README.
 */
export async function browseGithub(e: GithubEntry): Promise<GithubWorkflow[]> {
  if (isYaml(e.path)) {
    const dir = e.path.replace(/\/?(yamls\/)?[^/]+$/, '')
    const { title, description } = await readmeOf(e, dir)
    return [{ entry: e.entry, title, description }]
  }
  const paths = await treeOf(e)
  const under = e.path.replace(/\/$/, '')
  const dirs = new Set<string>()
  for (const p of paths) {
    if (under && !p.startsWith(`${under}/`) && p !== under) continue
    const m = /^(?:(.*)\/)?(?:workflow\.ya?ml|yamls\/[^/]+\.ya?ml)$/.exec(p)
    if (m) dirs.add(m[1] ?? '')
  }
  const list = [...dirs].sort().slice(0, 200)
  const out: GithubWorkflow[] = []
  // A few README fetches at a time; raw files are not counted against the API limit.
  for (let i = 0; i < list.length; i += 8) {
    const batch = await Promise.all(list.slice(i, i + 8).map(async dir => {
      const files = filesIn(paths, dir)
      const { title, description } = await readmeOf(e, dir)
      return files.map(f => ({
        entry: entryFor(e, f),
        title: files.length > 1 ? `${title} (${f.split('/').pop()!.replace(/\.ya?ml$/, '')})` : title,
        description,
      }))
    }))
    out.push(...batch.flat())
  }
  return out
}

export interface GithubDefinition {
  entry: string
  yaml: any
  title: string
  description: string
  permissions: string[]
}

/** The YAML behind a GitHub entry, resolved to one file, with its declared permissions. */
export async function githubDefinition(e: GithubEntry): Promise<GithubDefinition> {
  const files = await resolveGithubEntry(e)
  if (files.length > 1) {
    throw new KbError(409, `${e.entry} holds several workflows; name one: ${files.join(', ')}`)
  }
  const fileEntry = files[0]
  const path = fileEntry.replace(/^github\.com\/[^/]+\/[^/@]+\/?/, '').replace(/@.*$/, '')
  const res = await fetch(rawUrl(e, path), { signal: AbortSignal.timeout(20_000) }).catch(() => null)
  if (!res?.ok) {
    throw new KbError(res?.status === 404 ? 404 : 502, res?.status === 404
      ? `No workflow file at ${fileEntry}. Private repositories still run, but their form cannot be shown here.`
      : `Could not read ${fileEntry} from GitHub.`)
  }
  const yaml = parseYaml(await res.text())
  const dir = path.replace(/\/?(yamls\/)?[^/]+$/, '')
  const { title, description } = await readmeOf(e, dir)
  const perms = Array.isArray(yaml?.permissions) ? yaml.permissions.map(String) : []
  return { entry: fileEntry, yaml, title, description, permissions: perms }
}

export interface GithubSummary { title: string; description: string; thumbnail: string | null }

/**
 * A tile's title, summary, and icon for a GitHub entry: the README of the
 * workflow's directory, with the variant named when the directory has
 * several, and the first PNG, JPEG, or WebP in its thumbnails/ directory
 * (SVG is skipped, since the Studio serves icons from its own origin).
 */
export async function githubSummary(e: GithubEntry): Promise<GithubSummary> {
  const dir = isYaml(e.path) ? e.path.replace(/\/?(yamls\/)?[^/]+$/, '') : e.path.replace(/\/$/, '')
  const [{ title, description }, paths] = await Promise.all([readmeOf(e, dir), treeOf(e).catch(() => null)])
  const pre = dir ? `${dir}/` : ''
  const files = paths ? filesIn(paths, dir) : []
  const variant = isYaml(e.path) && files.length > 1 ? ` (${e.path.split('/').pop()!.replace(/\.ya?ml$/, '')})` : ''
  const thumb = paths
    ? ([...paths].filter(p => p.startsWith(`${pre}thumbnails/`) && /\.(png|jpe?g|webp)$/i.test(p)).sort()[0]
      ?? (paths.has(`${pre}thumbnail.png`) ? `${pre}thumbnail.png` : null))
    : null
  return { title: title + variant, description, thumbnail: thumb ? rawUrl(e, thumb) : null }
}

/** Tests replace GitHub with fixtures. */
export function resetGithubCaches(): void { treeCache.clear(); readmeCache.clear() }
