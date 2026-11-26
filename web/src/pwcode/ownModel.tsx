import { useEffect, useState, useSyncExternalStore } from 'react'
import { type ModelsList, useChat } from '@parallelworks/ui/ai'
import { useEngine } from '../chatEngine'
import { onPick, pickedModel } from './newChatPick'
import { onStart, startOf } from './newChatStart'

/**
 * pw code runs on its own model (the one Settings names, or the one picked
 * in the message box), so a chat it answers needs none of the Studio
 * assistant's. The chat package takes messages only once a model is
 * listed, so while pw code answers the open chat and no model is listed,
 * because there is none or the listing failed, pw code stands in for one.
 * A new chat of its with no model at all is held: its box stays closed
 * until one is chosen or set.
 */
export const PWCODE_MODEL = { id: 'pwcode', name: 'pw code' }

/** What a new pw code chat with no model at all says, in one line: every
 *  person meets it once where Settings names no model for pw code (the
 *  choice is kept for them after). Select a model is the picker's own
 *  word for it, in the box. */
export const HELD_TITLE = 'Choose a model to start'
export const HELD_SAY = 'Choose one with Select a model, below.'

/** The no-model empty state's button for a person who is signed in and
 *  only has no model: it opens the picker in the box, which is the fix,
 *  rather than Accounts, which has nothing for them to do. */
export const CHOOSE_MODEL = '#choose-model'

/** Opens the model picker of the message box under root. */
export function openModelPicker(root: ParentNode | null): void {
  const box = root ?? document
  const picker = box.querySelector<HTMLElement>('[data-testid="agents-model-chip"]')
    ?? [...box.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.trim() === 'Select a model')
  picker?.focus()
  picker?.click()
}

let standsIn = false
/** Whether pw code stands in when no model is listed. */
export function pwCodeStandsIn(): boolean { return standsIn && !held }

let held = false
let heldList: ModelsList['models'] | null = null
const listeners = new Set<() => void>()
const onHeld = (cb: () => void) => { listeners.add(cb); return () => { listeners.delete(cb) } }
function hold(on: boolean): void {
  if (held === on) return
  held = on
  for (const l of [...listeners]) l()
}

/** Whether the open chat waits for a model. */
export function modelHeld(): boolean { return held }

/** Whether the chat's models are held back, keeping them for the picker. */
export function holdModels(models: ModelsList['models']): boolean {
  const next = held ? models : null
  const changed = (next?.length ?? -1) !== (heldList?.length ?? -1)
  heldList = next
  if (changed) for (const l of [...listeners]) l()
  return held
}

/** How many models the open chat that waits for one can choose among; -1
 *  before they are known. */
export function useHeldModelCount(): number { return useSyncExternalStore(onHeld, () => heldList?.length ?? -1) }

/** What the no-model empty state says where pw code lists no model to
 *  choose: then Accounts is where one is added. */
export const HELD_NONE_SAY = 'pw code lists no model for you. Add one under Settings, Accounts.'

/** The models held back from the chat package, which a pw code picker lists. */
export function heldModels(): ModelsList['models'] | null { return heldList }

export function useModelHeld(): boolean { return useSyncExternalStore(onHeld, modelHeld) }

const ASK_AGAIN_MS = 20_000

/** Whether pw code has no model of its own here, asked while `on`: again
 *  when the person comes back, and now and then while it has none. */
function useNeedsModel(on: boolean): boolean {
  const [needed, setNeeded] = useState(false)
  useEffect(() => {
    if (!on) { setNeeded(false); return }
    let gone = false
    const ask = () => {
      if (document.hidden) return
      fetch('/api/chat/pwcode/model').then(r => (r.ok ? r.json() : null))
        .then((d: { needed?: boolean } | null) => { if (!gone && d) setNeeded(!!d.needed) }, () => {})
    }
    if (!needed) ask()
    const timer = needed ? window.setInterval(ask, ASK_AGAIN_MS) : 0
    document.addEventListener('visibilitychange', ask)
    return () => { gone = true; window.clearInterval(timer); document.removeEventListener('visibilitychange', ask) }
  }, [on, needed])
  return needed
}

/** Keeps that as what answers the open chat changes. `fresh`: the chat has
 *  no session yet. */
export function PwCodeOwnModel({ conversationId, fresh }: { conversationId: string | null; fresh: boolean }) {
  const { models, hasLoadedModels, refreshModels } = useChat()
  const { info, current } = useEngine(conversationId)
  const on = !!info?.pwcode && (current || info.current || info.default) === 'pwcode'
  const picked = useSyncExternalStore(onPick, () => !!pickedModel(conversationId)?.model)
  // On another machine, the session runs on that one's own.
  const elsewhere = useSyncExternalStore(onStart, () => !!startOf(conversationId).machine)
  const asks = on && fresh && !info?.model && !info?.lastModel && !picked && !elsewhere
  const needed = useNeedsModel(asks)
  const holds = asks && needed
  const listed = models.some(m => m.id !== PWCODE_MODEL.id)
  const standing = models.some(m => m.id === PWCODE_MODEL.id)
  standsIn = on
  useEffect(() => {
    hold(holds)
    if (!hasLoadedModels) return
    if (holds !== !!heldList || (!holds && !listed && standing !== on)) void refreshModels()
  }, [holds, hasLoadedModels, listed, standing, on, refreshModels])
  useEffect(() => () => { standsIn = false; heldList = null; hold(false) }, [])
  return null
}
