import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TagMenu } from '../src/components/TagMenu'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

/** Two files, of which only the first carries `alpha`. */
function serve(posts: unknown[]) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST') {
      const body = JSON.parse(String(init.body))
      posts.push(body)
      return json({ updated: Object.fromEntries(body.paths.map((p: string) => [p, body.add])), stored: 'files' })
    }
    if (url.startsWith('/api/kb/tags')) {
      return json({ own: decodeURIComponent(url).endsWith('one.txt') ? ['alpha'] : [], inherited: [], stored: 'files' })
    }
    if (url.startsWith('/api/tags')) return json({ tags: [{ tag: 'alpha', count: 1 }, { tag: 'beta', count: 3 }] })
    return json({})
  }))
}

const open = (paths: string[]) => render(<TagMenu paths={paths} onClose={() => {}} />)

afterEach(() => vi.unstubAllGlobals())

describe('the labels panel with several items', () => {
  it('offers a label only some of them carry, and adds it to all of them', async () => {
    const posts: unknown[] = []
    serve(posts)
    const heard = vi.fn()
    window.addEventListener('ade-tags-changed', heard)
    open(['a/one.txt', 'a/two.txt'])

    const rest = await screen.findByRole('button', { name: /alpha\s+1 of 2/ })
    expect(screen.getByText('On some of these items')).toBeInTheDocument()
    fireEvent.click(rest)

    await waitFor(() => expect(posts).toEqual([{ paths: ['a/one.txt', 'a/two.txt'], add: ['alpha'], remove: [] }]))
    await waitFor(() => expect(screen.queryByText('On some of these items')).not.toBeInTheDocument())
    expect(screen.getByText('alpha')).toBeInTheDocument()
    expect(heard).toHaveBeenCalledTimes(1)
    window.removeEventListener('ade-tags-changed', heard)
  })

  it('removes a partly applied label from all of them', async () => {
    const posts: unknown[] = []
    serve(posts)
    open(['a/one.txt', 'a/two.txt'])
    fireEvent.click(await screen.findByRole('button', { name: 'Remove label from all of them' }))
    await waitFor(() => expect(posts).toEqual([{ paths: ['a/one.txt', 'a/two.txt'], add: [], remove: ['alpha'] }]))
  })

  it('shows one item\'s label as on it, with no partial row', async () => {
    serve([])
    open(['a/one.txt'])
    expect(await screen.findByRole('button', { name: 'Remove label' })).toBeInTheDocument()
    expect(screen.queryByText('On some of these items')).not.toBeInTheDocument()
  })

  it('does not claim a label is on everything when more are selected than it reads', async () => {
    serve([])
    open([...Array.from({ length: 29 }, (_, i) => `a/f${i}.txt`), 'a/one.txt', 'a/last.txt'])
    const rest = await screen.findByRole('button', { name: /^alpha$/ })
    expect(rest).toHaveAttribute('title', 'Add this label to all the selected items')
  })
})
