import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from '@parallelworks/ui'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TransfersSettingsSection } from '../src/components/TransfersSettingsSection'
import { TRANSFER_SETTINGS_CHANGED } from '../src/transferApi'
import { settingsSections } from '../src/views/SettingsView'
import { createTransfersMock, mockSettings, type MockOptions } from './transfersMock'

// A switch or checkbox forwards its press to its hidden input as a
// PointerEvent, which jsdom lacks.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

let mock: ReturnType<typeof createTransfersMock>
function serve(opts: MockOptions = {}) {
  mock = createTransfersMock(opts)
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const r = mock.handle(init?.method ?? 'GET', String(input), init?.body ? JSON.parse(String(init.body)) : undefined)
    return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
  }))
}
beforeEach(() => { vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} }) })
afterEach(() => { vi.unstubAllGlobals() })

const saved = () => mock.calls.filter(c => c.method === 'PUT' && c.path === '/api/transfers/settings').map(c => c.body as ReturnType<typeof mockSettings>)
const added = () => mock.calls.filter(c => c.method === 'POST' && c.path === '/api/transfers/settings/locations').map(c => c.body as Record<string, unknown>)

describe('Settings, Transfers', () => {
  it('sits beside Indexing only where the server has transfers', () => {
    expect(settingsSections({ authEnabled: false, transfers: true }).map(s => s.id).slice(0, 3)).toEqual(['general', 'indexing', 'transfers'])
    expect(settingsSections({ authEnabled: false }).map(s => s.id)).not.toContain('transfers')
  })

  it('opens on what is ready, then the locations, folders, movers and defaults', async () => {
    serve()
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    const ready = await screen.findByText('Ready to use')
    const box = ready.closest('.xfer-ready') as HTMLElement
    // 8 locations less 1 unavailable, plus 1 offered library host.
    await waitFor(() => expect(within(box).getByText('8 places ready')).toBeInTheDocument())
    expect(within(box).getByText(/1 library host/)).toBeInTheDocument()
    expect(within(box).getByText(/rsync 3\.2\.7 at \/usr\/bin\/rsync: used on this computer, for sends to hosts and for pulls from hosts/)).toBeInTheDocument()
    expect(within(table).getByText('Scratch on archive-host')).toBeInTheDocument()
    expect(screen.getByText('Saved locations')).toBeInTheDocument()
    expect(screen.getByText('Folders on this computer')).toBeInTheDocument()
    expect(screen.getAllByText('How files move').length).toBeGreaterThan(0)
    expect(screen.getByRole('switch', { name: 'Use Desktop' })).not.toBeChecked()
    expect(screen.getByRole('switch', { name: 'Use Downloads' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Offer storage-host' })).not.toBeChecked()
    expect(screen.getByText('/home/user/Projects')).toBeInTheDocument()
    expect(screen.getByText('Always on')).toBeInTheDocument()
    expect(screen.getByText(/Found rsync 3\.2\.7 at \/usr\/bin\/rsync: used on this computer, for sends to hosts and for pulls from hosts\./)).toBeInTheDocument()
    // CONDUIT is off, so its server choice is hidden.
    expect(screen.queryByRole('combobox', { name: 'CONDUIT server' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('checks the fields before testing, tests on save, and saves a location that passes', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('button', { name: 'Add a location' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    expect(within(dialog).getByText(/An ssh destination: user@host/)).toBeInTheDocument()
    fireEvent.change(within(dialog).getByLabelText('Host', { exact: false }), { target: { value: 'user@archive-host' } })
    fireEvent.change(within(dialog).getByLabelText('Folder', { exact: false }), { target: { value: 'srv' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    expect(await within(dialog).findByText(/Start the folder with \//)).toBeInTheDocument()
    expect(mock.calls.some(c => c.path === '/api/transfers/settings/test')).toBe(false)

    fireEvent.change(within(dialog).getByLabelText('Folder', { exact: false }), { target: { value: '/srv/archive' } })
    fireEvent.change(within(dialog).getByLabelText('Label', { exact: false }), { target: { value: 'Archive' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    // A new location is added on its own (POST), so it cannot undo or be
    // undone by any other change.
    await waitFor(() => expect(added()).toHaveLength(1))
    expect(saved()).toHaveLength(0)
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toEqual({ host: 'user@archive-host', root: '/srv/archive' })
    expect(added()[0]).toEqual({ id: '', label: 'Archive', host: 'user@archive-host', root: '/srv/archive' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const row = within(screen.getByRole('table')).getByText('Archive').closest('tbody') as HTMLElement
    expect(within(row).getByText(/^Reached, folder exists, rsync 3\.2\.7 found, writable/)).toBeInTheDocument()
  })

  it('shows why a test failed and can save the location anyway', async () => {
    serve({ testReply: { ok: false, why: 'Permission denied (publickey)' } })
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('button', { name: 'Add a location' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Host', { exact: false }), { target: { value: 'user@offline-host' } })
    fireEvent.change(within(dialog).getByLabelText('Folder', { exact: false }), { target: { value: '/data' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    expect(await within(dialog).findByText(/Permission denied \(publickey\)/)).toBeInTheDocument()
    expect(added()).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save anyway' }))
    await waitFor(() => expect(added()).toHaveLength(1))
    expect(added()[0]).toMatchObject({ label: 'user@offline-host', host: 'user@offline-host', root: '/data' })
    const row = (await within(screen.getByRole('table')).findByText('user@offline-host', { selector: 'td:first-child' })).closest('tbody') as HTMLElement
    expect(within(row).getByText('Test failed: Permission denied (publickey)')).toBeInTheDocument()
  })

  it('edits a location through the dialog, and removes one only after confirming', async () => {
    serve()
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    fireEvent.click(within(within(table).getAllByRole('row')[1]).getByRole('button', { name: 'Edit' }))
    const dialog = await screen.findByRole('dialog', { name: 'Edit saved location' })
    fireEvent.change(within(dialog).getByLabelText('Label', { exact: false }), { target: { value: 'Shared scratch' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].savedLocations.map(l => l.label)).toEqual(['Shared scratch', 'Results'])
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    const results = within(screen.getByRole('table')).getByText('Results').closest('tr') as HTMLElement
    fireEvent.click(within(results).getByRole('button', { name: 'Remove' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Remove Results?' })
    expect(saved()).toHaveLength(1)
    fireEvent.click(within(confirm).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(saved()).toHaveLength(2))
    expect(saved()[1].savedLocations.map(l => l.label)).toEqual(['Shared scratch'])
  })

  it('saves an added or removed folder and a switched built-in folder at once', async () => {
    serve()
    const success = vi.spyOn(toast, 'success')
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.change(screen.getByRole('textbox', { name: 'Folder to add' }), { target: { value: '/home/user/Data' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add folder' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].computerFolders.at(-1)).toEqual({ path: '/home/user/Data', label: 'Data', builtIn: false, enabled: true })
    expect(saved()[0].revision).toBe('r1')
    await waitFor(() => expect(success).toHaveBeenCalledWith('Added Data. Transfers can use it now.'))
    // Nothing is left unsaved, and the field is cleared for the next one.
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.queryByText('Unsaved changes')).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Folder to add' })).toHaveValue('')

    fireEvent.click(screen.getByRole('switch', { name: 'Use Downloads' }))
    await waitFor(() => expect(saved()).toHaveLength(2))
    expect(saved()[1].computerFolders.find(f => f.label === 'Downloads')!.enabled).toBe(false)
    expect(saved()[1].revision).toBe('r2')

    fireEvent.click(await screen.findByRole('button', { name: 'Remove Data' }))
    await waitFor(() => expect(saved()).toHaveLength(3))
    expect(saved()[2].computerFolders.map(f => f.path)).not.toContain('/home/user/Data')
    await waitFor(() => expect(success).toHaveBeenCalledWith('Removed Data. Its files are not touched.'))
    success.mockRestore()
  })

  it('says why a folder could not be added, and keeps what was typed', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    const handle = mock.handle
    mock.handle = (m, url, body) => (m === 'PUT' ? { status: 400, body: { error: 'computerFolders[4].path: /home/user/Gone is not there' } } : handle(m, url, body))
    fireEvent.change(screen.getByRole('textbox', { name: 'Folder to add' }), { target: { value: '/home/user/Gone' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add folder' }))
    expect(await screen.findByText(/\/home\/user\/Gone is not there/)).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Folder to add' })).toHaveValue('/home/user/Gone')
    expect(screen.queryByText('/home/user/Gone', { selector: 'code' })).toBeNull()
  })

  it('marks Save fields as unsaved and saves them with Save', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('checkbox', { name: 'Offer archive-host' }))
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].hostLocations).toEqual({ enabled: true, hidden: ['storage-host', 'archive-host'] })
    expect(saved()[0].revision).toBe('r1')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled())
    expect(screen.queryByText('Unsaved changes')).toBeNull()
  })

  it('keeps a long folder path inside the section, whole in its title, with Remove beside it', async () => {
    const long = `/home/user/${'very-long-folder-name-'.repeat(8)}end`
    const st = mockSettings()
    st.computerFolders = [...(st.computerFolders as object[]), { path: long, label: 'Long', builtIn: false, enabled: true }]
    serve({ settings: st })
    render(<TransfersSettingsSection />)
    const path = await screen.findByText(long, { selector: 'code' })
    expect(path).toHaveAttribute('title', long)
    expect(path).toHaveClass('xfer-settings-folder-path')
    const row = path.closest('li') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Remove Long' })).toBeInTheDocument()
    // jsdom has no layout, so the rules that keep it in place are checked.
    const css = readFileSync(resolve(__dirname, '../src/styles.css'), 'utf8')
    const rule = (sel: string) => new RegExp(`(^|\\n)${sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]*)\\}`).exec(css)?.[2] ?? ''
    expect(rule('.xfer-settings-folder-path')).toMatch(/min-width: 0/)
    expect(rule('.xfer-settings-folder-path')).toMatch(/text-overflow: ellipsis/)
    expect(rule('.xfer-settings-folder-path')).toMatch(/white-space: nowrap/)
    expect(rule('.xfer-settings-folders li')).toMatch(/min-width: 0/)
    expect(rule('.xfer-settings > *')).toMatch(/min-width: 0/)
    expect(css).toMatch(/\.xfer-settings-folders li > button[^{]*\{[^}]*flex: none/)
  })

  it('shows each row test\'s result beside what was tested', async () => {
    serve({ testReply: { ok: false, why: 'Permission denied (publickey)' } })
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    const row = within(table).getAllByRole('row')[2].closest('tbody') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Test' }))
    expect(await within(row).findByText('Test failed: Permission denied (publickey)')).toBeInTheDocument()
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toEqual({ host: 'user@compute-host', root: '/data/results', rsyncPath: '/opt/bin/rsync' })
  })

  it('describes a good test plainly', async () => {
    serve()
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    const row = within(table).getAllByRole('row')[1].closest('tbody') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Test' }))
    expect(await within(row).findByText(/^Reached, folder exists, rsync 3\.2\.7 found, writable, 2 TB free, 38 ms round trip\.$/)).toBeInTheDocument()
  })

  it('says what an old rsync on a host is used for', async () => {
    serve({ testReply: { ok: true, rsync: true, rsyncVersion: '2.6.9', rsyncText: 'rsync 2.6.9 there: used for sends and pulls with this computer and for copies on that host', writable: true, freeBytes: 2 * 1024 ** 4, roundTripMs: 38 } })
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    const row = within(table).getAllByRole('row')[1].closest('tbody') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Test' }))
    expect(await within(row).findByText(/^Reached, folder exists, rsync 2\.6\.9 found, writable, 2 TB free, 38 ms round trip\. rsync 2\.6\.9 there: used for sends and pulls with this computer and for copies on that host\.$/)).toBeInTheDocument()
  })

  it('says plainly when the account takes SFTP only', async () => {
    serve({ testReply: { ok: true, sftpOnly: true, rsync: false, writable: true, freeBytes: 2 * 1024 ** 4, roundTripMs: 40 } })
    render(<TransfersSettingsSection />)
    const table = await screen.findByRole('table')
    const row = within(table).getAllByRole('row')[1].closest('tbody') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Test' }))
    expect(await within(row).findByText(/^Reached over SFTP \(the account takes SFTP only\), folder exists, rsync not available, Studio copy will be used, writable, 2 TB free, 40 ms round trip\.$/)).toBeInTheDocument()
  })

  it('sends the advanced fields, and shows a server refusal under the field it names', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('button', { name: 'Add a location' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Host', { exact: false }), { target: { value: 'user@archive-host' } })
    fireEvent.change(within(dialog).getByLabelText('Folder', { exact: false }), { target: { value: '/srv/archive' } })
    fireEvent.click(within(dialog).getByText('Advanced'))
    fireEvent.change(await within(dialog).findByLabelText('Port', { exact: false }), { target: { value: '99999' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    expect(await within(dialog).findByText('A port from 1 to 65535.')).toBeInTheDocument()
    fireEvent.change(within(dialog).getByLabelText('Port', { exact: false }), { target: { value: '2222' } })
    fireEvent.change(within(dialog).getByLabelText('ssh options', { exact: false }), { target: { value: '-o IdentityFile=/home/user/.ssh/k' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    await waitFor(() => expect(added()).toHaveLength(1))
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toEqual({ host: 'user@archive-host', root: '/srv/archive', port: 2222, sshOptions: '-o IdentityFile=/home/user/.ssh/k' })
    expect(added()[0]).toEqual({ id: '', label: 'user@archive-host', host: 'user@archive-host', root: '/srv/archive', port: 2222, sshOptions: '-o IdentityFile=/home/user/.ssh/k' })
  })

  it('puts a 400 that names a field under that field', async () => {
    serve()
    const real = mock
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    // A refusal the page's own checks let through, as the server words it.
    const handle = real.handle
    real.handle = (m, url, body) => (url.endsWith('/settings/test') ? { status: 400, body: { error: 'root: give an absolute path with no ".."' } } : handle(m, url, body))
    fireEvent.click(screen.getByRole('button', { name: 'Add a location' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Host', { exact: false }), { target: { value: 'user@archive-host' } })
    fireEvent.change(within(dialog).getByLabelText('Folder', { exact: false }), { target: { value: '/srv/archive' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    expect(await within(dialog).findByText('Give an absolute path with no ".."')).toBeInTheDocument()
    expect(added()).toHaveLength(0)
  })

  it('shows the CONDUIT server and folders only when CONDUIT is on', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    expect(screen.getByText(/A site's data transfer service/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'CONDUIT' }))
    expect(await screen.findByRole('combobox', { name: 'CONDUIT server' })).toBeInTheDocument()
    const mounts = screen.getByRole('table', { name: 'CONDUIT folders' })
    expect(within(mounts).getByDisplayValue('/site/scratch')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add a CONDUIT folder' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'CONDUIT path 2' }), { target: { value: '/site/results' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].movers.conduit.mounts).toEqual([
      { location: 'saved:scratch', path: 'shared', conduit: '/site/scratch' },
      { location: '', path: '', conduit: '/site/results' },
    ])
  })

  it('with sign-in on, shows the values disabled and says the operator sets them', async () => {
    serve({ settings: { ...mockSettings(), managed: true } })
    render(<TransfersSettingsSection />)
    expect(await screen.findByText(/The operator of this Studio sets these/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add a location' })).toBeDisabled()
    // A switch is a span that says it is disabled.
    const off = (el: HTMLElement) => el.hasAttribute('data-disabled') || el.getAttribute('aria-disabled') === 'true'
    expect(off(screen.getByRole('switch', { name: 'Use Downloads' }))).toBe(true)
    expect(off(screen.getByRole('switch', { name: 'rsync' }))).toBe(true)
    for (const b of within(screen.getByRole('table')).getAllByRole('button')) expect(b).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
  })

  it('points to where MCP servers are attached when there are none', async () => {
    serve({ settings: { ...mockSettings(), mcpServers: [] } })
    render(<TransfersSettingsSection />)
    const link = await screen.findByRole('link', { name: 'Assistant tools, Attached MCP servers' })
    expect(link).toHaveAttribute('href', '#view=settings:tools')
  })
})

/** A location saved somewhere else: the Transfers page or the chat. */
const saveElsewhere = (label = 'Chat drop') =>
  mock.handle('POST', '/api/transfers/settings/locations', { label, host: 'user@drop-host', root: '/srv/drop' })
const table = () => screen.getByRole('table')

describe('Settings, Transfers never holds a stale copy', () => {
  it('keeps a location saved elsewhere when Save runs from an older read (the reported loss)', async () => {
    serve()
    const success = vi.spyOn(toast, 'success')
    // Settings, Transfers is opened once, and stays mounted while hidden.
    const { rerender } = render(<TransfersSettingsSection active />)
    await screen.findByRole('table')
    rerender(<TransfersSettingsSection active={false} />)
    saveElsewhere()
    // Back to it: it reads the section again and lists the location.
    rerender(<TransfersSettingsSection active />)
    expect(await within(table()).findByText('Chat drop')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'rsync' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].savedLocations.map(l => l.label)).toContain('Chat drop')
    const now = mock.handle('GET', '/api/transfers/settings').body as ReturnType<typeof mockSettings>
    expect((now.savedLocations as { label: string }[]).map(l => l.label)).toContain('Chat drop')
    expect((now.movers as { rsync: { enabled: boolean } }).rsync.enabled).toBe(false)
    success.mockRestore()
  })

  it('reads the section again when the window regains focus, unless it has unsaved edits', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    saveElsewhere('First')
    act(() => { window.dispatchEvent(new Event('focus')) })
    expect(await within(table()).findByText('First')).toBeInTheDocument()

    // With an unsaved edit, the edit stays and Save stays on; the lists
    // that save at once still show what is saved.
    fireEvent.click(screen.getByRole('switch', { name: 'rsync' }))
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    saveElsewhere('Second')
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(await within(table()).findByText('Second')).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'rsync' })).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('reads again when the Transfers page saves a location', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    saveElsewhere('From the page')
    act(() => { window.dispatchEvent(new Event(TRANSFER_SETTINGS_CHANGED)) })
    expect(await within(table()).findByText('From the page')).toBeInTheDocument()
  })

  it('on a 409 with no conflict, reads again, keeps the edits, saves them on top and says what changed', async () => {
    serve()
    const success = vi.spyOn(toast, 'success')
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('switch', { name: 'rsync' }))
    // Saved elsewhere after the edit, with no focus in between.
    saveElsewhere()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved()).toHaveLength(2))
    expect(saved()[0].revision).toBe('r1')
    expect(saved()[1].revision).toBe('r2')
    expect(saved()[1].savedLocations.map(l => l.label)).toContain('Chat drop')
    expect(saved()[1].movers.rsync.enabled).toBe(false)
    await waitFor(() => expect(success).toHaveBeenCalledWith('Saved. New transfers use these settings. Meanwhile saved locations changed elsewhere, and those changes are kept.'))
    expect(await within(table()).findByText('Chat drop')).toBeInTheDocument()
    success.mockRestore()
  })

  it('on a 409 that conflicts with an edit, saves nothing, says so, and Reload takes the saved settings', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.click(screen.getByRole('switch', { name: 'rsync' }))
    // Another window changed rsync too, and saved.
    const other = mock.handle('GET', '/api/transfers/settings').body as Record<string, unknown>
    const movers = other.movers as Record<string, unknown>
    mock.handle('PUT', '/api/transfers/settings', { ...other, movers: { ...movers, rsync: { enabled: true, path: '/opt/rsync/bin/rsync' } } })
    const puts = saved().length
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    const note = await screen.findByText(/Transfers settings changed elsewhere since this page loaded \(rsync\)/)
    expect(note).toHaveTextContent('Your unsaved changes to rsync differ from what was saved there, so nothing was saved.')
    // One PUT, refused; nothing sent again.
    expect(saved()).toHaveLength(puts + 1)
    // The edit is still there to save on top.
    expect(screen.getByRole('switch', { name: 'rsync' })).not.toBeChecked()
    fireEvent.click(within(note.closest('.xfer-settings-changed') as HTMLElement ?? note).getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(screen.getByRole('switch', { name: 'rsync' })).toBeChecked())
    expect(screen.getByDisplayValue('/opt/rsync/bin/rsync')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.queryByText(/changed elsewhere/)).toBeNull()
  })

  it('removes and edits a location by its id, against the section as saved now', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    // Saved elsewhere after this page read the section.
    saveElsewhere()
    const results = within(table()).getByText('Results').closest('tr') as HTMLElement
    fireEvent.click(within(results).getByRole('button', { name: 'Remove' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Remove' }))
    // The first try is refused (409); the removal is made again to what is
    // saved now, so the location saved elsewhere stays.
    await waitFor(() => expect(saved()).toHaveLength(2))
    expect(saved()[1].savedLocations.map(l => l.label)).toEqual(['Scratch on archive-host', 'Chat drop'])
  })
})

describe('Settings, Transfers: rclone', () => {
  const withRclone = (rclone: Record<string, unknown>, locations?: MockOptions['locations']) => {
    const s = mockSettings()
    s.movers = { ...(s.movers as object), rclone: { enabled: true, path: 'rclone' } }
    s.detected = { ...(s.detected as object), rclone }
    serve({ settings: s, ...(locations ? { locations } : {}) })
  }

  it('shows the version and the remotes with their types, and counts them in the Ready line', async () => {
    const locs = (await import('./transfersMock')).MOCK_LOCATIONS
    withRclone(
      { enabled: true, found: true, path: '/usr/bin/rclone', version: '1.76.0', remotes: [{ name: 'archive', type: 's3' }, { name: 'drive', type: 'drive' }] },
      [...locs, { id: 'rclone:archive', kind: 'rclone', label: 'archive', detail: 'rclone remote, s3', writable: true, indexed: false, browsable: true, available: true },
        { id: 'rclone:drive', kind: 'rclone', label: 'drive', detail: 'rclone remote, drive', writable: true, indexed: false, browsable: true, available: true }],
    )
    render(<TransfersSettingsSection />)
    expect(await screen.findByText(/Found at \/usr\/bin\/rclone, version 1\.76\.0\./)).toBeInTheDocument()
    const table = screen.getByRole('table', { name: 'rclone remotes' })
    expect(within(table).getByText('archive')).toBeInTheDocument()
    expect(within(table).getByText('s3')).toBeInTheDocument()
    const box = screen.getByText('Ready to use').closest('.xfer-ready') as HTMLElement
    await waitFor(() => expect(within(box).getByText(/2 rclone remotes/)).toBeInTheDocument())
    expect(screen.getByText(/never changes them/)).toBeInTheDocument()
  })

  it('says plainly when rclone is missing or has no remotes', async () => {
    withRclone({ enabled: true, found: false, remotes: [], why: 'rclone is not installed on this computer (looked for rclone)' })
    const { unmount } = render(<TransfersSettingsSection />)
    expect(await screen.findByText(/Rclone is not installed on this computer/)).toBeInTheDocument()
    const box = screen.getByText('Ready to use').closest('.xfer-ready') as HTMLElement
    await waitFor(() => expect(within(box).getByText(/rclone not found/)).toBeInTheDocument())
    unmount()
    withRclone({ enabled: true, found: true, version: '1.76.0', remotes: [], why: 'rclone has no remotes configured; add them with rclone config' })
    render(<TransfersSettingsSection />)
    expect(await screen.findByText(/rclone has no remotes\. Add them with/)).toBeInTheDocument()
    const box2 = screen.getByText('Ready to use').closest('.xfer-ready') as HTMLElement
    await waitFor(() => expect(within(box2).getByText(/rclone has no remotes/)).toBeInTheDocument())
  })

  it('saves the program and the switch with Save', async () => {
    withRclone({ enabled: true, found: true, version: '1.76.0', remotes: [] })
    render(<TransfersSettingsSection />)
    const program = (await screen.findAllByLabelText('Program'))[1]
    fireEvent.change(program, { target: { value: '/opt/tools/rclone' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved().length).toBe(1))
    expect((saved()[0].movers as { rclone: unknown }).rclone).toEqual({ enabled: true, path: '/opt/tools/rclone' })
  })
})

describe('rclone in the plain rules', () => {
  it('counts rclone remotes as places, and says why there are none', async () => {
    const { placesReady, readyParts, testText } = await import('../src/transferApi')
    const loc = (id: string, kind: 'rclone' | 'computer') => ({ id, kind, label: id, detail: '', writable: true, indexed: false, browsable: true, available: true })
    const st = mockSettings() as never as Parameters<typeof placesReady>[1]
    const p = placesReady([loc('rclone:a', 'rclone'), loc('rclone:b', 'rclone'), loc('computer:x', 'computer')], st)
    expect(p.total).toBe(3)
    expect(readyParts(p)).toContain('2 rclone remotes')
    const none = placesReady([], { ...st!, detected: { ...st!.detected!, rclone: { enabled: true, found: true, remotes: [] } } })
    expect(readyParts(none)).toContain('rclone has no remotes')
    const off = placesReady([], { ...st!, detected: { ...st!.detected!, rclone: { enabled: false, found: false, remotes: [] } } })
    expect(readyParts(off).join(' ')).not.toContain('rclone')
    expect(testText({ ok: true, rsync: true, rclone: true, rcloneVersion: '1.76.0' })).toContain('rclone 1.76.0 there')
  })
})
