import type { ReactElement } from 'react'
import { CopyCodeBlock, Disclosure } from '@parallelworks/ui'
import { toolSummary, useChatConfig, type ToolCallPart, type ToolPresentation, type ToolRendererProps } from '@parallelworks/ui/ai'

/** A GUFI command the server ran, as a shell takes it, and how it ended
 *  (gufiTrace.ts). */
export interface GufiCommand {
  engine: 'gufi_query' | 'gufi_vt' | 'gufi_sqlite3'
  /** The cluster or host it ran on, for a library there. */
  site?: string
  /** How to reach it from a terminal (pw ssh, or ssh with its options). */
  shell?: string
  command: string
  ms?: number
  /** When it started, as an ISO time. */
  at?: string
  exit?: number
  /** Why it has no exit status: a signal, a time limit, a failed session. */
  failed?: string
  /** Its error output, less the expected lines; absent when not kept. */
  stderr?: string
  /** Only the end of the error output was kept. */
  stderrCut?: boolean
  /** Expected lines of error output: no text or vector table in a directory. */
  expected?: number
  expectedExample?: string
}

/** Where a command runs, as the lines above it say. */
export function where(c: GufiCommand): string {
  if (c.engine === 'gufi_vt') return 'gufi_vt over the libraries, on the Studio host'
  return c.site ? `on ${c.site}, after ${c.shell ?? `pw ssh ${c.site}`}` : 'on the Studio host'
}

/** Whether the server said how the command ended (a command shown before
 *  it runs, such as a saved query's, has nothing to say). */
const ran = (c: GufiCommand) => c.at !== undefined || c.exit !== undefined || c.failed !== undefined || c.stderr !== undefined

/** The line above a command: where, when, how long, how it ended. */
export function commandMeta(c: GufiCommand): string {
  const at = c.at ? new Date(c.at) : null
  return [
    where(c),
    at && !Number.isNaN(at.getTime()) ? `at ${at.toLocaleTimeString()}` : '',
    c.ms != null ? `${c.ms} ms` : '',
    c.exit !== undefined ? `exit ${c.exit}` : c.failed ? c.failed : '',
  ].filter(Boolean).join(', ')
}

// A blob literal in the SQL, as written or inside a shell's single quotes.
const BLOB = /\bX('\\''|')([0-9a-fA-F]{64,})\1/g

/**
 * A command as the page shows it, which is not always what it runs: a search
 * by meaning carries its query vector as a hex literal, thousands of
 * characters in each statement, and a shell writes every quote inside a
 * statement as '\''. The vector is cut to its first bytes with a marker and
 * the quotes read as SQL. Copy and the command log have the command itself.
 */
export function shownCommand(command: string): { text: string; vectors: number; quotes: boolean } {
  let vectors = 0
  const cut = command.replace(BLOB, (_, q: string, hex: string) => {
    vectors++
    const what = hex.length % 8 === 0 ? `${hex.length / 8}-value query vector` : `${hex.length / 2}-byte value`
    return `X${q}${hex.slice(0, 8)}…${q} (${what})`
  })
  const text = cut.replaceAll(`'\\''`, `'`)
  return { text, vectors, quotes: text.length !== cut.length }
}

/** The line that says what the page changed, or null where it changed nothing. */
export function shownNote(commands: string[]): string | null {
  const shown = commands.map(shownCommand)
  const vectors = shown.some(s => s.vectors), quotes = shown.some(s => s.quotes)
  if (vectors) return `Query vectors are shortened on screen${quotes ? ' and shell quote escapes left out' : ''}. Copy has the full command.`
  return quotes ? 'Shell quote escapes are left out on screen. Copy has the command as it runs.' : null
}

const lines = (n: number, what = 'line') => `${n} ${what}${n === 1 ? '' : 's'}`

/** The toggle's words for a command's error output. */
export function errorsLabel(c: GufiCommand): string | null {
  if (c.stderr === undefined) return null
  const other = c.stderr ? c.stderr.split('\n').length : 0
  const expected = c.expected ?? 0
  if (!other && !expected) return null
  if (!other) return `Error output: ${lines(expected, 'expected line')}`
  return `Error output: ${lines(other)}${expected ? `, ${expected} expected` : ''}`
}

/** A command's error output: what it wrote as it came, and the expected
 *  "no such table" lines counted and named as expected. */
function CommandErrors({ c }: { c: GufiCommand }): ReactElement | null {
  if (!ran(c)) return null
  if (c.stderr === undefined) return <div className="gufi-cmd-errors-none muted">Its error output is not kept.</div>
  const label = errorsLabel(c)
  if (!label) return <div className="gufi-cmd-errors-none muted">No error output.</div>
  return (
    <Disclosure className="gufi-cmd-errors" summaryClassName="gufi-cmd-errors-summary" summary={label}>
      {c.stderr && (
        <>
          {c.stderrCut && <p className="gufi-cmd-errors-note muted">Only the end of its error output is kept.</p>}
          <pre className="gufi-stderr">{c.stderr}</pre>
        </>
      )}
      {!!c.expected && (
        <p className="gufi-cmd-errors-note muted">
          {c.expected === 1 ? 'One line says' : `${c.expected} lines say`} &ldquo;no such table&rdquo; for the text or
          vector tables. That is expected: the statement reads them in every directory, and directories without text
          or vectors have none.
          {c.expectedExample && <> For example: <code>{c.expectedExample}</code></>}
        </p>
      )}
    </Disclosure>
  )
}

/** The GUFI commands behind a search, a query or a chat tool call,
 *  collapsed, each ready to copy and run where it says, with its error
 *  output a click away. */
export function GufiCommands({ commands }: { commands?: GufiCommand[] }): ReactElement | null {
  if (!commands?.length) return null
  const note = shownNote(commands.map(c => c.command))
  return (
    <Disclosure
      className="gufi-cmds"
      summaryClassName="gufi-cmds-summary"
      panelClassName="gufi-cmds-panel"
      summary={`GUFI ${commands.length === 1 ? 'command' : `commands (${commands.length})`}`}
    >
      {commands.map((c, i) => (
        <div key={i} className="gufi-cmd">
          <div className="muted">{commandMeta(c)}</div>
          <CopyCodeBlock text={c.command} label="GUFI command">{shownCommand(c.command).text}</CopyCodeBlock>
          <CommandErrors c={c} />
        </div>
      ))}
      {note && <p className="gufi-cmds-note muted">{note}</p>}
    </Disclosure>
  )
}

// The commands a chat tool call ran, as its result arrived on the stream
// (adapter.ts); a stored conversation carries them on the call itself.
const streamed = new Map<string, GufiCommand[]>()

export function rememberToolCommands(callId: string, commands: GufiCommand[]): void {
  streamed.set(callId, commands)
  // A long session keeps the latest calls' only.
  if (streamed.size > 200) streamed.delete(streamed.keys().next().value!)
}

/** The chat tools that run GUFI: their blocks show its commands. */
export const GUFI_TOOLS = ['search_kb', 'query_corpus', 'list_kb_dir', 'read_kb_file']

/**
 * Drawn in a GUFI tool's row when it is opened: its result summary, which
 * the row shows itself until then, and the commands the call ran. The model
 * never sees the commands; the server sends them to the person who asked,
 * and only to them.
 */
export function GufiToolCommands({ part }: ToolRendererProps): ReactElement | null {
  const { strings } = useChatConfig()
  const commands = (part as ToolCallPart & { gufi?: GufiCommand[] }).gufi ?? streamed.get(part.id)
  const summary = part.status === 'running' ? strings.agentTranscript.running
    : part.status === 'ok' || part.status === 'error' ? toolSummary(part.name, part.args, part.result ?? '', part.status === 'error', { strings: strings.tools }) : ''
  if (!summary && !commands?.length) return null
  return (
    <div className="gufi-tool">
      {summary && <span className="gufi-tool-summary">{summary}</span>}
      <GufiCommands commands={commands} />
    </div>
  )
}

export const GUFI_TOOL_RENDERERS = Object.fromEntries(GUFI_TOOLS.map(n => [n, GufiToolCommands]))

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** How each GUFI tool's calls read, one and folded together. */
const WORDS: Record<string, { label: (a: Record<string, unknown>) => string; rollup: (n: number) => string }> = {
  search_kb: {
    label: a => { const q = a.query || a.text || a.name; return q ? `Searched for ${String(q)}` : 'Searched' },
    rollup: n => `ran ${count(n, 'search', 'searches')}`,
  },
  query_corpus: {
    label: a => (a.canned ? `Queried the index: ${String(a.canned)}` : 'Queried the index'),
    rollup: n => `ran ${count(n, 'index query', 'index queries')}`,
  },
  list_kb_dir: {
    label: a => (a.path ? `Listed ${String(a.path)}` : 'Listed the top folder'),
    rollup: n => `listed ${count(n, 'folder', 'folders')}`,
  },
  read_kb_file: {
    label: a => (a.path ? `Read ${String(a.path)}` : 'Read a file'),
    rollup: n => `read ${count(n, 'file', 'files')}`,
  },
}

/** A call's arguments, which are partial while they stream. */
function argsOf(json: string): Record<string, unknown> {
  try { const v = JSON.parse(json) as unknown; return v && typeof v === 'object' ? v as Record<string, unknown> : {} } catch { return {} }
}

/**
 * A GUFI tool's call in the transcript. With a presentation the package keeps
 * the drawing above inside the call's row, so a finished call folds into the
 * "Ran N tools" row like any other; without one a drawn call stays out of it,
 * and a reply's calls made a long list. Only a finished call folds: a running
 * one and a failed one keep their own rows.
 */
export function gufiToolPresentation(part: ToolCallPart): ToolPresentation | undefined {
  const words = WORDS[part.name]
  if (!words) return undefined
  const rollup = words.rollup
  return {
    label: words.label(argsOf(part.args)),
    summary: toolSummary(part.name, part.args, part.result ?? '', false),
    rollup,
    group: n => { const s = rollup(n); return s.charAt(0).toUpperCase() + s.slice(1) },
  }
}
