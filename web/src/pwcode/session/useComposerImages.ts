import {
  fitImageWith,
  IMAGE_TYPES,
  type PwCodeImageType,
  type PwCodeSavedImage,
  useComposerImages as usePwCodeComposerImages,
} from '@parallelworks/code'
import type { ComposerFiles } from '@parallelworks/ui/ai'
import { useEffect, useMemo, useRef } from 'react'
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
const accept = IMAGE_TYPES.join(',')

/** Images waiting in composers now closed, by what each composer is for. */
export type KeptImages = Map<string, Promise<PwCodeSavedImage[]>>

/** Images for the next message, saved with the session (POST .../images) as
 *  they arrive, or kept here until there is a session to save them with
 *  (`saveTo`). `files` is the composer's: what its + picks, is dropped on the
 *  conversation or is pasted into it, held to the image types pw code takes. */
export function useComposerImages({
  api = pwCode,
  machine,
  sessionId,
  messages,
  numbering,
  keep,
  onError,
}: {
  api?: Pick<typeof pwCode, 'saveImage'>
  machine?: string
  sessionId?: string
  /** The live transcript: the fetched history misses what the terminal sent since. */
  messages: readonly { content?: string | null | undefined }[] | undefined
  /** The counter every composer on one session numbers its images from. */
  numbering?: { current: number }
  /** Where the images waiting here go while this composer is closed, and come
   *  back from when one with the same key opens, as its typed draft does. */
  keep?: { in: KeptImages; as: string }
  onError: (text: string) => void
}) {
  const images = usePwCodeComposerImages<File>({
    api,
    machine,
    sessionId,
    messages,
    numbering,
    onError,
    describeError,
    encode,
    preview,
    release,
  })
  useKeptImages(images, keep)
  const { add } = images
  const files = useMemo<ComposerFiles>(() => ({ accept, onAdd: add }), [add])

  return { ...images, files }
}

/** Puts the images kept under `keep.as` back in the composer as it opens, and
 *  keeps those waiting in it there as it closes. They are saved with the
 *  session already, so they come back without being saved again. */
function useKeptImages(
  images: Pick<ReturnType<typeof usePwCodeComposerImages>, 'take' | 'restore'>,
  keep: { in: KeptImages; as: string } | undefined,
) {
  const latest = useRef(images)
  latest.current = images
  const store = keep?.in
  const key = keep?.as
  useEffect(() => {
    if (!store || key === undefined) return
    let unrestored = store.get(key)
    store.delete(key)
    void unrestored?.then(saved => {
      if (!unrestored) return
      unrestored = undefined
      latest.current.restore(saved)
    })
    return () => {
      // Closed again before they came back, they are kept with the rest.
      const earlier = unrestored
      unrestored = undefined
      const waiting = latest.current.take().then(({ saved, settle }) => {
        settle(true)
        return saved
      })
      store.set(key, earlier ? Promise.all([earlier, waiting]).then(sets => sets.flat()) : waiting)
    }
  }, [store, key])
}
