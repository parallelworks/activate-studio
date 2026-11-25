import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { reloadAppConfig } from '../src/config'
import { FirstVisitCard } from '../src/components/FirstVisitCard'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function studio(signIn: boolean, user = 'ada') {
  vi.stubGlobal('fetch', vi.fn(async (u: string) => json(String(u) === '/api/config'
    ? { appName: 'Studio', kbLabel: signIn ? 'Shared library' : 'My library', signIn, user: { id: user, username: user },
        libraries: [{ id: 'kb', label: signIn ? 'Shared library' : 'My library', primary: true, writable: !signIn, readOnlyWhy: signIn ? 'Only its administrators change it.' : undefined }] }
    : {})))
}

/** Render, then read the configuration again (it is kept between tests). */
function show() {
  const r = render(<FirstVisitCard />)
  act(() => reloadAppConfig())
  return r
}

// Phase 4: with sign-in on nobody gets the setup walkthrough, so a person's
// first visit names the two libraries they meet, once per person.
describe('the first visit with sign-in', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

  it('names Shared library and My library, once per person', async () => {
    studio(true)
    const { unmount } = show()
    expect(await screen.findByRole('region', { name: 'Welcome to Studio' })).toBeInTheDocument()
    expect(screen.getByText(/is the library everyone here searches\. It is read only for you/)).toBeInTheDocument()
    expect(screen.getByText(/is your own folder on one of your remote computers/)).toBeInTheDocument()
    // Add a library is in the arrow beside + Add, not + Add itself (phase 4, S).
    expect(screen.getByRole('region', { name: 'Welcome to Studio' })).toHaveTextContent('Add a library… under the arrow beside + Add in the Library adds it.')
    fireEvent.click(screen.getByRole('button', { name: 'Got it' }))
    expect(screen.queryByRole('region', { name: 'Welcome to Studio' })).toBeNull()
    unmount()
    show()
    await new Promise(r => setTimeout(r, 50))
    expect(screen.queryByRole('region', { name: 'Welcome to Studio' })).toBeNull()
  })

  it('shows again for another person in the same browser', async () => {
    localStorage.setItem('ade-first-visit:ada', '1')
    studio(true, 'bob')
    show()
    expect(await screen.findByRole('region', { name: 'Welcome to Studio' })).toBeInTheDocument()
  })

  it('is not shown without sign-in, where the walkthrough runs', async () => {
    studio(false)
    show()
    await waitFor(() => expect(fetch).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 50))
    expect(screen.queryByRole('region', { name: 'Welcome to Studio' })).toBeNull()
  })
})
