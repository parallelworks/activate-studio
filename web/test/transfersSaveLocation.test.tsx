import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from '@parallelworks/ui'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TransfersView, prefillFor } from '../src/views/TransfersView'
import { ToolApproval } from '../src/components/ToolApproval'
import { TRANSFER_SETTINGS_CHANGED } from '../src/transferApi'
import { MOCK_LOCATIONS, createTransfersMock, mockSettings, type MockLocation, type MockOptions } from './transfersMock'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom lacks.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

// A library whose files Studio cannot reach, with what the server knows of
// them, and one Studio has only the index of.
const PROJECT_X: MockLocation = {
  id: 'library:project-x', kind: 'library', label: 'Project X', detail: 'files not reachable from Studio',
  writable: false, indexed: false, browsable: false, available: false,
  why: 'files.example.com runs only gufi_query for this login (no shell), so this cannot run there.',
  suggest: { label: 'Project X files', host: 'user@files.example.com', root: '/srv/project-x', port: 2222 },
}
const INDEX_ONLY: MockLocation = {
  id: 'library:index-only', kind: 'library', label: 'Old index', detail: 'files not reachable from Studio',
  writable: false, indexed: false, browsable: false, available: false,
  why: 'Studio has only its index, not its files', suggest: { label: 'Old index files' },
}
// A library read only for transfers, as with sign-in on.
const SIGNED_IN_RO: MockLocation = {
  id: 'library:shared', kind: 'library', label: 'Shared', detail: 'this computer, /data/shared; read only for transfers',
  writable: false, indexed: false, browsable: true, available: true,
  readOnly: 'read only for transfers: its files are reached with the account the Studio runs as, whose permissions are not yours',
  hint: 'With sign-in on, saved locations are off. Your clusters, reached with your own key, can be written.',
}

type Mock = ReturnType<typeof createTransfersMock>
let mock: Mock

function serve(opts: MockOptions = {}): Mock {
  mock = createTransfersMock({ locations: [...MOCK_LOCATIONS, PROJECT_X, INDEX_ONLY], ...opts })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const r = mock.handle(init?.method ?? 'GET', String(input), init?.body ? JSON.parse(String(init.body)) : undefined)
    return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
  }))
  return mock
}

beforeEach(() => {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => {
  vi.unstubAllGlobals()
  history.replaceState(null, '', location.pathname)
})

async function openNew() {
  history.replaceState(null, '', `${location.pathname}#view=transfers:new`)
  render(<TransfersView />)
  await screen.findAllByRole('region', { name: /: files$/ })
  await waitFor(() => expect(mock.calls.some(c => c.path === '/api/transfers/settings')).toBe(true))
}

/** Opens a pane's chooser with layout given a size (its list draws only
 *  the rows that fit, which jsdom measures as zero), runs fn, and closes. */
async function withChooser<T>(paneName: string, fn: (u: ReturnType<typeof userEvent.setup>) => Promise<T>): Promise<T> {
  const rect = Element.prototype.getBoundingClientRect
  const sizes = ['offsetHeight', 'clientHeight', 'offsetWidth', 'clientWidth'] as const
  for (const k of sizes) Object.defineProperty(HTMLElement.prototype, k, { configurable: true, get: () => 400 })
  Element.prototype.getBoundingClientRect = () => ({ x: 0, y: 0, top: 0, left: 0, width: 400, height: 400, right: 400, bottom: 400, toJSON() {} }) as DOMRect
  try {
    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name: `${paneName}: location` }))
    return await fn(u)
  } finally {
    Element.prototype.getBoundingClientRect = rect
    for (const k of sizes) delete (HTMLElement.prototype as unknown as Record<string, unknown>)[k]
  }
}

const pick = (paneName: string, label: string) =>
  withChooser(paneName, async u => u.click(await screen.findByRole('option', { name: new RegExp(`^${label}`) })))

const second = () => screen.getByRole('region', { name: 'Second location' })

describe('saving a location from the Transfers page', () => {
  it('offers Save a location… in the chooser, which opens the Settings dialog empty', async () => {
    serve()
    await openNew()
    // It is the chooser's last row, below what the list draws at once, so
    // it is found by typing, as a person may.
    await withChooser('Second location', async u => {
      await u.clear(screen.getByRole('combobox', { name: 'Second location: location' }))
      await u.keyboard('Save')
      await u.click(await screen.findByRole('option', { name: /^Save a location…/ }))
    })
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    expect(within(dialog).getByLabelText('Host')).toHaveValue('')
    expect(within(dialog).getByLabelText('Folder')).toHaveValue('')
    // Nothing was chosen: the pane keeps its location.
    expect(screen.getByRole('combobox', { name: 'Second location: location', hidden: true })).toHaveValue('Downloads')
  })

  it('says why a library cannot be written and offers to save a location for its files, prefilled', async () => {
    serve()
    await openNew()
    await pick('Second location', 'Project X')
    const callout = await within(second()).findByText('Project X is not reachable')
    const box = callout.closest('[class*="callout"]') as HTMLElement ?? second()
    expect(within(box).getByText(/runs only gufi_query for this login/)).toBeInTheDocument()
    fireEvent.click(within(second()).getByRole('button', { name: 'Save a location for its files…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    expect(within(dialog).getByLabelText('Label')).toHaveValue('Project X files')
    expect(within(dialog).getByLabelText('Host')).toHaveValue('user@files.example.com')
    expect(within(dialog).getByLabelText('Folder')).toHaveValue('/srv/project-x')
    // The port is in Advanced, opened because it is filled.
    expect(within(dialog).getByLabelText('Port')).toHaveValue('2222')
    expect(within(dialog).getByText('Filled in with what Studio knows of where the files of Project X are.')).toBeInTheDocument()
  })

  it('leaves what Studio does not know empty, and says so', async () => {
    serve()
    await openNew()
    await pick('Second location', 'Old index')
    fireEvent.click(await within(second()).findByRole('button', { name: 'Save a location for its files…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    expect(within(dialog).getByLabelText('Label')).toHaveValue('Old index files')
    expect(within(dialog).getByLabelText('Host')).toHaveValue('')
    expect(within(dialog).getByLabelText('Folder')).toHaveValue('')
    expect(within(dialog).getByText('Studio does not know where the files of Old index are. Enter the host and the folder.')).toBeInTheDocument()
    expect(prefillFor({ ...PROJECT_X, suggest: { label: 'X files', host: 'h' } } as never).note).toMatch(/Enter the folder\.$/)
  })

  it('tests, saves, lists the locations again and switches the pane to the new one', async () => {
    serve()
    await openNew()
    await pick('Second location', 'Project X')
    fireEvent.click(await within(second()).findByRole('button', { name: 'Save a location for its files…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    const before = mock.calls.filter(c => c.path === '/api/transfers/locations').length
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add a saved location' })).toBeNull())
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/test')?.body).toMatchObject({ host: 'user@files.example.com', root: '/srv/project-x', port: 2222 })
    expect(mock.calls.find(c => c.path === '/api/transfers/settings/locations')?.body).toMatchObject({
      label: 'Project X files', host: 'user@files.example.com', root: '/srv/project-x', port: 2222,
    })
    expect(mock.calls.filter(c => c.path === '/api/transfers/locations').length).toBeGreaterThan(before)
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Second location: location' })).toHaveValue('Project X files'))
    expect(within(second()).getByText('user@files.example.com, /srv/project-x')).toBeInTheDocument()
    expect(within(second()).queryByText('Project X is not reachable')).toBeNull()
  })

  it('says what the test found in the toast, in Settings\' words, and tells Settings to read again', async () => {
    serve({ testReply: { ok: true, sftpOnly: true, rsync: false, writable: true, freeBytes: 2 * 1024 ** 4, roundTripMs: 40 } })
    const success = vi.spyOn(toast, 'success')
    const heard = vi.fn()
    window.addEventListener(TRANSFER_SETTINGS_CHANGED, heard)
    try {
      await openNew()
      await pick('Second location', 'Project X')
      fireEvent.click(await within(second()).findByRole('button', { name: 'Save a location for its files…' }))
      const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
      await waitFor(() => expect(success).toHaveBeenCalled())
      expect(success).toHaveBeenLastCalledWith('Saved Project X files. Reached over SFTP (the account takes SFTP only), folder exists, rsync not available, Studio copy will be used, writable, 2 TB free, 40 ms round trip. It is also in Settings, Transfers.')
      expect(heard).toHaveBeenCalledTimes(1)
    } finally {
      window.removeEventListener(TRANSFER_SETTINGS_CHANGED, heard)
      success.mockRestore()
    }
  })

  it('says in the toast that a location saved anyway failed its test', async () => {
    serve({ testReply: { ok: false, why: 'Permission denied (publickey)' } })
    const success = vi.spyOn(toast, 'success')
    try {
      await openNew()
      await pick('Second location', 'Project X')
      fireEvent.click(await within(second()).findByRole('button', { name: 'Save a location for its files…' }))
      const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
      fireEvent.click(await within(dialog).findByRole('button', { name: 'Save anyway' }))
      await waitFor(() => expect(success).toHaveBeenLastCalledWith('Saved Project X files. Test failed: Permission denied (publickey). It is also in Settings, Transfers.'))
    } finally { success.mockRestore() }
  })

  it('saves nothing when the test fails, until asked to save anyway', async () => {
    serve({ testReply: { ok: false, why: 'ssh: connect to host files.example.com port 2222: Connection refused' } })
    await openNew()
    await pick('Second location', 'Project X')
    fireEvent.click(await within(second()).findByRole('button', { name: 'Save a location for its files…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a saved location' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and save' }))
    expect(await within(dialog).findByText(/Connection refused/)).toBeInTheDocument()
    expect(mock.calls.some(c => c.path === '/api/transfers/settings/locations')).toBe(false)
  })
})

describe('with sign-in on', () => {
  it('offers no saved location, and says what can be written instead', async () => {
    serve({ settings: { ...mockSettings(), managed: true }, locations: [...MOCK_LOCATIONS, SIGNED_IN_RO] })
    await openNew()
    const options = await withChooser('Second location', async () => {
      await screen.findByRole('option', { name: /^Shared/ })
      return screen.getAllByRole('option').map(o => o.textContent ?? '')
    })
    expect(options.some(o => o.startsWith('Save a location'))).toBe(false)
    await pick('Second location', 'Shared')
    expect(await within(second()).findByText('Shared is read only for transfers')).toBeInTheDocument()
    expect(within(second()).getByText(/Your clusters, reached with your own key, can be written/)).toBeInTheDocument()
    expect(within(second()).queryByRole('button', { name: /Save a location/ })).toBeNull()
  })
})

describe('the approval card of a held save_transfer_location call', () => {
  it('shows the label, host and folder above the allow and deny buttons', () => {
    const onAnswer = vi.fn()
    const part = {
      kind: 'approval' as const, id: 'call_1', approvalKind: 'confirm' as const, toolName: 'save_transfer_location', subject: 'call' as const,
      header: 'Save the Transfers location Project X files?',
      reason: 'Studio tests the connection first, as Settings, Transfers does, and saves the location only when the test passes.',
      canAllowDirectory: false, expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(),
      savedLocation: { label: 'Project X files', host: 'user@files.example.com', root: '/srv/project-x', port: 2222 },
    }
    render(<ToolApproval part={part} message={{ id: 'srv-1', role: 'assistant', content: null, parentId: 'u1', parts: [] }} onApprovalAnswer={onAnswer} />)
    const card = within(screen.getByRole('region', { name: 'Location to save' }))
    expect(card.getByText('Project X files')).toBeInTheDocument()
    expect(card.getByText('user@files.example.com')).toBeInTheDocument()
    expect(card.getByText('/srv/project-x')).toBeInTheDocument()
    expect(card.getByText('2222')).toBeInTheDocument()
    expect(screen.getByText('Save the Transfers location Project X files?')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Yes, allow this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: true })
    fireEvent.click(screen.getByRole('button', { name: 'No, deny this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: false })
  })
})
