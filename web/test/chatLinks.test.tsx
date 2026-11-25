import { readFileSync } from 'node:fs'
import { act, render, screen } from '@testing-library/react'
import { Markdown } from '@parallelworks/ui'
import { beforeEach, describe, expect, it } from 'vitest'
import { markNotes } from '../src/adapter'
import { forgetEngines, knowEngine } from '../src/chatEngine'
import { ChatMarkdownLink } from '../src/chatLinks'

beforeEach(() => { forgetEngines() })

// The note opening a chat continued from a pw code session said its
// messages go to that session after the Studio assistant had taken the
// chat over.
describe("A continued chat's opening note", () => {
  const chat = () => ({
    id: 'c4',
    messages: [{ id: 'note-continued-1', content: 'Continued from the pw code session **x** on box-a. Messages you send here go to that session. [See the session on the Code page](#view=code?machine=m1&session=s1)' }],
  })

  it('says where messages go as what answers the chat changes', () => {
    const c = markNotes(markNotes(chat()))
    const link = c.messages[0].content!.match(/\[Messages you send here go to that session\.\]\(([^)]+)\)/)
    expect(link).toBeTruthy()
    expect(c.messages[0].content!.split('note-goes-to')).toHaveLength(2)
    render(<p><ChatMarkdownLink href={link![1]}>Messages you send here go to that session.</ChatMarkdownLink></p>)
    expect(screen.getByText('Messages you send here go to that session.')).toBeInTheDocument()
    act(() => knowEngine('c4', 'studio'))
    expect(screen.getByText('The Studio answers this chat now, so messages you send here do not go to that session.')).toBeInTheDocument()
    act(() => knowEngine('c4', 'pwcode'))
    expect(screen.getByText('Messages you send here go to that session.')).toBeInTheDocument()
    expect(screen.queryByRole('link')).toBeNull()
  })
})

// A link to a place in the Studio, the fallback note's to Settings,
// Accounts among them, said it opens in a new tab but opened in this one.
describe('A link in a chat message', () => {
  it('to a place in the Studio, opens here and is not said to open a new tab', () => {
    // As the markdown hands it over: with a new tab's target.
    render(<p><ChatMarkdownLink href="#view=settings:accounts?open=activate&note=engine" target="_blank" rel="noopener noreferrer">Settings, Accounts</ChatMarkdownLink></p>)
    const a = screen.getByRole('link', { name: 'Settings, Accounts' })
    expect(a).not.toHaveAttribute('target')
    expect(a).not.toHaveAttribute('rel')
    expect(a).toHaveTextContent(/^Settings, Accounts$/)
    expect(a).toHaveAttribute('href', '#view=settings:accounts?open=activate&note=engine')
  })

  // Streamdown 2.8 takes a link that starts with # for one to a heading of
  // its own and puts its headings' prefix on it.
  it('to a place in the Studio, keeps its address', async () => {
    render(<Markdown components={{ a: ChatMarkdownLink }}>{'[the notes](#open=file:a/notes.md&q=inlet) and [the run](#chat=c9)'}</Markdown>)
    expect(await screen.findByRole('link', { name: 'the notes' }, { timeout: 10_000 })).toHaveAttribute('href', '#open=file:a/notes.md&q=inlet')
    expect(screen.getByRole('link', { name: 'the run' })).toHaveAttribute('href', '#chat=c9')
  }, 20_000)

  it('to anywhere else, opens a new tab and says so', () => {
    render(<p><ChatMarkdownLink href="https://example.com/doc">the doc</ChatMarkdownLink></p>)
    const a = screen.getByRole('link', { name: /the doc/ })
    expect(a).toHaveAttribute('target', '_blank')
    expect(a).toHaveTextContent('(opens in a new tab)')
  })

  it("in the notes, is underlined, not told by colour alone", () => {
    const css = readFileSync('src/styles.css', 'utf8')
    for (const mark of ['note=engine', 'note=continued']) {
      const rule = css.split('}').find(r => r.includes(`a[href*="${mark}"]`) && r.includes('.chat-ink a') && r.includes('text-decoration: underline'))
      expect(rule, mark).toBeTruthy()
    }
  })
})
