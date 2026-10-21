// Release notes and the change log as entries: the format
// scripts/release-notes.mjs writes, housekeeping entries left out, headings
// inside code blocks ignored, and the change log this repository carries.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const { parseNotes, parseChangelog, readChangelog } = await import('../dist/changelog.js')

const NOTES = `### Change log through v1.141 (#505)

Regenerated with \`node scripts/release-notes.mjs --changelog\` after the v1.141 release.

### Search: faster on large libraries (#506)

Search read every folder's database in turn.

## Fix

It reads them in parallel.

\`\`\`sh
### not a heading (#999)
## v9.9 (2026-01-01)
\`\`\`

Verified: the search tests.

### Settings: a switch for the sidebar (#507)

Each page has a switch.
`

test('notes: one entry per pull request, housekeeping left out, code blocks kept whole', () => {
  const entries = parseNotes(NOTES)
  assert.deepEqual(entries.map(e => [e.title, e.pr]), [
    ['Search: faster on large libraries', 506],
    ['Settings: a switch for the sidebar', 507],
  ])
  assert.match(entries[0].body, /^Search read every folder's database in turn\./)
  assert.match(entries[0].body, /## Fix\n\nIt reads them in parallel\./, 'a heading in a description stays in it')
  assert.match(entries[0].body, /### not a heading \(#999\)\n## v9\.9 \(2026-01-01\)\n```/, 'nor does a fenced block end the entry')
  assert.match(entries[0].body, /Verified: the search tests\.$/)
  assert.equal(entries[1].body, 'Each page has a switch.')
})

test('notes: a tag with no pull requests has none; notes written by hand are one entry', () => {
  assert.deepEqual(parseNotes('_No pull requests; tag only._\n'), [])
  assert.deepEqual(parseNotes(''), [])
  assert.deepEqual(parseNotes('### Change log through v1.2 (#3)\n\nRegenerated.\n'), [])
  assert.deepEqual(parseNotes('## Changes\n\n- Faster search.\n'), [{ title: 'Release notes', pr: null, body: '## Changes\n\n- Faster search.' }])
  assert.deepEqual(parseNotes('### #12\n\n'), [{ title: 'Pull request #12', pr: 12, body: '' }])
  assert.deepEqual(parseNotes('### Windows line ends (#4)\r\n\r\nBody.\r\n').map(e => [e.title, e.body]), [['Windows line ends', 'Body.']])
})

test('change log: versions newest first with their dates and entries', () => {
  const md = `# Change log

Every version, newest first.

## v1.142 (2026-10-21)

${NOTES}
## v1.141 (2026-10-20)

### Change log through v1.140 (#500)

Regenerated.

## v1.140 (2026-10-19)

_No pull requests; tag only._

## v1.139 (2026-10-18)

### Chat: retry a reply (#498)

A reply that failed offers Retry.
`
  const versions = parseChangelog(md)
  assert.deepEqual(versions.map(v => [v.version, v.date, v.entries.length]), [
    ['v1.142', '2026-10-21', 2],
    ['v1.141', '2026-10-20', 0],
    ['v1.140', '2026-10-19', 0],
    ['v1.139', '2026-10-18', 1],
  ])
  assert.ok(!versions[0].entries.some(e => /^v9/.test(e.title)), 'the version heading in a code block is not a version')
  assert.deepEqual(versions[3].entries[0], { title: 'Chat: retry a reply', pr: 498, body: 'A reply that failed offers Retry.' })
})

test("this repository's CHANGELOG.md parses: every version, no housekeeping, every entry a pull request", () => {
  const md = fs.readFileSync(path.join(REPO, 'CHANGELOG.md'), 'utf8')
  const versions = parseChangelog(md)
  const headings = md.split('\n').filter(l => /^## v\d+\.\d+/.test(l)).length
  assert.ok(versions.length > 100)
  assert.equal(versions.length, headings)
  const entries = versions.flatMap(v => v.entries)
  assert.ok(entries.length > 100)
  assert.ok(entries.every(e => !/^change log through/i.test(e.title)))
  assert.ok(entries.every(e => Number.isInteger(e.pr) && e.title && !/\(#\d+\)$/.test(e.title)))
})

test('the bundled change log: read from the file, and reported missing when the build has none', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'changelog-'))
  const file = path.join(dir, 'CHANGELOG.md')
  const before = process.env.STUDIO_CHANGELOG_FILE
  try {
    process.env.STUDIO_CHANGELOG_FILE = file
    assert.deepEqual(readChangelog(), { included: false, versions: [] })
    fs.writeFileSync(file, '# Change log\n\n## v1.2 (2026-01-02)\n\n### A change (#2)\n\nIt changed.\n')
    assert.deepEqual(readChangelog(), { included: true, versions: [{ version: 'v1.2', date: '2026-01-02', entries: [{ title: 'A change', pr: 2, body: 'It changed.' }] }] })
    // A changed file is read again.
    fs.writeFileSync(file, '# Change log\n\n## v1.3 (2026-01-03)\n\n### Another change, longer (#3)\n\nIt changed again.\n')
    assert.equal(readChangelog().versions[0].version, 'v1.3')
    fs.rmSync(file)
    assert.equal(readChangelog().included, false)
  } finally {
    if (before === undefined) delete process.env.STUDIO_CHANGELOG_FILE; else process.env.STUDIO_CHANGELOG_FILE = before
    fs.rmSync(dir, { recursive: true, force: true })
  }
})
