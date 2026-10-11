import { GATEWAY_BASE } from './config.js'

/** A platform API read with a key, or null when it cannot be had. */
export async function platformJson<T = unknown>(path: string, key: string | null | undefined, timeoutMs = 20_000): Promise<T | null> {
  if (!key) return null
  let host: string
  try { host = new URL(GATEWAY_BASE).origin } catch { return null }
  const res = await fetch(`${host}${path}`, { headers: { Authorization: `Bearer ${key}` }, signal: AbortSignal.timeout(timeoutMs) }).catch(() => null)
  if (!res?.ok) return null
  return await res.json().catch(() => null) as T | null
}

/**
 * A workflow's record from the platform API, or null when the API cannot
 * give it (the caller then asks the CLI). Read this way first because
 * `pw workflows get -o json` hands the YAML over as a Go map, whose keys
 * come out sorted: a form built from it lists its inputs alphabetically,
 * not in the order the workflow declares them and the platform shows them.
 * The API returns them in the declared order.
 */
export async function workflowRecord(name: string, key: string | null | undefined): Promise<any | null> {
  const doc = await platformJson<any>(`/api/workflows/${encodeURIComponent(name)}`, key)
  return doc && typeof doc === 'object' && doc.yaml ? doc : null
}
