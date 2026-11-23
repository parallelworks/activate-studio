import { loadPwCodeMessages, type PwCodeMessages } from '@parallelworks/code'
import { useEffect, useState } from 'react'
import { IntlProvider } from 'use-intl'
import { describeError } from '../pwcode/describeError'
import { AccountPanelBody } from '../pwcode/session/AccountPanel'
import { IndexGroup } from './IndexGroup'

interface Here { available: boolean; hostname?: string }

/**
 * Settings, Accounts: the accounts pw code uses where this Studio runs it for
 * the person (machine "studio": this computer, or their own pw code on the
 * Studio's cluster), managed with the same panel as a session's account
 * chip. Nothing shows where the Studio has no pw code for them.
 */
export function ClaudeAccountSlot() {
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
  if (!here?.available || !messages) return null
  const machineName = here.hostname || "this Studio's machine"
  return (
    <IndexGroup
      title="Claude Code accounts"
      help="The accounts pw code signs in with where this Studio runs pw code for you, with their usage: add one, choose which a session uses from its account chip, remove one or move its sessions. A session on another machine shows that machine's accounts from its account chip.">
      <IntlProvider locale="en" messages={messages} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone}>
        <AccountPanelBody bare machine="studio" machineName={machineName} describeError={describeError} />
      </IntlProvider>
    </IndexGroup>
  )
}
