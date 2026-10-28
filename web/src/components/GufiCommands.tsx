import type { ReactElement } from 'react'
import { CopyCodeBlock, Disclosure } from '@parallelworks/ui'
import { toolSummary, useChatConfig, type ToolCallPart, type ToolRendererProps } from '@parallelworks/ui/ai'

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
          <CopyCodeBlock text={c.command} label="GUFI command">{c.command}</CopyCodeBlock>
          <CommandErrors c={c} />
        </div>
      ))}
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
export const GUFI_TOOLS = ['search_kb', 'query_corpus', 'list_kb_dir']

/**
 * Drawn under a GUFI tool's row in the chat: its result summary, which the
 * row shows itself for other tools, and the commands the call ran. The
 * model never sees the commands; the server sends them to the person who
 * asked, and only to them.
 */
export function GufiToolCommands({ part }: ToolRendererProps): ReactElement | null {
  const { strings } = useChatConfig()
  const commands = (part as ToolCallPart & { gufi?: GufiCommand[] }).gufi ?? streamed.get(part.id)
  // A failed call's reason is on the row already; a waiting one has none.
  const summary = part.status === 'running' ? strings.agentTranscript.running
    : part.status === 'ok' ? toolSummary(part.name, part.args, part.result ?? '', false, { strings: strings.tools }) : ''
  if (!summary && !commands?.length) return null
  return (
    <div className="gufi-tool">
      {summary && <span className="gufi-tool-summary">{summary}</span>}
      <GufiCommands commands={commands} />
    </div>
  )
}

export const GUFI_TOOL_RENDERERS = Object.fromEntries(GUFI_TOOLS.map(n => [n, GufiToolCommands]))
