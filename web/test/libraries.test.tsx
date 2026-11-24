import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, uploadAnswer, type UploadError, currentLibrary, setLibrary, settleLibraries, withLibrary } from '../src/api'
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

  it('renders a switcher only when there is a choice, marks read-only ones, and switches', async () => {
    const { unmount } = render(<LibrarySwitch libraries={[libs[0]]} />)
    expect(screen.queryByRole('combobox')).toBeNull()
    unmount()
    setLibrary('kb')
    render(<LibrarySwitch libraries={libs} />)
    const trigger = screen.getByRole('combobox', { name: 'Library' })
    expect(trigger).toHaveTextContent('Knowledge base')
    fireEvent.click(trigger)
    const options = await screen.findAllByRole('option')
    expect(options.length).toBe(3)
    expect(options[1]).toHaveTextContent('Read only')
    expect(options[2]).toHaveTextContent('All libraries')
    expect(libraryWritable(libs, 'all')).toBe(false)
    // A mouse click commits only when the press started on the option.
    fireEvent.pointerDown(options[1])
    fireEvent.click(options[1])
    await waitFor(() => expect(currentLibrary()).toBe('scratch'))
    expect(trigger).toHaveTextContent('Scratch · Read only')
    // The access is its own line under the name, so a narrow pane cuts neither.
    expect(trigger.querySelector('.library-switch-access')).toHaveTextContent('Read only')
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

describe('where an upload goes', () => {
  afterEach(() => { setLibrary('kb'); sessionStorage.clear() })

  it('is settled when the files are added, not when they are sent', () => {
    setLibrary('kb')
    const main = api.uploadTarget('notes')
    setLibrary('all')
    const all = api.uploadTarget('kb/notes')
    const top = api.uploadTarget('mine')
    setLibrary('mine')
    const mine = api.uploadTarget('a b')
    expect([main.query, main.index]).toEqual(['?dir=notes', { path: 'notes' }])
    expect([all.query, all.index, all.back('notes/x')]).toEqual(['?dir=notes', { path: 'notes' }, 'kb/notes/x'])
    expect([top.query, top.index]).toEqual(['?dir=&library=mine', { path: '', library: 'mine' }])
    expect([mine.query, mine.index, mine.back('x')]).toEqual(['?dir=a%20b&library=mine', { path: 'a b', library: 'mine' }, 'x'])
  })
})

describe('what a part answered', () => {
  const status = (code: number, text: string): unknown => {
    try { return uploadAnswer(code, text) } catch (e) { return [(e as UploadError).status, (e as Error).message] }
  }
  it('is read from its body where the answer began before it was written', () => {
    expect(status(200, '{"part":0,"left":1}')).toEqual({ part: 0, left: 1 })
    expect(status(200, '   {"part":0,"left":1}')).toEqual({ part: 0, left: 1 })
    expect(status(200, '  {"error":"there is no space left there","status":507}')).toEqual([507, 'there is no space left there'])
    expect(status(200, '  {"error":"the part did not arrive whole","status":503}')).toEqual([503, 'the part did not arrive whole'])
  })
  it('is not taken for an answer when it is cut short or a page', () => {
    expect(status(200, '   ')).toEqual([502, 'upload: 200'])
    expect(status(200, '<html>Sign in</html>')).toEqual([502, 'upload: 200'])
    expect(status(403, '{"error":"read only"}')).toEqual([403, 'read only'])
    expect(status(503, 'upstream')).toEqual([503, 'upload: 503'])
  })
})

describe('what a failed file had sent', () => {
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
  it('is asked to be removed until something answers', async () => {
    vi.useFakeTimers()
    const answers = [null, 503, 200]
    const fetched = vi.fn(async () => {
      const a = answers.shift()
      if (!a) throw new TypeError('Failed to fetch')
      return new Response('{}', { status: a })
    })
    vi.stubGlobal('fetch', fetched)
    await api.dropParts('x')
    expect(fetched).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(fetched).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(fetched).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(600_000)
    expect(fetched).toHaveBeenCalledTimes(3)
  })
})

describe('a part that stops moving', () => {
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })
  it('fails, to be sent again, and is not taken for a cancelled one', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('XMLHttpRequest', class {
      upload: { onprogress?: (e: { loaded: number }) => void } = {}
      onabort?: () => void
      onprogress?: () => void
      open() {}
      setRequestHeader() {}
      abort() { this.onabort?.() }
      send() {
        setTimeout(() => this.upload.onprogress?.({ loaded: 3 }), 100_000)
        setTimeout(() => this.onprogress?.(), 250_000)
      }
    })
    const ended = vi.fn()
    void api.sendPart('x', 0, new Blob(['abc'])).catch(ended)
    await vi.advanceTimersByTimeAsync(429_000)
    expect(ended).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(2_000)
    expect(ended.mock.calls[0][0]).toMatchObject({ status: 0, message: 'nothing was sent for a while' })
  })
})
