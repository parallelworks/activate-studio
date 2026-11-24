import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SettingsView } from '../src/views/SettingsView'
import { WorkflowsSection } from '../src/components/WorkflowsSection'
import { ownNavWidth, saveNavWidth } from '../src/config'

const LOCKED = "On a Studio with sign-in, only its administrators change its settings."

const EFFECTIVE = {
  appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', navWidth: 240, iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', historyIntervalSec: 86400, suggestedPrompts: ['What is here?'],
  visionModel: '', disabledTools: [], customTools: [{ name: 'greet', description: 'Says hi', command: 'echo hi' }], ragDefaultModel: '', ragTopK: 6,
  ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true, mcpServers: [], delegationEnabled: true,
  delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner', voiceEnabled: false, voiceUrl: '',
  ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false, chatSharedHistory: true,
  bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, kbMission: 'Our lab.', workflowCollection: ['hello'],
  sections: [], aiEnabled: true,
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

/** A Studio with sign-in, seen by someone who is (or is not) one of its administrators. */
function stub(locked: boolean, toolsApproved = true) {
  const puts: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/settings' && init?.method === 'PUT') { puts.push(JSON.parse(String(init.body))); return reply({ effective: EFFECTIVE }) }
    if (url === '/api/settings') return reply({ effective: EFFECTIVE, deploymentCredential: true, customToolsApproved: toolsApproved, ...(locked ? { locked: LOCKED } : {}) })
    if (url === '/api/config') return reply({ appName: 'Studio', aiEnabled: true, platform: true, signIn: true, settingsLocked: locked, navWidth: 240, user: { id: 'u', username: 'u' } })
    if (url.startsWith('/api/setup/status')) return reply({ firstRun: false, standalone: false, branding: { light: null, dark: null } })
    if (url === '/api/me/model-key') return reply({ authEnabled: true, verified: true, mode: 'none' })
    if (url === '/api/chat/tools') return reply({ tools: [] })
    if (url === '/api/workflows/catalog') return reply({ workflows: [{ name: 'hello', displayName: 'Hello' }], source: 'deployment', marketplace: [] })
    return reply({})
  }))
  return puts
}

const at = (hash: string) => history.replaceState(null, '', `${location.pathname}${hash}`)

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals(); at('') })

describe('Settings on a Studio with sign-in, for someone who is not an administrator', () => {
  it('shows the Studio\'s settings read only, and says why', async () => {
    stub(true)
    at('#view=settings:ai')
    render(<SettingsView />)
    const save = await screen.findByRole('button', { name: 'Save settings' })
    expect(save).toBeDisabled()
    expect(save.closest('fieldset')).toHaveAttribute('disabled')
    // The library's switches and checkboxes are not form controls a fieldset reaches: each is disabled itself.
    expect(screen.getByRole('switch', { name: 'AI features' })).toHaveAttribute('aria-disabled', 'true')
    for (const box of screen.getAllByRole('checkbox')) expect(box).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getAllByText(/only its administrators change its settings\. They show here as they are set/).length).toBeGreaterThan(0)
  })

  it('leaves the theme in this browser theirs', async () => {
    stub(true)
    at('#view=settings:appearance')
    render(<SettingsView />)
    const theme = await screen.findByText('Theme in this browser')
    expect(theme.closest('.settings-locked')).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Product name' })).toBeDisabled()
  })

  it('says custom tools wait for an administrator', async () => {
    stub(false, false)
    at('#view=settings:tools')
    render(<SettingsView />)
    expect(await screen.findByText(/These custom tools do not run yet/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save settings' })).toBeEnabled()
  })

  it('shows the workflow collection read only', async () => {
    stub(true)
    render(<WorkflowsSection />)
    expect(await screen.findByText(/The collection shows here as they set it/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove' })).toBeDisabled()
  })
})

describe('An administrator', () => {
  it('changes them as before', async () => {
    stub(false)
    at('#view=settings:ai')
    render(<SettingsView />)
    expect(await screen.findByRole('button', { name: 'Save settings' })).toBeEnabled()
    expect(screen.queryByText(/They show here as they are set/)).toBeNull()
  })
})

describe('The sidebar width', () => {
  it('is kept in the browser for someone who may not change the Studio\'s settings', async () => {
    const puts = stub(true)
    window.dispatchEvent(new Event('ade:config-changed'))
    // The app's config, as read once it arrives.
    const { useAppConfig } = await import('../src/config')
    function Probe() { const c = useAppConfig(); return <span>{c.loaded ? `locked ${String(c.settingsLocked)}` : 'loading'}</span> }
    render(<Probe />)
    await screen.findByText('locked true')
    await saveNavWidth(320)
    expect(ownNavWidth()).toBe(320)
    expect(puts).toEqual([])
    await waitFor(() => expect(document.documentElement.style.getPropertyValue('--nav-w')).toBe('320px'))
  })
})
