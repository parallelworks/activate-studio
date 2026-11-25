import { describe, expect, it } from 'vitest'
import { machineImage } from '../src/code/machineIcon'

const path = (src?: string) => src && decodeURIComponent(src).replace('/api/code-page/icon?path=', '')
const icon = (m: Parameters<typeof machineImage>[0]) => path(machineImage(m)?.light)

// PW Code on the web's rule.
describe('a machine\'s icon', () => {
  it('is its operating system\'s for a computer or a workspace, whatever else it has', () => {
    expect(icon({ kind: 'local', os: 'linux', imageUrl: '/api/blobs/0123456789', schedulerType: 'slurm' })).toBe('/assets/images/icons/compute/penguin.png')
    expect(icon({ kind: 'workspace', os: 'linux' })).toBe('/assets/images/icons/compute/penguin.png')
    expect(machineImage({ kind: 'local', os: 'darwin' })?.dark).toContain('macos-dark.svg')
    expect(machineImage({ kind: 'local' })).toBeUndefined()
  })
  it('is the image given to a resource, through the Studio', () => {
    expect(machineImage({ kind: 'existing', imageUrl: '/api/blobs/0123456789', schedulerType: 'slurm' })?.light).toBe('/api/code-page/icon?path=%2Fapi%2Fblobs%2F0123456789')
    expect(machineImage({ kind: 'cloud', imageUrl: 'https://images.example/a.png' })?.light).toBe('https://images.example/a.png')
  })
  it('is else its scheduler\'s or its cloud\'s', () => {
    expect(icon({ kind: 'existing', schedulerType: 'pbs', os: 'linux' })).toBe('/assets/images/icons/compute/pbs.svg')
    expect(icon({ kind: 'existing', os: 'linux' })).toBe('/assets/images/icons/compute/penguin.png')
    expect(icon({ kind: 'cloud', csp: 'google-slurm' })).toBe('/assets/images/icons/compute/google.png')
    expect(machineImage({ kind: 'cloud', csp: 'aws' })?.dark).toContain('aws-v2-dark.svg')
    expect(machineImage({ kind: 'cloud', csp: 'elsewhere' })).toBeUndefined()
  })
})
