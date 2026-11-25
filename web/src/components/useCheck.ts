import { useEffect, useRef, useState } from 'react'

export interface Checked<T> {
  /** What the check of the key as it is now answered. */
  result: T | null
  error: string
  busy: boolean
  /** Runs it again, as after something was changed there. */
  again: () => void
}

/**
 * A check that runs by itself: once `key` has stood for a moment, and again
 * when it changes, with the one before cancelled. No key is nothing to
 * check. An answer is only ever of the key as it is now.
 */
export function useCheck<T>(key: string | null, run: (signal: AbortSignal) => Promise<T>, waitMs = 350): Checked<T> {
  const [state, setState] = useState<{ key: string | null; result: T | null; error: string }>({ key: null, result: null, error: '' })
  const [round, setRound] = useState(0)
  const latest = useRef(run)
  latest.current = run
  useEffect(() => {
    if (key === null) return
    const stop = new AbortController()
    const done = (result: T | null, error: string) => { if (!stop.signal.aborted) setState({ key, result, error }) }
    const timer = window.setTimeout(() => {
      latest.current(stop.signal).then(r => done(r, ''), e => done(null, String((e as Error)?.message ?? e)))
    }, waitMs)
    return () => { window.clearTimeout(timer); stop.abort() }
  }, [key, round, waitMs])
  const mine = key !== null && state.key === key
  return {
    result: mine ? state.result : null,
    error: mine ? state.error : '',
    busy: key !== null && !mine,
    again: () => { setState({ key: null, result: null, error: '' }); setRound(n => n + 1) },
  }
}
