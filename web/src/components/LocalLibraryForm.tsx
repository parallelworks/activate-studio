import { useState } from 'react'
import { Button, Callout, Input } from '@parallelworks/ui'
import { api, type SettingsLibrary } from '../api'

const EMPTY = { indexRoot: '', sourceRoot: '', label: '', id: '' }

/**
 * Adds an existing GUFI index on the machine the Studio runs on, read only
 * (POST /api/libraries). The server checks the directory first and refuses
 * one that is not a GUFI index, with the reason; without an identifier it
 * makes one from the label.
 */
export function LocalLibraryForm({ onAdded }: { onAdded?: (lib: SettingsLibrary) => void }) {
  const [form, setForm] = useState(EMPTY)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }
  const indexName = form.indexRoot.trim().replace(/\/+$/, '').split('/').pop() || ''

  const add = async () => {
    setBusy(true)
    setError(null)
    try {
      const r = await api.addLibrary({
        id: form.id.trim(), label: form.label.trim() || undefined,
        indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined,
      })
      setForm(EMPTY)
      onAdded?.(r.library)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }

  return (
    <div className="local-library-form">
      <p className="muted view-sub">
        An index already built on this machine with GUFI. Read only: the Studio searches it and never writes to it.
      </p>
      <div className="settings-grid">
        <Input label="Index directory" mono placeholder="/path/to/index" spellCheck={false} value={form.indexRoot}
          description="The top of the GUFI index: the directory with db.db." onChange={e => set({ indexRoot: e.target.value })} />
        <Input label="Source directory" hint="optional" mono placeholder="/path/to/files" spellCheck={false} value={form.sourceRoot}
          description="The indexed files, to open them. Without it, names and metadata only." onChange={e => set({ sourceRoot: e.target.value })} />
        <Input label="Label" hint="optional" placeholder={indexName || 'Shown in the Library switcher'} value={form.label}
          onChange={e => set({ label: e.target.value })} />
        <Input label="Identifier" hint="optional" placeholder="made from the label" spellCheck={false} value={form.id}
          description="Lowercase letters, digits, dashes. Names the library's folder under All libraries."
          onChange={e => set({ id: e.target.value })} />
      </div>
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!form.indexRoot.trim() || busy} onClick={() => void add()}>
          {busy ? 'Checking…' : 'Add library'}
        </Button>
      </div>
    </div>
  )
}
