import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT } from './config.js'
import { KbError } from './kb.js'
import { gatewayConfigured } from './chat/gateway.js'

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
  iconUrl?: string
  iconUrlDark?: string
  sweepIntervalSec?: number
  suggestedPrompts?: string[]
  visionModel?: string
  disabledTools?: string[]
  customTools?: { name: string; description: string; command: string }[]
  ragDefaultModel?: string
  ragTopK?: number
  ragAllowDeploymentKey?: boolean
  ragAdvertiseRagModel?: boolean
  ragAdvertiseAgentModel?: boolean
  chatSharedHistory?: boolean
  ragEndpointAutoStart?: boolean
  ragEndpointName?: string
  requirePersonalKey?: boolean
  bannerText?: string
  bannerColor?: string
  bannerWhenEmbedded?: boolean
  allowKeySharing?: boolean
  serveWorkflows?: string
  hpcStatusUrl?: string
  hpcStatusWorkflow?: string
  chatTemplateKwargs?: string
  chatModelWindows?: string
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
    surface: s.surface ?? process.env.APP_SURFACE ?? 'neutral',
    iconUrl: s.iconUrl ?? process.env.APP_ICON ?? '',
    iconUrlDark: s.iconUrlDark ?? process.env.APP_ICON_DARK ?? '',
    sweepIntervalSec: s.sweepIntervalSec ?? Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
    suggestedPrompts: s.suggestedPrompts ?? envPrompts(),
    visionModel: s.visionModel ?? process.env.ADE_VISION_MODEL ?? '',
    disabledTools: s.disabledTools ?? [],
    customTools: s.customTools ?? [],
    ragDefaultModel: s.ragDefaultModel ?? process.env.RAG_DEFAULT_MODEL ?? '',
    ragTopK: s.ragTopK ?? Math.min(Math.max(Number(process.env.RAG_TOP_K) || 6, 1), 20),
    ragAllowDeploymentKey: s.ragAllowDeploymentKey ?? process.env.RAG_PROXY_ALLOW_DEPLOYMENT_KEY === '1',
    ragAdvertiseRagModel: s.ragAdvertiseRagModel ?? process.env.RAG_ADVERTISE_RAG_MODEL === '1',
    ragAdvertiseAgentModel: s.ragAdvertiseAgentModel ?? process.env.RAG_ADVERTISE_AGENT_MODEL !== '0',
    chatSharedHistory: s.chatSharedHistory ?? process.env.CHAT_SHARED_HISTORY !== '0',
    ragEndpointAutoStart: s.ragEndpointAutoStart ?? process.env.RAG_ENDPOINT_AUTOSTART === '1',
    ragEndpointName: s.ragEndpointName ?? process.env.RAG_ENDPOINT_NAME ?? '',
    requirePersonalKey: s.requirePersonalKey ?? process.env.REQUIRE_PERSONAL_KEY === '1',
    // Classification banner. The platform already shows one around an
    // embedded session, so by default the app draws its own only when it is
    // open on its own (a new tab or window), never twice.
    bannerText: s.bannerText ?? process.env.BANNER_TEXT ?? '',
    bannerColor: s.bannerColor ?? process.env.BANNER_COLOR ?? '#24612e',
    bannerWhenEmbedded: s.bannerWhenEmbedded ?? process.env.BANNER_WHEN_EMBEDDED === '1',
    // Whether a user may promote their own key to stand in for the
    // deployment credential. On by default: the deployments that need it
    // are the ones with no credential of their own.
    allowKeySharing: s.allowKeySharing ?? process.env.ALLOW_KEY_SHARING !== '0',
    // Model-serving workflows the serve_model chat tool can launch, as JSON:
    // {"<engine>": {"workflow": "<name>", "inputs": {...preset...}}}
    // Base URL of an HPC Status Monitor deployment (github.com/parallelworks/hpc_status);
    // empty disables the hpc_status tool.
    hpcStatusUrl: s.hpcStatusUrl ?? process.env.HPC_STATUS_URL ?? '',
    // Workflow that deploys the Status Monitor, so the assistant can stand
    // one up when none is running (hpcmp_status on the HPCMP platform,
    // hpc_status generally).
    hpcStatusWorkflow: s.hpcStatusWorkflow ?? process.env.HPC_STATUS_WORKFLOW ?? 'hpc_status',
    // Per-model chat template kwargs, JSON of {modelSubstring: kwargs}.
    // Default disables Qwen thinking: the gateway strips reasoning content
    // from session-model streams, so thinking is invisible dead time and a
    // turn can end with its answer trapped in the stripped channel.
    chatTemplateKwargs: s.chatTemplateKwargs ?? process.env.CHAT_TEMPLATE_KWARGS ?? JSON.stringify({ qwen: { enable_thinking: false } }),
    // Context windows for models that have small ones, JSON of
    // {modelSubstring: tokens}. The tool loop trims old tool results to
    // stay inside; without this a long loop overflows the window and the
    // serve answers 400, which the gateway forwards as an empty stream.
    chatModelWindows: s.chatModelWindows ?? process.env.CHAT_MODEL_WINDOWS ?? JSON.stringify({ qwen: 16384 }),
    serveWorkflows: s.serveWorkflows ?? process.env.SERVE_WORKFLOWS ?? JSON.stringify({
      vllm: { workflow: 'vllm-endpoint', inputs: {} },
      ollama: { workflow: 'ollama-endpoint', inputs: {} },
    }),
  }
}

export async function settingsRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/settings', async () => ({
    effective: effectiveSettings(),
    saved: loadSettings(),
    // Background work (image captioning during a sweep) runs with no user
    // present, so it can only use the deployment credential or the host's
    // pw CLI login. Report whether one exists rather than letting
    // captioning fail quietly on a bring-your-own-key deployment.
    deploymentCredential: gatewayConfigured(),
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
    for (const field of ['iconUrl', 'iconUrlDark'] as const) {
      if (body[field] === undefined) continue
      const v = String(body[field]).trim()
      if (v && !/^https?:\/\//i.test(v) && !v.startsWith('/')) throw new KbError(400, 'icon must be an http(s) URL or an absolute path')
      next[field] = v
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
    if (body.ragAdvertiseRagModel !== undefined) next.ragAdvertiseRagModel = Boolean(body.ragAdvertiseRagModel)
    if (body.ragAdvertiseAgentModel !== undefined) next.ragAdvertiseAgentModel = Boolean(body.ragAdvertiseAgentModel)
    if (body.chatSharedHistory !== undefined) next.chatSharedHistory = Boolean(body.chatSharedHistory)
    if (body.ragEndpointAutoStart !== undefined) next.ragEndpointAutoStart = Boolean(body.ragEndpointAutoStart)
    if (body.requirePersonalKey !== undefined) next.requirePersonalKey = Boolean(body.requirePersonalKey)
    if (body.bannerText !== undefined) next.bannerText = String(body.bannerText).slice(0, 200)
    if (body.bannerColor !== undefined) {
      const c = String(body.bannerColor).trim()
      if (c && !/^#[0-9a-fA-F]{6}$/.test(c)) throw new KbError(400, 'banner color must be a #rrggbb value')
      next.bannerColor = c || undefined
    }
    if (body.bannerWhenEmbedded !== undefined) next.bannerWhenEmbedded = Boolean(body.bannerWhenEmbedded)
    if (body.allowKeySharing !== undefined) next.allowKeySharing = Boolean(body.allowKeySharing)
    if (body.hpcStatusUrl !== undefined) {
      const v = String(body.hpcStatusUrl).trim().replace(/\/$/, '')
      if (v && !/^https?:\/\//i.test(v)) throw new KbError(400, 'hpcStatusUrl must be http(s)')
      next.hpcStatusUrl = v || undefined
    }
    if (body.hpcStatusWorkflow !== undefined) next.hpcStatusWorkflow = String(body.hpcStatusWorkflow).slice(0, 80) || undefined
    if (body.chatModelWindows !== undefined) {
      const raw = String(body.chatModelWindows).slice(0, 1000)
      try { JSON.parse(raw) } catch { throw new KbError(400, 'chatModelWindows must be JSON') }
      next.chatModelWindows = raw
    }
    if (body.chatTemplateKwargs !== undefined) {
      const raw = String(body.chatTemplateKwargs).slice(0, 2000)
      try { JSON.parse(raw) } catch { throw new KbError(400, 'chatTemplateKwargs must be JSON') }
      next.chatTemplateKwargs = raw
    }
    if (body.serveWorkflows !== undefined) {
      const raw = String(body.serveWorkflows).slice(0, 4000)
      try { JSON.parse(raw) } catch { throw new KbError(400, 'serveWorkflows must be JSON') }
      next.serveWorkflows = raw
    }
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
