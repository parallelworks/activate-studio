import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ToolApproval } from '../src/components/ToolApproval'
import { sizeText, type ChatPlan } from '../src/components/ChatTransferPlan'

const plan: ChatPlan = {
  id: 'p1', action: 'copy', label: 'Copy reports to gpu-cluster', mover: 'rsync', moverWhy: 'rsync is on both ends',
  movers: [
    { name: 'conduit', ok: false, why: 'turned off in Settings, Transfers' },
    { name: 'rsync', ok: true, why: 'rsync is on both ends' },
    { name: 'studio-copy', ok: true },
  ],
  source: { location: 'library:main', label: 'Knowledge base', paths: ['reports'] },
  destination: { location: 'cluster:gpu-cluster', label: 'gpu-cluster', path: 'data' },
  files: 12, dirs: 2, bytes: 4_402_341_478,
  replaces: [], deletes: [], replacesCount: 0, deletesCount: 0,
  skipped: { hidden: 0, links: 0 }, freeBytes: 900_000_000_000, warnings: [],
}

// A held transfer_files call as the server streams and stores it.
const approval = (p: ChatPlan, extra: Record<string, unknown> = {}) => ({
  kind: 'approval' as const,
  id: 'call_1',
  approvalKind: 'confirm' as const,
  toolName: 'transfer_files',
  subject: 'call' as const,
  header: 'Copy 12 files (4.1 GB) to gpu-cluster: data?',
  reason: 'It runs as a transfer on the Transfers page. The chat does not wait for it, and a note is added here when it ends.',
  canAllowDirectory: false,
  expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(),
  transfer: { id: 'abc123def456', link: '#view=transfers?transfer=abc123def456', plan: p },
  ...extra,
})
const message = { id: 'srv-1', role: 'assistant', content: null, parentId: 'u1', parts: [] }

describe('the plan card of a held transfer', () => {
  it('shows the plan as the Transfers page does, above the allow and deny buttons', () => {
    const onAnswer = vi.fn()
    render(<ToolApproval part={approval(plan)} message={message} onApprovalAnswer={onAnswer} />)
    const card = screen.getByRole('region', { name: 'Transfer plan' })
    const facts = within(card)
    expect(facts.getByText('Copy')).toBeInTheDocument()
    expect(facts.getByText('Knowledge base: reports')).toBeInTheDocument()
    expect(facts.getByText('gpu-cluster: data')).toBeInTheDocument()
    expect(facts.getByText('12 files in 2 folders')).toBeInTheDocument()
    expect(facts.getByText('4.1 GB')).toBeInTheDocument()
    expect(facts.getByText('838.2 GB')).toBeInTheDocument()
    expect(facts.getByText('rsync')).toBeInTheDocument()
    expect(facts.getByText('studio-copy: can also reach both ends')).toBeInTheDocument()
    expect(facts.getByText('conduit: turned off in Settings, Transfers')).toBeInTheDocument()
    expect(facts.getByRole('link', { name: 'Transfers page' })).toHaveAttribute('href', '#view=transfers?transfer=abc123def456')
    expect(screen.getByText('Copy 12 files (4.1 GB) to gpu-cluster: data?')).toBeInTheDocument()
    expect(screen.queryByText(/Replaces|Deletes/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Yes, allow this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: true })
    fireEvent.click(screen.getByRole('button', { name: 'No, deny this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: false })
  })

  it('lists replacements as a warning and deletions in the error tone, with the first names', () => {
    const deletes = Array.from({ length: 50 }, (_, i) => `mirror/old-${i}.txt`)
    const sync: ChatPlan = {
      ...plan, action: 'sync', replaces: ['mirror/a.txt'], replacesCount: 1, deletes, deletesCount: 73,
      warnings: ['1 file at the destination will be replaced', '73 files at the destination that the source does not have will be deleted', '2 hidden entries inside the folders will be left out'],
    }
    render(<ToolApproval part={approval(sync)} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByText('Sync: send new and changed files')).toBeInTheDocument()
    expect(screen.getByText('Replaces 1 file at the destination')).toBeInTheDocument()
    expect(screen.getByText('Deletes 73 files at the destination that the source does not have')).toBeInTheDocument()
    expect(screen.getByTestId('chat-xfer-deletes').className).toContain('theme-error')
    expect(screen.getByTestId('chat-xfer-replaces').className).toContain('theme-warning')
    expect(screen.getByText('mirror/old-0.txt')).toBeInTheDocument()
    expect(screen.queryByText('mirror/old-5.txt')).toBeNull()
    expect(screen.getByText('and 68 more')).toBeInTheDocument()
    // The callouts say the replacement and the deletion; the other warning stays.
    expect(screen.queryByText('73 files at the destination that the source does not have will be deleted')).toBeNull()
    expect(screen.getByText('2 hidden entries inside the folders will be left out')).toBeInTheDocument()
  })

  it('warns that a move removes the originals, and when the destination is short of space', () => {
    render(<ToolApproval part={approval({ ...plan, action: 'move', freeBytes: 1_000 })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByText('The originals are removed')).toBeInTheDocument()
    expect(screen.getByText(/reports 1,000 bytes free, less than the 4.1 GB to send/)).toBeInTheDocument()
  })

  it('once answered shows no plan', () => {
    render(<ToolApproval part={approval(plan, { resolved: true, answer: 'Allowed' })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.queryByRole('region', { name: 'Transfer plan' })).toBeNull()
    expect(screen.getByText(/Allowed/)).toBeInTheDocument()
  })

  it('offers no buttons for someone else\'s transfer, and no plan once the hold expired', () => {
    const { unmount } = render(<ToolApproval part={approval(plan, { waitingOn: 'alex' })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByRole('region', { name: 'Transfer plan' })).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
    unmount()
    render(<ToolApproval part={approval(plan, { expiresAt: new Date(Date.now() - 1000).toISOString() })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.queryByRole('region', { name: 'Transfer plan' })).toBeNull()
    expect(screen.getByText(/This request expired/)).toBeInTheDocument()
  })

  it('says when the index of a library other than the main one shows the change', () => {
    const note = 'The index of Reference shows this change after the index is rebuilt where it is built'
    render(<ToolApproval part={approval({ ...plan, indexNotes: [note] })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByTestId('chat-xfer-index')).toHaveTextContent(note + '.')
  })

  it('counts files later when the plan cannot list them, and says sizes as the page does', () => {
    render(<ToolApproval part={approval({ ...plan, files: -1, bytes: -1, freeBytes: null })} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getAllByText('Counted when the transfer starts')).toHaveLength(2)
    expect(screen.getByText('Not reported by the destination')).toBeInTheDocument()
    expect(sizeText(1)).toBe('1 byte')
    expect(sizeText(1536)).toBe('1.5 KB')
  })
})
