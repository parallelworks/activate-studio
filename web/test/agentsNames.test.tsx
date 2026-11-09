import { readFileSync } from 'node:fs'
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentsView } from '../src/views/AgentsView'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
const LONG = 'principal_proposal_compliance_reviewer_f'

beforeEach(() => {
  localStorage.setItem('ade-agents-page', 'library')
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url.startsWith('/api/extensions')) {
      return json({ dir: '/kb/.agents', tools: [], personas: [{ name: LONG, description: 'Reviews drafts.', shared: true }], skills: [{ name: 'quarterly_cash_forecast_and_collections', description: 'Forecasts.' }] })
    }
    return json({ runs: [], tasks: [], agents: [] })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

describe('names on the Agents page', () => {
  it('shows a long persona or skill name in full, with the name as its title', async () => {
    render(<AgentsView onOpen={() => {}} />)
    const persona = await screen.findByTitle(LONG)
    expect(persona).toHaveTextContent(LONG)
    expect(persona).toHaveClass('agents-name')
    expect(screen.getByTitle('quarterly_cash_forecast_and_collections')).toHaveTextContent('quarterly_cash_forecast_and_collections')
  })

  it('lets the name wrap instead of clipping it, and stacks it above the description in a narrow card', () => {
    const css = readFileSync('src/styles.css', 'utf8')
    expect(css).toMatch(/\.agents-view \.ov-row-path \{ flex: 0 0 auto; max-width: 50%; white-space: normal; overflow: visible; \}/)
    expect(css).toMatch(/\.agents-name \{[^}]*overflow-wrap: anywhere/)
    expect(css).toMatch(/@container \(max-width: 520px\) \{\s*\.agents-view \.ov-row \{ flex-direction: column/)
  })
})
