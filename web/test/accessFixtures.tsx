// Shared by the Accounts and Model access tests: a Studio's access status
// and a stand-in for the routes both pages read.
import { fireEvent, screen, within } from '@testing-library/react'
import { vi } from 'vitest'
import type { AccessStatus } from '../src/components/accessShared'

export const reply = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })

export const contexts = [
  { name: 'work', server: 'platform-a.example.com', organization: 'org-a', kind: 'api-key', expiresAt: null, expired: false, current: true, renewError: null },
  { name: 'lab', server: 'platform-b.example.com', organization: 'org-b', kind: 'sign-in', expiresAt: null, expired: false, current: false, renewError: null },
] as const

/** Picks a source by clicking its name, as a person does. */
export const choose = (name: string) => {
  const r = screen.getByRole('radio', { name })
  fireEvent.click(document.getElementById(r.getAttribute('aria-labelledby') ?? '')!)
}
/** The sources offered, in order, by name. */
export const choices = (group = 'How you sign in') => within(screen.getByRole('radiogroup', { name: group })).getAllByRole('radio')
  .map(r => document.getElementById(r.getAttribute('aria-labelledby') ?? '')?.textContent ?? '')

export function accessOf(over: Partial<AccessStatus> = {}): AccessStatus {
  const cli = { source: 'cli' as const, host: 'platform-a.example.com', context: 'work' }
  return {
    single: true, authEnabled: false,
    models: cli, platform: cli, deployment: cli,
    gatewayHost: 'platform-a.example.com', platformOrigin: 'https://platform-a.example.com',
    cli: { available: true, version: 'v7.1.0', path: '/usr/local/bin/pw', source: 'path', chosenPath: null, commonDirs: ['/home/me/.local/bin', '/usr/local/bin'], contexts: [...contexts], chosen: null, active: 'work', used: 'work', gatewayFromEnv: false, envKey: false },
    login: null,
    ...over,
  }
}

export interface Srv {
  access: AccessStatus
  health: Record<string, unknown>
  models: string[]
  calls: { method: string; url: string; body: unknown }[]
  login?: Record<string, unknown> | null
  /** Extra fields of /api/me/model-key, such as a stored endpoint's options. */
  key?: Record<string, unknown>
  /** What /api/me/model-key/test answers for a request body. */
  test?: (body: Record<string, unknown>) => unknown
}

export function stub(s: Srv) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (/^\/api\/access(\?|$)/.test(url)) return reply(s.access)
    if (url === '/api/me/model-key/test' && method === 'POST') return reply(s.test?.(body as Record<string, unknown>) ?? { status: 'ok', models: 1, ids: ['m'] })
    if (url === '/api/me/model-key' && method === 'POST') return reply({ authEnabled: false, single: true, verified: true, mode: 'stored', ...s.key })
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: s.access.models.source === 'endpoint' ? 'stored' : 'none', baseUrl: s.access.models.baseUrl ?? null, ...s.key })
    if (url.startsWith('/api/ai/health')) return reply(s.health)
    if (url.startsWith('/api/chat/models')) return reply({ models: s.models.map(id => ({ id })) })
    if (url === '/api/access/source' && method === 'PUT') {
      const name = (body as { context: string | null }).context
      s.access = accessOf({ cli: { ...accessOf().cli!, chosen: name, active: name ?? 'work', used: name ?? 'work' }, models: { source: 'cli', host: 'platform-b.example.com', context: name }, platform: { source: 'cli', host: 'platform-b.example.com', context: name } })
      return reply({ ok: true })
    }
    if (url === '/api/access/cli/path' && method === 'PUT') {
      const p = (body as { path: string | null }).path
      s.access = accessOf({ cli: { ...accessOf().cli!, path: p, source: p ? 'chosen' : 'path', chosenPath: p } })
      return reply({ cli: s.access.cli })
    }
    if (url === '/api/access/cli/login' && method === 'POST') {
      s.login = { host: 'platform-c.example.com', state: 'waiting', url: 'https://platform-c.example.com/device', code: 'ABCD-EFGH', context: null, message: null }
      return reply({ login: s.login })
    }
    if (url === '/api/access/cli/login') return reply({ login: s.login ?? null })
    if (url === '/api/access/subscriptions') return reply({ available: true, user: 'alex', org: 'org-a', codex: { offered: true, providers: [] }, copilot: { offered: false, providers: [], github: null } })
    if (url === '/api/config') return reply({ appName: 'Studio', platform: true })
    return reply({ error: 'not found' }, 404)
  }))
}

export const srv = (over: Partial<Srv> = {}): Srv => ({
  access: accessOf(), health: { status: 'ok', models: 2, gatewayHost: 'platform-a.example.com', message: null, platformAccess: null },
  models: ['me:openai/gpt-5', 'local/llama3.2'], calls: [], ...over,
})

