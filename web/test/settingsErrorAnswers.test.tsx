import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { okJSON } from '../src/api'
import { SettingsView } from '../src/views/SettingsView'

const EFFECTIVE = {
  appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', historyIntervalSec: 86400, suggestedPrompts: [],
  visionModel: '', disabledTools: [], customTools: [], ragDefaultModel: '', ragTopK: 6, ragAllowDeploymentKey: false,
  ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true, mcpServers: [], delegationEnabled: true,
  delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner', voiceEnabled: false, voiceUrl: '',
  ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false, chatSharedHistory: true,
  bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, allowKeySharing: true, kbMission: '',
  sections: [], aiEnabled: true,
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

// A server whose platform routes fail: /api/rag/endpoint and
// /api/tasks/runtime answer with an error body, as an unported route or a
// failing platform would.
function stub(failing: Record<string, Response | (() => Response)>) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    const f = failing[url]
    if (f) return typeof f === 'function' ? f() : f.clone()
    if (url === '/api/settings') return reply({ effective: EFFECTIVE, deploymentCredential: false })
    if (url.startsWith('/api/setup/status')) return reply({ firstRun: false, standalone: true, branding: { light: null, dark: null } })
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: 'none' })
    if (url === '/api/config') return reply({ appName: 'Studio', user: { id: 'u', username: 'u' } })
    if (url === '/api/updates') return reply({ error: 'not here' }, 404)
    return reply({})
  }))
}

const at = (hash: string) => history.replaceState(null, '', `${location.pathname}${hash}`)
afterEach(() => { vi.unstubAllGlobals(); at('') })

describe('Settings pages with error answers', () => {
  it('okJSON passes an OK body and turns an error answer into its message', async () => {
    expect(await okJSON(reply({ a: 1 }))).toEqual({ a: 1 })
    await expect(okJSON(reply({ error: 'not implemented' }, 501))).rejects.toThrow('not implemented')
    await expect(okJSON(new Response('<html>', { status: 502 }))).rejects.toThrow('The server answered 502.')
  })

  it('the RAG page shows a failed registration status and keeps working', async () => {
    stub({
      '/api/rag/endpoint': () => reply({ error: 'not implemented' }, 501),
      '/api/tasks/runtime': () => reply({ error: 'not implemented' }, 501),
    })
    at('#view=settings:rag')
    render(<SettingsView />)
    expect(await screen.findByText('Registration status unavailable')).toBeInTheDocument()
    // Both the registration and the agent runtime (also on this page) say why.
    const alerts = screen.getAllByRole('alert').map(a => a.textContent)
    expect(alerts).toEqual(['not implemented', 'not implemented'])
    // The page still offers its actions.
    expect(screen.getByRole('button', { name: 'Register now' })).toBeInTheDocument()
  })

  it('the page says when the agent runtime cannot be read, in place of crashing', async () => {
    stub({ '/api/tasks/runtime': () => reply({ error: 'runtime unavailable' }, 500) })
    at('#view=settings:rag')
    render(<SettingsView />)
    expect(await screen.findByText('runtime unavailable')).toBeInTheDocument()
  })

  it('an OK runtime answer without its fields is not read as one', async () => {
    stub({})
    at('#view=settings:rag')
    render(<SettingsView />)
    expect(await screen.findByText('The server did not describe the agent runtime.')).toBeInTheDocument()
  })
})
