import { Button, Callout, ProgressBar } from '@parallelworks/ui'
import { dismissTransfer, type Transfer, useTransfers } from '../transfers'

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

const VERB = {
  upload: { doing: 'Uploading to', did: 'added to', label: 'Upload progress' },
  move: { doing: 'Moving into', did: 'moved to', label: 'Move progress' },
  copy: { doing: 'Copying into', did: 'copied to', label: 'Copy progress' },
  delete: { doing: 'Deleting', did: 'deleted', label: 'Delete progress' },
}

function title(t: Transfer): string {
  const kind = t.kind ?? 'upload'
  const where = kind === 'delete' ? '' : ` ${t.dir || 'root'}`
  const count = `${t.done} / ${t.total}`
  if (t.phase === 'queued') return `Waiting to upload ${t.total} ${t.total === 1 ? 'item' : 'items'} to${where}`
  if (t.phase === 'done') {
    if (t.cancelled) return `Cancelled: ${t.done} of ${t.total} had already been added to${where}`
    return `${t.done} of ${t.total} ${VERB[kind].did}${where}${t.indexMs ? `, indexed in ${(t.indexMs / 1000).toFixed(1)}s` : ''}`
  }
  if (t.phase === 'indexing') {
    return kind === 'delete' ? 'Updating the index…' : `Indexing ${t.done} ${t.done === 1 ? 'item' : 'items'} in${where}…`
  }
  const sizes = t.totalBytes ? ` (${fmtBytes(t.bytes)} of ${fmtBytes(t.totalBytes)})` : ''
  return `${VERB[kind].doing}${where}: ${count}${sizes}`
}

/** Bytes drive the bar while uploading, so one large file still moves;
 *  indexing has no count to report, so it runs indeterminate. */
function amount(t: Transfer): number | null {
  if (t.phase === 'indexing') return null
  if (t.phase === 'queued') return 0
  return (t.totalBytes ? t.bytes / t.totalBytes : t.done / Math.max(t.total, 1)) * 100
}

export function TransferPanel() {
  const transfers = useTransfers()
  if (!transfers.length) return null
  return (
    <div className="upload-panels">
      {transfers.map(t => {
        const sending = t.phase === 'uploading' || t.phase === 'queued'
        return (
          <div className="upload-panel" key={t.id}>
            <div className="upload-panel-head">
              <span>{title(t)}</span>
              {sending && t.cancel && <Button variant="ghost" size="sm" onClick={t.cancel}>cancel</Button>}
              {t.phase === 'done' && <Button variant="ghost" size="sm" onClick={() => dismissTransfer(t.id)}>dismiss</Button>}
            </div>
            <ProgressBar className="upload-progress" aria-label={VERB[t.kind ?? 'upload'].label} value={amount(t)} />
            {t.phase !== 'done' && t.current && <div className="upload-current">{t.current}</div>}
            {t.indexError && (
              <Callout tone="error" size="sm" className="upload-failed">
                {t.kind === 'upload' ? `Uploaded, but indexing failed, so these files are not searchable yet: ${t.indexError}` : t.indexError}
              </Callout>
            )}
            {t.failed.length > 0 && (
              <Callout tone="error" size="sm" className="upload-failed">
                {t.failed.length} failed: {t.failed.slice(0, 3).map(f => `${f.name} (${f.error})`).join(', ')}{t.failed.length > 3 ? '…' : ''}
              </Callout>
            )}
          </div>
        )
      })}
    </div>
  )
}
