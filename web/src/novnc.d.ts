// The part of noVNC's RFB client (@novnc/novnc, core/rfb.js) the desktop
// viewer uses. The package ships no types.
declare module '@novnc/novnc' {
  export interface RFBOptions {
    shared?: boolean
    wsProtocols?: string[]
    credentials?: { username?: string; password?: string; target?: string }
  }
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string, options?: RFBOptions)
    scaleViewport: boolean
    clipViewport: boolean
    resizeSession: boolean
    showDotCursor: boolean
    focusOnClick: boolean
    viewOnly: boolean
    qualityLevel: number
    compressionLevel: number
    background: string
    disconnect(): void
    sendCtrlAltDel(): void
    clipboardPasteFrom(text: string): void
    focus(options?: FocusOptions): void
    blur(): void
  }
}
