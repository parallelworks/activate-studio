import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LocalLibraryForm } from '../src/components/LocalLibraryForm'

const SUGGESTIONS = [
  { id: 'documents', label: 'Documents', path: '/Users/u/Documents', note: 'iCloud files download as they are read.', access: 'ok' },
  { id: 'mail', label: 'Apple Mail', path: '/Users/u/Library/Mail', note: 'Your Apple Mail messages.', access: 'full-disk-access' },
]

function serve() {
  const calls: { url: string; body?: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const body = url.startsWith('/api/libraries/suggestions/full-disk-access') ? { opened: true }
      : url.startsWith('/api/libraries/suggestions') ? { suggestions: SUGGESTIONS }
      : url.startsWith('/api/libraries/build/check') ? { ok: true, steps: [{ label: 'GUFI', ok: true, detail: 'found' }], create: [], sourceRoot: '/Users/u/Documents', indexDir: null }
      : {}
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

describe('suggested folders in the form that builds a library here', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('fills the folder and label from a suggestion and checks it at once', async () => {
    const calls = serve()
    render(<LocalLibraryForm mode="build" browse compact />)
    fireEvent.click(await screen.findByRole('button', { name: 'Documents' }))
    await waitFor(() => expect(calls.some(c => c.url.startsWith('/api/libraries/build/check'))).toBe(true))
    const check = calls.find(c => c.url.startsWith('/api/libraries/build/check'))!
    expect(check.body).toMatchObject({ sourceRoot: '/Users/u/Documents', label: 'Documents' })
    expect((screen.getByLabelText(/Folder to index/) as HTMLInputElement).value).toBe('/Users/u/Documents')
    expect(screen.getByText('iCloud files download as they are read.')).toBeTruthy()
    await waitFor(() => expect((screen.getByRole('button', { name: 'Build the index' }) as HTMLButtonElement).disabled).toBe(false))
  })

  it('says when macOS needs Full Disk Access and opens the setting', async () => {
    const calls = serve()
    render(<LocalLibraryForm mode="build" browse compact />)
    fireEvent.click(await screen.findByRole('button', { name: 'Apple Mail' }))
    expect(await screen.findByText('Needs Full Disk Access')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Open System Settings' }))
    await waitFor(() => expect(calls.some(c => c.url === '/api/libraries/suggestions/full-disk-access')).toBe(true))
  })

  it('offers none where folders cannot be browsed, or to connect an index', async () => {
    const calls = serve()
    render(<LocalLibraryForm mode="build" compact />)
    render(<LocalLibraryForm browse compact />)
    await new Promise(r => setTimeout(r, 20))
    expect(calls.some(c => c.url.startsWith('/api/libraries/suggestions'))).toBe(false)
    expect(screen.queryByRole('group', { name: 'Suggested folders' })).toBeNull()
  })
})
