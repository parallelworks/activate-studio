import { useSyncExternalStore } from 'react'

/** What browsers that install web apps fire when the Studio qualifies. */
interface InstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

// The event fires once and early, possibly before React renders, so it is
// caught when this module loads; main.tsx imports it first.
let pending: InstallPromptEvent | null = null
const listeners = new Set<() => void>()
const changed = () => { for (const l of listeners) l() }

if (typeof window !== 'undefined') {
  window.addEventListener('beforeinstallprompt', e => {
    // The rail offers it; the browser's own infobar would duplicate it.
    e.preventDefault()
    pending = e as InstallPromptEvent
    changed()
  })
  window.addEventListener('appinstalled', () => { pending = null; changed() })
}

const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l) } }

/**
 * Whether the browser can install the Studio as an app now, and a function
 * that asks it to. Only Chromium browsers offer this, and never inside the
 * platform's frame; elsewhere it stays false.
 */
export function useInstall(): { canInstall: boolean; install: () => Promise<void> } {
  const canInstall = useSyncExternalStore(subscribe, () => pending !== null, () => false)
  const install = async () => {
    const e = pending
    if (!e) return
    await e.prompt()
    // A prompt shows once; the browser fires a new event if it is still installable.
    pending = null
    changed()
  }
  return { canInstall, install }
}
