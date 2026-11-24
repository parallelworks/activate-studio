import { fireEvent, render, screen, within } from '@testing-library/react'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeMessages } from '@parallelworks/code'
import type { ReactNode } from 'react'

const hooks = vi.hoisted(() => ({
  panel: null as unknown,
  signIn: null as unknown,
  usePanel: vi.fn(),
  useSignIn: vi.fn(),
}))
vi.mock('@parallelworks/code', async original => ({
  ...(await original<typeof import('@parallelworks/code')>()),
  useClaudeAccountPanel: (...args: unknown[]) => {
    hooks.usePanel(...args)
    return hooks.panel
  },
  useClaudeSignIn: (...args: unknown[]) => {
    hooks.useSignIn(...args)
    return hooks.signIn
  },
}))
vi.mock('../src/pwcode/api', () => ({
  pwCode: {
    useMachine: () => ({ machine: undefined, recheck: vi.fn() }),
    useClaudeAccounts: vi.fn(),
    useRefreshClaudeModels: vi.fn(),
    useRemoveClaudeAccount: vi.fn(),
    useMoveClaudeAccountSessions: vi.fn(),
    startClaudeLogin: vi.fn(),
    followClaudeLogin: vi.fn(),
    writeClaudeLogin: vi.fn(),
    cancelClaudeLogin: vi.fn(),
  },
}))

import { AccountChip, AccountPanelBody, AccountPanelModal } from '../src/pwcode/session/AccountPanel'
import { AccountSignIn, terminalText } from '../src/pwcode/session/AccountSignIn'

let messages: PwCodeMessages
beforeAll(async () => { messages = await loadPwCodeMessages('en') })

function intl(children: ReactNode) {
  return <IntlProvider locale="en" messages={messages} timeZone="UTC">{children}</IntlProvider>
}

const facts = { installed: true, allowed: true, login: true, accountRemove: true }

function panel(over: Record<string, unknown> = {}) {
  return {
    available: true, notice: null, view: { kind: 'list' }, title: 'Accounts', description: 'Accounts on gpu-box',
    signInNote: null, removeBody: null, emptyHint: null,
    accounts: [
      { name: 'default', email: 'dev@example.org', fiveHourUsedPercent: 42, weeklyUsedPercent: 10 },
      { name: 'work', signInRequired: true },
    ],
    loading: false, loadedUsage: true, error: null, empty: false, refreshing: false,
    canAdd: true, showAdd: true, showRefresh: true, startIndex: 0, selectedIndex: 0, firstEnabledIndex: 0,
    isDisabled: () => false,
    detail: (a: { email?: string }) => (a.email ? { kind: 'email', text: a.email } : null),
    choose: vi.fn(), select: vi.fn(),
    actions: () => [],
    add: vi.fn(), startSignIn: vi.fn(), finishSignIn: vi.fn(), selectSignedIn: undefined, back: vi.fn(),
    refresh: vi.fn(async () => {}), removeAccount: vi.fn(async () => {}),
    handoff: false, others: 0, moveOthersLabel: null, moveOthers: true, setMoveOthers: vi.fn(),
    ...over,
  }
}

function signIn(over: Record<string, unknown> = {}) {
  return {
    attempt: 0, login: { id: 'l1', status: 'running' }, error: null, truncated: false, inputTooLong: false,
    link: null, signInPage: null, instructions: 'Open the sign-in page and follow the prompts.',
    truncatedNote: null, closeLabel: 'Cancel sign-in', output: '', acceptsInput: true, starting: false,
    signedIn: false, finished: false, use: undefined, canReconnect: false, canRetry: false,
    status: 'Follow the prompts in the terminal.',
    input: vi.fn(() => true), resized: vi.fn(), reconnect: vi.fn(), retry: vi.fn(),
    ...over,
  }
}

describe('The account panel', () => {
  it('lists the accounts with their usage and chooses one for the session', () => {
    const p = panel()
    hooks.panel = p
    const account = { value: 'default', onChange: vi.fn() }
    render(intl(<AccountPanelBody machine="gpu-box" machineName="gpu-box" sessionId="s1" account={account} machineFacts={facts} />))
    expect(screen.getByText('Accounts on gpu-box')).toBeInTheDocument()
    const list = screen.getByRole('listbox')
    const rows = within(list).getAllByRole('option')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('default')
    expect(rows[0]).toHaveTextContent('dev@example.org')
    expect(rows[0]).toHaveAttribute('aria-checked', 'true')
    expect(within(rows[0]!).getAllByRole('meter')).toHaveLength(2)
    expect(rows[1]).toHaveTextContent('Not signed in')
    expect(hooks.usePanel).toHaveBeenLastCalledWith(expect.anything(), expect.objectContaining({
      machine: 'gpu-box', sessionId: 's1', claudeLogin: true, account,
    }))
    fireEvent.click(rows[1]!)
    expect(p.choose).toHaveBeenCalledWith(expect.objectContaining({ name: 'work' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add account' }))
    expect(p.add).toHaveBeenCalled()
  })

  it('in a settings card, picks nothing for a signed-in account but signs in a signed-out one', () => {
    const p = panel()
    hooks.panel = p
    render(intl(<AccountPanelBody machine="studio" machineName="This machine" machineFacts={facts} />))
    const rows = screen.getAllByRole('option')
    expect(rows[0]).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(rows[0]!)
    expect(p.choose).not.toHaveBeenCalled()
    fireEvent.click(rows[1]!)
    expect(p.choose).toHaveBeenCalledWith(expect.objectContaining({ name: 'work' }))
    expect(screen.queryByRole('button', { name: 'Close Claude Code accounts' })).toBeNull()
  })

  it('in a settings card, scrolls nothing until the reader moves in the list', () => {
    // The selected account starts highlighted; bringing it into view on load scrolled the
    // whole Settings page down to it.
    const scrolled = vi.fn()
    const had = Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = scrolled
    try {
      hooks.panel = panel({
        accounts: [{ name: 'a' }, { name: 'b' }, { name: 'c' }, { name: 'd' }],
        startIndex: 3, selectedIndex: 3,
      })
      render(intl(<AccountPanelBody machine="studio" machineName="This machine" machineFacts={facts} />))
      const list = screen.getByRole('listbox')
      expect(within(list).getAllByRole('option')[3]).toHaveAttribute('data-highlighted')
      expect(scrolled).not.toHaveBeenCalled()
      fireEvent.mouseMove(within(list).getAllByRole('option')[1]!)
      expect(scrolled).not.toHaveBeenCalled()
      fireEvent.focus(list)
      fireEvent.keyDown(list, { key: 'ArrowDown' })
      expect(within(list).getAllByRole('option')[2]).toHaveAttribute('data-highlighted')
      expect(scrolled).toHaveBeenCalled()
      expect(list).toHaveAttribute('aria-activedescendant', within(list).getAllByRole('option')[2]!.id)
    } finally {
      Element.prototype.scrollIntoView = had
    }
  })

  it('says when the machine has none, and when it cannot run them', () => {
    hooks.panel = panel({ accounts: [], empty: true })
    const { unmount } = render(intl(<AccountPanelBody machine="gpu-box" machineName="gpu-box" machineFacts={facts} />))
    expect(screen.getByText('No Claude Code accounts are configured on this machine.')).toBeInTheDocument()
    unmount()
    hooks.panel = panel({ notice: 'Not installed here' })
    render(intl(<AccountPanelBody machine="gpu-box" machineName="gpu-box" machineFacts={facts} />))
    expect(screen.getByText('Not installed here')).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('opens as a dialog over the session, which closes', async () => {
    hooks.panel = panel()
    const onClose = vi.fn()
    render(intl(<AccountPanelModal machine="gpu-box" machineName="gpu-box" sessionId="s1" account={{ value: 'default', onChange: vi.fn() }} machineFacts={facts} onClose={onClose} />))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Accounts')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: /^Close .* accounts$/ }))
    expect(onClose).toHaveBeenCalled()
  })

  it('shows the chip only for a model that runs on an account', () => {
    const onOpen = vi.fn()
    const { rerender } = render(intl(<AccountChip model="claude-code/sonnet" account="work" onOpen={onOpen} />))
    const chip = screen.getByTestId('pwcode-account-chip')
    expect(chip).toHaveTextContent('work')
    fireEvent.click(chip)
    expect(onOpen).toHaveBeenCalled()
    rerender(intl(<AccountChip model="gpt-example" account="work" onOpen={onOpen} />))
    expect(screen.queryByTestId('pwcode-account-chip')).toBeNull()
  })
})

describe('The sign-in view', () => {
  const props = { machine: 'gpu-box', machineName: 'gpu-box', sessionId: 's1', account: 'work', onComplete: vi.fn(), onUse: undefined, onBack: vi.fn() }

  it('offers the sign-in page only at a web address', () => {
    hooks.signIn = signIn({ signInPage: 'https://example.org/oauth' })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const { unmount } = render(intl(<AccountSignIn {...props} />))
    fireEvent.click(screen.getByRole('button', { name: 'Open sign-in page' }))
    expect(open).toHaveBeenCalledWith('https://example.org/oauth', '_blank', 'noopener,noreferrer')
    unmount()
    hooks.signIn = signIn({ signInPage: 'javascript:alert(1)' })
    render(intl(<AccountSignIn {...props} />))
    expect(screen.queryByRole('button', { name: 'Open sign-in page' })).toBeNull()
    open.mockRestore()
  })

  it('sends a pasted code with Enter', () => {
    const s = signIn()
    hooks.signIn = s
    render(intl(<AccountSignIn {...props} />))
    const field = screen.getByTestId('pwcode-signin-code')
    fireEvent.change(field, { target: { value: ' abc123 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(s.input).toHaveBeenCalledWith('abc123\r')
    expect(field).toHaveValue('')
  })

  it('draws the output without escape sequences', () => {
    hooks.signIn = signIn({
      output: '\u001b[1mWelcome\u001b[0m\r\n\u001b]8;;https://example.org/x\u0007Sign in here\u001b]8;;\u0007\r\nWorking\u001b[2K\rDone\r\n',
    })
    render(intl(<AccountSignIn {...props} />))
    const out = screen.getByTestId('pwcode-signin-output')
    expect(out.textContent).toBe('Welcome\nSign in here\nDone')
    expect(out.textContent).not.toContain('\u001b')
  })

  it('offers the account once signed in, and the hook\'s retry and reconnect', () => {
    const use = vi.fn()
    const s = signIn({ signedIn: true, use, status: 'Signed in as dev@example.org.', closeLabel: 'Back to accounts', canRetry: true, canReconnect: true })
    hooks.signIn = s
    render(intl(<AccountSignIn {...props} />))
    expect(screen.queryByTestId('pwcode-signin-output')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Signed in as dev@example.org.')
    fireEvent.click(screen.getByRole('button', { name: 'Use this account' }))
    expect(use).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(s.retry).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }))
    expect(s.reconnect).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Back to accounts' }))
    expect(props.onBack).toHaveBeenCalled()
  })

  it('applies a redraw rather than repeating it', () => {
    expect(terminalText('Waiting.\r\nline\u001b[1A\u001b[2K\rWaiting..\r\n')).toBe('Waiting..\nline')
  })
})
