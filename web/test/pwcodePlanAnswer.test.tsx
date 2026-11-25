import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { MessagePart } from '@parallelworks/ui/ai'
import { PlanAnswer, openPlan } from '../src/pwcode/session/PlanAnswer'

const plan = (id: string, resolved?: boolean) => ({ kind: 'approval', id, approvalKind: 'plan', plan: 'one, two, three', ...(resolved ? { resolved } : {}) })

afterEach(() => { vi.unstubAllGlobals() })

describe('A plan pw code waits on in a chat', () => {
  it('is the open plan approval of the turn in flight', () => {
    const parts = (...p: unknown[]) => p as MessagePart[]
    expect(openPlan(parts(plan('p1')))).toEqual({ id: 'p1', plan: 'one, two, three' })
    expect(openPlan(parts({ kind: 'approval', id: 'a1', approvalKind: 'confirm' }, plan('p2')))?.id).toBe('p2')
    expect(openPlan(parts(plan('p1', true)))).toBeNull()
    expect(openPlan(parts({ kind: 'approval', id: 'a1', approvalKind: 'confirm' }))).toBeNull()
    expect(openPlan(null)).toBeNull()
  })

  it('is approved here, or sent back with what to change', async () => {
    const calls: { url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, body: JSON.parse(String(init?.body)) })
      return new Response('{"ok":true}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const { unmount } = render(<PlanAnswer approvals="/api/chat/pwcode/c1/approvals" approvalId="p1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve plan' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0]).toEqual({ url: '/api/chat/pwcode/c1/approvals/p1', body: { kind: 'plan', allowed: true } })
    await waitFor(() => expect(screen.queryByTestId('pwcode-plan-answer')).toBeNull())
    unmount()

    render(<PlanAnswer approvals="/api/chat/pwcode/c1/approvals" approvalId="p2" />)
    fireEvent.change(screen.getByRole('textbox', { name: 'What to change in the plan' }), { target: { value: 'Use four lines' } })
    fireEvent.click(screen.getByRole('button', { name: 'Keep planning' }))
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1].body).toEqual({ kind: 'plan', allowed: false, message: 'Use four lines' })
  })

  it('says so when the answer does not go through, and can be sent again', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"error":"pw code did not answer"}', { status: 502, headers: { 'Content-Type': 'application/json' } })))
    render(<PlanAnswer approvals="/api/chat/pwcode/c1/approvals" approvalId="p1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Approve plan' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('pw code did not answer')
    expect(screen.getByRole('button', { name: 'Approve plan' })).toBeEnabled()
  })
})
