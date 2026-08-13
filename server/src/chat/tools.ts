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
        'Full-text search over the Parallel Works knowledge base (proposals, partnerships, strategy, reports, pipeline, conventions). Returns matching file paths with snippets. Use this before answering questions about company work.',
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
      description: 'List the ACTIVATE platform workflows registered in this account (name and type).',
      parameters: { type: 'object', properties: {}, required: [] },
    },
  },
  {
    type: 'function',
    function: {
      name: 'get_workflow',
      description:
        'Fetch an ACTIVATE workflow definition by name. Returns its YAML jobs, and a DAG summary of jobs and needs-edges suitable for describing or previewing the workflow.',
      parameters: {
        type: 'object',
        properties: {
          name: { type: 'string', description: 'Workflow name from list_workflows' },
        },
        required: ['name'],
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

export async function executeTool(name: string, argsJson: string): Promise<{ result: string; summary: string }> {
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
        const out = await pwCli(['workflows', 'ls'])
        return { result: out.trim(), summary: 'listed' }
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
