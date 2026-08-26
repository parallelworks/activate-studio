import { execFile } from 'node:child_process'
import { AsyncLocalStorage } from 'node:async_hooks'
import fs from 'node:fs/promises'
import path from 'node:path'
import { gufiAvailable, isMissingCli, KB_ROOT, MAX_PREVIEW_BYTES, NO_CLI_MESSAGE, PROJECT_ROOT, PW_CLI } from '../config.js'
import { listDir, readFileContent, KbError } from '../kb.js'
import { blendHits, searchFts, searchNames, searchVector } from '../gufi.js'
import { annotateHits } from '../tags.js'
import { effectiveSettings } from '../settings.js'
import { composeWorkflows } from '../workflowCompose.js'
import { agentBody, extAgents, extSkills, extTools, skillBody } from '../extensions.js'

export interface ToolSpec {
  type: 'function'
  function: {
    name: string
    description: string
    parameters: Record<string, unknown>
  }
}

/** What each built-in actually executes, shown in the Settings tool
 *  catalog so "built-in" is not a black box. */
export const TOOL_CALLS: Record<string, string> = {
  search_kb: 'gufi_query full-text (fts5 words), filename, and vector (sqlite-lembed gvec) searches over the index, blended and label-annotated; grep -ri over the knowledge base when no index exists',
  read_kb_file: 'reads the file under the knowledge base root; office documents and PDFs return the extracted text cached beside the index',
  list_kb_dir: 'directory listing under the knowledge base root',
  query_corpus: 'canned gufi_query aggregations over the per-directory index databases',
  get_labels: 'reads the user.studio.tags xattr maps cached from the filesystem',
  write_kb_file: 'fs write into the knowledge base (path-checked, excluded dirs refused) followed by an incremental reindex of the touched subtree',
  suggest_labels: 'reads the first ~1200 characters of each unlabelled file and asks the deployment model to propose labels from the existing vocabulary (proposals only, nothing applied)',
  apply_labels: 'setfattr user.studio.tags on the paths plus an in-place upsert into the index databases',
  list_workflows: 'pw workflows ls',
  get_workflow: 'pw workflows get <name> -o json',
  serve_model: 'pw workflows get/run on the deployment-configured serving workflow for the engine (settings key serveWorkflows)',
  run_workflow: 'pw workflows run <name> (dry-run validation unless a real run was requested)',
  workflow_runs: 'pw workflows runs ls',
  workflow_run_detail: 'pw workflows runs get <id>',
  compose_workflow: 'reads each source workflow with pw workflows get, then emits a workflow that runs them as `uses:` subworkflow steps',
  pw_help: 'pw --help / pw <command> --help',
  list_clusters: 'pw cluster ls -o json',
  hpc_status: 'GET against the configured Status Monitor REST API (fleet, cluster-usage, placement, insights, storage, events); launch=true instead runs the deployment-configured monitor workflow via the pw CLI',
  cluster_command: 'pw ssh <resource> <command> (read-only scheduler guidance; state changes only on explicit request)',
  show_in_viewer: 'no external call; returns viewer deep-link or inline-embed markdown',
  studio_docs: 'reads docs/HELP.md, docs/ARCHITECTURE.md, or docs/CUSTOMIZATION.md from the app installation',
  use_skill: 'loads extensions/skills/<name>.md into the conversation',
}

export const TOOL_SPECS: ToolSpec[] = [
  {
    type: 'function',
    function: {
      name: 'search_kb',
      description:
        'Full-text and semantic search over the connected knowledge base. Returns matching file paths with snippets. Use this before answering questions about the knowledge base content.',
      parameters: {
        type: 'object',
        properties: {
          query: { type: 'string', description: 'Search terms (plain words, no operators)' },
          limit: { type: 'number', description: 'Max results, default 10' },
          tags: { type: 'string', description: 'Comma-separated label filter: only return material carrying at least one of these labels (e.g. "validated" or "research,theoretical")' },
        },
        required: ['query'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'read_kb_file',
      description:
        'Read a file from the knowledge base by its relative path (as returned by search_kb or list_kb_dir). Returns text content; DOCX/PDF return extracted text when available.',
      parameters: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'Path relative to the knowledge base root' },
          offset: { type: 'number', description: 'Character offset to start from, default 0' },
        },
        required: ['path'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'list_kb_dir',
      description: 'List a knowledge base directory. Empty path lists the root.',
      parameters: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'Directory path relative to the knowledge base root, empty for root' },
        },
        required: [],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'list_workflows',
      description:
        'List the platform workflows registered in this account with names, descriptions, and tags. These are composable building blocks: use this catalog to recommend which workflows fit a task or could be assembled together.',
      parameters: { type: 'object', properties: {}, required: [] },
    },
  },
  {
    type: 'function',
    function: {
      name: 'get_workflow',
      description:
        'Fetch a workflow definition by name. Returns its YAML jobs, inputs, and a DAG summary (jobs and needs-edges) suitable for describing or previewing the workflow before running it.',
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', description: 'Workflow name from list_workflows' },
        },
        required: ['name'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'pw_help',
      description:
        'Show the help text for a pw CLI command to discover the platform command surface (clusters, sessions, storage, workflows, AI). Example commands: "workflows run", "clusters", "sessions".',
      parameters: {
        type: 'object',
        properties: {
          command: { type: 'string', description: 'Subcommand path, space separated; empty for the top-level command list' },
        },
        required: [],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'compose_workflow',
      description:
        'Combine existing workflows into a new one that runs each as a subworkflow step, merging their inputs. Use it to assemble a chain toward a goal: list_workflows to see what exists, then compose the ones that fit. Returns the composed workflow.yaml for review; nothing is saved to the platform.',
      parameters: {
        type: 'object',
        properties: {
          workflows: {
            type: 'array',
            description: 'Workflows to run, in order. Each is a name (cloudinit) or a marketplace reference with an optional version (marketplace/cloudinit/v1.0.1).',
            items: {
              type: 'object',
              properties: {
                uses: { type: 'string', description: 'Workflow reference' },
                name: { type: 'string', description: 'Label for the step' },
                id: { type: 'string', description: 'Job id; derived from the reference when omitted' },
                needs: { type: 'array', items: { type: 'string' }, description: 'Job ids to wait for; omit for a straight chain' },
              },
              required: ['uses'],
            },
          },
          parallel: { type: 'boolean', description: 'Run the steps independently instead of chaining each after the previous one' },
        },
        required: ['workflows'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'serve_model',
      description:
        'Launch a model-serving workflow (vLLM or Ollama) so a new model becomes available on the platform. Without launch=true it describes the chosen engine: which workflow it runs, the inputs that workflow takes, and this deployment\'s preset values. With launch=true it submits the run; do that only when the user asked to launch a model in this conversation. A vLLM launch downloads the model, serves it on the chosen resource, and registers it in the platform model catalog (session:<owner>:<name>/<model>); an Ollama launch serves an Ollama plus Open WebUI session on a Kubernetes cluster, and models are pulled inside it afterwards. Watch progress with workflow_runs and workflow_run_detail.',
      parameters: {
        type: 'object',
        properties: {
          engine: { type: 'string', description: "Which serving engine: 'vllm' or 'ollama' (the configured engines; call with no engine to list them)" },
          model: { type: 'string', description: 'For vllm: the HuggingFace model id to serve (fills model.hf_model_id). For ollama, models are pulled after launch instead.' },
          inputs: { type: 'string', description: 'JSON object of workflow inputs, merged over the deployment preset (e.g. {"resource": "a30gpuserver", "endpoint": {"name": "qwen38-27b"}})' },
          launch: { type: 'boolean', description: 'false (default) describes; true submits the run' },
        },
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'run_workflow',
      description:
        'Run a platform workflow, or validate it with dry_run. When composing or exploring on your own initiative, validate with dry_run:true. When the user has asked in this conversation to run a workflow, their request is the authorization: launch the real run without asking again for confirmation.',
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', description: 'Workflow name from list_workflows' },
          inputs: { type: 'string', description: 'JSON object of input values, if the workflow needs them' },
          dry_run: { type: 'boolean', description: 'true validates without executing (default true)' },
        },
        required: ['name'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'workflow_runs',
      description: 'List recent workflow runs with their status (running, completed, error).',
      parameters: {
        type: 'object',
        properties: {
          limit: { type: 'number', description: 'Max runs to return, default 15' },
        },
        required: [],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'query_corpus',
      description:
        'Structured statistics about the corpus from the file index: largest, newest, oldest, recent (last N days), by_extension totals, big_directories. Use for questions about corpus composition, sizes, and recent activity.',
      parameters: {
        type: 'object',
        properties: {
          canned: { type: 'string', enum: ['largest', 'newest', 'oldest', 'recent', 'by_extension', 'big_directories'] },
          n: { type: 'number', description: 'Row limit, default 25' },
          days: { type: 'number', description: 'For recent: window in days, default 7' },
          subtree: { type: 'string', description: 'Restrict to a subtree, empty for whole corpus' },
        },
        required: ['canned'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'suggest_labels',
      description:
        'Read files that carry no labels and propose labels for them, reusing the corpus vocabulary where it fits and proposing new labels where it does not. Returns proposals only; nothing is applied. Use when the user asks how the library should be labelled, or asks for help classifying material, then show the proposals and apply them with apply_labels once the user agrees.',
      parameters: {
        type: 'object',
        properties: {
          dir: { type: 'string', description: 'KB-relative directory to look at; empty for the whole corpus' },
          limit: { type: 'number', description: 'How many files to consider (default 40, max 200)' },
          include_labelled: { type: 'boolean', description: 'Also consider files that already have labels (default false)' },
        },
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'write_kb_file',
      description:
        'Write a text file into the knowledge base and index it, for material generated in the conversation: a case file, a script, a geometry (ASCII STL), a note. Use ONLY when the user asked for something to be created or saved. Excluded and dot-directories are refused, and an existing file is only replaced with overwrite=true. After writing a viewable file (STL, markdown, case files), show_in_viewer displays it.',
      parameters: {
        type: 'object',
        properties: {
          path: { type: 'string', description: 'KB-relative path to write, e.g. models/generated/dam.stl' },
          content: { type: 'string', description: 'The full file content (text; up to 2 MB)' },
          overwrite: { type: 'boolean', description: 'Replace an existing file (default false)' },
        },
        required: ['path', 'content'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'apply_labels',
      description:
        'Add or remove provenance labels on knowledge base files or directories. A directory label applies to its whole subtree. Use ONLY when the user explicitly asks to label, tag, or reclassify something.',
      parameters: {
        type: 'object',
        properties: {
          paths: { type: 'string', description: 'Comma-separated KB-relative paths' },
          add: { type: 'string', description: 'Comma-separated labels to add' },
          remove: { type: 'string', description: 'Comma-separated labels to remove' },
        },
        required: ['paths'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'get_labels',
      description: 'List the label vocabulary in use across the knowledge base with usage counts (labels convey provenance such as research versus validated).',
      parameters: { type: 'object', properties: {}, required: [] },
    },
  },
  {
    type: 'function',
    function: {
      name: 'studio_docs',
      description:
        'Read this Studio\'s own documentation to answer questions about using the application itself: "help" is the in-app user guide (chat, library, search, query, labels, adding material, navigation), "architecture" explains the index and retrieval design, "customization" covers deployment configuration. Use for any question about how the Studio works or how to do something in it; do not search the knowledge base for these.',
      parameters: {
        type: 'object',
        properties: {
          doc: { type: 'string', enum: ['help', 'architecture', 'customization'], description: 'Which document to read' },
        },
        required: ['doc'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'show_in_viewer',
      description:
        'Get a link that opens something in the library viewer: a knowledge base file (images render as images, PDFs as PDFs, STL and STEP models in a 3D viewer) or a workflow DAG diagram. Returns a markdown link; include it verbatim in your reply so the user can click through. Use whenever the user asks to see, show, open, or display a file, image, document, or workflow graph.',
      parameters: {
        type: 'object',
        properties: {
          kind: { type: 'string', enum: ['file', 'workflow_dag'], description: 'What to display' },
          target: { type: 'string', description: 'KB-relative file path, or workflow name for workflow_dag' },
        },
        required: ['kind', 'target'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'workflow_run_detail',
      description: 'Inspect one workflow run: status detail, and its errors and recent logs when it failed or is running.',
      parameters: {
        type: 'object',
        properties: {
          run: { type: 'string', description: 'Run identifier from workflow_runs' },
        },
        required: ['run'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'list_clusters',
      description: 'List the compute clusters connected to this account: name, status, active nodes, type. Use this before any cluster_command call to find the right cluster name and confirm it is running.',
      parameters: { type: 'object', properties: {} },
    },
  },
  {
    type: 'function',
    function: {
      name: 'hpc_status',
      description:
        'Live status of the HPC fleet from the deployment\'s Status Monitor: which systems are up and how busy, queue depth and node availability per system, allocation burn, storage quotas and purge exposure, open insights, and placement recommendations for where a job would start fastest. Use before recommending where to submit or launch anything (including serve_model targets), and to answer "is X up", "which queue", "how much allocation is left". Read-only.',
      parameters: {
        type: 'object',
        properties: {
          view: { type: 'string', description: "One of: summary (fleet at a glance), fleet (every system), queues (queue health; add cluster for one system), placement (where to submit), insights (open findings), storage (quotas and purge), events (recent changes)" },
          cluster: { type: 'string', description: 'System name, for queues' },
          launch: { type: 'boolean', description: 'Deploy a Status Monitor for this user via the configured workflow, when none is running. Only when the user asked for one.' },
          inputs: { type: 'string', description: 'JSON workflow inputs for the launch, when the workflow needs any' },
        },
        required: ['view'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'cluster_command',
      description: 'Run a command on a running cluster over SSH (pw ssh <cluster> <command>). Intended for read-only scheduler and system queries: squeue, sinfo, sacct, scontrol show, qstat, pbsnodes, df, nvidia-smi, hostname. State-changing commands (sbatch, scancel, qsub, qdel, rm, kill) are appropriate when the user asked for that action in this conversation; the request itself is the authorization, so do not ask again for confirmation. Do not initiate state changes the user did not request.',
      parameters: {
        type: 'object',
        properties: {
          cluster: { type: 'string', description: 'Cluster or resource name from list_clusters' },
          command: { type: 'string', description: 'The command line to run on the cluster' },
        },
        required: ['cluster', 'command'],
      },
    },
  },
]

const TOOL_OUTPUT_CAP = 24_000

/** Split a model-supplied argument string into argv words.
 *
 *  Quote-aware so `--flag "two words"` still arrives as two arguments, and
 *  deliberately nothing more: no metacharacter, expansion, or substitution
 *  handling. The words produced here are passed to the command as positional
 *  parameters, so anything the model writes is data rather than shell syntax. */
export function splitToolArgs(input: string): string[] {
  const out: string[] = []
  let cur = ''
  let started = false
  let quote: '"' | "'" | null = null
  for (const ch of input) {
    if (quote) {
      if (ch === quote) quote = null
      else cur += ch
      continue
    }
    if (ch === '"' || ch === "'") {
      quote = ch
      started = true
      continue
    }
    if (/\s/.test(ch)) {
      if (started || cur) out.push(cur)
      cur = ''
      started = false
      continue
    }
    cur += ch
    started = true
  }
  if (started || cur) out.push(cur)
  return out
}

/** Specs for user-defined command tools from Settings. Names that collide
 *  with a built-in are dropped. */
/** Settings-stored custom tools plus file-based tools from
 *  extensions/tools/*.json; settings win on a name collision. */
function allCustomTools(): { name: string; description: string; command: string }[] {
  const merged = new Map<string, { name: string; description: string; command: string }>()
  for (const t of extTools()) merged.set(t.name, t)
  for (const t of effectiveSettings().customTools ?? []) if (t.name && t.command) merged.set(t.name, t)
  return [...merged.values()]
}

/** The command line behind a custom or file-based tool, for display. */
export function commandFor(name: string): string | undefined {
  return allCustomTools().find(t => t.name === name)?.command
}

export function customToolSpecs(): ToolSpec[] {
  const builtin = new Set(TOOL_SPECS.map(t => t.function.name))
  return allCustomTools()
    .filter(t => t.name && t.command && !builtin.has(t.name))
    .map(t => ({
      type: 'function' as const,
      function: {
        name: t.name,
        description: `${t.description || 'Custom deployment tool.'} (Runs a configured command on the Studio server; output is returned here.)`,
        parameters: {
          type: 'object',
          properties: {
            args: { type: 'string', description: 'Extra command-line arguments for the configured command; omit for none. Passed as positional parameters, so shell syntax has no effect and quotes group words.' },
          },
        },
      },
    }))
}

/** The tool list a chat request actually gets: built-ins plus custom tools,
 *  minus anything disabled in Settings. */
/** use_skill only exists when skill files are present; its description
 *  carries the current catalog so the model knows what is loadable. */
export function skillToolSpec(): ToolSpec | null {
  const skills = extSkills()
  if (!skills.length) return null
  return {
    type: 'function',
    function: {
      name: 'use_skill',
      description: `Load a skill: task-specific instructions to follow for the current request. Available skills: ${skills.map(s => `${s.name}${s.description ? ` (${s.description})` : ''}`).join('; ')}. Load one whenever the user names it or the task clearly matches its description, then follow the returned instructions.`,
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', enum: skills.map(s => s.name), description: 'Skill to load' },
        },
        required: ['name'],
      },
    },
  }
}

/** Deterministic /help listing of everything slash-invocable. */
export function slashListing(): string {
  const lines: string[] = ['Slash commands resolve before the model sees the message.', '']
  const skills = extSkills()
  if (skills.length) {
    lines.push('Skills (instructions applied to your request):')
    for (const sk of skills) lines.push(`- /${sk.name}${sk.description ? ` : ${sk.description}` : ''}`)
    lines.push('')
  }
  const agents = extAgents()
  if (agents.length) {
    lines.push('Agents (standing instructions adopted for one message):')
    for (const a of agents) lines.push(`- /${a.name}${a.description ? ` : ${a.description}` : ''}`)
    lines.push('')
  }
  const customs = allCustomTools()
  if (customs.length) {
    lines.push('Command tools (run directly; add arguments after the name):')
    for (const t of customs) lines.push(`- /${t.name}${t.description ? ` : ${t.description}` : ''}`)
    lines.push('')
  }
  lines.push('Built-in tools can also be invoked by name, e.g. /search_kb my topic.')
  lines.push('Everything here also works in plain language; slash form just makes it explicit.')
  return lines.join('\n')
}

/** Resolve a leading /command on a user message. Returns the replacement
 *  message content, or { listing } for meta commands answered without a
 *  model call, or null when the message is not a recognized command. */
export function expandSlashCommand(content: string): string | { listing: string } | null {
  const m = /^\/([A-Za-z0-9_-]+)([\s\S]*)$/.exec(content.trim())
  if (!m) return null
  const name = m[1].toLowerCase().replace(/-/g, '_')
  const rest = m[2].trim()
  if (name === 'help' || name === 'commands' || name === 'skills') return { listing: slashListing() }
  const skill = skillBody(name)
  if (skill != null) {
    return `Follow these instructions for this request.\n\n${skill}\n\nRequest: ${rest || 'Apply the instructions to the current conversation context.'}`
  }
  const agent = agentBody(name)
  if (agent != null) {
    return `Adopt these standing instructions for this request.\n\n${agent}\n\nRequest: ${rest || 'Proceed.'}`
  }
  const toolNames = new Set([...TOOL_SPECS, ...customToolSpecs()].map(t => t.function.name))
  if (toolNames.has(name)) {
    return `Call the ${name} tool now${rest ? ` with arguments derived from: ${rest}` : ''}, then answer from its output.`
  }
  return null
}

export function activeToolSpecs(): ToolSpec[] {
  const disabled = new Set(effectiveSettings().disabledTools ?? [])
  const skill = skillToolSpec()
  return [...TOOL_SPECS, ...customToolSpecs(), ...(skill ? [skill] : [])].filter(t => !disabled.has(t.function.name))
}

function pwCli(args: string[], timeoutMs = 30_000): Promise<string> {
  // The caller's own key (when they added one) scopes platform actions to
  // their account; otherwise the deployment credential applies.
  const userKey = toolContext.getStore()?.userKey
  const env = userKey ? { ...process.env, PW_API_KEY: userKey } : process.env
  return new Promise((resolve, reject) => {
    execFile(PW_CLI, args, { timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024, env }, (err, stdout, stderr) => {
      // "spawn pw ENOENT" tells the model nothing it can act on and reads
      // as though the command failed, so name the actual condition.
      if (err) return reject(new Error(isMissingCli(err) ? NO_CLI_MESSAGE : (stderr || err.message)))
      resolve(stdout)
    })
  })
}

function grepFallback(query: string, limit: number): Promise<string[]> {
  const args = [
    '-ril', '--include=*.md', '--include=*.txt', '--include=*.py', '--include=*.yaml',
    '--exclude-dir=.git', '--exclude-dir=node_modules', '--exclude-dir=.venv', '--exclude-dir=__pycache__',
    query, KB_ROOT,
  ]
  return new Promise(resolve => {
    execFile('grep', args, { timeout: 20_000, maxBuffer: 8 * 1024 * 1024 }, (_err, stdout) => {
      const rels = stdout.split('\n').filter(Boolean).slice(0, limit)
        .map(p => p.startsWith(KB_ROOT + '/') ? p.slice(KB_ROOT.length + 1) : p)
      resolve(rels)
    })
  })
}

export interface ToolOutcome {
  result: string
  summary: string
}

// Per-invocation context (caller's own platform key for pw CLI executions)
// travels via AsyncLocalStorage so nested helpers stay signature-stable and
// concurrent tool calls from different users cannot cross-contaminate.
const toolContext = new AsyncLocalStorage<{ userKey: string | null }>()

export async function executeTool(name: string, argsJson: string, ctx?: { labelScope?: string[]; userKey?: string | null; model?: string | null }): Promise<ToolOutcome> {
  return toolContext.run({ userKey: ctx?.userKey ?? null }, () => executeToolImpl(name, argsJson, ctx))
}

async function executeToolImpl(name: string, argsJson: string, ctx?: { labelScope?: string[]; userKey?: string | null; model?: string | null }): Promise<ToolOutcome> {
  let args: any = {}
  try { args = argsJson ? JSON.parse(argsJson) : {} } catch { /* tolerate bad JSON */ }
  try {
    switch (name) {
      case 'search_kb': {
        const query = String(args.query ?? '').slice(0, 200)
        const limit = Math.min(Number(args.limit) || 10, 25)
        if (gufiAvailable()) {
          const [fts, names, vec] = await Promise.all([
            searchFts(query, limit),
            searchNames(query, 5),
            searchVector(query, Math.min(limit, 8)).catch(() => []),
          ])
          // A conversation-level label scope is enforced here regardless of
          // what the model asked for; model-requested tags narrow further
          // only when they fall inside the scope.
          const scope = ctx?.labelScope?.filter(Boolean) ?? []
          const requested = args.tags ? String(args.tags).split(',').filter(Boolean) : undefined
          let filter = requested
          if (scope.length) {
            const within = requested?.filter(t => scope.includes(t))
            filter = within?.length ? within : scope
          }
          const hits = await annotateHits(blendHits(fts, names, vec, limit + 5), filter)
          const result = hits.length
            ? hits.map(h => `${h.path}${h.tags?.length ? ` [labels: ${h.tags.join(', ')}]` : ''}\n  ${h.snippet || '(filename match)'}`).join('\n')
            : 'No matches.'
          return { result, summary: `${hits.length} hits` }
        }
        const rels = await grepFallback(query, limit)
        return { result: rels.length ? rels.join('\n') : 'No matches.', summary: `${rels.length} hits (grep fallback)` }
      }
      case 'read_kb_file': {
        const rel = String(args.path ?? '')
        const offset = Math.max(0, Number(args.offset) || 0)
        const fc = await readFileContent(rel)
        if (fc.content == null) {
          return { result: `Binary file (${fc.kind}, ${fc.size} bytes); no text available.`, summary: 'binary' }
        }
        const slice = fc.content.slice(offset, offset + TOOL_OUTPUT_CAP)
        const more = fc.content.length > offset + TOOL_OUTPUT_CAP
          ? `\n[truncated; continue with offset=${offset + TOOL_OUTPUT_CAP}]` : ''
        return { result: slice + more, summary: `${slice.length} chars (${fc.source})` }
      }
      case 'list_kb_dir': {
        const entries = await listDir(String(args.path ?? ''))
        const result = entries.map(e => `${e.type === 'dir' ? 'd' : '-'} ${e.path}${e.type === 'dir' ? '/' : ` (${e.size}b)`}`).join('\n')
        return { result: result || '(empty)', summary: `${entries.length} entries` }
      }
      case 'compose_workflow': {
        const list = (args.workflows ?? []) as { uses: string; name?: string; id?: string; needs?: string[] }[]
        const composed = await composeWorkflows(list, { chain: !args.parallel })
        const notes = [
          composed.missing.length ? `could not read: ${composed.missing.join(', ')} (inputs for those steps were not merged)` : '',
          composed.conflicts.length ? `input conflicts: ${composed.conflicts.join('; ')}` : '',
        ].filter(Boolean)
        return {
          result: [
            composed.yamlText,
            notes.length ? `\n# ${notes.join('\n# ')}` : '',
          ].join(''),
          summary: `composed ${list.length} workflows, ${composed.inputs.length} inputs`,
        }
      }
      case 'list_workflows': {
        const out = await pwCli(['workflows', 'ls', '-o', 'json'])
        const wfs = JSON.parse(out) as any[]
        const catalog = wfs.map(w => ({
          name: w.name,
          displayName: w.displayName,
          description: w.description || '',
          tags: (w.tags ?? []).filter(Boolean),
          type: w.type,
        }))
        return { result: JSON.stringify(catalog, null, 1), summary: `${catalog.length} workflows` }
      }
      case 'pw_help': {
        const parts = String(args.command ?? '').split(/\s+/).filter(Boolean)
          .map(p => p.replace(/[^a-z0-9-]/gi, '')).filter(Boolean).slice(0, 4)
        const out = await pwCli([...parts, '--help'])
        return { result: out.trim().slice(0, TOOL_OUTPUT_CAP), summary: `pw ${parts.join(' ')} --help` }
      }
      case 'serve_model': {
        const { effectiveSettings } = await import('../settings.js')
        let engines: Record<string, { workflow: string; inputs?: Record<string, unknown> }> = {}
        try { engines = JSON.parse(effectiveSettings().serveWorkflows) } catch { /* fall through to empty */ }
        const engine = String(args.engine ?? '').toLowerCase()
        if (!engine || !engines[engine]) {
          const listing = Object.entries(engines).map(([k, v]) => `${k}: workflow ${v.workflow}`).join('\n')
          return {
            result: `Configured serving engines:\n${listing || '(none configured on this deployment)'}\nCall again with engine and launch=false to see that workflow's inputs.`,
            summary: 'engines listed',
          }
        }
        const cfg = engines[engine]
        // Deep-merge: preset under caller inputs, group by group.
        const merge = (base: any, over: any): any => {
          if (!over || typeof over !== 'object' || Array.isArray(over)) return over ?? base
          const out = { ...(base && typeof base === 'object' ? base : {}) }
          for (const [k, v] of Object.entries(over)) out[k] = merge(out[k], v)
          return out
        }
        let merged: Record<string, unknown> = { ...(cfg.inputs ?? {}) }
        if (args.inputs) merged = merge(merged, JSON.parse(String(args.inputs)))
        if (engine === 'vllm' && args.model) {
          merged = merge(merged, { model: { hf_model_id: String(args.model) } })
        }
        if (args.launch !== true) {
          // Describe: the workflow's declared inputs plus the preset.
          let declared = ''
          try {
            const doc = JSON.parse((await pwCli(['workflows', 'get', cfg.workflow, '-o', 'json'], 60_000)).split('\n\n')[0])
            const ins = doc?.yaml?.on?.execute?.inputs ?? doc?.yaml?.[true as any]?.execute?.inputs ?? {}
            const rows: string[] = []
            const walk = (prefix: string, obj: Record<string, any>) => {
              for (const [k, v] of Object.entries(obj ?? {})) {
                if (v && typeof v === 'object' && v.type === 'group') walk(`${prefix}${k}.`, v.items ?? {})
                else if (v && typeof v === 'object') rows.push(`${prefix}${k} (default: ${JSON.stringify(v.default ?? null)})`)
              }
            }
            walk('', ins)
            declared = rows.join('\n')
          } catch (e) {
            declared = `(could not read the workflow: ${String((e as Error).message ?? e).slice(0, 120)})`
          }
          return {
            result: `Engine ${engine} runs workflow ${cfg.workflow}.\nDeclared inputs:\n${declared}\n\nInputs that would be submitted now:\n${JSON.stringify(merged, null, 1)}\n\nCall again with launch=true to submit. Only do so if the user asked to launch a model.`,
            summary: `${engine} serve described`,
          }
        }
        const out = await pwCli(['workflows', 'run', '-i', JSON.stringify(merged), cfg.workflow, '-o', 'json'], 180_000)
        return {
          result: `Run submitted for ${cfg.workflow}.\n${out.trim().slice(0, 2000)}\nWatch it with workflow_runs / workflow_run_detail. A vLLM endpoint appears in the model catalog once the model finishes loading.`,
          summary: `${engine} serve launched`,
        }
      }
      case 'run_workflow': {
        const wfName = String(args.name ?? '').replace(/[^A-Za-z0-9_./-]/g, '')
        const dryRun = args.dry_run !== false
        const cliArgs = ['workflows', 'run']
        if (dryRun) cliArgs.push('--dry-run')
        if (args.inputs) {
          const parsed = JSON.parse(String(args.inputs))
          cliArgs.push('-i', JSON.stringify(parsed))
        }
        cliArgs.push(wfName, '-o', 'json')
        const out = await pwCli(cliArgs, 120_000)
        return {
          result: out.trim().slice(0, TOOL_OUTPUT_CAP) || (dryRun ? 'Validation passed.' : 'Run submitted.'),
          summary: dryRun ? 'dry run ok' : 'run submitted',
        }
      }
      case 'workflow_runs': {
        const limit = Math.min(Number(args.limit) || 15, 50)
        const out = await pwCli(['workflows', 'runs', 'list', '-o', 'json']).catch(() => pwCli(['workflows', 'runs', 'list']))
        let result = out.trim()
        try {
          const runs = (JSON.parse(out) as any[]).slice(0, limit)
          result = JSON.stringify(runs, null, 1)
        } catch { /* plain text fallback */ }
        return { result: result.slice(0, TOOL_OUTPUT_CAP), summary: 'runs listed' }
      }
      case 'query_corpus': {
        const { runCanned } = await import('../query.js')
        const r = await runCanned(String(args.canned ?? ''), {
          n: Math.min(Number(args.n) || 25, 100),
          days: Math.min(Number(args.days) || 7, 3650),
          subtree: args.subtree ? String(args.subtree) : undefined,
        })
        const lines = [r.columns.join(' | '), ...r.rows.map(row => row.join(' | '))]
        return { result: lines.join('\n').slice(0, TOOL_OUTPUT_CAP), summary: `${r.rows.length} rows in ${r.elapsedMs} ms` }
      }
      case 'suggest_labels': {
        const { suggestLabels } = await import('../labeling.js')
        const { effectiveSettings } = await import('../settings.js')
        // The model already answering this conversation reads the files
        // too; the configured default is only a fallback for callers that
        // have no conversation, such as the Library button.
        const model = ctx?.model || effectiveSettings().ragDefaultModel
        if (!model) {
          return {
            result: 'No model is available for labelling on this deployment.',
            summary: 'no labelling model',
          }
        }
        const out = await suggestLabels({
          dir: String(args.dir ?? ''),
          limit: Number(args.limit) || 40,
          includeLabelled: !!args.include_labelled,
          model,
          key: ctx?.userKey ?? null,
        })
        if (!out.proposals.length) {
          return { result: `Looked at ${out.considered} files and had nothing to propose.`, summary: 'no proposals' }
        }
        const lines = out.proposals.map(p2 => `${p2.path}: [${p2.labels.join(', ')}]${p2.why ? ` (${p2.why})` : ''}`)
        const fresh = out.newLabels.length ? `\nNew labels proposed: ${out.newLabels.join(', ')}` : ''
        return {
          result: `Proposals for ${out.proposals.length} of ${out.considered} unlabelled files. Nothing has been applied; use apply_labels once the user agrees.${fresh}\n${lines.join('\n')}`,
          summary: `${out.proposals.length} label proposals`,
        }
      }
      case 'write_kb_file': {
        const { resolveKb, KbError } = await import('../kb.js')
        const { EXCLUDE_DIRS } = await import('../config.js')
        const fsp = await import('node:fs/promises')
        const pathMod = await import('node:path')
        const rel = String(args.path ?? '').replace(/^\/+|\/+$/g, '')
        const content = String(args.content ?? '')
        if (!rel || !content) return { result: 'path and content are required', summary: 'error' }
        if (content.length > 2 * 1024 * 1024) return { result: 'Content over 2 MB; write it in parts or as multiple files.', summary: 'too large' }
        for (const part of rel.split('/')) {
          if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) return { result: `Not writable: ${part} is excluded.`, summary: 'refused' }
        }
        let abs: string
        try { abs = resolveKb(rel) } catch (e) { return { result: String((e as Error).message ?? e), summary: 'refused' } }
        const exists = await fsp.stat(abs).then(() => true, () => false)
        if (exists && args.overwrite !== true) {
          return { result: `${rel} already exists. Pass overwrite=true to replace it, or choose another name.`, summary: 'exists' }
        }
        await fsp.mkdir(pathMod.dirname(abs), { recursive: true })
        await fsp.writeFile(abs, content)
        const { reindexForFile } = await import('../indexing.js')
        const { ms } = await reindexForFile(rel).catch(() => ({ ms: 0 }))
        // The affordance arrives exactly when it is needed: a written model
        // or page comes back with the ready-made embed markdown, so the
        // assistant shows the built-in viewer instead of building its own.
        const ext = pathMod.extname(rel).toLowerCase()
        let show = 'show_in_viewer can display it.'
        if (['.stl', '.step', '.stp', '.obj', '.glb', '.gltf'].includes(ext)) {
          show = `Show the interactive 3D model inline in your reply with this markdown, then its link:\n![${rel.split('/').pop()}](/?embed=model&path=${encodeURIComponent(rel)})\n[${rel}](#open=file:${rel.replace(/ /g, '%20')})\nIterate by rewriting the file with overwrite=true; the embed always renders the current version.`
        } else if (['.html', '.htm'].includes(ext)) {
          show = `Show the page inline, sandboxed, with this markdown, then its link:\n![${rel.split('/').pop()}](/?embed=html&path=${encodeURIComponent(rel)})\n[${rel}](#open=file:${rel.replace(/ /g, '%20')})\nThe sandbox allows inline scripts and styles only, no network requests, so keep the page self-contained.`
        }
        return {
          result: `Wrote ${rel} (${content.length} bytes)${exists ? ', replacing the previous version' : ''} and re-indexed in ${ms} ms. ${show}`,
          summary: `wrote ${rel}`,
        }
      }
      case 'apply_labels': {
        const { applyTagsCore } = await import('../tags.js')
        const paths = String(args.paths ?? '').split(',').map((x: string) => x.trim()).filter(Boolean)
        const add = String(args.add ?? '').split(',').map((x: string) => x.trim()).filter(Boolean)
        const remove = String(args.remove ?? '').split(',').map((x: string) => x.trim()).filter(Boolean)
        const updated = await applyTagsCore(paths, add, remove)
        const result = Object.entries(updated).map(([p2, t]) => `${p2}: [${t.join(', ')}]`).join('\n')
        return { result: `Labels updated and re-indexed:\n${result}`, summary: `${paths.length} paths labeled` }
      }
      case 'get_labels': {
        const { tagMaps } = await import('../tags.js')
        const maps = await tagMaps()
        const counts = new Map<string, number>()
        for (const tags of [...maps.dirTags.values(), ...maps.fileTags.values()]) {
          for (const t of tags) counts.set(t, (counts.get(t) ?? 0) + 1)
        }
        const result = [...counts.entries()].map(([t, c]) => `${t}: ${c}`).join('\n') || 'No labels in use yet.'
        return { result, summary: `${counts.size} labels` }
      }
      case 'studio_docs': {
        const which = String(args.doc ?? 'help').toLowerCase()
        const file = which === 'architecture' ? 'ARCHITECTURE.md' : which === 'customization' ? 'CUSTOMIZATION.md' : 'HELP.md'
        let md = await fs.readFile(path.join(PROJECT_ROOT, 'docs', file), 'utf8')
        const eff = effectiveSettings()
        md = md.replaceAll('{appName}', eff.appName).replaceAll('{kbLabel}', eff.kbLabel)
        return { result: md.slice(0, TOOL_OUTPUT_CAP), summary: `${file} (${md.length} chars)` }
      }
      case 'show_in_viewer': {
        const kind = args.kind === 'workflow_dag' ? 'workflow_dag' : 'file'
        let target = String(args.target ?? '')
        if (kind === 'file') {
          const fc = await readFileContent(target).catch(() => null)
          if (!fc) {
            const entries = await listDir(target).catch(() => null)
            if (!entries) return { result: `Not found in the knowledge base: ${target}`, summary: 'not found' }
          }
        } else {
          target = target.replace(/[^A-Za-z0-9_./-]/g, '')
          await pwCli(['workflows', 'get', target])
        }
        const href = `#open=${kind}:${encodeURIComponent(target)}`
        const label = kind === 'file'
          ? `Open ${target.split('/').pop()} in the Library`
          : `Open the ${target} DAG in the Library`
        // Images and page-rendered documents can be SHOWN inline in the chat,
        // not just linked; interactive DAG and 3D viewers embed as windows
        // (the chat renders /?embed= image sources as same-origin iframes).
        const suffix = (target.match(/\.[a-z0-9]+$/i)?.[0] ?? '').toLowerCase()
        const rawUrl = `/api/kb/raw?path=${encodeURIComponent(target)}`
        if (kind === 'workflow_dag') {
          return {
            result: `Show the interactive DAG inline in your reply with this markdown, then the link:\n![${target} DAG](/?embed=dag&workflow=${encodeURIComponent(target)})\n[${label}](${href})`,
            summary: `inline DAG + link for ${target}`,
          }
        }
        if (kind === 'file' && ['.stl', '.step', '.stp'].includes(suffix)) {
          return {
            result: `Show the interactive 3D model inline in your reply with this markdown, then the link:\n![${target.split('/').pop()}](/?embed=model&path=${encodeURIComponent(target)})\n[${label}](${href})`,
            summary: `inline 3D viewer + link for ${target}`,
          }
        }
        if (kind === 'file' && ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg'].includes(suffix)) {
          return {
            result: `This is an image; show it inline in your reply with this markdown, then the link:\n![${target.split('/').pop()}](${rawUrl})\n[${label}](${href})`,
            summary: `inline image + link for ${target}`,
          }
        }
        if (kind === 'file' && ['.pdf', '.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods'].includes(suffix)) {
          return {
            result: `This document renders as page images; you can show its first page inline with this markdown, then the link:\n![${target.split('/').pop()} page 1](/api/kb/pdf-page?path=${encodeURIComponent(target)}&page=1)\n[${label}](${href})`,
            summary: `inline page + link for ${target}`,
          }
        }
        return {
          result: `Link ready. Include this markdown link verbatim in your reply so the user can open it (${kind === 'file' ? 'the Library viewer opens the file and reveals it in its directory' : 'the Library viewer renders the workflow DAG'}): [${label}](${href})`,
          summary: `link for ${target}`,
        }
      }
      case 'workflow_run_detail': {
        const id = String(args.run ?? '').replace(/[^A-Za-z0-9_.:/-]/g, '')
        const view = await pwCli(['workflows', 'runs', 'view', id, '-o', 'json']).catch(() =>
          pwCli(['workflows', 'runs', 'view', id]))
        const errors = await pwCli(['workflows', 'runs', 'errors', id]).catch(() => '')
        const logs = await pwCli(['workflows', 'runs', 'logs', id]).catch(() => '')
        const parts = [`Run detail:\n${view.trim()}`]
        if (errors.trim()) parts.push(`Errors:\n${errors.trim().slice(0, 4000)}`)
        if (logs.trim()) parts.push(`Logs (tail):\n${logs.trim().slice(-4000)}`)
        return { result: parts.join('\n\n').slice(0, TOOL_OUTPUT_CAP), summary: 'run inspected' }
      }
      case 'get_workflow': {
        const wfName = String(args.name ?? '').replace(/[^A-Za-z0-9_.-]/g, '')
        const out = await pwCli(['workflows', 'get', wfName, '-o', 'json'])
        const wf = JSON.parse(out)
        const jobs = wf.yaml?.jobs ?? {}
        const edges: string[] = []
        for (const [job, def] of Object.entries<any>(jobs)) {
          for (const dep of def?.needs ?? []) edges.push(`${dep} -> ${job}`)
        }
        const dag = { name: wf.name, displayName: wf.displayName, jobs: Object.keys(jobs), edges }
        const result = JSON.stringify({ dag, yaml: wf.yaml }, null, 1)
        return {
          result: result.length > TOOL_OUTPUT_CAP ? JSON.stringify({ dag }, null, 1) : result,
          summary: `${dag.jobs.length} jobs, ${edges.length} edges`,
        }
      }
      case 'list_clusters': {
        const out = await pwCli(['cluster', 'ls', '-o', 'json'])
        let rows: any[] = []
        try { rows = JSON.parse(out) } catch { return { result: out.slice(0, TOOL_OUTPUT_CAP), summary: 'clusters' } }
        const slim = rows.map(c => ({
          name: c.name, status: c.status, activeNodes: c.activeNodes, type: c.type,
          connection: c.connectionString || undefined,
        }))
        return { result: JSON.stringify(slim, null, 1), summary: `${slim.length} clusters` }
      }
      case 'hpc_status': {
        const { effectiveSettings } = await import('../settings.js')
        const eff2 = effectiveSettings()
        if (args.launch === true) {
          const wf = eff2.hpcStatusWorkflow
          const cliArgs = ['workflows', 'run']
          if (args.inputs) cliArgs.push('-i', JSON.stringify(JSON.parse(String(args.inputs))))
          cliArgs.push(wf)
          const out = await pwCli(cliArgs, 180_000)
          return {
            result: `Status Monitor launch submitted via workflow ${wf}.\n${out.trim().slice(0, 1500)}\nWatch it with workflow_runs; once it serves, set its URL as the Status Monitor URL in Settings so this tool can read it.`,
            summary: 'monitor launch submitted',
          }
        }
        const base = eff2.hpcStatusUrl
        if (!base) {
          return { result: `No Status Monitor is configured on this deployment (set the Status Monitor URL in Settings, or HPC_STATUS_URL). One can be deployed for this user with launch=true, which runs the ${eff2.hpcStatusWorkflow} workflow; only do that if the user asked for it.`, summary: 'not configured' }
        }
        const view = String(args.view ?? 'summary')
        const cluster = String(args.cluster ?? '').replace(/[^\w.-]/g, '')
        const paths: Record<string, string> = {
          summary: '/api/fleet/summary',
          fleet: '/api/fleet',
          queues: cluster ? `/api/cluster-usage/${cluster}` : '/api/cluster-usage',
          placement: '/api/placement',
          insights: '/api/insights',
          storage: '/api/storage',
          events: '/api/events',
        }
        const path2 = paths[view]
        if (!path2) return { result: `Unknown view: ${view}. Views: ${Object.keys(paths).join(', ')}`, summary: 'bad view' }
        try {
          const res = await fetch(`${base}${path2}`, { signal: AbortSignal.timeout(30_000) })
          if (!res.ok) return { result: `Status Monitor returned ${res.status} for ${path2}.`, summary: `http ${res.status}` }
          const text = await res.text()
          return { result: text.slice(0, TOOL_OUTPUT_CAP), summary: `${view}${cluster ? `:${cluster}` : ''}` }
        } catch (e) {
          return { result: `Status Monitor unreachable at ${base}: ${String((e as Error).message ?? e).slice(0, 120)}`, summary: 'unreachable' }
        }
      }
      case 'cluster_command': {
        const cluster = String(args.cluster ?? '').replace(/[^\w.\/:@-]/g, '')
        const command = String(args.command ?? '')
        if (!cluster || !command) return { result: 'cluster and command are required', summary: 'error' }
        // Refuse rather than truncate: a command cut mid-token is a
        // different command, and the failure it produces points nowhere.
        if (command.length > 8000) {
          return { result: 'Command too long (over 8000 characters). For writing files into the knowledge base, use write_kb_file instead of shell redirection.', summary: 'command too long' }
        }
        const out = await pwCli(['ssh', cluster, command], 90_000)
        return {
          result: (out.trim() || '(no output)').slice(0, TOOL_OUTPUT_CAP),
          summary: `${cluster}: ${command.slice(0, 40)}`,
        }
      }
      case 'use_skill': {
        const skillName = String(args.name ?? '')
        const body = skillBody(skillName)
        if (body == null) return { result: `No such skill: ${skillName}`, summary: 'error' }
        return {
          result: `Skill ${skillName} loaded. Follow these instructions for the current request:\n\n${body.slice(0, TOOL_OUTPUT_CAP)}`,
          summary: `skill ${skillName}`,
        }
      }
      default: {
        const custom = allCustomTools().find(t => t.name === name)
        if (custom) {
          const extra = String(args.args ?? '').slice(0, 400)
          const out = await new Promise<string>((resolve, reject) => {
            execFile('bash', ['-lc', `${custom.command} "$@"`, custom.name, ...splitToolArgs(extra)],
              { timeout: 90_000, maxBuffer: 8 * 1024 * 1024 },
              (err, so, se) => (err ? reject(new Error(se || err.message)) : resolve(so)))
          })
          return { result: (out.trim() || '(no output)').slice(0, TOOL_OUTPUT_CAP), summary: name }
        }
        return { result: `The tool ${name} does not exist in this environment; it may be a backend built-in that is unavailable here. Use only the tools declared for this conversation, or answer in plain text.`, summary: `no such tool ${name}` }
      }
    }
  } catch (err: any) {
    const msg = err instanceof KbError ? err.message : String(err?.message ?? err)
    return { result: `Tool error: ${msg}`, summary: 'error' }
  }
}
