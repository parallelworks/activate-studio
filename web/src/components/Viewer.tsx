import { useEffect, useState } from 'react'
import { Streamdown } from 'streamdown'
import { api, FileContent } from '../api'

export function Viewer({ path }: { path: string | null }) {
  const [file, setFile] = useState<FileContent | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setFile(null)
    setError(null)
    if (!path) return
    api.file(path).then(setFile).catch(e => setError(String(e)))
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

  const isMarkdown = path.toLowerCase().endsWith('.md')
  const isImage = file.kind === 'image'
  return (
    <div className="viewer">
      <div className="viewer-head">
        <span className="viewer-path">{file.path}</span>
        <span className="viewer-meta">
          {file.source === 'extracted' && <span className="pill pill-name">extracted text</span>}
          <span>{new Date(file.mtime * 1000).toISOString().slice(0, 10)}</span>
          <a className="btn-primary" href={api.downloadUrl(file.path)}>Download</a>
        </span>
      </div>
      <div className="viewer-body">
        {isImage ? (
          <img src={api.downloadUrl(file.path)} alt={file.path} style={{ maxWidth: '100%' }} />
        ) : file.content == null ? (
          <p className="muted">No text preview for this file type.</p>
        ) : isMarkdown ? (
          <div className="md-body"><Streamdown>{file.content}</Streamdown></div>
        ) : (
          <pre className="text-body">{file.content}</pre>
        )}
        {file.truncated && <p className="muted">Preview truncated; use Download for the full file.</p>}
      </div>
    </div>
  )
}
