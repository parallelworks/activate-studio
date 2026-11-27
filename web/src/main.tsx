// First: the install event can fire before React renders.
import './install'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { UIProvider } from '@parallelworks/ui'
import { useRunFile } from './runFile'
import App from './App'
import { rememberHashChanges, restoreLastHash } from './lastLocation'
import { applyRememberedFavicon, applyRememberedNavWidth } from './config'
import { fitToVisualViewport, keepShellInPlace, settleAfterKeyboard } from './shellScroll'
import { installClipboardFallback } from './clipboard'
import { installDownloadNotices } from './downloadNotice'
import { guardDrops } from './dropGuard'
import { registerShellWorker } from './shellWorker'
// Geist Sans and Geist Mono, the platform's typefaces, from the shared package.
import '@parallelworks/ui/fonts.css'
// The UI package's prebuilt stylesheet (chat rules included) loads before
// ours, so ours wins at equal specificity.
import './layers.css'
import '@parallelworks/ui/styles.css'
import '@parallelworks/ui/theme.css'
import '@parallelworks/ui/logviewer.css'
import './styles.css'

keepShellInPlace()
settleAfterKeyboard()
// The app keeps to what is visible above an on-screen keyboard.
fitToVisualViewport()
installClipboardFallback()
// A notice for each file the page saves (an exported conversation, a CSV).
installDownloadNotices()
// The app shell on the device, so the next open starts without waiting on
// the network (web/shell/sw.js).
registerShellWorker()

// Before the first render: components read the hash while initializing.
restoreLastHash()
rememberHashChanges()
applyRememberedFavicon()
applyRememberedNavWidth()

// The workflow parser (and its WebAssembly) loads only once a workflow form
// or graph first renders.
const loadWorkflowEngine = () =>
  import('@parallelworks/workflow-parser').then(m => m.createWorkflowEngine())

guardDrops()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <UIProvider engine={loadWorkflowEngine} data={{ useRunFile }}>
      <App />
    </UIProvider>
  </React.StrictMode>,
)
