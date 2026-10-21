// Compares the @parallelworks packages the lockfile resolves with the latest
// on npm. When the Studio is behind, one open issue labeled foundation-update
// lists what changed in between, from each package's changelog; a newer
// release adds a comment so watchers hear of it; once the Studio is current
// the issue closes. DRY_RUN=1 prints the issue instead of writing it.
import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

const LABEL = 'foundation-update'
const dry = !!process.env.DRY_RUN

/** Every @parallelworks package an importer depends on, with the version it resolves to. */
function lockedVersions(lock) {
  const out = new Map()
  let inImporters = false, pending = null
  for (const line of lock.split('\n')) {
    if (/^\S/.test(line)) inImporters = line.startsWith('importers:')
    if (!inImporters) continue
    const name = /^ {6}'(@parallelworks\/[^']+)':$/.exec(line)
    if (name) { pending = name[1]; continue }
    const version = /^ {8}version: ([0-9][^(\s]*)/.exec(line)
    if (pending && version) { out.set(pending, version[1]); pending = null }
  }
  return out
}

const parts = v => v.split(/[.-]/).slice(0, 3).map(Number)
const newer = (a, b) => { const [x, y] = [parts(a), parts(b)]; for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] > y[i]; return false }

/** A package's changelog sections after `from` up to `to`, newest first. */
async function changes(meta, from, to) {
  const repo = /github\.com[/:]([^/]+\/[^/.]+)/.exec(meta.repository?.url ?? '')?.[1]
  if (!repo) return null
  const dir = meta.repository?.directory ? `${meta.repository.directory}/` : ''
  const res = await fetch(`https://raw.githubusercontent.com/${repo}/HEAD/${dir}CHANGELOG.md`)
  if (!res.ok) return null
  const text = await res.text()
  const sections = text.split(/\n(?=## )/).filter(s => s.startsWith('## '))
  const kept = sections.filter(s => {
    const v = /^## \[?(\d+\.\d+\.\d+)/.exec(s)?.[1]
    return v && newer(v, from) && !newer(v, to)
  })
  // Issue and commit links shortened to references GitHub links on its own.
  return { repo, text: kept.map(s => s
    .replace(/ \(\[#(\d+)\]\([^)]*\)\)/g, ` (${repo}#$1)`)
    .replace(/ \(\[[0-9a-f]{7,}\]\([^)]*\)\)/g, '')
    .replace(/^### /gm, '#### ')
    .replace(/^## \[([^\]]+)\]\([^)]*\)/, '### $1')
    .replace(/\n{3,}/g, '\n\n')
    .trim()).join('\n\n') }
}

const gh = (...args) => execFileSync('gh', args, { encoding: 'utf8' })

const locked = lockedVersions(readFileSync('pnpm-lock.yaml', 'utf8'))
const behind = []
for (const [name, current] of locked) {
  const meta = await (await fetch(`https://registry.npmjs.org/${name.replace('/', '%2f')}`)).json()
  const latest = meta['dist-tags']?.latest
  if (latest && newer(latest, current)) behind.push({ name, current, latest, released: meta.time?.[latest]?.slice(0, 10) ?? '', log: await changes(meta, current, latest) })
}

const open = dry ? [] : JSON.parse(gh('issue', 'list', '--label', LABEL, '--state', 'open', '--json', 'number,title'))
if (!behind.length) {
  console.log('every @parallelworks package is current')
  for (const issue of open) if (!dry) gh('issue', 'close', String(issue.number), '--comment', 'The Studio is on the latest release of every @parallelworks package.')
  process.exit(0)
}

const title = `Foundation update: ${behind.map(b => `${b.name.replace('@parallelworks/', '')} ${b.latest}`).join(', ')}`
const breaking = behind.some(b => /BREAKING/.test(b.log?.text ?? ''))
let body = [
  'The Studio is behind these `@parallelworks` releases. This issue is kept current by the foundation-watch workflow and closes once the lockfile catches up.',
  '',
  '| Package | Studio | Latest | Released |',
  '| --- | --- | --- | --- |',
  ...behind.map(b => `| \`${b.name}\` | ${b.current} | ${b.latest} | ${b.released} |`),
  '',
  ...(breaking ? ['At least one release in between has breaking changes, marked below.', ''] : []),
  'To integrate: bump with pnpm from the root (`pnpm --filter ./web add @parallelworks/ui@^<version>`); run `UPDATE_THEME_DEFAULTS=1 npx vitest run test/themeDefaults.test.ts` in `web` if the theme test fails; typecheck, test and build; read the changes below for anything the Studio should now use; compare every view in light and dark against the previous release; release.',
  '',
  ...behind.map(b => b.log
    ? `## ${b.name} ${b.current} to ${b.latest}\n\nFrom [${b.log.repo}](https://github.com/${b.log.repo}).\n\n${b.log.text || 'No changelog entries found.'}`
    : `## ${b.name} ${b.current} to ${b.latest}\n\nThe package names no repository, so no changelog is shown.`),
].join('\n')
if (body.length > 60000) body = body.slice(0, 60000) + '\n\n(Cut at the issue size limit; see the changelogs.)'

if (dry) { console.log(title + '\n\n' + body); process.exit(0) }
const file = path.join(mkdtempSync(path.join(tmpdir(), 'fw-')), 'body.md')
writeFileSync(file, body)
gh('label', 'create', LABEL, '--color', '0e8a16', '--description', 'A newer @parallelworks release to integrate', '--force')
if (open.length) {
  const [issue] = open
  gh('issue', 'edit', String(issue.number), '--title', title, '--body-file', file)
  if (issue.title !== title) gh('issue', 'comment', String(issue.number), '--body', `New release: ${title.replace('Foundation update: ', '')}.`)
  console.log(`updated #${issue.number}: ${title}`)
} else {
  console.log(gh('issue', 'create', '--title', title, '--label', LABEL, '--body-file', file).trim())
}
