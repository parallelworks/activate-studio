import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createStudioAdapter } from '../src/adapter'
import { SettingsView } from '../src/views/SettingsView'

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const EFFECTIVE = {
  appName: 'Studio', kbLabel: 'Shared library', theme: 'light', accent: 'navy', surface: 'neutral', iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', historyIntervalSec: 86400, suggestedPrompts: [], visionModel: '', disabledTools: [],
  customTools: [], ragDefaultModel: '', ragTopK: 6, ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true,
  mcpServers: [], delegationEnabled: true, delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner',
  voiceEnabled: false, voiceUrl: '', ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false,
  chatSharedHistory: false, bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, kbMission: '', sections: [], aiEnabled: true,
}

async function settings(authEnabled: boolean) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url === '/api/settings') return reply({ effective: EFFECTIVE, deploymentCredential: false })
    if (url === '/api/me/model-key') return reply({ authEnabled, single: !authEnabled, verified: true, mode: 'none' })
    if (url === '/api/config') return reply({ appName: 'Studio', aiEnabled: true, user: { id: 'u', username: 'u' } })
    if (url.startsWith('/api/setup/status')) return reply({ firstRun: false, standalone: false, branding: {} })
    return reply({})
  }))
  history.replaceState(null, '', `${location.pathname}#view=settings:ai`)
  render(<SettingsView />)
  await act(async () => { await new Promise(r => setTimeout(r, 200)) })
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

describe('shared chat history', () => {
  it('says by its switch what turning it on shares', async () => {
    await settings(true)
    expect(screen.getByRole('checkbox', { name: /Allow users to see each other's chat sessions/ })).not.toBeChecked()
    expect(screen.getByTestId('shared-history-note')).toHaveTextContent("Answers can quote files from people's own libraries, so turning this on shares those quotes with everyone who signs in.")
  })

  it('is not offered on a Studio one person uses', async () => {
    await settings(false)
    expect(screen.queryByRole('checkbox', { name: /Allow users to see/ })).toBeNull()
    expect(screen.queryByTestId('shared-history-note')).toBeNull()
  })

  it('a chat that is gone is deleted without an error, and another refusal is one', async () => {
    let status = 404
    vi.stubGlobal('fetch', vi.fn(async () => reply({ error: 'conversation not found' }, status)))
    await expect(createStudioAdapter().conversations.remove('c1')).resolves.toBeUndefined()
    status = 500
    await expect(createStudioAdapter().conversations.remove('c1')).rejects.toThrow()
  })
})
