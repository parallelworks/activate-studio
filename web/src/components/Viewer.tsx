import { ReactElement, Suspense, lazy, useEffect, useRef, useState } from 'react'
import { Streamdown } from 'streamdown'
import { api, FileContent } from '../api'
import { forgetHash } from '../lastLocation'
import { TagMenu } from './TagMenu'
const ModelViewer = lazy(() => import('./ModelViewer').then(m => ({ default: m.ModelViewer })))
import { DataTree } from './DataTree'
import { DagViewer } from './DagViewer'

const OFFICE_SUFFIXES = ['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods']
type Tab = 'preview' | 'indexed' | 'source' | 'workflow'

const JSON_SUFFIXES = ['.json', '.jsonl', '.geojson', '.ipynb']
const YAML_SUFFIXES = ['.yaml', '.yml']

const PDF_PAGE_CAP = 150

/** PDFs and office docs preview as server-rendered page images; the
 *  browser's own PDF viewer is unavailable in the platform's sandboxed
 *  session iframe, so an <iframe src=...pdf> shows nothing there. */
function PdfPages({ path, markdown }: { path: string; markdown: boolean }) {
  const [pages, setPages] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [fallbackText, setFallbackText] = useState<string | null>(null)

  useEffect(() => {
    setPages(null)
    setError(null)
    setFallbackText(null)
    fetch(`/api/kb/pdf-info?path=${encodeURIComponent(path)}`)
      .then(r => (r.ok ? r.json() : r.json().then((d: { error?: string }) => Promise.reject(new Error(d.error ?? `${r.status}`)))))
      .then(d => setPages(d.pages))
      .catch(async e => {
        setError(String((e as Error).message ?? e))
        // A host without LibreOffice cannot render office pages, but the
        // extracted text is already indexed; show that instead of a wall.
        try {
          const f = await fetch(`/api/kb/file?path=${encodeURIComponent(path)}`).then(r => (r.ok ? r.json() : null))
          if (f?.content?.trim()) setFallbackText(f.content)
        } catch { /* no fallback available */ }
      })
  }, [path])

  if (error && fallbackText) {
    return (
      <div className="pad">
        <p className="muted">Page rendering is unavailable here ({error}) so this is the document's extracted text.</p>
        {markdown
          ? <div className="md-body"><Streamdown>{fallbackText}</Streamdown></div>
          : <pre className="extracted-fallback">{fallbackText}</pre>}
      </div>
    )
  }
  if (error) return <p className="error pad">Could not render this document: {error}</p>
  if (pages === null) return <p className="muted pad">Rendering preview…</p>
  const shown = Math.min(pages, PDF_PAGE_CAP)
  return (
    <div className="pdf-pages">
      {Array.from({ length: shown }, (_, i) => (
        <img
          key={i}
          className="pdf-page-img"
          src={`/api/kb/pdf-page?path=${encodeURIComponent(path)}&page=${i + 1}`}
          alt={`page ${i + 1}`}
          loading="lazy"
        />
      ))}
      {pages > shown && (
        <p className="muted pad">Showing the first {shown} of {pages} pages; use Download for the full document.</p>
      )}
    </div>
  )
}

function suffixOf(p: string): string {
  const i = p.lastIndexOf('.')
  return i >= 0 ? p.slice(i).toLowerCase() : ''
}

/**
 * Scroll a range into the middle of whatever scrolls it. scrollIntoView
 * on the range's element would land on the top of a long <pre>, not on
 * the match inside it, so the scroll is computed from the range's own
 * rectangle against the nearest scrolling ancestor.
 */
function scrollRangeIntoView(range: Range): void {
  const rect = range.getBoundingClientRect()
  let el: HTMLElement | null = range.startContainer.parentElement
  while (el) {
    const st = getComputedStyle(el)
    if (/(auto|scroll)/.test(st.overflowY) && el.scrollHeight > el.clientHeight) {
      const box = el.getBoundingClientRect()
      el.scrollTop += rect.top - box.top - el.clientHeight / 2 + rect.height / 2
      return
    }
    el = el.parentElement
  }
  window.scrollBy({ top: rect.top - window.innerHeight / 2 })
}

/** The words a find string is really asking for: quotes and fts5 operators dropped. */
export function findTerms(text: string): string[] {
  return text.split(/\s+/)
    .map(t => t.replace(/^["'(]+|["')]+$/g, ''))
    .filter(t => t.length >= 2 && !/^(AND|OR|NOT|NEAR)$/i.test(t))
}

export function Viewer({ path, find, onDeleted }: {
  path: string | null
  /** Text to land on and highlight, typically the search query that led here. */
  find?: string
  onDeleted: (path: string) => void
}) {
  const [file, setFile] = useState<FileContent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('preview')
  const [confirming, setConfirming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [tags, setTags] = useState<{ own: string[]; inherited: string[] }>({ own: [], inherited: [] })
  const [labelsOpen, setLabelsOpen] = useState(false)
  // Find in this file. Matches are painted with the CSS Custom Highlight
  // API rather than by wrapping text in <mark>, because the rendered
  // Markdown and the text bodies are React's DOM and rewriting their text
  // nodes underneath React breaks the next update.
  const bodyRef = useRef<HTMLDivElement>(null)
  const [findText, setFindText] = useState('')
  const [findOpen, setFindOpen] = useState(false)
  const [hitCount, setHitCount] = useState(0)
  const [hitIdx, setHitIdx] = useState(0)

  // Opened from a search result: seed the find bar with the query and
  // land on the tab that actually holds the matched text. A PDF or office
  // file previews as page images, which cannot be searched, so those open
  // on the indexed text, which is exactly what the search matched.
  useEffect(() => {
    if (!file || !path || file.path !== path) return
    setFindText(find ?? '')
    setFindOpen(!!find)
    setHitIdx(0)
    if (find) {
      const sfx = suffixOf(file.path)
      const paged = sfx === '.pdf' || OFFICE_SUFFIXES.includes(sfx) || file.kind === 'image'
      if (paged && file.content != null) setTab('indexed')
    }
  }, [find, path, file?.path])

  useEffect(() => {
    const root = bodyRef.current
    const H = (globalThis as unknown as { Highlight?: new (...r: Range[]) => unknown }).Highlight
    const reg = (globalThis as unknown as { CSS?: { highlights?: Map<string, unknown> } }).CSS?.highlights
    if (!root || !H || !reg) return
    reg.delete('find-hit'); reg.delete('find-current')
    const terms = findTerms(findText)
    if (!terms.length) { setHitCount(0); return }
    const re = new RegExp(terms.map(t => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'), 'gi')
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    const ranges: Range[] = []
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const text = (n as Text).data
      re.lastIndex = 0
      let m: RegExpExecArray | null
      while ((m = re.exec(text))) {
        const r = document.createRange()
        r.setStart(n, m.index); r.setEnd(n, m.index + m[0].length)
        ranges.push(r)
        if (!m[0].length) re.lastIndex++
      }
    }
    setHitCount(ranges.length)
    if (!ranges.length) return
    const i = Math.min(hitIdx, ranges.length - 1)
    reg.set('find-hit', new H(...ranges))
    reg.set('find-current', new H(ranges[i]))
    scrollRangeIntoView(ranges[i])
    return () => { reg.delete('find-hit'); reg.delete('find-current') }
  }, [findText, hitIdx, tab, findOpen, file?.content])

  useEffect(() => {
    if (!path) return
    const load = () => api.tagsFor(path).then(setTags).catch(() => setTags({ own: [], inherited: [] }))
    load()
    window.addEventListener('ade-tags-changed', load)
    return () => window.removeEventListener('ade-tags-changed', load)
  }, [path])

  useEffect(() => {
    setFile(null)
    setError(null)
    setConfirming(false)
    if (!path) return
    api.file(path).then(f => {
      setFile(f)
      // Text files ARE their own preview; office docs open on the rendered
      // original; anything else opens on whatever it has.
      setTab('preview')
    }).catch(e => {
      setError(String(e))
      // A file that cannot be opened should not keep reopening: drop it from
      // the remembered location so the next visit lands on the library.
      forgetHash()
      if (location.hash.startsWith('#open=')) {
        history.replaceState(null, '', location.pathname + location.search + '#view=library')
      }
    })
  }, [path])

  if (!path) {
    return (
      <div className="viewer empty">
        <div className="empty-state">
          <h2>Nothing open</h2>
          <p className="muted">Pick a file from the tree, drop new material on the Add button, or find something in Search.</p>
        </div>
      </div>
    )
  }
  if (error) {
    return (
      <div className="viewer">
        <div className="empty-state">
          <h2>Cannot open this file</h2>
          <p className="muted">{path}</p>
          <p className="error">{error}</p>
          <p className="muted">It may have been moved, renamed, or deleted since this link was made.</p>
        </div>
      </div>
    )
  }
  if (!file) return <div className="viewer"><p className="muted">Loading {path}…</p></div>

  const suffix = suffixOf(path)
  const isMarkdown = suffix === '.md'
  const isHtml = /\.html?$/i.test(path)
  const isImage = file.kind === 'image'
  const isModel = file.kind === 'model'
  const isPdf = suffix === '.pdf'
  const isOffice = OFFICE_SUFFIXES.includes(suffix)
  const isText = file.kind === 'text'
  const isJson = JSON_SUFFIXES.includes(suffix)
  const isYaml = YAML_SUFFIXES.includes(suffix)
  const isStructured = (isJson || isYaml) && isText
  // A YAML file with a jobs map is a platform workflow, and the DAG is the
  // reading of it people actually want. The heuristic only decides whether
  // the tab appears; the server's parse is the authority on the content.
  const isWorkflowYaml = isYaml && isText && /^jobs:/m.test(file?.content ?? '')
  // Whether extracted text is Markdown is the server's to know: the same
  // PDF is downmark Markdown on a host with the native binary and
  // pdftotext's column-aligned text without it, and rendering that as
  // Markdown turns its columns into code blocks and reflows its rows.
  const extractedMarkdown = file.source === 'extracted' && file.format === 'markdown'

  const doDelete = async () => {
    setDeleting(true)
    try {
      await api.deleteFile(path)
      onDeleted(path)
    } catch (e) {
      setError(String(e))
      setDeleting(false)
    }
  }

  const indexedView = file.content == null
    ? <p className="muted pad">No text is stored in the index for this file{isImage ? ' yet; the next index pass adds OCR text and a description' : ''}.</p>
    : (
      <div className="viewer-body">
        <p className="index-note">This is the text representation stored in the search index, not the original file.</p>
        {isMarkdown || extractedMarkdown
          ? <div className="md-body"><Streamdown>{file.content}</Streamdown></div>
          : <pre className="text-body">{file.content}</pre>}
        {file.truncated && <p className="muted">Preview truncated; use Download for the full file.</p>}
      </div>
    )

  const sourceView = (
    <div className="viewer-body">
      <pre className="text-body">{file.content ?? ''}</pre>
      {file.truncated && <p className="muted">Truncated; use Download for the full file.</p>}
    </div>
  )

  const treeView = <DataTree text={file.content ?? ''} format={isYaml ? 'yaml' : 'json'} />

  const previewView = isStructured ? treeView : isHtml ? (
    <iframe
      sandbox="allow-scripts"
      src={`/api/kb/html?path=${encodeURIComponent(path)}`}
      title={path}
      className="html-preview-frame"
    />
  ) : isModel ? (
    <Suspense fallback={<p className="muted pad">Loading the 3D viewer…</p>}><ModelViewer path={path} /></Suspense>
  ) : isImage ? (
    <div className="viewer-body"><img src={api.rawUrl(path)} alt={path} style={{ maxWidth: '100%' }} /></div>
  ) : isPdf || isOffice ? (
    <PdfPages path={path} markdown={extractedMarkdown} />
  ) : isText ? (
    <div className="viewer-body">
      {isMarkdown
        ? <div className="md-body"><Streamdown>{file.content ?? ''}</Streamdown></div>
        : <pre className="text-body">{file.content ?? ''}</pre>}
      {file.truncated && <p className="muted">Preview truncated; use Download for the full file.</p>}
    </div>
  ) : (
    <p className="muted pad">No inline preview for this file type; use Download.</p>
  )

  // A file's tabs are the representations it actually has: a rendered
  // preview where one exists, the bytes as written, and the text the
  // index holds for formats whose text is extracted rather than read.
  const previewLabel = isStructured ? 'Tree' : isMarkdown ? 'Rendered' : isHtml ? 'Page' : 'Preview'
  const views: { key: Tab; label: string; node: ReactElement }[] = []
  if (isWorkflowYaml) {
    // First, so a workflow file opens on its DAG rather than on the tree.
    views.push({ key: 'workflow', label: 'Workflow', node: <div className="workflow-tab-body"><DagViewer path={path} /></div> })
  }
  const hasRendered = isStructured || isMarkdown || isHtml || isImage || isPdf || isOffice || isModel
  if (hasRendered) views.push({ key: 'preview', label: previewLabel, node: previewView })
  if (isText) views.push({ key: 'source', label: hasRendered ? 'Source' : 'Text', node: sourceView })
  if (!isText && (isImage || isPdf || isOffice || isModel)) {
    views.push({ key: 'indexed', label: 'Indexed text', node: indexedView })
  }
  const active = views.some(v => v.key === tab) ? tab : (views[0]?.key ?? 'preview')

  return (
    <div className="viewer">
      <div className="viewer-head">
        <span className="viewer-path">
          {file.path}
          {(tags.own.length > 0 || tags.inherited.length > 0) && (
            <span className="viewer-tags">
              {tags.own.map(t => <span key={t} className="tag-pill on small">{t}</span>)}
              {tags.inherited.map(t => <span key={t} className="tag-pill inherited small" title="inherited from a parent directory">{t}</span>)}
            </span>
          )}
        </span>
        <span className="viewer-meta">
          <span>{new Date(file.mtime * 1000).toISOString().slice(0, 10)}</span>
          <span className="upload-menu">
            <button className="btn-secondary" onClick={() => setFindOpen(o => !o)} title="Find text in this file">Find</button>
            <button className="btn-secondary" onClick={() => setLabelsOpen(o => !o)}>Labels</button>
            {labelsOpen && (
              <TagMenu
                paths={[file.path]}
                onClose={() => setLabelsOpen(false)}
                onApplied={() => window.dispatchEvent(new Event('ade-tags-changed'))}
              />
            )}
          </span>
          <a className="btn-primary" href={api.downloadUrl(file.path)}>Download</a>
          {confirming ? (
            <span className="confirm-group">
              <button className="btn-danger" onClick={doDelete} disabled={deleting}>
                {deleting ? 'Deleting…' : 'Confirm delete'}
              </button>
              <button className="btn-secondary" onClick={() => setConfirming(false)} disabled={deleting}>Cancel</button>
            </span>
          ) : (
            <button className="btn-danger-outline" onClick={() => setConfirming(true)}>Delete</button>
          )}
        </span>
      </div>
      {views.length > 0 && (
        <div className="viewer-tabs">
          {views.map(v => (
            <button key={v.key} className={active === v.key ? 'active' : ''} onClick={() => setTab(v.key)}>{v.label}</button>
          ))}
        </div>
      )}
      {(findOpen || findText) && (
        <div className="find-bar">
          <input
            value={findText}
            autoFocus
            placeholder="Find in this file"
            onChange={e => { setFindText(e.target.value); setHitIdx(0) }}
            onKeyDown={e => {
              if (e.key === 'Enter' && hitCount) setHitIdx(i => e.shiftKey ? (i - 1 + hitCount) % hitCount : (i + 1) % hitCount)
              if (e.key === 'Escape') { setFindOpen(false); setFindText('') }
            }}
          />
          <span className="find-count muted">
            {hitCount
              ? `${Math.min(hitIdx, hitCount - 1) + 1} of ${hitCount}`
              : (isPdf || isOffice || isImage) && active === 'preview' && file.content != null
                ? <>Pages are images; <button className="link-button" onClick={() => setTab('indexed')}>show matches in the indexed text</button></>
                : findText ? 'No matches' : ''}
          </span>
          <button className="btn-secondary" disabled={!hitCount} onClick={() => setHitIdx(i => (i - 1 + hitCount) % hitCount)}>Previous</button>
          <button className="btn-secondary" disabled={!hitCount} onClick={() => setHitIdx(i => (i + 1) % hitCount)}>Next</button>
          <button className="btn-secondary" onClick={() => { setFindOpen(false); setFindText('') }}>Close</button>
        </div>
      )}
      <div ref={bodyRef} className="viewer-find-scope">
        {views.find(v => v.key === active)?.node ?? previewView}
      </div>
    </div>
  )
}
