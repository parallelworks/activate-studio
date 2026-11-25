import { Fragment, memo, useId, type ReactElement } from 'react'
import {
  donePullRequestsToFold,
  foldPullRequests,
  isPullRequestState,
  lastSegmentAt,
  type LabeledPullRequest,
  pathSegments,
  type PullRequestFoldGroup,
  type PullRequestState,
  type PullRequestStateOf,
  pullRequestGroupState,
  pullRequestShortLabel,
  pullRequestStateBreaks,
  pullRequestTone,
  pullRequestWords,
  type PwCodeAddedDirectory,
  type PwCodeAllowedDirectory,
  sessionContextRow,
  sessionDirectories,
  useRemoveSessionDirectory,
  usePwCodeTranslations,
  workingDirectoryText,
} from '@parallelworks/code'
import { IconButton, Popover, Tooltip } from '@parallelworks/ui'
import { ExternalLinkIcon, FolderIcon, PullRequestIcon, RemoveIcon } from '@parallelworks/ui/icons'
import { MachineIcon } from '../../code/parts'
import { type MachineRef, useMachineLook } from '../../machines'
import { pwCode } from '../api'
import { describeError as describeDefault } from '../describeError'
import './ContextRow.css'

export interface SessionContextRowProps {
  machine: string
  sessionId: string
  /** The machine's name as the session shows it, such as `gpu-box`. */
  machineName: string
  /** The machine as it is read, for the resource it is. */
  on?: MachineRef
  workspace: string
  added: PwCodeAddedDirectory[] | null | undefined
  allowed: PwCodeAllowedDirectory[] | null | undefined
  pullRequests: string[] | null | undefined
  /** Words a failed removal; the session view's own by default. */
  describeError?: (e: unknown) => string
}

// Pull requests shown before the rest fold into counts; done ones always fold.
const SHOWN_PULL_REQUESTS = 3
// Added directories shown before the rest fold into the last one's count.
const SHOWN_DIRECTORIES = 2

/** The session's machine and folder, the directories added to it and its pull
 *  requests, on one line over the composer. Memoized: the view draws again for
 *  each frame of a reply. */
export const SessionContextRow = memo(function SessionContextRow({
  machine,
  sessionId,
  machineName,
  on,
  workspace,
  added,
  allowed,
  pullRequests,
  describeError = describeDefault,
}: SessionContextRowProps) {
  const states = pwCode.useSessionPullRequestStates(machine, sessionId, pullRequests)
  const context = sessionContextRow({ added, allowed, pullRequests, states })
  const opens = (trigger: ReactElement) => (
    <DirectoriesPopover
      trigger={trigger}
      machine={machine}
      sessionId={sessionId}
      workspace={workspace}
      added={context.added}
      allowed={context.allowed}
      describeError={describeError}
    />
  )
  return (
    <div className="pwcode-ctx" data-testid="pwcode-context-row">
      <WorkspaceChip machine={{ ...on, id: machine, name: machineName }} workspace={workspace} opens={opens} />
      {context.added.length > 0 && <AddedDirectoryChips dirs={context.added} opens={opens} />}
      {context.pullRequests.length > 0 && <PullRequestPills ordered={context.pullRequests} stateOf={context.stateOf} />}
    </div>
  )
})

type Opens = (trigger: ReactElement) => ReactElement

/** The machine's icon and `name:folder`, as the Studio has always named the
 *  session's place; the host and the full path are in its tooltip, and the
 *  path in its popover. */
function WorkspaceChip({ machine, workspace, opens }: { machine: MachineRef; workspace: string; opens: Opens }) {
  const t = usePwCodeTranslations('agents')
  const look = useMachineLook(machine)
  const { label } = workingDirectoryText(look.name, workspace, t)
  const folder = workspace.split('/').filter(Boolean).pop() ?? workspace
  return opens(
    <button type="button" className="pwcode-ctx-chip" aria-label={label} title={`${look.title || look.name}:${workspace}`} data-testid="pwcode-workspace">
      <MachineIcon machine={look.icon} />
      <span className="pwcode-ctx-text">{look.name}:{folder}</span>
    </button>,
  )
}

/** A path whose leading part ellipsizes first, keeping its last segment whole. */
function PathText({ path }: { path: string }) {
  const cut = lastSegmentAt(path)
  return (
    <span className="pwcode-ctx-path">
      {cut > 0 && <span className="pwcode-ctx-path-head">{path.slice(0, cut)}</span>}
      <span className="pwcode-ctx-path-tail">{path.slice(cut)}</span>
    </span>
  )
}

function AddedDirectoryChips({ dirs, opens }: { dirs: PwCodeAddedDirectory[]; opens: Opens }) {
  const t = usePwCodeTranslations('agents')
  const shown = dirs.slice(0, SHOWN_DIRECTORIES)
  const folded = dirs.length - shown.length
  return (
    <>
      {shown.map((dir, i) => {
        const more = i === shown.length - 1 ? folded : 0
        return (
          <Fragment key={dir.path}>
            {opens(
              <button
                type="button"
                className="pwcode-ctx-chip"
                title={dir.path}
                data-testid="pwcode-added-directory"
                aria-label={more > 0
                  ? t('detail.addedDirectoryAndMore', { path: dir.path, count: more })
                  : t('detail.addedDirectory', { path: dir.path })}
              >
                <FolderIcon aria-hidden className="pwcode-ctx-icon" />
                <PathText path={dir.path} />
                {more > 0 && <span className="pwcode-ctx-more">{t('detail.directoriesMore', { count: more })}</span>}
              </button>,
            )}
          </Fragment>
        )
      })}
    </>
  )
}

/** A full path that wraps after a separator rather than inside a folder name. */
function WrappingPath({ path }: { path: string }) {
  const segments = pathSegments(path)
  return (
    <span className="pwcode-ctx-wrap-path">
      {segments.map((segment, i) => (
        <Fragment key={i}>
          <span className="pwcode-ctx-segment">{segment}</span>
          {i < segments.length - 1 && <wbr />}
        </Fragment>
      ))}
    </span>
  )
}

interface DirectoriesPanelProps {
  machine: string
  sessionId: string
  workspace: string
  added: PwCodeAddedDirectory[]
  allowed: PwCodeAllowedDirectory[]
  describeError: (e: unknown) => string
}

function DirectoriesPopover({ trigger, ...panel }: DirectoriesPanelProps & { trigger: ReactElement }) {
  const t = usePwCodeTranslations('agents')
  return (
    <Popover align="start" title={t('detail.directoriesTitle')} trigger={trigger} className="pwcode-ctx-popover">
      <DirectoriesPanel {...panel} />
    </Popover>
  )
}

/** The working directory, those added (each added for the session alone
 *  removable, POST .../commands) and those approvals opened. */
function DirectoriesPanel({ machine, sessionId, workspace, added, allowed, describeError }: DirectoriesPanelProps) {
  const t = usePwCodeTranslations('agents')
  const allowedHeading = useId()
  const { pending, error, remove } = useRemoveSessionDirectory(pwCode, machine, sessionId)
  const listed = sessionDirectories(added, allowed, t)
  return (
    <div className="pwcode-ctx-panel">
      <ul className="pwcode-ctx-dirs">
        <li className="pwcode-ctx-dir-col">
          <WrappingPath path={workspace} />
          <span className="pwcode-ctx-muted">{t('detail.originalDirectory')}</span>
        </li>
        {listed.added.map(dir => (
          <li key={dir.path} className="pwcode-ctx-dir-row" data-testid="pwcode-directory-row">
            <FolderIcon aria-hidden className="pwcode-ctx-icon pwcode-ctx-muted" />
            <span className="pwcode-ctx-dir-col pwcode-ctx-grow">
              <WrappingPath path={dir.path} />
              {dir.scopes.map(scope => <span key={scope} className="pwcode-ctx-muted">{scope}</span>)}
            </span>
            {dir.removable && (
              <IconButton
                size="xs"
                icon={<RemoveIcon aria-hidden />}
                label={dir.removeLabel}
                aria-disabled={pending.has(dir.path) || undefined}
                onClick={() => { if (!pending.has(dir.path)) void remove(dir.path) }}
              />
            )}
          </li>
        ))}
      </ul>
      {!!error && <p role="alert" className="pwcode-ctx-error">{describeError(error)}</p>}
      {listed.savedNote && <p className="pwcode-ctx-muted">{listed.savedNote}</p>}
      {listed.allowed.length > 0 && (
        <section aria-labelledby={allowedHeading} className="pwcode-ctx-allowed">
          <h3 id={allowedHeading}>{t('detail.allowedDirectoriesTitle')}</h3>
          <ul className="pwcode-ctx-dirs">
            {listed.allowed.map(dir => (
              <li key={dir.path} className="pwcode-ctx-dir-col">
                <WrappingPath path={dir.path} />
                <span className="pwcode-ctx-muted">{dir.access}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}

// Paths from GitHub's Primer Octicons (MIT, (c) GitHub Inc.): git-pull-request, git-pull-request-draft, git-merge and git-pull-request-closed at 16px.
const statePaths: Record<PullRequestState, string> = {
  open: 'M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.251 2.251 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354ZM3.75 2.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm0 9.5a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm8.25.75a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0Z',
  draft: 'M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 14a2.25 2.25 0 1 1 0-4.5 2.25 2.25 0 0 1 0 4.5ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5ZM14 7.5a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Zm0-4.25a1.25 1.25 0 1 1-2.5 0 1.25 1.25 0 0 1 2.5 0Z',
  merged: 'M5.45 5.154A4.25 4.25 0 0 0 9.25 7.5h1.378a2.251 2.251 0 1 1 0 1.5H9.25A5.734 5.734 0 0 1 5 7.123v3.505a2.25 2.25 0 1 1-1.5 0V5.372a2.25 2.25 0 1 1 1.95-.218ZM4.25 13.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Zm8.5-4.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5ZM5 3.25a.75.75 0 1 0 0 .005V3.25Z',
  closed: 'M3.25 1A2.25 2.25 0 0 1 4 5.372v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.251 2.251 0 0 1 3.25 1Zm9.5 5.5a.75.75 0 0 1 .75.75v3.378a2.251 2.251 0 1 1-1.5 0V7.25a.75.75 0 0 1 .75-.75Zm-2.03-5.273a.75.75 0 0 1 1.06 0l.97.97.97-.97a.748.748 0 0 1 1.265.332.75.75 0 0 1-.205.729l-.97.97.97.97a.751.751 0 0 1-.018 1.042.751.751 0 0 1-1.042.018l-.97-.97-.97.97a.749.749 0 0 1-1.275-.326.749.749 0 0 1 .215-.734l.97-.97-.97-.97a.75.75 0 0 1 0-1.06ZM2.5 3.25a.75.75 0 1 0 1.5 0 .75.75 0 0 0-1.5 0ZM3.25 12a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Zm9.5 0a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Z',
}

/** The forge's icon for a known state; nothing until it is read. */
export function PullRequestStateIcon({ state, label }: { state: PullRequestState | undefined; label: string }) {
  if (!state) return null
  return (
    <svg viewBox="0 0 16 16" width={12} height={12} fill="currentColor" role="img" aria-label={label}
      data-testid={`pwcode-pull-request-${state}`} className={`pwcode-ctx-pr-icon pwcode-ctx-tone-${pullRequestTone(state)}`}>
      <title>{label}</title>
      <path d={statePaths[state]} />
    </svg>
  )
}

function usePullRequestWords() {
  return pullRequestWords(usePwCodeTranslations('agents'))
}

function PullRequestPill({ pr, url, reported }: { pr: string; url: string; reported: string | undefined }) {
  const words = usePullRequestWords()
  const state = isPullRequestState(reported) ? reported : undefined
  return (
    <Tooltip content={words.title(pr, reported)} describesAnchor>
      <a href={url} target="_blank" rel="noreferrer" className="pwcode-ctx-chip pwcode-ctx-pr" data-testid="pwcode-pull-request">
        <PullRequestStateIcon state={state} label={state ? words.state(state) : ''} />
        <span className="pwcode-ctx-text">{pullRequestShortLabel(pr)}</span>
      </a>
    </Tooltip>
  )
}

/** A count's pull requests as rows, with a hairline where the state changes. */
function FoldedRows({ folded, stateOf, label }: { folded: LabeledPullRequest[]; stateOf: PullRequestStateOf; label: string }) {
  const words = usePullRequestWords()
  const breaks = pullRequestStateBreaks(folded, stateOf)
  return (
    <ul aria-label={label} className="pwcode-ctx-pr-list">
      {folded.map(({ url, pr }, i) => {
        const reported = stateOf(url)
        const state = isPullRequestState(reported) ? reported : undefined
        return (
          <Fragment key={url}>
            {breaks[i] && <li aria-hidden="true" className="pwcode-ctx-pr-break" />}
            <li>
              <a href={url} target="_blank" rel="noreferrer" title={words.title(pr, reported)} className="pwcode-ctx-pr-row" data-testid="pwcode-pull-request">
                {state ? <PullRequestStateIcon state={state} label={words.state(state)} /> : <span className="pwcode-ctx-pr-icon" />}
                <span className="pwcode-ctx-pr-label">{pullRequestShortLabel(pr)}</span>
                <ExternalLinkIcon aria-hidden className="pwcode-ctx-pr-out" />
              </a>
            </li>
          </Fragment>
        )
      })}
    </ul>
  )
}

function FoldedCount({ group, folded, stateOf }: { group: Exclude<PullRequestFoldGroup, 'all'>; folded: LabeledPullRequest[]; stateOf: PullRequestStateOf }) {
  const words = usePullRequestWords()
  const state = pullRequestGroupState(folded, stateOf)
  return (
    <Popover
      align="start"
      flush
      aria-label={words.groupTitle(group, folded.length)}
      trigger={
        <button type="button" className="pwcode-ctx-chip" aria-label={words.showGroup(group, folded.length)}
          data-testid="pwcode-pull-requests-more" data-group={group}>
          {state ? <PullRequestStateIcon state={state} label={words.state(state)} /> : <PullRequestIcon aria-hidden className="pwcode-ctx-pr-icon" />}
          <span className="pwcode-ctx-text">{words.more(folded.length)}</span>
        </button>
      }
    >
      <FoldedRows folded={folded} stateOf={stateOf} label={words.groupTitle(group, folded.length)} />
    </Popover>
  )
}

/** The first few open pull requests as pills; merged and closed ones, and any
 *  past the first few, fold into a count per group that lists them. */
function PullRequestPills({ ordered, stateOf }: { ordered: LabeledPullRequest[]; stateOf: PullRequestStateOf }) {
  const folded = Math.min(
    Math.max(donePullRequestsToFold(ordered, stateOf, 1), ordered.length - SHOWN_PULL_REQUESTS),
    ordered.length - 1,
  )
  const { shown, groups } = foldPullRequests(ordered, Math.max(folded, 0), stateOf)
  return (
    <>
      {shown.map(({ url, pr }) => <PullRequestPill key={url} pr={pr} url={url} reported={stateOf(url)} />)}
      {groups.map(({ group, pullRequests }) => (
        <FoldedCount key={group} group={group} folded={pullRequests} stateOf={stateOf} />
      ))}
    </>
  )
}
