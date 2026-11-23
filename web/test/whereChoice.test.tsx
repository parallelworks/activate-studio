import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ToolApproval } from '../src/components/ToolApproval'

// A write_kb_file call held because the chat's context does not say which
// library the file goes to, as the server streams it.
const mine = { label: 'My library', library: 'space-ada', path: 'plan.md', shown: 'My library/plan.md', args: '{"content":"x","library":"space-ada","path":"plan.md"}' }
const shared = { label: 'Shared library', library: 'kb', path: 'plan.md', shown: 'Shared library/plan.md', args: '{"content":"x","library":"kb","path":"plan.md"}' }
const part = {
  kind: 'approval' as const,
  id: 'call_w',
  approvalKind: 'confirm' as const,
  toolName: 'write_kb_file',
  subject: 'call' as const,
  header: 'Where should this be saved?',
  command: 'plan.md',
  reason: 'The Studio asks because this chat has not worked with any files yet. Your answer is used for this chat\'s later files too.',
  canAllowDirectory: false,
  expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(),
  where: [mine, shared],
}
const message = { id: 'srv-1', role: 'assistant', content: null, parentId: 'u1', parts: [] }

describe('the choice of where a file is saved', () => {
  it('offers each place with its path, and answers with the call for the place chosen', () => {
    const onAnswer = vi.fn()
    render(<ToolApproval part={part} message={message} onApprovalAnswer={onAnswer} />)
    expect(screen.getByText('Where should this be saved?')).toBeInTheDocument()
    expect(screen.getByText(/has not worked with any files yet/)).toBeInTheDocument()
    expect(screen.getByText('My library/plan.md')).toBeInTheDocument()
    expect(screen.getByText('Shared library/plan.md')).toBeInTheDocument()
    // No plain allow: a place must be chosen.
    expect(screen.queryByRole('button', { name: 'Yes, allow this call' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'My library' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_w', { kind: 'confirm', allowed: true, args: mine.args })
    fireEvent.click(screen.getByRole('button', { name: 'Shared library' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_w', { kind: 'confirm', allowed: true, args: shared.args })
    fireEvent.click(screen.getByRole('button', { name: "Don't save it" }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_w', { kind: 'confirm', allowed: false })
  })

  it('once answered, shows the answer as other approvals do, with no choice', () => {
    render(<ToolApproval part={{ ...part, resolved: true, answer: 'Allowed' }} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'My library' })).toBeNull()
  })
})
