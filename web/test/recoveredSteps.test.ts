import { describe, expect, it } from 'vitest'
import { RECOVERED_NOTE, foldRecovered, recoveryTracker } from '../src/recoveredSteps'
import { markNotes } from '../src/adapter'

const call = (id: string, path: string, status: string, args?: string) =>
  ({ kind: 'tool_call', id, name: 'write_kb_file', args: args ?? JSON.stringify({ path }), status, result: status === 'error' ? 'error: Could not write' : 'Wrote' })

// The run from the report: the first write fails on the model's JSON, and
// the next writes of the same workbook go through.
const run = () => [
  call('w1', 'emu.xlsx', 'error', '{"path":"emu.xlsx","rows":[{"a":'),
  call('w2', 'emu.xlsx', 'ok'),
  call('w3', 'emu.xlsx', 'ok'),
]

describe('a failed step the turn recovered from', () => {
  it('is folded with the other steps when the chat is opened, and still says it failed', () => {
    const chat = markNotes({ messages: [{ id: 'a1', content: 'Done.', parts: run() }] })
    const parts = chat.messages[0]!.parts!
    expect(parts[0]).toMatchObject({ status: 'ok', result: `${RECOVERED_NOTE}error: Could not write` })
    // The thread folds a run of settled calls into one row.
    expect(parts.every(p => p.status === 'ok')).toBe(true)
  })
  it('is folded as soon as the call that recovers it ends, while the reply streams', () => {
    const t = recoveryTracker()
    t.start('w1', 'write_kb_file', '{"path":"emu.xlsx","rows":[{"a":')
    expect(t.end('w1', 'error: Could not write', true)).toEqual([])
    t.start('w2', 'write_kb_file', '{"path":"emu.xlsx"}')
    expect(t.end('w2', 'Wrote', false)).toEqual([{ id: 'w1', result: `${RECOVERED_NOTE}error: Could not write` }])
  })
  it('stays a failure on its own row when nothing redid it', () => {
    const parts = foldRecovered([call('w1', 'emu.xlsx', 'error'), call('w2', 'other.xlsx', 'ok'), call('w3', 'other.xlsx', 'ok')])
    expect(parts[0]!.status).toBe('error')
    const t = recoveryTracker()
    t.start('w1', 'write_kb_file', '{"path":"emu.xlsx"}'); t.end('w1', 'error', true)
    t.start('w2', 'read_kb_file', '{"path":"emu.xlsx"}')
    expect(t.end('w2', 'text', false)).toEqual([])
  })
})

describe('the chat stream', () => {
  it('settles the failed write when the next write of the same file goes through', async () => {
    const { createStudioAdapter } = await import('../src/adapter')
    const sse = [
      ['tool', { phase: 'call', id: 'w1', name: 'write_kb_file', args: '{"path":"emu.xlsx","rows":[' }],
      ['tool', { phase: 'end', id: 'w1', summary: 'error: Could not write', error: true }],
      ['tool', { phase: 'call', id: 'w2', name: 'write_kb_file', args: '{"path":"emu.xlsx"}' }],
      ['tool', { phase: 'end', id: 'w2', summary: 'Wrote' }],
      ['done', { finishReason: 'stop' }],
    ].map(([e, d]) => `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`).join('')
    const real = globalThis.fetch
    globalThis.fetch = (async () => new Response(sse, { status: 200 })) as typeof fetch
    const ends: unknown[] = []
    try {
      await createStudioAdapter().streamCompletion!({ conversationId: 'c', model: 'm', messages: [] } as never,
        { onContent: () => {}, onReasoning: () => {}, onPart: (p: { type: string }) => { if (p.type === 'tool_end') ends.push(p) }, onDone: () => {}, onError: () => {} } as never,
        new AbortController().signal).catch(() => {})
    } finally { globalThis.fetch = real }
    expect(ends).toEqual([
      { type: 'tool_end', id: 'w1', result: 'error: Could not write', isError: true },
      { type: 'tool_end', id: 'w2', result: 'Wrote', isError: false },
      { type: 'tool_end', id: 'w1', result: `${RECOVERED_NOTE}error: Could not write`, isError: false },
    ])
  })
})
