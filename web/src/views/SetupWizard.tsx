import { type CSSProperties, useEffect, useRef, useState } from 'react'
import { Button, Callout, EmptyState, Loader, Switch } from '@parallelworks/ui'
import { FolderPicker, IndexBuildProgress, json, post, errText, useIndexBuild, type SetupStatus } from '../components/PrimaryFolder'
import { SshLibraryForm, type SshLibraryAdded } from '../components/SshLibraryForm'
import { SECTION_SWITCHES, reloadAppConfig, saveSections, sectionAllowed, sectionsSetting, useAppConfig } from '../config'

/**
 * First-run setup for a single-user install: which pages the sidebar shows,
 * the folder to index, and indexes on other machines. Shown over the app on
 * a first run, and from Settings, General afterwards. The folder step is
 * the same picker and index build as Settings, Libraries (PrimaryFolder).
 */

export type { IndexJob, SetupStatus } from '../components/PrimaryFolder'

const STEPS = ['Welcome', 'Sidebar', 'Your folder', 'Remote indexes', 'Done']

/** Switches for the sidebar's pages, saved as the sections setting. */
export function SidebarSwitches({ value, platform, onChange }: {
  value: string[] | undefined
  platform: boolean
  onChange: (sections: string[]) => void
}) {
  const fromValue = () => SECTION_SWITCHES.map(s => s.id).filter(id => sectionAllowed(id, { sections: value, platform: true }))
  const [shown, setShown] = useState(fromValue)
  const key = (value ?? []).join(',')
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setShown(fromValue()), [key])
  const toggle = (id: string, on: boolean) => {
    const next = on ? [...shown, id] : shown.filter(x => x !== id)
    setShown(next)
    onChange(sectionsSetting(next))
  }
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '10px 16px' }}>
      {SECTION_SWITCHES.map(s => (
        <label key={s.id} className="key-persist" style={{ margin: 0, color: 'var(--pw-text)' }}>
          <Switch checked={shown.includes(s.id)} onCheckedChange={v => toggle(s.id, v)} aria-label={s.label} />
          <span>
            {s.label}
            {s.id === 'workflows' && !platform && <span className="muted"> (needs a platform login)</span>}
          </span>
        </label>
      ))}
    </div>
  )
}

const overlay: CSSProperties = {
  position: 'fixed', inset: 0, zIndex: 60, overflowY: 'auto', background: 'var(--pw-bg)',
  display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '6vh 16px 24px',
}
const panel: CSSProperties = { width: '100%', maxWidth: 640, padding: 24, display: 'flex', flexDirection: 'column', gap: 16 }
const row: CSSProperties = { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }

export function SetupWizard({ onClose, pollMs = 1000 }: {
  /** `folderChanged` says the Studio restarted on another folder. */
  onClose: (folderChanged: boolean) => void
  pollMs?: number
}) {
  const cfg = useAppConfig()
  const [step, setStep] = useState(0)
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [statusError, setStatusError] = useState('')
  const [restarting, setRestarting] = useState(false)
  const [folderChanged, setFolderChanged] = useState(false)
  const [added, setAdded] = useState<SshLibraryAdded[]>([])
  const [sectionsError, setSectionsError] = useState('')
  const [finishing, setFinishing] = useState(false)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])
  // Each step starts with focus on its heading, so a screen reader reads it
  // and Tab moves through that step.
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => { heading.current?.focus() }, [step])

  const loadStatus = async (): Promise<SetupStatus | null> => {
    try {
      const s = await json<SetupStatus>('/api/setup/status?files=1')
      if (alive.current) { setStatus(s); setStatusError('') }
      return s
    } catch (e) {
      if (alive.current) setStatusError(errText(e))
      return null
    }
  }

  const index = useIndexBuild({ pollMs, onDone: async () => { await loadStatus(); reloadAppConfig() } })

  useEffect(() => {
    void loadStatus().then(s => {
      // A build started before the wizard was reopened.
      if (s?.job) void index.follow(s.job)
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const folderChangedTo = async () => {
    setFolderChanged(true)
    index.reset()
    const s = await loadStatus()
    reloadAppConfig()
    return s?.primary?.path
  }

  const finish = async () => {
    setFinishing(true)
    try { await post('/api/setup/complete') } catch { /* shown again next time; nothing lost */ }
    onClose(folderChanged)
  }

  const changeSections = (next: string[]) => {
    setSectionsError('')
    saveSections(next).catch(e => setSectionsError(errText(e)))
  }

  const primary = status?.primary
  const building = index.building
  const remote = [
    ...(status?.libraries ?? []).filter(l => !l.primary).map(l => ({ id: l.id, label: l.label })),
    ...added,
  ].filter((l, i, all) => all.findIndex(x => x.id === l.id) === i)
  const shownLabels = SECTION_SWITCHES.filter(s => sectionAllowed(s.id, { sections: cfg.sections, platform: cfg.platform })).map(s => s.label)

  const indexState = !primary ? '' : !primary.exists ? 'This folder does not exist yet.'
    : primary.indexed ? (primary.files !== undefined ? `Indexed: ${primary.files.toLocaleString()} files.` : 'Indexed.')
    : 'Not indexed yet.'

  const last = STEPS.length - 1
  return (
    <div style={overlay} role="dialog" aria-modal="true" aria-labelledby="setup-title">
      <div className="card" style={panel}>
        <div>
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>Step {step + 1} of {STEPS.length}</p>
          <h1 id="setup-title" ref={heading} tabIndex={-1} style={{ margin: '4px 0 0', fontSize: 20, outline: 'none' }}>{STEPS[step]}</h1>
        </div>

        {step === 0 && (
          <>
            <p style={{ margin: 0 }}>
              {cfg.appName} indexes a folder on this computer so you can browse, search and query its files, and it can search
              GUFI indexes on other machines over ssh.
            </p>
            <p className="muted" style={{ margin: 0 }}>The next steps set the sidebar, the folder and any remote indexes. Each can be changed later in Settings.</p>
          </>
        )}

        {step === 1 && (
          <>
            <p className="muted" style={{ margin: 0 }}>Pages to show in the sidebar. Settings and Help always show.</p>
            <SidebarSwitches value={cfg.sections} platform={!!cfg.platform} onChange={changeSections} />
            {sectionsError && <Callout tone="error" live>{sectionsError}</Callout>}
          </>
        )}

        {step === 2 && (
          <>
            {statusError && <Callout tone="error">{statusError}</Callout>}
            {!status && !statusError && <Loader inline size={16} label="Loading…" />}
            {primary && (
              <div>
                <p style={{ margin: 0 }}>Folder in use: <b style={{ wordBreak: 'break-all' }}>{primary.path}</b></p>
                <p className="muted" style={{ margin: '2px 0 0' }}>{indexState}</p>
              </div>
            )}
            {status && (
              <FolderPicker current={primary?.path} start={primary?.exists ? primary.path : '~'} disabled={building}
                pollMs={pollMs} onRestarting={setRestarting} onChanged={folderChangedTo} />
            )}
            {status && !restarting && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {status.gufi && !status.gufi.found && (
                  <Callout tone="warning">GUFI was not found in {status.gufi.bin}, so the index cannot be built.</Callout>
                )}
                <div style={row}>
                  <Button variant="secondary" loading={building} disabled={building || !status.gufi?.found || !primary?.exists}
                    onClick={() => void index.start()}>
                    {primary?.indexed ? 'Rebuild the index' : 'Build the index'}
                  </Button>
                </div>
                <IndexBuildProgress job={index.job} error={index.error} />
              </div>
            )}
          </>
        )}

        {step === 3 && (
          <>
            <p className="muted" style={{ margin: 0 }}>
              A GUFI index on another machine, reached with your own ssh keys and ssh config. Remote indexes are read only.
            </p>
            <SshLibraryForm compact onAdded={lib => setAdded(a => [...a, lib])} />
            {remote.length ? (
              <div>
                <p style={{ margin: '0 0 4px' }}>Connected:</p>
                <ul style={{ margin: 0, paddingLeft: 20 }}>{remote.map(l => <li key={l.id}>{l.label}</li>)}</ul>
              </div>
            ) : <EmptyState variant="bare" title="No remote indexes yet" description="They can also be added later in Settings, Libraries." />}
          </>
        )}

        {step === 4 && (
          <ul style={{ margin: 0, paddingLeft: 20, display: 'flex', flexDirection: 'column', gap: 4 }}>
            <li>Folder: {primary ? `${primary.path}. ${indexState}` : 'unknown'}</li>
            <li>Sidebar: {shownLabels.length ? shownLabels.join(', ') : 'Settings and Help only'}</li>
            <li>Remote indexes: {remote.length ? remote.map(l => l.label).join(', ') : 'none'}</li>
          </ul>
        )}

        <div style={{ ...row, justifyContent: 'space-between', marginTop: 8 }}>
          {step < last
            ? <Button variant="ghost" disabled={finishing || restarting} onClick={() => void finish()}>Skip setup</Button>
            : <span />}
          <div style={row}>
            {step > 0 && <Button variant="secondary" disabled={restarting} onClick={() => setStep(s => s - 1)}>Back</Button>}
            {step < last
              ? <Button variant="primary" disabled={restarting} onClick={() => setStep(s => s + 1)}>Next</Button>
              : <Button variant="primary" loading={finishing} onClick={() => void finish()}>Finish</Button>}
          </div>
        </div>
      </div>
    </div>
  )
}
