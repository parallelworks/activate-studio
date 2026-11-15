import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TransfersSettingsSection } from '../src/components/TransfersSettingsSection'
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
    expect(within(box).getByText(/rsync 3\.2\.7 found/)).toBeInTheDocument()
    expect(within(table).getByText('Scratch on archive-host')).toBeInTheDocument()
    expect(screen.getByText('Saved locations')).toBeInTheDocument()
    expect(screen.getByText('Folders on this computer')).toBeInTheDocument()
    expect(screen.getAllByText('How files move').length).toBeGreaterThan(0)
    expect(screen.getByRole('switch', { name: 'Use Desktop' })).not.toBeChecked()
    expect(screen.getByRole('switch', { name: 'Use Downloads' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Offer storage-host' })).not.toBeChecked()
    expect(screen.getByText('/home/user/Projects')).toBeInTheDocument()
    expect(screen.getByText('Always on')).toBeInTheDocument()
    expect(screen.getByText(/Found here, version 3\.2\.7/)).toBeInTheDocument()
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
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toEqual({ host: 'user@archive-host', root: '/srv/archive' })
    const body = saved()[0]
    expect(body).not.toHaveProperty('managed')
    expect(body).not.toHaveProperty('mcpServers')
    expect(body).not.toHaveProperty('detected')
    expect(body.savedLocations.at(-1)).toEqual({ id: '', label: 'Archive', host: 'user@archive-host', root: '/srv/archive' })
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
    expect(saved()).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save anyway' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(saved()[0].savedLocations.at(-1)).toMatchObject({ label: 'user@offline-host', host: 'user@offline-host', root: '/data' })
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

  it('adds a folder, switches one off, hides a library host, and saves them with Save', async () => {
    serve()
    render(<TransfersSettingsSection />)
    await screen.findByRole('table')
    fireEvent.change(screen.getByRole('textbox', { name: 'Folder to add' }), { target: { value: '/home/user/Data' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add folder' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Use Downloads' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Offer archive-host' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saved()).toHaveLength(1))
    const body = saved()[0]
    expect(body.computerFolders.find(f => f.label === 'Downloads')!.enabled).toBe(false)
    expect(body.computerFolders.at(-1)).toEqual({ path: '/home/user/Data', label: 'Data', builtIn: false, enabled: true })
    expect(body.hostLocations).toEqual({ enabled: true, hidden: ['storage-host', 'archive-host'] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled())
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
    await waitFor(() => expect(saved()).toHaveLength(1))
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toEqual({ host: 'user@archive-host', root: '/srv/archive', port: 2222, sshOptions: '-o IdentityFile=/home/user/.ssh/k' })
    expect(saved()[0].savedLocations.at(-1)).toEqual({ id: '', label: 'user@archive-host', host: 'user@archive-host', root: '/srv/archive', port: 2222, sshOptions: '-o IdentityFile=/home/user/.ssh/k' })
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
    expect(saved()).toHaveLength(0)
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
