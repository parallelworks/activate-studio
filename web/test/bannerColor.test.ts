import { describe, expect, it } from 'vitest'
import { bannerShownColor } from '../src/components/ClassificationBanner'

describe('the installed app title bar color', () => {
  it('is the banner color when a banner is drawn, and nothing otherwise', () => {
    expect(bannerShownColor({ bannerText: 'CUI', bannerColor: '#24612e' })).toBe('#24612e')
    expect(bannerShownColor({ bannerText: 'CUI', bannerColor: '' })).toBe('#24612e')
    expect(bannerShownColor({ bannerText: '   ', bannerColor: '#24612e' })).toBeNull()
    expect(bannerShownColor({ bannerText: '', bannerColor: '#c8102e' })).toBeNull()
  })
})
