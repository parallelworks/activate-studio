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
    expect(screen.getByText(/Each result says how it matched: name, words, or meaning/)).toBeInTheDocument()
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

describe('the main folder\'s level, in setup', () => {
  it('shows the level in force and saves another, with what it does', async () => {
    const { vi } = await import('vitest')
    const { MainFolderMeaning } = await import('../src/components/MeaningLevel')
    const { waitFor } = await import('@testing-library/react')
    const calls: { method: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const settings = { threads: null, documents: true, pdfReader: 'auto', ocr: true, imageDescriptions: true, legacyOffice: true, meaning: 'documents' }
      return new Response(JSON.stringify(String(input) === '/api/index/settings' && init?.method === 'PUT'
        ? { settings: { ...settings, ...(JSON.parse(String(init.body))) }, threads: 4, readers: {} }
        : { settings, defaults: settings, single: true, cpus: 4, threads: 4 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    try {
      render(<MainFolderMeaning />)
      expect(await screen.findByText(/in documents and images: PDF, Office, text, Markdown/)).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Off' }))
      await waitFor(() => expect(calls.some(c => c.method === 'PUT' && (c.body as { meaning: string }).meaning === 'off')).toBe(true))
      expect(screen.getByText(/Nothing more is computed, so indexing is fastest/)).toBeInTheDocument()
    } finally { vi.unstubAllGlobals() }
  })
})
