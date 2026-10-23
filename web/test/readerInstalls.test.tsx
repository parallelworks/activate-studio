import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReadersReport } from '../src/api'
import { ReaderInstalls } from '../src/components/IndexingSection'

const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function report(items: ReadersReport['install'] extends infer I ? (I extends { items: infer X } ? X : never) : never, brew: string | null = '/opt/homebrew/bin/brew'): ReadersReport {
  return { extractors: [], formats: [], missing: [], install: { platform: 'darwin', brew, apt: false, items } }
}

const BREW = [
  { name: 'tesseract', adds: 'OCR reads scanned PDFs and text in images.', command: 'brew install tesseract', link: null },
  { name: 'LibreOffice', adds: 'LibreOffice renders previews of Office files.', command: 'brew install --cask libreoffice', link: null },
]

describe('installing what the index can still use', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('gives one line per missing tool, with the command for this machine and a copy button', async () => {
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    render(<ReaderInstalls readers={report(BREW)} single running={false} onReaders={() => {}} onBuild={() => {}} />)
    expect(screen.getByText('OCR reads scanned PDFs and text in images.')).toBeInTheDocument()
    expect(screen.getByText('brew install tesseract')).toBeInTheDocument()
    expect(screen.getByText('LibreOffice renders previews of Office files.')).toBeInTheDocument()
    expect(screen.getByText('brew install --cask libreoffice')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the tesseract install command' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('brew install tesseract'))
    expect(screen.getByRole('button', { name: 'Copy the LibreOffice install command' })).toBeInTheDocument()
  })

  it('without Homebrew, points to its install page instead of a command', () => {
    render(<ReaderInstalls readers={report([{ name: 'tesseract', adds: 'OCR reads scanned PDFs and text in images.', command: null, link: 'https://brew.sh' }], null)}
      single running={false} onReaders={() => {}} onBuild={() => {}} />)
    expect(screen.queryByText(/brew install/)).toBeNull()
    expect(screen.getByRole('link', { name: /Homebrew/ })).toHaveAttribute('href', 'https://brew.sh')
  })

  it('Check again reads the report afresh, says what was found, and offers to update the index now', async () => {
    const fetchMock = vi.fn(async (input: string) => (String(input) === '/api/index/extractors?fresh=1'
      ? reply(report([BREW[1]]))
      : reply({})))
    vi.stubGlobal('fetch', fetchMock)
    const onReaders = vi.fn()
    const onBuild = vi.fn()
    render(<ReaderInstalls readers={report(BREW)} single running={false} onReaders={onReaders} onBuild={onBuild} />)
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByText('Found tesseract. The next index update uses it.')).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(c => c[0] === '/api/index/extractors?fresh=1')).toBe(true)
    expect(onReaders).toHaveBeenCalledWith(expect.objectContaining({ install: expect.objectContaining({ items: [BREW[1]] }) }))
    fireEvent.click(screen.getByRole('button', { name: 'Update the index now' }))
    expect(onBuild).toHaveBeenCalled()
  })

  it('offers no update while one runs, or where the deployment builds the index', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(report([]))))
    render(<ReaderInstalls readers={report(BREW)} single={false} running={false} onReaders={() => {}} onBuild={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    expect(await screen.findByText('Found tesseract and LibreOffice. The next index update uses them.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update the index now' })).toBeNull()
  })
})
