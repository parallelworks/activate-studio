import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { rememberHashChanges, restoreLastHash } from './lastLocation'
import { applyRememberedFavicon } from './config'
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
// Two packages ship their own Tailwind build, and their utilities collide at
// equal specificity, so whichever loads last wins. The chat package must come
// second: its responsive variants (a two-column starter grid, for one) were
// being overridden by the UI package's base utilities. Ours loads last of all.
import '@parallelworks/ui/styles.css'
import '@parallelworks/ui/theme.css'
import '@parallelworks/ai-chat/styles.css'
import './styles.css'

// Before the first render: components read the hash while initializing.
restoreLastHash()
rememberHashChanges()
applyRememberedFavicon()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
