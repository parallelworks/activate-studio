import { useState, type ReactNode } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeMessages } from '@parallelworks/code'
import type { TodoItem } from '@parallelworks/ui/ai'

const api = vi.hoisted(() => ({
  states: [] as { url: string; state: string | undefined }[],
  listing: undefined as unknown,
  removeSessionDirectory: vi.fn(async () => ({})),
}))
vi.mock('../src/pwcode/api', () => ({
  pwCode: {
    useSessionPullRequestStates: () => api.states,
    useSessions: () => ({ data: api.listing }),
    removeSessionDirectory: api.removeSessionDirectory,
  },
}))
vi.mock('../src/pwcode/PWCodeSession', () => ({ describeError: (e: unknown) => String(e) }))

import { SessionContextRow } from '../src/pwcode/session/ContextRow'
import { SessionTasks } from '../src/pwcode/session/SessionTasks'
import { UsageLimitWait } from '../src/pwcode/session/UsageLimitWait'
import { TerminalHandoff } from '../src/pwcode/session/TerminalHandoff'

let messages: PwCodeMessages
beforeAll(async () => { messages = await loadPwCodeMessages('en') })
beforeEach(() => {
  api.states = []
  api.listing = undefined
  api.removeSessionDirectory.mockClear()
})

function withIntl(node: ReactNode) {
  return render(<IntlProvider locale="en" messages={messages} timeZone="UTC">{node}</IntlProvider>)
}

const pr = (n: number) => `https://github.com/example/repo/pull/${n}`

describe('The session context row', () => {
  it("names the session's machine and folder, and lists and removes an added directory", async () => {
    withIntl(
      <SessionContextRow machine="m1" sessionId="s1" machineName="gpu-box" workspace="/home/user/project"
        added={[{ path: '/data/shared' }]} allowed={[]} pullRequests={null} />,
    )
    const chip = screen.getByTestId('pwcode-workspace')
    expect(chip).toHaveTextContent('gpu-box:project')
    expect(chip).toHaveAttribute('title', 'gpu-box:/home/user/project')
    // Its machine is not one the Studio knows: its own name, and a glyph.
    expect(chip.querySelector('svg.code-machine-icon')).not.toBeNull()
    expect(chip.querySelector('img')).toBeNull()
    expect(screen.getByTestId('pwcode-added-directory')).toHaveTextContent('/data/shared')

    fireEvent.click(screen.getByTestId('pwcode-added-directory'))
    const row = await screen.findByTestId('pwcode-directory-row')
    expect(screen.getByText('Original working directory')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button'))
    await waitFor(() => expect(api.removeSessionDirectory).toHaveBeenCalledWith('m1', 's1', '/data/shared'))
  })

  it('folds directories past the first two into a count', () => {
    withIntl(
      <SessionContextRow machine="m1" sessionId="s1" machineName="gpu-box" workspace="/w"
        added={[{ path: '/a' }, { path: '/b' }, { path: '/c' }, { path: '/d' }]} allowed={null} pullRequests={null} />,
    )
    const chips = screen.getAllByTestId('pwcode-added-directory')
    expect(chips).toHaveLength(2)
    expect(chips[1]).toHaveTextContent('+2')
  })

  it('shows open pull requests with their state and folds the done ones into a count', async () => {
    api.states = [
      { url: pr(1), state: 'open' },
      { url: pr(2), state: 'merged' },
      { url: pr(3), state: 'closed' },
    ]
    withIntl(
      <SessionContextRow machine="m1" sessionId="s1" machineName="gpu-box" workspace="/w"
        added={null} allowed={null} pullRequests={[pr(1), pr(2), pr(3)]} />,
    )
    const pills = screen.getAllByTestId('pwcode-pull-request')
    expect(pills).toHaveLength(1)
    expect(pills[0]).toHaveAttribute('href', pr(1))
    expect(within(pills[0]).getByTestId('pwcode-pull-request-open')).toBeInTheDocument()

    const more = screen.getByTestId('pwcode-pull-requests-more')
    expect(more).toHaveAttribute('data-group', 'done')
    fireEvent.click(more)
    await waitFor(() => expect(screen.getAllByTestId('pwcode-pull-request')).toHaveLength(3))
    expect(screen.getByRole('link', { name: /#2/ })).toHaveAttribute('href', pr(2))
  })
})

describe("The session's tasks", () => {
  const todos: TodoItem[] = [
    { content: 'Read the code', status: 'completed' },
    { content: 'Write the fix', activeForm: 'Writing the fix', status: 'in_progress' },
    { content: 'Run the tests', status: 'pending' },
  ]

  it('lists the tasks and, folded, names the running one', () => {
    function Host() {
      const [open, setOpen] = useState(true)
      return <SessionTasks todos={todos} open={open} onOpenChange={setOpen} />
    }
    withIntl(<Host />)
    const head = screen.getByRole('button', { expanded: true })
    expect(screen.getAllByRole('listitem')).toHaveLength(3)
    fireEvent.click(head)
    expect(screen.queryAllByRole('listitem')).toHaveLength(0)
    expect(screen.getByRole('button', { expanded: false })).toHaveTextContent('Writing the fix')
  })
})

describe('The usage limit wait', () => {
  it('is a plain line without accounts, and opens them when it can', () => {
    const { unmount } = withIntl(<UsageLimitWait resumeAt={null} />)
    expect(screen.getByTestId('pwcode-usage-limit-wait').tagName).toBe('DIV')
    unmount()
    const onOpenAccounts = vi.fn()
    withIntl(<UsageLimitWait resumeAt={new Date(Date.now() + 3600_000).toISOString()} onOpenAccounts={onOpenAccounts} />)
    fireEvent.click(screen.getByTestId('pwcode-usage-limit-wait'))
    expect(onOpenAccounts).toHaveBeenCalledOnce()
  })
})

describe('The terminal handoff', () => {
  const listing = {
    machines: [{ id: 'm1', name: 'gpu-box', kind: 'resource', status: 'connected' }],
    sessions: [{ id: 's1', machineId: 'm1' }],
  }

  it('offers the attach command, and opens it in the terminal only when it can', async () => {
    api.listing = listing
    const onOpenTerminal = vi.fn()
    const { unmount } = withIntl(<TerminalHandoff machine="m1" sessionId="s1" username="user" onOpenTerminal={onOpenTerminal} />)
    fireEvent.click(screen.getByRole('button', { name: 'Continue in a terminal' }))
    expect(await screen.findByText('gpu-box/s1')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Open in terminal/ }))
    expect(onOpenTerminal).toHaveBeenCalledWith('pw code attach gpu-box/s1')
    unmount()

    withIntl(<TerminalHandoff machine="m1" sessionId="s1" username="user" />)
    fireEvent.click(screen.getByRole('button', { name: 'Continue in a terminal' }))
    expect(await screen.findByText('gpu-box/s1')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Open in terminal/ })).toBeNull()
  })

  it('says so while the machine is not listed', async () => {
    withIntl(<TerminalHandoff machine="m1" sessionId="s1" username="user" />)
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Continue in a terminal' })) })
    expect(await screen.findByText(/Looking up this session/)).toBeInTheDocument()
  })
})
