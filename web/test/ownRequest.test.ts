import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/api'
import { postJson } from '../src/components/LibraryFormParts'

const sent: [string, RequestInit | undefined][] = []
const answer = () => {
  sent.length = 0
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    sent.push([url, init])
    return new Response('{}', { headers: { 'Content-Type': 'application/json' } })
  }))
}
afterEach(() => vi.unstubAllGlobals())

// The server knows its own page by this header where the browser does not say
// (plain http behind a proxy that rewrites Host): libapi's otherSite.
describe('the Studio page names its requests', () => {
  const calls: [string, () => Promise<unknown>][] = [
    ['rebuild', () => api.rebuildLibrary('papers')],
    ['add a bucket', () => postJson('/api/libraries/bucket', { location: 'rclone:store' })],
    ['list buckets', () => api.pwBuckets()],
    ['folders of a bucket', () => api.pwBucketFolder('pw://ada/papers', '')],
    ['how often', () => api.setBucketEvery('papers', 60)],
    ['its endpoint', () => api.setBucketEndpoint('papers', '')],
    ['its text', () => api.setBucketText('papers', true)],
    ['the text estimate', () => api.bucketTextEstimate('papers')],
    ['stop the text', () => api.stopBucketText('papers')],
    ['remove', () => api.removeLibrary('papers')],
    ['open a file', () => api.file('a.txt')],
  ]
  it.each(calls)('%s', async (_, call) => {
    answer()
    await call()
    expect(sent).toHaveLength(1)
    const h = new Headers(sent[0][1]?.headers)
    expect(h.get('X-Studio-Request')).toBe('1')
    if (typeof sent[0][1]?.body === 'string') expect(h.get('Content-Type')).toBe('application/json')
  })
})
