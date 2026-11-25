import type { ReactElement } from 'react'
import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths } from '@parallelworks/code'
import { DirectoryPicker } from '../code/DirectoryPicker'
import { PwCodeWords } from '../pwcode/PWCodeSession'
import type { Target } from './addLibraryTarget'

/**
 * A folder on the computer a library is added from, chosen as the Code page
 * chooses one (DirectoryPicker): its folders are listed as the person, over
 * the session the library will be read with, and on this computer by the
 * Studio. Loaded when Add a library opens, with the parts it shares.
 */

/** The machine id of a place, which folderUrl turns into its route. */
export const placeId = (t: Target, sshOptions = ''): string =>
  t.kind === 'cluster' ? JSON.stringify({ cluster: t.name })
    : t.kind === 'ssh' ? JSON.stringify({ destination: t.destination, ...(sshOptions ? { sshOptions } : {}) })
      : '{}'

/** Where a place's folders are listed. */
export function folderUrl(url: URL): URL {
  const id = url.pathname.match(/\/machines\/([^/]+)\/directories$/)
  const place = (id ? JSON.parse(decodeURIComponent(id[1])) : {}) as Record<string, string>
  const to = new URL(Object.keys(place).length ? '/api/user-libraries/folders' : '/api/libraries/folders', url)
  for (const [k, v] of [...Object.entries(place), ...url.searchParams]) to.searchParams.set(k, v)
  return to
}

const folderApi = createPwCodeApi(createClient<PwCodePaths>({
  baseUrl: typeof location === 'undefined' ? '' : location.origin,
  fetch: async (request: Request) => {
    const url = folderUrl(new URL(request.url))
    const res = await fetch(url.pathname + url.search, { signal: request.signal, credentials: 'same-origin' })
    if (res.ok) return res
    const body = (await res.json().catch(() => ({}))) as { error?: string }
    const problem = { title: body.error ?? `${res.status}`, detail: body.error, ...(res.status === 400 ? { code: 'agents_workspace_invalid' } : {}) }
    return new Response(JSON.stringify(problem), { status: res.status, headers: { 'Content-Type': 'application/json' } })
  },
}), { hidden: () => document.hidden })

export default function FolderChoice({ place, sshOptions, value, label, trigger, anew, onPick }: {
  place: Target
  sshOptions?: string
  value: string
  label: string
  trigger: ReactElement
  /** One that is not there yet can be named, to be made. */
  anew?: boolean
  onPick: (path: string) => void
}) {
  const id = placeId(place, sshOptions)
  return (
    <PwCodeWords loading={trigger}>
      <DirectoryPicker key={id} api={folderApi} machine={{ id, directories: true } as never} directory={value} recents={[]}
        onPick={onPick} trigger={trigger} label={label} anew={anew} placeholder="/path/to/folder" />
    </PwCodeWords>
  )
}
