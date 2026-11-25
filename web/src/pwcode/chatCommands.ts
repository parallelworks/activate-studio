import { useCallback, useEffect, useState, useSyncExternalStore } from 'react'
import { paletteOptions, type PwCodePalette } from '@parallelworks/code'
import { onPick, pickedModel } from './newChatPick'
import { onStart, startOf, STUDIO_MACHINE } from './newChatStart'

/** What "/" lists in a pw code chat whose session is not shown: one row. */
export interface ChatCommand { name: string; description: string; hint?: string; kind: 'command' | 'skill' | 'prompt' }

/** A palette as the Studio answers it: pw code's, and what "/" leaves out
 *  (server-go chat, pwcodepalette.go). */
export type ListedPalette = PwCodePalette & { notListed?: string[] | null }

/** The names "/" leaves out of a session's commands. */
export function notListed(palette: unknown): string[] {
  return (palette as ListedPalette | null)?.notListed ?? []
}

/** A palette's rows, in its order. */
export function chatCommands(palette: ListedPalette | null): ChatCommand[] {
  if (!palette) return []
  const out = new Set(notListed(palette))
  const skills = new Set(palette.prompts ?? []), prompts = new Set((palette.mcpPrompts ?? []).map(p => p.name))
  // The package leaves the account choice out of a palette and puts its own first.
  const account = palette.commands?.find(c => c.name === 'claude-code')
  return paletteOptions(palette, account?.description).filter(c => !out.has(c.name)).map(c => ({
    name: c.name, description: c.description ?? '', hint: c.argumentHint,
    kind: prompts.has(c.name) || c.name.startsWith('mcp__') ? 'prompt' : skills.has(c.name) ? 'skill' : 'command',
  }))
}

/** How long a list is kept before "/" asks again. */
const KEPT_MS = 30_000
const kept = new Map<string, { at: number; palette: ListedPalette }>()
let viewer = 0
/** Forgets every list: someone else is the viewer, or their ACTIVATE
 *  sign-in changed (viewerCache.ts). */
export function forgetCommands(): void { kept.clear(); viewer++ }

const read = (url: string) => fetch(url).then(r => (r.ok ? r.json() as Promise<ListedPalette> : null), () => null)

/**
 * The commands a pw code chat takes: its session's own where it has one,
 * else what pw code has where the chat starts, for the folder and the model
 * chosen. `load` reads them, as "/" is typed; until the machine has
 * answered, and where it does not, they are pw code's own commands only,
 * which the Studio knows without asking it.
 */
export function useChatCommands({ conversationId, persona, machine, session }: {
  conversationId: string | null
  persona: string | null
  /** The chat's session, and the machine it is shown under. */
  machine?: string
  session?: string
}): { commands: ChatCommand[]; load: () => void } {
  const start = useSyncExternalStore(onStart, () => startOf(conversationId))
  const model = useSyncExternalStore(onPick, () => pickedModel(conversationId)?.model ?? '')
  const at = session && machine ? machine : start.machine?.id ?? STUDIO_MACHINE
  const base = `/api/chat/pwcode/start/machines/${encodeURIComponent(at)}/palette`
  const q = new URLSearchParams()
  if (start.folder) q.set('workspace', start.folder)
  if (model) q.set('model', model)
  if (persona) q.set('persona', persona)
  const url = session ? `/api/code/machines/${encodeURIComponent(at)}/sessions/${encodeURIComponent(session)}/palette` : `${base}?${q}`
  const own = `${base}?own=1`
  const [wanted, setWanted] = useState(0)
  const [, redraw] = useState(0)
  useEffect(() => {
    if (!wanted) return
    let gone = false
    const asked = viewer
    const take = (key: string) => (palette: ListedPalette | null) => {
      if (!palette || asked !== viewer) return
      kept.set(key, { at: Date.now(), palette })
      if (!gone) redraw(n => n + 1)
    }
    const had = kept.get(url)
    if (had && Date.now() - had.at < KEPT_MS) return
    if (!had) void read(own).then(take(own))
    void read(url).then(take(url))
    return () => { gone = true }
  }, [wanted, url, own])
  const load = useCallback(() => setWanted(n => n + 1), [])
  return { commands: chatCommands((kept.get(url) ?? kept.get(own))?.palette ?? null), load }
}
