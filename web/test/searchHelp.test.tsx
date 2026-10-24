import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { setLibrary, type PublicLibrary } from '../src/api'
import { ALL_LIBRARIES } from '../src/components/LibrarySwitch'
import { SearchHelp } from '../src/views/SearchView'

const lib = (id: string, label: string, meaning: PublicLibrary['meaning'], vectors = false): PublicLibrary => ({
  id, label, primary: id === 'kb', writable: id === 'kb', pinned: id === 'kb', source: true, caps: { index: true, fullText: true, vectors }, meaning,
})

describe('the Search page says what search matches', () => {
  afterEach(() => { act(() => setLibrary('kb')) })

  it('names, words and meaning where search by meaning is on', () => {
    render(<SearchHelp libraries={[lib('kb', 'Notes', 'documents')]} />)
    expect(screen.getByText(/Matches file names, the words inside files, and their meaning/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'How search works' }))
    expect(screen.getByText(/passages that say the same thing in other words/)).toBeInTheDocument()
    expect(screen.getByText('Notes: documents')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings, Libraries' })).toHaveAttribute('href', '#view=settings:libraries')
    expect(screen.getByText(/Each result says how it matched: name, folder, words, or meaning/)).toBeInTheDocument()
    expect(screen.getByText(/a folder the file is in has the search in its name/)).toBeInTheDocument()
  })

  it('names and words only where it is off', () => {
    render(<SearchHelp libraries={[lib('kb', 'Notes', 'off', true)]} />)
    expect(screen.getByText(/Search by meaning is off here, so use words the files contain/)).toBeInTheDocument()
  })

  it('across libraries, the ones searched by meaning, including an index with vectors of its own', () => {
    act(() => setLibrary(ALL_LIBRARIES))
    render(<SearchHelp libraries={[lib('kb', 'Notes', 'documents'), lib('papers', 'Papers', 'off'), lib('site', 'Site index', null, true), lib('logs', 'Logs', null)]} />)
    expect(screen.getByText(/and also meaning in Notes, Site index\./)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'How search works' }))
    expect(screen.getByText('Site index: its own vectors', { exact: false })).toBeInTheDocument()
    expect(screen.getByText(', Papers: off', { exact: false })).toBeInTheDocument()
  })
})

describe('the main folder\'s depth, in setup', () => {
  const stub = async (extractors: { name: string; available: boolean }[] = []) => {
    const { vi } = await import('vitest')
    const calls: { method: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const settings = { threads: null, depth: 'meaning-documents', documents: true, pdfReader: 'auto', ocr: true, imageDescriptions: true, legacyOffice: true, meaning: 'documents' }
      return new Response(JSON.stringify(String(input) === '/api/index/settings' && init?.method === 'PUT'
        ? { settings: { ...settings, ...(JSON.parse(String(init.body))) }, threads: 4, readers: {} }
        : { settings, defaults: settings, single: true, cpus: 4, threads: 4, readers: { extractors, formats: [], missing: [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    return { calls, restore: () => vi.unstubAllGlobals() }
  }

  it('names every stop, shows the one in force, and saves another with what it adds', async () => {
    const { MainFolderDepth } = await import('../src/components/IndexDepth')
    const { waitFor } = await import('@testing-library/react')
    const { calls, restore } = await stub()
    try {
      render(<MainFolderDepth />)
      expect(await screen.findByText(/Adds search by meaning: a plain question also finds passages/)).toBeInTheDocument()
      const stops = ['Names', 'Text', 'Text + OCR', 'Meaning on documents', 'Meaning on all files']
      for (const name of stops) expect(screen.getByRole('button', { name })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Meaning on documents' })).toHaveAttribute('aria-pressed', 'true')
      expect(screen.getByRole('slider')).toHaveAttribute('aria-valuenow', '3')
      fireEvent.click(screen.getByRole('button', { name: 'Names' }))
      await waitFor(() => expect(calls.some(c => c.method === 'PUT' && (c.body as { depth: string }).depth === 'names')).toBe(true))
      expect(screen.getByText(/nothing is read from inside the files/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Names' })).toHaveAttribute('aria-pressed', 'true')
    } finally { restore() }
  })

  it('says where tesseract is missing, at the stops that read images', async () => {
    const { MainFolderDepth } = await import('../src/components/IndexDepth')
    const { restore } = await stub([{ name: 'tesseract', available: false }])
    try {
      render(<MainFolderDepth />)
      expect(await screen.findByText(/tesseract is not installed here, so images and scans are found by name until it is/)).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Text' }))
      expect(screen.queryByText(/tesseract is not installed here/)).toBeNull()
    } finally { restore() }
  })
})

describe('the depth scale', () => {
  it('moving across stops saves once, where it settles', async () => {
    const { vi } = await import('vitest')
    const { IndexDepthControl } = await import('../src/components/IndexDepth')
    vi.useFakeTimers()
    try {
      const picked: string[] = []
      render(<IndexDepthControl value="names" onChange={d => picked.push(d)} />)
      const slider = screen.getByRole('slider', { name: 'Index depth: Names' })
      fireEvent.keyDown(slider, { key: 'ArrowRight' })
      fireEvent.keyDown(slider, { key: 'ArrowRight' })
      expect(screen.getByText(/Adds text in images and scanned PDF pages/)).toBeInTheDocument()
      expect(picked).toEqual([])
      act(() => { vi.advanceTimersByTime(400) })
      expect(picked).toEqual(['ocr'])
    } finally { vi.useRealTimers() }
  })
})

describe('what the search box and the page say a search can match', () => {
  it('meaning where a library in scope has it, else words, else names', async () => {
    const { searchReach, SEARCH_PLACEHOLDER, SEARCH_NOTE } = await import('../src/searchReach')
    const words = (id: string, label: string, fullText = true): PublicLibrary => ({ ...lib(id, label, null), caps: { index: true, fullText, vectors: false } })
    const kb = lib('kb', 'Notes', 'documents')
    expect(searchReach([kb], 'kb')).toBe('meaning')
    expect(searchReach([lib('kb', 'Notes', 'off')], 'kb')).toBe('words')
    expect(searchReach([kb, words('site', 'Site'), words('names', 'Names only', false)], 'names')).toBe('names')
    expect(searchReach([kb, words('site', 'Site')], 'site')).toBe('words')
    expect(searchReach([kb, words('site', 'Site')], 'all')).toBe('meaning')
    expect(SEARCH_PLACEHOLDER.meaning).toMatch(/^Ask in plain words/)
    expect(SEARCH_PLACEHOLDER.names).toBe('Part of a file or folder name')
    expect(SEARCH_NOTE.words).toBe('Names and words, including Office and PDF content')
  })
})

describe('how a result was found', () => {
  it('names every way, the most exact first, and a folder match has its own label', async () => {
    const { foundBy } = await import('../src/views/SearchView')
    expect(foundBy({ mode: 'fts', kinds: ['fts', 'name', 'vector'] })).toBe('Found by its words, its name and its meaning')
    expect(foundBy({ mode: 'path', kinds: ['path'] })).toBe('Found by a folder it is in')
    expect(foundBy({ mode: 'vector' })).toBe('Found by its meaning')
  })
})
