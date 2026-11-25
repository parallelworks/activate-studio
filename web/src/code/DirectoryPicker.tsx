import { cloneElement, Fragment, type KeyboardEvent, type ReactElement, type Ref, useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { directoryChip, listedText, pickedOnClose, type PwCodeApi, type PwCodePathRow, type RecentDirectory } from '@parallelworks/code'
import { Input, Kbd, ListboxGroup, ListboxOption, LoadingRegion, Popover, Skeleton, useListboxNavigation } from '@parallelworks/ui'
import { ArrowUpIcon, ChevronDownIcon, ChevronRightIcon, FolderIcon, FolderOpenIcon, HistoryIcon, RepositoryIcon } from '@parallelworks/ui/icons'
import { useTranslations } from 'use-intl'
import { describeError } from '../pwcode/PWCodeSession'
import { pwCodePage } from './api'
import type { Translate } from './parts'
import { nameTip } from '../components/NameTip'

/**
 * The folder a session starts in, as PW Code on the web chooses it (its
 * DirectoryPicker): a chip that opens a path field listing the machine's
 * folders under what is typed, with the recent ones first. Tab opens a
 * folder, Enter uses it, and a path typed and left is still the one meant.
 */

type Draft = { typed: string; home: string | undefined; edited: boolean }
/** Its full height and the space around it: the field, a list of 20rem and the keys. */
const FULL_HEIGHT = 420
/** Below the row, or above it where there is more room and below is too little. */
function roomier(row: Element | null): 'top' | 'bottom' {
  if (!row) return 'bottom'
  const { top, bottom } = row.getBoundingClientRect()
  const below = window.innerHeight - bottom
  return below >= FULL_HEIGHT || below >= top ? 'bottom' : 'top'
}

type Machine = Parameters<typeof pwCodePage.usePathCompletion>[0]['machine'] & {}

export function DirectoryPicker({ api = pwCodePage, machine, directory, recents, onPick, trigger, label, placeholder, anew = false }: {
  /** The client whose directories route lists the machine's folders. */
  api?: Pick<PwCodeApi, 'usePathCompletion'>
  machine: Machine
  directory: string
  recents: readonly RecentDirectory[]
  onPick: (path: string) => void
  /** What opens it in place of the chip, and what names it. */
  trigger?: ReactElement
  label?: string
  /** An example, in place of a code folder's. */
  placeholder?: string
  /** A folder that is not there yet can be named, for one that is made later. */
  anew?: boolean
}) {
  const t = useTranslations('agents')
  const [open, setOpen] = useState(false)
  const [side, setSide] = useState<'top' | 'bottom'>('bottom')
  const row = useRef<HTMLElement>(null)
  const draft = useRef<Draft | null>(null)
  const escaped = useRef(false)
  const keepDraft = useCallback((next: Draft) => { draft.current = next }, [])

  useEffect(() => {
    if (!open) return
    const onKey = (event: globalThis.KeyboardEvent) => { if (event.key === 'Escape') escaped.current = true }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [open])

  useEffect(() => {
    if (!open) return
    const onResize = () => setSide(roomier(row.current))
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [open])

  const settle = (next: boolean) => {
    if (next) {
      draft.current = null
      escaped.current = false
      setSide(roomier(row.current))
      setOpen(true)
      return
    }
    setOpen(false)
    const left = draft.current
    draft.current = null
    const path = left && !escaped.current ? pickedOnClose(left, directory) : null
    if (path) onPick(path)
  }
  const use = (path: string) => {
    draft.current = null
    setOpen(false)
    onPick(path)
  }
  const narrow = typeof window !== 'undefined' && window.matchMedia?.('(max-width: 639.98px)').matches === true
  const chip = directoryChip(directory, t)
  const button = (trigger ?? (
    <button type="button" aria-label={chip.label} data-testid="agents-directory-chip" className="code-chip">
      <FolderIcon aria-hidden className="code-chip-icon" />
      <span className="code-chip-label">{chip.text}</span>
      <ChevronDownIcon className="code-chip-caret" aria-hidden />
    </button>
  )) as ReactElement<{ ref?: Ref<HTMLElement> }>

  return (
    <Popover
      open={open}
      onOpenChange={settle}
      align="start"
      // Chosen as it opens and kept, so the list never jumps while one types.
      side={side}
      keepSide
      flush
      initialFocus={narrow ? 'popup' : 'first-control'}
      aria-label={label ?? t('start.chooseDirectory')}
      className="code-path-popover"
      trigger={cloneElement(button, { ref: row })}
    >
      <PathField placeholder={placeholder} anew={anew} api={api} machine={machine} directory={directory} recents={recents} typing={!narrow} onDraft={keepDraft} onUse={use} />
    </Popover>
  )
}

function PathField({ placeholder, anew, api, machine, directory, recents, typing, onDraft, onUse }: {
  placeholder?: string
  anew: boolean
  api: Pick<PwCodeApi, 'usePathCompletion'>
  machine: Machine
  directory: string
  recents: readonly RecentDirectory[]
  typing: boolean
  onDraft: (draft: Draft) => void
  onUse: (path: string) => void
}) {
  const t = useTranslations('agents')
  const keysHint = useId()
  const input = useRef<HTMLInputElement>(null)
  const [typed, setTyped] = useState(() => (directory ? listedText(directory) : ''))
  const [untouched, setUntouched] = useState(true)
  const { rows, sections, note, listing, loading } = api.usePathCompletion({ machine, typed, untouched, recents, describeError })
  const home = listing?.home
  useEffect(() => { onDraft({ typed, home, edited: !untouched }) }, [onDraft, typed, home, untouched])
  useEffect(() => { if (typing) input.current?.select() }, [typing])
  useLayoutEffect(() => {
    const field = input.current
    if (field && typed && field.selectionEnd === typed.length) field.scrollLeft = field.scrollWidth
  }, [typed])

  const nav = useListboxNavigation<PwCodePathRow>({
    items: rows,
    chooseKeys: [],
    onChoose: row => (row.opens ? list(row.path) : onUse(row.path)),
  })
  const named = typed.trim().replace(/(.)\/+$/, '$1')
  const full = home && /^~(?=\/|$)/.test(named) ? home + named.slice(1) : named
  const newFolder = anew && !untouched && !loading && /^[/~]/.test(named) && !rows.some(r => r.path === full || r.path === named) ? named : ''
  const edit = (text: string) => {
    setTyped(text)
    setUntouched(false)
    nav.setActive(0)
  }
  const list = (path: string) => {
    edit(listedText(path))
    input.current?.focus()
  }
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    const plain = !(event.shiftKey || event.altKey || event.metaKey || event.ctrlKey)
    if (event.key === 'Tab' && plain && !event.nativeEvent.isComposing) {
      const row = rows[nav.active]
      if (row && row.kind !== 'use') {
        event.preventDefault()
        list(row.path)
      }
      return
    }
    if (event.key === 'Enter' && plain && newFolder && !rows.length) {
      event.preventDefault()
      onUse(newFolder)
      return
    }
    nav.handleKey(event)
  }
  const option = (row: PwCodePathRow) => (
    <PathRow key={`${row.kind}:${row.path}`} row={row} option={nav.optionProps(rows.indexOf(row))} current={row.path === directory} onOpen={() => list(row.path)} t={t} />
  )

  return (
    <div className="code-path">
      <div className="code-path-field">
        <Input
          ref={input}
          size="sm"
          mono
          role="combobox"
          aria-expanded={rows.length > 0}
          aria-label={t('start.workspaceLabel')}
          aria-describedby={keysHint}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          data-1p-ignore
          value={typed}
          onChange={event => edit(event.target.value)}
          onKeyDown={onKeyDown}
          placeholder={placeholder ?? t('start.workspacePlaceholder')}
          {...nav.hostProps}
        />
      </div>
      <div className="code-path-list">
        <div id={nav.listProps.id} role="listbox" aria-label={t('start.directorySuggestions')}>
          {sections.map(section =>
            section.label
              ? <ListboxGroup key={section.key} label={section.label}>{section.rows.map(option)}</ListboxGroup>
              : <Fragment key={section.key}>{section.rows.map(option)}</Fragment>)}
        </div>
        {loading && (
          <LoadingRegion label={t('start.directoriesLoading')} className="code-path-loading">
            <Skeleton width="38%" height={12} />
            <Skeleton width="54%" height={12} />
            <Skeleton width="30%" height={12} />
          </LoadingRegion>
        )}
        {newFolder && (
          <button type="button" className="code-path-new" data-testid="directory-new" onClick={() => onUse(newFolder)}>
            <FolderIcon aria-hidden className="code-path-icon" /><span>New folder <span className="code-mono">{newFolder}</span></span>
          </button>
        )}
        {note !== null && !(newFolder && !rows.length) && <p role="status" data-testid="agents-directory-note" className="code-path-note">{note}</p>}
      </div>
      <p id={keysHint} className="code-path-keys">
        <KeyHint keys={['↑', '↓']} does={t('start.pathKeys.move')} />
        <KeyHint keys="Enter" does={t('start.pathKeys.use')} />
        <KeyHint keys="Tab" does={t('start.pathKeys.open')} />
        <KeyHint keys="Escape" does={t('start.pathKeys.cancel')} />
      </p>
    </div>
  )
}

function KeyHint({ keys, does }: { keys: string | readonly string[]; does: string }) {
  return <span><Kbd keys={keys} size="xs" />{does}</span>
}

function PathRow({ row, option, current, onOpen, t }: {
  row: PwCodePathRow
  option: ReturnType<ReturnType<typeof useListboxNavigation<PwCodePathRow>>['optionProps']>
  current: boolean
  onOpen: () => void
  t: Translate
}) {
  const checked = current ? true : undefined
  const tipped = (name: string, detail?: string) => ({ ...option, ...nameTip(name, detail) })
  switch (row.kind) {
    case 'use':
      return <ListboxOption option={tipped(row.path)} icon={<FolderOpenIcon aria-hidden className="code-path-icon" />} label={<span className="code-mono" data-tip-text="">{row.path}</span>} description={row.detail} checked={checked} />
    case 'recent':
      return (
        <ListboxOption option={tipped(row.name, row.detail)} icon={<HistoryIcon aria-hidden className="code-path-icon" />} checked={checked}
          label={<span className="code-path-recent code-mono"><span data-tip-text="">{row.name}</span><span className="muted">{row.detail}</span></span>} />
      )
    case 'up':
      return (
        <ListboxOption option={option} inline icon={<ArrowUpIcon aria-hidden className="code-path-icon" />}
          label={<><span aria-hidden="true" className="code-mono">..</span><span className="sr-only">{t('start.parentDirectory')}</span></>}
          description={<span className="code-mono">{row.detail}</span>} />
      )
    case 'folder':
      return (
        <ListboxOption
          option={tipped(row.name)}
          icon={row.repository
            ? <span title={t('start.repository')} className="code-path-icon-wrap"><RepositoryIcon role="img" aria-label={t('start.repository')} className="code-path-icon" /></span>
            : <FolderIcon aria-hidden className="code-path-icon" />}
          label={<span className="code-mono" data-tip-text="">{row.name}</span>}
          checked={checked}
          hint={
            <button type="button" tabIndex={-1} aria-hidden="true" title={t('start.openDirectory', { name: row.name })} data-testid="agents-directory-open" className="code-path-open"
              onClick={event => { event.stopPropagation(); onOpen() }}>
              <ChevronRightIcon />
            </button>
          }
        />
      )
  }
}
