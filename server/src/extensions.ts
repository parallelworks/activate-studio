import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT, PROJECT_ROOT } from './config.js'

/**
 * File-based extensions, read from INDEX_BASE/extensions/ (deployment-local,
 * never in the repository):
 *   tools/*.json  — { "name", "description", "command" }; each becomes a
 *                   callable chat tool that runs the command on this server.
 *   skills/*.md   — instruction files; the assistant loads one on demand
 *                   through the use_skill tool. Frontmatter (name,
 *                   description) is optional; the filename is the fallback.
 *   agents/default.md — appended to the system prompt as the deployment's
 *                   standing persona and instructions.
 * Directory contents are re-read at most every few seconds, so dropping a
 * file in takes effect without a restart.
 */

export const EXT_DIR = path.join(INDEX_BASE, 'extensions')
const STARTER_DIR = path.join(PROJECT_ROOT, 'extensions-starter')
const TTL_MS = 5_000

/** First-run seeding: when no extensions directory exists yet, copy the
 *  repository's starter set so a fresh deployment has working examples.
 *  Runs only when the directory is entirely absent, so deletions and edits
 *  are never resurrected. */
export function seedExtensions(log?: (msg: string) => void): void {
  if (fs.existsSync(EXT_DIR) || !fs.existsSync(STARTER_DIR)) return
  try {
    fs.cpSync(STARTER_DIR, EXT_DIR, { recursive: true })
    log?.(`seeded starter extensions into ${EXT_DIR}`)
  } catch { /* seeding is best-effort */ }
}

export interface ExtTool { name: string; description: string; command: string; file: string }
export interface ExtDoc { name: string; description: string; file: string }

const normalizeName = (raw: string): string =>
  raw.toLowerCase().replace(/[^a-z0-9_]/g, '_').replace(/^_+|_+$/g, '').slice(0, 40)

function listFiles(sub: string, ext: string): string[] {
  try {
    return fs.readdirSync(path.join(EXT_DIR, sub))
      .filter(f => f.endsWith(ext) && !f.startsWith('.'))
      .map(f => path.join(EXT_DIR, sub, f))
  } catch { return [] }
}

/** name/description from `---` frontmatter, else first heading + first prose line. */
function docMeta(file: string): ExtDoc {
  const fallback = normalizeName(path.basename(file, '.md'))
  let name = fallback
  let description = ''
  try {
    const text = fs.readFileSync(file, 'utf8')
    const fm = /^---\n([\s\S]*?)\n---/.exec(text)
    if (fm) {
      const get = (k: string) => new RegExp(`^${k}:\\s*(.+)$`, 'm').exec(fm[1])?.[1]?.trim()
      name = normalizeName(get('name') ?? fallback) || fallback
      description = get('description') ?? ''
    }
    if (!description) {
      const body = fm ? text.slice(fm[0].length) : text
      description = body.split('\n').map(l => l.trim()).find(l => l && !l.startsWith('#')) ?? ''
    }
  } catch { /* unreadable file: fall through with defaults */ }
  return { name, description: description.slice(0, 200), file: path.basename(file) }
}

let toolCache: { at: number; tools: ExtTool[] } | null = null
export function extTools(): ExtTool[] {
  if (toolCache && Date.now() - toolCache.at < TTL_MS) return toolCache.tools
  const tools: ExtTool[] = []
  for (const file of listFiles('tools', '.json')) {
    try {
      const raw = JSON.parse(fs.readFileSync(file, 'utf8'))
      const name = normalizeName(String(raw.name ?? path.basename(file, '.json')))
      const command = String(raw.command ?? '').slice(0, 500)
      if (name && command) {
        tools.push({ name, description: String(raw.description ?? '').slice(0, 300), command, file: path.basename(file) })
      }
    } catch { /* skip malformed file */ }
  }
  toolCache = { at: Date.now(), tools }
  return tools
}

let skillCache: { at: number; skills: ExtDoc[] } | null = null
export function extSkills(): ExtDoc[] {
  if (skillCache && Date.now() - skillCache.at < TTL_MS) return skillCache.skills
  skillCache = { at: Date.now(), skills: listFiles('skills', '.md').map(docMeta) }
  return skillCache.skills
}

export function skillBody(name: string): string | null {
  const skill = extSkills().find(s => s.name === normalizeName(name))
  if (!skill) return null
  try { return fs.readFileSync(path.join(EXT_DIR, 'skills', skill.file), 'utf8') } catch { return null }
}

export function extAgents(): ExtDoc[] {
  return listFiles('agents', '.md').map(docMeta)
}

export function agentBody(name: string): string | null {
  const agent = extAgents().find(a => a.name === normalizeName(name))
  if (!agent) return null
  try { return fs.readFileSync(path.join(EXT_DIR, 'agents', agent.file), 'utf8') } catch { return null }
}

/**
 * Personas live in two places and merge into one library.
 *
 * The corpus directory (KB_ROOT/.agents) is the shared one: every Studio
 * over the same knowledge base sees the same personas, they version and
 * sync with the corpus itself, and a team grows the library by adding
 * files to material they already share. The deployment directory
 * (extensions/agents) stays for personas belonging to one deployment, and
 * wins on a name collision so a deployment can override a shared persona
 * without editing the corpus.
 *
 * default.md remains the standing persona when a turn names none, so a
 * deployment that never touches this keeps working exactly as before.
 */
export const CORPUS_AGENT_DIR = path.join(KB_ROOT, '.agents')

function agentFiles(): { name: string; file: string; shared: boolean }[] {
  const found = new Map<string, { name: string; file: string; shared: boolean }>()
  for (const [dir, shared] of [[CORPUS_AGENT_DIR, true], [path.join(EXT_DIR, 'agents'), false]] as const) {
    let names: string[] = []
    try { names = fs.readdirSync(dir).filter(f => f.endsWith('.md') && !f.startsWith('.')) } catch { continue }
    for (const f of names) {
      const meta = docMeta(path.join(dir, f))
      // The deployment directory is read second, so it overwrites.
      found.set(meta.name, { name: meta.name, file: path.join(dir, f), shared })
    }
  }
  return [...found.values()]
}

/** The persona library, for the picker and the Settings list. */
export function personas(): (ExtDoc & { shared: boolean })[] {
  return agentFiles().map(a => {
    const meta = docMeta(a.file)
    return { ...meta, file: path.basename(a.file), shared: a.shared }
  })
}

/** The persona text for a turn: the named one, else the standing default. */
export function agentPrompt(name?: string | null): string {
  const wanted = normalizeName(String(name ?? ''))
  if (wanted) {
    const hit = agentFiles().find(a => a.name === wanted)
    if (hit) {
      try { return fs.readFileSync(hit.file, 'utf8').replace(/^---\n[\s\S]*?\n---\n?/, '').trim() } catch { /* fall through */ }
    }
  }
  try { return fs.readFileSync(path.join(EXT_DIR, 'agents', 'default.md'), 'utf8').trim() } catch { return '' }
}

/**
 * Authoring endpoints. Personas write into the corpus by default, which
 * is what makes them shared: the file lands in the knowledge base, syncs
 * and versions with everything else, and every Studio over that corpus
 * sees it. Skills stay deployment-local, since they carry instructions
 * for how this deployment works.
 *
 * The name is normalized to a safe slug and the file is written by
 * joining that slug to a fixed directory, so a crafted name cannot climb
 * out of it.
 */
function writeDoc(dir: string, rawName: string, body: string, description: string): { name: string; file: string } {
  const name = normalizeName(rawName)
  if (!name) throw new Error('a name is required')
  const text = String(body ?? '').slice(0, 100_000).trim()
  if (!text) throw new Error('the document is empty')
  const desc = String(description ?? '').replace(/\n/g, ' ').slice(0, 200).trim()
  // Frontmatter is rewritten from the supplied fields so the listing and
  // the file never disagree; a pasted document's own frontmatter is
  // replaced rather than duplicated.
  const withoutFm = text.replace(/^---\n[\s\S]*?\n---\n?/, '').trim()
  const doc = `---\nname: ${name}\ndescription: ${desc}\n---\n\n${withoutFm}\n`
  fs.mkdirSync(dir, { recursive: true })
  const file = path.join(dir, `${name}.md`)
  fs.writeFileSync(file, doc)
  return { name, file }
}

export async function extensionRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/extensions/persona', async (req, reply) => {
    const b = (req.body ?? {}) as { name?: string; description?: string; body?: string; shared?: boolean }
    try {
      const dir = b.shared === false ? path.join(EXT_DIR, 'agents') : CORPUS_AGENT_DIR
      const { name } = writeDoc(dir, String(b.name ?? ''), String(b.body ?? ''), String(b.description ?? ''))
      return { ok: true, name, shared: b.shared !== false }
    } catch (e) {
      return reply.status(400).send({ error: String((e as Error).message) })
    }
  })

  app.post('/api/extensions/skill', async (req, reply) => {
    const b = (req.body ?? {}) as { name?: string; description?: string; body?: string }
    try {
      const { name } = writeDoc(path.join(EXT_DIR, 'skills'), String(b.name ?? ''), String(b.body ?? ''), String(b.description ?? ''))
      return { ok: true, name }
    } catch (e) {
      return reply.status(400).send({ error: String((e as Error).message) })
    }
  })

  app.delete('/api/extensions/persona', async req => {
    const name = normalizeName(String((req.query as { name?: string }).name ?? ''))
    if (!name) return { ok: false }
    for (const dir of [CORPUS_AGENT_DIR, path.join(EXT_DIR, 'agents')]) {
      try { fs.rmSync(path.join(dir, `${name}.md`), { force: true }) } catch { /* absent */ }
    }
    return { ok: true }
  })

  app.get('/api/extensions', async () => ({
    dir: EXT_DIR,
    tools: extTools().map(({ command, ...t }) => ({ ...t, command })),
    skills: extSkills(),
    agents: extAgents().map(a => ({ ...a, active: a.file === 'default.md' })),
    personas: personas(),
    corpusAgentDir: CORPUS_AGENT_DIR,
  }))
}
