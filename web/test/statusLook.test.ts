import { describe, expect, it } from 'vitest'
import { statusLook } from '../src/statusLook'

describe('statusLook', () => {
  it('gives each state one meaning', () => {
    expect(statusLook('working')).toEqual({ variant: 'info', pulse: true })
    expect(statusLook('running')).toEqual({ variant: 'info', pulse: true })
    expect(statusLook('completed')).toEqual({ variant: 'success' })
    expect(statusLook('failed')).toEqual({ variant: 'error' })
    expect(statusLook('input-required')).toEqual({ variant: 'warning' })
  })

  it('leaves idle and unknown states unlit', () => {
    expect(statusLook('idle')).toEqual({ off: true })
    expect(statusLook('something-new')).toEqual({ off: true })
  })
})
