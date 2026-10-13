import { describe, expect, it } from 'vitest'
import { latestRunSlug } from '../src/components/RunPane'

describe('the run the chat follows', () => {
  it('is the newest run a run tool launched or followed, streaming parts included', () => {
    const messages = [
      { parts: [{ kind: 'tool_call', name: 'run_workflow', result: 'run submitted: study-doe-00004' }] },
      { parts: [{ kind: 'tool_call', name: 'search_kb', result: 'notes-00009 matched' }, { kind: 'tool_call', name: 'watch_run', result: 'study-doe-00005: running\n\nlater text' }] },
    ]
    expect(latestRunSlug(messages)).toBe('study-doe-00005')
    expect(latestRunSlug(messages, [{ kind: 'tool_call', name: 'run_workflow', result: 'run submitted: study-doe-00006' }])).toBe('study-doe-00006')
  })

  it('is nothing when no run tool named one', () => {
    expect(latestRunSlug([{ parts: [{ kind: 'tool_call', name: 'run_workflow', result: 'dry run ok' }] }])).toBeNull()
    expect(latestRunSlug([{ parts: null }])).toBeNull()
  })
})
