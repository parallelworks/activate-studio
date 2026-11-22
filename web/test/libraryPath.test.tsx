import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { shownPath } from '../src/libraryPath'
import { UploadMenu } from '../src/components/UploadMenu'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
const LIBS = [{ id: 'kb', label: 'My library', primary: true, writable: true }, { id: 'u-space-zz', label: 'zz-test-mylib', primary: false, writable: true }]

// Phase 4, L: with All libraries showing, a path starts with its library's
// id, and that id showed where the library's name should (the Add panel's
// destination, the viewer's header, the Delete dialog).
describe('a path in All libraries names its library', () => {
  afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

  it('reads the id as the library\'s name, only in All libraries', () => {
    setLibrary('all')
    expect(shownPath('u-space-zz/uploads/a.md', LIBS)).toBe('zz-test-mylib/uploads/a.md')
    expect(shownPath('u-space-zz', LIBS)).toBe('zz-test-mylib')
    expect(shownPath('unknown/a.md', LIBS)).toBe('unknown/a.md')
    setLibrary('u-space-zz')
    expect(shownPath('kb/a.md', LIBS)).toBe('kb/a.md')
  })

  it('the Add panel names the library and takes the folder inside it', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (u: string) => json(String(u) === '/api/config' ? { appName: 'Studio', kbLabel: 'My library', libraries: LIBS } : {})))
    await reloadAppConfig()
    setLibrary('all')
    const onTargetDir = vi.fn()
    render(<UploadMenu targetDir="u-space-zz/uploads" onTargetDir={onTargetDir} onDone={() => {}} open onOpenChange={() => {}} />)
    const field = await screen.findByLabelText('Destination folder')
    await waitFor(() => expect(field).toHaveValue('uploads'))
    expect(screen.getByText('In zz-test-mylib')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('u-space-zz')
    fireEvent.change(field, { target: { value: 'papers' } })
    expect(onTargetDir).toHaveBeenLastCalledWith('u-space-zz/papers')
  })
})
