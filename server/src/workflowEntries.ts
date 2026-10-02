/**
 * An entry in a Studio's workflow collection is one of:
 *
 *   name                                  a workflow saved on the account
 *   marketplace/<slug>                    a marketplace workflow, run by slug
 *   github.com/<owner>/<repo>[/path][@ref] a workflow.yaml in a GitHub repository
 *
 * The last two run without being added to anyone's account; `pw workflows
 * run` accepts both forms as they are written here.
 */

export type WorkflowEntry =
  | { kind: 'account'; entry: string; name: string }
  | { kind: 'marketplace'; entry: string; slug: string }
  | { kind: 'github'; entry: string; owner: string; repo: string; path: string; ref: string | null }

const NAME = /^[A-Za-z0-9_.-]{1,120}$/
const SEG = '[A-Za-z0-9_.-]+'
const GITHUB = new RegExp(`^github\\.com/(${SEG})/(${SEG})((?:/${SEG})*)(?:@([A-Za-z0-9_./-]{1,120}))?$`)

export function parseWorkflowEntry(raw: string): WorkflowEntry | null {
  const s = String(raw ?? '').trim()
  if (!s || s.length > 300 || s.includes('..')) return null
  if (NAME.test(s)) return { kind: 'account', entry: s, name: s }
  const mp = /^marketplace\/([A-Za-z0-9_.-]{1,120})$/.exec(s)
  if (mp) return { kind: 'marketplace', entry: s, slug: mp[1] }
  const gh = GITHUB.exec(s.replace(/^https?:\/\//, ''))
  if (gh) {
    const entry = s.replace(/^https?:\/\//, '')
    return { kind: 'github', entry, owner: gh[1], repo: gh[2], path: gh[3].replace(/^\//, ''), ref: gh[4] ?? null }
  }
  return null
}

/** The raw file URLs to try for a GitHub entry: the named file, or
 *  workflow.yaml then workflow.yml in the named directory. */
export function githubYamlUrls(e: Extract<WorkflowEntry, { kind: 'github' }>): string[] {
  const base = `https://raw.githubusercontent.com/${e.owner}/${e.repo}/${e.ref ?? 'HEAD'}`
  if (/\.ya?ml$/.test(e.path)) return [`${base}/${e.path}`]
  const dir = e.path ? `${base}/${e.path}` : base
  return [`${dir}/workflow.yaml`, `${dir}/workflow.yml`]
}

/** Where a GitHub workflow's thumbnail would be, by the platform's convention. */
export function githubThumbnail(e: Extract<WorkflowEntry, { kind: 'github' }>): string {
  const dir = /\.ya?ml$/.test(e.path) ? e.path.replace(/\/?[^/]+$/, '') : e.path
  return `https://raw.githubusercontent.com/${e.owner}/${e.repo}/${e.ref ?? 'HEAD'}/${dir ? `${dir}/` : ''}thumbnail.png`
}

/** A readable default title: the repository, or its last path segment. */
export function entryTitle(e: WorkflowEntry): string {
  if (e.kind === 'account') return e.name
  if (e.kind === 'marketplace') return e.slug
  const tail = e.path.replace(/\/?workflow\.ya?ml$/, '').split('/').filter(Boolean).pop()
  return tail || e.repo
}
