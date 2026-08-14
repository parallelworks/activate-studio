import { useEffect, useState } from 'react'
import { Streamdown } from 'streamdown'
import { api, FileContent } from '../api'
import { TagMenu } from './TagMenu'
import { ModelViewer } from './ModelViewer'

const OFFICE_SUFFIXES = ['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods']

type Tab = 'preview' | 'indexed'

function suffixOf(p: string): string {
  const i = p.lastIndexOf('.')
  return i >= 0 ? p.slice(i).toLowerCase() : ''
}

export function Viewer({ path, onDeleted }: {
  path: string | null
  onDeleted: (path: string) => void
}) {
  const [file, setFile] = useState<FileContent | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('preview')
  const [confirming, setConfirming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [tags, setTags] = useState<{ own: string[]; inherited: string[] }>({ own: [], inherited: [] })
  const [labelsOpen, setLabelsOpen] = useState(false)

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
    }).catch(e => setError(String(e)))
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
  if (error) return <div className="viewer"><p className="error">{error}</p></div>
  if (!file) return <div className="viewer"><p className="muted">Loading {path}…</p></div>

  const suffix = suffixOf(path)
  const isMarkdown = suffix === '.md'
  const isImage = file.kind === 'image'
  const isModel = file.kind === 'model'
  const isPdf = suffix === '.pdf'
  const isOffice = OFFICE_SUFFIXES.includes(suffix)
  const isText = file.kind === 'text'
  // Text files have one representation; binary formats have an original
  // rendering and, separately, the text stored in the search index.
  const hasTabs = !isText && (isImage || isPdf || isOffice || isModel)

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
        {isMarkdown
          ? <div className="md-body"><Streamdown>{file.content}</Streamdown></div>
          : <pre className="text-body">{file.content}</pre>}
        {file.truncated && <p className="muted">Preview truncated; use Download for the full file.</p>}
      </div>
    )

  const previewView = isModel ? (
    <ModelViewer path={path} />
  ) : isImage ? (
    <div className="viewer-body"><img src={api.rawUrl(path)} alt={path} style={{ maxWidth: '100%' }} /></div>
  ) : isPdf ? (
    <iframe className="pdf-frame" src={api.rawUrl(path)} title={path} />
  ) : isOffice ? (
    <iframe className="pdf-frame" src={api.pdfUrl(path)} title={path} />
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
      {hasTabs && (
        <div className="viewer-tabs">
          <button className={tab === 'preview' ? 'active' : ''} onClick={() => setTab('preview')}>Preview</button>
          <button className={tab === 'indexed' ? 'active' : ''} onClick={() => setTab('indexed')}>Indexed text</button>
        </div>
      )}
      {hasTabs ? (tab === 'preview' ? previewView : indexedView) : previewView}
    </div>
  )
}
