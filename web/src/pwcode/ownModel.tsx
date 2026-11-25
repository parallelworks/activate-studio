import { useEffect } from 'react'
import { useChat } from '@parallelworks/ui/ai'
import { useEngine } from '../chatEngine'

/**
 * pw code runs on its own model (the one Settings names, or the one picked
 * in the message box), so a chat it answers needs none of the Studio
 * assistant's. The chat package takes messages only once a model is
 * listed, so while pw code answers the open chat and no model is listed,
 * because there is none or the listing failed, pw code stands in for one.
 */
export const PWCODE_MODEL = { id: 'pwcode', name: 'pw code' }

let standsIn = false
/** Whether pw code stands in when no model is listed. */
export function pwCodeStandsIn(): boolean { return standsIn }

/** Keeps that as what answers the open chat changes. */
export function PwCodeOwnModel({ conversationId }: { conversationId: string | null }) {
  const { models, hasLoadedModels, refreshModels } = useChat()
  const { info, current } = useEngine(conversationId)
  const on = !!info?.pwcode && (current || info.current || info.default) === 'pwcode'
  const listed = models.some(m => m.id !== PWCODE_MODEL.id)
  const standing = models.some(m => m.id === PWCODE_MODEL.id)
  standsIn = on
  useEffect(() => {
    if (hasLoadedModels && !listed && standing !== on) void refreshModels()
  }, [hasLoadedModels, listed, standing, on, refreshModels])
  useEffect(() => () => { standsIn = false }, [])
  return null
}
