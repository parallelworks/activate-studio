import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT } from './config.js'
import { KbError } from './kb.js'

/**
 * Runtime-editable settings, stored beside the index. Environment variables
 * provide the defaults; anything saved here overrides them immediately, so
 * deployments can be reconfigured from the Settings page without a restart.
 */

const FILE = path.join(INDEX_BASE, 'settings.json')

export interface StudioSettings {
  appName?: string
  kbLabel?: string
  theme?: 'light' | 'dark'
  accent?: string
  surface?: string
  sweepIntervalSec?: number
  suggestedPrompts?: string[]
  visionModel?: string
  disabledTools?: string[]
  customTools?: { name: string; description: string; command: string }[]
  ragDefaultModel?: string
  ragTopK?: number
  ragAllowDeploymentKey?: boolean
  ragEndpointAutoStart?: boolean
  ragEndpointName?: string
}

let cache: StudioSettings | null = null

export function loadSettings(): StudioSettings {
  if (cache) return cache
  try { cache = JSON.parse(fs.readFileSync(FILE, 'utf8')) } catch { cache = {} }
  return cache!
}

function envPrompts(): string[] {
  const fallback = [
    'What tools can you use and what can I ask you to do?',
    'What is in this knowledge base?',
    'Which workflows are available to run?',
  ]
  try {
    const fromEnv = JSON.parse(process.env.SUGGESTED_PROMPTS ?? '[]')
    return Array.isArray(fromEnv) && fromEnv.length ? fromEnv : fallback
  } catch { return fallback }
}

/** Effective values: saved settings override environment defaults. */
export function effectiveSettings(): Required<StudioSettings> {
  const s = loadSettings()
  return {
    appName: s.appName ?? process.env.APP_NAME ?? 'Studio',
    kbLabel: s.kbLabel ?? process.env.KB_LABEL ?? path.basename(KB_ROOT),
    theme: s.theme ?? (process.env.THEME === 'dark' ? 'dark' : 'light'),
    accent: s.accent ?? process.env.APP_ACCENT ?? 'navy',
    surface: s.surface ?? process.env.APP_SURFACE ?? 'cool',
    sweepIntervalSec: s.sweepIntervalSec ?? Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
    suggestedPrompts: s.suggestedPrompts ?? envPrompts(),
    visionModel: s.visionModel ?? process.env.ADE_VISION_MODEL ?? '',
    disabledTools: s.disabledTools ?? [],
    customTools: s.customTools ?? [],
    ragDefaultModel: s.ragDefaultModel ?? process.env.RAG_DEFAULT_MODEL ?? '',
    ragTopK: s.ragTopK ?? Math.min(Math.max(Number(process.env.RAG_TOP_K) || 6, 1), 20),
    ragAllowDeploymentKey: s.ragAllowDeploymentKey ?? process.env.RAG_PROXY_ALLOW_DEPLOYMENT_KEY === '1',
    ragEndpointAutoStart: s.ragEndpointAutoStart ?? process.env.RAG_ENDPOINT_AUTOSTART === '1',
    ragEndpointName: s.ragEndpointName ?? process.env.RAG_ENDPOINT_NAME ?? '',
  }
}

export async function settingsRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/settings', async () => ({
    effective: effectiveSettings(),
    saved: loadSettings(),
  }))

  app.put('/api/settings', async req => {
    const body = req.body as StudioSettings
    const next: StudioSettings = { ...loadSettings() }
    if (body.appName !== undefined) next.appName = String(body.appName).slice(0, 60) || undefined
    if (body.kbLabel !== undefined) next.kbLabel = String(body.kbLabel).slice(0, 60) || undefined
    if (body.theme !== undefined) next.theme = body.theme === 'dark' ? 'dark' : 'light'
    if (body.accent !== undefined) {
      const a = String(body.accent)
      if (!/^([a-z][a-z0-9-]{0,23}|custom:#[0-9a-fA-F]{6})$/.test(a)) throw new KbError(400, 'invalid accent')
      next.accent = a
    }
    if (body.surface !== undefined) {
      const a = String(body.surface)
      if (!/^[a-z][a-z0-9-]{0,23}$/.test(a)) throw new KbError(400, 'invalid surface name')
      next.surface = a
    }
    if (body.sweepIntervalSec !== undefined) {
      const n = Number(body.sweepIntervalSec)
      if (!Number.isFinite(n) || n < 0 || n > 86400) throw new KbError(400, 'sweep interval must be 0 to 86400 seconds')
      next.sweepIntervalSec = n
    }
    if (body.suggestedPrompts !== undefined) {
      if (!Array.isArray(body.suggestedPrompts)) throw new KbError(400, 'suggestedPrompts must be an array')
      next.suggestedPrompts = body.suggestedPrompts.map(p => String(p).slice(0, 300)).filter(Boolean).slice(0, 12)
    }
    if (body.visionModel !== undefined) next.visionModel = String(body.visionModel).slice(0, 120) || undefined
    if (body.disabledTools !== undefined) {
      if (!Array.isArray(body.disabledTools)) throw new KbError(400, 'disabledTools must be an array')
      next.disabledTools = body.disabledTools.map(t => String(t).slice(0, 60)).filter(Boolean).slice(0, 50)
    }
    if (body.customTools !== undefined) {
      if (!Array.isArray(body.customTools)) throw new KbError(400, 'customTools must be an array')
      next.customTools = body.customTools.slice(0, 12).map(t => ({
        name: String(t?.name ?? '').toLowerCase().replace(/[^a-z0-9_]/g, '_').replace(/^_+|_+$/g, '').slice(0, 40),
        description: String(t?.description ?? '').slice(0, 300),
        command: String(t?.command ?? '').slice(0, 500),
      })).filter(t => t.name && t.command)
    }
    if (body.ragDefaultModel !== undefined) next.ragDefaultModel = String(body.ragDefaultModel).slice(0, 120) || undefined
    if (body.ragTopK !== undefined) {
      const n = Number(body.ragTopK)
      if (!Number.isFinite(n) || n < 1 || n > 20) throw new KbError(400, 'ragTopK must be 1 to 20')
      next.ragTopK = n
    }
    if (body.ragAllowDeploymentKey !== undefined) next.ragAllowDeploymentKey = Boolean(body.ragAllowDeploymentKey)
    if (body.ragEndpointAutoStart !== undefined) next.ragEndpointAutoStart = Boolean(body.ragEndpointAutoStart)
    if (body.ragEndpointName !== undefined) {
      const n = String(body.ragEndpointName).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40)
      next.ragEndpointName = n || undefined
    }
    await fsp.mkdir(INDEX_BASE, { recursive: true })
    await fsp.writeFile(FILE, JSON.stringify(next, null, 1))
    cache = next
    return { effective: effectiveSettings(), saved: next }
  })
}
