import { AsyncLocalStorage } from 'node:async_hooks'
import { shellFor, siteName } from './remoteExec.js'

/**
 * The GUFI commands a request ran, so the Search and Query views can show
 * them. A route wraps its work in traced(); each place that runs GUFI notes
 * its argv with noteGufi(), which does nothing outside a trace.
 */

export interface GufiCommand {
  /** gufi_query on one index, or one gufi_vt statement over several. */
  engine: 'gufi_query' | 'gufi_vt'
  /** The cluster it ran on, for a library there; absent on this host. */
  site?: string
  /** As a shell would take it, ready to copy. */
  command: string
  ms?: number
}

const store = new AsyncLocalStorage<GufiCommand[]>()
const MAX = 40

export async function traced<T>(fn: () => Promise<T>): Promise<{ result: T; commands: GufiCommand[] }> {
  const commands: GufiCommand[] = []
  const result = await store.run(commands, fn)
  return { result, commands }
}

/** Records a command; `script` is SQL on its standard input, shown as a
 *  here-document so a long statement reads as written. */
export function noteGufi(argv: string[], o: { engine?: GufiCommand['engine']; site?: string; ms?: number; script?: string } = {}): void {
  const list = store.getStore()
  if (!list || list.length >= MAX) return
  const line = argv.map(shellQuote).join(' ')
  const command = o.script !== undefined ? `${line} <<'SQL'\n${o.script}\nSQL` : line
  list.push({ engine: o.engine ?? 'gufi_query', ...(o.site ? { site: o.site } : {}), command, ...(o.ms !== undefined ? { ms: o.ms } : {}) })
}

/** The commands as lines for a person to read and copy: a cluster's
 *  command under a note naming where to run it. */
export function formatCommands(commands: GufiCommand[], cap = 4000): string {
  const text = commands.map(c => [
    c.engine === 'gufi_vt' ? '# one gufi_vt statement over the libraries' : '',
    c.site ? `# on ${siteName(c.site)} (${shellFor(c.site).join(' ')})` : '',
    c.command,
  ].filter(Boolean).join('\n')).join('\n\n')
  return text.length > cap ? `${text.slice(0, cap)}\n[truncated]` : text
}

/** One argument as a POSIX shell reads it back unchanged. */
export function shellQuote(a: string): string {
  if (a === '\x1e') return "$'\\x1e'"
  if (/^[A-Za-z0-9_/.,:=@+%-]+$/.test(a)) return a
  return `'${a.replace(/'/g, `'\\''`)}'`
}
