import type { PwCodePlatformModelList } from '@parallelworks/code'
import { useChat } from '@parallelworks/ui/ai'
import { useEffect, useMemo, useState } from 'react'
import { heldModels } from '../ownModel'

/** Model sources that are not ACTIVATE: their models are not ones pw code can run. */
const NOT_ACTIVATE = new Set(['endpoint', 'env-endpoint', 'admin-endpoint', 'sidecar', 'none'])

const EMPTY: PwCodePlatformModelList['models'] = []

/** How long where the models come from is kept: a chat switched to reads
 *  it again only after this, and the setting changes rarely. */
const SOURCE_KEPT_MS = 60_000

// Where the Studio's models come from (GET /api/access), kept for the page.
const modelSource: { value: string | null; at: number; read: Promise<string | null> | null } = { value: null, at: 0, read: null }

function readModelSource(): Promise<string | null> {
  if (modelSource.read && Date.now() - modelSource.at < SOURCE_KEPT_MS) return modelSource.read
  modelSource.at = Date.now()
  modelSource.read = fetch('/api/access')
    .then(r => (r.ok ? r.json() : null))
    .then((a: { models?: { source?: string } } | null) => (modelSource.value = a?.models?.source ?? null))
    .catch(() => { modelSource.read = null; return modelSource.value })
  return modelSource.read
}

/** Forgets where the models come from, for the next person. */
export function forgetModelSource(): void {
  modelSource.value = null
  modelSource.at = 0
  modelSource.read = null
}

/**
 * The platform models a pw code session can run: the chat's own models
 * (useChat) while they come from ACTIVATE, and none while the Studio takes
 * its models from an OpenAI-compatible endpoint or a local model server,
 * whose models pw code cannot reach. While the source is unknown (the
 * access status has not answered, or failed) the chat's models stand.
 */
export function usePlatformModels(): PwCodePlatformModelList {
  const { models, providerIssues, refreshModels, isLoadingModels, modelsError } = useChat()
  const [source, setSource] = useState<string | null>(() => modelSource.value)
  useEffect(() => {
    let gone = false
    readModelSource().then(v => { if (!gone) setSource(v) }, () => {})
    return () => { gone = true }
  }, [])
  const offList = source !== null && NOT_ACTIVATE.has(source)
  return useMemo(() => ({
    models: offList ? EMPTY : heldModels() ?? models,
    providerIssues: offList ? [] : providerIssues,
    isLoading: isLoadingModels,
    error: offList ? null : modelsError,
    refresh: refreshModels,
  }), [offList, models, providerIssues, isLoadingModels, modelsError, refreshModels])
}
