import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeConversation, type PwCodeMessages } from '@parallelworks/code'
import type { ReactNode } from 'react'

const hooks = vi.hoisted(() => ({ subagent: null as unknown, usePwCodeSubagent: vi.fn(), useSubagentDetail: vi.fn() }))
vi.mock('@parallelworks/code', async original => ({
  ...(await original<typeof import('@parallelworks/code')>()),
  usePwCodeSubagent: (...args: unknown[]) => {
    hooks.usePwCodeSubagent(...args)
    return hooks.subagent
  },
}))
vi.mock('../src/pwcode/api', () => ({
  pwCode: { useSubagentDetail: (...args: unknown[]) => { hooks.useSubagentDetail(...args); return { data: undefined, error: undefined, refresh: () => {} } } },
}))
// The transcript itself is the package's; here only what the view hands it matters.
vi.mock('@parallelworks/ui/ai', async original => ({
  ...(await original<typeof import('@parallelworks/ui/ai')>()),
  SessionView: (props: { label: string; banners: ReactNode; composer: { activity: ReactNode; settingsRight: ReactNode; settingsLeft: ReactNode; placeholder: string } }) => (
    <section aria-label={props.label}>
      {props.banners}
      <div data-testid="activity">{props.composer.activity}</div>
      <div data-testid="settings">{props.composer.settingsLeft}{props.composer.settingsRight}</div>
      <textarea aria-label={props.composer.placeholder} />
    </section>
  ),
}))

import { SessionStatusRow, useSwitcherState } from '../src/pwcode/session/AgentSwitcher'
import { SubagentView } from '../src/pwcode/session/SubagentView'

let messages: PwCodeMessages
beforeAll(async () => { messages = await loadPwCodeMessages('en') })

function intl(children: ReactNode) {
  return <IntlProvider locale="en" messages={messages} timeZone="UTC">{children}</IntlProvider>
}

const started = new Date(Date.now() - 65_000).toISOString()
const subagents = [
  { id: 'a1', agentType: 'explore', description: 'Find the config loader', status: 'running' as const, current: true, startedAt: started },
  { id: 'a2', agentType: 'review', description: 'Check the tests on gpu-box', status: 'done' as const, current: true, startedAt: started, endedAt: new Date().toISOString() },
]

function conversation(over: Partial<PwCodeConversation> = {}): PwCodeConversation {
  return {
    subagents,
    liveState: null,
    mainState: 'running',
    stopSubagent: vi.fn(async () => {}),
    stopAllSubagents: vi.fn(async () => {}),
    dismissSubagent: vi.fn(async () => true),
    ...over,
  } as unknown as PwCodeConversation
}

function Row(props: { conv: PwCodeConversation; agent?: string; onSelect: (id: string | undefined) => void; goal?: ReactNode; tasks?: ReactNode }) {
  const switcher = useSwitcherState()
  return (
    <SessionStatusRow
      conversation={props.conv}
      screen={{ activity: { kind: 'working', startedAt: started, inputTokens: 1200, outputTokens: 300, word: undefined } as never }}
      goal={props.goal}
      tasks={props.tasks}
      agent={props.agent}
      onSelectAgent={props.onSelect}
      switcher={switcher}
    />
  )
}

describe("The session's status row", () => {
  it("shows the turn's line, the goal, the tasks and the agents, and opens the list", () => {
    const onSelect = vi.fn()
    render(intl(<Row conv={conversation()} onSelect={onSelect} goal={<span>Goal chip</span>} tasks={<div>Task list</div>} />))
    expect(screen.getByTestId('agent-activity')).toBeInTheDocument()
    expect(screen.getByText('Goal chip')).toBeInTheDocument()
    expect(screen.getByText('Task list')).toBeInTheDocument()
    const toggle = screen.getByRole('button', { name: /Agents/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    const list = screen.getByRole('region', { name: 'Agents' })
    const rows = within(list).getAllByTestId('agent-switcher-row')
    expect(rows.map(r => r.getAttribute('data-agent'))).toEqual(['', 'a1', 'a2'])
    expect(within(list).getByText('Main conversation')).toBeInTheDocument()
    fireEvent.click(within(list).getByText('Find the config loader'))
    expect(onSelect).toHaveBeenCalledWith('a1')
    fireEvent.click(within(list).getByText('Main conversation'))
    expect(onSelect).toHaveBeenLastCalledWith(undefined)
  })

  it('stops one agent, stops them all, and takes a finished one off the list', async () => {
    const conv = conversation()
    render(intl(<Row conv={conv} onSelect={vi.fn()} />))
    fireEvent.click(screen.getByRole('button', { name: /Agents/ }))
    const list = screen.getByRole('region', { name: 'Agents' })
    const [running, done] = within(list).getAllByTestId('agent-switcher-row').slice(1)
    fireEvent.click(within(running!).getByRole('button', { name: /^Stop / }))
    expect(conv.stopSubagent).toHaveBeenCalledWith('a1')
    fireEvent.click(within(list).getByRole('button', { name: 'Stop all' }))
    expect(conv.stopAllSubagents).toHaveBeenCalled()
    fireEvent.click(within(done!).getByRole('button', { name: /Remove .* from the list/ }))
    await waitFor(() => expect(conv.dismissSubagent).toHaveBeenCalledWith('a2'))
  })

  it('goes back to the main conversation when the agent in view is taken off the list', async () => {
    const onSelect = vi.fn()
    const conv = conversation({ subagents: [subagents[1]!] } as never)
    render(intl(<Row conv={conv} agent="a2" onSelect={onSelect} />))
    fireEvent.click(screen.getByRole('button', { name: /Agents/ }))
    const row = screen.getAllByTestId('agent-switcher-row')[1]!
    expect(within(row).getAllByRole('button')[0]).toHaveAttribute('aria-current', 'true')
    fireEvent.click(within(row).getByRole('button', { name: /Remove .* from the list/ }))
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(undefined))
  })

  it('draws nothing while there is nothing to show', () => {
    const { container } = render(intl(
      <SessionStatusRow conversation={conversation({ subagents: [], mainState: 'idle' } as never)} screen={{ activity: null }} agent={undefined} onSelectAgent={vi.fn()} />,
    ))
    expect(container).toBeEmptyDOMElement()
  })
})

describe("A subagent's view", () => {
  it('names the agent over its transcript, with its model, and goes back or stops it', () => {
    const stop = vi.fn(async () => {})
    hooks.subagent = {
      transcript: { messages: [], streaming: null, todos: [{ content: 'Read the loader', status: 'in_progress' }] },
      messages: [], queued: [], canRemoveQueued: () => false, queueDelivery: () => 'nextStep',
      agent: subagents[0], draftKey: 'k', historyId: 'h', model: 'm', modelLabel: 'Example Model 4',
      turn: { running: true, stop, stopping: false }, send: vi.fn(), answerApproval: vi.fn(), showAuthor: false,
      failed: null, notice: null, dismissNotice: vi.fn(), recall: { available: false, take: async () => [] },
      activity: { kind: 'working', startedAt: started, inputTokens: 10, outputTokens: 5, word: undefined },
      composer: { placeholder: 'Message explore', hint: 'Sent as direction while the agent runs.', locked: false, disabled: false },
    }
    const onBack = vi.fn()
    const status = vi.fn((activity: ((brief: boolean) => ReactNode) | null) => <div>{activity?.(false)}</div>)
    render(intl(
      <SubagentView machine="gpu-box" sessionId="s1" subId="a1" listed={undefined} usage={undefined}
        subscribe={() => () => {}} loadPalette={async () => null} sessionLock={null}
        sessionBanner={null} notice={null} status={status} context={null} modelNames={undefined}
        username="ada" onSent={vi.fn()} onBack={onBack} />,
    ))
    expect(hooks.useSubagentDetail).toHaveBeenCalledWith('gpu-box', 's1', 'a1')
    expect(hooks.usePwCodeSubagent).toHaveBeenCalledWith(expect.objectContaining({ machine: 'gpu-box', sessionId: 's1', subId: 'a1' }))
    const bar = screen.getByTestId('subagent-bar')
    expect(within(bar).getByText('explore')).toBeInTheDocument()
    expect(within(bar).getByText('Find the config loader')).toBeInTheDocument()
    expect(screen.getByText('Example Model 4')).toBeInTheDocument()
    expect(screen.getByTestId('subagent-composer-note')).toHaveTextContent('Sent as direction')
    expect(screen.getByTestId('subagent-activity')).toBeInTheDocument()
    expect(status).toHaveBeenCalledWith(expect.any(Function), { tasks: [{ content: 'Read the loader', status: 'in_progress' }] })
    fireEvent.click(within(bar).getByRole('button', { name: 'Back to main' }))
    expect(onBack).toHaveBeenCalled()
    expect(within(bar).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
  })
})
