import type { ReactElement } from 'react'
import { CopyCodeBlock, Disclosure } from '@parallelworks/ui'

/** A GUFI command the server ran, as a shell takes it (gufiTrace.ts). */
export interface GufiCommand {
  engine: 'gufi_query' | 'gufi_vt'
  /** The cluster it ran on, for a library there. */
  site?: string
  command: string
  ms?: number
}

function where(c: GufiCommand): string {
  if (c.engine === 'gufi_vt') return 'gufi_vt over the libraries, on the Studio host'
  return c.site ? `on ${c.site}, after pw ssh ${c.site}` : 'on the Studio host'
}

/** The GUFI commands behind a search or query, collapsed under the results,
 *  each ready to copy and run where it says. */
export function GufiCommands({ commands }: { commands?: GufiCommand[] }): ReactElement | null {
  if (!commands?.length) return null
  return (
    <Disclosure
      className="gufi-cmds"
      summaryClassName="gufi-cmds-summary"
      summary={`GUFI ${commands.length === 1 ? 'command' : `commands (${commands.length})`}`}
    >
      {commands.map((c, i) => (
        <div key={i} className="gufi-cmd">
          <div className="muted">{where(c)}{c.ms != null ? `, ${c.ms} ms` : ''}</div>
          <CopyCodeBlock text={c.command} label="GUFI command">{c.command}</CopyCodeBlock>
        </div>
      ))}
    </Disclosure>
  )
}
