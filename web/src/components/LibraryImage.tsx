import { useEffect, useState, type ComponentProps } from 'react'

/** Whether an image's source is a file of the library, drawn by the server. */
export function isLibrarySource(src: unknown): src is string {
  return typeof src === 'string' && src.startsWith('/api/kb/')
}

/** What to say for a library file the server would not send. */
export function goneNote(status: number): string {
  if (status === 404) return 'This file is no longer in the library.'
  if (status === 416) return 'This page is no longer in the file.'
  return 'This file could not be shown.'
}

/**
 * A picture of a library file in a chat reply (an image, or a page of a
 * PDF or Office file). A reply outlives the file it shows: once the file
 * is moved, renamed or deleted the picture cannot load, and a short note
 * stands in its place. The server is asked why once, and not again.
 */
export function LibraryImage(props: ComponentProps<'img'>) {
  // Kept with the source it is about: a reply still being written changes
  // its picture's source under the same element.
  const [failed, setFailed] = useState<{ src: string; note: string } | null>(null)
  const src = String(props.src ?? '')
  if (failed?.src === src) return <span className="library-image-gone muted">{failed.note}</span>
  return (
    <img
      {...props}
      onError={e => {
        props.onError?.(e)
        setFailed({ src, note: '' })
        const say = (status: number) => setFailed(f => (f?.src === src ? { src, note: goneNote(status) } : f))
        fetch(src).then(r => say(r.ok ? 0 : r.status)).catch(() => say(0))
      }}
    />
  )
}

/**
 * A library HTML page in a frame of its own (the chat's html embed). The
 * frame cannot say that its page did not load, so the page is asked for
 * first; one that is gone gets the same note as a picture.
 */
export function LibraryPageFrame({ src }: { src: string }) {
  const [state, setState] = useState<{ src: string; note: string } | null>(null)
  useEffect(() => {
    let alive = true
    const say = (note: string) => { if (alive) setState({ src, note }) }
    fetch(src, { method: 'HEAD' }).then(r => say(r.ok ? '' : goneNote(r.status))).catch(() => say(''))
    return () => { alive = false }
  }, [src])
  if (state?.src !== src) return null
  if (state.note) return <p className="library-image-gone muted pad">{state.note}</p>
  return <iframe sandbox="allow-scripts allow-modals" src={src} title="sandboxed page" style={{ width: '100%', height: '100%', border: 0 }} />
}
