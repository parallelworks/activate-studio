import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryView } from '../src/views/QueryView'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const builder = { source: 'files', filters: [{ field: 'extension', op: 'eq', value: 'pdf' }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 100, subtree: '' }
const SAVED = [
  { id: 'starter-pdfs', name: 'Largest PDFs', createdAt: '', payload: { builder } },
  { id: 'q-raw', name: 'Raw names', createdAt: '', payload: { sql: 'SELECT name FROM vrpentries;', subtree: '' } },
]
const COMPILED = {
  stages: [
    { flag: '-I', sql: 'CREATE TABLE t (path TEXT, size INT64, mtime INT64);' },
    { flag: '-K', sql: 'CREATE TABLE agg (path TEXT, size INT64, mtime INT64);' },
    { flag: '-E', sql: "INSERT INTO t SELECT path, size, mtime FROM vrpentries WHERE ext = 'pdf' ORDER BY size DESC LIMIT 100;" },
    { flag: '-J', sql: 'INSERT INTO agg SELECT * FROM t;' },
    { flag: '-G', sql: "SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY size DESC LIMIT 100;" },
  ],
  commands: [{ engine: 'gufi_query', command: "/opt/gufi/bin/gufi_query -n 8 -a 1 -E 'INSERT INTO t ...' /ix/kb" }],
}

let asked: unknown[] = []
beforeEach(() => {
  asked = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/query/saved') return json(SAVED)
    if (url === '/api/query/sql') {
      const body = JSON.parse(String(init?.body))
      asked.push(body)
      if (body.sql) return json({ stages: [{ flag: '-E', sql: body.sql }], commands: [{ engine: 'gufi_query', site: 'cluster-a', command: `/c/bin/gufi_query -n 4 -E '${body.sql}' /c/idx` }] })
      return json(COMPILED)
    }
    if (url === '/api/config') return json({ appName: 'Studio', libraries: [] })
    return json({})
  }))
})
afterEach(() => vi.unstubAllGlobals())

describe('a saved query\'s SQL', () => {
  it('shows each gufi_query stage the builder compiles to and the command, each with a copy button', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'SQL for Largest PDFs' }))
    const panel = await screen.findByRole('dialog', { name: 'SQL for Largest PDFs' })
    expect(await within(panel).findByText(COMPILED.stages[2].sql)).toBeInTheDocument()
    for (const s of COMPILED.stages) {
      expect(within(panel).getByText(s.sql)).toBeInTheDocument()
      expect(within(panel).getByRole('button', { name: `Copy ${s.flag} SQL` })).toBeInTheDocument()
    }
    expect(within(panel).getByText(COMPILED.commands[0].command)).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: 'Copy GUFI command' })).toBeInTheDocument()
    expect(within(panel).getByText(/on the Studio host/)).toBeInTheDocument()
    // Compiled from the saved payload itself, not the form on screen.
    expect(asked).toEqual([{ builder }])
  })

  it('raw SQL is its one statement, with where its command runs', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'SQL for Raw names' }))
    const panel = await screen.findByRole('dialog', { name: 'SQL for Raw names' })
    expect(await within(panel).findByText('SELECT name FROM vrpentries;')).toBeInTheDocument()
    expect(within(panel).getByText(/on cluster-a/)).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Copy -G SQL' })).toBeNull()
  })

  it('says why when the SQL cannot be read', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => (String(input) === '/api/query/saved'
      ? json(SAVED) : String(input) === '/api/query/sql' ? json({ message: 'unknown field: owner' }, 400) : json({}))))
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'SQL for Largest PDFs' }))
    expect(await screen.findByText(/unknown field: owner/)).toBeInTheDocument()
  })
})
