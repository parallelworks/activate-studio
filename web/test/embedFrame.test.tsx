import { act, render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { EmbedFrame } from '../src/components/EmbedFrame'

describe('EmbedFrame', () => {
  it('tells the embed the page theme and follows a toggle', async () => {
    document.documentElement.dataset.theme = 'dark'
    const { container } = render(<EmbedFrame src="/?embed=dag&workflow=x" title="x" />)
    const frame = () => container.querySelector('iframe') as HTMLIFrameElement
    expect(frame().getAttribute('src')).toContain('&theme=dark')
    await act(async () => { document.documentElement.dataset.theme = 'light'; await new Promise(r => setTimeout(r, 0)) })
    expect(frame().getAttribute('src')).toContain('&theme=light')
  })
})
