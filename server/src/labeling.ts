import fsp from 'node:fs/promises'
import path from 'node:path'
import { EXCLUDE_DIRS } from './config.js'
import { extractPath, readFileContent, resolveKb } from './kb.js'
import { streamTurn } from './chat/gateway.js'
import { effectiveTags, tagMaps } from './tags.js'

/**
 * Proposed labels for material that has none.
 *
 * Labelling a corpus by hand is the step people skip, so the labels stay
 * thin and the filters stay useless. This reads what each file actually
 * says, reuses the vocabulary already in the corpus wherever it fits, and
 * proposes a small number of new labels where it does not. Nothing is
 * applied here: the proposals go back for review, and applying them runs
 * through the same path as a label set by hand.
 */

const HEAD_CHARS = 1200
const BATCH = 12

export interface LabelProposal {
  path: string
  labels: string[]
  /** One short line on why, so a reviewer can judge without opening it. */
  why: string
}

export interface LabelSuggestions {
  proposals: LabelProposal[]
  /** Labels proposed that the corpus does not use yet. */
  newLabels: string[]
  vocabulary: { tag: string; count: number }[]
  considered: number
  model: string
}

/** A file's opening text, whatever form it is stored in. */
async function headText(rel: string): Promise<string> {
  try {
    const cached = await fsp.readFile(extractPath(rel), 'utf8')
    if (cached.trim()) return cached.slice(0, HEAD_CHARS)
  } catch { /* not an extracted format, or not extracted yet */ }
  try {
    const f = await readFileContent(rel)
    return (f.content ?? '').slice(0, HEAD_CHARS)
  } catch { return '' }
}

/** Files under a directory, skipping ones that already carry labels. */
async function candidates(dir: string, limit: number, includeLabelled: boolean): Promise<string[]> {
  const out: string[] = []
  const walk = async (rel: string, depth: number): Promise<void> => {
    if (out.length >= limit || depth > 6) return
    let entries
    try { entries = await fsp.readdir(resolveKb(rel), { withFileTypes: true }) } catch { return }
    for (const e of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      if (out.length >= limit) return
      if (e.name.startsWith('.')) continue
      const child = rel ? `${rel}/${e.name}` : e.name
      if (e.isDirectory()) {
        if (EXCLUDE_DIRS.has(e.name)) continue
        await walk(child, depth + 1)
      } else {
        out.push(child)
      }
    }
  }
  await walk(dir.replace(/^\/+|\/+$/g, ''), 0)
  if (includeLabelled) return out
  // A file inherits its directory's labels, so "unlabelled" means nothing
  // of its own and nothing above it.
  const unlabelled: string[] = []
  for (const rel of out) {
    const t = await effectiveTags(rel).catch(() => ({ own: [], inherited: [] }))
    if (!t.own.length && !t.inherited.length) unlabelled.push(rel)
  }
  return unlabelled
}

/**
 * One turn through the same path the chat uses.
 *
 * A plain non-streaming completion with temperature 0 is rejected by the
 * providers this gateway fronts ("An error occurred while generating the
 * response"), so this goes through streamTurn, which is the call shape the
 * chat already works with, and collects the text.
 */
async function complete(messages: unknown[], key: string | null, baseUrl: string | null, model: string): Promise<string> {
  const turn = await streamTurn(
    { model, messages },
    null,
    { onContent: () => {} },
    undefined,
    key,
    baseUrl,
  )
  return turn.content
}

/** Models wrap JSON in prose and fences often enough to plan for it. */
function parseJson(text: string): unknown {
  const fenced = /```(?:json)?\s*([\s\S]*?)```/.exec(text)
  const body = fenced ? fenced[1] : text
  const start = body.search(/[[{]/)
  if (start < 0) throw new Error('no JSON in the reply')
  const end = Math.max(body.lastIndexOf(']'), body.lastIndexOf('}'))
  return JSON.parse(body.slice(start, end + 1))
}

function normalizeLabel(raw: unknown): string {
  return String(raw ?? '').toLowerCase().trim()
    .replace(/[^a-z0-9 _-]/g, '').replace(/\s+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40)
}

export async function suggestLabels(opts: {
  dir?: string
  limit?: number
  includeLabelled?: boolean
  model: string
  key?: string | null
  baseUrl?: string | null
}): Promise<LabelSuggestions> {
  const limit = Math.min(Math.max(opts.limit ?? 40, 1), 200)
  const files = await candidates(opts.dir ?? '', limit, !!opts.includeLabelled)
  const maps = await tagMaps().catch(() => ({ dirTags: new Map(), fileTags: new Map() } as Awaited<ReturnType<typeof tagMaps>>))
  const counts = new Map<string, number>()
  for (const tags of [...maps.dirTags.values(), ...maps.fileTags.values()]) {
    for (const t of tags) counts.set(t, (counts.get(t) ?? 0) + 1)
  }
  const vocabulary = [...counts.entries()].map(([tag, count]) => ({ tag, count })).sort((a, b) => b.count - a.count)
  const known = vocabulary.map(v => v.tag)

  const proposals: LabelProposal[] = []
  for (let i = 0; i < files.length; i += BATCH) {
    const batch = files.slice(i, i + BATCH)
    const described = await Promise.all(batch.map(async rel => ({
      path: rel,
      name: path.basename(rel),
      text: (await headText(rel)).replace(/\s+/g, ' ').slice(0, HEAD_CHARS),
    })))
    const system = [
      'You label documents in a knowledge base so they can be filtered later.',
      'Reuse an existing label whenever it fits; propose a new one only when nothing existing describes the material.',
      'Labels are lowercase, hyphenated, one or two words, and describe subject or document type, never the filename.',
      'Give each file one to three labels. Say nothing about files you cannot judge; leave their labels empty.',
      known.length ? `Labels already in use: ${known.join(', ')}.` : 'The corpus has no labels yet.',
      'Reply with JSON only: [{"path": "...", "labels": ["..."], "why": "short reason"}]',
    ].join('\n')
    const user = described.map(d => `PATH: ${d.path}\nNAME: ${d.name}\nTEXT: ${d.text || '(no extracted text)'}`).join('\n\n---\n\n')

    const reply = await complete(
      [{ role: 'system', content: system }, { role: 'user', content: user }],
      opts.key ?? null, opts.baseUrl ?? null, opts.model,
    )
    let rows: unknown
    try { rows = parseJson(reply) } catch { continue }
    if (!Array.isArray(rows)) continue
    for (const row of rows as Record<string, unknown>[]) {
      const rel = String(row.path ?? '')
      if (!batch.includes(rel)) continue
      const labels = Array.isArray(row.labels)
        ? [...new Set(row.labels.map(normalizeLabel).filter(Boolean))].slice(0, 3)
        : []
      if (!labels.length) continue
      proposals.push({ path: rel, labels, why: String(row.why ?? '').slice(0, 200) })
    }
  }

  const newLabels = [...new Set(proposals.flatMap(p => p.labels))].filter(l => !known.includes(l))
  return { proposals, newLabels, vocabulary, considered: files.length, model: opts.model }
}
