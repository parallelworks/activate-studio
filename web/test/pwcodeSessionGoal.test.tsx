import { fireEvent, render, screen, within } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeMessages, type PwCodeSessionGoal } from '@parallelworks/code'
import type { ReactNode } from 'react'

const hooks = vi.hoisted(() => ({ goal: null as unknown, useSessionGoal: vi.fn() }))
vi.mock('@parallelworks/code', async original => ({
  ...(await original<typeof import('@parallelworks/code')>()),
  useSessionGoal: (...args: unknown[]) => {
    hooks.useSessionGoal(...args)
    return hooks.goal
  },
  useGoalElapsed: (_status: unknown, clock: string) => (clock === 'chip' ? '3m' : '3m 12s'),
}))
vi.mock('../src/pwcode/api', () => ({ pwCode: { runGoal: vi.fn(), saveImage: vi.fn(), requestRecap: vi.fn() } }))

import { GoalControl } from '../src/pwcode/session/GoalControl'
import { ComposerImages } from '../src/pwcode/session/ComposerImages'
import { BackgroundCommandHint, BackgroundCommandsContext } from '../src/pwcode/session/BackgroundCommandHint'

let messages: PwCodeMessages
beforeAll(async () => { messages = await loadPwCodeMessages('en') })

function intl(children: ReactNode) {
  return <IntlProvider locale="en" messages={messages} timeZone="UTC">{children}</IntlProvider>
}

function goal(over: Partial<PwCodeSessionGoal> = {}): PwCodeSessionGoal {
  return {
    goal: { condition: 'All tests pass on gpu-box', turns: 2, tokens: 1500 } as PwCodeSessionGoal['goal'],
    loading: false, pending: null, error: null,
    edit: vi.fn(async () => true), pause: vi.fn(async () => true), resume: vi.fn(async () => true), clear: vi.fn(async () => true),
    view: 'goal', note: null, busy: false,
    state: { tone: 'info', off: false, pulse: false, text: 'Active' },
    lastCheck: { title: 'Last check', reason: 'Two tests still fail' },
    pausedNote: null,
    toggle: { action: 'pause', label: 'Pause' },
    draft: null, setDraft: vi.fn(), startEdit: vi.fn(), empty: false, unchanged: true, canSave: false, saving: false,
    save: vi.fn(async () => true),
    ...over,
  }
}

const label = { tone: 'info' as const, off: false, pulse: false, active: true, text: 'Goal active' }
const status = { status: 'active' } as never

describe('The goal chip', () => {
  it('shows the goal and its time, and opens to its condition, facts and actions', async () => {
    const g = goal()
    hooks.goal = g
    render(intl(<GoalControl machine="gpu-box" sessionId="s1" label={label} status={status} locked={false} />))
    const chip = screen.getByTestId('agent-goal')
    expect(chip).toHaveTextContent('Goal active')
    expect(screen.getByTestId('agent-goal-elapsed')).toHaveTextContent('3m')
    expect(hooks.useSessionGoal).toHaveBeenLastCalledWith(expect.anything(), 'gpu-box', 's1', { open: false, status, locked: false })

    fireEvent.click(chip)
    expect(await screen.findByTestId('agent-goal-condition')).toHaveTextContent('All tests pass on gpu-box')
    expect(hooks.useSessionGoal).toHaveBeenLastCalledWith(expect.anything(), 'gpu-box', 's1', { open: true, status, locked: false })
    const facts = screen.getByTestId('agent-goal-facts')
    expect(facts).toHaveTextContent('Active')
    expect(facts).toHaveTextContent('3m 12s')
    expect(facts).toHaveTextContent('2 turns checked')
    expect(screen.getByTestId('agent-goal-reason')).toHaveTextContent('Two tests still fail')

    fireEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(g.pause).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Clear goal' }))
    expect(g.clear).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    expect(g.startEdit).toHaveBeenCalled()
  })

  it('edits the condition and saves it with ctrl+enter', async () => {
    const g = goal({ view: 'editing', draft: 'All tests pass', canSave: true })
    hooks.goal = g
    render(intl(<GoalControl machine="gpu-box" sessionId="s1" label={label} status={status} locked={false} />))
    fireEvent.click(screen.getByTestId('agent-goal'))
    const editor = await screen.findByTestId('agent-goal-editor')
    fireEvent.change(editor, { target: { value: 'All tests pass twice' } })
    expect(g.setDraft).toHaveBeenCalledWith('All tests pass twice')
    fireEvent.keyDown(editor, { key: 'Enter', ctrlKey: true })
    expect(g.save).toHaveBeenCalled()
  })

  it('says why an action failed', async () => {
    hooks.goal = goal({ error: { detail: 'The machine refused' } })
    render(intl(<GoalControl machine="gpu-box" sessionId="s1" label={label} status={status} locked={false} />))
    fireEvent.click(screen.getByTestId('agent-goal'))
    expect(await screen.findByRole('alert')).toHaveTextContent('The machine refused')
  })
})

describe('The composer images', () => {
  const images = [
    { key: 'a', placeholder: '[Image #1]', previewUrl: 'blob:one', saved: true },
    { key: 'b', placeholder: '[Image #2]', previewUrl: 'blob:two', saved: false },
  ]

  it('draws nothing without images', () => {
    const { container } = render(intl(<ComposerImages images={[]} onRemove={vi.fn()} />))
    expect(container).toBeEmptyDOMElement()
  })

  it('shows each image, marks one still saving, and removes one', () => {
    const onRemove = vi.fn()
    render(intl(<ComposerImages images={images} onRemove={onRemove} />))
    const list = screen.getByRole('list', { name: 'Images to send' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(2)
    expect(within(list).getAllByRole('listitem')[1]).toHaveAttribute('aria-busy', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Remove [Image #2]' }))
    expect(onRemove).toHaveBeenCalledWith('b')
  })

  it('opens an image at full size', async () => {
    render(intl(<ComposerImages images={images} onRemove={vi.fn()} />))
    fireEvent.click(screen.getByAltText('[Image #1]'))
    const viewer = await screen.findByRole('dialog')
    expect(within(viewer).getByRole('img', { name: '[Image #1]' })).toHaveAttribute('src', 'blob:one')
  })

  it('cannot remove while the composer is disabled', () => {
    render(intl(<ComposerImages images={images} onRemove={vi.fn()} disabled />))
    expect(screen.getByRole('button', { name: 'Remove [Image #1]' })).toBeDisabled()
  })
})

describe('The background hint', () => {
  const part = { type: 'tool_call', id: 't1', name: 'Bash', status: 'running', args: {} } as never
  const message = { id: 'm1', role: 'assistant', content: '' } as never

  it('draws nothing in a chat with no session, even without pw code strings', () => {
    const { container } = render(<BackgroundCommandHint part={part} message={message} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('offers ctrl+b under a running Bash call and moves the commands', () => {
    const move = vi.fn(async () => {})
    render(intl(
      <BackgroundCommandsContext value={{ offered: true, moving: false, move }}>
        <BackgroundCommandHint part={part} message={message} />
      </BackgroundCommandsContext>,
    ))
    fireEvent.click(screen.getByTestId('agent-background'))
    expect(move).toHaveBeenCalled()
  })

  it('draws nothing until the session offers it', () => {
    const { container } = render(intl(
      <BackgroundCommandsContext value={{ offered: false, moving: false, move: vi.fn() }}>
        <BackgroundCommandHint part={part} message={message} />
      </BackgroundCommandsContext>,
    ))
    expect(container).toBeEmptyDOMElement()
  })
})
