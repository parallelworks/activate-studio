import { useEffect, useState } from 'react'
import type { RunFileResult } from '@parallelworks/ui'

/**
 * The UI package's run file hook, bound in main.tsx: the step dialogs of the
 * run view (summary, log, script) read a file in the run's directory through
 * the server, which asks the platform with the viewer's key. A null path
 * fetches nothing; a refresh interval polls while the dialog is open.
 */
export function useRunFile(slug: string, path: string | null, options?: { refreshInterval?: number }): RunFileResult {
  const [state, setState] = useState<RunFileResult>({ data: undefined, isLoading: !!path, error: undefined })
  const every = options?.refreshInterval
  useEffect(() => {
    if (!slug || !path) { setState({ data: undefined, isLoading: false, error: undefined }); return }
    let live = true
    const ctrl = new AbortController()
    setState(s => ({ ...s, isLoading: true }))
    const load = () => fetch(`/api/runs/file?slug=${encodeURIComponent(slug)}&path=${encodeURIComponent(path)}`, { signal: ctrl.signal })
      .then(async r => {
        const body = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(body?.error ?? `HTTP ${r.status}`)
        if (live) setState({ data: String(body.text ?? ''), isLoading: false, error: undefined })
      })
      .catch(error => { if (live && !ctrl.signal.aborted) setState(s => ({ data: s.data, isLoading: false, error })) })
    void load()
    const timer = every ? setInterval(load, every) : undefined
    return () => { live = false; ctrl.abort(); if (timer) clearInterval(timer) }
  }, [slug, path, every])
  return state
}
