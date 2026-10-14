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

  it('takes the id as the tool wrote it, numbered or not', () => {
    expect(latestRunSlug([{ parts: [{ kind: 'tool_call', name: 'run_workflow', result: 'run submitted: gh-owner-workflows' }] }])).toBe('gh-owner-workflows')
    expect(latestRunSlug([{ parts: [{ kind: 'tool_call', name: 'watch_run', result: 'gh-owner-workflows: completed' }] }])).toBe('gh-owner-workflows')
    expect(latestRunSlug([{ parts: [{ kind: 'tool_call', name: 'watch_run', result: 'not found' }] }])).toBeNull()
  })

  it('is nothing when no run tool named one', () => {
    expect(latestRunSlug([{ parts: [{ kind: 'tool_call', name: 'run_workflow', result: 'dry run ok' }] }])).toBeNull()
    expect(latestRunSlug([{ parts: null }])).toBeNull()
  })
})

describe('which run a session belongs to, on the client', async () => {
  const { sessionOfRun } = await import('../src/components/SessionView')
  it('is the recorded run, or the run its name ends with', () => {
    expect(sessionOfRun({ name: 'viewer-gh-owner-workflows-2', run: null }, 'gh-owner-workflows-2')).toBe(true)
    expect(sessionOfRun({ name: 'viewer-gh-owner-workflows-2', run: null }, 'gh-owner-workflows')).toBe(false)
    expect(sessionOfRun({ name: 'viewer-x', run: { slug: 'study-00001', workflow: 'w', number: 1, status: null } }, 'study-00001')).toBe(true)
  })
})
