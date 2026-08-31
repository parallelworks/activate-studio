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
import { callRemoteTool, isRemoteTool, remoteToolSpecs } from '../mcpClient.js'
import { gatewayKey } from './gateway.js'

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
  workflow_configs: 'pw workflows get <name> -o json, reading its saved input configurations',
  hpc_environments: 'pw environments ls (scheduler partitions, limits and required fields per system)',
  watch_run: 'pw workflows runs view <id>, polled until the run changes or exposes a session',
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
  delegate: 'spawns concurrent pw code one-shot agents with a server-enforced ceiling and depth limit; results are written under tasks/ in the corpus',
  task_status: 'reads the in-memory task board and its subtask tree',
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
        'Combine existing workflows into a new one that runs each as a subworkflow step, merging their inputs. Use it to assemble a chain toward a goal: list_workflows to see what exists, then compose the ones that fit. Returns the composed workflow.yaml for review; nothing is saved to the platform. To show it, write the YAML into the knowledge base and embed /?embed=dag&path=<that file>.',
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
      name: 'workflow_configs',
      description:
        'List a workflow\'s saved input configurations: the named, ready-made settings for running it on a particular HPC system. Most site-specific detail a user should not have to know (queue or partition, QoS, walltime, GPU directives, account) is already correct in these. Call this before run_workflow whenever a request names a system ("on makau", "on carpenter"), and prefer the configuration whose name matches that system over composing scheduler settings yourself. Configurations marked builtin ship with the workflow; the others were saved by this account and usually carry a resource and a chosen model as well.',
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', description: 'Workflow name from list_workflows' },
          config: { type: 'string', description: 'Optional: one configuration name, to see its full resolved inputs instead of the summary list' },
        },
        required: ['name'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'hpc_environments',
      description:
        'List the schedulable partitions (queues) on a connected HPC system, with their state, free node count, walltime ceiling, the defaults the site applies, and which submission fields the site requires. Use this to choose a partition and a legal walltime, to check a system has capacity before submitting, and when a request names a system that workflow_configs has no configuration for, so scheduler settings have to be built from the site\'s own values rather than guessed.',
      parameters: {
        type: 'object',
        properties: {
          cluster: { type: 'string', description: 'Cluster name from list_clusters; omit to list every system that has partitions' },
        },
        required: [],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'run_workflow',
      description:
        'Run a platform workflow, or validate it with dry_run. Pass config to start from a saved configuration from workflow_configs, which is the reliable way to get site-correct scheduler settings; inputs are merged on top of it and win. When composing or exploring on your own initiative, validate with dry_run:true. When the user has asked in this conversation to run a workflow, their request is the authorization: launch the real run without asking again for confirmation. After a real run, report the run id and call watch_run.',
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', description: 'Workflow name from list_workflows' },
          config: { type: 'string', description: 'Saved configuration name from workflow_configs to load settings from' },
          resource: { type: 'string', description: 'Target system name, e.g. makau. Sets the workflow\'s compute-clusters input; only needed when the configuration does not already carry one, or to retarget it' },
          inputs: { type: 'string', description: 'JSON object of input values, merged over the configuration' },
          dry_run: { type: 'boolean', description: 'true validates without executing (default true)' },
        },
        required: ['name'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'watch_run',
      description:
        'Check a workflow run and wait briefly for it to make progress. Returns the run state, the state of each job, and the session URL once the run exposes one. Use it after launching anything the user is waiting on, such as a served model or an interactive session, and call it again to keep following a run that is still working. It returns as soon as something changes, so repeated calls are how progress gets reported.',
      parameters: {
        type: 'object',
        properties: {
          run: { type: 'string', description: 'Run identifier from run_workflow or workflow_runs; omit to follow the most recent run' },
          wait: { type: 'number', description: 'Seconds to wait for a change before returning, 0-120, default 45' },
        },
        required: [],
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
      name: 'delegate',
      description:
        'Break the current request into subtasks and have agents work them concurrently. Each agent gets its own context and writes a result file into the corpus; you get back a task id, not their output. Delegate when the work genuinely splits into independent strands that are each substantial, for example one per document, per system, or per question. Do not delegate work that is small enough to do directly, that is tightly interdependent, or where one strand needs another\'s answer first. Fewer, larger subtasks beat many small ones.',
      parameters: {
        type: 'object',
        properties: {
          objective: { type: 'string', description: 'What the whole task is trying to achieve' },
          subtasks: {
            type: 'array',
            description: 'The independent strands. Give each a clear objective, a boundary, and what it should produce.',
            items: {
              type: 'object',
              properties: {
                persona: { type: 'string', description: 'Optional persona name from the library' },
                objective: { type: 'string', description: 'This subtask, stated so an agent with no other context can do it' },
              },
              required: ['objective'],
            },
          },
        },
        required: ['objective', 'subtasks'],
      },
    },
  },
  {
    type: 'function',
    function: {
      name: 'task_status',
      description: 'The state of a delegated task: each subtask, its state and lineage, and recent board messages. With no id, lists tasks.',
      parameters: {
        type: 'object',
        properties: { id: { type: 'string', description: 'Task id from delegate; omit to list all' } },
        required: [],
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
  // Every skill's name and description ship in this tool's description on
  // every request, so a large library is a standing token cost: ~146
  // characters each, which is roughly 2,200 tokens at sixty skills. Past a
  // threshold the descriptions are clipped to the part that identifies the
  // skill, which keeps selection possible while bounding the cost; the
  // full text is always in the file the skill loads.
  const FULL_DESC_LIMIT = 25
  const clip = skills.length > FULL_DESC_LIMIT
  const listed = skills
    .map(s => {
      const d = clip ? s.description.slice(0, 60).trim() : s.description
      return `${s.name}${d ? ` (${d}${clip && s.description.length > 60 ? '…' : ''})` : ''}`
    })
    .join('; ')
  return {
    type: 'function',
    function: {
      name: 'use_skill',
      description: `Load a skill: task-specific instructions to follow for the current request. Available skills: ${listed}. Load one whenever the user names it or the task clearly matches its description, then follow the returned instructions.${clip ? ' Descriptions here are shortened; loading a skill returns its full instructions.' : ''}`,
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
  // The names people actually try. "tools" was the one that reached the
  // model as literal text and got a confused answer back.
  if (['help', 'commands', 'skills', 'tools', 'agents', 'personas', 'list'].includes(name)) {
    return { listing: slashListing() }
  }
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
  // A bare unknown /word is someone guessing at the command set, and
  // passing it through means the model answers a question about a string.
  // Anything with content after it is left alone, so a pasted path such as
  // /shared/corpus/file.md still reaches the model as what it is.
  if (!rest) return { listing: `There is no /${m[1]} command.\n\n${slashListing()}` }
  return null
}

export function activeToolSpecs(): ToolSpec[] {
  const eff = effectiveSettings()
  const disabled = new Set(eff.disabledTools ?? [])
  // Delegation is opt-in, and an unavailable capability should not be
  // described to the model at all: a tool it can see is a tool it will
  // try, and the refusal costs a turn.
  if (!eff.delegationEnabled) { disabled.add('delegate'); disabled.add('task_status') }
  const skill = skillToolSpec()
  return [...TOOL_SPECS, ...customToolSpecs(), ...(skill ? [skill] : [])].filter(t => !disabled.has(t.function.name))
}

/** The tool list including any attached MCP servers. Async because a
 *  remote listing is a network call; cached for a minute in the client. */
export async function activeToolSpecsWithRemote(): Promise<ToolSpec[]> {
  const disabled = new Set(effectiveSettings().disabledTools ?? [])
  const remote = await remoteToolSpecs().catch(() => [])
  return [...activeToolSpecs(), ...remote.filter(t => !disabled.has(t.function.name))]
}

/**
 * Find a running Status Monitor without being told where it is.
 *
 * The monitor is usually a session in the same account, so its URL is
 * discoverable rather than configuration: list sessions, keep the running
 * ones whose name or workflow looks like a monitor, and probe each for
 * the fleet endpoint. The first that answers is it. Configuration still
 * wins when set, since a deployment may point at a shared monitor that is
 * not a session of this user's at all.
 *
 * Probing is what makes this safe to guess with: a session that merely
 * sounds like a monitor but does not serve /api/fleet/summary is skipped
 * rather than reported as one.
 */
let monitorCache: { at: number; url: string | null } = { at: 0, url: null }
const MONITOR_TTL_MS = 300_000

function platformAuth(): Record<string, string> {
  const key = gatewayKey()
  return key ? { Authorization: `Bearer ${key}` } : {}
}

async function probeMonitor(base: string): Promise<boolean> {
  try {
    const res = await fetch(`${base}/api/fleet/summary`, {
      headers: platformAuth(), signal: AbortSignal.timeout(6000), redirect: 'manual',
    })
    if (!res.ok) return false
    // A platform login redirect answers 200 with HTML; the monitor answers JSON.
    return (res.headers.get('content-type') ?? '').includes('json')
  } catch {
    return false
  }
}

export async function discoverStatusMonitor(): Promise<string | null> {
  if (Date.now() - monitorCache.at < MONITOR_TTL_MS) return monitorCache.url
  let found: string | null = null
  try {
    const rows = JSON.parse(await pwCli(['sessions', 'ls', '-o', 'json'], 20_000)) as any[]
    const sessions = Array.isArray(rows) ? rows : []
    const looksLikeMonitor = (x: any) => /status|hpc.?mon/i.test(`${x?.name ?? ''} ${x?.targetName ?? ''}`)
    const candidates = sessions
      .filter(x => String(x?.status) === 'running' && typeof x?.externalHref === 'string' && /^https?:/i.test(x.externalHref))
      .sort((a, b) => Number(looksLikeMonitor(b)) - Number(looksLikeMonitor(a)))
      .slice(0, 8)
    for (const c of candidates) {
      const base = String(c.externalHref).replace(/\/+$/, '')
      if (await probeMonitor(base)) { found = base; break }
    }
  } catch { /* no CLI or no sessions: nothing to discover */ }
  monitorCache = { at: Date.now(), url: found }
  return found
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

/**
 * The pw CLI appends notices to stdout ("A new version of the PW CLI is
 * available"), so `-o json` output is JSON followed by prose often enough
 * that a bare JSON.parse is a latent failure in every tool that calls one.
 * Take the first balanced JSON value and ignore whatever trails it.
 */
function jsonFromCli<T = unknown>(text: string): T {
  const start = text.search(/[[{]/)
  if (start < 0) throw new Error(`no JSON in CLI output: ${text.slice(0, 160)}`)
  const open = text[start]
  const close = open === '[' ? ']' : '}'
  let depth = 0
  let inStr = false
  let esc = false
  for (let i = start; i < text.length; i++) {
    const c = text[i]
    if (inStr) {
      if (esc) esc = false
      else if (c === '\\') esc = true
      else if (c === '"') inStr = false
      continue
    }
    if (c === '"') inStr = true
    else if (c === open) depth++
    else if (c === close && --depth === 0) return JSON.parse(text.slice(start, i + 1)) as T
  }
  return JSON.parse(text.slice(start)) as T
}

/** A workflow record, including its saved input configurations. */
async function wfDoc(name: string): Promise<any> {
  const clean = name.replace(/[^A-Za-z0-9_.-]/g, '')
  return jsonFromCli<any>(await pwCli(['workflows', 'get', clean, '-o', 'json'], 60_000))
}

/** Declared inputs of a workflow, flattened to dotted paths with types. */
function declaredInputs(wf: any): { path: string; type: string; def: unknown }[] {
  const on = wf?.yaml?.on ?? wf?.yaml?.[true as unknown as string] ?? {}
  const rows: { path: string; type: string; def: unknown }[] = []
  const walk = (prefix: string, obj: Record<string, any>): void => {
    for (const [k, v] of Object.entries(obj ?? {})) {
      if (!v || typeof v !== 'object') continue
      if (v.type === 'group') walk(`${prefix}${k}.`, v.items ?? {})
      else rows.push({ path: `${prefix}${k}`, type: String(v.type ?? ''), def: v.default })
    }
  }
  walk('', on?.execute?.inputs ?? {})
  return rows
}

function setPath(obj: Record<string, any>, dotted: string, value: unknown): void {
  const parts = dotted.split('.')
  let cur = obj
  for (const p of parts.slice(0, -1)) {
    if (!cur[p] || typeof cur[p] !== 'object') cur[p] = {}
    cur = cur[p]
  }
  cur[parts[parts.length - 1]] = value
}

/** Preset under caller inputs: later values win, groups merge by key. */
function mergeInputs(base: any, over: any): any {
  if (!over || typeof over !== 'object' || Array.isArray(over)) return over ?? base
  const out = { ...(base && typeof base === 'object' && !Array.isArray(base) ? base : {}) }
  for (const [k, v] of Object.entries(over)) out[k] = mergeInputs(out[k], v)
  return out
}

/**
 * A workflow's compute-clusters input wants `pw://<owner>/<name>`, and a
 * user says "makau". The owner is whatever account the cluster is attached
 * under, which the environments listing reports, so resolve it there and
 * fall back to the bare name rather than guessing an owner.
 */
async function resolveResource(name: string): Promise<string> {
  const raw = name.trim()
  if (raw.startsWith('pw://')) return raw
  const bare = raw.replace(/[^\w.-]/g, '')
  try {
    const envs = jsonFromCli<any[]>(await pwCli(['environments', 'ls', '-o', 'json'], 60_000))
    const hit = envs.find(e => String(e?.clusterName ?? '').toLowerCase() === bare.toLowerCase())
    if (hit?.clusterUser) return `pw://${hit.clusterUser}/${hit.clusterName}`
    if (hit?.clusterName) return String(hit.clusterName)
  } catch { /* the bare name is still worth trying */ }
  return bare
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
      case 'workflow_configs': {
        const wf = await wfDoc(String(args.name ?? ''))
        const configs = (wf?.configurations ?? []) as any[]
        if (!configs.length) {
          const sites = declaredInputs(wf).filter(r => r.type === 'compute-clusters').map(r => r.path)
          return {
            result: `Workflow ${wf?.name ?? args.name} has no saved configurations. Build inputs from its declared schema (get_workflow) and the site's own values (hpc_environments).`
              + (sites.length ? `\nIts target-system input is: ${sites.join(', ')}.` : ''),
            summary: 'no configurations',
          }
        }
        const wanted = args.config ? String(args.config).toLowerCase() : ''
        if (wanted) {
          const one = configs.find(c => String(c.name ?? c.id ?? '').toLowerCase() === wanted)
          if (!one) return { result: `No configuration named ${args.config}. Available: ${configs.map(c => c.name ?? c.id).join(', ')}`, summary: 'no such config' }
          return {
            result: `Configuration "${one.name ?? one.id}"${one.builtin ? ' (builtin)' : ''} of ${wf.name} sets:\n${JSON.stringify(one.inputs ?? {}, null, 1)}`,
            summary: `config ${one.name ?? one.id}`,
          }
        }
        // A summary line per configuration: enough for the model to pick
        // one by system name without pulling every nested input.
        const rows = configs.map(c => {
          const ins = (c.inputs ?? {}) as any
          const sl = ins.cluster?.slurm ?? {}
          const pb = ins.cluster?.pbs ?? {}
          const bits: string[] = []
          if (ins.resource) bits.push(`resource=${ins.resource}`)
          if (sl.partition) bits.push(`partition=${sl.partition}`)
          if (sl.qos) bits.push(`qos=${sl.qos}`)
          // The site account is the one required value the builtin
          // configurations omit, so name it wherever it does appear.
          if (sl.account) bits.push(`account=${sl.account}`)
          if (sl.time) bits.push(`walltime=${sl.time}`)
          if (sl.scheduler_directives) bits.push(`sbatch="${String(sl.scheduler_directives).trim().replace(/\n+/g, '; ')}"`)
          if (pb.scheduler_directives) bits.push(`pbs="${String(pb.scheduler_directives).trim().split('\n').filter(l => l && !l.startsWith('##')).join('; ')}"`)
          if (ins.service?.models) bits.push(`models=${ins.service.models}`)
          return `${c.name ?? c.id}${c.builtin ? ' (builtin)' : ' (saved by this account)'}: ${bits.join(', ') || '(no scheduler settings)'}`
        })
        return {
          result: `Saved configurations for ${wf.name}:\n${rows.join('\n')}\n\n`
            + 'Pass one as run_workflow config to launch with these settings; add resource only if the configuration has none.',
          summary: `${configs.length} configurations`,
        }
      }
      case 'hpc_environments': {
        const want = String(args.cluster ?? '').replace(/[^\w.-]/g, '').toLowerCase()
        const envs = jsonFromCli<any[]>(await pwCli(['environments', 'ls', '-o', 'json'], 90_000))
        const rows = want ? envs.filter(e => String(e.clusterName ?? '').toLowerCase() === want) : envs
        if (!rows.length) {
          const names = [...new Set(envs.map(e => e.clusterName))].sort()
          return { result: `No partitions found for "${args.cluster}". Systems with partitions: ${names.join(', ')}`, summary: 'no match' }
        }
        if (!want) {
          // Fleet view: one line per system, so the model can pick a target
          // before asking for that system's partitions in detail.
          const by = new Map<string, any[]>()
          for (const e of rows) {
            const k = String(e.clusterName)
            if (!by.has(k)) by.set(k, [])
            by.get(k)!.push(e)
          }
          const lines = [...by.entries()].sort().map(([name, list]) => {
            const up = list.filter(e => e.partitionState === 'up')
            const free = up.reduce((n, e) => n + (Number(e.availNodes) || 0), 0)
            return `${name} (${list[0].clusterDisplayName || name}), ${list[0].schedulerType}: ${list.length} partitions, ${up.length} up, ${free} nodes free`
          })
          return { result: `Systems with schedulable partitions:\n${lines.join('\n')}\n\nCall again with a cluster name for its partitions and submission fields.`, summary: `${by.size} systems` }
        }
        const head = `${rows[0].clusterDisplayName || want} (${want}), scheduler ${rows[0].schedulerType}, attached as user ${rows[0].clusterUser}`
        const detail = rows.map(e => {
          const req = (e.fields ?? []).filter((f: any) => f.required).map((f: any) => f.key)
          return `  ${e.partitionName}: ${e.partitionState}, ${e.availNodes}/${e.totalNodes} nodes free, max walltime ${e.maxTime}`
            + `\n    defaults ${JSON.stringify(e.defaults ?? {})}`
            + `\n    required ${req.join(', ') || '(none)'}`
        })
        return {
          result: `${head}\n${detail.join('\n')}\n\n`
            + 'account and qos are per-user site values; take them from a saved configuration (workflow_configs) when one exists for this system rather than inventing them.',
          summary: `${rows.length} partitions on ${want}`,
        }
      }
      case 'run_workflow': {
        const wfName = String(args.name ?? '').replace(/[^A-Za-z0-9_./-]/g, '')
        const dryRun = args.dry_run !== false
        const cliArgs = ['workflows', 'run']
        if (dryRun) cliArgs.push('--dry-run')
        // Resource targeting needs the workflow's schema: the input that
        // carries the target system is declared as compute-clusters, and
        // its key differs between workflows.
        let inputs: Record<string, unknown> = args.inputs ? JSON.parse(String(args.inputs)) : {}
        if (args.resource) {
          const target = await resolveResource(String(args.resource))
          let doc: any = null
          try { doc = await wfDoc(wfName) } catch { /* fall back to the conventional key */ }
          const slots = doc ? declaredInputs(doc).filter(r => r.type === 'compute-clusters').map(r => r.path) : []
          for (const p of slots.length ? slots : ['resource']) setPath(inputs, p, target)
        }
        if (args.config) cliArgs.push('--saved-inputs', String(args.config))
        // --inputs overrides --saved-inputs wholesale in the CLI, so merge
        // the configuration in here rather than letting it be dropped.
        if (Object.keys(inputs).length) {
          if (args.config) {
            try {
              const doc = await wfDoc(wfName)
              const cfg = (doc?.configurations ?? []).find((c: any) =>
                String(c.name ?? c.id ?? '').toLowerCase() === String(args.config).toLowerCase())
              if (cfg?.inputs) inputs = mergeInputs(cfg.inputs, inputs)
            } catch { /* send what we have */ }
          }
          cliArgs.push('-i', JSON.stringify(inputs))
        }
        cliArgs.push(wfName, '-o', 'json')
        let out: string
        try {
          out = await pwCli(cliArgs, 180_000)
        } catch (e) {
          // The builtin site configurations carry a partition, QoS and
          // walltime but no account, because an account is per user. That
          // failure is the common one here and it has a specific remedy,
          // so name it instead of returning the raw validation line.
          const msg = String((e as Error).message ?? e)
          if (/missing required field/i.test(msg)) {
            const missing = msg.split(':').pop()?.trim() ?? 'a required field'
            let known = ''
            try {
              const doc = await wfDoc(wfName)
              const accts = [...new Set((doc?.configurations ?? [])
                .map((c: any) => c?.inputs?.cluster?.slurm?.account)
                .filter(Boolean))]
              if (accts.length) known = ` Configurations on this workflow use account ${accts.join(' or ')}; reuse one if it applies to this system.`
            } catch { /* the plain message still helps */ }
            return {
              result: `The submission was rejected for a missing value: ${missing}.${known}`
                + ' Site account and QoS are per user and cannot be inferred from the system itself,'
                + ' so take them from a saved configuration for that system, or ask the user for the project account to charge.',
              summary: 'missing required input',
            }
          }
          throw e
        }
        const trimmed = out.trim()
        let where = ''
        try {
          const r = jsonFromCli<any>(trimmed)
          const run = r?.run ?? r
          if (run?.slug || run?.id) where = `\nRun ${run.slug ?? run.id}. Follow it with watch_run.`
        } catch { /* text output is fine */ }
        return {
          result: (trimmed.slice(0, TOOL_OUTPUT_CAP) || (dryRun ? 'Validation passed.' : 'Run submitted.')) + where,
          summary: dryRun ? 'dry run ok' : 'run submitted',
        }
      }
      case 'watch_run': {
        const wait = Math.max(0, Math.min(Number(args.wait ?? 45) || 45, 120))
        let id = String(args.run ?? '').replace(/[^A-Za-z0-9_.:/-]/g, '')
        if (!id) {
          const listed = jsonFromCli<any>(await pwCli(['workflows', 'runs', 'list', '-o', 'json']))
          const runs = (Array.isArray(listed) ? listed : listed?.runs ?? []) as any[]
          if (!runs.length) return { result: 'No workflow runs on this account yet.', summary: 'none' }
          id = String(runs[0].slug ?? runs[0].id)
        }
        // The view is addressed by slug; an id is what a submission hands
        // back, so accept either and translate when the first lookup fails.
        const view = async (key: string): Promise<any> =>
          jsonFromCli<any>(await pwCli(['workflows', 'runs', 'view', key, '-o', 'json'], 60_000))
        let doc: any
        try {
          doc = await view(id)
        } catch {
          const listed = jsonFromCli<any>(await pwCli(['workflows', 'runs', 'list', '-o', 'json']))
          const runs = (Array.isArray(listed) ? listed : listed?.runs ?? []) as any[]
          const hit = runs.find(r => r.id === id || r.slug === id)
          if (!hit) return { result: `No run named ${id}. List them with workflow_runs.`, summary: 'not found' }
          id = String(hit.slug ?? hit.id)
          doc = await view(id)
        }
        const stamp = (d: any): string => `${d?.status}|${Object.entries(d?.executedJobs ?? {})
          .map(([k, v]: [string, any]) => `${k}:${v?.status}`).join(',')}`
        const TERMINAL = new Set(['completed', 'error', 'failed', 'canceled', 'cancelled'])
        const started = Date.now()
        let before = stamp(doc)
        // Poll rather than sleep the full wait: the point is to return the
        // moment something changes, so a caller reporting progress in chat
        // is not a fixed interval behind the run.
        while (wait > 0 && !TERMINAL.has(String(doc?.status)) && Date.now() - started < wait * 1000) {
          await new Promise(r => setTimeout(r, 5000))
          try {
            const next = await view(id)
            doc = next
            if (stamp(next) !== before) { before = stamp(next); break }
          } catch { break }
        }
        const jobs = Object.entries(doc?.executedJobs ?? {}).map(([k, v]: [string, any]) => {
          // Which step is executing only means something while the job is
          // live; on a skipped or finished job it names a step that never ran.
          const live = /^(running|in_progress|active|started)$/i.test(String(v?.status))
          const step = live ? (v?.steps ?? []).find((s: any) => s?.status && s.status !== 'completed') : null
          // A job the run has not reached yet carries no status at all, and
          // "undefined" reads as a fault rather than as its turn not coming.
          const state = v?.status ? String(v.status) : 'not started'
          return `  ${k}: ${state}${v?.ssh?.remoteHost ? ` on ${v.ssh.remoteHost}` : ''}${step?.name ? ` (at "${step.name}")` : ''}`
        })
        const svc = (doc?.inputs as any)?.service ?? {}
        const parts = [`Run ${doc?.slug ?? id} of ${doc?.workflowName}: ${doc?.status}`, jobs.join('\n')]
        if (svc.endpoint_name) {
          const live = !TERMINAL.has(String(doc?.status))
          parts.push(`Endpoint "${svc.endpoint_name}"${svc.models ? ` serving ${svc.models}` : ''}: `
            + (live
              ? 'appears in the platform model catalog and session list once wait_for_endpoint completes.'
              : `not serving, the run is ${doc?.status}.`))
        }
        if (TERMINAL.has(String(doc?.status)) && String(doc?.status) !== 'completed') {
          parts.push('Use workflow_run_detail for the errors and log tail.')
        }
        return { result: parts.filter(Boolean).join('\n').slice(0, TOOL_OUTPUT_CAP), summary: `${doc?.slug ?? id}: ${doc?.status}` }
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
        try { rows = jsonFromCli<any[]>(out) } catch { return { result: out.slice(0, TOOL_OUTPUT_CAP), summary: 'clusters' } }
        // schedulerType decides whether a job is described with #SBATCH or
        // #PBS, so a listing without it cannot be acted on.
        const slim = rows.map(c => ({
          name: c.name,
          displayName: c.displayName || undefined,
          status: c.status,
          scheduler: c.schedulerType || undefined,
          activeNodes: c.activeNodes,
          type: c.type,
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
        const base = eff2.hpcStatusUrl || await discoverStatusMonitor()
        if (!base) {
          return { result: `No Status Monitor is configured on this deployment, and no running session on this account answers as one. Set the Status Monitor URL in Settings (or HPC_STATUS_URL), or deploy one with launch=true, which runs the ${eff2.hpcStatusWorkflow} workflow; only do that if the user asked for it.`, summary: 'not configured' }
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
          const res = await fetch(`${base}${path2}`, { headers: platformAuth(), signal: AbortSignal.timeout(30_000) })
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
      case 'delegate': {
        const { createTask, taskSummary } = await import('../tasks.js')
        const agents = Array.isArray(args.subtasks) ? args.subtasks : []
        if (!args.objective || !agents.length) return { result: 'objective and subtasks are required', summary: 'error' }
        const model = String(ctx?.model ?? '')
        if (!model) return { result: 'No model in context to run agents with.', summary: 'error' }
        const eff = effectiveSettings()
        if (!eff.delegationEnabled) {
          return { result: 'Delegation is disabled for this deployment; do the work yourself in this conversation.', summary: 'disabled' }
        }
        if (agents.length > eff.delegationMaxAgents) {
          return { result: `Too many subtasks: this deployment allows ${eff.delegationMaxAgents}. Combine them into fewer, larger strands.`, summary: 'over ceiling' }
        }
        try {
          const m = createTask({
            objective: String(args.objective), model,
            agents: agents.map((a: { persona?: string; objective: string }) => ({ persona: a.persona, objective: a.objective })),
            maxAgents: eff.delegationMaxAgents,
            maxDepth: eff.delegationMaxDepth,
          })
          const sum = taskSummary(m)
          return {
            result: `Task ${sum.id} started with ${sum.agents.length} subtasks (ceiling ${sum.maxAgents}, depth ${sum.maxDepth}). Results land under tasks/${sum.id}/ in the knowledge base and the operator can watch the tree on the Agents tab. Poll it with task_status; do not wait idly, tell the user it is running.`,
            summary: `task ${sum.id}`,
          }
        } catch (e) {
          return { result: `Could not delegate: ${String((e as Error).message)}`, summary: 'error' }
        }
      }
      case 'task_status': {
        const { getTask, listTasks, taskSummary } = await import('../tasks.js')
        const mid = String(args.id ?? '')
        if (!mid) {
          const all = listTasks()
          return { result: all.length ? JSON.stringify(all, null, 1) : 'No delegated tasks.', summary: `${all.length} tasks` }
        }
        const m = getTask(mid)
        if (!m) return { result: `No such task: ${mid}`, summary: 'error' }
        const sum = taskSummary(m)
        const tail = m.board.slice(-15).map(b => `[${b.topic}] ${b.from}: ${b.body.slice(0, 200)}`).join('\n')
        return {
          result: `${JSON.stringify({ ...sum, agents: sum.agents.map(a => ({ name: a.name, status: a.state, depth: a.depth, parent: a.parent, note: a.note, result: a.resultPath })) }, null, 1)}\n\nRecent board:\n${tail}`,
          summary: `task ${mid}: ${sum.state}`,
        }
      }
      default: {
        if (isRemoteTool(name)) {
          const out = await callRemoteTool(name, args)
          return { result: (out.trim() || '(no output)').slice(0, TOOL_OUTPUT_CAP), summary: name }
        }
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
