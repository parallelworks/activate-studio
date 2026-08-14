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
  sweepIntervalSec?: number
  suggestedPrompts?: string[]
  visionModel?: string
}

let cache: StudioSettings | null = null

export function loadSettings(): StudioSettings {
  if (cache) return cache
  try { cache = JSON.parse(fs.readFileSync(FILE, 'utf8')) } catch { cache = {} }
  return cache!
}

function envPrompts(): string[] {
  try { return JSON.parse(process.env.SUGGESTED_PROMPTS ?? '[]') } catch { return [] }
}

/** Effective values: saved settings override environment defaults. */
export function effectiveSettings(): Required<StudioSettings> {
  const s = loadSettings()
  return {
    appName: s.appName ?? process.env.APP_NAME ?? 'Studio',
    kbLabel: s.kbLabel ?? process.env.KB_LABEL ?? path.basename(KB_ROOT),
    theme: s.theme ?? (process.env.THEME === 'dark' ? 'dark' : 'light'),
    sweepIntervalSec: s.sweepIntervalSec ?? Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
    suggestedPrompts: s.suggestedPrompts ?? envPrompts(),
    visionModel: s.visionModel ?? process.env.ADE_VISION_MODEL ?? '',
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
    await fsp.mkdir(INDEX_BASE, { recursive: true })
    await fsp.writeFile(FILE, JSON.stringify(next, null, 1))
    cache = next
    return { effective: effectiveSettings(), saved: next }
  })
}
