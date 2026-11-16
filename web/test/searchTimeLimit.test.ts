import { describe, expect, it } from 'vitest'
import { SEARCH_TIME_LIMITS, searchTimeLimits } from '../src/views/SettingsView'

describe('the search time limit choices', () => {
  it('run from 10 seconds to an hour, then no limit, with 20 seconds among them', () => {
    expect(SEARCH_TIME_LIMITS.map(i => i.value)).toEqual([10, 20, 30, 60, 120, 300, 600, 1800, 3600, 0])
    expect(SEARCH_TIME_LIMITS.find(i => i.value === 0)?.label).toBe('No limit')
    expect(searchTimeLimits(20)).toBe(SEARCH_TIME_LIMITS)
  })

  it('keep a limit set some other way as it is', () => {
    expect(searchTimeLimits(45)[0]).toEqual({ value: 45, label: '45 seconds' })
    expect(searchTimeLimits(45)).toHaveLength(SEARCH_TIME_LIMITS.length + 1)
  })
})
