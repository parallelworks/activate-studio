import {
  fitImageWith,
  type PwCodeImageType,
  useComposerImages as usePwCodeComposerImages,
} from '@parallelworks/code'
import type { ClipboardEvent } from 'react'
import { pwCode } from '../api'
import { describeError } from '../describeError'

export {
  IMAGE_READ_MAX_BYTES as MAX_PASTE_BYTES,
  nextImageSeq,
  type PwCodeComposerImage as ComposerImage,
  type PwCodeSavedImage as SavedImage,
  type PwCodeTakenImages as TakenImages,
} from '@parallelworks/code'

/** Scales an image down until it fits the terminal's paste limits, as clipimage.Fit does. */
export async function fitImage(file: Blob, mime: PwCodeImageType): Promise<{ blob: Blob; mime: PwCodeImageType }> {
  const bitmap = await createImageBitmap(file)
  try {
    const fitted = await fitImageWith(
      { width: bitmap.width, height: bitmap.height, size: file.size, type: mime },
      ({ width, height, type }) => {
        const canvas = new OffscreenCanvas(width, height)
        canvas.getContext('2d')?.drawImage(bitmap, 0, 0, width, height)
        return canvas.convertToBlob({ type })
      },
    )
    return fitted ? { blob: fitted.image, mime: fitted.mime } : { blob: file, mime }
  } finally {
    bitmap.close()
  }
}

function readBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).split(',')[1] ?? '')
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

async function encode(file: File, mime: PwCodeImageType) {
  const fitted = await fitImage(file, mime)
  return { data: await readBase64(fitted.blob), mime: fitted.mime }
}

const preview = (file: File) => URL.createObjectURL(file)
const release = (url: string) => URL.revokeObjectURL(url)

/** Images pasted into the composer, saved with the session (POST .../images)
 *  as they arrive; `onPaste` takes a paste's images from the composer's textarea. */
export function useComposerImages({
  machine,
  sessionId,
  messages,
  onError,
}: {
  machine?: string
  sessionId?: string
  /** The live transcript: the fetched history misses what the terminal sent since. */
  messages: readonly { content?: string | null | undefined }[] | undefined
  onError: (text: string) => void
}) {
  const images = usePwCodeComposerImages<File>({
    api: pwCode,
    machine,
    sessionId,
    messages,
    onError,
    describeError,
    encode,
    preview,
    release,
  })
  const { add } = images

  const onPaste = (event: ClipboardEvent) => {
    if (!(event.target instanceof HTMLTextAreaElement) || event.target.disabled) return
    const files = Array.from(event.clipboardData.files).filter(file => file.type.startsWith('image/'))
    if (files.length > 0) {
      event.preventDefault()
      event.stopPropagation()
      add(files)
    }
  }

  return { ...images, onPaste }
}
