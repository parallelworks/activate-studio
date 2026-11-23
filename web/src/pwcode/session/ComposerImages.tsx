import { usePwCodeTranslations } from '@parallelworks/code'
import { AttachmentImageViewer } from '@parallelworks/ui/ai'
import { CloseIcon } from '@parallelworks/ui/icons'
import { useRef, useState } from 'react'
import type { ComposerImage } from './useComposerImages'
import './ComposerImages.css'

export interface ComposerImagesProps {
  images: readonly ComposerImage[]
  onRemove: (key: string) => void
  disabled?: boolean
}

/** The images waiting to go with the next message, over the composer: each
 *  opens at full size, and each can be taken out. */
export function ComposerImages({ images, onRemove, disabled = false }: ComposerImagesProps) {
  const t = usePwCodeTranslations('agents')
  // By key, so removing or sending an earlier image never moves the viewer to another.
  const [viewing, setViewing] = useState<string | null>(null)
  const thumbnails = useRef(new Map<string, HTMLButtonElement>())
  const index = images.findIndex(image => image.key === viewing)
  // A failed send puts its images back under the same keys, which mustn't reopen one.
  if (viewing !== null && index < 0) setViewing(null)
  if (images.length === 0) return null
  return (
    <>
      <ul aria-label={t('detail.imagesLabel')} data-testid="agent-composer-images" className="pwcode-images">
        {images.map(image => {
          const remove = t('detail.removeImage', { placeholder: image.placeholder })
          return (
            <li key={image.key} className="pwcode-image" aria-busy={!image.saved}>
              <button
                type="button"
                className="pwcode-image-open"
                ref={element => {
                  if (element) thumbnails.current.set(image.key, element)
                  else thumbnails.current.delete(image.key)
                }}
                onClick={() => setViewing(image.key)}
              >
                <img
                  src={image.previewUrl}
                  alt={image.placeholder}
                  title={image.placeholder}
                  className={image.saved ? 'pwcode-image-thumb' : 'pwcode-image-thumb pwcode-image-saving'}
                />
              </button>
              <button
                type="button"
                className="pwcode-image-remove"
                disabled={disabled}
                onClick={() => onRemove(image.key)}
                aria-label={remove}
                title={remove}
              >
                <CloseIcon aria-hidden />
              </button>
            </li>
          )
        })}
      </ul>
      <AttachmentImageViewer
        images={images.map(image => ({ name: image.placeholder, src: image.previewUrl }))}
        index={index < 0 ? null : index}
        onIndexChange={next => setViewing(images[next]?.key ?? null)}
        onClose={() => setViewing(null)}
        returnFocusTo={next => {
          const key = images[next]?.key
          return key === undefined ? undefined : thumbnails.current.get(key)
        }}
      />
    </>
  )
}
