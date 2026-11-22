import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const list = vi.hoisted(() => [{ id: 1, kind: 'upload', phase: 'done', total: 1, done: 1, bytes: 1, totalBytes: 1, current: '', failed: [], indexMs: 1200, finished: true, dir: 'kb' }])
vi.mock('../src/transfers', () => ({ useTransfers: () => list, dismissTransfer: () => {} }))

import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { TransferPanel } from '../src/components/TransferPanel'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

// Phase 4, U: with All libraries showing, the upload panel named the
// library by its id ("1 of 1 added to kb").
describe('the transfer panel in All libraries', () => {
  afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

  it('names the library the files went to', async () => {
    vi.stubGlobal('fetch', vi.fn(async (u: string) => json(String(u) === '/api/config'
      ? { appName: 'Studio', kbLabel: 'My library', libraries: [{ id: 'kb', label: 'My library', primary: true, writable: true }] } : {})))
    setLibrary('all')
    render(<TransferPanel />)
    act(() => reloadAppConfig())
    expect(await screen.findByText('1 of 1 added to My library, indexed in 1.2s')).toBeInTheDocument()
  })
})
