import { isComputer, type PwCodeComponents, type PwCodeMachine } from '@parallelworks/code'

/** What the platform's listing says of a machine that an icon goes by. */
export type IconMachine = Pick<PwCodeMachine, 'kind'> & Pick<PwCodeComponents['schemas']['AgentMachine'], 'csp' | 'imageUrl' | 'os' | 'schedulerType'>

/** An icon's image, and the one for the dark theme where it has its own. */
export interface MachineImage { light: string; dark?: string }

/** Where the Studio passes the platform's icons on (codesessions/pageicon.go). */
export const iconSrc = (path: string) => `/api/code-page/icon?path=${encodeURIComponent(path)}`

const at = (light: string, dark?: string): MachineImage => ({
  light: iconSrc(`/assets/images/icons/${light}`),
  ...(dark ? { dark: iconSrc(`/assets/images/icons/${dark}`) } : {}),
})

const SYSTEM: Record<string, MachineImage> = {
  linux: at('compute/penguin.png'),
  windows: at('os/windows.svg'),
  darwin: at('os/macos.svg', 'os/macos-dark.svg'),
}
const SCHEDULER: Record<string, MachineImage> = {
  slurm: at('compute/slurm.png', 'compute/slurm-dark.png'),
  pbs: at('compute/pbs.svg', 'compute/pbs-dark.svg'),
  lsf: at('compute/ibm-lsf.png'),
}
const CLOUD: Record<string, MachineImage> = {
  aws: at('compute/aws-v2.svg', 'compute/aws-v2-dark.svg'),
  google: at('compute/google.png'),
  azure: at('compute/azure.png'),
  openstack: at('compute/openstack.png'),
  oci: at('compute/oci.svg'),
  kubernetes: at('compute/kubernetes.svg'),
  existing: at('compute/generic.png'),
  generic: at('compute/generic.png'),
}

/** The first of a table a name goes with: a listing says "aws-slurm" or
 *  "google-slurm" for a cloud, "pbspro" for a scheduler. */
function named(table: Record<string, MachineImage>, name: string | undefined): MachineImage | undefined {
  const n = (name ?? '').toLowerCase()
  return n ? table[n] ?? Object.entries(table).find(([key]) => n.startsWith(key))?.[1] : undefined
}

/** A machine's icon, by PW Code on the web's rule: a computer or workspace
 *  has its operating system's; a resource the image it was given, else its
 *  scheduler's or its cloud's. Undefined is the Studio's own glyph. */
export function machineImage(machine: IconMachine): MachineImage | undefined {
  if (isComputer(machine as PwCodeMachine) || machine.kind === 'workspace') return named(SYSTEM, machine.os)
  if (machine.imageUrl) return { light: machine.imageUrl.startsWith('/') ? iconSrc(machine.imageUrl) : machine.imageUrl }
  if (machine.kind === 'existing') return named(SCHEDULER, machine.schedulerType) ?? named(SYSTEM, machine.os) ?? named(CLOUD, machine.csp)
  return named(CLOUD, machine.csp)
}
