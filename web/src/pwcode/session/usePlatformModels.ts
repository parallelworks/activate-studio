import type { PwCodePlatformModelList } from '@parallelworks/code'
import { useChat } from '@parallelworks/ui/ai'
import { useEffect, useMemo, useState } from 'react'

/** Model sources that are not ACTIVATE: their models are not ones pw code can run. */
const NOT_ACTIVATE = new Set(['endpoint', 'sidecar', 'none'])

const EMPTY: PwCodePlatformModelList['models'] = []

/**
 * The platform models a pw code session can run: the chat's own models
 * (useChat) while they come from ACTIVATE, and none while the Studio takes
 * its models from an OpenAI-compatible endpoint or a local model server,
 * whose models pw code cannot reach. While the source is unknown (the
 * access status has not answered, or failed) the chat's models stand.
 */
export function usePlatformModels(): PwCodePlatformModelList {
  const { models, providerIssues, refreshModels, isLoadingModels, modelsError } = useChat()
  const [source, setSource] = useState<string | null>(null)
  useEffect(() => {
    let gone = false
    fetch('/api/access')
      .then(r => (r.ok ? r.json() : null))
      .then((a: { models?: { source?: string } } | null) => { if (!gone) setSource(a?.models?.source ?? null) }, () => {})
    return () => { gone = true }
  }, [])
  const offList = source !== null && NOT_ACTIVATE.has(source)
  return useMemo(() => ({
    models: offList ? EMPTY : models,
    providerIssues: offList ? [] : providerIssues,
    isLoading: isLoadingModels,
    error: offList ? null : modelsError,
    refresh: refreshModels,
  }), [offList, models, providerIssues, isLoadingModels, modelsError, refreshModels])
}
