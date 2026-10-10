import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { parseHelp } from '../src/views/HelpView'

describe('the user guide rail', () => {
  it('groups pages under # headings and keeps the intro, ### included, as the Overview', () => {
    const { intro, sections } = parseHelp('Intro line.\n\n### Start here\n\n1. Ask.\n\n# Basics\n\n## Chat\n\nAsk things.\n\n## Search\n\nFind things.\n\n# More\n\n## The index\n\nGUFI.\n')
    expect(intro).toBe('Intro line.\n\n### Start here\n\n1. Ask.')
    expect(sections.map(s => [s.group, s.title, s.body])).toEqual([
      ['Basics', 'Chat', 'Ask things.'],
      ['Basics', 'Search', 'Find things.'],
      ['More', 'The index', 'GUFI.'],
    ])
  })

  it('reads a guide without groups as one flat list, as a deployment\'s own help file may be', () => {
    const { intro, sections } = parseHelp('About.\n\n## One\n\nA.\n\n## Two\n\nB.\n')
    expect(intro).toBe('About.')
    expect(sections.map(s => [s.group, s.title])).toEqual([[null, 'One'], [null, 'Two']])
  })

  it('puts every page of the shipped guide in a group', () => {
    const md = readFileSync(path.resolve(process.cwd(), '../docs/HELP.md'), 'utf8')
    const { intro, sections } = parseHelp(md)
    expect(intro).toMatch(/### Start here/)
    expect(sections.length).toBeGreaterThan(8)
    expect(sections.every(s => s.group)).toBe(true)
  })
})
