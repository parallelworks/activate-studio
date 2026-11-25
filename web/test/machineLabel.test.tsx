import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AccessText } from '../src/components/LibraryScope'
import { libraryMachine, locationMachine, MachineGlyph, MachineLabel, TransferEnd } from '../src/components/MachineLabel'
import { findMachine, forgetMachines, knowSystems, machineLook, type KnownMachine, type MachineRef } from '../src/machines'
import type { PublicLibrary } from '../src/api'
import { useNoticesRead } from '../src/notices'

const CLOUD: KnownMachine = { id: 'm1', name: 'examplecluster', owner: 'alex', kind: 'cloud', csp: 'google', os: 'linux', imageUrl: '/api/blobs/0123456789', hostname: 'alex-examplecluster-00037-mgmt', daemonId: 'd1' }
const ONSITE: KnownMachine = { id: 'm2', name: 'labcluster', owner: 'alex', kind: 'existing', os: 'linux', hostname: 'login1.lab.example', daemonId: 'd2' }
const known = [CLOUD, ONSITE]
const named = (ref: MachineRef, of: KnownMachine[] = known) => findMachine(ref, of)?.name

describe('which machine a name is of', () => {
  it('is the one with its id, its daemon, its name, its address or its host name', () => {
    expect(named({ id: 'm2', name: 'anything' })).toBe('labcluster')
    expect(named({ id: 'studio', name: 'laptop', daemonId: 'd1' })).toBe('examplecluster')
    expect(named({ name: 'examplecluster' })).toBe('examplecluster')
    expect(named({ name: 'ExampleCluster' })).toBe('examplecluster')
    expect(named({ name: 'pw://alex/labcluster' })).toBe('labcluster')
    expect(named({ name: 'alex-examplecluster-00037-mgmt' })).toBe('examplecluster')
    expect(named({ name: 'login1' })).toBe('labcluster')
    expect(named({ name: 'login1.lab.example' })).toBe('labcluster')
  })
  it('is a cloud cluster\'s by the names of its nodes, of any start of it', () => {
    expect(named({ name: 'alex-examplecluster-00041-mgmt' })).toBe('examplecluster')
    expect(named({ name: 'alex-examplecluster-old' })).toBeUndefined()
    expect(named({ name: 'robin-examplecluster-00041-mgmt' })).toBeUndefined()
  })
  it('is none for a name it cannot match, or that several have', () => {
    expect(named({ name: 'gpu-07' })).toBeUndefined()
    expect(named({})).toBeUndefined()
    const shared: KnownMachine = { id: 'm3', name: 'examplecluster', owner: 'robin', kind: 'cloud' }
    expect(named({ name: 'examplecluster' }, [...known, shared])).toBeUndefined()
    expect(named({ name: 'pw://robin/examplecluster' }, [...known, shared])).toBe('examplecluster')
    expect(findMachine({ name: 'pw://robin/examplecluster' }, [...known, shared])?.id).toBe('m3')
    const twin: KnownMachine = { id: 'm4', name: 'other', hostname: 'login1.lab.example', owner: 'alex', kind: 'existing' }
    expect(named({ name: 'login1' }, [...known, twin])).toBeUndefined()
  })
  it('is none of another\'s for an address, and none for a host over plain ssh', () => {
    expect(named({ name: 'pw://robin/examplecluster' })).toBeUndefined()
    expect(named({ name: 'examplecluster', kind: 'ssh' })).toBeUndefined()
    expect(named({ name: 'alex@examplecluster' })).toBeUndefined()
  })
  it('is not by its host name where another daemon is there', () => {
    expect(named({ name: 'login1', daemonId: 'd9' })).toBeUndefined()
  })
  it('puts what both listings say of one together', () => {
    const fromClusters: KnownMachine = { name: 'labcluster', owner: 'alex', kind: 'existing', schedulerType: 'slurm' }
    expect(findMachine({ name: 'labcluster' }, [ONSITE, fromClusters])).toMatchObject({ id: 'm2', hostname: 'login1.lab.example', schedulerType: 'slurm' })
  })
})

describe('how it is shown', () => {
  it('is its resource\'s name, with its host name in the tooltip', () => {
    expect(machineLook({ name: 'alex-examplecluster-00041-mgmt' }, known)).toMatchObject({ name: 'examplecluster', title: 'examplecluster (alex-examplecluster-00037-mgmt)', known: true })
  })
  it('is the name given where it cannot be matched, an address by its last part', () => {
    expect(machineLook({ name: 'gpu-07' }, known)).toEqual({ name: 'gpu-07', title: 'gpu-07', known: false, icon: { kind: 'existing' } })
    expect(machineLook({ name: 'pw://robin/othercluster' }, known)).toMatchObject({ name: 'othercluster', title: 'pw://robin/othercluster', known: false })
    expect(machineLook({ name: 'laptop', kind: 'local', os: 'linux' }, known)).toMatchObject({ name: 'laptop', icon: { kind: 'local', os: 'linux' } })
  })
})

describe('the label', () => {
  let systems: unknown
  let listing: { listed: boolean; machines: KnownMachine[] }
  let asked: string[]
  beforeEach(() => {
    systems = { usable: true, why: null, error: null, systems: [] }
    listing = { listed: true, machines: known }
    asked = []
    forgetMachines()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input instanceof Request ? input.url : input)
      asked.push(url)
      const out = url.startsWith('/api/notices') ? { items: [], chats: {}, sessions: [], ...listing } : url.includes('/pw/systems') ? systems : {}
      return new Response(JSON.stringify(out), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); forgetMachines() })
  function Page({ machine }: { machine: MachineRef }) {
    useNoticesRead(true)
    return <MachineLabel machine={machine}>:data</MachineLabel>
  }
  const label = () => screen.getByTestId('machine-label')
  const images = () => [...label().querySelectorAll('img')].map(i => decodeURIComponent(i.getAttribute('src') ?? ''))

  it('has the uploaded icon, the resource\'s name and the host name in its tooltip', async () => {
    render(<Page machine={{ name: 'alex-examplecluster-00037-mgmt' }} />)
    await waitFor(() => expect(label()).toHaveTextContent('examplecluster:data'))
    expect(label()).toHaveAttribute('title', 'examplecluster (alex-examplecluster-00037-mgmt)')
    expect(images()).toEqual(['/api/code-page/icon?path=/api/blobs/0123456789'])
    expect(asked.filter(u => u.includes('/pw/systems'))).toEqual([])
  })
  it('has its operating system\'s icon where none was uploaded', async () => {
    render(<Page machine={{ name: 'labcluster' }} />)
    await waitFor(() => expect(images()[0]).toContain('/compute/penguin.png'))
  })
  it('keeps the host name, with a glyph and no image, where it matches nothing', async () => {
    render(<Page machine={{ name: 'gpu-07' }} />)
    await waitFor(() => expect(asked.some(u => u.includes('/pw/systems'))).toBe(true))
    expect(label()).toHaveTextContent('gpu-07:data')
    expect(label().querySelector('img')).toBeNull()
    expect(label().querySelector('svg')).not.toBeNull()
  })
  it('falls back to the glyph for an image that cannot be shown', async () => {
    const { container } = render(<Page machine={{ name: 'examplecluster' }} />)
    await waitFor(() => expect(images()).toHaveLength(1))
    fireEvent.error(container.querySelector('img')!)
    expect(label().querySelector('img')).toBeNull()
    expect(label().querySelector('svg')).not.toBeNull()
  })
  it('reads the viewer\'s clusters once for what the listing has not, and forgets them with the viewer', async () => {
    listing = { listed: true, machines: [] }
    systems = { usable: true, why: null, error: null, systems: [{ name: 'offcluster', label: 'offcluster', status: 'off', running: false, type: 'aws', owner: 'alex' }] }
    render(<><Page machine={{ name: 'offcluster' }} /><MachineGlyph machine={{ name: 'elsewhere' }} /></>)
    await waitFor(() => expect(images()[0]).toContain('aws'))
    expect(asked.filter(u => u.includes('/pw/systems'))).toHaveLength(1)
    systems = { usable: true, why: null, error: null, systems: [] }
    act(() => { window.dispatchEvent(new Event('ade:viewer-caches-cleared')) })
    expect(images()).toEqual([])
  })
  it('shows what a form has read of the clusters', () => {
    render(<MachineLabel machine={{ name: 'offcluster' }} />)
    expect(label().querySelector('img')).toBeNull()
    act(() => knowSystems({ usable: true, why: null, error: null, systems: [{ name: 'offcluster', label: 'offcluster', status: 'active', running: true, type: 'google-slurm' }] } as never))
    expect(images()[0]).toContain('google')
  })
  it('stands in a library\'s access and before a transfer\'s end', () => {
    const { container } = render(<><p><AccessText text="Read/write (on labcluster)" machine={{ name: 'labcluster' }} /></p><em><TransferEnd location="cluster:labcluster">labcluster: /data</TransferEnd></em></>)
    expect(container.querySelector('p')).toHaveTextContent('Read/write (on labcluster)')
    expect(container.querySelectorAll('p .machine-label')).toHaveLength(1)
    expect(container.querySelector('em')).toHaveTextContent('labcluster: /data')
    expect(container.querySelectorAll('em .machine-glyph')).toHaveLength(1)
  })
})

describe('the machine of a library and of a transfer\'s location', () => {
  const lib = (o: Partial<PublicLibrary>) => o as PublicLibrary
  it('is its cluster, its ssh host as it is, or none', () => {
    expect(libraryMachine(lib({ site: 'pw://alex/labcluster', access: { where: 'system', system: 'labcluster', via: 'pw' } as never }))).toEqual({ name: 'pw://alex/labcluster' })
    expect(libraryMachine(lib({ site: 'ssh://box', access: { where: 'system', system: 'me@box', via: 'ssh' } as never }))).toEqual({ name: 'me@box', kind: 'ssh' })
    expect(libraryMachine(lib({ host: 'labcluster' }))).toEqual({ name: 'labcluster' })
    expect(libraryMachine(lib({ access: { where: 'bucket' } as never, site: 'x' }))).toBeNull()
    expect(libraryMachine(lib({}))).toBeNull()
    expect(libraryMachine(undefined)).toBeNull()
  })
  it('is none for a place that is no machine of the platform\'s', () => {
    const libs = [lib({ id: 'far', site: 'pw://alex/labcluster' })]
    expect(locationMachine('cluster:labcluster', libs)).toEqual({ name: 'labcluster' })
    expect(locationMachine('library:far', libs)).toEqual({ name: 'pw://alex/labcluster' })
    expect(locationMachine('library:other', libs)).toBeNull()
    expect(locationMachine('rclone:remote', libs)).toBeNull()
    expect(locationMachine('saved:1', libs)).toMatchObject({ kind: 'ssh' })
  })
})
