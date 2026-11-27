import { STUDIO_MACHINE } from '../pwcode/newChatStart'

/**
 * The machine where this Studio runs pw code for the person: in a new
 * chat's menu the one the Studio drives (id studio), and in the Code page's
 * listing the one the server marks (studioHome), which is the cluster the
 * Studio runs beside under sign-in, or this computer without it. It is named
 * as this Studio's wherever a machine is picked or shown, listed first, and
 * started on by default.
 */

export interface StudioMachineFacts {
  id: string
  name: string
  owner?: string
  studioHome?: boolean
  error?: string
}

/** Whether m is the machine this Studio runs pw code on for the person. */
export function isStudioMachine(m: Partial<StudioMachineFacts> | undefined): boolean {
  return !!m && (m.id === STUDIO_MACHINE || m.studioHome === true)
}

const MARK = ' (this Studio)'

/** Its name as the menus and the chip show it: "my-cluster (this Studio)". */
export function studioMachineName(m: Pick<StudioMachineFacts, 'name'> & Partial<StudioMachineFacts>): string {
  if (!isStudioMachine(m)) return m.name
  if (/^this studio\b/i.test(m.name)) return m.name
  return m.name + MARK
}

/** What its tooltip says: where it is, and whom pw code there runs as. */
export function studioMachineTip(m: Partial<StudioMachineFacts> | undefined): string | undefined {
  if (!isStudioMachine(m)) return undefined
  if (m?.error) return m.error
  const as = m?.owner ? `as you (${m.owner})` : 'as you'
  return `Where this Studio runs. pw code here runs ${as}, with this Studio's tools.`
}
