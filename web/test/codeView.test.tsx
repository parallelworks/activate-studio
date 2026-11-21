import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ago, argSummary, groupSessions, shortPath, Transcript, type CodeSession } from '../src/views/CodeView'

const row = (o: Partial<CodeSession>): CodeSession => ({ machineId: 'm', machine: 'm1', id: 'x', title: 't', messages: 1, status: 'idle', url: '', ...o })

describe('Code sessions', () => {
  it('groups what needs an answer first and archived last, pinned first within a group', () => {
    const g = groupSessions([
      row({ id: 'a', status: 'idle', updatedAt: '2026-11-12T10:00:00Z' }),
      row({ id: 'b', status: 'running', updatedAt: '2026-11-12T09:00:00Z' }),
      row({ id: 'c', status: 'awaiting_approval' }),
      row({ id: 'd', status: 'idle', archived: true }),
      row({ id: 'e', status: 'idle', pinned: true, updatedAt: '2026-11-01T00:00:00Z' }),
    ])
    expect(g.map(x => [x.key, x.rows.map(r => r.id).join('')])).toEqual([['needs', 'c'], ['working', 'b'], ['recent', 'ea'], ['archived', 'd']])
  })
  it('names times, paths and tool arguments briefly', () => {
    const now = Date.parse('2026-11-12T12:00:00Z')
    expect(ago('2026-11-12T11:58:00Z', now)).toBe('2m')
    expect(ago('2026-11-10T12:00:00Z', now)).toBe('2d')
    expect(shortPath('/home/u/work/project/src')).toBe('…/project/src')
    expect(argSummary('{"path":"a/b.go","limit":5}')).toBe('a/b.go')
    expect(argSummary('{"x":1}')).toBe('')
  })
  it('reads tool arguments the server cut short, and never shows them raw', () => {
    // The server keeps the first 600 characters, so long arguments are not JSON.
    const cut = '{"command":"ssh host \'python3 -c \\"\\n# Check the coordinates\\nimport os\\nprint(1)'
    expect(argSummary(cut, 'Bash')).toBe("ssh host 'python3 -c \" …")
    expect(argSummary('{"todos":[{"activeForm":"Checking the mesh","content":"Check the mesh","status":"completed"},{"activeForm":"Validating the domain","status":"in_progress"', 'TodoWrite'))
      .toBe('Validating the domain')
    expect(argSummary('{"todos":[{"activeForm":"Checking the mesh","content":"Check the mesh","status":"in_progress"}]}', 'TodoWrite')).toBe('Checking the mesh')
    expect(argSummary('{"pattern":"radarAxes","path":"app/explorer.html"}', 'GrepSearch')).toBe('radarAxes in app/explorer.html')
    expect(argSummary('{"file_path":"app/explorer.html","old_string":"a","new_string":" .d-actions { display: flex', 'EditFile')).toBe('app/explorer.html')
    expect(argSummary('{"new_string":" .d-actions { display: flex; }\\n .d-note { margin: 0', 'EditFile')).toBe('.d-actions { display: flex; } …')
  })
  it('folds a run of tool calls into one line that opens to each', () => {
    const call = (n: string, a: string) => ({ kind: 'tool_call', toolName: n, toolArgs: a })
    const { container } = render(<Transcript earlier={0} items={[
      { kind: 'user', text: 'Tidy the explorer' },
      call('ReadFile', '{"path":"a.html"}'), { kind: 'tool_result', text: '<html>' },
      call('ReadFile', '{"path":"a.html"}'),
      call('EditFile', '{"path":"a.html"}'),
      call('Bash', '{"command":"git status -s"}'),
      { kind: 'assistant', text: 'Done.' },
      call('ReadFile', '{"path":"b.html"}'),
    ]} />)
    const runs = container.querySelectorAll('details.code-tool-run')
    expect(runs.length).toBe(1)
    expect(runs[0].querySelector('summary')?.textContent).toBe('4 tool calls ReadFile 2, EditFile, Bash')
    expect(runs[0].querySelectorAll('.code-tool-run-body details.code-tool').length).toBe(4)
    expect((runs[0] as HTMLDetailsElement).open).toBe(false)
    // A lone call after the reply stays a line of its own.
    expect(container.querySelectorAll('.code-transcript > details.code-tool:not(.code-tool-run)').length).toBe(1)
  })
  it('folds each tool result under its call', () => {
    const { container } = render(<Transcript earlier={2} items={[
      { kind: 'user', text: 'Fix the build' },
      { kind: 'tool_call', toolName: 'ReadFile', toolArgs: '{"path":"go.mod"}' },
      { kind: 'tool_result', text: 'module x' },
      { kind: 'assistant', text: 'Done: **fixed**.' },
    ]} />)
    expect(container.querySelectorAll('details.code-tool').length).toBe(1)
    expect(container.querySelector('details pre')?.textContent).toBe('module x')
    expect(container.querySelector('.code-msg.user')?.textContent).toBe('Fix the build')
    expect(container.textContent).toContain('2 earlier items')
  })
})
