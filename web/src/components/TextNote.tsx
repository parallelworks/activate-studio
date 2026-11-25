import { textTitle, useTransfers } from '../transfers'

/** What became of the open file's last save, as an upload's row says it:
 *  its library's index catching up, then done. */
export function TextNote({ path }: { path: string }) {
  const t = useTransfers().find(x => x.kind === 'text' && x.dir === path)
  if (!t) return null
  return <p className={`text-note${t.indexError ? ' failed' : ''}`} role="status">{textTitle(t)}</p>
}
