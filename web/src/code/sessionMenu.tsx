import type { ReactNode } from 'react'
import {
  type OpenSessionRef,
  type PwCodeMachine,
  type PwCodeWorktreeLines,
  type SessionActions,
  type SessionMenuAction,
  type SessionMenuCommand,
  type SessionMenuItem,
  type SessionRowKey,
  useSessionActions,
  useWorktreeChoice,
  worktreeWork,
} from '@parallelworks/code'
import { Callout, type MenuEntry, menuDivider, RadioGroup, SettingModeOption, toast, useConfirm, useShortcutFormat } from '@parallelworks/ui'
import { RenameDialog, type SidebarRowShortcut, useSessionMenu } from '@parallelworks/ui/ai'
import {
  AddIcon, ArchiveIcon, BranchIcon, EditIcon, FolderIcon, ForkIcon, PinIcon, RestoreIcon, StopIcon, TrashIcon,
} from '@parallelworks/ui/icons'
import { useTranslations } from 'use-intl'
import { describeError } from '../pwcode/PWCodeSession'
import { pwCodePage } from './api'

/**
 * A session's menu on the Code page: @parallelworks/code's session actions
 * (rename, pin, move to a group, fork, stop, archive or restore, delete,
 * leave its worktree), drawn with @parallelworks/ui's menus and dialogs as
 * PW Code on the web draws them (its useAgentSessionMenu).
 */

type Session = Parameters<SessionActions<HTMLLIElement>['rename']['setName']> extends never ? never : Parameters<SessionActions<HTMLLIElement>['menu']>[0]

const ICONS: Partial<Record<SessionMenuAction, ReactNode>> = {
  restore: <RestoreIcon />, pin: <PinIcon />, unpin: <PinIcon />, fork: <ForkIcon />, leaveWorktree: <BranchIcon />,
  stop: <StopIcon />, archive: <ArchiveIcon />, moveToGroup: <FolderIcon />, moveToNewGroup: <AddIcon />,
  renameGroup: <EditIcon />, deleteGroup: <TrashIcon />, newSession: <AddIcon />, newGroup: <FolderIcon />,
}
const iconOf = (action: SessionMenuAction) => (ICONS[action] ? { icon: ICONS[action] } : {})
const disabledBy = (reason: string | undefined) => (reason ? { disabled: true, disabledReason: reason } : {})

function commandEntry(item: SessionMenuCommand, shortcut: string | undefined): MenuEntry {
  return {
    label: item.label,
    ...iconOf(item.action),
    onClick: item.run,
    ...(item.checked === undefined ? {} : { checked: item.checked }),
    ...(item.destructive ? { destructive: true } : {}),
    ...(shortcut ? { shortcut } : {}),
    ...disabledBy(item.disabledReason),
  }
}

export function rowShortcut(key: SessionRowKey<HTMLLIElement>): SidebarRowShortcut {
  return { keys: key.keys, onPress: key.run }
}

/** The package's session actions, asking and reporting as the Studio does,
 *  and a way to draw the menus they make. */
export function useCodeSessionActions({ machineById, open, onOpen, showStart }: {
  machineById: ReadonlyMap<string, PwCodeMachine>
  open: OpenSessionRef
  /** Opens a session, as forking does with the copy: in the page's pane. */
  onOpen: (s: Pick<Session, 'machineId' | 'id'>) => void
  showStart: () => void
}): { actions: SessionActions<HTMLLIElement>; toMenu: (items: SessionMenuItem[]) => MenuEntry[]; dialogs: ReactNode } {
  const { confirm, dialog } = useConfirm()
  const sessionMenu = useSessionMenu()
  const { text: keyText } = useShortcutFormat()
  const actions = useSessionActions<HTMLLIElement>({
    api: pwCodePage,
    machineById,
    open,
    confirm: request => {
      const worktree: { remove: string | undefined } = { remove: undefined }
      void confirm({
        removes: request.removes,
        initialFocus: request.fromKey ? 'confirm' : undefined,
        ...(request.offersWorktreeRemoval
          ? { align: 'center' as const, children: <SessionWorktreeChoice session={request.session} onChange={root => { worktree.remove = root }} /> }
          : {}),
        destructive: request.destructive,
        title: request.title,
        description: request.description,
        confirmLabel: request.confirmLabel,
        errorMessage: describeError,
        onConfirm: () => request.onConfirm(worktree.remove),
      })
    },
    onRefusal: refusal => toast.error(describeError(refusal)),
    onWarning: message => toast.warning(message),
    showStart,
    showSession: s => onOpen(s),
  })
  const toMenu = (items: SessionMenuItem[]): MenuEntry[] =>
    items.flatMap((item): MenuEntry[] => {
      if (item === 'divider') return [menuDivider]
      if ('items' in item) {
        return [{ label: item.label, ...iconOf(item.action), ...disabledBy(item.disabledReason), items: toMenu(item.items) }]
      }
      const shortcut = item.shortcut ? keyText(item.shortcut) : undefined
      if (item.action === 'rename') return sessionMenu({ rename: { onClick: item.run, disabledReason: item.disabledReason } })
      if (item.action === 'delete') return sessionMenu({ remove: { onClick: item.run, disabledReason: item.disabledReason, shortcut } })
      return [commandEntry(item, shortcut)]
    })
  const { rename } = actions
  return {
    actions,
    toMenu,
    dialogs: (
      <>
        {dialog}
        <RenameDialog
          open={rename.session !== null}
          onClose={rename.close}
          title={rename.title}
          label={rename.label}
          placeholder={rename.placeholder}
          confirmLabel={rename.confirmLabel}
          value={rename.name}
          onChange={rename.setName}
          onConfirm={rename.confirm}
        />
      </>
    ),
  }
}

const worktreeApi = { useSessionWorktree: pwCodePage.useSessionWorktree }

/** The worktree a session works in, for its delete and archive dialogs,
 *  with the choice to remove it too (kept by default). */
export function SessionWorktreeChoice({ session, onChange }: { session: Session; onChange: (removeRoot: string | undefined) => void }) {
  const t = useTranslations('agents')
  const { worktree, remove, choose } = useWorktreeChoice(worktreeApi, session, onChange)
  const root = worktree?.root
  if (!root) return null
  const work = remove ? worktreeWork(worktree) : null
  return (
    <div className="code-worktree">
      <div className="code-worktree-where">
        <div className="muted">{t('sidebar.worktree')}</div>
        <code>{root}</code>
        {worktree.branch && <div className="muted">{t('sidebar.worktreeBranch', { branch: worktree.branch })}</div>}
      </div>
      {worktree.removable ? (
        <RadioGroup aria-label={t('sidebar.worktreeChoice')} value={remove ? 'remove' : 'keep'} onValueChange={value => choose(value === 'remove')}>
          <SettingModeOption value="keep" label={t('sidebar.keepWorktree')} description={t('sidebar.keepWorktreeBody')} />
          <SettingModeOption value="remove" label={t('sidebar.removeWorktree')} description={t('sidebar.removeWorktreeBody')} />
        </RadioGroup>
      ) : (
        <p className="muted">{t('sidebar.worktreeStays')}</p>
      )}
      {work && (
        <Callout tone="warning">
          {work.unchecked && <p>{t('sidebar.worktreeUnchecked')}</p>}
          {work.files && <WorkLines label={t('sidebar.worktreeFiles', { count: work.files.total })} work={work.files} />}
          {work.commits && <WorkLines label={t('sidebar.worktreeCommits', { count: work.commits.total })} work={work.commits} />}
        </Callout>
      )}
    </div>
  )
}

function WorkLines({ label, work }: { label: string; work: PwCodeWorktreeLines }) {
  const t = useTranslations('agents')
  return (
    <div>
      <p>{label}</p>
      <pre className="code-worktree-lines">{work.lines.join('\n')}</pre>
      {work.more > 0 && <p className="muted">{t('sidebar.worktreeMore', { count: work.more })}</p>}
    </div>
  )
}
