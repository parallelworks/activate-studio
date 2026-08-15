import fs from 'node:fs'
import path from 'node:path'
import { KB_ROOT } from './config.js'
import { effectiveSettings } from './settings.js'

/**
 * First-run layout for an empty knowledge base: a few directories and a
 * README, so a new deployment opens on something with shape rather than an
 * empty tree that gives a reader nothing to do.
 *
 * It runs only when the corpus is genuinely empty, so it can never disturb
 * existing material, and a deployment that wants a bare tree sets the
 * starter list to nothing.
 */

const DEFAULT_DIRS = ['papers', 'software', 'datasets', 'reports']

/** A singular label per directory, so filtering by kind works from the
 *  first day and the labels have a visible purpose. */
const DIR_LABEL: Record<string, string> = {
  papers: 'paper',
  software: 'software',
  datasets: 'dataset',
  reports: 'report',
}

function readme(appName: string, kbLabel: string, dirs: string[]): string {
  const lines = [
    `# ${kbLabel}`,
    '',
    `This directory is the corpus behind ${appName}. Everything in it is indexed for search and available to the assistant, which answers by reading these files and citing them.`,
    '',
  ]
  if (dirs.length) {
    lines.push('It starts with a few directories. Rename them, remove them, or add your own.', '')
    for (const d of dirs) lines.push(`- ${d}`)
    lines.push('')
  }
  lines.push(
    'Add material by dragging files onto the Library tree or with the Add button, which also accepts a URL. Files that arrive on disk another way are picked up by the background sync within minutes.',
    '',
    'Labels organize the corpus without moving anything: a label on a directory covers everything beneath it, and search, the query builder, and the assistant can all filter by them.',
    '',
  )
  return lines.join('\n')
}

/** True when the corpus has nothing in it (dot-directories aside). */
function isEmpty(dir: string): boolean {
  try {
    return fs.readdirSync(dir).filter(n => !n.startsWith('.')).length === 0
  } catch {
    return true
  }
}

export async function seedKnowledgeBase(log: (msg: string) => void): Promise<void> {
  const configured = process.env.KB_STARTER_DIRS
  const dirs = (configured === undefined ? DEFAULT_DIRS : configured.split(',').map(d => d.trim()).filter(Boolean))
    .map(d => d.replace(/[^A-Za-z0-9._-]/g, '')).filter(Boolean)

  if (fs.existsSync(KB_ROOT) && !isEmpty(KB_ROOT)) return
  try {
    fs.mkdirSync(KB_ROOT, { recursive: true })
    for (const d of dirs) fs.mkdirSync(path.join(KB_ROOT, d), { recursive: true })
    const eff = effectiveSettings()
    fs.writeFileSync(path.join(KB_ROOT, 'README.md'), readme(eff.appName, eff.kbLabel, dirs))
    log(`seeded an empty knowledge base with ${dirs.length} directories and a README`)
  } catch (err) {
    log(`could not seed the knowledge base: ${String((err as Error).message ?? err)}`)
    return
  }

  // Labels are a nicety, and a filesystem that refuses them (or an index
  // that is not built yet) must not turn a fresh start into an error.
  try {
    const { applyTagsCore } = await import('./tags.js')
    for (const d of dirs) {
      const label = DIR_LABEL[d]
      if (label) await applyTagsCore([d], [label], []).catch(() => {})
    }
  } catch { /* labels are optional */ }
}
