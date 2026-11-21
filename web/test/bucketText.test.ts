import { describe, expect, it } from 'vitest'
import { textCost, type BucketTextEstimate } from '../src/api'

const est = (o: Partial<BucketTextEstimate>): BucketTextEstimate => ({
  objects: 2000, bytes: 50e9, done: 0, doneBytes: 0, over: 0, overBytes: 0, other: 0, maxBytes: 100 << 20, provider: 'AWS', rate: 0, ...o,
})

describe('the cost of reading a bucket library\'s text', () => {
  it('prices data out and GET requests on AWS', () => {
    const { money, time } = textCost(est({}))
    expect(money).toContain('about $4.50 in data out')
    expect(money).toContain('under $0.01 in requests')
    expect(time).toContain('At 25 MB a second')
    expect(time).toContain('the first pass measures the real rate')
  })
  it('names no price for a store on premises or another provider, and uses the measured rate', () => {
    const { money, time } = textCost(est({ provider: 'Minio', rate: 10e6, bytes: 1e9, objects: 100 }))
    expect(money).toContain('A store on premises charges nothing')
    expect(money).not.toContain('$')
    expect(time).toBe('At the 10.0 MB a second the last pass read, it takes about 2 minutes.')
  })
  it('says nothing is charged when there is nothing to read', () => {
    expect(textCost(est({ objects: 0, bytes: 0 }))).toEqual({ money: 'Nothing is read, so nothing is charged.', time: '' })
  })
})
