import fs from 'node:fs/promises'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { KB_ROOT, gufiAvailable } from '../config.js'
import { corpusStats } from '../gufi.js'

let cached: string | null = null

function pwList(): Promise<string> {
  return new Promise(resolve => {
    execFile('pw', ['workflows', 'ls'], { timeout: 20_000 }, (_e, stdout) => resolve(stdout?.trim() ?? ''))
  })
}

/**
 * The awareness block: what the assistant knows the moment the interface
 * opens. If the knowledge base carries a CLAUDE.md conventions file, it is
 * loaded whole so its working rules bind chat output.
 */
export async function systemPrompt(): Promise<string> {
  if (cached) return cached
  let conventions = ''
  try { conventions = await fs.readFile(path.join(KB_ROOT, 'CLAUDE.md'), 'utf8') } catch { /* absent */ }

  let statsLine = 'Corpus index not yet built; search_kb falls back to grep.'
  if (gufiAvailable()) {
    try {
      const s = await corpusStats()
      if (s.available) statsLine = `Indexed corpus: ${s.files} files across ${s.dirs} directories, ${Math.round((s.totalBytes ?? 0) / 1e6)} MB.`
    } catch { /* stats are best-effort */ }
  }
  const workflows = await pwList()

  cached = [
    `You are the assistant inside ade-studio, a knowledge base interface. The knowledge base at ${KB_ROOT} is the working corpus for this deployment.`,
    '',
    'Ground every answer about the knowledge base in its actual content: call search_kb first, read the files that matter with read_kb_file, and cite the relative file paths you used. If retrieval returns nothing relevant, say so instead of guessing.',
    '',
    statsLine,
    '',
    'Platform workflows registered in this account (list_workflows/get_workflow give detail):',
    workflows || '(workflow list unavailable)',
    '',
    conventions
      ? 'The knowledge base ships its own working conventions, loaded below. Its rules on tone, terminology, and claims not to make are binding for every draft you produce here, because text produced in this interface can end up in deliverables.'
      : '',
    conventions ? '--- KNOWLEDGE BASE CONVENTIONS (CLAUDE.md) ---' : '',
    conventions,
  ].filter(Boolean).join('\n')
  return cached
}

export function invalidateContext(): void {
  cached = null
}
