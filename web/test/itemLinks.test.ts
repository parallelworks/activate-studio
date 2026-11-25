import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@parallelworks/ui'
import { copyItemLink, copyLinkLabel, itemInAddress, linkAddress, linkStart, onLinkToShow, openItem, replacedText, thisComputerOnly, withoutItem } from '../src/itemLinks'
import { restoreLastHash } from '../src/lastLocation'

const ID = 'AbCdEfGhIjKlMnOpQrSt_-'
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); history.replaceState(null, '', '/'); sessionStorage.clear() })

describe('the form of a link', () => {
  it('holds the ID, in the query and the fragment, and nothing else of the item', () => {
    expect(linkAddress('https://studio.example.com', ID)).toBe(`https://studio.example.com/?item=${ID}#item=${ID}`)
  })
  it('starts with the lasting address where there is one, else the page\'s own', () => {
    const at = { origin: 'https://here.example.com', pathname: '/me/session/ada/studio/index.html' }
    expect(linkStart('https://studio.example.com/', at)).toBe('https://studio.example.com')
    expect(linkStart('', at)).toBe('https://here.example.com/me/session/ada/studio')
    expect(linkStart(undefined, { origin: 'https://here.example.com', pathname: '/' })).toBe('https://here.example.com')
  })
  it('says when it opens on this computer only', () => {
    for (const b of ['http://localhost:4180', 'http://127.0.0.1:7000/x', 'http://[::1]:80', 'http://studio.localhost', 'wails://studio', 'nonsense']) expect(thisComputerOnly(b)).toBe(true)
    for (const b of ['https://studio.example.com', 'http://10.1.2.3:8080', 'https://localhost.example.com']) expect(thisComputerOnly(b)).toBe(false)
    expect(copyLinkLabel('https://studio.example.com')).toBe('Copy link')
    expect(copyLinkLabel('')).toBe('Copy link (this computer only)')
  })
})

describe('an address with an item in it', () => {
  it('is read from the fragment, else the query', () => {
    expect(itemInAddress({ search: `?item=${ID}`, hash: '' })).toBe(ID)
    expect(itemInAddress({ search: '', hash: `#item=${ID}` })).toBe(ID)
    expect(itemInAddress({ search: '?item=other', hash: `#item=${ID}` })).toBe(ID)
    expect(itemInAddress({ search: '', hash: '#open=file:item' })).toBeNull()
    expect(itemInAddress({ search: '?x=1', hash: '' })).toBeNull()
  })
  it('asks the server nothing odd for an ID no Studio makes', () => {
    for (const bad of ['', '..', '../../api/kb/file', `${ID}x`, 'reports/a.md']) expect(itemInAddress({ search: `?item=${encodeURIComponent(bad)}`, hash: '' })).toBe('invalid')
  })
  it('is none of an embed\'s', () => {
    expect(itemInAddress({ search: `?embed=html&item=${ID}`, hash: '' })).toBeNull()
  })
  it('loses the item and keeps the rest', () => {
    expect(withoutItem({ pathname: '/me/session/a/b/', search: `?item=${ID}&theme=dark`, hash: `#item=${ID}` })).toBe('/me/session/a/b/?theme=dark')
    expect(withoutItem({ pathname: '/', search: `?item=${ID}`, hash: '#open=file:a.md' })).toBe('/#open=file:a.md')
  })
  it('is not replaced by where the tab last was', () => {
    sessionStorage.setItem('ade-last-hash', '#open=file:old.md')
    history.replaceState(null, '', `/?item=${ID}`)
    restoreLastHash()
    expect(location.hash).toBe('')
    history.replaceState(null, '', '/')
    restoreLastHash()
    expect(location.hash).toBe('#open=file:old.md')
  })
})

describe('Copy link', () => {
  beforeEach(() => { vi.spyOn(toast, 'success').mockReturnValue(1 as never); vi.spyOn(toast, 'error').mockReturnValue(1 as never) })
  const clipboard = (c: unknown) => Object.defineProperty(navigator, 'clipboard', { value: c, configurable: true })

  it('asks for the item\'s ID and copies the link', async () => {
    const fetched = vi.fn(async () => json({ id: ID }))
    vi.stubGlobal('fetch', fetched)
    const writeText = vi.fn(async () => {})
    clipboard({ writeText })
    await copyItemLink({ library: 'remote-1', path: 'reports/a.md' }, 'https://studio.example.com')
    expect(fetched).toHaveBeenCalledWith('/api/links', expect.objectContaining({ method: 'POST', body: JSON.stringify({ path: 'reports/a.md', library: 'remote-1' }) }))
    expect(writeText).toHaveBeenCalledWith(`https://studio.example.com/?item=${ID}#item=${ID}`)
    expect(toast.success).toHaveBeenCalledOnce()
  })
  it('gives the clipboard a promise, which WebKit takes from the click only', async () => {
    vi.stubGlobal('fetch', async () => json({ id: ID }))
    const given: Promise<Blob>[] = []
    vi.stubGlobal('ClipboardItem', class { constructor(d: Record<string, Promise<Blob>>) { given.push(d['text/plain']) } })
    const write = vi.fn(async () => { await given[0] })
    clipboard({ write, writeText: vi.fn() })
    const done = copyItemLink({ library: 'kb', path: 'a.md' }, 'https://studio.example.com')
    expect(write).toHaveBeenCalledOnce()
    await done
    expect((await given[0]).type).toBe('text/plain')
    expect(toast.success).toHaveBeenCalledOnce()
  })
  it('shows the link to copy by hand where nothing copies it', async () => {
    vi.stubGlobal('fetch', async () => json({ id: ID }))
    clipboard({ writeText: async () => { throw new Error('refused') } })
    const shown = vi.fn()
    const off = onLinkToShow(shown)
    await copyItemLink({ library: 'kb', path: 'a.md' }, 'https://studio.example.com')
    off()
    expect(shown).toHaveBeenCalledWith(`https://studio.example.com/?item=${ID}#item=${ID}`)
    expect(toast.success).not.toHaveBeenCalled()
  })
  it('says why no link was made', async () => {
    vi.stubGlobal('fetch', async () => json({ error: 'That item is not there any more, or it is not open to you.' }, 404))
    clipboard({ writeText: vi.fn() })
    await copyItemLink({ library: 'kb', path: 'a.md' }, '')
    expect(toast.error).toHaveBeenCalledWith('That item is not there any more, or it is not open to you.')
  })
})

describe('following a link', () => {
  const answer = (b: unknown, status: number) => vi.stubGlobal('fetch', async () => json(b, status))
  it('gives the item\'s library and path', async () => {
    answer({ id: ID, library: 'kb', path: 'a.md', type: 'file', replaced: true }, 200)
    expect(await openItem(ID)).toEqual({ state: 'opened', library: 'kb', path: 'a.md', type: 'file', replaced: true })
  })
  it('says what each refusal is, in the server\'s words', async () => {
    answer({ error: 'nothing for you', reason: 'none' }, 404)
    expect(await openItem(ID)).toEqual({ state: 'none', text: 'nothing for you' })
    answer({ error: 'deleted', reason: 'deleted' }, 410)
    expect(await openItem(ID)).toEqual({ state: 'deleted', text: 'deleted' })
    answer({ error: 'not now', reason: 'unreachable' }, 502)
    expect(await openItem(ID)).toEqual({ state: 'unreachable', text: 'not now' })
    answer({ error: 'sign-in required', signIn: '/api/auth/login' }, 401)
    expect(await openItem(ID)).toEqual({ state: 'signed-out', text: 'Sign in to open this link.', signIn: '/api/auth/login' })
    vi.stubGlobal('fetch', async () => { throw new TypeError('network') })
    expect((await openItem(ID)).state).toBe('failed')
  })
  it('says a replaced item may be a different one', () => {
    expect(replacedText('file')).toMatch(/replaced after the link was made/)
    expect(replacedText('dir')).toMatch(/folder was replaced/)
  })
})
