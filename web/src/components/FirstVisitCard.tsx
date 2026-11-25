import { useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { mainWritable, useAppConfig } from '../config'
import { accountsHref } from '../accounts'

/**
 * Where people sign in, nobody gets the setup walkthrough (it sets the
 * Studio itself, which is the deployment's to set), so a person's first
 * visit says what the two libraries they meet are: the one everyone shares,
 * and My library, their own folder on a remote computer, and where they
 * sign in with ACTIVATE (Settings, Accounts). It shows once per
 * person in each browser, and changes nothing. It is a part of the page, at
 * its foot: floating, it would lie over the message box or a form's buttons.
 * On a phone or in a low window it is two lines, so the page keeps its room.
 */
const seenKey = (user: string) => `ade-first-visit:${user}`

function seen(user: string): boolean {
  try { return localStorage.getItem(seenKey(user)) === '1' } catch { return true }
}

export function FirstVisitCard() {
  const cfg = useAppConfig()
  const user = cfg.user?.id ?? ''
  const [closed, setClosed] = useState(false)
  const framed = typeof window !== 'undefined' && (window.parent !== window || new URLSearchParams(location.search).has('embed'))
  if (!cfg.signIn || !user || user === 'user' || framed || closed || seen(user)) return null
  const readOnly = !mainWritable(cfg)
  const close = () => {
    try { localStorage.setItem(seenKey(user), '1') } catch { /* shown again next time */ }
    setClosed(true)
  }
  return (
    <div className="first-visit" role="region" aria-label={`Welcome to ${cfg.appName}`}>
      <Callout title={`Welcome to ${cfg.appName}`} action={<Button size="sm" variant="secondary" onClick={close}>Got it</Button>}>
        <p className="first-visit-short">
          Sign in with ACTIVATE under <a href={accountsHref('activate')} onClick={close}>Settings, Accounts</a>.{' '}
          <a href="#view=help">Help</a> says what {cfg.kbLabel} and My library are.
        </p>
        <p className="first-visit-long"><strong>{cfg.kbLabel}</strong> is the library everyone here searches.{' '}
          {readOnly ? 'It is read only for you; its administrators change it.' : 'You may change it.'}</p>
        <p className="first-visit-long"><strong>My library</strong> is your own folder on one of your remote computers, read and written as you. Where it is
          offered, <strong>Add a library…</strong> under the arrow beside <strong>+ Add</strong> in the Library adds it.</p>
        <p className="first-visit-long"><strong>Settings, Accounts</strong> is where you sign in with ACTIVATE, as yourself: My library, pw code, workflows and
          desktops run with that sign-in, never with the Studio's own account. <a href={accountsHref('activate')} onClick={close}>Open Accounts</a></p>
      </Callout>
    </div>
  )
}
