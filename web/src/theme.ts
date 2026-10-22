import { useEffect, useState } from 'react'

/**
 * The theme the page is actually painted in, live. App decides the
 * effective theme (explicit choice, system preference, deployment
 * default, or the theme an embed is told) and writes it to
 * document.documentElement.dataset.theme; everything that picks a
 * dark or light asset reads it from there. A one-time read at render is
 * how the empty-chat mark stayed on the old icon after a toggle, so this
 * subscribes to the attribute instead of sampling it.
 */
export function useEffectiveTheme(): 'dark' | 'light' {
  const read = () => (document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light')
  const [theme, setTheme] = useState<'dark' | 'light'>(read)
  useEffect(() => {
    const obs = new MutationObserver(() => setTheme(read()))
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    setTheme(read())
    return () => obs.disconnect()
  }, [])
  return theme
}

/** What a person picked for this browser: a theme of its own, or the
 *  system's (the device's preference, over the deployment's default). */
export type ThemeChoice = 'light' | 'dark' | 'system'

const CHOICE_KEY = 'ade-theme'
const CHOICE_EVENT = 'ade:theme-choice'

export function themeChoice(): ThemeChoice {
  try {
    const t = localStorage.getItem(CHOICE_KEY)
    return t === 'light' || t === 'dark' ? t : 'system'
  } catch { return 'system' }
}

/** The choice in effect: this browser's own, else Dark where the default is
 *  dark (which the device's setting does not change), else System. */
export function currentThemeChoice(defaultTheme: 'light' | 'dark'): ThemeChoice {
  const own = themeChoice()
  return own !== 'system' ? own : defaultTheme === 'dark' ? 'dark' : 'system'
}

/** Remember a choice for this browser and have the app paint in it at once. */
export function setThemeChoice(choice: ThemeChoice): void {
  try {
    if (choice === 'system') localStorage.removeItem(CHOICE_KEY)
    else localStorage.setItem(CHOICE_KEY, choice)
  } catch { /* applies to this page only */ }
  window.dispatchEvent(new CustomEvent<ThemeChoice>(CHOICE_EVENT, { detail: choice }))
}

/** Calls `fn` with each choice made through setThemeChoice. */
export function onThemeChoice(fn: (choice: ThemeChoice) => void): () => void {
  const on = (e: Event) => fn((e as CustomEvent<ThemeChoice>).detail)
  window.addEventListener(CHOICE_EVENT, on)
  return () => window.removeEventListener(CHOICE_EVENT, on)
}
