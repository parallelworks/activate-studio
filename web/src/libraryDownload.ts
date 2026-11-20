import { toast } from '@parallelworks/ui'
import { api } from './api'

/**
 * Download from the Library's right-click menu. A file downloads as itself.
 * A folder downloads as a zip of what the Library shows in it, after the
 * server has said what the zip holds, so a folder too large to zip, or one
 * with no files, is a notice and not a saved file holding the error.
 */

/** What a folder's zip holds, as the server counts it. */
export interface FolderZip { name: string; files: number; bytes: number }

function sizeText(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

/** The notice when a folder's zip starts. */
export function zipNotice(z: FolderZip): string {
  return `Downloading ${z.name}: ${z.files} ${z.files === 1 ? 'file' : 'files'}, ${sizeText(z.bytes)}.`
}

/** Starts a download of a path on the Studio by a link with a download
 *  attribute. A browser keeps it with its downloads, the macOS window saves
 *  it to Downloads, and the Linux and Windows window fetches it into
 *  Downloads and reports where it went. */
export function saveFromStudio(url: string, name: string): void {
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
}

/** Downloads a library file, or a folder as a zip. */
export async function downloadPath(path: string, isDir: boolean): Promise<void> {
  const name = path.split('/').filter(Boolean).pop() || 'library'
  if (!isDir) {
    saveFromStudio(api.downloadUrl(path), name)
    return
  }
  const url = `${api.downloadUrl(path)}&folder=1`
  let zip: FolderZip
  try {
    const res = await fetch(`${url}&check=1`)
    const body = await res.json().catch(() => null) as (FolderZip & { error?: string }) | null
    if (!res.ok || !body) throw new Error(body?.error ?? `the Studio answered ${res.status}`)
    zip = body
  } catch (e) {
    toast.error(`Could not download ${name}: ${(e as Error).message}`)
    return
  }
  if (!zip.files) {
    toast.error(`${name} has no files to download.`)
    return
  }
  saveFromStudio(url, zip.name)
  toast.success(zipNotice(zip))
}
