import { afterEach, describe, expect, it } from 'vitest'
import { GROUND_COOKIE, hexColor, rememberGround } from '../src/theme'

const clear = () => { document.cookie = `${GROUND_COOKIE}=; Path=/; Max-Age=0` }

describe('the splash color cookie', () => {
  afterEach(clear)

  it('reads the forms a computed ground comes in', () => {
    expect(hexColor('#0C1320')).toBe('#0c1320')
    expect(hexColor(' #abc ')).toBe('#aabbcc')
    expect(hexColor('rgb(12, 19, 32)')).toBe('#0c1320')
    expect(hexColor('rgb(12 19 32 / 1)')).toBe('#0c1320')
    expect(hexColor('rgba(12, 19, 32, 0.5)')).toBeNull()
    expect(hexColor('')).toBeNull()
  })

  it('keeps the ground as six hex digits the manifest reads', () => {
    rememberGround('#F3F4F6')
    expect(document.cookie).toContain(`${GROUND_COOKIE}=f3f4f6`)
    rememberGround('rgb(12, 19, 32)')
    expect(document.cookie).toContain(`${GROUND_COOKIE}=0c1320`)
    expect(document.cookie).not.toContain('f3f4f6')
  })

  it('leaves the cookie alone for a color it cannot read', () => {
    rememberGround('#0c1320')
    rememberGround('rgba(0, 0, 0, 0)')
    expect(document.cookie).toContain(`${GROUND_COOKIE}=0c1320`)
  })
})
