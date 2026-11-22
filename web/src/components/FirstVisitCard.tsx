import { useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { useAppConfig } from '../config'

/**
 * Where people sign in, nobody gets the setup walkthrough (it sets the
 * Studio itself, which is the deployment's to set), so a person's first
 * visit says what the two libraries they meet are: the one everyone shares,
 * and My library, their own folder on a remote computer. It shows once per
 * person in each browser, and changes nothing.
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
  const main = cfg.libraries?.find(l => l.primary)
  const readOnly = !!main && (main.writable === false || !!main.readOnlyWhy)
  const close = () => {
    try { localStorage.setItem(seenKey(user), '1') } catch { /* shown again next time */ }
    setClosed(true)
  }
  return (
    <div className="first-visit" role="region" aria-label={`Welcome to ${cfg.appName}`}>
      <Callout title={`Welcome to ${cfg.appName}`} action={<Button size="sm" variant="secondary" onClick={close}>Got it</Button>}>
        <p><strong>{cfg.kbLabel}</strong> is the library everyone here searches.{' '}
          {readOnly ? 'It is read only for you; its administrators change it.' : 'You may change it.'}</p>
        <p><strong>My library</strong> is your own folder on one of your remote computers, read and written as you. Where it is
          offered, add it from the Library&apos;s <strong>+ Add</strong> menu, <strong>Add a library…</strong>.</p>
      </Callout>
    </div>
  )
}
