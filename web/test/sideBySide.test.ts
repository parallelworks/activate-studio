import { describe, expect, it } from 'vitest'
import { besideFeatures } from '../src/sideBySide'

describe('a window beside the Studio', () => {
  it('takes the right half of the screen and leaves the left half for the Studio', () => {
    const { features, left } = besideFeatures({ availWidth: 2560, availHeight: 1400, availLeft: 0, availTop: 25 })
    expect(features).toBe('popup,left=1280,top=25,width=1280,height=1400')
    expect(left).toEqual({ x: 0, y: 25, w: 1280, h: 1400 })
  })

  it('works on a second screen and an odd width', () => {
    expect(besideFeatures({ availWidth: 1919, availHeight: 1080, availLeft: 2560, availTop: 0 }).features)
      .toBe('popup,left=3519,top=0,width=960,height=1080')
  })
})
