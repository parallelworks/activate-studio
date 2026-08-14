import fs from 'node:fs/promises'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { KB_ROOT, gufiAvailable } from '../config.js'
import { corpusStats } from '../gufi.js'

let cached: string | null = null

function pwCatalog(): Promise<string> {
  return new Promise(resolve => {
    execFile('pw', ['workflows', 'ls', '-o', 'json'], { timeout: 20_000 }, (_e, stdout) => {
      try {
        const wfs = JSON.parse(stdout ?? '[]') as any[]
        resolve(wfs.map(w => {
          const tags = (w.tags ?? []).filter(Boolean)
          return `- ${w.name}: ${w.description || w.displayName || ''}${tags.length ? ` [tags: ${tags.join(', ')}]` : ''}`
        }).join('\n'))
      } catch {
        resolve(stdout?.trim() ?? '')
      }
    })
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
  const workflows = await pwCatalog()

  cached = [
    `You are the assistant inside ACTIVATE Studio, a knowledge base interface. The knowledge base at ${KB_ROOT} is the working corpus for this deployment.`,
    '',
    'Ground every answer about the knowledge base in its actual content: call search_kb first, read the files that matter with read_kb_file, and cite the files you used. Write every citation as a markdown link of the form [relative/path.md](#open=file:relative/path.md) so the reader can click through to the document in the library viewer; the viewer renders text, office documents, PDFs, images, and 3D models, so link whichever file grounds the claim. Percent-encode spaces in the link target (%20) and leave the link text as the plain path. If retrieval returns nothing relevant, say so instead of guessing.',
    '',
    'The knowledge base carries a labeling system: files and directory subtrees can be tagged (for example research or theoretical versus validated or experimental). Search results show labels as [labels: ...], and search_kb accepts a tags filter. When retrieved material carries labels that bear on its reliability or provenance, state them in your answer, and never present material labeled as research or theoretical as established fact.',
    '',
    'You have memory of past conversations in this Studio: every chat session is exported as markdown into the chat-sessions/ directory, labeled chat-session. When the user asks about previous chats, earlier sessions, or something "we discussed before", search with search_kb using tags: ["chat-session"] (and list_kb_dir on chat-sessions/ for a chronological listing), then read the relevant transcripts. These transcripts are context about prior discussion, not knowledge base source material; distinguish the two when citing.',
    '',
    statsLine,
    '',
    'Platform workflows registered in this account. These are composable building blocks: recommend which fit a task, preview a DAG with get_workflow before anything runs, validate with run_workflow dry_run when acting on your own initiative, and treat a user request to run something as the authorization to run it, with no second confirmation. Monitor with workflow_runs and workflow_run_detail. pw_help discovers the wider platform command surface.',
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
