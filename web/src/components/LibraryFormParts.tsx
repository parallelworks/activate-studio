import { ConfirmModal, DescriptionList, DescriptionListItem, StatusDot } from '@parallelworks/ui'

/** The parts the Add a library forms share: the steps of a check, and the
 *  confirmation before folders are made. */

export interface CheckStep { name: string; ok: boolean; detail: string; warn?: boolean }

export const STEP_LABEL: Record<string, string> = {
  ssh: 'Connection', gufi: 'GUFI', index: 'Index', source: 'Source folder', extract: 'Text extraction',
}

/** A check's steps, each with what it found or what to change. */
export function StepList({ steps, label }: { steps: CheckStep[]; label: string }) {
  return (
    <DescriptionList aria-label={label}>
      {steps.map(s => (
        <DescriptionListItem key={s.name} label={STEP_LABEL[s.name] ?? s.name}>
          <StatusDot variant={!s.ok ? 'error' : s.warn ? 'warning' : 'success'} /> {s.detail}
        </DescriptionListItem>
      ))}
    </DescriptionList>
  )
}

/** Asks before a build makes folders that do not exist yet. */
export function CreateConfirm({ paths, where, open, onClose, onConfirm }: {
  paths: string[]
  /** Where the folders are, such as "on me@box". */
  where?: string
  open: boolean
  onClose: () => void
  onConfirm: () => Promise<unknown> | void
}) {
  return (
    <ConfirmModal open={open} onClose={onClose} onConfirm={onConfirm}
      title={paths.length === 1 ? 'Create this folder?' : 'Create these folders?'}
      description={paths.length === 1
        ? `It does not exist yet${where ? ` ${where}` : ''}. The build makes it, then indexes the source folder.`
        : `They do not exist yet${where ? ` ${where}` : ''}. The build makes them, then indexes the source folder.`}
      confirmLabel={paths.length === 1 ? 'Create it and build' : 'Create them and build'}>
      <ul className="lib-create-list">{paths.map(p => <li key={p}><code>{p}</code></li>)}</ul>
    </ConfirmModal>
  )
}

export async function postJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = (await res.json().catch(() => ({}))) as { error?: string }
  if (!res.ok) throw new Error(data.error ?? `${url}: ${res.status}`)
  return data as T
}
