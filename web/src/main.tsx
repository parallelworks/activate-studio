// First: the install event can fire before React renders.
import './install'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { UIProvider } from '@parallelworks/ui'
import { useRunFile } from './runFile'
import App from './App'
import { rememberHashChanges, restoreLastHash } from './lastLocation'
import { applyRememberedFavicon } from './config'
// Geist Sans and Geist Mono, the platform's typefaces, from the shared package.
import '@parallelworks/ui/fonts.css'
// The UI package's prebuilt stylesheet (chat rules included) loads before
// ours, so ours wins at equal specificity.
import './layers.css'
import '@parallelworks/ui/styles.css'
import '@parallelworks/ui/theme.css'
import '@parallelworks/ui/logviewer.css'
import './styles.css'

// The shell never scrolls; each pane scrolls itself. A scrollIntoView or
// focus anywhere (a frame, the chat package) can still scroll the window
// and boxes that only hide their overflow, which slid the Studio up and
// sideways out of the user's reach. Whatever a script scrolls there is put
// back at once.
const SHELL = '.app, .app-body, main.content, .chat-canvas, .chat-wrap, .view'
document.addEventListener('scroll', e => {
  const t = e.target === document ? document.scrollingElement : e.target
  if (!(t instanceof Element)) return
  if (t === document.scrollingElement || t === document.body || t.id === 'root' || t.matches(SHELL)) {
    if (t.scrollTop || t.scrollLeft) { t.scrollTop = 0; t.scrollLeft = 0 }
  } else if (t.matches('.sidenav') && t.scrollLeft) {
    t.scrollLeft = 0
  }
}, { capture: true, passive: true })

// Before the first render: components read the hash while initializing.
restoreLastHash()
rememberHashChanges()
applyRememberedFavicon()

// The workflow parser (and its WebAssembly) loads only once a workflow form
// or graph first renders.
const loadWorkflowEngine = () =>
  import('@parallelworks/workflow-parser').then(m => m.createWorkflowEngine())

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <UIProvider engine={loadWorkflowEngine} data={{ useRunFile }}>
      <App />
    </UIProvider>
  </React.StrictMode>,
)
