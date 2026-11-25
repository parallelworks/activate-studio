import { useEffect, useState, useSyncExternalStore } from 'react'
import { isClaudeModel, type PwCodePicker } from '@parallelworks/code'
import type { BoundSetting } from '@parallelworks/ui/ai'
import { AgentModelControls } from '../code/AgentModelControls'
import { pwCode } from './api'
import { engineInfo, useEngine } from '../chatEngine'
import { onPick, pickModel, pickedModel } from './newChatPick'
import { type MachineFacts, onStart, startOf, STUDIO_MACHINE } from './newChatStart'
import { usePlatformModels } from './session/usePlatformModels'
import { PwCodeWords } from './PWCodeSession'


/**
 * pw code's model picker in a new pw code chat, before its first message:
 * until then the chat has no session, whose own composer picks its model,
 * and the composer's picker sets the Studio assistant's model only, so it
 * is hidden while pw code answers. It lists what the session would run on
 * (the platform's models pw code can run, and Claude's where the machine it
 * starts on runs Claude Code), starting from the model Settings names, and the first
 * message carries the pick (newChatPick.ts).
 */
export function NewChatModel({ conversationId }: { conversationId: string | null }) {
  const [machine, setMachine] = useState<MachineFacts>({})
  // The model Settings names, from what the Studio said of this chat
  // (chatEngine.ts; the chat bar's engine menu reads it).
  const byDefault = useEngine(conversationId).info?.model ?? ''
  useEffect(() => {
    let gone = false
    fetch('/api/code/sessions').then(r => (r.ok ? r.json() : null))
      .then((d: { machines?: MachineFacts[] } | null) => { if (!gone) setMachine(d?.machines?.find(m => m.id === STUDIO_MACHINE) ?? {}) })
      .catch(() => {})
    void engineInfo(conversationId)
    return () => { gone = true }
  }, [conversationId])
  const [pick, setPick] = useState(() => pickedModel(conversationId))
  useEffect(() => {
    setPick(pickedModel(conversationId))
    return onPick(() => setPick(pickedModel(conversationId)))
  }, [conversationId])
  const platform = usePlatformModels()
  // The machine the chat is set to start on, whose models these are.
  const chosen = useSyncExternalStore(onStart, () => startOf(conversationId)).machine
  const on = chosen ?? machine
  const claudeReady = !!(on.claudeCodeAllowed && on.claudeInstalled)
  const [panel, setPanel] = useState<PwCodePicker>(null)
  const model: BoundSetting = { value: pick?.model || (chosen && !claudeReady && isClaudeModel(byDefault) ? '' : byDefault) || null, onChange: v => pickModel(conversationId, { model: v }) }
  const allocation: BoundSetting = { value: pick?.allocation ?? null, onChange: v => pickModel(conversationId, { allocation: v }) }
  return (
    <span className="pwcode-new-model" data-testid="pwcode-new-model">
      <AgentModelControls key={chosen?.id ?? STUDIO_MACHINE} api={pwCode} platform={platform} machine={chosen?.id ?? STUDIO_MACHINE}
        claudeReady={claudeReady} canStart
        model={model} allocation={allocation} account={undefined} panel={panel} onPanelChange={setPanel} />
    </span>
  )
}

/** Whether pw code answers the open chat: the chat bar's engine menu marks
 *  the canvas with it (PWCodeEngine.tsx). */
export function useAnsweredByPWCode(): boolean {
  const read = () => document.querySelector('.chat-canvas')?.getAttribute('data-engine') === 'pwcode'
  const [on, setOn] = useState(read)
  useEffect(() => {
    setOn(read())
    const canvas = document.querySelector('.chat-canvas')
    if (!canvas) return
    const mo = new MutationObserver(() => setOn(read()))
    mo.observe(canvas, { attributes: true, attributeFilter: ['data-engine'] })
    return () => mo.disconnect()
  }, [])
  return on
}

/** The picker, where it belongs: a pw code chat with no session yet. */
export function NewChatModelSlot({ conversationId, hasSession }: { conversationId: string | null; hasSession: boolean }) {
  const pwcode = useAnsweredByPWCode()
  return pwcode && !hasSession ? <PwCodeWords><NewChatModel conversationId={conversationId} /></PwCodeWords> : null
}
