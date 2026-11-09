import { useEffect, useRef, useState } from 'react'
import { Loader } from '@parallelworks/ui'

/** Where the build puts pdf.js's fonts, maps and decoders (vite.config). */
const PDFJS_ASSETS = new URL('pdfjs/', document.baseURI).href

/**
 * A PDF's pages drawn here in the page by pdf.js, where the server cannot
 * draw them (no poppler on this computer, as in the macOS app). The file
 * comes from `url` (the raw file, fetched from another system for a
 * library there); each page is drawn when it scrolls near, at the width
 * of the column, up to `cap` pages. `onFail` says the file could not be
 * read or drawn, and the viewer shows its text instead.
 */
export function PdfJsPages({ url, cap, onFail }: { url: string; cap: number; onFail: (why: string) => void }) {
  const [pages, setPages] = useState<number | null>(null)
  const doc = useRef<import('pdfjs-dist').PDFDocumentProxy | null>(null)
  const failed = useRef(false)
  const fail = (why: string) => { if (!failed.current) { failed.current = true; onFail(why) } }

  useEffect(() => {
    let alive = true
    failed.current = false
    setPages(null)
    let task: import('pdfjs-dist').PDFDocumentLoadingTask | null = null
    void (async () => {
      try {
        const pdfjs = await import('pdfjs-dist/legacy/build/pdf.mjs')
        pdfjs.GlobalWorkerOptions.workerSrc = (await import('pdfjs-dist/legacy/build/pdf.worker.min.mjs?url')).default
        task = pdfjs.getDocument({
          url, useSystemFonts: true, enableXfa: false,
          cMapUrl: `${PDFJS_ASSETS}cmaps/`, standardFontDataUrl: `${PDFJS_ASSETS}standard_fonts/`,
          iccUrl: `${PDFJS_ASSETS}iccs/`, wasmUrl: `${PDFJS_ASSETS}wasm/`,
        })
        const d = await task.promise
        if (!alive) return
        doc.current = d
        setPages(d.numPages)
      } catch (e) {
        if (alive) fail(`the PDF could not be read here (${String((e as Error)?.message ?? e)})`)
      }
    })()
    return () => {
      alive = false
      void task?.destroy()
      doc.current = null
    }
  }, [url])

  if (pages === null) return <div className="muted pad"><Loader inline size={16} label="Rendering preview…" /></div>
  const shown = Math.min(pages, cap)
  return (
    <div className="pdf-pages">
      {Array.from({ length: shown }, (_, i) => <PdfJsPage key={`${url}#${i}`} doc={doc} n={i + 1} onFail={fail} />)}
      {pages > shown && (
        <p className="muted pad">Showing the first {shown} of {pages} pages; use Download for the full document.</p>
      )}
    </div>
  )
}

/** One page: a placeholder until it comes near the view, then a canvas. */
function PdfJsPage({ doc, n, onFail }: {
  doc: React.MutableRefObject<import('pdfjs-dist').PDFDocumentProxy | null>
  n: number
  onFail: (why: string) => void
}) {
  const box = useRef<HTMLDivElement>(null)
  const canvas = useRef<HTMLCanvasElement>(null)
  const [near, setNear] = useState(n <= 2)
  const [aspect, setAspect] = useState(1.294)

  useEffect(() => {
    if (near || !box.current) return
    if (typeof IntersectionObserver === 'undefined') { setNear(true); return }
    const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) setNear(true) }, { rootMargin: '800px 0px' })
    io.observe(box.current)
    return () => io.disconnect()
  }, [near])

  useEffect(() => {
    if (!near) return
    let alive = true
    let render: import('pdfjs-dist').RenderTask | null = null
    void (async () => {
      const d = doc.current
      const c = canvas.current
      if (!d || !c) return
      try {
        const page = await d.getPage(n)
        if (!alive) return
        const base = page.getViewport({ scale: 1 })
        setAspect(base.height / base.width)
        const width = Math.max(320, box.current?.clientWidth ?? 800)
        const ratio = Math.min(window.devicePixelRatio || 1, 2)
        const vp = page.getViewport({ scale: (width / base.width) * ratio })
        c.width = Math.floor(vp.width)
        c.height = Math.floor(vp.height)
        render = page.render({ canvas: c, viewport: vp })
        await render.promise
      } catch (e) {
        const name = (e as Error)?.name
        if (alive && name !== 'RenderingCancelledException') onFail('a page could not be rendered')
      }
    })()
    return () => { alive = false; render?.cancel() }
  }, [near, n])

  return (
    <div ref={box} className="pdf-page-js" style={{ aspectRatio: `1 / ${aspect}` }}>
      {near && <canvas ref={canvas} className="pdf-page-img" aria-label={`page ${n}`} role="img" />}
    </div>
  )
}
