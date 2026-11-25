import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useCallback, useState } from 'react'
import App, { type Display } from '../src/App'
import { LibraryView } from '../src/views/LibraryView'
import { api, setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetTransfersProbe } from '../src/transferApi'
import { leaveUnsaved, unsavedPath } from '../src/unsaved'

// The pages that are not under test are stand-ins.
const { page } = vi.hoisted(() => ({ page: (name: string) => ({ [name]: () => `${name} page` }) }))
vi.mock('../src/views/ChatView', () => page('ChatView'))
vi.mock('../src/views/SearchView', () => page('SearchView'))
vi.mock('../src/views/QueryView', () => page('QueryView'))
vi.mock('../src/views/OverviewView', () => page('OverviewView'))
vi.mock('../src/views/HistoryView', () => page('HistoryView'))
vi.mock('../src/views/AgentsView', () => page('AgentsView'))
vi.mock('../src/views/WorkflowsView', () => page('WorkflowsView'))
vi.mock('../src/views/SettingsView', () => page('SettingsView'))
vi.mock('../src/views/HelpView', () => page('HelpView'))
vi.mock('../src/components/StatusFooter', () => page('StatusFooter'))
vi.mock('../src/components/RunPane', () => ({ ...page('RunPane'), ...page('FullViewHost') }))

// Make and edit text files in the Library: New file, Edit, Save, and a save
// that never writes over a change made since the file was opened.

const lib = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false }, ...extra,
})
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const WHY = 'On a Studio with sign-in, the main library is read only.'

/** A Studio whose files are these, by library. */
function studio(o: { files?: Record<string, string>; libs?: ReturnType<typeof lib>[]; size?: number } = {}) {
  const files = new Map(Object.entries(o.files ?? { 'kb/notes.md': '# Notes\n', 'kb/other.txt': 'other\n', 'kb/docs/in.md': 'in\n' }))
  const ver = (s: string) => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7).toString(16).padStart(64, '0')
  const state = { files, ver, puts: [] as { path: string; base: string; body: string; library: string }[], jobs: [] as string[], fail: '' }
  let tick = 1
  const libs = o.libs ?? [lib('kb', 'Projects')]
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const u = new URL(url, 'http://x')
    const p = u.searchParams.get('path') ?? ''
    const l = u.searchParams.get('library') ?? 'kb'
    const key = `${l}/${p}`
    const method = init?.method ?? 'GET'
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: libs, sections: ['chat', 'library', 'search', 'settings'], aiEnabled: true })
    if (url.startsWith('/api/setup')) return json({ firstRun: false })
    if (url.startsWith('/api/kb/tree')) {
      const seen = new Map<string, unknown>()
      for (const k of files.keys()) {
        const full = l === 'all' ? k : k.startsWith(`${l}/`) ? k.slice(l.length + 1) : null
        if (full === null || (p && !full.startsWith(`${p}/`))) continue
        const rest = p ? full.slice(p.length + 1) : full
        const name = rest.split('/')[0]
        const path = p ? `${p}/${name}` : name
        seen.set(name, rest.includes('/') ? { name, path, type: 'dir', size: 0, mtime: 0 } : { name, path, type: 'file', size: 10, mtime: 0 })
      }
      return json({ path: p, entries: [...seen.values()] })
    }
    if (url.startsWith('/api/kb/text')) {
      if (method === 'GET') {
        if (state.fail) return json({ error: state.fail }, 415)
        const c = files.get(key)
        return c === undefined ? json({ error: 'no such file' }, 404) : json({ path: p, content: c, version: ver(c), size: c.length })
      }
      const base = u.searchParams.get('base') ?? ''
      const body = String(init?.body ?? '')
      state.puts.push({ path: p, base, body, library: l })
      if (state.fail) return json({ error: state.fail }, 500)
      const c = files.get(key)
      if (base === 'none' ? c !== undefined : c === undefined) return json({ error: 'taken', conflict: base === 'none' ? 'exists' : 'missing' }, 409)
      if (base !== 'none' && ver(c!) !== base) return json({ error: 'changed', conflict: 'changed', version: ver(c!) }, 409)
      files.set(key, body)
      tick++
      return json({ path: p, version: ver(body), size: body.length, indexed: false, deferred: true, ...(l.startsWith('u-') ? { reindexing: true } : {}) })
    }
    if (url.startsWith('/api/kb/rename')) {
      const b = JSON.parse(String(init!.body))
      return json({ renamed: { from: b.path, to: b.name }, indexMs: 0 })
    }
    if (url.startsWith('/api/kb/file')) {
      const at = l === 'all' ? p : key
      const c = files.get(at)
      if (c === undefined) return json({ error: 'no such file' }, 404)
      return json({ path: p, kind: 'text', source: 'raw', content: c, size: o.size ?? c.length, mtime: tick, truncated: false })
    }
    if (url.startsWith('/api/index/job')) {
      if (method === 'POST') { state.jobs.push(String(JSON.parse(String(init!.body)).path)); return json({ id: 1, state: 'running' }) }
      return json({ id: 1, state: 'done', ms: 800 })
    }
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (url.startsWith('/api/transfers')) return json({ error: 'no' }, 404)
    if (url.startsWith('/api/user-libraries')) return json({ libraries: [], available: false })
    return json({})
  }))
  return state
}

beforeEach(() => {
  resetTransfersProbe()
  localStorage.clear()
  sessionStorage.clear()
  history.replaceState(null, '', '/')
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Element.prototype.scrollIntoView = () => {}
  setLibrary('kb')
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

const settle = () => act(async () => { await new Promise(r => setTimeout(r, 25)) })
const configLoaded = async () => { await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 25)) }) }

/** The Library as the app shows it: what is open changes only once an
 *  unsaved edit is settled. */
function Shown({ first }: { first: Display | null }) {
  const [display, setDisplay] = useState(first)
  const show = useCallback((d: Display | null) => leaveUnsaved(() => setDisplay(d)), [])
  return <LibraryView display={display} onDisplay={show} />
}
async function open(path: string | null = 'notes.md') {
  render(<Shown first={path ? { kind: 'file', target: path } : null} />)
  await configLoaded()
}
const area = () => screen.getByRole('textbox', { name: /^Text of / }) as HTMLTextAreaElement
const editor = () => screen.queryByRole('textbox', { name: /^Text of / })
const button = (name: string | RegExp) => screen.getByRole('button', { name })
const edit = async () => { fireEvent.click(await screen.findByRole('button', { name: 'Edit' })); return await screen.findByRole('textbox', { name: /^Text of / }) as HTMLTextAreaElement }
const type = (text: string) => fireEvent.change(area(), { target: { value: text } })
const row = (name: string) => document.querySelector<HTMLElement>(`.tree-row[data-path="${name}"]`)!
const menu = async (path: string, item: RegExp) => {
  fireEvent.contextMenu(row(path))
  return await screen.findByRole('menuitem', { name: item })
}

describe('editing a text file', () => {
  it('opens its text in a plain field and saves it, with the rendered view back after', async () => {
    const s = studio()
    await open()
    const field = await edit()
    expect(field).toHaveValue('# Notes\n')
    expect(field).toHaveFocus()
    expect(button('Save')).toBeDisabled()
    type('# Plan\n')
    expect(screen.getByText('Not saved')).toBeInTheDocument()
    fireEvent.click(button('Save'))
    expect(await screen.findByRole('heading', { name: 'Plan' })).toBeInTheDocument()
    expect(editor()).toBeNull()
    expect(s.puts).toEqual([{ path: 'notes.md', base: s.ver('# Notes\n'), body: '# Plan\n', library: 'kb' }])
    // Indexed as an upload is, and the viewer says so.
    expect(s.jobs).toEqual([''])
    expect(await screen.findByText(/^Saved, indexed in 0\.8s/, {}, { timeout: 3000 })).toBeInTheDocument()
  })

  it('saves on Ctrl+S and Cmd+S and stays in the editor, over its own last save', async () => {
    const s = studio()
    await open()
    await edit()
    for (const [text, key] of [['one', { ctrlKey: true }], ['two', { metaKey: true }]] as const) {
      type(text)
      fireEvent.keyDown(area(), { key: 's', ...key })
      await waitFor(() => expect(s.files.get('kb/notes.md')).toBe(text))
      await waitFor(() => expect(screen.queryByText('Not saved')).toBeNull())
      expect(area()).toHaveValue(text)
    }
    expect(s.puts.map(p => p.base)).toEqual([s.ver('# Notes\n'), s.ver('one')])
    fireEvent.click(button('Close'))
    expect(editor()).toBeNull()
  })

  it('keeps CRLF line endings and a byte order mark', async () => {
    const s = studio({ files: { 'kb/dos.txt': '﻿one\r\ntwo\r\n' } })
    await open('dos.txt')
    expect(await edit()).toHaveValue('one\ntwo\n')
    type('one\nthree\n')
    fireEvent.click(button('Save'))
    await waitFor(() => expect(s.files.get('kb/dos.txt')).toBe('﻿one\r\nthree\r\n'))
  })

  it('says why a file cannot be edited', async () => {
    const s = studio({ files: { 'kb/mixed.txt': 'a\r\nb\n' } })
    await open('mixed.txt')
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(await screen.findByText(/end in more than one way/)).toBeInTheDocument()
    s.fail = 'mixed.txt is not UTF-8 text, so it cannot be edited here'
    fireEvent.click(button('Edit'))
    expect(await screen.findByText(s.fail)).toBeInTheDocument()
    expect(editor()).toBeNull()
  })

  it('keeps what was typed when a save fails', async () => {
    const s = studio()
    await open()
    await edit()
    type('mine')
    s.fail = 'cluster-a: connection refused'
    fireEvent.click(button('Save'))
    expect(await screen.findByText(s.fail)).toBeInTheDocument()
    expect(area()).toHaveValue('mine')
    s.fail = ''
    fireEvent.click(button('Save'))
    await waitFor(() => expect(s.files.get('kb/notes.md')).toBe('mine'))
  })

  it('is not disturbed by a change to the library or the file going', async () => {
    const s = studio()
    await open()
    await edit()
    type('mine')
    s.files.delete('kb/notes.md')
    window.dispatchEvent(new Event('ade-kb-changed'))
    window.dispatchEvent(new Event('focus'))
    await settle()
    expect(area()).toHaveValue('mine')
  })

  it('is disabled with the reason in a read only library and over 1 MB', async () => {
    studio({ libs: [lib('kb', 'Projects', { writable: false, readOnlyWhy: WHY })] })
    await open()
    expect(await screen.findByRole('button', { name: 'Edit' })).toHaveAttribute('aria-description', WHY)
    expect(button('Edit')).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(button('Edit'))
    await settle()
    expect(editor()).toBeNull()
    cleanup()
    studio({ size: 1024 * 1024 + 1 })
    await open()
    expect((await screen.findByRole('button', { name: 'Edit' })).getAttribute('aria-description')).toMatch(/larger than 1 MB/)
  })

  it('is not offered for text read out of another kind of file', async () => {
    studio()
    const real = api.file
    vi.spyOn(api, 'file').mockImplementation(async p => ({ ...(await real(p)), source: 'extracted' as const }))
    await open()
    await screen.findByRole('button', { name: 'Find' })
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
  })
})

describe('a save on top of a change made since', () => {
  const conflicted = async () => {
    const s = studio()
    await open()
    await edit()
    type('mine')
    // Of the same size, within the same second, and a heading of the same length.
    s.files.set('kb/notes.md', '# Other\n')
    fireEvent.click(button('Save'))
    const dialog = await screen.findByRole('alertdialog', { name: 'This file changed after you opened it' })
    expect(s.files.get('kb/notes.md')).toBe('# Other\n')
    return { s, dialog }
  }

  it('keeps theirs', async () => {
    const { s, dialog } = await conflicted()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep theirs' }))
    await waitFor(() => expect(editor()).toBeNull())
    expect(await screen.findByRole('heading', { name: 'Other' })).toBeInTheDocument()
    expect(s.files.get('kb/notes.md')).toBe('# Other\n')
    expect(unsavedPath()).toBeNull()
  })

  it('saves mine as a copy, under the next free name, and opens it', async () => {
    const { s, dialog } = await conflicted()
    s.files.set('kb/notes (copy).md', 'taken')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save mine as a copy' }))
    await waitFor(() => expect(s.files.get('kb/notes (copy 2).md')).toBe('mine'))
    expect(await screen.findByText('mine')).toBeInTheDocument()
    expect(s.files.get('kb/notes.md')).toBe('# Other\n')
    expect(s.files.get('kb/notes (copy).md')).toBe('taken')
    expect(editor()).toBeNull()
  })

  it('overwrites only the version it was told of', async () => {
    const { s, dialog } = await conflicted()
    s.files.set('kb/notes.md', 'theirs again')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Overwrite' }))
    await waitFor(() => expect(s.puts).toHaveLength(2))
    await settle()
    expect(s.files.get('kb/notes.md')).toBe('theirs again')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Overwrite' }))
    await waitFor(() => expect(s.files.get('kb/notes.md')).toBe('mine'))
    await waitFor(() => expect(editor()).toBeNull())
  })

  it('keeps the text when the person goes back to editing', async () => {
    const { s, dialog } = await conflicted()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(area()).toHaveValue('mine')
    expect(unsavedPath()).toBe('notes.md')
    expect(s.files.get('kb/notes.md')).toBe('# Other\n')
  })

  it('makes a file that has gone again only when asked, and not over a new one', async () => {
    const s = studio()
    await open()
    await edit()
    type('mine')
    s.files.delete('kb/notes.md')
    fireEvent.click(button('Save'))
    const dialog = await screen.findByRole('alertdialog', { name: /moved or deleted/ })
    expect(s.files.has('kb/notes.md')).toBe(false)
    s.files.set('kb/notes.md', 'new one')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Make it again' }))
    expect(await screen.findByRole('alertdialog', { name: 'This file changed after you opened it' })).toBeInTheDocument()
    expect(s.files.get('kb/notes.md')).toBe('new one')
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Overwrite' }))
    await waitFor(() => expect(s.files.get('kb/notes.md')).toBe('mine'))
  })
})

describe('leaving an edit that is not saved', () => {
  const dirty = async () => {
    const s = studio()
    await open()
    await edit()
    type('mine')
    return s
  }
  const asked = () => screen.findByRole('alertdialog', { name: 'Save your changes?' })

  it('asks before another file opens, and stays, saves or drops as told', async () => {
    const s = await dirty()
    fireEvent.click(row('other.txt'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(area()).toHaveValue('mine')
    fireEvent.click(row('other.txt'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('other')).toBeInTheDocument()
    expect(s.files.get('kb/notes.md')).toBe('mine')
    expect(editor()).toBeNull()
    // Dropped, this time.
    await edit()
    type('nope')
    fireEvent.click(row('notes.md'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Discard' }))
    expect(await screen.findByText('mine')).toBeInTheDocument()
    expect(s.files.get('kb/other.txt')).toBe('other\n')
  })

  it('goes on to the other file once a conflict is settled', async () => {
    const s = await dirty()
    s.files.set('kb/notes.md', 'theirs')
    fireEvent.click(row('other.txt'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Save' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'This file changed after you opened it' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Overwrite' }))
    expect(await screen.findByText('other')).toBeInTheDocument()
    expect(s.files.get('kb/notes.md')).toBe('mine')
  })

  it('asks on Cancel, a rename and a delete of the file, and a tab closing', async () => {
    const s = await dirty()
    fireEvent.click(button('Cancel'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Keep editing' }))
    for (const item of [/^Rename/, /^Delete/]) {
      fireEvent.click(await menu('notes.md', item))
      fireEvent.click(within(await asked()).getByRole('button', { name: 'Keep editing' }))
      await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
      expect(screen.queryByRole('dialog')).toBeNull()
    }
    const e = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
    fireEvent.click(button('Cancel'))
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(editor()).toBeNull())
    expect(s.files.get('kb/notes.md')).toBe('# Notes\n')
    const after = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(after)
    expect(after.defaultPrevented).toBe(false)
  })

  it('does not ask when nothing changed, or for another file\'s rename', async () => {
    studio()
    await open()
    await edit()
    fireEvent.click(row('other.txt'))
    expect(await screen.findByText('other')).toBeInTheDocument()
    await edit()
    type('mine')
    fireEvent.click(await menu('notes.md', /^Rename/))
    expect(await screen.findByRole('dialog', { name: 'Rename' })).toBeInTheDocument()
  })

  it('asks before the library is switched', async () => {
    const s = studio({ libs: [lib('kb', 'Projects'), lib('u-mine', 'My library', { mine: true })], files: { 'kb/notes.md': 'n', 'u-mine/a.md': 'a' } })
    await open()
    await edit()
    type('mine')
    const { openInLibrary } = await import('../src/scope')
    const { openWritten } = await import('../src/components/NextUp')
    const { currentLibrary } = await import('../src/api')
    const went = vi.fn()
    act(() => openInLibrary(went, 'u-mine', 'a.md'))
    expect(await asked()).toBeInTheDocument()
    expect(currentLibrary()).toBe('kb')
    expect(went).not.toHaveBeenCalled()
    act(() => openWritten({ lib: 'u-mine', path: 'a.md' } as never))
    expect(currentLibrary()).toBe('kb')
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name: 'Library' }))
    await u.click(await screen.findByRole('option', { name: /My library/ }))
    expect(currentLibrary()).toBe('kb')
    fireEvent.click(within(await asked()).getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(currentLibrary()).toBe('u-mine'))
    expect(s.files.get('kb/notes.md')).toBe('n')
  })
})

describe('leaving an edit through the address', () => {
  const started = async () => {
    const s = studio()
    history.replaceState(null, '', '/#open=file:notes.md')
    render(<App />)
    await configLoaded()
    await edit()
    type('mine')
    return s
  }
  const goTo = async (hash: string) => {
    const oldURL = location.href
    history.pushState(null, '', `/${hash}`)
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: location.href })) })
  }

  it('asks on Back to another file and stays at the file\'s address meanwhile', async () => {
    const s = await started()
    await goTo('#open=file:other.txt')
    const dialog = await screen.findByRole('alertdialog', { name: 'Save your changes?' })
    expect(location.hash).toBe('#open=file:notes.md')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(area()).toHaveValue('mine')
    await goTo('#open=file:other.txt')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('other')).toBeInTheDocument()
    expect(location.hash).toBe('#open=file:other.txt')
    expect(s.files.get('kb/notes.md')).toBe('mine')
  })

  it('asks on the Library\'s own address and its menu entry, and keeps the edit behind another page', async () => {
    const s = await started()
    await goTo('#view=library')
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Keep editing' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await goTo('#view=settings')
    expect(screen.queryByRole('alertdialog')).toBeNull()
    // Ctrl+S on another page is not the hidden editor's.
    fireEvent.keyDown(document.body, { key: 's', ctrlKey: true })
    expect(s.puts).toHaveLength(0)
    fireEvent.click(screen.getByTitle('Library'))
    await settle()
    expect(area()).toHaveValue('mine')
    fireEvent.click(screen.getByTitle('Library'))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(editor()).toBeNull())
    expect(screen.getByText('Nothing open')).toBeInTheDocument()
  })
})

describe('New file', () => {
  it('is beside New folder, takes a name with Untitled.md selected to the dot, and opens ready to type', async () => {
    const s = studio()
    await open(null)
    fireEvent.contextMenu(row('docs'))
    const items = (await screen.findAllByRole('menuitem')).map(i => i.textContent)
    expect(items.indexOf('New file…')).toBe(items.indexOf('New folder') + 1)
    fireEvent.click(screen.getByRole('menuitem', { name: 'New file…' }))
    const dialog = await screen.findByRole('dialog', { name: 'New file' })
    expect(dialog).toHaveTextContent('In docs')
    const name = within(dialog).getByRole('textbox', { name: 'File name' }) as HTMLInputElement
    fireEvent.focus(name)
    expect(name).toHaveValue('Untitled.md')
    expect(name.value.slice(name.selectionStart!, name.selectionEnd!)).toBe('Untitled')
    fireEvent.change(name, { target: { value: 'in.md' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await within(dialog).findByText('There is already something named in.md in this folder. Choose another name.')).toBeInTheDocument()
    fireEvent.change(name, { target: { value: 'plan.txt' } })
    fireEvent.keyDown(name, { key: 'Enter' })
    expect(await screen.findByRole('textbox', { name: 'Text of docs/plan.txt' })).toHaveValue('')
    await waitFor(() => expect(area()).toHaveFocus())
    expect(s.files.get('kb/docs/plan.txt')).toBe('')
    expect(s.files.get('kb/docs/in.md')).toBe('in\n')
    expect(s.jobs).toEqual(['docs'])
    type('hello')
    fireEvent.click(button('Save'))
    await waitFor(() => expect(s.files.get('kb/docs/plan.txt')).toBe('hello'))
    // Opened again later, it shows as any file does.
    await waitFor(() => expect(editor()).toBeNull())
  })

  it('is in + Add, for the folder that is open', async () => {
    const s = studio()
    await open(null)
    fireEvent.click(row('docs'))
    fireEvent.click(button('+ Add'))
    fireEvent.click(await screen.findByRole('button', { name: 'New file…' }))
    const dialog = await screen.findByRole('dialog', { name: 'New file' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await screen.findByRole('textbox', { name: 'Text of docs/Untitled.md' })).toBeInTheDocument()
    expect(s.files.has('kb/docs/Untitled.md')).toBe(true)
  })

  it('goes to the library its folder is in, with All libraries shown', async () => {
    const s = studio({ libs: [lib('kb', 'Projects'), lib('u-mine', 'My library', { space: true })], files: { 'kb/a.md': 'a', 'u-mine/docs/b.md': 'b' } })
    setLibrary('all')
    await open(null)
    fireEvent.contextMenu(document.querySelector('.explorer')!)
    await screen.findAllByRole('menuitem')
    expect(screen.queryByRole('menuitem', { name: 'New file…' })).toBeNull()
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('menuitem')).toBeNull())
    fireEvent.click(await menu('u-mine', /^New file/))
    const dialog = await screen.findByRole('dialog', { name: 'New file' })
    expect(dialog).toHaveTextContent('In My library')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }))
    await screen.findByRole('textbox', { name: /^Text of/ })
    expect(s.puts).toEqual([{ path: 'Untitled.md', base: 'none', body: '', library: 'u-mine' }])
    // Its index follows by itself: no pass of the main library's is asked for.
    expect(s.jobs).toEqual([])
    expect(screen.getByText(/search finds it once the library's index catches up/)).toBeInTheDocument()
  })

  it('is disabled with the reason where the library is read only', async () => {
    studio({ libs: [lib('kb', 'Projects', { writable: false, readOnlyWhy: WHY })] })
    await open(null)
    const item = await menu('docs', /^New file/)
    expect(item).toHaveAttribute('aria-disabled', 'true')
    expect(document.body.textContent).toContain(WHY)
  })
})
