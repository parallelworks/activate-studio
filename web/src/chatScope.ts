import { useEffect } from 'react'
import type { PublicLibrary } from './api'
import { setChatLibrary } from './labelScope'
import { saveScope, scopeBody, settleScope, useScopeValue, type Scope } from './scope'

/**
 * The libraries the chat searches, kept in this browser as Search and Query
 * keep theirs, and handed to the adapter, which sends them with each turn.
 * Until the config names the libraries the kept choice goes as it is, and
 * the server drops ids it has no library by.
 */
export function useChatScope(libraries: PublicLibrary[] | undefined): [Scope, (s: Scope) => void] {
  const kept = useScopeValue('chat')
  const libs = libraries ?? []
  const scope = libs.length ? settleScope(kept, libs) : kept
  const sent = scopeBody(scope, libraries ? libs.length : undefined).library ?? null
  const sentKey = JSON.stringify(sent)
  useEffect(() => { setChatLibrary(sent) }, [sentKey]) // eslint-disable-line react-hooks/exhaustive-deps
  return [scope, (next: Scope) => saveScope('chat', libs.length ? settleScope(next, libs) : next)]
}
