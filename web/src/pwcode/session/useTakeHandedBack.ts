import type { SessionViewHandle } from '@parallelworks/ui/ai'
import { type RefObject, useEffect, useRef } from 'react'

/** Takes a message a turn failed or stopped on before the model read it back
 *  into the session view's composer as it comes back, as the terminal puts
 *  one in its box, and not only on ↑. */
export function useTakeHandedBack(
  view: RefObject<SessionViewHandle | null>,
  composer: { handedBack: number; handedBackShown: boolean; dropShownHandedBack: () => void },
  { shown = true, holdsImages = false }: { shown?: boolean; holdsImages?: boolean } = {},
) {
  const latest = useRef({ composer, holdsImages })
  latest.current = { composer, holdsImages }
  const handedBack = shown ? composer.handedBack : 0
  useEffect(() => {
    const current = view.current
    if (handedBack === 0 || !current) return
    // A failed turn's message is still drawn where it was sent, so it fills
    // only an empty box and is let go otherwise; what else waits comes back
    // as the count drops.
    const { composer: { handedBackShown, dropShownHandedBack }, holdsImages: images } = latest.current
    // The view's ifEmpty cannot see images, which the page holds itself.
    if (handedBackShown && images) {
      dropShownHandedBack()
      return
    }
    void current.recall(handedBackShown ? { ifEmpty: true } : undefined).then(took => {
      if (!took && handedBackShown) dropShownHandedBack()
    })
  }, [view, handedBack])
}
