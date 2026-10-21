#!/usr/bin/env node
// Release notes from the pull requests a tag contains. Every merged pull
// request in this repository carries a description written as a change
// note, so the notes for a version are those descriptions, in order, and
// the change log is the same thing for every version at once.
//
//   node scripts/release-notes.mjs v1.59            # notes for one tag, to stdout
//   node scripts/release-notes.mjs --changelog      # CHANGELOG.md for every tag
//   node scripts/release-notes.mjs --changelog --next v1.60
//                                                   # and the commits since the last
//                                                   # tag as v1.60, before it is tagged
//
// A release writes the change log with --next before it tags, so the tagged
// build carries its own version's notes (the app shows them).
//
// Needs the gh CLI, authenticated. Squash merges put "(#N)" at the end of
// the commit subject, which is how commits map to pull requests.
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'

const sh = (cmd, args) => execFileSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 << 20 })
const tags = sh('git', ['tag', '-l', 'v*', '--sort=v:refname', '--format=%(refname:short)\t%(creatordate:short)'])
  .trim().split('\n').filter(Boolean).map(l => { const [tag, date] = l.split('\t'); return { tag, date, ref: tag } })
const nextAt = process.argv.indexOf('--next')
if (nextAt > 0) {
  const next = process.argv[nextAt + 1] ?? ''
  if (!/^v\d+\.\d+(\.\d+)?$/.test(next)) { console.error(`--next takes a version such as v1.60, not '${next}'`); process.exit(1) }
  if (tags.some(t => t.tag === next)) { console.error(`${next} is already a tag`); process.exit(1) }
  tags.push({ tag: next, date: new Date().toISOString().slice(0, 10), ref: 'HEAD' })
}
const prs = new Map(JSON.parse(sh('gh', ['pr', 'list', '--state', 'merged', '--limit', '1000', '--json', 'number,title,body']))
  .map(p => [p.number, p]))

function prsBetween(prev, ref) {
  const subjects = sh('git', ['log', '--format=%s', prev ? `${prev}..${ref}` : ref]).trim().split('\n')
  const nums = []
  for (const s of subjects) { const m = /\(#(\d+)\)\s*$/.exec(s); if (m) nums.push(Number(m[1])) }
  return nums.reverse() // oldest first within the version
}

// Lines that are not change content: attribution trailers and session links.
const dropLine = l => /^(Claude-Session|Co-Authored-By|Signed-off-by):/i.test(l) || /claude\.ai\/code\/session/i.test(l)

function notesFor(ref, prev) {
  const parts = []
  for (const n of prsBetween(prev, ref)) {
    const pr = prs.get(n)
    if (!pr) { parts.push(`### #${n}\n`); continue }
    const heading = `### ${pr.title} (#${n})`
    let body = (pr.body || '').split('\n').filter(l => !dropLine(l)).join('\n').trim()
    // Descriptions that open with their own title heading would print it twice.
    const first = body.split('\n')[0].replace(/^#+\s*/, '').replace(/\s*\(#\d+\)\s*$/, '').trim()
    if (/^#+\s/.test(body) && first === pr.title.trim()) body = body.split('\n').slice(1).join('\n').trim()
    parts.push(`${heading}\n\n${body}\n`)
  }
  return parts.join('\n')
}

if (process.argv[2] === '--changelog') {
  let out = '# Change log\n\nEvery version, newest first. Each entry is the description of the pull request that made the change, which is written as a change note when the change is made.\n\n'
  for (let i = tags.length - 1; i >= 0; i--) {
    const { tag, date, ref } = tags[i]
    const prev = i > 0 ? tags[i - 1].tag : null
    const notes = notesFor(ref, prev)
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
