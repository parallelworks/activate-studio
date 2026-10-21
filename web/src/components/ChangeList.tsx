import { Streamdown } from 'streamdown'
import { Disclosure, formatDateTime } from '@parallelworks/ui'
import type { ChangeEntry, UpdateRelease } from '../api'

const REPO_URL = 'https://github.com/parallelworks/activate-studio'

export interface ChangeGroup { version: string; date: string | null; entries: ChangeEntry[] }

/** A change log date (YYYY-MM-DD) as the reader's calendar day, or a release
 *  time as its date. */
export function dayText(d: string | null): string {
  if (!d) return ''
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d)
  return formatDateTime(m ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])) : d, 'date')
}

/** One change: its title, which opens its description. */
function Entry({ entry }: { entry: ChangeEntry }) {
  return (
    <li>
      <Disclosure className="changes-entry" summaryClassName="changes-entry-title" panelClassName="changes-entry-body"
        summary={<span>{entry.title}</span>}>
        {entry.body
          ? <div className="md-body changes-md"><Streamdown>{entry.body}</Streamdown></div>
          : <p className="muted">No description.</p>}
        {entry.pr != null && (
          <p className="changes-pr">
            <a href={`${REPO_URL}/pull/${entry.pr}`} target="_blank" rel="noopener noreferrer">Pull request #{entry.pr}</a>
          </p>
        )}
      </Disclosure>
    </li>
  )
}

/**
 * Versions newest first, each with the titles of its changes; a title opens
 * the change's description, rendered as markdown. The update notice,
 * Settings, Updates and What's new all list changes this way.
 */
export function ChangeList({ versions, headingLevel = 3 }: { versions: ChangeGroup[]; headingLevel?: 2 | 3 | 4 }) {
  const H = `h${headingLevel}` as 'h2' | 'h3' | 'h4'
  return (
    <div className="changes">
      {versions.map(v => (
        <section key={v.version} className="changes-version" aria-label={v.version}>
          <H className="changes-version-head">
            <span className="changes-version-name">{v.version}</span>
            {v.date && <span className="muted changes-version-date">{dayText(v.date)}</span>}
          </H>
          {v.entries.length ? (
            <ul className="changes-entries">
              {v.entries.map((e, i) => <Entry key={`${e.pr ?? 'n'}-${i}`} entry={e} />)}
            </ul>
          ) : <p className="muted changes-none">No notes for this version.</p>}
        </section>
      ))}
    </div>
  )
}

/** The releases an update brings, as a change list. */
export function ReleaseList({ releases, headingLevel }: { releases: UpdateRelease[]; headingLevel?: 2 | 3 | 4 }) {
  return <ChangeList headingLevel={headingLevel} versions={releases.map(r => ({ version: r.version, date: r.publishedAt, entries: r.entries }))} />
}
