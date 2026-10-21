import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { ChatProvider, ChatThread, type ChatAdapter, type StreamRequest } from '@parallelworks/ui/ai'
import { ToolApproval } from '../src/components/ToolApproval'
import { createStudioAdapter } from '../src/adapter'

// A held stop_cluster call as the server stores and streams it.
const approval = {
  kind: 'approval' as const,
  id: 'call_1',
  approvalKind: 'confirm' as const,
  toolName: 'stop_cluster',
  subject: 'call' as const,
  header: 'Stop cluster cluster-a?',
  command: 'stop_cluster(user: "alex", clusterName: "cluster-a")',
  reason: 'Stops its controller to stop its compute billing. Its disks and settings are kept, and it can be resumed.',
  canAllowDirectory: false,
  expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(),
  instead: [{ tool: 'destroy_cluster', label: 'Destroy it instead', description: 'Deletes its disks too, so nothing bills.' }],
}
const message = { id: 'srv-1', role: 'assistant', content: null, parentId: 'u1', parts: [] }

afterEach(() => vi.restoreAllMocks())

describe('the approval for a held call', () => {
  it('draws the package\'s block, with allow and deny for the call it shows, and the alternative under it', () => {
    const onAnswer = vi.fn()
    render(<ToolApproval part={approval} message={message} onApprovalAnswer={onAnswer} />)
    expect(screen.getByText('Stop cluster cluster-a?')).toBeInTheDocument()
    expect(screen.getByText(approval.command)).toBeInTheDocument()
    expect(screen.queryByText(/allow this directory/i)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Yes, allow this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: true })
    fireEvent.click(screen.getByRole('button', { name: 'No, deny this call' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: false })
    fireEvent.click(screen.getByRole('button', { name: 'Destroy it instead' }))
    expect(onAnswer).toHaveBeenLastCalledWith('call_1', { kind: 'confirm', allowed: true, instead: 'destroy_cluster' })
    expect(screen.getByText('Deletes its disks too, so nothing bills.')).toBeInTheDocument()
  })

  it('offers no buttons for someone else\'s call, and names who it waits for', () => {
    const onAnswer = vi.fn()
    render(<ToolApproval part={{ ...approval, waitingOn: 'alex' }} message={message} onApprovalAnswer={onAnswer} />)
    expect(screen.getByText('Waiting for alex to answer')).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it('offers nothing once the hold has expired', () => {
    render(<ToolApproval part={{ ...approval, expiresAt: new Date(Date.now() - 1000).toISOString() }} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByText(/This request expired/)).toBeInTheDocument()
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it('draws a call without an alternative as the package does', () => {
    render(<ToolApproval part={{ ...approval, toolName: 'start_cluster', header: 'Start cluster cluster-a?', instead: undefined }} message={message} onApprovalAnswer={vi.fn()} />)
    expect(screen.getByText('Start cluster cluster-a?')).toBeInTheDocument()
    expect(screen.queryByText(/instead/)).toBeNull()
  })
})

describe('the adapter', () => {
  it('turns the server\'s wait and approval events into the package\'s parts, and sends an answer back as resume', async () => {
    const sse = [
      `event: tool\ndata: ${JSON.stringify({ phase: 'call', id: 'call_1', name: 'stop_cluster', args: '{"clusterName":"cluster-a"}' })}`,
      `event: tool\ndata: ${JSON.stringify({ phase: 'wait', id: 'call_1', name: 'stop_cluster' })}`,
      `event: approval\ndata: ${JSON.stringify({ approval })}`,
      `event: done\ndata: ${JSON.stringify({ finishReason: 'tool_approval', messageId: 'srv-1' })}`,
    ].join('\n\n') + '\n\n'
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(sse, { status: 200 }))
    const parts: unknown[] = []
    const out = await createStudioAdapter().streamCompletion(
      { model: 'm', messages: [], conversationId: 'c1', userMessageId: 'r1', resume: { messageId: 'srv-1', toolCallId: 'call_1', allowed: true, tool: 'destroy_cluster' } },
      { onContent: () => {}, onPart: d => parts.push(d) },
    )
    expect(parts).toEqual([
      { type: 'tool_start', id: 'call_1', name: 'stop_cluster', args: '{"clusterName":"cluster-a"}' },
      { type: 'tool_wait', id: 'call_1' },
      { type: 'approval_request', approval },
    ])
    expect(out.finishReason).toBe('tool_approval')
    const sent = JSON.parse(String(fetchMock.mock.calls[0][1]?.body))
    expect(sent.resume).toEqual({ messageId: 'srv-1', toolCallId: 'call_1', allowed: true, tool: 'destroy_cluster' })
  })
})

describe('in the chat', () => {
  // jsdom has no layout: the thread scrolls and measures as it renders.
  beforeAll(() => {
    if (!Element.prototype.scrollTo) Element.prototype.scrollTo = function scrollTo() {}
    if (!globalThis.ResizeObserver) globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} } as never
  })
  // A conversation paused on a held call, as the server returns it.
  const conversation = () => ({
    id: 'c1', title: 'clusters', activeBranchId: 'srv-1', createdAt: '2026-10-21T00:00:00Z', isOwner: true, canCollaborate: true,
    messages: [
      { id: 'u1', role: 'user', content: 'stop cluster-a', parentId: null, timestamp: '2026-10-21T00:00:00Z' },
      { id: 'srv-1', role: 'assistant', content: null, parentId: 'u1', timestamp: '2026-10-21T00:00:01Z', model: 'm',
        parts: [{ kind: 'tool_call', id: 'call_1', name: 'stop_cluster', args: '{"clusterName":"cluster-a"}', status: 'waiting' }, approval] },
    ],
  })
  function mount() {
    const requests: StreamRequest[] = []
    const adapter = {
      conversations: {
        list: async () => [{ id: 'c1', title: 'clusters', createdAt: '2026-10-21T00:00:00Z', updatedAt: '2026-10-21T00:00:00Z', isOwner: true, canCollaborate: true }],
        get: async () => conversation(),
        create: async () => ({ id: 'c2' }),
        rename: async () => {},
        remove: async () => {},
      },
      models: { list: async () => ({ models: [{ id: 'm', name: 'Model' }], unreachableSessions: [], providerIssues: [] }) },
      streamCompletion: async (req: StreamRequest, handlers: { onPart?: (d: unknown) => void; onContent: (t: string) => void }) => {
        requests.push(req)
        handlers.onPart?.({ type: 'tool_end', id: 'call_1', result: 'stopped cluster-a' })
        handlers.onContent('Stopped.')
        return { content: 'Stopped.', messageId: 'srv-2', toolCalls: [], finishReason: 'stop', model: 'm', reasoning: '', responsesOutput: [] }
      },
    } as unknown as ChatAdapter
    render(
      <ChatProvider
        adapter={adapter}
        currentUser={{ id: 'u-alex', username: 'alex', name: 'Alex' } as never}
        navigation={{ toConversation: () => {}, toNewChat: () => {} }}
        activeConversationId="c1"
        config={{ slots: { partRenderers: { approval: ToolApproval } } }}
      >
        <ChatThread conversationId="c1" />
      </ChatProvider>,
    )
    return requests
  }

  it('an answer resumes the reply with the held call\'s ids, and the alternative rides as resume.tool', async () => {
    const requests = mount()
    const allow = await screen.findByRole('button', { name: 'Yes, allow this call' }, { timeout: 5000 })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Destroy it instead' })).toBeInTheDocument())
    await act(async () => { fireEvent.click(allow) })
    await waitFor(() => expect(requests).toHaveLength(1))
    expect(requests[0].resume).toEqual({ messageId: 'srv-1', toolCallId: 'call_1', allowed: true })
    expect(requests[0].parentMessageId).toBe('srv-1')
  })

  it('running the alternative sends its name, not new arguments', async () => {
    const requests = mount()
    const instead = await screen.findByRole('button', { name: 'Destroy it instead' }, { timeout: 5000 })
    await act(async () => { fireEvent.click(instead) })
    await waitFor(() => expect(requests).toHaveLength(1))
    expect(requests[0].resume).toEqual({ messageId: 'srv-1', toolCallId: 'call_1', allowed: true, tool: 'destroy_cluster' })
  })
})
