import { Callout } from '@parallelworks/ui'
import { TransferEnd } from './MachineLabel'

/** A transfer's plan as the server holds it with an assistant's transfer
 *  call (internal/transfers Plan): the fields the card shows. */
export interface ChatPlan {
  id: string
  action: 'copy' | 'move' | 'sync'
  label: string
  mover: string
  moverWhy: string
  movers: { name: string; ok: boolean; why?: string }[]
  source: { location: string; label: string; paths?: string[] }
  destination: { location: string; label: string; path?: string }
  files: number
  dirs: number
  bytes: number
  replaces: string[]
  deletes: string[]
  replacesCount: number
  deletesCount: number
  skipped: { hidden: number; links: number }
  freeBytes: number | null
  warnings: string[]
  indexNotes?: string[]
}

/** What an approval part for a held transfer carries besides the block's
 *  own fields: the transfer awaiting approval, its page, and its plan. */
export interface HeldTransfer { id: string; link?: string; plan: ChatPlan }

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`

/** A size as the Transfers page says it. */
export function sizeText(n: number): string {
  if (n < 0) return 'Counted when the transfer starts'
  if (n < 1024) return plural(n, 'byte', 'bytes')
  const units = 'KMGTPE'
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(1)} ${units[i]}B`
}

function countText(files: number, dirs: number): string {
  if (files < 0) return 'Counted when the transfer starts'
  const parts = [plural(files, 'file', 'files')]
  if (dirs > 0) parts.push(plural(dirs, 'folder', 'folders'))
  return parts.join(' in ')
}

function where(label: string, path: string | undefined): string {
  return path ? `${label}: ${path}` : label
}

/** The first names of a list the plan cut at 50, and how many more. */
function Names({ names, count }: { names: string[]; count: number }) {
  const shown = names.slice(0, 5)
  const more = count - shown.length
  return (
    <ul className="chat-xfer-names">
      {shown.map(n => <li key={n} title={n}>{n}</li>)}
      {more > 0 && <li className="chat-xfer-muted">and {more.toLocaleString()} more</li>}
    </ul>
  )
}

/**
 * The plan of a transfer the assistant asked for, drawn above the chat's
 * approval buttons with the numbers the Transfers page's confirm dialog
 * shows: what goes from where to where, how much, which mover and why,
 * what is replaced and deleted (deletions in the error tone), and the free
 * space at the destination.
 */
export function ChatTransferPlan({ transfer }: { transfer: HeldTransfer }) {
  const plan = transfer.plan
  const paths = plan.source.paths ?? []
  const others = plan.movers.filter(m => m.name !== plan.mover)
  const free = plan.freeBytes
  const short = free !== null && plan.bytes > 0 && plan.bytes > free
  const action = plan.action === 'sync'
    ? 'Sync: send new and changed files'
    : plan.action === 'move' ? 'Move: copy, check, then remove the originals' : 'Copy'
  // Warnings the callouts below already say are not repeated.
  const warnings = plan.warnings.filter(w => !/ will be (replaced|deleted)$/.test(w))
  return (
    <section className="chat-xfer" aria-label="Transfer plan" data-testid="chat-transfer-plan">
      <dl className="chat-xfer-facts">
        <dt>Action</dt><dd>{action}</dd>
        <dt>From</dt>
        <dd className="chat-xfer-mono">
          {paths.length > 1
            ? <><TransferEnd location={plan.source.location}>{plan.source.label}</TransferEnd><Names names={paths} count={paths.length} /></>
            : <TransferEnd location={plan.source.location}>{where(plan.source.label, paths[0])}</TransferEnd>}
        </dd>
        <dt>To</dt><dd className="chat-xfer-mono"><TransferEnd location={plan.destination.location}>{where(plan.destination.label, plan.destination.path)}</TransferEnd></dd>
        <dt>Files</dt><dd>{countText(plan.files, plan.dirs)}</dd>
        <dt>Size</dt><dd>{sizeText(plan.bytes)}</dd>
        <dt>Free space</dt><dd>{free === null ? 'Not reported by the destination' : sizeText(free)}</dd>
        <dt>Mover</dt>
        <dd>
          <b>{plan.mover}</b>{plan.moverWhy ? `: ${plan.moverWhy}` : ''}
          {others.length > 0 && (
            <ul className="chat-xfer-movers">
              {others.map(m => <li key={m.name}>{m.name}: {m.ok ? 'can also reach both ends' : (m.why ?? 'cannot reach both ends')}</li>)}
            </ul>
          )}
        </dd>
      </dl>
      {plan.replacesCount > 0 && (
        <Callout tone="warning" size="sm" testId="chat-xfer-replaces" title={`Replaces ${plural(plan.replacesCount, 'file', 'files')} at the destination`}>
          <Names names={plan.replaces} count={plan.replacesCount} />
        </Callout>
      )}
      {plan.deletesCount > 0 && (
        <Callout tone="error" size="sm" testId="chat-xfer-deletes" title={`Deletes ${plural(plan.deletesCount, 'file', 'files')} at the destination that the source does not have`}>
          <Names names={plan.deletes} count={plan.deletesCount} />
        </Callout>
      )}
      {plan.action === 'move' && (
        <Callout tone="warning" size="sm" title="The originals are removed">
          Once every file is checked at the destination, Studio removes them from {plan.source.label}.
        </Callout>
      )}
      {short && <Callout tone="error" size="sm">The destination reports {sizeText(free!)} free, less than the {sizeText(plan.bytes)} to send.</Callout>}
      {warnings.map(w => <Callout key={w} tone="warning" size="sm">{w}</Callout>)}
      {(plan.indexNotes ?? []).map(n => <Callout key={n} tone="info" size="sm" testId="chat-xfer-index">{n}.</Callout>)}
      <p className="chat-xfer-muted chat-xfer-foot">
        Listed on the <a href={transfer.link ?? '#view=transfers'}>Transfers page</a> as waiting for approval.
      </p>
    </section>
  )
}
