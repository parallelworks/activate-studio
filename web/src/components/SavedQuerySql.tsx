import { useState } from 'react'
import { Button, Callout, CopyCodeBlock, IconButton, Loader, Popover } from '@parallelworks/ui'
import { CodeIcon } from '@parallelworks/ui/icons'
import { currentLibrary } from '../api'
import { where, type GufiCommand } from './GufiCommands'

/** What a query runs, from POST /api/query/sql (server/src/query.ts). */
export interface QuerySql {
  stages: { flag: '-I' | '-K' | '-E' | '-J' | '-G'; sql: string }[]
  commands: GufiCommand[]
}

/** What each of gufi_query's stages does. */
const STAGE: Record<QuerySql['stages'][number]['flag'], string> = {
  '-I': "creates each thread's table",
  '-K': 'creates the result table',
  '-E': 'runs in every directory',
  '-J': "adds each thread's rows to the result",
  '-G': 'orders and limits the result',
}

/**
 * A saved query's SQL: the SQL of each gufi_query stage the query compiles
 * to, and the command that runs it, each with a copy button. Read from the
 * server when opened, for the library the page is on, so it is the SQL a
 * run would use, labels and scope included.
 */
export function SavedQuerySql({ name, payload, button }: {
  name: string
  payload: Record<string, unknown>
  /** A text button that opens it, in place of the code icon. */
  button?: string
}) {
  const [sql, setSql] = useState<QuerySql | null>(null)
  const [error, setError] = useState('')
  const load = async () => {
    setError('')
    try {
      const lib = currentLibrary()
      const res = await fetch('/api/query/sql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...payload, ...(lib !== 'kb' ? { library: lib } : {}) }),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.message ?? data.error ?? `${res.status}`)
      setSql(data as QuerySql)
    } catch (e) {
      setError(String((e as Error).message ?? e))
    }
  }
  return (
    <Popover
      trigger={button
        ? <Button size="md" aria-label={`SQL for ${name}`}><CodeIcon aria-hidden="true" /> {button}</Button>
        : <IconButton size="xs" icon={<CodeIcon />} label={`SQL for ${name}`} />}
      title={`SQL for ${name}`}
      className="saved-sql"
      onOpenChange={open => { if (open) void load() }}
    >
      {error ? (
        <Callout tone="error">{error}</Callout>
      ) : !sql ? (
        <Loader inline size={14} label="Reading the SQL" />
      ) : (
        <>
          <p className="saved-sql-note">gufi_query runs {sql.stages.length === 1 ? 'this statement' : 'these stages in order'}; the command runs it in a terminal.</p>
          {sql.stages.map(s => (
            <div key={s.flag} className="saved-sql-stage">
              <div className="saved-sql-label"><code>{s.flag}</code> {STAGE[s.flag]}</div>
              <CopyCodeBlock text={s.sql} label={`${s.flag} SQL`} wrap>{s.sql}</CopyCodeBlock>
            </div>
          ))}
          {sql.commands.map((c, i) => (
            <div key={i} className="saved-sql-stage">
              <div className="saved-sql-label">Command, {where(c)}</div>
              <CopyCodeBlock text={c.command} label="GUFI command" wrap>{c.command}</CopyCodeBlock>
            </div>
          ))}
        </>
      )}
    </Popover>
  )
}
