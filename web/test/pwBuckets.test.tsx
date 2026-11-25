import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { bucketWhere, listingCost } from '../src/api'
import { AddLibrary } from '../src/components/AddLibrary'
import { addChoices, type AddOptions } from '../src/components/addLibraryTarget'
import { addOptions, addedMessage, mayAddLibraries } from '../src/components/LibraryAddDialogs'
import { chooseComputer, closeMenu, computers } from './addLibraryHelpers'

// A checkbox forwards its press to its hidden input as a PointerEvent,
// which jsdom does not have.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

// ACTIVATE buckets in Add a library: listed by name beside the clusters, one
// added with a folder and a name, or several in one go, and the rclone
// route named as the other way.
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const b = (name: string, more: object = {}) => ({ uri: `pw://ada/${name}`, name, namespace: 'ada', label: name, csp: 'aws', region: 'us-east-1', store: `store-${name}`, ready: true, ...more })
const BUCKETS = [b('papers'), b('gov-data', { region: 'us-gov-west-1' }), b('images', { csp: 'google', region: 'us-central1' }),
  b('old', { csp: 'oracle', region: 'us-ashburn-1', why: 'In Oracle Cloud, which the pw CLI gives no token for (unsupported CSP)' })]
const library = (uri: string, path = '', more: object = {}) => ({ id: `lib-${uri.split('/').pop()}`, label: uri.split('/').pop(), bucket: { location: uri, path, every: 1440, csp: 'aws', region: 'us-gov-west-1', ...more } })

interface Calls { method: string; url: string; body?: Record<string, unknown> }
function stub(over: { buckets?: object; libs?: object[]; refuse?: Record<string, string> } = {}) {
  const calls: Calls[] = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined
    calls.push({ method: init?.method ?? 'GET', url, body })
    if (url === '/api/libraries/buckets') return json(over.buckets ?? { available: true, buckets: BUCKETS })
    if (url.startsWith('/api/libraries/buckets/folders')) {
      const path = new URLSearchParams(url.split('?')[1]).get('path')
      return json(path ? { path, parent: '', dirs: [], objects: 12, more: false } : { path: '', parent: null, dirs: ['plans', 'raw'], objects: 2, more: false })
    }
    if (url === '/api/libraries') return json({ libraries: over.libs ?? [], canBuild: true, buckets: true })
    if (url === '/api/libraries/bucket') {
      const uri = String(body?.bucket)
      if (over.refuse?.[uri]) return json({ error: over.refuse[uri] }, 409)
      return json({ library: library(uri, String(body?.path ?? '')) })
    }
    return json({ error: `no route for ${url}` }, 404)
  }))
  return { calls, posted: () => calls.filter(c => c.url === '/api/libraries/bucket').map(c => c.body) }
}
const ONE: AddOptions = { buildHere: true, ssh: false, pw: false, workflow: false, bucket: true, buckets: true }
const SIGNED: AddOptions = { buildHere: false, ssh: false, pw: false, workflow: true, buckets: true }

describe('ACTIVATE buckets in Add a library', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('lists the person\'s buckets by name, and the rclone route as the other way', async () => {
    stub()
    render(<AddLibrary options={ONE} onAdded={() => {}} />)
    await waitFor(async () => { expect((await computers()).map(r => r.textContent).join('|')).toContain('papers'); closeMenu() })
    const rows = await computers()
    expect(rows.map(r => r.textContent)).toEqual(expect.arrayContaining(['This computer', 'papersada · AWS us-east-1', 'gov-dataada · AWS us-gov-west-1',
      'imagesada · Google Cloud us-central1', 'Another bucket (rclone remote)']))
    const old = rows.find(r => r.textContent?.startsWith('old'))!
    expect(old).toHaveTextContent('gives no token')
    expect(old).toHaveAttribute('aria-disabled', 'true')
  })

  it('says why they could not be listed, and when there are none', async () => {
    stub({ buckets: { available: true, buckets: [], why: 'the platform answered 503: not now' } })
    const r = render(<AddLibrary options={ONE} onAdded={() => {}} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('not now') })
    r.unmount()
    stub({ buckets: { available: false, buckets: [], why: 'Sign in with ACTIVATE under Settings, Accounts to choose from your buckets' } })
    const r2 = render(<AddLibrary options={SIGNED} onAdded={() => {}} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('Your bucketsSign in with ACTIVATE') })
    r2.unmount()
    stub({ buckets: { available: true, buckets: [] } })
    render(<AddLibrary options={ONE} onAdded={() => {}} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('No buckets yet') })
  })

  it('adds one, with a folder in it and a name', async () => {
    const s = stub()
    const onAdded = vi.fn()
    render(<AddLibrary options={SIGNED} onAdded={onAdded} />)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Choose a place'))
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('papers') })
    await chooseComputer('papers')
    expect(await screen.findByText(/listed and read with your own ACTIVATE sign-in/)).toBeInTheDocument()
    expect(screen.getByText(/costs about \$0.005 per 1,000 requests/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Choose a folder' }))
    fireEvent.click(await screen.findByRole('button', { name: 'plans/' }))
    expect(await screen.findByText(/No folders in it; 12 objects/)).toBeInTheDocument()
    fireEvent.change(screen.getByRole('textbox', { name: 'Name' }), { target: { value: 'My plans' } })
    fireEvent.click(screen.getByRole('button', { name: 'List and index' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1))
    expect(s.posted()).toEqual([{ bucket: 'pw://ada/papers', path: 'plans', label: 'My plans', every: 1440 }])
    expect(onAdded.mock.calls[0].slice(1)).toEqual([true, true, 1])
  })

  it('adds the third and the fourth without another form', async () => {
    const s = stub()
    const onAdded = vi.fn()
    render(<AddLibrary options={ONE} onAdded={onAdded} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('papers') })
    await chooseComputer('papers')
    const list = await screen.findByRole('list', { name: 'More buckets to add' })
    expect(within(list).getByRole('checkbox', { name: 'Add old' })).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(within(list).getByRole('checkbox', { name: 'Add gov-data' }))
    fireEvent.click(within(list).getByRole('checkbox', { name: 'Add images' }))
    // Whole buckets, under their own names.
    expect(screen.queryByRole('textbox', { name: 'Name' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'List and index 3 buckets' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1))
    expect(s.posted()).toEqual(['papers', 'gov-data', 'images'].map(n => ({ bucket: `pw://ada/${n}`, every: 1440 })))
    expect(onAdded.mock.calls[0][0].label).toBe('3 buckets')
    expect(addedMessage({ label: '3 buckets', bucket: true }, true, true, 3)).toBe('Listing 3 buckets. Their progress shows in the list. Each is searchable by name once its listing is indexed.')
  })

  it('keeps what was added when one is refused, and says which', async () => {
    const s = stub({ refuse: { 'pw://ada/gov-data': 'the store answered AccessDenied: Access Denied' } })
    const onAdded = vi.fn()
    render(<AddLibrary options={ONE} onAdded={onAdded} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('papers') })
    await chooseComputer('papers')
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Add gov-data' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'Add images' }))
    fireEvent.click(screen.getByRole('button', { name: 'List and index 3 buckets' }))
    expect(await screen.findByText('gov-data: the store answered AccessDenied: Access Denied')).toBeInTheDocument()
    expect(onAdded).not.toHaveBeenCalled()
    expect(s.posted()).toHaveLength(3)
    expect(screen.getByRole('checkbox', { name: 'Add images' })).toHaveAttribute('aria-disabled', 'true')
  })

  it('remembers the interval and the endpoint of the buckets added before, and marks those', async () => {
    const at = 'https://bucket.vpce-0example.s3.us-gov-west-1.vpce.amazonaws.com'
    const s = stub({ libs: [library('pw://ada/papers', '', { region: 'us-gov-west-1', endpoint: at, every: 360 })] })
    const onAdded = vi.fn()
    render(<AddLibrary options={ONE} onAdded={onAdded} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('gov-data') })
    await chooseComputer('gov-data')
    expect(await screen.findByRole('checkbox', { name: 'Add papers' })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('Added already')).toBeInTheDocument())
    // An endpoint is one region's: a bucket elsewhere is not asked there.
    fireEvent.click(screen.getByRole('checkbox', { name: 'Add images' }))
    fireEvent.click(screen.getByRole('button', { name: 'More options' }))
    expect(screen.getByRole('textbox', { name: /Endpoint/ })).toHaveValue(at)
    fireEvent.click(screen.getByRole('button', { name: 'List and index 2 buckets' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
    expect(s.posted()).toEqual([{ bucket: 'pw://ada/gov-data', every: 360, endpoint: at }, { bucket: 'pw://ada/images', every: 360 }])
  })

  it('refuses the same bucket twice before it is asked', async () => {
    stub({ libs: [library('pw://ada/papers')] })
    render(<AddLibrary options={ONE} onAdded={() => {}} />)
    await waitFor(async () => { const t = (await computers()).map(x => x.textContent).join('|'); closeMenu(); expect(t).toContain('papers') })
    await chooseComputer('papers')
    expect(await screen.findByText(/already a library of yours/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'List and index' })).toBeDisabled()
  })

  it('is a way in on a Studio where people sign in, with nothing else to add', () => {
    const user = { available: false, ssh: false, pwOffered: false, workflow: false }
    expect(mayAddLibraries(false, user, true, true)).toBe(true)
    expect(mayAddLibraries(false, user, true, false)).toBe(false)
    const o = addOptions(false, { ...user, workflow: true, available: true }, true, true)
    expect([o.buckets, o.bucket]).toEqual([true, false])
    expect(addChoices(o).workflowOnly).toBe(false)
  })

  it('names a bucket and what a listing costs', () => {
    expect(bucketWhere({ location: 'pw://ada/papers', path: '', label: '' })).toBe('ada/papers')
    expect(listingCost({ requests: 101, provider: 'AWS', every: 1440, route: 'direct' })).toMatch(/^101 list requests a listing, under \$0.01/)
    expect(listingCost({ requests: 12, provider: 'Azure', every: 0, route: 'pw' })).toBe('12 runs of pw buckets a listing, each at least one list request')
  })
})
