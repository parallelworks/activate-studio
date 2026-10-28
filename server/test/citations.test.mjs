// Citation links in replies, repaired as they stream: the prompt's
// placeholder copied into a path, a citation left without its target, a
// path the tools returned written as a bare [path], percent-encoding in
// the text, and a file of another library linked without its library.
// Everything else in brackets is left as the model wrote it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'cite-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'cite-ix-'))
const { repairCitations, CitationStream, openTarget } = await import('../dist/chat/citations.js')

const ctx = (known = {}, library = null) => ({ known: new Map(Object.entries(known)), library })
const fix = (text, c = ctx()) => repairCitations(text, c)

test('the placeholder path becomes a viewer link, with %20 in the target only', () => {
  assert.equal(fix('[relative/path/morepics/R&D-2.jpg]'), '[morepics/R&D-2.jpg](#open=file:morepics/R%26D-2.jpg)')
  assert.equal(fix('See [relative/path/projects%202024/vendor/spread-tree.pptx].'),
    'See [projects 2024/vendor/spread-tree.pptx](#open=file:projects%202024/vendor/spread-tree.pptx).')
  assert.equal(fix('[relative/path/a b.md](#open=file:relative/path/a%20b.md)'), '[a b.md](#open=file:a%20b.md)')
  assert.equal(fix('[the notes](relative/path/notes/a.md)'), '[the notes](#open=file:notes/a.md)')
  assert.equal(fix('[relative/path/file (1).pdf]'), '[file (1).pdf](#open=file:file%20%281%29.pdf)', 'parentheses kept out of the link destination')
})

test('a bare [path] is linked only when the tools returned it', () => {
  const c = ctx({ 'notes/a b.md': null })
  assert.equal(fix('From [notes/a b.md], and [notes/other.md].', c), 'From [notes/a b.md](#open=file:notes/a%20b.md), and [notes/other.md].')
  assert.equal(fix('[./notes/a b.md]', c), '[notes/a b.md](#open=file:notes/a%20b.md)')
  assert.equal(fix('[notes/a%20b.md]', c), '[notes/a b.md](#open=file:notes/a%20b.md)')
  assert.equal(fix('a link to a bare path [here](notes/a b.md)', c), 'a link to a bare path [here](#open=file:notes/a%20b.md)')
})

test('a file of another library opens in that library', () => {
  const c = ctx({ 'specs/pump.pdf': 'archive', 'readme.md': null })
  assert.equal(fix('[specs/pump.pdf]', c), '[specs/pump.pdf](#open=file:specs/pump.pdf&lib=archive)')
  assert.equal(fix('[specs/pump.pdf](#open=file:specs/pump.pdf)', c), '[specs/pump.pdf](#open=file:specs/pump.pdf&lib=archive)')
  assert.equal(fix('[specs/pump.pdf](#open=file:specs/pump.pdf&lib=other)', c), '[specs/pump.pdf](#open=file:specs/pump.pdf&lib=other)', 'a library the link names is kept')
  assert.equal(fix('[p](#open=file:specs/pump.pdf&q=flow)', c), '[p](#open=file:specs/pump.pdf&q=flow&lib=archive)', 'the query is kept')
  assert.equal(fix('[readme.md](#open=file:readme.md)', c), '[readme.md](#open=file:readme.md)', 'the main folder needs no library')
  // A path no tool returned is taken to be in the library the conversation searches.
  const inShelf = ctx({}, 'shelf')
  assert.equal(fix('[relative/path/x.md]', inShelf), '[x.md](#open=file:x.md&lib=shelf)')
  assert.equal(fix('[x.md](#open=file:x.md)', inShelf), '[x.md](#open=file:x.md&lib=shelf)')
  assert.equal(fix('[x.md](#open=file:x.md)', ctx({}, 'all')), '[x.md](#open=file:x.md)')
  assert.equal(openTarget('a.md', 'kb'), '#open=file:a.md')
})

test('everything else in brackets is left as written', () => {
  const c = ctx({ 'a.md': null })
  for (const s of [
    '[a.md](#open=file:a.md)', '[1] and [x] and [ ]', '- [ ] a task', '[a.md][1]', '[a.md]: https://example.com/a',
    '![a.md](/api/kb/raw?path=a.md)', '`[a.md]`', '\\[a.md]', '[site](https://example.com/relative/path/x)',
    '[runs](#view=agents:t1)', 'arr[i] = [relative/path', '[relative/path.md](#open=file:relative/path.md)',
  ]) assert.equal(fix(s, c), s, s)
})

test('streamed in pieces, the text comes out as it would whole, held only while a bracket is open on its line', () => {
  const c = ctx({ 'notes/a b.md': 'shelf' })
  const text = 'One [relative/path/x%20y.pdf] two [notes/a b.md] three [t](relative/path/z.md) four [1] five ![i](/x.png) six [open\nline] end [notes/a b.md]'
  const whole = fix(text, c)
  assert.match(whole, /^One \[x y\.pdf\]\(#open=file:x%20y\.pdf\) two \[notes\/a b\.md\]\(#open=file:notes\/a%20b\.md&lib=shelf\) three \[t\]\(#open=file:z\.md\) four \[1\]/)
  for (let i = 1; i < text.length; i++) {
    const s = new CitationStream(c)
    assert.equal(s.push(text.slice(0, i)) + s.push(text.slice(i)) + s.flush(), whole, `split at ${i}`)
  }
  const s = new CitationStream(c)
  assert.equal(s.push('see [notes/a b'), 'see ', 'held while open')
  assert.equal(s.push('.md] and'), '[notes/a b.md](#open=file:notes/a%20b.md&lib=shelf) and')
  assert.equal(s.push(' [no close\non this line'), ' [no close\non this line', 'a line ends it')
  assert.equal(s.push(' [x'), ' ')
  s.drop()
  assert.equal(s.flush(), '', 'dropped with a retried turn')
})
