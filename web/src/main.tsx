// First: the install event can fire before React renders.
import './install'
import React from 'react'
import ReactDOM from 'react-dom/client'
import { UIProvider } from '@parallelworks/ui'
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
import './styles.css'

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
    <UIProvider engine={loadWorkflowEngine}>
      <App />
    </UIProvider>
  </React.StrictMode>,
)
