import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, PROJECT_ROOT } from './config.js'

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

/** The standing persona: agents/default.md, if present. */
export function agentPrompt(): string {
  try { return fs.readFileSync(path.join(EXT_DIR, 'agents', 'default.md'), 'utf8').trim() } catch { return '' }
}

export async function extensionRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/extensions', async () => ({
    dir: EXT_DIR,
    tools: extTools().map(({ command, ...t }) => ({ ...t, command })),
    skills: extSkills(),
    agents: extAgents().map(a => ({ ...a, active: a.file === 'default.md' })),
  }))
}
