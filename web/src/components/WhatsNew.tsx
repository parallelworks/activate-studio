import { useEffect, useState } from 'react'
import { Button, Callout, Loader } from '@parallelworks/ui'
import { changelogApi, type Changelog } from '../api'
import { ChangeList } from './ChangeList'

/** Versions shown at first, and added by each "Show older versions". */
const FIRST = 10
const MORE = 20

/**
 * Help, What's new: the change log, versions newest first, each change's
 * title opening to its description. The server serves the change log the
 * build carries; a build without one says so.
 */
export function WhatsNew() {
  const [log, setLog] = useState<Changelog | null>(null)
  const [error, setError] = useState('')
  const [count, setCount] = useState(FIRST)

  useEffect(() => {
    let stop = false
    changelogApi.get()
      .then(l => { if (!stop) setLog(l) })
      .catch(e => { if (!stop) setError(String((e as Error)?.message ?? e)) })
    return () => { stop = true }
  }, [])

  return (
    <div className="whats-new">
      <h1>What's new</h1>
      {error ? (
        <Callout tone="error">Could not read the change log: {error}</Callout>
      ) : !log ? (
        <Loader inline size={14} label="Reading the change log" />
      ) : (
        <>
          {!log.included && <Callout tone="muted">The change log is not included in this build.</Callout>}
          {log.versions.length > 0 && (
            <>
              <p className="muted help-hint">This is version {log.current}. Each version's changes, newest first; open one to read its description.</p>
              <ChangeList versions={log.versions.slice(0, count)} headingLevel={2} />
              {log.versions.length > count && (
                <div className="query-actions">
                  <Button variant="secondary" size="sm" onClick={() => setCount(c => c + MORE)}>Show older versions</Button>
                </div>
              )}
            </>
          )}
          {log.included && !log.versions.length && <p className="muted">The change log lists no versions.</p>}
        </>
      )}
    </div>
  )
}
