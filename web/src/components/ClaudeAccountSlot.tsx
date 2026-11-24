import { loadPwCodeMessages, type PwCodeMessages } from '@parallelworks/code'
import { useEffect, useState } from 'react'
import { StatusLabel } from '@parallelworks/ui'
import { IntlProvider } from 'use-intl'
import { describeError } from '../pwcode/describeError'
import { pwCode } from '../pwcode/api'
import { AccountPanelBody } from '../pwcode/session/AccountPanel'
import { AccountRow } from './AccountRow'

interface Here { available: boolean; hostname?: string }

/**
 * Settings, Accounts: the accounts pw code uses where this Studio runs it for
 * the person (machine "studio": this computer, or their own pw code on the
 * Studio's cluster), managed with the same panel as a session's account
 * chip. Nothing shows where the Studio has no pw code for them.
 *
 * A row like the page's others, its state the number of accounts. The page
 * is told that number once it is known (`onKnown`), for whether the row
 * starts open.
 */
export function ClaudeAccountSlot({ open = true, onOpenChange, onKnown }: {
  open?: boolean
  onOpenChange?: (open: boolean) => void
  onKnown?: (accounts: number) => void
} = {}) {
  const [here, setHere] = useState<Here | null>(null)
  const [messages, setMessages] = useState<PwCodeMessages | null>(null)
  useEffect(() => {
    let gone = false
    fetch('/api/chat/pwcode/here')
      .then(r => (r.ok ? r.json() : null))
      .then((h: Here | null) => { if (!gone) setHere(h) }, () => {})
    void loadPwCodeMessages('en').then(m => { if (!gone) setMessages(m) }, () => {})
    return () => { gone = true }
  }, [])
  const shown = !!here?.available && !!messages
  // The names alone, as the panel reads them (one read, shared with it).
  const names = pwCode.useClaudeAccountNames('studio', shown)
  const count = names.data?.length ?? null
  const settled = count !== null || !!names.error
  useEffect(() => { if (shown && settled) onKnown?.(count ?? 0) }, [shown, settled]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!shown) return null
  const machineName = here?.hostname || "this Studio's machine"
  return (
    <AccountRow id="claude-code" title="Claude Code accounts" open={open} onOpenChange={x => onOpenChange?.(x)}
      description="The Claude accounts pw code signs in with where this Studio runs it, with their usage."
      state={count ? <StatusLabel variant="success">{count === 1 ? '1 account' : `${count} accounts`}</StatusLabel>
        : count === 0 ? <StatusLabel variant="muted">None added</StatusLabel>
        : names.error ? <StatusLabel variant="warning">Could not read</StatusLabel>
        : <StatusLabel variant="muted">Reading…</StatusLabel>}>
      <div>
        <p className="muted view-sub">
          Add one, choose which a session uses from its account chip, remove one or move its sessions. A session on
          another machine shows that machine's accounts from its account chip.
        </p>
        <IntlProvider locale="en" messages={messages} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone}>
          <AccountPanelBody bare machine="studio" machineName={machineName} describeError={describeError} />
        </IntlProvider>
      </div>
    </AccountRow>
  )
}
