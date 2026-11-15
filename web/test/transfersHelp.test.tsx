import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { resetTransfersProbe } from '../src/transferApi'
import { HelpView } from '../src/views/HelpView'

const HELP = readFileSync(path.resolve(process.cwd(), '../docs/HELP.md'), 'utf8')

function serve(transfers: boolean) {
  resetTransfersProbe()
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url === '/api/help') return new Response(HELP, { status: 200 })
    if (url === '/api/transfers/locations') return new Response(JSON.stringify(transfers ? { locations: [] } : { error: 'not found' }), { status: transfers ? 200 : 404 })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
}

describe('Transfers in Help', () => {
  afterEach(() => { vi.unstubAllGlobals(); resetTransfersProbe(); history.replaceState(null, '', location.pathname) })

  it('has a page beside Library where the server has transfers', async () => {
    serve(true)
    render(<HelpView />)
    const button = await screen.findByRole('button', { name: 'Transfers' })
    const rail = [...document.querySelectorAll('button')].map(b => b.textContent?.trim())
    expect(rail.indexOf('Transfers')).toBe(rail.indexOf('Library') + 1)
    fireEvent.click(button)
    expect(await screen.findByRole('heading', { name: 'Transfers' })).toBeInTheDocument()
    expect(screen.getByText(/Every transfer is confirmed/)).toBeInTheDocument()
  })

  it('leaves the page out on a server without them', async () => {
    serve(false)
    render(<HelpView />)
    await screen.findByRole('button', { name: 'Library' })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Transfers' })).toBeNull())
  })
})
