import { useCallback, useEffect, useRef, useState } from 'react'
import { useEffectiveTheme } from '../theme'

/**
 * The iframe a chat reply's /?embed= image becomes, with a control to take
 * it full screen. The case this serves: a simulation campaign writes an
 * interactive HTML artifact into the corpus, the assistant embeds it in
 * the reply, and reading it properly needs more than a 480px box.
 *
 * Two ways out of the box, tried in order. The Fullscreen API is asked
 * first; when the studio itself runs inside the platform's session frame
 * that request is refused unless the platform's iframe grants fullscreen,
 * which is not ours to set, so a refusal falls back to a fixed overlay
 * filling the studio viewport. Same button, best available result.
 *
 * The iframe node must never move or remount across the toggle: the
 * artifact inside is stateful (a running visualization, a filled form) and
 * a remount reloads it from scratch. Both paths therefore only change
 * classes on the wrapper the iframe already lives in.
 */
export function EmbedFrame({ src, title }: { src: string; title: string }) {
  const wrapRef = useRef<HTMLDivElement>(null)
  // The embed is its own document and used to re-derive the theme for
  // itself, which is not guaranteed to reach the same answer as the page
  // hosting it; a dark chat then held a light DAG. The parent knows its
  // theme, so it is passed along rather than re-derived.
  // Read live: a theme toggle changes the src, which reloads the embed in
  // the new theme, the same way a fresh open would.
  const theme = useEffectiveTheme()
  const themed = !src.includes('theme=') ? `${src}&theme=${theme}` : src
  const [mode, setMode] = useState<'inline' | 'native' | 'overlay'>('inline')

  const enter = useCallback(() => {
    const el = wrapRef.current
    if (!el) return
    const req = el.requestFullscreen?.()
    if (req && typeof req.then === 'function') {
      req.then(() => setMode('native')).catch(() => setMode('overlay'))
    } else {
      setMode('overlay')
    }
  }, [])

  const exit = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen()
    setMode('inline')
  }, [])

  // Native fullscreen ends on Esc without asking us; mirror that into
  // state or the button would keep saying "exit" over an inline frame.
  useEffect(() => {
    const onChange = () => {
      if (!document.fullscreenElement) setMode(m => (m === 'native' ? 'inline' : m))
    }
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  // The overlay has no browser-provided Esc, so provide one.
  useEffect(() => {
    if (mode !== 'overlay') return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') exit() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [mode, exit])

  const full = mode !== 'inline'
  return (
    <div ref={wrapRef} className={`chat-embed-wrap${mode === 'overlay' ? ' overlay' : ''}${mode === 'native' ? ' native-full' : ''}`}>
      <iframe src={themed} title={title} className="chat-embed-frame" />
      <button
        type="button"
        className="chat-embed-expand"
        title={full ? 'Exit full screen (Esc)' : 'Full screen'}
        aria-label={full ? 'Exit full screen' : 'Full screen'}
        onClick={full ? exit : enter}
      >
        {full ? (
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
            <path d="M6 2v4H2M10 2v4h4M6 14v-4H2M10 14v-4h4" />
          </svg>
        ) : (
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
            <path d="M2 6V2h4M14 6V2h-4M2 10v4h4M14 10v4h-4" />
          </svg>
        )}
      </button>
    </div>
  )
}
