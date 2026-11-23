import { useState } from 'react'
import type { MessagePart } from '@parallelworks/ui/ai'
import { Button, Callout } from '@parallelworks/ui'
import './PlanAnswer.css'

/** The plan waiting for an answer, among the session's open approvals
 *  (pendingApprovals from @parallelworks/code: the unresolved ones of the
 *  turn in flight). */
export function openPlan(pending: readonly MessagePart[] | null | undefined): { id: string; plan: string } | null {
  for (const p of pending ?? []) {
    if (p.kind === 'approval' && p.approvalKind === 'plan' && !p.resolved) return { id: p.id, plan: p.plan ?? '' }
  }
  return null
}

/**
 * Answers a plan pw code waits on, here in the chat. The package draws the
 * plan with no way to answer it (PW Code on the web answers in the
 * terminal), and the Studio has no terminal, so without this the session
 * stayed paused until it was stopped. The answer goes through the chat's
 * own route (POST /api/chat/pwcode/{id}/approvals/{approval}), which picks
 * the mode an approved plan moves on to and holds it to the machine's
 * ceiling, as for a mode change.
 */
export function PlanAnswer({ conversationId, approvalId }: { conversationId: string; approvalId: string }) {
  const [change, setChange] = useState('')
  const [busy, setBusy] = useState<'approve' | 'keep' | ''>('')
  const [sent, setSent] = useState(false)
  const [error, setError] = useState('')
  if (sent) return null
  const answer = async (allowed: boolean) => {
    setBusy(allowed ? 'approve' : 'keep'); setError('')
    const message = change.trim()
    try {
      const res = await fetch(`/api/chat/pwcode/${encodeURIComponent(conversationId)}/approvals/${encodeURIComponent(approvalId)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: 'plan', allowed, ...(!allowed && message ? { message } : {}) }),
      })
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? `${res.status}`)
      setSent(true)
    } catch (e) {
      setError(`Could not send the answer: ${String((e as Error).message ?? e)}`)
    } finally { setBusy('') }
  }
  return (
    <div className="pwcode-plan-answer" data-testid="pwcode-plan-answer">
      <Callout variant="bar" tone="warning" live title="pw code has a plan for you to approve"
        action={<>
          <Button variant="primary" size="sm" loading={busy === 'approve'} disabled={!!busy} onClick={() => void answer(true)}>Approve plan</Button>
          <Button variant="secondary" size="sm" loading={busy === 'keep'} disabled={!!busy} onClick={() => void answer(false)}>Keep planning</Button>
        </>}>
        <p className="muted">Approve it and pw code starts the work; keep planning and it revises the plan, with what you write here.</p>
        <input className="pwcode-answer" type="text" aria-label="What to change in the plan" placeholder="What to change, sent with Keep planning"
          value={change} onChange={e => setChange(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && change.trim() && !busy) { e.preventDefault(); void answer(false) } }} />
        {error && <p role="alert">{error}</p>}
      </Callout>
    </div>
  )
}
