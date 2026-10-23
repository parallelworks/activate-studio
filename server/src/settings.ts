import fs from 'node:fs'
import { parseWorkflowEntry } from './workflowEntries.js'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT } from './config.js'
import { authEnabled } from './auth.js'
import { KbError } from './kb.js'
import { gatewayConfigured } from './chat/gateway.js'
import { cachedRemoteStatus, invalidateRemote, remoteStatus } from './mcpClient.js'

/**
 * Runtime-editable settings, stored beside the index. Environment variables
 * provide the defaults; anything saved here overrides them immediately, so
 * deployments can be reconfigured from the Settings page without a restart.
 */

const FILE = path.join(INDEX_BASE, 'settings.json')

export type AgentExecution = 'runner' | 'sessions' | 'both'
const AGENT_EXECUTIONS: AgentExecution[] = ['runner', 'sessions', 'both']
const asExecution = (v: unknown): AgentExecution | undefined =>
  AGENT_EXECUTIONS.includes(v as AgentExecution) ? v as AgentExecution : undefined

/** Whether the Studio looks for new releases, and whether the macOS app
 *  installs them on its own (updates.ts). */
export interface UpdatePolicy {
  check: boolean
  install: 'manual' | 'auto'
  channel: 'stable' | 'prerelease'
}
const UPDATE_DEFAULTS: UpdatePolicy = { check: true, install: 'manual', channel: 'stable' }
const asPolicy = (v: Partial<UpdatePolicy> | undefined): UpdatePolicy => ({
  check: typeof v?.check === 'boolean' ? v.check : UPDATE_DEFAULTS.check,
  install: v?.install === 'auto' ? 'auto' : 'manual',
  channel: v?.channel === 'prerelease' ? 'prerelease' : 'stable',
})

export interface StudioSettings {
  appName?: string
  kbLabel?: string
  /** Which sections the app shows; unset means all of them. */
  sections?: string[]
  /** The workflows this Studio offers on its Workflows tab, by record name. */
  workflowCollection?: string[]
  theme?: 'light' | 'dark'
  accent?: string
  surface?: string
  iconUrl?: string
  iconUrlDark?: string
  sweepIntervalSec?: number
  /** How a search across libraries runs: each library in parallel, merged
   *  here (the default), or as one gufi_vt statement. */
  searchEngine?: 'parallel' | 'gufi_vt'
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
  kbMission?: string
  bannerColor?: string
  bannerWhenEmbedded?: boolean
  allowKeySharing?: boolean
  serveWorkflows?: string
  hpcStatusUrl?: string
  hpcStatusWorkflow?: string
  chatTemplateKwargs?: string
  chatModelWindows?: string
  mcpEnabled?: boolean
  ragProxyEnabled?: boolean
  historyIntervalSec?: number
  mcpServers?: { name: string; url: string; header?: string }[]
  delegationEnabled?: boolean
  delegationMaxAgents?: number
  /** Model rounds one chat reply may spend calling tools before it answers. */
  maxToolRounds?: number
  delegationMaxDepth?: number
  /** How delegated agents run: one-shot pw code runs (runner), sessions
   *  in the pw code daemon (sessions), or either, chosen per task (both). */
  agentExecution?: AgentExecution
  /** Feature preview: voice conversations through an Unmute deployment. */
  voiceEnabled?: boolean
  voiceUrl?: string
  /** The first-run setup was finished or skipped (single-user installs). */
  setupDone?: boolean
  /** Chat, agents and every other model call. Off leaves search, the
   *  library, query and statistics as they are. */
  aiEnabled?: boolean
  updates?: UpdatePolicy
}

let cache: StudioSettings | null = null

export function loadSettings(): StudioSettings {
  if (cache) return cache
  try { cache = JSON.parse(fs.readFileSync(FILE, 'utf8')) } catch { cache = {} }
  return cache!
}

/** A Studio on one person's own computer: nobody signs in, and the platform
 *  did not deploy it. Its default prompts and mission are about that
 *  person's own files rather than a shared deployment. */
export function personalInstall(): boolean {
  return !authEnabled() && !process.env.PW_PLATFORM_HOST
}

export const PERSONAL_PROMPTS = [
  'What is in my folder, and how is it organized?',
  'Which files changed most recently, and what changed in them?',
  'Summarize the documents in a folder I name.',
  'Find my notes and documents about a topic I name.',
  'Which PDFs do I have, and what is each one about?',
  'Which are my largest files and folders?',
  'Compare two documents I name and list how they differ.',
  'Which spreadsheets do I have, and what does each one track?',
  'List my presentations and the topic of each.',
  'What have I written about a project I name, and when?',
]

/** A deployment's default prompts, which an earlier version also saved on a
 *  computer of its own; saved there unchanged, they count as unset. */
const DEPLOYMENT_PROMPTS = [
  'What tools can you use and what can I ask you to do?',
  'What is in this knowledge base?',
  'Which workflows are available to run?',
]

/** The pages a computer of its own shows until setup is finished: all but
 *  Workflows, which needs an ACTIVATE platform and is turned on where wanted.
 *  Settings and Help are always shown. */
export const PERSONAL_SECTIONS = ['chat', 'library', 'search', 'query', 'overview', 'history', 'agents', 'settings', 'help']

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])

/** The saved prompts, or the defaults where none are saved, an empty list
 *  is saved, or a computer of its own kept a deployment's defaults. */
function promptsOf(saved: string[] | undefined): string[] {
  if (!saved?.length || (personalInstall() && sameList(saved, DEPLOYMENT_PROMPTS))) return envPrompts()
  return saved
}

/** The default mission: KB_MISSION, else, on a computer of its own, one
 *  written for its owner's files. */
function defaultMission(): string {
  return process.env.KB_MISSION ?? (personalInstall() ? PERSONAL_MISSION : '')
}

export const PERSONAL_MISSION = [
  "This Studio indexes one person's own files on their computer: notes, documents, papers, spreadsheets, code and other files in the indexed folder.",
  'The person asking owns these files.',
  'Answer from the files, cite the ones used, and say when the files do not cover a question.',
  'Most questions are about finding a file, summarizing or comparing documents, or what changed recently.',
].join(' ')

function envPrompts(): string[] {
  const fallback = personalInstall() ? PERSONAL_PROMPTS : DEPLOYMENT_PROMPTS
  try {
    const fromEnv = JSON.parse(process.env.SUGGESTED_PROMPTS ?? '[]')
    return Array.isArray(fromEnv) && fromEnv.length ? fromEnv : fallback
  } catch { return fallback }
}

/** A computer of its own whose setup is not finished: AI features start off
 *  and Workflows hidden there. Finishing setup saves whatever is still unset,
 *  so an install that finished setup before these defaults keeps its own. */
function freshPersonal(s: StudioSettings): boolean {
  return personalInstall() && !s.setupDone
}

/** Effective values: saved settings override environment defaults. */
export function effectiveSettings(): Required<StudioSettings> {
  const s = loadSettings()
  return {
    appName: s.appName ?? process.env.APP_NAME ?? 'Studio',
    kbLabel: s.kbLabel ?? process.env.KB_LABEL ?? path.basename(KB_ROOT),
    sections: s.sections ?? (process.env.STUDIO_SECTIONS ? process.env.STUDIO_SECTIONS.split(',').map(x => x.trim()).filter(Boolean)
      : freshPersonal(s) ? PERSONAL_SECTIONS : []),
    workflowCollection: s.workflowCollection ?? (process.env.STUDIO_WORKFLOWS ? process.env.STUDIO_WORKFLOWS.split(',').map(x => x.trim()).filter(Boolean) : []),
    theme: s.theme ?? (process.env.THEME === 'dark' ? 'dark' : 'light'),
    accent: s.accent ?? process.env.APP_ACCENT ?? 'navy',
    surface: s.surface ?? process.env.APP_SURFACE ?? 'neutral',
    iconUrl: s.iconUrl ?? process.env.APP_ICON ?? '',
    iconUrlDark: s.iconUrlDark ?? process.env.APP_ICON_DARK ?? '',
    sweepIntervalSec: s.sweepIntervalSec ?? Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
    searchEngine: s.searchEngine ?? (process.env.STUDIO_SEARCH_ENGINE === 'gufi_vt' ? 'gufi_vt' : 'parallel'),
    suggestedPrompts: promptsOf(s.suggestedPrompts),
    visionModel: s.visionModel ?? process.env.ADE_VISION_MODEL ?? '',
    disabledTools: s.disabledTools ?? [],
    mcpEnabled: s.mcpEnabled ?? process.env.MCP_ENABLED !== '0',
    ragProxyEnabled: s.ragProxyEnabled ?? process.env.RAG_PROXY_ENABLED !== '0',
    // One snapshot a day by default. Every index pass would make the
    // timeline a churn log and the retention window cover barely a day;
    // 0 captures every pass, for a corpus worth watching that closely.
    historyIntervalSec: s.historyIntervalSec ?? Number(process.env.HISTORY_INTERVAL_SEC ?? 86400),
    mcpServers: s.mcpServers ?? [],
    // Fan-out is the assistant's judgment, but its bounds are the
    // operator's. Off by default: a deployment opts into spending many
    // model runs on one request.
    // On by default since v1.13: the guardrails (server-side ceiling,
    // depth limit, read-only agents) are the control, and shipping it off
    // meant the Agents tab advertised a capability most deployments never
    // saw. DELEGATION_ENABLED=0 or the setting turns it back off.
    delegationEnabled: s.delegationEnabled ?? process.env.DELEGATION_ENABLED !== '0',
    delegationMaxAgents: s.delegationMaxAgents ?? Number(process.env.DELEGATION_MAX_AGENTS ?? 6),
    maxToolRounds: s.maxToolRounds ?? (Number(process.env.STUDIO_MAX_TOOL_ROUNDS) || 50),
    delegationMaxDepth: s.delegationMaxDepth ?? Number(process.env.DELEGATION_MAX_DEPTH ?? 1),
    agentExecution: s.agentExecution ?? asExecution(process.env.AGENT_EXECUTION) ?? 'runner',
    voiceEnabled: s.voiceEnabled ?? process.env.VOICE_ENABLED === '1',
    voiceUrl: s.voiceUrl ?? process.env.VOICE_URL ?? '',
    setupDone: s.setupDone ?? false,
    // Off on a new computer of its own until it is turned on (setup's AI
    // step, or Settings); on elsewhere, and where setup finished before this
    // default, unless AI_ENABLED=0.
    aiEnabled: s.aiEnabled ?? (freshPersonal(s) ? process.env.AI_ENABLED === '1' : process.env.AI_ENABLED !== '0'),
    updates: asPolicy(s.updates),
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
    // A blank mission saved by the AI features page is no mission chosen.
    kbMission: s.kbMission?.trim() ? s.kbMission : defaultMission(),
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
    // one up when none is running (hpc_status by default; a site can name
    // its own variant).
    hpcStatusWorkflow: s.hpcStatusWorkflow ?? process.env.HPC_STATUS_WORKFLOW ?? 'hpc_status',
    // Per-model chat template kwargs, JSON of {modelSubstring: kwargs}.
    // Default disables Qwen thinking: the gateway strips reasoning content
    // from session-model streams, so thinking is invisible dead time and a
    // turn can end with its answer trapped in the stripped channel.
    chatTemplateKwargs: s.chatTemplateKwargs ?? process.env.CHAT_TEMPLATE_KWARGS ?? JSON.stringify({ 'qwen': { enable_thinking: false }, 'gpt-oss': { reasoning_effort: 'low' } }),
    // Context windows for models that have small ones, JSON of
    // {modelSubstring: tokens}. The tool loop trims old tool results to
    // stay inside; without this a long loop overflows the window and the
    // serve answers 400, which the gateway forwards as an empty stream.
    chatModelWindows: s.chatModelWindows ?? process.env.CHAT_MODEL_WINDOWS ?? JSON.stringify({ 'qwen': 57344, 'gpt-oss': 65536 }),
    serveWorkflows: s.serveWorkflows ?? process.env.SERVE_WORKFLOWS ?? JSON.stringify({
      vllm: { workflow: 'vllm-endpoint', inputs: {} },
      ollama: { workflow: 'ollama-endpoint', inputs: {} },
    }),
  }
}

/** Save some settings outside the Settings form, keeping the rest. */
export async function updateSettings(patch: StudioSettings): Promise<StudioSettings> {
  const next: StudioSettings = { ...loadSettings(), ...patch }
  await fsp.mkdir(INDEX_BASE, { recursive: true })
  await fsp.writeFile(FILE, JSON.stringify(next, null, 1))
  cache = next
  return next
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

  // Connection state for each attached MCP server, so the page can show
  // whether it answered and how many tools it offered. ?cached=1 reports
  // the last state seen and contacts no server, which is what a poll wants.
  app.get('/api/settings/mcp-servers', async req => ({
    servers: (req.query as { cached?: string }).cached === '1' ? cachedRemoteStatus() : await remoteStatus(),
  }))

  app.put('/api/settings', async req => {
    const body = req.body as StudioSettings
    const next: StudioSettings = { ...loadSettings() }
    if (body.appName !== undefined) next.appName = String(body.appName).slice(0, 60) || undefined
    if (body.kbLabel !== undefined) next.kbLabel = String(body.kbLabel).slice(0, 60) || undefined
    if (body.sections !== undefined) next.sections = Array.isArray(body.sections) && body.sections.length ? body.sections.map(String).slice(0, 20) : undefined
    if (body.workflowCollection !== undefined) {
      const list = Array.isArray(body.workflowCollection) ? body.workflowCollection.map(n => parseWorkflowEntry(String(n))?.entry).filter((n): n is string => !!n) : []
      next.workflowCollection = list.length ? [...new Set(list)].slice(0, 60) : undefined
    }
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
    if (body.searchEngine !== undefined) {
      if (body.searchEngine !== 'parallel' && body.searchEngine !== 'gufi_vt') throw new KbError(400, 'search engine must be parallel or gufi_vt')
      next.searchEngine = body.searchEngine
    }
    if (body.sweepIntervalSec !== undefined) {
      const n = Number(body.sweepIntervalSec)
      if (!Number.isFinite(n) || n < 0 || n > 86400) throw new KbError(400, 'sweep interval must be 0 to 86400 seconds')
      next.sweepIntervalSec = n
    }
    if (body.suggestedPrompts !== undefined) {
      if (!Array.isArray(body.suggestedPrompts)) throw new KbError(400, 'suggestedPrompts must be an array')
      const list = body.suggestedPrompts.map(p => String(p).slice(0, 300)).filter(Boolean).slice(0, 12)
      // The defaults, saved as they are, stay defaults, so a later version's
      // list reaches them.
      next.suggestedPrompts = list.length && !sameList(list, envPrompts()) ? list : undefined
    }
    if (body.visionModel !== undefined) next.visionModel = String(body.visionModel).slice(0, 120) || undefined
    if (body.mcpEnabled !== undefined) next.mcpEnabled = !!body.mcpEnabled
    if (body.ragProxyEnabled !== undefined) {
      next.ragProxyEnabled = !!body.ragProxyEnabled
      // Off means off everywhere. The /v1 routes already refuse calls, but
      // the platform registration is a separate process holding a session
      // that keeps the model listed in the catalog looking alive; leaving
      // it up made the switch look ignored. Dynamic import because the
      // endpoint module imports settings.
      if (!next.ragProxyEnabled) {
        void import('./ragEndpoint.js').then(m => m.stopRagEndpoint()).catch(() => {})
      }
    }
    if (body.delegationEnabled !== undefined) next.delegationEnabled = !!body.delegationEnabled
    if (body.aiEnabled !== undefined) next.aiEnabled = Boolean(body.aiEnabled)
    if (body.agentExecution !== undefined) {
      const v = asExecution(body.agentExecution)
      if (!v) throw new KbError(400, 'agent execution must be runner, sessions, or both')
      next.agentExecution = v
    }
    if (body.voiceEnabled !== undefined) next.voiceEnabled = !!body.voiceEnabled
    if (body.voiceUrl !== undefined) {
      const u = String(body.voiceUrl).trim().replace(/\/+$/, '')
      if (u && !/^https?:\/\//.test(u)) throw new KbError(400, 'voice URL must be http(s)')
      next.voiceUrl = u || undefined
    }
    if (body.maxToolRounds !== undefined) {
      const n = Number(body.maxToolRounds)
      if (!Number.isInteger(n) || n < 5 || n > 200) throw new KbError(400, 'tool rounds must be 5 to 200')
      next.maxToolRounds = n
    }
    if (body.delegationMaxAgents !== undefined) {
      const n = Number(body.delegationMaxAgents)
      if (!Number.isFinite(n) || n < 1 || n > 24) throw new KbError(400, 'agents must be 1 to 24')
      next.delegationMaxAgents = n
    }
    if (body.delegationMaxDepth !== undefined) {
      const n = Number(body.delegationMaxDepth)
      if (!Number.isFinite(n) || n < 0 || n > 3) throw new KbError(400, 'depth must be 0 to 3')
      next.delegationMaxDepth = n
    }
    if (body.mcpServers !== undefined) {
      if (!Array.isArray(body.mcpServers)) throw new KbError(400, 'mcpServers must be an array')
      next.mcpServers = body.mcpServers.slice(0, 10).map(m => {
        const name = String(m?.name ?? '').toLowerCase().replace(/[^a-z0-9_]/g, '_').replace(/^_+|_+$/g, '').slice(0, 24)
        const url = String(m?.url ?? '').trim().slice(0, 500)
        if (url && !/^https?:\/\//i.test(url)) throw new KbError(400, 'MCP server url must be http(s)')
        return { name, url, header: String(m?.header ?? '').slice(0, 300) || undefined }
      }).filter(m => m.name && m.url)
    }
    if (body.historyIntervalSec !== undefined) {
      const n = Number(body.historyIntervalSec)
      if (!Number.isFinite(n) || n < 0 || n > 2592000) throw new KbError(400, 'history interval must be 0 to 2592000 seconds')
      next.historyIntervalSec = n
    }
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
    if (body.kbMission !== undefined) {
      // Blank, or the default as it is, keeps the default (and a later one).
      const m = String(body.kbMission).slice(0, 4000)
      next.kbMission = m.trim() && m.trim() !== defaultMission().trim() ? m : undefined
    }
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
    if (body.updates !== undefined) {
      const u = body.updates as Partial<UpdatePolicy> | null
      if (!u || typeof u !== 'object' || Array.isArray(u)) throw new KbError(400, 'updates must be an object')
      if (u.check !== undefined && typeof u.check !== 'boolean') throw new KbError(400, 'updates.check must be true or false')
      if (u.install !== undefined && u.install !== 'manual' && u.install !== 'auto') throw new KbError(400, 'updates.install must be manual or auto')
      if (u.channel !== undefined && u.channel !== 'stable' && u.channel !== 'prerelease') throw new KbError(400, 'updates.channel must be stable or prerelease')
      next.updates = asPolicy({ ...asPolicy(next.updates), ...u })
    }
    if (body.ragEndpointName !== undefined) {
      const n = String(body.ragEndpointName).toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40)
      next.ragEndpointName = n || undefined
    }
    await fsp.mkdir(INDEX_BASE, { recursive: true })
    await fsp.writeFile(FILE, JSON.stringify(next, null, 1))
    cache = next
    invalidateRemote()
    return { effective: effectiveSettings(), saved: next }
  })
}
