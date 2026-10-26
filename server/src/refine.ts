import { KbError } from './kb.js'
import { parseNameFilter, type RefineSpec } from './gufi.js'

/**
 * A refined search's parts, as the Search page, its address and the
 * assistant's search tool send them: `name` (names and folder paths),
 * `text` (the words in files, as fts5 syntax, and their meaning) and
 * `match` ('any' lists either kind of match, 'all' looks for the text only
 * in the files whose name or path matches). Null when neither field is
 * filled in, which leaves the single search box's behavior as it was.
 */
export function refineOf(p: { name?: unknown; text?: unknown; match?: unknown }): RefineSpec | null {
  const name = String(p.name ?? '').trim().slice(0, 500)
  const text = String(p.text ?? '').trim().slice(0, 500)
  if (!name && !text) return null
  return { name, text, match: String(p.match ?? '').toLowerCase() === 'all' ? 'all' : 'any' }
}

/** The text with fts5's syntax taken out, as the phrase searched by
 *  meaning: operators, quotes, brackets, prefix stars, column filters and
 *  NEAR's distance go, the words stay in order. */
export function meaningPhrase(text: string): string {
  return text
    .replace(/\bNEAR\s*\(/g, '(')
    .replace(/,\s*\d+\s*\)/g, ')')
    .replace(/\b(AND|OR|NOT)\b/g, ' ')
    .replace(/\b[\p{L}_][\p{L}\p{N}_]*\s*:/gu, ' ')
    .replace(/["()*^+{}]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

type Checker = (expr: string) => string | null
let checker: Promise<Checker | null> | null = null

/** fts5's own parser, from node:sqlite where this Node has it with fts5;
 *  null where it does not, and checkShape is all there is. */
function fts5Checker(): Promise<Checker | null> {
  checker ??= import('node:sqlite').then(m => {
    const db = new m.DatabaseSync(':memory:')
    db.exec('CREATE VIRTUAL TABLE t USING fts5(x)')
    const st = db.prepare('SELECT count(*) FROM t WHERE t MATCH ?')
    return (expr: string) => {
      try { st.all(expr); return null } catch (e) { return String((e as Error).message ?? e) }
    }
  }).catch(() => null)
  return checker
}

/** What a hand check finds wrong with an expression: an odd number of
 *  quotes, brackets that do not pair, or an operator at an end. */
function checkShape(expr: string): string | null {
  if ((expr.match(/"/g)?.length ?? 0) % 2) return 'a quotation mark is not closed'
  let depth = 0
  for (const c of expr.replace(/"[^"]*"/g, '')) {
    if (c === '(') depth++
    if (c === ')' && --depth < 0) return 'a closing bracket has no opening one'
  }
  if (depth) return 'a bracket is not closed'
  if (/^\s*(AND|OR|NOT)\b|\b(AND|OR|NOT)\s*$/.test(expr)) return 'AND, OR and NOT need words on both sides'
  return null
}

/**
 * Checks the text field before a search runs, since a malformed fts5
 * expression otherwise fails in every directory and the search finds
 * nothing without saying why. Throws a 400 that says what is wrong and how
 * to write it.
 */
export async function validateText(text: string): Promise<void> {
  if (!text.trim()) return
  const shape = checkShape(text)
  const parser = await fts5Checker()
  const err = shape ?? (parser ? parser(text) : null)
  if (!err) return
  const why = err
    .replace(/^fts5:\s*/, '')
    .replace(/^no such column: (.+)$/, (_m, c) => `"${c}" was read as a column name, so a word with a hyphen or a colon needs quotes`)
  throw new KbError(400, `The text search cannot be read (${why}). Put a word with punctuation in double quotes, such as "covid-19", and write AND, OR and NOT in capitals between words.`)
}

/** Whether a path below its library matches the name field, as the walk's
 *  filter does: for a search by meaning a walk could not carry (an index
 *  from before the current model), whose hits are filtered here. */
export function namePathMatches(name: string, relPath: string): boolean {
  const groups = parseNameFilter(name)
  if (!groups.length) return true
  const file = relPath.split('/').pop() ?? relPath
  const folders = relPath.split('/').slice(0, -1).join('/').toLowerCase()
  const like = (s: string, t: string) => s.toLowerCase().includes(t.toLowerCase())
  const glob = (s: string, g: string) => new RegExp(`^${g.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.')}$`, 'i').test(s)
  const one = (t: { kind: string; text: string }) => (t.kind === 'glob'
    ? glob(t.text.includes('/') ? relPath : file, t.text)
    : like(file, t.text) || like(folders, t.text))
  return groups.some(g => g.every(t => (t.neg ? !one(t) : one(t))))
}
