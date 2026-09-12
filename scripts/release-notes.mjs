#!/usr/bin/env node
// Release notes from the pull requests a tag contains. Every merged pull
// request in this repository carries a description written as a change
// note, so the notes for a version are those descriptions, in order, and
// the change log is the same thing for every version at once.
//
//   node scripts/release-notes.mjs v1.59            # notes for one tag, to stdout
//   node scripts/release-notes.mjs --changelog      # CHANGELOG.md for every tag
//
// Needs the gh CLI, authenticated. Squash merges put "(#N)" at the end of
// the commit subject, which is how commits map to pull requests.
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'

const sh = (cmd, args) => execFileSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 << 20 })
const tags = sh('git', ['tag', '-l', 'v*', '--sort=v:refname', '--format=%(refname:short)\t%(taggerdate:short)'])
  .trim().split('\n').filter(Boolean).map(l => { const [tag, date] = l.split('\t'); return { tag, date } })
const prs = new Map(JSON.parse(sh('gh', ['pr', 'list', '--state', 'merged', '--limit', '1000', '--json', 'number,title,body']))
  .map(p => [p.number, p]))

function prsBetween(prev, tag) {
  const subjects = sh('git', ['log', '--format=%s', prev ? `${prev}..${tag}` : tag]).trim().split('\n')
  const nums = []
  for (const s of subjects) { const m = /\(#(\d+)\)\s*$/.exec(s); if (m) nums.push(Number(m[1])) }
  return nums.reverse() // oldest first within the version
}

// Lines that are not change content: attribution trailers and session links.
const dropLine = l => /^(Claude-Session|Co-Authored-By|Signed-off-by):/i.test(l) || /claude\.ai\/code\/session/i.test(l)

function notesFor(tag, prev) {
  const parts = []
  for (const n of prsBetween(prev, tag)) {
    const pr = prs.get(n)
    if (!pr) { parts.push(`### #${n}\n`); continue }
    const body = (pr.body || '').split('\n').filter(l => !dropLine(l)).join('\n').trim()
    parts.push(`### ${pr.title} (#${n})\n\n${body}\n`)
  }
  return parts.join('\n')
}

if (process.argv[2] === '--changelog') {
  let out = '# Change log\n\nEvery version, newest first. Each entry is the description of the pull request that made the change, which is written as a change note when the change is made.\n\n'
  for (let i = tags.length - 1; i >= 0; i--) {
    const { tag, date } = tags[i]
    const prev = i > 0 ? tags[i - 1].tag : null
    const notes = notesFor(tag, prev)
    out += `## ${tag} (${date})\n\n${notes || '_No pull requests; tag only._\n'}\n`
  }
  fs.writeFileSync('CHANGELOG.md', out)
  console.log(`CHANGELOG.md: ${tags.length} versions`)
} else {
  const want = process.argv[2]
  const i = tags.findIndex(t => t.tag === want)
  if (i < 0) { console.error(`no such tag: ${want}`); process.exit(1) }
  process.stdout.write(notesFor(want, i > 0 ? tags[i - 1].tag : null) || '_No pull requests; tag only._\n')
}
