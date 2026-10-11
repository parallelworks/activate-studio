import { ReactElement, useState } from 'react'

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

function Copy({ text }: { text: string }): ReactElement {
  const [done, setDone] = useState(false)
  return (
    <button type="button" className="link-btn gufi-copy"
      onClick={() => { void navigator.clipboard?.writeText(text).then(() => { setDone(true); setTimeout(() => setDone(false), 1500) }).catch(() => {}) }}>
      {done ? 'Copied' : 'Copy'}
    </button>
  )
}

/** The GUFI commands behind a search or query, collapsed under the results,
 *  each ready to copy and run where it says. */
export function GufiCommands({ commands }: { commands?: GufiCommand[] }): ReactElement | null {
  if (!commands?.length) return null
  return (
    <details className="gufi-cmds">
      <summary>GUFI {commands.length === 1 ? 'command' : `commands (${commands.length})`}</summary>
      {commands.map((c, i) => (
        <div key={i} className="gufi-cmd">
          <div className="gufi-cmd-head">
            <span className="muted">{where(c)}{c.ms != null ? `, ${c.ms} ms` : ''}</span>
            <Copy text={c.command} />
          </div>
          <pre>{c.command}</pre>
        </div>
      ))}
    </details>
  )
}
