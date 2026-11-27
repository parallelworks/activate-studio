import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_MARK, EmptyChatBrand, emptyChatIcon } from '../src/components/EmptyChatBrand'
import { About } from '../src/components/About'
import { HelpView } from '../src/views/HelpView'
import { StatusFooter } from '../src/components/StatusFooter'
import { openAbout } from '../src/api'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
const VERSION = { version: 'v1.150', commit: 'abc1234', builtAt: null, startedAt: '2026-10-22T00:00:00Z', source: 'git' }

beforeEach(() => {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url === '/api/version') return json(VERSION)
    if (url === '/api/help') return new Response('Intro.\n\n### Quick start\n\n1. Ask.\n\n# Basics\n\n## Chat\n\nAsk things.\n')
    if (url === '/api/config') return json({ appName: 'Example Studio', platform: false })
    if (url.startsWith('/api/stats')) return json({ files: 3, dirs: 1, totalBytes: 10 })
    if (url.startsWith('/api/index/status')) return json({ running: false, sweepIntervalSec: 60, lastSweepAt: null, lastChanges: [] })
    if (url === '/api/whoami') return json({ authEnabled: false, header: '', verified: false, user: null, token: null, claims: null })
    if (url.startsWith('/api/ai/health')) return json({ status: 'ok', models: 1, gatewayHost: 'models.example', checkedAt: '2026-10-22T00:00:00Z' })
    if (url.startsWith('/api/health')) return json({ gufi: true })
    if (url.startsWith('/api/updates')) return json({ mode: 'hosted', available: false, latest: null })
    return json({})
  }))
})
afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

describe('the empty chat\'s icon', () => {
  const none = { iconUrl: null, iconUrlDark: null }
  it('is the Studio\'s own mark when none is uploaded, in light and dark', () => {
    expect(emptyChatIcon(none, false)).toBe(DEFAULT_MARK)
    expect(emptyChatIcon(none, true)).toBe(DEFAULT_MARK)
    const { container } = render(<EmptyChatBrand cfg={none} dark={false}><h1>Hello</h1></EmptyChatBrand>)
    const wrap = container.querySelector('.empty-with-brand') as HTMLElement
    expect(wrap).toHaveClass('default-mark')
    expect(wrap.style.getPropertyValue('--empty-brand-icon')).toBe(`url("${DEFAULT_MARK}")`)
  })

  it('is the uploaded icon when there is one, the dark one in dark mode, else the light one', () => {
    expect(emptyChatIcon({ iconUrl: '/api/brand-icon?v=1', iconUrlDark: null }, true)).toBe('/api/brand-icon?v=1')
    expect(emptyChatIcon({ iconUrl: '/api/brand-icon?v=1', iconUrlDark: '/api/brand-icon?variant=dark&v=1' }, true)).toBe('/api/brand-icon?variant=dark&v=1')
    const { container } = render(<EmptyChatBrand cfg={{ iconUrl: '/api/brand-icon?v=1', iconUrlDark: null }} dark={false}><h1>Hello</h1></EmptyChatBrand>)
    const wrap = container.querySelector('.empty-with-brand') as HTMLElement
    expect(wrap).not.toHaveClass('default-mark')
    expect(wrap.style.getPropertyValue('--empty-brand-icon')).toBe('url("/api/brand-icon?v=1")')
  })
})

describe('About', () => {
  it('says who builds and maintains the Studio, with a link, and the running version', async () => {
    render(<About />)
    const link = screen.getByRole('link', { name: /Parallel Works/ })
    expect(link).toHaveAttribute('href', 'https://parallelworks.com')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link.closest('.about-maker')).toHaveTextContent('Built and maintained by Parallel Works')
    expect(await screen.findByText('v1.150 (abc1234)')).toBeInTheDocument()
    expect(screen.getByText('Apache License 2.0')).toBeInTheDocument()
  })

  it('is a page of Help, opened from its rail, its address, and the footer\'s version panel', async () => {
    history.replaceState(null, '', `${location.pathname}#view=help:about`)
    render(<HelpView />)
    expect(await screen.findByRole('heading', { name: 'About' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    expect(screen.queryByRole('heading', { name: 'About' })).toBeNull()
    act(() => openAbout())
    expect(await screen.findByRole('heading', { name: 'About' })).toBeInTheDocument()
    expect(location.hash).toBe('#view=help:about')
    fireEvent.click(screen.getByRole('button', { name: 'Overview' }))
    fireEvent.click(screen.getByRole('button', { name: 'About' }))
    expect(screen.getByRole('heading', { name: 'About' })).toBeInTheDocument()
  })

  it('the version panel in the footer has an About button that opens it', async () => {
    render(<StatusFooter />)
    fireEvent.click(await screen.findByRole('button', { name: /v1\.150/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'About' }))
    await waitFor(() => expect(location.hash).toBe('#view=help:about'))
  })
})

describe('the Studio mark in the accent', () => {
  it('is the app icon for the default accent, and the same drawing in the colors of any other', async () => {
    const { studioMark, DEFAULT_MARK: mark } = await import('../src/studioMark')
    const { markColors } = await import('../src/accents')
    expect(studioMark(undefined)).toBe(mark)
    expect(studioMark('navy')).toBe(mark)
    expect(studioMark('nonsense')).toBe(mark)
    const teal = studioMark('teal')
    expect(teal.startsWith('data:image/svg+xml')).toBe(true)
    const svg = decodeURIComponent(teal.split(',')[1])
    const c = markColors('teal')!
    for (const color of [c.top, c.bottom, c.sparkTop, c.sparkBottom]) expect(svg).toContain(color)
    expect(svg).toContain('M366 86')
    expect(studioMark('teal')).toBe(teal)
    expect(studioMark('custom:#a83279')).not.toBe(teal)
    expect(emptyChatIcon({ iconUrl: null, iconUrlDark: null, accent: 'teal' }, false)).toBe(teal)
    expect(emptyChatIcon({ iconUrl: '/api/brand-icon?v=1', iconUrlDark: null, accent: 'teal' }, false)).toBe('/api/brand-icon?v=1')
  })
})

describe('the icons a browser installs', () => {
  const web = join(__dirname, '..')
  // Width and height from the PNG's header.
  const size = (f: string) => { const b = readFileSync(join(web, 'public', f)); return [b.readUInt32BE(16), b.readUInt32BE(20)] }
  it('are the header\'s mark, at the sizes the manifest and the page name', () => {
    expect(readFileSync(join(web, 'public', 'favicon.svg'), 'utf8')).toBe(readFileSync(join(web, 'icons', 'icon.svg'), 'utf8'))
    expect(size('icon-192.png')).toEqual([192, 192])
    expect(size('icon-512.png')).toEqual([512, 512])
    expect(size('icon-maskable-512.png')).toEqual([512, 512])
    expect(size('apple-touch-icon.png')).toEqual([180, 180])
  })
})

describe('the tab icon', () => {
  afterEach(() => { for (const l of document.querySelectorAll('link[rel="icon"]')) l.remove() })

  it('keeps the icon the server wrote into the page when it is the same, and drops the one for a dark device once the app sets it', async () => {
    const { setTabIcon } = await import('../src/config')
    document.head.insertAdjacentHTML('beforeend',
      '<link rel="icon" href="/api/favicon?v=1" /><link rel="icon" href="/api/favicon?variant=dark&amp;v=2" media="(prefers-color-scheme: dark)" />')
    const first = document.querySelector<HTMLLinkElement>('link[rel="icon"]')!
    setTabIcon('/api/favicon?v=1')
    const links = document.querySelectorAll<HTMLLinkElement>('link[rel="icon"]')
    expect(links).toHaveLength(1)
    expect(links[0]).toBe(first)
    expect(links[0].getAttribute('href')).toBe('/api/favicon?v=1')
    setTabIcon('/api/favicon?variant=dark&v=2')
    expect(document.querySelector('link[rel="icon"]')!.getAttribute('href')).toBe('/api/favicon?variant=dark&v=2')
  })

  it('adds one where the page has none', async () => {
    const { setTabIcon } = await import('../src/config')
    setTabIcon('/favicon.svg')
    expect(document.querySelector('link[rel="icon"]')!.getAttribute('href')).toBe('/favicon.svg')
  })
})
