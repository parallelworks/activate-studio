import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('the footer\'s model line when AI features are switched', () => {
  it('reads the model health again as soon as the configuration says AI features changed, without waiting for the poll', async () => {
    let ai = false
    const asked: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      asked.push(url)
      if (url === '/api/config') return reply({ appName: 'Studio', aiEnabled: ai, user: { id: 'u', username: 'u' } })
      if (url === '/api/ai/health') {
        return reply(ai
          ? { ok: true, status: 'ok', models: 2, gatewayHost: 'gw.test', message: null, lastCallFailure: null, checkedAt: new Date().toISOString() }
          : { ok: false, status: 'off', models: 0, gatewayHost: '', message: null, lastCallFailure: null, checkedAt: new Date().toISOString() })
      }
      if (url === '/api/whoami') return reply({ authEnabled: false, verified: true })
      if (url.startsWith('/api/kb/stats')) return reply({ files: 3, dirs: 1, totalBytes: 10 })
      if (url === '/api/index/status') return reply({ running: false, lastSweepAt: new Date().toISOString(), lastSweepMs: 1, lastChanges: [], lastError: null, sweepIntervalSec: 300 })
      if (url === '/api/health') return reply({ gufi: true })
      if (url === '/api/version') return reply({ version: '1.0.0', commit: 'abc' })
      return reply({})
    }))
    vi.resetModules()
    const { StatusFooter } = await import('../src/components/StatusFooter')
    const { useAppConfig } = await import('../src/config')
    // The app reads the configuration as the sidebar does.
    function Probe() { useAppConfig(); return null }
    render(<><Probe /><StatusFooter /></>)
    // Off: no model line at all.
    await waitFor(() => expect(asked).toContain('/api/ai/health'))
    expect(await screen.findByText('3 files indexed')).toBeInTheDocument()
    expect(screen.queryByText(/models ready|AI features off/)).toBeNull()
    const healthReads = asked.filter(u => u === '/api/ai/health').length

    // The switch saves the setting, then the app reads its configuration again.
    ai = true
    await act(async () => { window.dispatchEvent(new Event('ade:config-changed')) })
    expect(await screen.findByText('2 models ready', {}, { timeout: 2000 })).toBeInTheDocument()
    expect(screen.queryByText('AI features off')).toBeNull()
    expect(asked.filter(u => u === '/api/ai/health').length).toBeGreaterThan(healthReads)
  })
})
