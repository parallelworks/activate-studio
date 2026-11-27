import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { attachmentDownloadUrl, attachmentPath, ChatImageViewer, openAttachment } from '../src/chatAttachments'

/** The Studio's id for an attachment: the base64url of its path. */
const studioId = (rel: string) =>
  btoa(String.fromCharCode(...new TextEncoder().encode(rel))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

// What the pw code session view gives its sent images as ids: the address
// the session serves the picture at, or its preview as a data: URL.
const sessionImage = '/api/code/machines/studio/sessions/6f1c2a7e-5b9d-4e3a-8c21-000000000002/images/9f86d081884c7d650000000000000001'
const preview = 'data:image/png;base64,iVBORw0KGgo='

afterEach(() => { location.hash = '' })

describe('chat attachment ids', () => {
  it('reads the path of an attachment the Studio keeps', () => {
    expect(attachmentPath(studioId('uploads/chat/Screenshot 2026-11-27 at 10.00.00.png'))).toBe('uploads/chat/Screenshot 2026-11-27 at 10.00.00.png')
    expect(attachmentPath(studioId('uploads/chat/Résumé.png'))).toBe('uploads/chat/Résumé.png')
  })
  it('reads no path from any other id', () => {
    for (const id of [sessionImage, preview, studioId('notes/plan.md'), studioId('uploads/chat/../secret'), studioId('uploads/chat/'), 'not base64!', studioId('uploads/chat/a').slice(0, -3) + 'ÿ']) {
      expect(attachmentPath(id)).toBeNull()
    }
  })
  it('downloads the Studio’s from the Studio and a session’s picture from the session', () => {
    const id = studioId('uploads/chat/image.png')
    expect(attachmentDownloadUrl(id)).toBe(`/api/chat/attachments/${id}/download`)
    expect(attachmentDownloadUrl(sessionImage)).toBe(sessionImage)
    expect(attachmentDownloadUrl(preview)).toBe('')
  })
})

describe('opening an attachment from a chat message', () => {
  it('opens the Studio’s in the Library', () => {
    openAttachment({ id: studioId('uploads/chat/my shot.png') })
    expect(location.hash).toBe('#open=file:uploads/chat/my%20shot.png')
  })
  it('opens a pw code session’s picture in the image viewer, never the Library', async () => {
    render(<ChatImageViewer />)
    act(() => openAttachment({ id: sessionImage, contentType: 'image/png', previewUrl: preview }))
    expect(location.hash).toBe('')
    const dialog = await screen.findByRole('dialog')
    expect(dialog.querySelector('img')?.getAttribute('src')).toMatch(/^(data:image\/png|\/api\/code\/machines\/)/)
  })
  it('opens a picture known only by its preview in the viewer', async () => {
    render(<ChatImageViewer />)
    act(() => openAttachment({ id: preview }))
    expect(location.hash).toBe('')
    expect(await screen.findByRole('dialog')).toBeTruthy()
  })
})

describe('the chat adapter', () => {
  it('sends a session’s picture to the viewer and leaves the address alone', async () => {
    const { createStudioAdapter } = await import('../src/adapter')
    const att = createStudioAdapter().attachments as unknown as { onOpen: (a: object) => void; downloadUrl: (id: string) => string }
    location.hash = '#chat=c1'
    att.onOpen({ id: sessionImage, previewUrl: preview })
    expect(location.hash).toBe('#chat=c1')
    expect(att.downloadUrl(sessionImage)).toBe(sessionImage)
    // The first import of the adapter loads much of the app, slow on a busy full run.
  }, 30_000)
})
