import {
  type ClaudeAccount as Account,
  type ClaudeAccountAction as AccountAction,
  type ClaudeAccountDetail as AccountDetail,
  type ClaudeAccountPanelState as AccountPanelState,
  type ClaudeUsageLimit as UsageLimit,
  type ClaudeUsageReset as UsageReset,
  claudeAccountChip as accountChip,
  claudeOnMachine as runsOnMachine,
  useClaudeAccountName as useAccountName,
  useClaudeAccountPanel as useAccountPanel,
  useClaudeAccountRemoval as useAccountRemoval,
  useClaudeAccountStartRow as useAccountStartRow,
  useClaudeAccountUsage as useAccountUsage,
  usePwCodeTranslations,
} from '@parallelworks/code'
import {
  Button,
  buttonClasses,
  Checkbox,
  Field,
  IconButton,
  InfoTip,
  Input,
  type ListboxNavigation,
  LoadingRegion,
  type MenuEntry,
  Meter,
  Modal,
  ModalDescription,
  ModalTitle,
  type OpenMenu,
  RefreshButton,
  Skeleton,
  useContextMenu,
  useListboxNavigation,
} from '@parallelworks/ui'
import { type BoundSetting, NoticeCard } from '@parallelworks/ui/ai'
import { AddIcon, CheckIcon, CloseIcon, MoreIcon, SignInIcon, TrashIcon, UserIcon } from '@parallelworks/ui/icons'
import { rowContextMenuProps } from '@parallelworks/ui/list'
import { type RefObject, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useLocale } from 'use-intl'
import { pwCode } from '../api'
import { describeError as defaultDescribeError } from '../PWCodeSession'
import { AccountSignIn } from './AccountSignIn'
import './AccountPanel.css'

const accountCalls = {
  useClaudeAccounts: pwCode.useClaudeAccounts,
  useRefreshClaudeModels: pwCode.useRefreshClaudeModels,
  useRemoveClaudeAccount: pwCode.useRemoveClaudeAccount,
  useMoveClaudeAccountSessions: pwCode.useMoveClaudeAccountSessions,
}

/** What the machine allows, as the session screen reports it. */
export interface MachineAccountFacts {
  installed: boolean
  allowed: boolean
  login: boolean
  accountRemove: boolean
}

export interface AccountPanelBodyProps {
  machine: string
  machineName: string
  sessionId?: string | undefined
  /** Leaves the panel; a choice for the session closes it too. */
  onClose?: (() => void) | undefined
  /** The session's account setting (`settings.account`). Without one the
   *  panel only manages the machine's accounts: nothing is chosen. */
  account?: BoundSetting | undefined
  /** The session's turn waits at its account's limit (`screen.waitingAtLimit`). */
  waitingAtLimit?: boolean | undefined
  onMovedOthers?: ((message: string, moved: number, account: string) => void) | undefined
  onMoveError?: ((error: unknown) => void) | undefined
  /** The session screen's machine facts; read from the machine when not given. */
  machineFacts?: MachineAccountFacts | undefined
  describeError?: ((error: unknown) => string) | undefined
  /** Inside a settings card that gives the title and frame: the panel
   *  draws neither. */
  bare?: boolean | undefined
}

const noop = () => {}

/** The account panel's content without a modal, for a settings card or the modal below. */
export function AccountPanelBody(props: AccountPanelBodyProps) {
  return <PanelContent {...props} inModal={false} />
}

export interface AccountPanelModalProps extends AccountPanelBodyProps {
  onClose: () => void
}

/** The account panel over the session view, opened from its composer's chip. */
export function AccountPanelModal(props: AccountPanelModalProps) {
  return <PanelContent {...props} inModal />
}

function PanelContent({
  machine,
  machineName,
  sessionId,
  onClose = noop,
  account,
  waitingAtLimit = false,
  onMovedOthers,
  onMoveError,
  machineFacts,
  describeError = defaultDescribeError,
  inModal,
  bare = false,
}: AccountPanelBodyProps & { inModal: boolean }) {
  const t = usePwCodeTranslations('agents')
  const marker = useRef<HTMLSpanElement>(null)
  const returnFocus = useRef<HTMLTextAreaElement | null>(null)
  const panel = useRef<HTMLDivElement>(null)
  const choices = useRef<HTMLDivElement>(null)
  const nameInput = useRef<HTMLInputElement>(null)
  const { machine: info } = pwCode.useMachine(machine)
  const facts = machineFacts ?? runsOnMachine(info)
  // Without the facts given, the machine's are unknown until the list of machines arrives.
  const factsLoading = !machineFacts && !info
  // No session: nothing to choose, so a row's pick only opens a sign-in.
  const browsing = !account
  const [unbound] = useState<BoundSetting>(() => ({ value: undefined, onChange: noop }))
  const setting = account ?? unbound
  const accounts = useAccountPanel(accountCalls, {
    machine,
    machineName,
    sessionId,
    claudeInstalled: facts.installed,
    claudeAllowed: facts.allowed,
    claudeLogin: facts.login,
    claudeAccountRemove: facts.accountRemove,
    waitingAtLimit,
    account: setting,
    onClose,
    onMovedOthers,
    onMoveError,
  })
  const { view } = accounts
  const rowActions = (a: Account) => accounts.actions(a).filter(x => !browsing || x.kind !== 'use')
  const menu = useAccountMenu(rowActions)
  const choose = (a: Account) => {
    if (!browsing) accounts.choose(a)
    else if (a.signInRequired) accounts.choose(a)
  }
  // The list brings its highlighted account into view only while the reader is in it. Its
  // start row, set on load, would otherwise scroll the page around a settings card to it.
  const [inList, setInList] = useState(false)
  const navigation = useListboxNavigation({
    items: accounts.accounts,
    open: inList,
    onChoose: choose,
    onDismiss: inModal ? onClose : undefined,
    isDisabled: accounts.isDisabled,
    chooseKeys: [],
    homeEnd: true,
    onContextMenu: (a, _index, anchor) => menu.opener(a)(anchor, menu.actions(a)) === true,
  })
  useAccountStartRow(accounts, navigation.setActive)
  usePanelFocus({
    marker,
    returnFocus,
    panel,
    choices,
    nameInput,
    adding: view.kind === 'add',
    // In a settings card the list waits for the reader rather than taking focus.
    listFocusable: inModal && view.kind !== 'signIn' && view.kind !== 'remove' && !accounts.loading && accounts.accounts.length > 0,
  })

  const body = factsLoading ? (
    <LoadingRegion label={t('claude.loadingAccounts')} className="pwcode-accounts-pad">
      <Skeleton height={52} count={3} />
    </LoadingRegion>
  ) : accounts.notice !== null ? (
    <div className="pwcode-accounts-pad"><NoticeCard tone="warning">{accounts.notice}</NoticeCard></div>
  ) : view.kind === 'signIn' ? (
    <AccountSignIn
      machine={machine}
      machineName={machineName}
      sessionId={sessionId}
      account={view.account}
      onComplete={accounts.finishSignIn}
      onUse={browsing ? undefined : accounts.selectSignedIn}
      onBack={accounts.back}
      describeError={describeError}
    />
  ) : view.kind === 'add' ? (
    <AddAccount nameInput={nameInput} disabled={!!setting.disabled} onBack={accounts.back} onContinue={accounts.startSignIn} />
  ) : view.kind === 'remove' ? (
    <RemoveAccount body={accounts.removeBody} remove={() => accounts.removeAccount(view.account)} onBack={accounts.back} describeError={describeError} />
  ) : (
    <AccountList
      accounts={accounts}
      navigation={navigation}
      onInList={setInList}
      menu={menu}
      selected={setting.value}
      hasActions={!setting.disabled}
      browsing={browsing}
      choices={choices}
      describeError={describeError}
    />
  )
  const header = (
    <AccountPanelHeader accounts={accounts} machineName={machineName} onClose={inModal ? onClose : undefined} inModal={inModal} titled={!bare} />
  )

  if (!inModal) {
    return (
      <div className={`pwcode-accounts ${bare ? 'pwcode-accounts-bare' : 'pwcode-accounts-card'}`} ref={panel} data-testid="pwcode-accounts">
        {header}
        {body}
      </div>
    )
  }
  return (
    <>
      <span hidden ref={marker} />
      <Modal
        ref={panel}
        open
        aria-modal
        onClose={onClose}
        align="center"
        size={view.kind === 'signIn' ? 'xl' : 'md'}
        initialFocus={() => choices.current ?? panel.current ?? true}
        finalFocus={() => returnFocus.current ?? false}
        className="pwcode-accounts pwcode-accounts-modal"
        data-testid="pwcode-accounts"
      >
        {header}
        {body}
      </Modal>
    </>
  )
}

function AccountPanelHeader({ accounts, machineName, onClose, inModal, titled }: {
  accounts: AccountPanelState
  machineName: string
  onClose: (() => void) | undefined
  inModal: boolean
  titled: boolean
}) {
  const t = usePwCodeTranslations('agents')
  // The modal's title and description name its dialog; a card's are plain text.
  const title = inModal
    ? <ModalTitle className="pwcode-accounts-title">{accounts.title}</ModalTitle>
    : <h3 className="pwcode-accounts-title">{accounts.title}</h3>
  const description = inModal
    ? <ModalDescription className="pwcode-accounts-description" title={machineName}>{accounts.description}</ModalDescription>
    : <p className="pwcode-accounts-description" title={machineName}>{accounts.description}</p>
  return (
    <div className="pwcode-accounts-header">
      <div className="pwcode-accounts-heading">
        {titled && title}
        <div className="pwcode-accounts-subline">
          {description}
          {accounts.signInNote !== null && <InfoTip text={accounts.signInNote} />}
        </div>
      </div>
      {accounts.showAdd && <IconButton label={t('claude.addAccount')} onClick={accounts.add} icon={<AddIcon />} size="xs" />}
      {accounts.showRefresh && <RefreshButton onRefresh={accounts.refresh} refreshing={accounts.refreshing} size="xs" iconOnly />}
      {onClose && <IconButton label={t('claude.close')} onClick={onClose} icon={<CloseIcon />} size="xs" />}
    </div>
  )
}

function actionIcon(kind: AccountAction['kind']) {
  switch (kind) {
    case 'use':
      return <CheckIcon />
    case 'remove':
      return <TrashIcon />
    default:
      return <SignInIcon />
  }
}

function useAccountMenu(accountActions: (account: Account) => AccountAction[]) {
  const { openMenu, contextMenu } = useContextMenu()
  const [openFor, setOpenFor] = useState<string | null>(null)
  const actions = (a: Account): MenuEntry[] =>
    accountActions(a).map(action => ({
      label: action.label,
      icon: actionIcon(action.kind),
      ...(action.destructive && { destructive: true }),
      onClick: action.run,
    }))
  // However a row's menu opens, its row stays lit until the menu closes.
  const opener = (a: Account): OpenMenu => (at, items, options) => {
    const opened = openMenu(at, items, {
      ...options,
      onClose: () => {
        setOpenFor(null)
        options?.onClose?.()
      },
    })
    if (opened !== false) setOpenFor(a.name)
    return opened
  }
  return { contextMenu, openFor, actions, opener }
}

function AccountList({ accounts, navigation, onInList, menu, selected, hasActions, browsing, choices, describeError }: {
  accounts: AccountPanelState
  navigation: ListboxNavigation<Account>
  onInList: (inList: boolean) => void
  menu: ReturnType<typeof useAccountMenu>
  selected: string | null | undefined
  hasActions: boolean
  browsing: boolean
  choices: RefObject<HTMLDivElement | null>
  describeError: (error: unknown) => string
}) {
  const t = usePwCodeTranslations('agents')
  const showsList = accounts.loading || accounts.accounts.length > 0
  return (
    <>
      {Boolean(accounts.error) && (
        <div className="pwcode-accounts-error"><NoticeCard tone="warning">{describeError(accounts.error)}</NoticeCard></div>
      )}
      {showsList && (
        <div aria-hidden className="pwcode-accounts-columns">
          <div className="pwcode-account-frame">
            <div className="pwcode-account-grid">
              <span className="pwcode-accounts-name-head">{t('claude.shortAccount')}</span>
              <span>{t('claude.fiveHourColumn')}</span>
              <span>{t('claude.weekColumn')}</span>
            </div>
            <span className="pwcode-account-actions" />
          </div>
        </div>
      )}
      {accounts.loading && (
        <LoadingRegion label={t('claude.loadingAccounts')} className="pwcode-accounts-list">
          <Skeleton height={52} count={5} />
        </LoadingRegion>
      )}
      {!accounts.loading && accounts.accounts.length > 0 && (
        <div
          id={navigation.listId}
          role="listbox"
          ref={choices}
          tabIndex={0}
          aria-label={t('claude.accounts')}
          aria-activedescendant={navigation.hostProps['aria-activedescendant']}
          onFocus={() => onInList(true)}
          onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget)) onInList(false) }}
          onKeyDown={event => {
            // Keys pressed on a row's actions button stay with that button.
            if (event.target === event.currentTarget) navigation.handleKey(event)
          }}
          className="pwcode-accounts-list"
        >
          {accounts.accounts.map((a, i) => (
            <AccountRow
              key={a.name}
              account={a}
              option={navigation.optionProps(i)}
              selected={!browsing && a.name === selected}
              browsing={browsing}
              disabled={accounts.isDisabled(a)}
              loaded={accounts.loadedUsage}
              hasActions={hasActions}
              actions={() => menu.actions(a)}
              openMenu={menu.opener(a)}
              menuOpen={menu.openFor === a.name}
              detail={accounts.detail(a)}
            />
          ))}
        </div>
      )}
      {menu.contextMenu}
      {accounts.empty && (
        <div className="pwcode-accounts-empty">
          <p>{t('claude.noAccounts')}</p>
          {accounts.emptyHint !== null && <p>{accounts.emptyHint}</p>}
        </div>
      )}
      {accounts.moveOthersLabel !== null && (
        <div className="pwcode-accounts-move">
          <Field controlFirst label={accounts.moveOthersLabel}>
            <Checkbox checked={accounts.moveOthers} onCheckedChange={accounts.setMoveOthers} />
          </Field>
        </div>
      )}
    </>
  )
}

function RemoveAccount({ body, remove, onBack, describeError }: {
  body: string | null
  remove: () => Promise<void>
  onBack: () => void
  describeError: (error: unknown) => string
}) {
  const t = usePwCodeTranslations('agents')
  const back = useRef<HTMLButtonElement>(null)
  const { pending, error, confirm } = useAccountRemoval(remove)
  useEffect(() => {
    back.current?.focus()
  }, [])
  return (
    <div className="pwcode-accounts-pad pwcode-accounts-stack">
      {Boolean(error) && <NoticeCard tone="warning">{describeError(error)}</NoticeCard>}
      <p className="pwcode-accounts-text">{body}</p>
      <div className="pwcode-accounts-buttons">
        <Button ref={back} variant="secondary" disabled={pending} onClick={onBack}>{t('claude.backToAccounts')}</Button>
        <Button variant="danger" loading={pending} onClick={() => void confirm()}>{t('claude.removeAccount')}</Button>
      </div>
    </div>
  )
}

function AddAccount({ nameInput, disabled, onBack, onContinue }: {
  nameInput: RefObject<HTMLInputElement | null>
  disabled: boolean
  onBack: () => void
  onContinue: (name: string) => void
}) {
  const t = usePwCodeTranslations('agents')
  const { name, invalid, maxLength, change, submit } = useAccountName(onContinue)
  return (
    <form
      className="pwcode-accounts-pad pwcode-accounts-stack"
      onSubmit={event => {
        event.preventDefault()
        submit()
      }}
    >
      <Input
        ref={nameInput}
        label={t('claude.accountName')}
        description={t('claude.accountNameHelp')}
        value={name}
        onChange={event => change(event.target.value)}
        error={invalid ? t('claude.invalidAccountName') : undefined}
        autoComplete="off"
        spellCheck={false}
        maxLength={maxLength}
        required
      />
      <div className="pwcode-accounts-buttons">
        <Button variant="secondary" onClick={onBack}>{t('claude.backToAccounts')}</Button>
        <Button type="submit" variant="primary" disabled={disabled}>{t('claude.continueSignIn')}</Button>
      </div>
    </form>
  )
}

const focusOnControlIn = (container: HTMLElement | null) =>
  document.activeElement !== container && !!container?.contains(document.activeElement)

/** Remembers the composer to return to, and moves focus to the name field or the list as the panel's view changes. */
function usePanelFocus({ marker, returnFocus, panel, choices, nameInput, adding, listFocusable }: {
  marker: RefObject<HTMLSpanElement | null>
  returnFocus: RefObject<HTMLTextAreaElement | null>
  panel: RefObject<HTMLDivElement | null>
  choices: RefObject<HTMLDivElement | null>
  nameInput: RefObject<HTMLInputElement | null>
  adding: boolean
  listFocusable: boolean
}) {
  useLayoutEffect(() => {
    returnFocus.current = marker.current?.closest('.chat-composer')?.querySelector('textarea') ?? null
  }, [marker, returnFocus])
  useEffect(() => {
    if (adding) {
      nameInput.current?.focus()
    } else if (listFocusable && !focusOnControlIn(panel.current)) {
      // A list that arrives late must not pull focus from a control the reader moved to.
      choices.current?.focus()
    }
  }, [adding, listFocusable, nameInput, panel, choices])
}

function AccountRow({ account, option, selected, browsing, disabled, loaded, hasActions, actions, openMenu, menuOpen, detail }: {
  account: Account
  option: ReturnType<ListboxNavigation<Account>['optionProps']>
  selected: boolean
  browsing: boolean
  disabled: boolean
  loaded: boolean
  hasActions: boolean
  actions: () => MenuEntry[]
  openMenu: OpenMenu
  menuOpen: boolean
  detail: AccountDetail | null
}) {
  const t = usePwCodeTranslations('agents')
  const { onMouseDown, onMouseMove, onClick, ...optionAttributes } = option
  const entries = actions()
  const showsActions = hasActions && entries.length > 0
  return (
    // The option inside carries the role; the whole row, its actions gutter included, takes the pointer.
    <div
      className="pwcode-account-frame pwcode-account-row"
      data-selected={selected || undefined}
      data-disabled={disabled || undefined}
      data-menu-open={menuOpen || undefined}
      onMouseDown={onMouseDown}
      onMouseMove={onMouseMove}
      onClick={onClick}
      {...rowContextMenuProps(openMenu, actions)}
    >
      <div {...optionAttributes} role="option" tabIndex={-1} aria-checked={selected} className="pwcode-account-grid pwcode-account-option">
        <span className="pwcode-account-who">
          {!browsing && (
            <span aria-hidden className="pwcode-account-mark" data-hidden={account.signInRequired || undefined}>
              {selected && <CheckIcon />}
            </span>
          )}
          <span className="pwcode-account-names">
            <span className="pwcode-account-name" data-muted={account.signInRequired || undefined} title={account.name}>{account.name}</span>
            {detail && (
              <span className={detail.kind === 'signInHint' ? 'pwcode-account-detail is-hint' : 'pwcode-account-detail'} title={detail.text}>
                {detail.text}
              </span>
            )}
          </span>
        </span>
        <AccountUsage account={account} loaded={loaded} />
      </div>
      <div className="pwcode-account-actions">
        {showsActions && (
          // Pointer and touch only: keys open the same menu with Shift+F10 or the menu key,
          // so the listbox keeps option-only children and one Tab stop.
          <button
            type="button"
            tabIndex={-1}
            aria-hidden
            title={t('claude.accountActions', { account: account.name })}
            data-popup-open={menuOpen || undefined}
            onClick={event => {
              event.stopPropagation()
              openMenu(event.currentTarget, actions())
            }}
            className={`${buttonClasses({ variant: 'ghost', size: 'xs', iconOnly: true })} pwcode-account-more`}
          >
            <MoreIcon />
          </button>
        )}
      </div>
    </div>
  )
}

function AccountUsage({ account, loaded }: { account: Account; loaded: boolean }) {
  const { status, limits } = useAccountUsage(account, loaded)
  if (status) return <span className="pwcode-account-status">{status}</span>
  return <>{limits.map(limit => <AccountLimit key={limit.window} limit={limit} />)}</>
}

function AccountLimit({ limit }: { limit: UsageLimit }) {
  const t = usePwCodeTranslations('agents')
  const locale = useLocale()
  const { label, used } = limit
  return (
    <div className="pwcode-account-limit">
      {typeof used === 'number' ? (
        <Meter
          value={used}
          aria-label={label}
          label={<span className="sr-only">{label}</span>}
          locale={locale}
          tone="accent"
          size="sm"
          className="pwcode-account-meter"
          trackClassName="pwcode-account-track"
        />
      ) : (
        <span className="pwcode-account-muted">
          <span aria-hidden>-</span>
          <span className="sr-only">{t('claude.usageUnavailable')}</span>
        </span>
      )}
      <AccountReset reset={limit.reset} />
    </div>
  )
}

function AccountReset({ reset }: { reset: UsageReset | null }) {
  if (!reset) return null
  return (
    <span className="pwcode-account-reset" title={reset.at}>
      <span className="pwcode-account-reset-short">{reset.shortCountdown}</span>
      <span className="pwcode-account-reset-long">{reset.countdown}</span>
    </span>
  )
}

export interface AccountChipProps {
  /** The session's model; the chip shows only for a model that runs on an account. */
  model: string | null | undefined
  /** The session's account setting's value. */
  account: string | null | undefined
  disabled?: boolean | undefined
  onOpen: () => void
}

/** The composer chip naming the session's account, which opens the account panel. */
export function AccountChip({ model, account, disabled, onOpen }: AccountChipProps) {
  const t = usePwCodeTranslations('agents')
  const chip = useMemo(() => accountChip(model, account, t), [model, account, t])
  if (!chip) return null
  return (
    <button
      type="button"
      className="pwcode-account-chip"
      data-testid="pwcode-account-chip"
      disabled={disabled}
      aria-haspopup="dialog"
      aria-label={chip.label}
      title={chip.label}
      onClick={onOpen}
    >
      <UserIcon className="pwcode-account-chip-icon" aria-hidden />
      <span className="pwcode-ellipsis">{chip.account}</span>
    </button>
  )
}

