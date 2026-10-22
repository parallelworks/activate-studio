import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_MARK, EmptyChatBrand, emptyChatIcon } from '../src/components/EmptyChatBrand'
import { About } from '../src/components/About'
import { HelpView } from '../src/views/HelpView'
import { StatusFooter } from '../src/components/StatusFooter'
import { openAbout } from '../src/api'

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
