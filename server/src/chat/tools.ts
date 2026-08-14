import { execFile } from 'node:child_process'
import { gufiAvailable, KB_ROOT, MAX_PREVIEW_BYTES } from '../config.js'
import { listDir, readFileContent, KbError } from '../kb.js'
import { blendHits, searchFts, searchNames, searchVector } from '../gufi.js'
import { annotateHits } from '../tags.js'
import { effectiveSettings } from '../settings.js'
import { agentBody, extAgents, extSkills, extTools, skillBody } from '../extensions.js'

export interface ToolSpec {
  type: 'function'
  function: {
    name: string
    description: string
    parameters: Record<string, unknown>
  }
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
            args: { type: 'string', description: 'Extra command-line arguments appended to the configured command; omit for none.' },
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
  return new Promise((resolve, reject) => {
    execFile('pw', args, { timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024 }, (err, stdout, stderr) => {
      if (err) return reject(new Error(stderr || err.message))
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

export async function executeTool(name: string, argsJson: string, ctx?: { labelScope?: string[] }): Promise<ToolOutcome> {
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
      case 'cluster_command': {
        const cluster = String(args.cluster ?? '').replace(/[^\w.\/:@-]/g, '')
        const command = String(args.command ?? '').slice(0, 500)
        if (!cluster || !command) return { result: 'cluster and command are required', summary: 'error' }
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
          const cmd = extra ? `${custom.command} ${extra}` : custom.command
          const out = await new Promise<string>((resolve, reject) => {
            execFile('bash', ['-lc', cmd], { timeout: 90_000, maxBuffer: 8 * 1024 * 1024 },
              (err, so, se) => (err ? reject(new Error(se || err.message)) : resolve(so)))
          })
          return { result: (out.trim() || '(no output)').slice(0, TOOL_OUTPUT_CAP), summary: name }
        }
        return { result: `Unknown tool: ${name}`, summary: 'error' }
      }
    }
  } catch (err: any) {
    const msg = err instanceof KbError ? err.message : String(err?.message ?? err)
    return { result: `Tool error: ${msg}`, summary: 'error' }
  }
}
