import { fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, currentLibrary, setLibrary, settleLibraries, withLibrary } from '../src/api'
import { LibrarySwitch, libraryWritable } from '../src/components/LibrarySwitch'

const libs = [
  { id: 'kb', label: 'Knowledge base', primary: true, writable: true, pinned: true, source: true, caps: { index: true, fullText: true, vectors: true } },
  { id: 'scratch', label: 'Scratch', primary: false, writable: false, pinned: true, source: false, caps: { index: true, fullText: false, vectors: false } },
]

describe('libraries on the client', () => {
  it('sends nothing for the primary and names any other library on every URL', () => {
    setLibrary('kb')
    expect(withLibrary('/api/kb/tree?path=a')).toBe('/api/kb/tree?path=a')
    setLibrary('scratch')
    expect(withLibrary('/api/kb/tree?path=a')).toBe('/api/kb/tree?path=a&library=scratch')
    expect(withLibrary('/api/kb/stats')).toBe('/api/kb/stats?library=scratch')
    setLibrary('kb')
  })

  it('renders a switcher only when there is a choice, marks read-only ones, and switches', () => {
    const { container: one } = render(<LibrarySwitch libraries={[libs[0]]} />)
    expect(one.querySelector('select')).toBeNull()
    const { container } = render(<LibrarySwitch libraries={libs} />)
    const select = container.querySelector('select') as HTMLSelectElement
    expect(select.options.length).toBe(3)
    expect(select.options[1].text).toContain('read only')
    expect(select.options[2].text).toBe('All libraries')
    expect(libraryWritable(libs, 'all')).toBe(false)
    fireEvent.change(select, { target: { value: 'scratch' } })
    expect(currentLibrary()).toBe('scratch')
    expect(libraryWritable(libs, 'scratch')).toBe(false)
    expect(libraryWritable(libs, 'kb')).toBe(true)
    setLibrary('kb')
  })

  it('defaults to All libraries where there are several, and keeps a tab\'s own choice', () => {
    sessionStorage.clear()
    settleLibraries(['kb', 'scratch'])
    expect(currentLibrary()).toBe('all')
    settleLibraries(['kb'])
    expect(currentLibrary()).toBe('kb')
    setLibrary('scratch')
    expect(sessionStorage.getItem('ade-library')).toBe('scratch')
    settleLibraries(['kb', 'scratch'])
    expect(currentLibrary()).toBe('scratch')
    // A choice that no longer exists falls back to the default.
    settleLibraries(['kb', 'other'])
    expect(currentLibrary()).toBe('all')
    sessionStorage.clear()
    setLibrary('kb')
  })
})

describe('All libraries addresses each path\'s own library', () => {
  afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb'); sessionStorage.clear() })

  it('leaves the routes that read All as they are and points the rest at the library', () => {
    setLibrary('all')
    expect(withLibrary('/api/kb/tree?path=kb%2Fa')).toBe('/api/kb/tree?path=kb%2Fa&library=all')
    expect(withLibrary(`/api/kb/raw?path=${encodeURIComponent('kb/a b/c.pdf')}`)).toBe(`/api/kb/raw?path=${encodeURIComponent('a b/c.pdf')}`)
    expect(withLibrary(`/api/kb/pdf?path=${encodeURIComponent('scratch/x.pdf')}`)).toBe(`/api/kb/pdf?path=x.pdf&library=scratch`)
    // A library's own folder is not a file to read.
    expect(withLibrary('/api/kb/raw?path=kb')).toBe('/api/kb/raw?path=kb&library=all')
    // An upload into a library's folder goes to that library.
    expect(withLibrary('/api/kb/upload?dir=kb&index=0')).toBe('/api/kb/upload?dir=&index=0')
  })

  it('writes inside the writable library and gives back paths in the All tree', async () => {
    setLibrary('all')
    const sent: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? '{}'))
      sent.push(body)
      const reply = body.paths ? { deleted: body.paths, skipped: [], indexMs: 0 } : { created: body.path }
      return new Response(JSON.stringify(reply), { status: 200 })
    }))
    expect((await api.mkdir('kb/notes/new')).created).toBe('kb/notes/new')
    expect(sent[0]).toEqual({ path: 'notes/new' })
    expect((await api.deleteFiles(['kb/a.txt', 'kb/b.txt'])).deleted).toEqual(['kb/a.txt', 'kb/b.txt'])
    expect(sent[1]).toEqual({ paths: ['a.txt', 'b.txt'] })
    // Paths in two libraries stay addressed to All, which refuses them.
    await api.deleteFiles(['kb/a.txt', 'scratch/b.txt'])
    expect(sent[2]).toEqual({ paths: ['kb/a.txt', 'scratch/b.txt'], library: 'all' })
  })
})
