import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary } from '../src/components/AddLibrary'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

// Phase 4, J: the form ends Settings, Indexing, so a choice that shows a
// shorter form shortened the page and the browser scrolled up to fit,
// moving the choices under the pointer. The form keeps the tallest height
// it had while it is open.
describe('Add a library keeps its height across choices', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('holds the height it had before a Where choice', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async () => json({})))
    const tall = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (this: HTMLElement) {
      return this.classList.contains('lib-add-flow') ? 900 : 0
    })
    try {
      const { container } = render(<AddLibrary options={{ buildHere: true, ssh: false, pw: false, workflow: false, bucket: true }} onAdded={() => {}} />)
      fireEvent.click(await screen.findByRole('button', { name: 'Bucket' }))
      expect(container.querySelector<HTMLElement>('.lib-add-flow')!.style.minHeight).toBe('900px')
    } finally { tall.mockRestore() }
  })
})
