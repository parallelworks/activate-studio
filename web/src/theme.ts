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
