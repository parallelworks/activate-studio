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
 * The awareness block: what the assistant knows the moment the interface opens.
 * CLAUDE.md is loaded whole because its guardrails (claims not to make, IL
 * terminology, no user counts) must bind chat output, which can end up in
 * deliverables.
 */
export async function systemPrompt(): Promise<string> {
  if (cached) return cached
  let claudeMd = ''
  try { claudeMd = await fs.readFile(path.join(KB_ROOT, 'CLAUDE.md'), 'utf8') } catch { /* absent */ }

  let statsLine = 'Corpus index not yet built; search_kb falls back to grep.'
  if (gufiAvailable()) {
    try {
      const s = await corpusStats()
      if (s.available) statsLine = `Indexed corpus: ${s.files} files across ${s.dirs} directories, ${Math.round((s.totalBytes ?? 0) / 1e6)} MB.`
    } catch { /* stats are best-effort */ }
  }
  const workflows = await pwList()

  cached = [
    'You are the assistant inside ade-studio, the Parallel Works knowledge base interface. The knowledge base at ' + KB_ROOT + ' holds the company\'s proposals, partnerships, strategy, reports, pipeline, and conventions.',
    '',
    'Ground every answer about company work in the knowledge base: call search_kb first, read the files that matter with read_kb_file, and cite the relative file paths you used. If retrieval returns nothing relevant, say so instead of guessing.',
    '',
    statsLine,
    '',
    'ACTIVATE platform workflows registered in this account (list_workflows/get_workflow give detail):',
    workflows || '(workflow list unavailable)',
    '',
    'The knowledge base working conventions below are binding. In particular: the writing-tone rules, the accuracy guardrails under "Things NOT to claim", the platform security posture wording, and the company facts apply to every draft you produce here, because text produced in this interface can end up in deliverables.',
    '',
    '--- KNOWLEDGE BASE CLAUDE.md ---',
    claudeMd,
  ].join('\n')
  return cached
}

export function invalidateContext(): void {
  cached = null
}
