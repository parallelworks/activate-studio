import { ReactElement, Suspense, lazy, useEffect, useRef, useState } from 'react'
import { Button, Callout, ConfirmModal, EmptyState, Loader, TabPanel, Tabs, Tooltip } from '@parallelworks/ui'
import { Streamdown } from 'streamdown'
import { api, withLibrary, FileContent, type OfficeBlock, type OfficeOutline } from '../api'
import { forgetHash } from '../lastLocation'
import { TagMenu } from './TagMenu'
const ModelViewer = lazy(() => import('./ModelViewer').then(m => ({ default: m.ModelViewer })))
import { DataTree } from './DataTree'
import { DagViewer } from './DagViewer'

const OFFICE_SUFFIXES = ['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods']
/** Office files the server reads without LibreOffice (officePreview.ts). */
const OUTLINE_SUFFIXES = ['.pptx', '.docx', '.xlsx', '.odp', '.odt', '.ods']
const SLIDE_SUFFIXES = ['.pptx', '.ppt', '.odp']
const SHEET_SUFFIXES = ['.xlsx', '.xls', '.ods']
type Tab = 'preview' | 'indexed' | 'source' | 'workflow'

const JSON_SUFFIXES = ['.json', '.jsonl', '.geojson', '.ipynb']
const YAML_SUFFIXES = ['.yaml', '.yml']

const PDF_PAGE_CAP = 150

/** What an Office file's Preview tab is called: what it shows. */
function officeLabel(suffix: string): string {
  return SLIDE_SUFFIXES.includes(suffix) ? 'Slides' : SHEET_SUFFIXES.includes(suffix) ? 'Sheets' : 'Document'
}

/**
 * PDFs and Office files preview as server-rendered page images; the
 * browser's own PDF viewer is unavailable in the platform's sandboxed
 * session iframe, so an <iframe src=...pdf> shows nothing there. Page images
 * need LibreOffice (for Office files) and poppler; where they cannot be
 * made, an Office file shows its outline read from the file, and a PDF its
 * extracted text. A page image that fails to load is treated the same way,
 * so the tab never shows broken images. `onUnavailable` says there is
 * nothing to preview at all, and the viewer opens the indexed text instead.
 */
function PagedPreview({ path, office, markdown, onUnavailable }: {
  path: string
  office: boolean
  markdown: boolean
  onUnavailable: (why: string) => void
}) {
  const [pages, setPages] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [stage, setStage] = useState<'pages' | 'outline' | 'text'>('pages')
  const [fallbackText, setFallbackText] = useState<string | null>(null)
  const outline = OUTLINE_SUFFIXES.includes(suffixOf(path))
  // Several page images can fail at once; the first one decides.
  const failed = useRef(false)

  useEffect(() => {
    failed.current = false
    setPages(null)
    setError(null)
    setStage('pages')
    setFallbackText(null)
    fetch(withLibrary(`/api/kb/pdf-info?path=${encodeURIComponent(path)}`))
      .then(r => (r.ok ? r.json() : r.json().then((d: { error?: string }) => Promise.reject(new Error(d.error ?? `${r.status}`)))))
      .then(d => setPages(d.pages))
      .catch(e => fail(String((e as Error).message ?? e)))
  }, [path])

  // Page images cannot be had: the outline for an Office file the server
  // reads, else the extracted text (a PDF), else nothing to preview.
  const fail = (why: string) => {
    if (failed.current) return
    failed.current = true
    setError(why)
    if (office) {
      if (outline) setStage('outline')
      else onUnavailable(why)
      return
    }
    setStage('text')
    fetch(withLibrary(`/api/kb/file?path=${encodeURIComponent(path)}`)).then(r => (r.ok ? r.json() : null))
      .then(f => { if (f?.content?.trim()) setFallbackText(f.content); else onUnavailable(why) })
      .catch(() => onUnavailable(why))
  }

  if (stage === 'outline') return <OfficeOutlineView path={path} onFail={why => onUnavailable(why || error || '')} />
  if (stage === 'text') {
    if (fallbackText == null) return <div className="muted pad"><Loader inline size={16} label="Reading the text…" /></div>
    return (
      <div className="pad">
        <p className="muted">Page images cannot be made here ({error}), so this is the document's extracted text.</p>
        {markdown
          ? <div className="md-body"><Streamdown>{fallbackText}</Streamdown></div>
          : <pre className="extracted-fallback">{fallbackText}</pre>}
      </div>
    )
  }
  if (pages === null) return <div className="muted pad"><Loader inline size={16} label="Rendering preview…" /></div>
  const shown = Math.min(pages, PDF_PAGE_CAP)
  return (
    <div className="pdf-pages">
      {Array.from({ length: shown }, (_, i) => (
        <img
          key={i}
          className="pdf-page-img"
          src={api.pdfPageUrl(path, i + 1)}
          alt={`page ${i + 1}`}
          loading="lazy"
          onError={() => fail('a page could not be rendered')}
        />
      ))}
      {pages > shown && (
        <p className="muted pad">Showing the first {shown} of {pages} pages; use Download for the full document.</p>
      )}
    </div>
  )
}

function OfficeBlocks({ path, blocks }: { path: string; blocks: OfficeBlock[] }) {
  return (
    <>
      {blocks.map((b, i) => {
        if (b.t === 'h') {
          const H = (`h${Math.min(6, b.level + 2)}`) as 'h3'
          return <H key={i} className="office-h">{b.text}</H>
        }
        if (b.t === 'p') {
          return (
            <p key={i} className={b.list ? 'office-p office-li' : 'office-p'} style={b.level ? { marginLeft: `${b.level * 1.25}rem` } : undefined}>
              {b.text}
            </p>
          )
        }
        if (b.t === 'table') return <OfficeTable key={i} rows={b.rows} />
        return <img key={i} className="office-img" src={api.officeMediaUrl(path, b.media)} alt="" loading="lazy" onError={e => { e.currentTarget.hidden = true }} />
      })}
    </>
  )
}

function OfficeTable({ rows }: { rows: string[][] }) {
  if (!rows.length) return null
  return (
    <div className="office-table-wrap">
      <table className="office-table">
        <tbody>
          {rows.map((r, i) => <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>)}
        </tbody>
      </table>
    </div>
  )
}

/** An Office file's slides, paragraphs, tables and sheets, read from the
 *  file itself where LibreOffice is not there to render its pages. */
function OfficeOutlineView({ path, onFail }: { path: string; onFail: (why: string) => void }) {
  const [outline, setOutline] = useState<OfficeOutline | null>(null)
  useEffect(() => {
    setOutline(null)
    api.officePreview(path)
      .then(o => {
        const empty = o.kind === 'slides' ? !o.slides.length : o.kind === 'sheets' ? !o.sheets.length : !o.blocks.length
        if (empty) onFail('')
        else setOutline(o)
      })
      .catch(e => onFail(String((e as Error).message ?? e)))
  }, [path])
  if (!outline) return <div className="muted pad"><Loader inline size={16} label="Reading the file…" /></div>
  return (
    <div className="viewer-body office-outline">
      <p className="muted office-note">The text and pictures in the file, without its layout. A rendering of its pages needs LibreOffice.</p>
      {outline.kind === 'slides' && outline.slides.map(sl => (
        <section key={sl.n} className="office-slide" aria-label={`Slide ${sl.n}`}>
          <div className="office-slide-n">Slide {sl.n}</div>
          {sl.title && <h3 className="office-slide-title">{sl.title}</h3>}
          <OfficeBlocks path={path} blocks={sl.blocks} />
        </section>
      ))}
      {outline.kind === 'document' && <OfficeBlocks path={path} blocks={outline.blocks} />}
      {outline.kind === 'sheets' && outline.sheets.map(sh => (
        <section key={sh.name} className="office-sheet">
          <h3 className="office-h">{sh.name}</h3>
          <OfficeTable rows={sh.rows} />
          {sh.truncated && <p className="muted">The first 200 rows and 30 columns; use Download for the whole sheet.</p>}
        </section>
      ))}
      {outline.truncated && <p className="muted">Part of the file is shown; use Download for all of it.</p>}
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

export function Viewer({ path, find, onDeleted, readOnly }: {
  path: string | null
  /** Text to land on and highlight, typically the search query that led here. */
  find?: string
  onDeleted: (path: string) => void
  /** Why the file cannot be deleted here, for a file in a read-only
   *  library; Delete is then disabled and says so. */
  readOnly?: string | null
}) {
  const [file, setFile] = useState<FileContent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('preview')
  // Set when the file has no preview to show (no page images and nothing
  // read from it): the Preview tab goes and the indexed text says why.
  const [previewGone, setPreviewGone] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [tags, setTags] = useState<{ own: string[]; inherited: string[] }>({ own: [], inherited: [] })
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
    setPreviewGone(null)
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
        <EmptyState
          variant="bare"
          title="Nothing open"
          description="Pick a file from the tree, drop new material on the Add button, or find something in Search."
        />
      </div>
    )
  }
  if (error) {
    return (
      <div className="viewer">
        <Callout tone="error" title="Cannot open this file" className="viewer-callout">
          <p className="viewer-callout-path">{path}</p>
          <p>{error}</p>
          <p>It may have been moved, renamed, or deleted since this link was made.</p>
        </Callout>
      </div>
    )
  }
  if (!file) return <div className="viewer"><div className="muted pad"><Loader inline size={16} label={`Loading ${path}…`} /></div></div>

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

  // A failed delete replaces the view with the error, as a failed open does.
  const doDelete = async () => {
    try {
      await api.deleteFile(path)
      onDeleted(path)
    } catch (e) {
      setError(String(e))
    }
  }

  const indexedView = file.content == null
    ? <p className="muted pad">No text is stored in the index for this file{isImage ? ' yet; the next index pass adds OCR text and a description' : ''}.</p>
    : (
      <div className="viewer-body">
        {previewGone !== null && isOffice && (
          <p className="muted preview-gone-note">A preview of this file needs LibreOffice, so this is its indexed text.</p>
        )}
        <Callout tone="warning" size="sm" className="index-note">This is the text representation stored in the search index, not the original file.</Callout>
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
    <Suspense fallback={<div className="muted pad"><Loader inline size={16} label="Loading the 3D viewer…" /></div>}><ModelViewer path={path} /></Suspense>
  ) : isImage ? (
    <div className="viewer-body"><img src={api.rawUrl(path)} alt={path} style={{ maxWidth: '100%' }} /></div>
  ) : isPdf || isOffice ? (
    <PagedPreview path={path} office={isOffice} markdown={extractedMarkdown} onUnavailable={why => { setPreviewGone(why); setTab('indexed') }} />
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
  const previewLabel = isStructured ? 'Tree' : isMarkdown ? 'Rendered' : isHtml ? 'Page' : isPdf ? 'Pages' : isOffice ? officeLabel(suffix) : 'Preview'
  const views: { key: Tab; label: string; node: ReactElement }[] = []
  if (isWorkflowYaml) {
    // First, so a workflow file opens on its DAG rather than on the tree.
    views.push({ key: 'workflow', label: 'Workflow', node: <div className="workflow-tab-body"><DagViewer path={path} /></div> })
  }
  const hasRendered = isStructured || isMarkdown || isHtml || isImage || ((isPdf || isOffice) && previewGone === null) || isModel
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
          <Button variant="secondary" size="sm" onClick={() => setFindOpen(o => !o)} title="Find text in this file">Find</Button>
          <TagMenu
            paths={[file.path]}
            trigger={<Button variant="secondary" size="sm">Labels</Button>}
            align="end"
            onApplied={() => window.dispatchEvent(new Event('ade-tags-changed'))}
          />
          <Button variant="primary" size="sm" href={api.downloadUrl(file.path)} reloadDocument>Download</Button>
          {readOnly
            ? (
              <Tooltip content={readOnly}>
                <Button variant="outline" size="sm" disabled focusableWhenDisabled aria-description={readOnly}>Delete</Button>
              </Tooltip>
            )
            : <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>Delete</Button>}
          <ConfirmModal
            open={confirming}
            onClose={() => setConfirming(false)}
            title="Delete this file?"
            description={file.path}
            confirmLabel="Delete"
            destructive
            onConfirm={doDelete}
          />
        </span>
      </div>
      {/* The find bar and the body sit inside the tabs, so the shown view is
          the tab's panel. A file with one view shows no tab row. */}
      <Tabs
        label="File views"
        items={views.map(v => ({ value: v.key, label: v.label }))}
        value={active}
        onValueChange={setTab}
        className="file-tabs"
      >
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
                  ? <>Pages are images; <Button variant="ghost" size="sm" onClick={() => setTab('indexed')}>show matches in the indexed text</Button></>
                  : findText ? 'No matches' : ''}
            </span>
            <Button variant="secondary" size="sm" disabled={!hitCount} onClick={() => setHitIdx(i => (i - 1 + hitCount) % hitCount)}>Previous</Button>
            <Button variant="secondary" size="sm" disabled={!hitCount} onClick={() => setHitIdx(i => (i + 1) % hitCount)}>Next</Button>
            <Button variant="secondary" size="sm" onClick={() => { setFindOpen(false); setFindText('') }}>Close</Button>
          </div>
        )}
        <div ref={bodyRef} className="viewer-find-scope">
          <TabPanel value={active}>{views.find(v => v.key === active)?.node ?? previewView}</TabPanel>
        </div>
      </Tabs>
    </div>
  )
}
