import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE } from './config.js'

/**
 * Durable label store for filesystems that refuse user xattrs (NFS and EFS
 * return ENOTSUP). The GUFI index remains the runtime read path; this
 * overlay is the persistence that survives full reindexes, which rebuild
 * xattr rows from a filesystem that has none to offer. Keyed by KB-relative
 * path. Empty on xattr-capable deployments and costs nothing there.
 */

const FILE = path.join(INDEX_BASE, 'tags-overlay.json')
let cache: Record<string, string[]> | null = null

function load(): Record<string, string[]> {
  if (cache) return cache
  try { cache = JSON.parse(fs.readFileSync(FILE, 'utf8')) } catch { cache = {} }
  return cache!
}

export function overlayGet(rel: string): string[] | null {
  return load()[rel] ?? null
}

export function overlaySet(rel: string, tags: string[]): void {
  const all = load()
  if (tags.length) all[rel] = tags
  else if (all[rel]) delete all[rel]
  else return
  fs.writeFileSync(FILE, JSON.stringify(all, null, 1))
}

export function overlayUnder(prefix: string): Record<string, string[]> {
  const all = load()
  const out: Record<string, string[]> = {}
  for (const [rel, tags] of Object.entries(all)) {
    if (!prefix || rel === prefix || rel.startsWith(prefix + '/')) out[rel] = tags
  }
  return out
}

export function overlayEmpty(): boolean {
  return Object.keys(load()).length === 0
}
