import { ReactElement, Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react'
import { CountBadge, IconButton, SearchInput, StatusDot } from '@parallelworks/ui'
import { buildOpenHash, parseOpenHash } from './nav'
import { currentLibrary, linkTarget, setLibrary, settleLibraries } from './api'
import { rememberHash } from './lastLocation'
import { dropItem, itemInAddress, openItem, replacedText } from './itemLinks'
import { LinkNotice, LinkToCopy, type LinkState } from './components/LinkNotice'
import { leaveUnsaved, unsavedPath } from './unsaved'
import { ChatView } from './views/ChatView'
import { LibraryPageFrame } from './components/LibraryImage'
import { DagViewer } from './components/DagViewer'
import { SessionView } from './components/SessionView'
const ModelViewer = lazy(() => import('./components/ModelViewer').then(m => ({ default: m.ModelViewer })))
import { LibraryView, type Reveal } from './views/LibraryView'
import { SearchView } from './views/SearchView'
import { QueryView } from './views/QueryView'
import { OverviewView } from './views/OverviewView'
import { HistoryView } from './views/HistoryView'
import { useInstall } from './install'
import { WorkflowsView } from './views/WorkflowsView'
import { CodePage } from './code/CodePage'
import { useCodeWaiting } from './code/waiting'
import { countFor, setTransfersShown, useNotices, useNoticesRead } from './notices'
import { NoticesView } from './views/NoticesView'
import { AgentsView } from './views/AgentsView'
import { HelpView } from './views/HelpView'
import { SettingsView } from './views/SettingsView'
import { TransfersView } from './views/TransfersView'
import { TRANSFERS_SEEN, transferApi, transfersPresent, type TransferSummary } from './transferApi'
import { SetupWizard } from './views/SetupWizard'
import { FirstVisitCard } from './components/FirstVisitCard'
import { StatusFooter } from './components/StatusFooter'
import { embedded, signInHref } from './signIn'
import { ClassificationBanner, bannerInk, bannerShownColor } from './components/ClassificationBanner'
import { aiPagesMounted, applyNavWidth, sectionAllowed, setTabIcon, shownView, useAppConfig } from './config'
import { NameTips } from './components/NameTip'
import { NavResize } from './components/NavResize'
import { applyAccent, applySurface } from './accents'
import { onThemeChoice, rememberGround } from './theme'
import { FullViewHost, RunPane } from './components/RunPane'
import { startLive, useWatch } from './live'
import { PageHeadSlot } from './pageHead'
import { PanelToggle, StatusPanel } from './components/StatusPanel'
import { setPanelView } from './panelState'
import { studioMark } from './studioMark'
import { SEARCH_NOTE, searchReach, useSearchScope } from './searchReach'

type ViewId = 'chat' | 'library' | 'transfers' | 'search' | 'query' | 'overview' | 'history' | 'agents' | 'workflows' | 'code' | 'notices' | 'settings' | 'help'
/** Pages whose sidebar item always leads to their main page: Workflows from a
 *  workflow's form, Transfers from New transfer or a transfer's details. */
const HOME_EVENT: Partial<Record<ViewId, string>> = { workflows: 'ade:workflows-home', transfers: 'ade:transfers-home' }
const goHome = (id: ViewId) => { const e = HOME_EVENT[id]; if (e) window.dispatchEvent(new Event(e)) }
export type Display = { kind: 'file'; target: string; q?: string; lib?: string } | { kind: 'workflow_dag'; target: string }

// The destinations that keep a spot in the phone bottom bar (Notices with
// them, for its count); the rest move into the More sheet.
const MOBILE_PRIMARY = new Set<string>(['chat', 'library', 'search', 'overview', 'notices'])

// The list-style views share one header bar: the section's name and a
// short note on what it covers. Assistant, the library, settings, and help lay
// out their own tops.
const PAGE_NOTES: Partial<Record<ViewId, string>> = {
  // Search's note says what the libraries searched can match (searchReach.ts).
  query: 'Sizes, ages, types, labels, and locations, from the index',
  overview: 'What the knowledge base holds and how the index is doing',
  agents: '',
  notices: 'What needs you across the Studio',
  workflows: 'Each runs under your own account',
  // The Code page's own start says what it is for.
  code: '',
  history: 'The corpus at each index pass',
  transfers: 'Copy, move and sync files between libraries, this computer, clusters and saved locations',
}

const NAV: { id: ViewId; label: string; icon: ReactElement }[] = [
  {
    id: 'chat',
    label: 'Assistant',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M6.5 3 7.7 6.3 11 7.5 7.7 8.7 6.5 12 5.3 8.7 2 7.5 5.3 6.3 6.5 3z"/><path d="M12.5 1.8v3.4M10.8 3.5h3.4M12.5 10.8v3M11 12.3h3"/></svg>,
  },
  {
    id: 'library',
    label: 'Library',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>,
  },
  {
    id: 'transfers',
    label: 'Transfers',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 5h10.5M10 2.5 12.5 5 10 7.5"/><path d="M14 11H3.5M6 8.5 3.5 11 6 13.5"/></svg>,
  },
  {
    id: 'search',
    label: 'Search',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="7" cy="7" r="4.5"/><path d="m10.5 10.5 3.5 3.5"/></svg>,
  },
  {
    id: 'query',
    label: 'Query',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><ellipse cx="8" cy="3.5" rx="5.5" ry="2"/><path d="M2.5 3.5v9c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2v-9"/><path d="M2.5 8c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2"/></svg>,
  },
  {
    id: 'overview',
    label: 'Stats',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 13.5h12"/><path d="M3.5 13.5V8"/><path d="M7 13.5V4.5"/><path d="M10.5 13.5V6.5"/><path d="M14 13.5V2.5"/></svg>,
  },
  {
    id: 'agents',
    label: 'Agents',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="5.5" r="2.8"/><path d="M2.6 14c.6-3 2.8-4.5 5.4-4.5s4.8 1.5 5.4 4.5"/></svg>,
  },
  {
    id: 'workflows',
    label: 'Workflows',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><rect x="1.5" y="2" width="5" height="4" rx="1"/><rect x="9.5" y="10" width="5" height="4" rx="1"/><path d="M4 6v3a1.5 1.5 0 0 0 1.5 1.5h4"/><path d="m8 9 1.5 1.5L8 12"/></svg>,
  },
  {
    id: 'code',
    label: 'Code',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="m5.5 4.5-3.5 3.5 3.5 3.5"/><path d="m10.5 4.5 3.5 3.5-3.5 3.5"/></svg>,
  },
  {
    id: 'history',
    label: 'History',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="6.5"/><path d="M8 4.4V8l2.6 1.6"/></svg>,
  },
  {
    id: 'notices',
    label: 'Notices',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M12.5 11V7a4.5 4.5 0 0 0-9 0v4L2.2 12.5h11.6L12.5 11z"/><path d="M6.6 14.2a1.6 1.6 0 0 0 2.8 0"/></svg>,
  },
  {
    id: 'settings',
    label: 'Settings',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="2.4"/><path d="M8 1.6v2M8 12.4v2M1.6 8h2M12.4 8h2M3.5 3.5l1.4 1.4M11.1 11.1l1.4 1.4M12.5 3.5l-1.4 1.4M4.9 11.1l-1.4 1.4"/></svg>,
  },
  {
    id: 'help',
    label: 'Help',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="6.5"/><path d="M6 6.2a2 2 0 0 1 3.9.6c0 1.2-1.9 1.5-1.9 2.7"/><circle cx="8" cy="11.8" r="0.3" fill="currentColor"/></svg>,
  },
]

// The menu's groups, after the platform's own navigation: a few direct
// destinations, then categories that open to their pages. A group the
// deployment leaves with one page shows that page on its own.
type NavEntry = { id: ViewId } | { group: string; label: string; items: ViewId[] }
const NAV_LAYOUT: NavEntry[] = [
  { id: 'chat' },
  {
    group: 'knowledge', label: 'Knowledge', items: ['library', 'transfers', 'search', 'query', 'overview', 'history'],
  },
  {
    group: 'run', label: 'Run', items: ['workflows', 'agents', 'code'],
  },
]
const navItem = (id: ViewId) => NAV.find(n => n.id === id)!

function readClosedGroups(): Set<string> {
  try { return new Set(JSON.parse(localStorage.getItem('ade-nav-closed') ?? '[]') as string[]) } catch { return new Set() }
}


/** A file link opened in the library it names. With All libraries open it
 *  stays open, and the link's path becomes All mode's path for it (the
 *  library's id first); a link from a chat names no library for the main
 *  one. Otherwise a link to another library switches to it. */
function inOpenLibrary(d: Display | null): Display | null {
  if (d?.kind !== 'file') return d
  if (d.lib === 'all' || currentLibrary() !== 'all') {
    if (d.lib && d.lib !== currentLibrary()) setLibrary(d.lib)
    return d
  }
  return { ...d, target: linkTarget(d.target, d.lib), lib: 'all' }
}
export default function App() {
  const [view, setView] = useState<ViewId>('chat')
  const [display, setDisplay] = useState<Display | null>(null)
  // What is open in the Library is put away only once an edit of it that is
  // not saved is settled.
  const showing = useRef<Display | null>(null)
  showing.current = display
  const showsOpen = useCallback((d: Display) => {
    const cur = showing.current
    if (d.kind !== 'file' || cur?.kind !== 'file') return false
    const to = d.lib === 'all' || currentLibrary() !== 'all' ? d.target : linkTarget(d.target, d.lib)
    return cur.target === to && (!d.lib || d.lib === 'all' || currentLibrary() === 'all' || d.lib === currentLibrary())
  }, [])
  const showDisplay = useCallback((d: Display | null) => {
    if (d && showsOpen(d)) setDisplay(d)
    else leaveUnsaved(() => setDisplay(d))
  }, [showsOpen])
  const [navCollapsed, setNavCollapsed] = useState(() => localStorage.getItem('ade-nav-collapsed') === '1')
  // The sidebar's search box, as on the platform: Enter searches the
  // knowledge base in the Search view, and Cmd+K or Ctrl+K reaches it from
  // anywhere (the Search view itself where the box is hidden).
  const [navQuery, setNavQuery] = useState('')
  const navSearchRef = useRef<HTMLInputElement>(null)
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'k' || e.shiftKey || e.altKey) return
      e.preventDefault()
      const box = navSearchRef.current
      if (box && box.offsetParent !== null) { box.focus(); box.select() } else setView('search')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
  const submitNavSearch = () => {
    const q = navQuery.trim()
    if (!q) return
    setView('search')
    window.dispatchEvent(new CustomEvent('ade:search', { detail: { q } }))
    setNavQuery('')
    navSearchRef.current?.blur()
  }
  // Groups start open; closing one is remembered, like the platform's menu.
  const [closedGroups, setClosedGroups] = useState<Set<string>>(readClosedGroups)
  const toggleGroup = (g: string) => setClosedGroups(prev => {
    const next = new Set(prev)
    if (next.has(g)) next.delete(g); else next.add(g)
    try { localStorage.setItem('ade-nav-closed', JSON.stringify([...next])) } catch { /* not remembered */ }
    return next
  })
  const cfg = useAppConfig()
  const { canInstall, install } = useInstall()
  // A deployment can leave sections out, which is how the Studio becomes an
  // index viewer with no assistant. Settings and Help always stay reachable.
  // The Workflows tab lists platform workflows, so it exists only there, or
  // where Accounts can sign in to the platform (it then asks to sign in).
  // Transfers needs a server that has them: an older one answers 404 and
  // the page stays out of the menu, so the page can ship before the server.
  const [hasTransfers, setHasTransfers] = useState<boolean | null>(null)
  useEffect(() => { void transfersPresent().then(setHasTransfers) }, [])
  // The menu entry's mark: how many transfers are under way, or a warning
  // when one failed in the last day and has not been seen. Followed at 15
  // seconds, and the notices bring it as well. While the Transfers page is
  // open its failures are seen, so the read marks them seen first; a
  // transfer's details mark theirs and send the summary.
  const [xferSummary, setXferSummary] = useState<TransferSummary | null>(null)
  // Sessions waiting on the person, as PW Code on the web's menu counts
  // them, and what else needs them: one read for every mark (notices.ts).
  const codeWaiting = useCodeWaiting(cfg.loaded && sectionAllowed('code', cfg))
  useNoticesRead(cfg.loaded)
  // What the pages follow comes over one socket (live.ts).
  useEffect(() => startLive(), [])
  const notices = useNotices()
  const onTransfers = view === 'transfers'
  useEffect(() => { setTransfersShown(onTransfers) }, [onTransfers])
  useEffect(() => {
    if (notices.transfers && !onTransfers) setXferSummary(notices.transfers)
  }, [notices.transfers, onTransfers])
  useWatch<TransferSummary>('/api/transfers/summary', XFER_EVERY_MS, hasTransfers === true && !onTransfers, setXferSummary)
  useEffect(() => {
    if (!hasTransfers) return
    const read = () => {
      if (onTransfers && document.visibilityState !== 'hidden') transferApi.seen().then(setXferSummary, () => {})
    }
    read()
    const t = onTransfers ? setInterval(read, XFER_EVERY_MS) : undefined
    const seen = (e: Event) => {
      const sum = (e as CustomEvent<TransferSummary | undefined>).detail
      if (sum) setXferSummary(sum)
    }
    window.addEventListener(TRANSFERS_SEEN, seen)
    return () => { clearInterval(t); window.removeEventListener(TRANSFERS_SEEN, seen) }
  }, [hasTransfers, onTransfers])
  // It shows when the server has it and the pages setting allows it.
  const allowed = (id: ViewId) => (id === 'transfers' ? hasTransfers === true && sectionAllowed(id, cfg) : sectionAllowed(id, cfg))
  useEffect(() => {
    if (view !== 'transfers') return
    if (hasTransfers === false || (cfg.loaded && !sectionAllowed('transfers', cfg))) {
      history.replaceState(null, '', location.pathname + location.search)
      setView('chat')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasTransfers, view, cfg.loaded, cfg.sections])
  // A hidden page is not reachable by a link or the address either: asking
  // for one shows the first page that is shown, in place of the request.
  useEffect(() => {
    if (!cfg.loaded || view === 'transfers') return
    const next = shownView(view, NAV.map(n => n.id), cfg)
    if (next === view) return
    history.replaceState(null, '', location.pathname + location.search + (next === 'chat' ? '' : `#view=${next}`))
    setView(next)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cfg.loaded, cfg.sections, cfg.platform, cfg.localCode, cfg.aiEnabled, cfg.accounts, view])

  // First-run setup on a single-user install, and again from Settings. A
  // framed page is a session inside the platform, or an embed, never a first run.
  const [setupOpen, setSetupOpen] = useState(false)
  useEffect(() => {
    const open = () => setSetupOpen(true)
    window.addEventListener('ade:setup-open', open)
    if (!new URLSearchParams(location.search).has('embed') && window.parent === window) {
      fetch('/api/setup/status').then(r => (r.ok ? r.json() : null))
        .then((s: { firstRun?: boolean } | null) => { if (s?.firstRun) setSetupOpen(true) })
        .catch(() => {})
    }
    return () => window.removeEventListener('ade:setup-open', open)
  }, [])

  const [theme, setTheme] = useState<'light' | 'dark' | null>(() => {
    const t = localStorage.getItem('ade-theme')
    return t === 'dark' || t === 'light' ? t : null
  })
  // A choice made in setup or Settings, Appearance paints at once.
  useEffect(() => onThemeChoice(c => setTheme(c === 'system' ? null : c)), [])

  // The device's own preference, live: a phone flipping to dark at sunset
  // carries the app with it unless the user has chosen explicitly. A
  // deployment that brands itself dark stays dark either way, so the
  // system signal only ever adds dark, never strips a deliberate look.
  const [sysDark, setSysDark] = useState(() => matchMedia('(prefers-color-scheme: dark)').matches)
  useEffect(() => {
    const mq = matchMedia('(prefers-color-scheme: dark)')
    const on = () => setSysDark(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])

  // Phone-only: the bottom bar keeps the main destinations and the rest
  // live in a sheet, along with the status footer, which has no room in a
  // 48px bar.
  const [moreOpen, setMoreOpen] = useState(false)

  // Returning to a view that was hidden is a size change for anything
  // inside it; some components only re-measure on a resize signal.
  useEffect(() => {
    const t = setTimeout(() => window.dispatchEvent(new Event('resize')), 0)
    return () => clearTimeout(t)
  }, [view])

  useEffect(() => { localStorage.setItem('ade-nav-collapsed', navCollapsed ? '1' : '0') }, [navCollapsed])
  // The status panel steps aside for the side view of the page on screen.
  useEffect(() => { setPanelView(view) }, [view])
  const searchScope = useSearchScope()
  const pageNote = view === 'search' ? SEARCH_NOTE[searchReach(cfg.libraries ?? [], searchScope)]
    : view === 'code' && !cfg.platform && cfg.localCode ? 'Your pw code sessions on this computer'
    : PAGE_NOTES[view]
  useEffect(() => { if (cfg.loaded) document.title = cfg.appName }, [cfg])
  // The tab's library: its own choice, else All libraries where there are
  // several, once the configuration says which exist.
  const libIds = (cfg.libraries ?? []).map(l => l.id).join(',')
  useEffect(() => { if (cfg.loaded) settleLibraries(libIds ? libIds.split(',') : []) }, [cfg.loaded, libIds])
  // A link to a file or folder (itemLinks.ts), in the address the page was
  // opened at or a later one in it. It is followed once the libraries are
  // known and settled above, and stays in the address until then, where a
  // sign-in on the way finds it.
  const [link, setLink] = useState<LinkState | null>(() => {
    const id = itemInAddress(location)
    return id ? { id, state: 'opening' } : null
  })
  const [reveal, setReveal] = useState<Reveal | null>(null)
  const [replaced, setReplaced] = useState<{ path: string; text: string } | null>(null)
  const opening = link?.state === 'opening' ? link : null
  useEffect(() => {
    if (!opening || !cfg.loaded) return
    let live = true
    void openItem(opening.id).then(o => {
      if (!live) return
      if (o.state !== 'opened') {
        if (o.state === 'signed-out') {
          if (o.signIn && !embedded()) { location.assign(signInHref(o.signIn)); return }
        } else {
          dropItem()
          setView('library')
        }
        setLink({ id: opening.id, ...o })
        return
      }
      dropItem()
      setLink(null)
      leaveUnsaved(() => {
        const to = inOpenLibrary({ kind: 'file', target: o.path, lib: o.library }) as { target: string; lib?: string }
        // All libraries names the library first, with nothing after it for
        // the library itself.
        const at = to.target.replace(/\/$/, '')
        setReplaced(o.replaced ? { path: at, text: replacedText(o.type) } : null)
        if (o.type === 'dir') {
          setDisplay(null)
          setReveal({ path: at })
        } else setDisplay({ kind: 'file', ...to })
        setView('library')
      })
    })
    return () => { live = false }
  }, [opening, cfg.loaded])
  const effectiveTheme = theme ?? (sysDark ? 'dark' : cfg.theme)
  // A mark drawn in dark ink disappears on a dark background, so a
  // deployment may configure a second image; fall back to the one image
  // when it has not.
  const brandIcon = effectiveTheme === 'dark' ? (cfg.iconUrlDark ?? cfg.iconUrl) : cfg.iconUrl
  useEffect(() => {
    // The remembered brand fills these on the first frame, so this runs
    // without waiting for the configuration request.
    // With no icon of its own (one just removed, say), the bundled mark.
    const favicon = (effectiveTheme === 'dark' ? (cfg.faviconUrlDark ?? cfg.faviconUrl) : cfg.faviconUrl) ?? (cfg.loaded ? '/favicon.svg' : null)
    if (!favicon) return
    setTabIcon(favicon)
  }, [cfg, effectiveTheme])
  useEffect(() => { if (cfg.loaded) { applyAccent(cfg.accent); applySurface(cfg.surface) } }, [cfg])
  useEffect(() => { if (cfg.loaded) applyNavWidth(cfg.navWidth) }, [cfg.loaded, cfg.navWidth])
  useEffect(() => {
    // An embed document is told its theme by the page hosting it, and that
    // instruction outranks every local derivation: the iframe must match
    // its parent, whatever this document would have decided for itself.
    const told = new URLSearchParams(location.search).get('theme')
    const effective = (told === 'dark' || told === 'light')
      ? told : theme ?? (sysDark ? 'dark' : cfg.theme)
    document.documentElement.dataset.theme = effective
    // The shared packages key their dark styles on a "dark" class, and the
    // browser draws native controls (select arrows, scrollbars, the code
    // editor) from color-scheme; without both they stay light in dark mode.
    document.documentElement.classList.toggle('dark', effective === 'dark')
    document.documentElement.style.colorScheme = effective
    // An installed app's title bar takes this color: the classification
    // banner's when one is drawn, so the bar and the banner read as one
    // strip, otherwise the page ground. Remembered so the next launch
    // paints it before the configuration arrives.
    requestAnimationFrame(() => {
      const meta = document.querySelector('meta[name="theme-color"]')
      const color = bannerShownColor(cfg) ?? getComputedStyle(document.documentElement).getPropertyValue('--pw-bg').trim()
      if (meta && color) meta.setAttribute('content', color)
      if (cfg.loaded && color) { try { localStorage.setItem('ade-theme-color', color) } catch { /* ignore */ } }
      // The splash of the next launch takes this ground (the manifest's
      // background_color). Not from a document whose theme its host chose.
      if (cfg.loaded && told !== 'dark' && told !== 'light') rememberGround(getComputedStyle(document.documentElement).getPropertyValue('--pw-bg'))
      // The banner too, so the next launch's loading screen draws it: in an
      // installed app it is the title bar, which otherwise shows the page's
      // ground between the window controls until the app arrives.
      if (cfg.loaded) {
        // Whether a banner is shown, once that is known: the Mac app's
        // window moves its controls to suit (desktop/wails/shell/shim.js).
        document.documentElement.dataset.banner = bannerShownColor(cfg) ? 'shown' : 'none'
        try {
          const shown = bannerShownColor(cfg)
          if (shown) localStorage.setItem('ade-banner', JSON.stringify({ text: cfg.bannerText.trim(), color: shown, ink: bannerInk(shown), whenEmbedded: !!cfg.bannerWhenEmbedded }))
          else localStorage.removeItem('ade-banner')
        } catch { /* ignore */ }
      }
    })
    // Cached so the next load paints in this theme before config arrives.
    if (cfg.loaded) { try { localStorage.setItem('ade-theme-default', cfg.theme) } catch { /* ignore */ } }
  }, [theme, cfg, sysDark])

  const toggleTheme = () => {
    const next = (theme ?? (sysDark ? 'dark' : cfg.theme)) === 'dark' ? 'light' : 'dark'
    setTheme(next)
    localStorage.setItem('ade-theme', next)
  }

  // Navigation state lives in the URL hash: #open=kind:target for an open
  // document, #view=<id> for a bare view, empty for the default chat view.
  // applyHash covers initial load, chat links, manual edits, and browser
  // back/forward (hash-only history moves fire hashchange).
  useEffect(() => {
    const applyHash = (e?: HashChangeEvent) => {
      // At the first load the link is already being followed.
      const item = e ? itemInAddress({ search: '', hash: location.hash }) : null
      if (item) { setLink({ id: item, state: 'opening' }); return }
      const wanted = parseOpenHash(location.hash)
      // Where it would put away an edit that is not saved, the page stays at
      // the address it had while that is asked, and goes once it is settled.
      const away = wanted ? !showsOpen(wanted) : /^#view=library(?:\?|:|$)/.test(location.hash)
      if (away && unsavedPath() !== null) {
        const to = location.hash
        const from = e?.oldURL ? new URL(e.oldURL).hash : ''
        if (from !== to) history.pushState(null, '', location.pathname + location.search + from)
        leaveUnsaved(() => { if (location.hash === to) applyHash(); else location.hash = to })
        return
      }
      const opened = inOpenLibrary(wanted)
      if (opened) {
        setDisplay(opened)
        setView('library')
        return
      }
      // A view may carry its own fields after a ?, as Search does its query.
      const vm = location.hash.match(/^#view=([a-z]+)(?::([a-z0-9-]+))?(?:\?.*)?$/)
      const v = vm?.[1]
      if (v && NAV.some(n => n.id === v)) {
        // #view=library is a request for the library itself. Leaving the
        // open document in state made the app rewrite the hash straight
        // back to #open=, so the page could never be reached.
        if (v === 'library') setDisplay(null)
        setView(v as ViewId)
        if (vm?.[2]) window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: v, section: vm[2] } }))
      } else if (!location.hash || location.hash.startsWith('#chat=')) setView('chat')
    }
    applyHash()
    // Chat links render with target=_blank; catch #open= clicks in the
    // capture phase and route them to the viewer in this tab instead.
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement).closest?.('a[href*="#open="], a[href*="#view="]') as HTMLAnchorElement | null
      const href = a?.getAttribute('href') ?? ''
      const linked = parseOpenHash(href.slice(href.indexOf('#open=')))
      if (linked) {
        e.preventDefault()
        e.stopPropagation()
        const go = () => { setDisplay(inOpenLibrary(linked)); setView('library') }
        if (showsOpen(linked)) go(); else leaveUnsaved(go)
        return
      }
      // A chat reply can hand the user a view link ("watch the agents
      // here"); routed in this tab, since a new tab of the whole app is
      // never what such a link means.
      const vm = href.match(/#view=[a-z]+(?::[a-z0-9-]+)?(?:\?[^#]*)?$/)
      if (vm) {
        e.preventDefault()
        e.stopPropagation()
        location.hash = vm[0]
      }
    }
    window.addEventListener('hashchange', applyHash)
    window.addEventListener('click', onClick, true)
    return () => {
      window.removeEventListener('hashchange', applyHash)
      window.removeEventListener('click', onClick, true)
    }
  }, [])

  // Push a history entry whenever the navigation state changes, so the
  // browser's back/forward buttons walk opens and view switches. After a
  // back/forward the hash already matches the restored state, so the
  // comparison makes this a no-op instead of a re-push (no feedback loop).
  // The first run only arms the ref: on a deep-linked load the state update
  // from applyHash has not landed yet, and writing here would clobber the
  // incoming hash. Slashes stay literal in the hash for readable URLs.
  const navReady = useRef(false)
  useEffect(() => {
    if (!navReady.current) { navReady.current = true; return }
    const hash = display && view === 'library'
      ? buildOpenHash(display)
      : view !== 'chat' ? `#view=${view}` : ''
    if ((location.hash || '') === hash) return
    // Views may own a :section suffix or ?fields on their hash; leave them theirs.
    if (hash && (location.hash.startsWith(`${hash}:`) || location.hash.startsWith(`${hash}?`))) return
    // The chat view keeps its open conversation in the hash; do not clear it.
    if (!hash && location.hash.startsWith('#chat=')) return
    history.pushState(null, '', location.pathname + location.search + hash)
    rememberHash(hash)
  }, [view, display])

  // A search result opens with the query it matched, so the viewer can
  // land on the match instead of the top of the file.
  const openFile = (path: string, q?: string) => {
    const lib = currentLibrary()
    const d: Display = { kind: 'file', target: path }
    if (q) d.q = q
    if (lib !== 'kb') d.lib = lib
    const go = () => { setDisplay(d); setView('library') }
    if (showsOpen(d)) go(); else leaveUnsaved(go)
  }

  // Embed mode: /?embed=dag&workflow=<name> or /?embed=model&path=<rel>
  // renders a single interactive viewer with no app chrome, for iframes
  // hosted inside chat replies (the ai-chat img override turns same-origin
  // /?embed= image sources into iframes).
  const embedParams = new URLSearchParams(location.search)
  const embedKind = embedParams.get('embed')
  if (embedKind === 'dag' && (embedParams.get('workflow') || embedParams.get('path'))) {
    return (
      <div className="embed-root">
        <DagViewer
          workflow={embedParams.get('workflow') ?? undefined}
          path={embedParams.get('path') ?? undefined}
        />
      </div>
    )
  }
  if (embedKind === 'model' && embedParams.get('path')) {
    return <div className="embed-root"><Suspense fallback={null}><ModelViewer path={embedParams.get('path')!} /></Suspense></div>
  }
  // A run popped out of the chat's side view into a window of its own.
  if (embedKind === 'run' && embedParams.get('slug')) {
    return <div className="embed-root run-embed"><RunPane slug={embedParams.get('slug')!} onClose={() => window.close()} /></div>
  }
  if (embedKind === 'session' && (embedParams.get('name') || embedParams.get('host') || embedParams.get('url') || embedParams.get('run'))) {
    let host = embedParams.get('host') ?? ''
    if (!host && embedParams.get('url')) { try { host = new URL(embedParams.get('url')!).host } catch { /* not an address */ } }
    return <div className="embed-root"><SessionView user={embedParams.get('user') ?? ''} name={embedParams.get('name') ?? ''} host={host} run={embedParams.get('run') ?? ''} /></div>
  }
  if (embedKind === 'html' && embedParams.get('path')) {
    // The inner frame is the sandbox; the served page also carries a CSP
    // sandbox header, so scripts run with no origin privileges and no
    // network access.
    return (
      <div className="embed-root">
        <LibraryPageFrame src={`/api/kb/html?path=${encodeURIComponent(embedParams.get('path')!)}`} />
      </div>
    )
  }

  // An embed this page has no view for, or any page of the Studio framed
  // by the Studio itself. Rendering the app there puts the whole Studio
  // inside a reply, and that copy opens the same conversation and frames
  // itself again, each copy polling the server, until the server stops
  // answering. The platform's page framing the Studio is another site and
  // is not affected.
  const framedBySelf = (() => { try { return window.parent !== window && window.parent.location.origin === window.location.origin } catch { return false } })()
  if (embedKind || framedBySelf) return <UnknownEmbed kind={embedKind ?? 'page'} />

  // A menu entry's mark: what there needs the person (notices.ts).
  const navMark = (id: ViewId): ReactElement | null => {
    if (id === 'transfers') return <TransferMark summary={xferSummary} />
    const of = id === 'code' ? codeWaiting : id === 'notices' ? notices.items.length : id === 'chat' || id === 'agents' ? countFor(notices, id) : 0
    if (!of) return null
    const waits = notices.items.some(i => i.what === 'waiting' && (id === 'notices' || i.nav.includes(id)))
    return <CountMark id={id} count={of} waiting={waits} said={id === 'code' || id === 'agents' ? `${of} waiting for you` : `${of} ${of === 1 ? 'needs' : 'need'} you`} />
  }

  return (
    <div className="app">
      <ClassificationBanner />
      <LinkNotice link={link} onClose={() => { dropItem(); setLink(null) }} onRetry={id => setLink({ id, state: 'opening' })} />
      <LinkToCopy />
      <NameTips />
      {/* The setup panel covers the app; the app behind it takes no focus or clicks. */}
      <div className="app-body" inert={setupOpen}>
      <nav className={`sidenav ${navCollapsed ? 'collapsed' : ''}`}>
        <div className="sidenav-head">
          <div className="sidenav-brand" title={cfg.loaded ? cfg.appName : undefined}>
            {/* Both halves come from the remembered brand on the first frame,
                so a returning visitor never sees the generic name swap to the
                deployment's. A first-ever visit has nothing to remember, and
                shows the slot empty rather than the wrong name. */}
            {/* With no icon uploaded, the Studio's own mark in the accent: the
                app icon itself for the default accent (studioMark.ts). */}
            <img className="brand-icon" src={brandIcon ?? studioMark(cfg.accent)} alt="" />
            {(cfg.loaded || cfg.brandRemembered) && <span className="brand-text"><b>{cfg.appName}</b></span>}
          </div>
          <IconButton
            className="nav-collapse"
            label={navCollapsed ? 'Expand navigation' : 'Collapse navigation'}
            onClick={() => setNavCollapsed(c => !c)}
            icon={<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.5"><rect x="1.75" y="2.25" width="12.5" height="11.5" rx="2"/><path d="M6 2.5v11"/></svg>}
          />
        </div>
        {allowed('search') && (
          <form className="sidenav-search" role="search" onSubmit={e => { e.preventDefault(); submitNavSearch() }}>
            <SearchInput ref={navSearchRef} className="sidenav-search-box" value={navQuery} onValueChange={setNavQuery}
              label="Search the knowledge base" placeholder="Search"
              onKeyDown={e => { if (e.key === 'Escape') { setNavQuery(''); e.currentTarget.blur() } }} />
            <kbd>{isMac ? '⌘K' : 'Ctrl K'}</kbd>
          </form>
        )}
        <div className="sidenav-items">
          {NAV_LAYOUT.flatMap(entry => {
            // One page button; inGroup indents it under its group header and
            // hides it while the group is closed (the collapsed rail and the
            // phone bar ignore groups and show every page).
            const pageButton = (id: ViewId, inGroup: string | null) => {
              const item = navItem(id)
              return (
                <button
                  key={item.id}
                  className={`sidenav-item ${view === item.id ? 'active' : ''}${MOBILE_PRIMARY.has(item.id) ? '' : ' mob-hide'}${inGroup ? ' in-group' : ''}`}
                  title={item.label}
                  onClick={() => {
                    if (item.id === 'library' && view === 'library') showDisplay(null)
                    // On a phone the conversations rail is a slide-over, and
                    // re-tapping Assistant in the bottom bar is its toggle.
                    if (item.id === 'chat' && view === 'chat') window.dispatchEvent(new Event('ade:toggle-chat-rail'))
                    // Workflows and Transfers always open on their main page,
                    // not on the form that was last open there.
                    goHome(item.id)
                    setView(item.id)
                  }}
                >
                  {item.icon}
                  <span>{item.label}</span>
                  {navMark(item.id)}
                </button>
              )
            }
            if ('id' in entry) return allowed(entry.id) ? [pageButton(entry.id, null)] : []
            const pages = entry.items.filter(allowed)
            if (pages.length <= 1) return pages.map(id => pageButton(id, null))
            const open = !closedGroups.has(entry.group)
            // The header is a label, as in the platform's menu; the pages
            // slide open beneath it. On a phone and in the icon rail the
            // wrappers drop out of the layout and every page shows.
            return [
              <div key={`group-${entry.group}`} className={`sidenav-group${open ? ' open' : ''}`}>
                <button
                  className={`sidenav-group-head${!open && pages.includes(view) ? ' active' : ''}`}
                  aria-expanded={open}
                  title={open ? `Hide ${entry.label}` : `Show ${entry.label}`}
                  onClick={() => toggleGroup(entry.group)}
                >
                  <span>{entry.label}</span>
                  <svg className="sidenav-chev" viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.6"><path d="m6 4 4 4-4 4"/></svg>
                </button>
                <div className="sidenav-group-body"><div className="sidenav-group-inner">
                  {pages.map(id => pageButton(id, entry.group))}
                </div></div>
              </div>,
            ]
          })}
          {/* In the phone's bar; the sidebar has it at its foot. */}
          <button className={`sidenav-item nav-notices-phone ${view === 'notices' ? 'active' : ''}`} title="Notices" onClick={() => setView('notices')}>
            {navItem('notices').icon}
            <span>Notices</span>
            {navMark('notices')}
          </button>
          <button
            className={`sidenav-item nav-more ${moreOpen || !MOBILE_PRIMARY.has(view) ? 'active' : ''}`}
            title="More"
            onClick={() => setMoreOpen(o => !o)}
          >
            <svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor"><circle cx="3" cy="8" r="1.5" /><circle cx="8" cy="8" r="1.5" /><circle cx="13" cy="8" r="1.5" /></svg>
            <span>More</span>
          </button>
        </div>
        {moreOpen && (
          <>
            <div className="scope-overlay" onClick={() => setMoreOpen(false)} />
            <div className="nav-more-sheet card">
              {NAV.filter(i => !MOBILE_PRIMARY.has(i.id) && allowed(i.id)).map(item => (
                <button
                  key={item.id}
                  className={`sidenav-item ${view === item.id ? 'active' : ''}`}
                  onClick={() => { goHome(item.id); setView(item.id); setMoreOpen(false) }}
                >
                  {item.icon}
                  <span>{item.label}</span>
                  {navMark(item.id)}
                </button>
              ))}
              <StatusFooter />
            </div>
          </>
        )}
        <div className="sidenav-items sidenav-secondary">
          {NAV.filter(i => i.id === 'notices' || i.id === 'settings' || i.id === 'help').map(item => (
            <button
              key={item.id}
              className={`sidenav-item ${view === item.id ? 'active' : ''}`}
              title={item.label}
              onClick={() => setView(item.id)}
            >
              {item.icon}
              <span>{item.label}</span>
              {navMark(item.id)}
            </button>
          ))}
          {canInstall && (
            <button className="sidenav-item" title={`Install ${cfg.appName} as an app`} onClick={() => void install()}>
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M8 2v8M4.8 7 8 10.2 11.2 7"/><path d="M2.5 11v1.5a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1V11"/></svg>
              <span>Install app</span>
            </button>
          )}
          <button
            className="sidenav-item"
            title={effectiveTheme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            onClick={toggleTheme}
          >
            {effectiveTheme === 'dark'
              ? <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="3.2"/><path d="M8 1.2v1.8M8 13v1.8M1.2 8H3M13 8h1.8M3.2 3.2l1.3 1.3M11.5 11.5l1.3 1.3M12.8 3.2l-1.3 1.3M4.5 11.5l-1.3 1.3"/></svg>
              : <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M13.5 9.5A6 6 0 0 1 6.5 2.5a6 6 0 1 0 7 7z"/></svg>}
            <span>{effectiveTheme === 'dark' ? 'Light mode' : 'Dark mode'}</span>
          </button>
        </div>
        <StatusFooter collapsed={navCollapsed} />
      </nav>
      {!navCollapsed && <NavResize width={cfg.navWidth} />}
      <main className="content">
        {view in PAGE_NOTES && (
          <header className="page-head">
            <h1>{NAV.find(n => n.id === view)?.label}</h1>
            {pageNote && <span className="page-note" title={pageNote}>{pageNote}</span>}
            <PageHeadSlot />
            <PanelToggle className="page-head-toggle" />
          </header>
        )}
        {/* Assistant puts the status panel's toggle beside its own controls; the
            library, settings, and help have no header, so it floats at
            their top right. */}
        {/* With AI features off, Assistant and Agents are not mounted at all, so
            nothing of theirs polls the server or reads a model. */}
        {aiPagesMounted(cfg) && <div className={`view view-chat${view === 'chat' ? '' : ' hidden'}`}><ChatView active={view === 'chat'} /></div>}
        <div className={view === 'library' ? 'view with-toggle' : 'view hidden'}>
          <LibraryView display={display} onDisplay={showDisplay} reveal={reveal} replaced={replaced} onReplacedRead={() => setReplaced(null)} />
          {view === 'library' && <PanelToggle className="panel-toggle-float" />}
        </div>
        {view === 'transfers' && hasTransfers && <div className="view"><TransfersView /></div>}
        <div className={view === 'search' ? 'view' : 'view hidden'}><SearchView onOpen={openFile} /></div>
        <div className={view === 'query' ? 'view' : 'view hidden'}><QueryView onOpen={openFile} /></div>
        <div className={view === 'overview' ? 'view' : 'view hidden'}><OverviewView active={view === 'overview'} onOpen={openFile} /></div>
        <div className={view === 'history' ? 'view' : 'view hidden'}><HistoryView onOpen={openFile} /></div>
        {aiPagesMounted(cfg) && <div className={view === 'agents' ? 'view' : 'view hidden'}><AgentsView active={view === 'agents'} onOpen={openFile} /></div>}
        {/* Without ACTIVATE, where Accounts can sign in to it, these ask to sign in. */}
        {allowed('workflows') && view === 'workflows' && <div className="view"><WorkflowsView /></div>}
        {allowed('code') && view === 'code' && <div className="view"><CodePage /></div>}
        {view === 'notices' && <div className="view"><NoticesView /></div>}
        <div className={view === 'settings' ? 'view with-toggle' : 'view hidden'}>
          <SettingsView active={view === 'settings'} />
          {view === 'settings' && <PanelToggle className="panel-toggle-float" />}
        </div>
        <div className={view === 'help' ? 'view with-toggle' : 'view hidden'}>
          <HelpView />
          {view === 'help' && <PanelToggle className="panel-toggle-float" />}
        </div>
        <FirstVisitCard />
      </main>
      <StatusPanel view={view} cfg={cfg} />
      <FullViewHost />
      </div>
      {setupOpen && <SetupWizard onClose={changed => { setSetupOpen(false); if (changed) location.reload() }} />}
    </div>
  )
}

/** How often the Transfers entry's summary is read. */
const XFER_EVERY_MS = 15_000

/** A menu entry's count of what needs the person: in the warning color when
 *  something there waits on them, else in blue (a reply to read). */
function CountMark({ id, count, waiting, said }: { id: string; count: number; waiting: boolean; said: string }) {
  return (
    <span className="nav-xfer-mark" title={said} data-testid={`nav-${id}-waiting`}>
      <CountBadge count={count} max={99} size="sm" variant={waiting ? 'warning' : 'info'} />
      <span className="sr-only">{said}</span>
    </span>
  )
}

/** The Transfers entry's mark: the count under way, or a warning dot when
 *  one failed in the last day and has not been seen, with the same said
 *  for a screen reader. */
function TransferMark({ summary }: { summary: TransferSummary | null }) {
  if (!summary) return null
  const said = [
    summary.active ? `${summary.active} under way` : '',
    summary.awaitingApproval ? `${summary.awaitingApproval} awaiting approval` : '',
    summary.failedLast24h ? `${summary.failedLast24h} failed in the last day` : '',
  ].filter(Boolean).join(', ')
  if (!said) return null
  return (
    <span className="nav-xfer-mark" title={said}>
      {summary.active > 0 && <CountBadge count={summary.active} max={99} size="sm" variant={summary.awaitingApproval ? 'warning' : 'accent'} />}
      {summary.failedLast24h > 0 && <StatusDot variant="error" />}
      <span className="sr-only">{said}</span>
    </span>
  )
}

/** A one-line note in place of an embed this Studio does not know, sized
 *  for the frame that holds it. */
function UnknownEmbed({ kind }: { kind: string }) {
  useEffect(() => {
    if (window.parent !== window) window.parent.postMessage({ type: 'ade-embed-height', height: 44 }, window.location.origin)
  }, [])
  return <div className="embed-root embed-unknown"><p className="muted">This reply asked for a "{kind}" view, which the Studio does not have.</p></div>
}
