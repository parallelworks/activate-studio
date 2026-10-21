import { useEffect, useRef, useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { reloadAppConfig } from '../config'
import { errText } from './PrimaryFolder'

/**
 * The Studio's icon on a single-user install, uploaded from this computer
 * (POST /api/setup/icon): one for light mode and, optionally, one for dark
 * mode. Each preview sits on the background it is for, so an icon that
 * disappears on dark shows it here. The tab icon follows. Used by the setup
 * wizard and Settings, General.
 */

/** As /api/setup/status `branding` and the icon routes give it: the
 *  addresses the browser loads, and the settings behind them. */
export interface Branding { light: string | null; dark: string | null; iconUrl?: string; iconUrlDark?: string }

type Variant = 'light' | 'dark'
const ACCEPT = '.png,.jpg,.jpeg,.svg,.webp,image/png,image/jpeg,image/svg+xml,image/webp'
const MAX_BYTES = 1024 * 1024
const LABEL: Record<Variant, string> = { light: 'Icon for light mode', dark: 'Icon for dark mode' }

async function send(url: string, init: RequestInit): Promise<Branding> {
  const res = await fetch(url, init)
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((body as { error?: string }).error ?? `${res.status}`)
  return body as Branding
}

export function BrandIcons({ value, onChange }: { value: Branding | null | undefined; onChange?: (b: Branding) => void }) {
  const [state, setState] = useState<Branding>(value ?? { light: null, dark: null })
  useEffect(() => { if (value) setState(value) }, [value?.light, value?.dark]) // eslint-disable-line react-hooks/exhaustive-deps
  const [busy, setBusy] = useState<Variant | null>(null)
  const [error, setError] = useState('')
  const inputs = { light: useRef<HTMLInputElement>(null), dark: useRef<HTMLInputElement>(null) }

  const run = async (variant: Variant, fn: () => Promise<Branding>) => {
    setBusy(variant)
    setError('')
    try {
      const b = await fn()
      setState(b)
      onChange?.(b)
      reloadAppConfig()
    } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  const upload = (variant: Variant, file: File | undefined) => {
    if (!file) return
    if (file.size > MAX_BYTES) { setError(`${file.name} is over 1 MB.`); return }
    const form = new FormData()
    form.append('file', file)
    void run(variant, () => send(`/api/setup/icon?variant=${variant}`, { method: 'POST', body: form }))
  }
  const remove = (variant: Variant) => void run(variant, () => send(`/api/setup/icon?variant=${variant}`, { method: 'DELETE' }))

  const slot = (variant: Variant) => {
    const own = state[variant]
    // Without one of its own, dark mode shows the light icon: previewed so.
    const shown = own ?? (variant === 'dark' ? state.light : null)
    return (
      <div key={variant} className="brand-icon-slot">
        <div className={`brand-icon-preview ${variant}`} aria-hidden="true">
          {shown ? <img src={shown} alt="" style={{ opacity: own ? 1 : 0.55 }} /> : <span>None</span>}
        </div>
        <div className="brand-icon-text">
          <div className="brand-icon-name">{variant === 'light' ? 'Light mode' : 'Dark mode'}{variant === 'dark' && <span className="muted"> (optional)</span>}</div>
          <div className="muted brand-icon-note">
            {own ? 'Uploaded.' : variant === 'dark' ? (state.light ? 'Uses the light mode icon.' : 'Uses the light mode icon, once there is one.') : 'The default mark.'}
          </div>
          <div className="brand-icon-actions">
            <input ref={inputs[variant]} type="file" accept={ACCEPT} hidden aria-label={LABEL[variant]}
              onChange={e => { upload(variant, e.target.files?.[0]); e.target.value = '' }} />
            <Button size="sm" variant="secondary" loading={busy === variant} disabled={busy !== null}
              onClick={() => inputs[variant].current?.click()}>
              {own ? 'Replace…' : 'Upload…'}
            </Button>
            {own && <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => remove(variant)} aria-label={`Remove the ${variant} mode icon`}>Remove</Button>}
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="brand-icons">
      <div className="brand-icon-slots">{slot('light')}{slot('dark')}</div>
      <p className="muted brand-icon-help">PNG, JPEG, SVG or WebP, up to 1 MB. A square image works best; the tab icon follows it.</p>
      {error && <Callout tone="error" live>{error}</Callout>}
    </div>
  )
}
