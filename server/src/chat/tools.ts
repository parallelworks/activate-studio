import { execFile } from 'node:child_process'
import { gufiAvailable, KB_ROOT, MAX_PREVIEW_BYTES } from '../config.js'
import { listDir, readFileContent, KbError } from '../kb.js'
import { blendHits, searchFts, searchNames, searchVector } from '../gufi.js'

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
        'Run a platform workflow, or validate it with dry_run. ALWAYS validate with dry_run:true first. Only launch a real run when the user has explicitly asked in this conversation to run the workflow.',
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
      name: 'show_in_viewer',
      description:
        'Display something in the library viewer pane for the user: a knowledge base file (images render as images, PDFs as PDFs) or a workflow DAG diagram. Use whenever the user asks to see, show, open, or display a file, image, document, or workflow graph.',
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
]

const TOOL_OUTPUT_CAP = 24_000

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
  display?: { kind: 'file' | 'workflow_dag'; target: string }
}

export async function executeTool(name: string, argsJson: string): Promise<ToolOutcome> {
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
          const hits = blendHits(fts, names, vec, limit + 5)
          const result = hits.length
            ? hits.map(h => `${h.path}\n  ${h.snippet || '(filename match)'}`).join('\n')
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
        return {
          result: `Now displayed in the library viewer: ${kind === 'file' ? target : `DAG of workflow ${target}`}.`,
          summary: `showing ${target}`,
          display: { kind, target },
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
      default:
        return { result: `Unknown tool: ${name}`, summary: 'error' }
    }
  } catch (err: any) {
    const msg = err instanceof KbError ? err.message : String(err?.message ?? err)
    return { result: `Tool error: ${msg}`, summary: 'error' }
  }
}
