import { useEffect, useState } from 'react'

export interface AppConfig {
  appName: string
  kbLabel: string
  suggestedPrompts: string[]
  user: { id: string; username: string; name?: string }
  loaded: boolean
}

const DEFAULTS: AppConfig = {
  appName: 'Studio',
  kbLabel: 'Knowledge base',
  suggestedPrompts: [
    'What is in this knowledge base?',
    'Summarize the most recently changed documents.',
    'What workflows are available in this account?',
    'Find everything related to a topic I name.',
  ],
  user: { id: 'user', username: 'user' },
  loaded: false,
}

let cached: AppConfig | null = null

export function useAppConfig(): AppConfig {
  const [cfg, setCfg] = useState<AppConfig>(cached ?? DEFAULTS)
  useEffect(() => {
    if (cached) return
    fetch('/api/config')
      .then(r => r.json())
      .then((d: Partial<AppConfig>) => {
        cached = {
          appName: d.appName || DEFAULTS.appName,
          kbLabel: d.kbLabel || DEFAULTS.kbLabel,
          suggestedPrompts: d.suggestedPrompts?.length ? d.suggestedPrompts : DEFAULTS.suggestedPrompts,
          user: d.user?.id ? (d.user as AppConfig['user']) : DEFAULTS.user,
          loaded: true,
        }
        setCfg(cached)
      })
      .catch(() => setCfg({ ...DEFAULTS, loaded: true }))
  }, [])
  return cfg
}
